package adapters

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sportsbook-normalizer/internal/identity"
	"sportsbook-normalizer/internal/model"
	"sportsbook-normalizer/internal/taxonomy"
)

// pmEvent is the subset of a Gamma event needed by the normalizer. The
// normalizer deliberately owns this shape instead of importing the feed module:
// the two modules remain independently buildable.
type pmEvent struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	StartDate string     `json:"startDate"`
	EndDate   string     `json:"endDate"`
	StartTime string     `json:"startTime"`
	EventDate string     `json:"eventDate"`
	Live      bool       `json:"live"`
	Teams     []pmTeam   `json:"teams"`
	Markets   []pmMarket `json:"markets"`
}

type pmTeam struct {
	Name     string `json:"name"`
	Alias    string `json:"alias"`
	Ordering string `json:"ordering"`
}

type pmMarket struct {
	ID               string      `json:"id"`
	Question         string      `json:"question"`
	GroupItemTitle   string      `json:"groupItemTitle"`
	Line             *float64    `json:"line"`
	Outcomes         string      `json:"outcomes"`
	OutcomePrices    string      `json:"outcomePrices"`
	ClobTokenIDs     string      `json:"clobTokenIds"`
	SportsMarketType string      `json:"sportsMarketType"`
	BestBid          *float64    `json:"bestBid"`
	BestAsk          *float64    `json:"bestAsk"`
	LastTradePrice   *float64    `json:"lastTradePrice"`
	GameStartTime    string      `json:"gameStartTime"`
	Closed           bool        `json:"closed"`
	Archived         bool        `json:"archived"`
	Volume           pmFlexFloat `json:"volume"`
	Liquidity        pmFlexFloat `json:"liquidity"`
}

type pmFlexFloat float64

func (f *pmFlexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = pmFlexFloat(v)
	return nil
}

func (e pmEvent) start() time.Time {
	for _, s := range []string{e.EndDate, e.StartTime, e.EventDate} {
		if t := pmParseTime(s); !t.IsZero() {
			return t
		}
	}
	for _, m := range e.Markets {
		if t := pmParseTime(m.GameStartTime); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func (e pmEvent) awayHome() (away, home string) {
	for _, t := range e.Teams {
		switch strings.ToLower(t.Ordering) {
		case "away":
			away = t.Name
		case "home":
			home = t.Name
		}
	}
	if away == "" || home == "" {
		title := e.Title
		for _, suffix := range []string{" - Player Props", " - First Touchdown Scorer", " - Exact Margin", " - Highest Scoring Quarter"} {
			title = strings.TrimSuffix(title, suffix)
		}
		parts := strings.Split(title, " vs. ")
		if len(parts) == 2 {
			if away == "" {
				away = strings.TrimSpace(parts[0])
			}
			if home == "" {
				home = strings.TrimSpace(parts[1])
			}
		}
	}
	return away, home
}

type pmSide struct {
	Name  string
	Price float64
	Token string
}

func (m pmMarket) sides() ([]pmSide, error) {
	names, err := pmStringArray(m.Outcomes)
	if err != nil {
		return nil, err
	}
	prices, err := pmFloatArray(m.OutcomePrices)
	if err != nil {
		return nil, err
	}
	tokens, err := pmStringArray(m.ClobTokenIDs)
	if err != nil {
		return nil, err
	}
	out := make([]pmSide, 0, len(names))
	for i, name := range names {
		s := pmSide{Name: name}
		if i < len(prices) {
			s.Price = prices[i]
		}
		if i < len(tokens) {
			s.Token = tokens[i]
		}
		out = append(out, s)
	}
	return out, nil
}

func pmStringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var out []string
	return out, json.Unmarshal([]byte(raw), &out)
}

func pmFloatArray(raw string) ([]float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var values []any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(values))
	for _, v := range values {
		switch x := v.(type) {
		case float64:
			out = append(out, x)
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
	}
	return out, nil
}

func pmParseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05-07", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// PM parses a Polymarket Gamma event file (array or {"events": [...]}) into
// canonical entities. Polymarket is binary: each spread/total line is a
// separate market and each market has two outcome tokens.
func PM(path, sport string) (*model.BookData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	events, err := decodePMEvents(raw)
	if err != nil {
		return nil, err
	}
	book := &model.BookData{Book: "polymarket", Sport: sport, League: sport}
	eventByKey := map[string]model.Event{}

	for _, e := range events {
		awayName, homeName := e.awayHome()
		away, okA := identity.ResolveTeam(sport, awayName)
		home, okH := identity.ResolveTeam(sport, homeName)
		start := e.start()
		if !okA || !okH || start.IsZero() {
			continue
		}
		ev := model.Event{
			Book: "polymarket", BookEventID: e.ID, Sport: sport, League: sport,
			Name: e.Title, Start: start, Home: home, Away: away,
		}
		ev.Key = model.ScopeEvent(sport, start, away.ID, home.ID)
		if existing, ok := eventByKey[ev.Key]; ok {
			// Gamma splits game lines, props and exact-margin markets into
			// separate events with the same teams/start. Normalize them under
			// one event identity.
			ev = existing
		} else {
			eventByKey[ev.Key] = ev
			book.Events = append(book.Events, ev)
		}

		// The most liquid line per family is treated as the main line; all
		// other full-game spread/total markets are alternates.
		mainLine := pmMainLines(e.Markets)

		for _, m := range e.Markets {
			class := classifyPM(sport, ev, m, mainLine)
			class.BookMarketID = m.ID
			book.Markets = append(book.Markets, class)
			if !class.Comparable {
				continue
			}
			sides, err := m.sides()
			if err != nil || len(sides) == 0 {
				continue
			}
			for i, side := range sides {
				q, ok := pmQuote(sport, ev, m, class, side, i)
				if !ok {
					continue
				}
				book.Quotes = append(book.Quotes, q)
			}
		}
	}
	book.Recompute()
	return book, nil
}

func decodePMEvents(raw []byte) ([]pmEvent, error) {
	var events []pmEvent
	if err := json.Unmarshal(raw, &events); err == nil {
		return events, nil
	}
	var wrapper struct {
		Events []pmEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Events, nil
}

func pmMainLines(markets []pmMarket) map[string]float64 {
	best := map[string]float64{}
	bestVol := map[string]float64{}
	for _, m := range markets {
		if m.SportsMarketType != "spreads" && m.SportsMarketType != "totals" {
			continue
		}
		line, ok := pmLine(m)
		if !ok {
			continue
		}
		key := m.SportsMarketType
		vol := float64(m.Volume)
		if vol == 0 {
			vol = float64(m.Liquidity)
		}
		if _, seen := best[key]; !seen || vol > bestVol[key] {
			best[key] = line
			bestVol[key] = vol
		}
	}
	return best
}

func classifyPM(sport string, ev model.Event, m pmMarket, mainLine map[string]float64) model.MarketClass {
	period := pmPeriod(m.SportsMarketType)
	class := model.MarketClass{
		Book: "polymarket", Sport: sport, ScopeKey: ev.Key, MarketName: m.Question,
		MarketType: m.SportsMarketType, Period: period, Rule: "unknown",
		Family: model.FamilyUnknown, Comparator: model.ComparatorNone,
		SubjectKind: "event",
	}
	line, hasLine := pmLine(m)

	switch m.SportsMarketType {
	case "moneyline":
		class.Family, class.Comparator, class.Rule, class.Confidence, class.Comparable = model.FamilyMoneyline, model.ComparatorNone, "pm.main.moneyline", 1, true
	case "spreads", "first_half_spreads", "second_half_spreads", "q1_spreads", "q2_spreads", "q3_spreads", "q4_spreads":
		class.Family, class.Comparator, class.Rule, class.Confidence, class.Comparable = model.FamilySpread, model.ComparatorNone, "pm.spread", 0.95, true
		if m.SportsMarketType == "spreads" && hasLine {
			main, ok := mainLine["spreads"]
			class.Alt = !ok || line != main
		} else {
			class.Alt = true
		}
	case "totals", "first_half_totals", "second_half_totals", "q1_totals", "q2_totals", "q3_totals", "q4_totals":
		class.Family, class.Stat, class.Comparator, class.Rule, class.Confidence, class.Comparable = model.FamilyTotal, taxonomy.TotalStat(sport), model.ComparatorOU, "pm.total", 0.95, true
		if m.SportsMarketType == "totals" && hasLine {
			main, ok := mainLine["totals"]
			class.Alt = !ok || line != main
		} else {
			class.Alt = true
		}
	case "team_totals", "first_half_team_totals", "second_half_team_totals":
		class.Family, class.Stat, class.Comparator, class.Rule, class.Confidence, class.Comparable = model.FamilyTeamTotal, taxonomy.TotalStat(sport), model.ComparatorOU, "pm.team_total", 0.9, true
		class.Alt = true
	default:
		if stat, ok := pmPropStat(m.SportsMarketType); ok {
			class.Family, class.Stat, class.Rule, class.Confidence, class.Comparable = model.FamilyPlayerProp, stat, "pm.prop", 0.85, true
			class.SubjectKind = "player"
			class.Alt = true
			if m.SportsMarketType == "anytime_touchdowns" || m.SportsMarketType == "two_plus_touchdowns" || m.SportsMarketType == "first_touchdowns" {
				class.Comparator = model.ComparatorYesNo
			} else {
				class.Comparator = model.ComparatorOU
			}
		}
	}
	return class
}

func pmPropStat(t string) (string, bool) {
	switch t {
	case "points":
		return "points", true
	case "rebounds":
		return "rebounds", true
	case "assists":
		return "assists", true
	case "threes":
		return "threes", true
	case "steals":
		return "steals", true
	case "blocks":
		return "blocks", true
	case "turnovers":
		return "turnovers", true
	case "passing_yards":
		return "passing_yards", true
	case "passing_touchdowns":
		return "passing_touchdowns", true
	case "passing_attempts":
		return "passing_attempts", true
	case "passing_completions":
		return "passing_completions", true
	case "rushing_yards":
		return "rushing_yards", true
	case "rushing_touchdowns":
		return "rushing_touchdowns", true
	case "receiving_yards":
		return "receiving_yards", true
	case "receptions":
		return "receptions", true
	case "anytime_touchdowns":
		return "anytime_touchdown", true
	case "two_plus_touchdowns":
		return "two_plus_touchdown", true
	case "first_touchdowns":
		return "first_touchdown", true
	case "longest_reception":
		return "longest_reception", true
	}
	return "", false
}

func pmPeriod(t string) string {
	switch {
	case strings.HasPrefix(t, "first_half_"):
		return model.PeriodH1
	case strings.HasPrefix(t, "second_half_"):
		return model.PeriodH2
	case strings.HasPrefix(t, "q1_"):
		return model.PeriodQ1
	case strings.HasPrefix(t, "q2_"):
		return model.PeriodQ2
	case strings.HasPrefix(t, "q3_"):
		return model.PeriodQ3
	case strings.HasPrefix(t, "q4_"):
		return model.PeriodQ4
	default:
		return model.PeriodFull
	}
}

var pmNumber = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)

func pmLine(m pmMarket) (float64, bool) {
	if m.Line != nil {
		return *m.Line, true
	}
	for _, s := range []string{m.GroupItemTitle, m.Question} {
		matches := pmNumber.FindAllString(s, -1)
		if len(matches) > 0 {
			if v, err := strconv.ParseFloat(strings.ReplaceAll(matches[len(matches)-1], "+", ""), 64); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

func pmQuote(sport string, ev model.Event, m pmMarket, class model.MarketClass, side pmSide, index int) (model.Quote, bool) {
	price, ok := pmMid(m, index)
	if !ok {
		return model.Quote{}, false
	}
	q := model.Quote{
		Book: "polymarket", BookMarketID: m.ID, BookSelectID: side.Token,
		Sport: sport, League: sport, ScopeKey: ev.Key, Family: class.Family,
		Period: class.Period, Stat: class.Stat, Comparator: class.Comparator,
		MarketName: m.Question, MarketType: m.SportsMarketType, Selection: side.Name,
		Confidence: class.Confidence, Rule: class.Rule, Comparable: class.Comparable,
		Alt: class.Alt,
	}
	out := strings.ToLower(strings.TrimSpace(side.Name))
	switch class.Family {
	case model.FamilyMoneyline:
		team, ok := identity.ResolveTeam(sport, side.Name)
		if !ok {
			return model.Quote{}, false
		}
		q.Subject, q.Outcome = "event", team.ID
	case model.FamilySpread:
		team, ok := identity.ResolveTeam(sport, side.Name)
		if !ok {
			return model.Quote{}, false
		}
		line, has := pmLine(m)
		if !has {
			return model.Quote{}, false
		}
		if index == 1 {
			line = -line
		}
		q.Subject, q.Outcome, q.Line = "event", team.ID, &line
		q.LineUnit = taxonomy.TotalStat(sport)
	case model.FamilyTotal:
		line, has := pmLine(m)
		if !has {
			return model.Quote{}, false
		}
		q.Subject, q.Line, q.LineUnit = "event", &line, class.Stat
		switch out {
		case "over", "under":
			q.Outcome = out
		default:
			return model.Quote{}, false
		}
	case model.FamilyTeamTotal:
		line, has := pmLine(m)
		if !has {
			return model.Quote{}, false
		}
		teamName := pmTeamFromQuestion(m.Question)
		team, ok := identity.ResolveTeam(sport, teamName)
		if !ok {
			return model.Quote{}, false
		}
		q.Subject, q.Line, q.LineUnit = team.ID, &line, class.Stat
		switch out {
		case "over", "under":
			q.Outcome = out
		default:
			return model.Quote{}, false
		}
	case model.FamilyPlayerProp:
		player := pmPlayerFromQuestion(m.Question)
		if player == "" {
			return model.Quote{}, false
		}
		q.Subject = model.PlayerSubject(player)
		if class.Comparator == model.ComparatorYesNo {
			switch out {
			case "yes", "no":
				q.Outcome = out
			default:
				return model.Quote{}, false
			}
		} else {
			line, has := pmLine(m)
			if !has {
				return model.Quote{}, false
			}
			q.Line = &line
			switch out {
			case "over":
				q.Outcome = "over"
			case "under":
				q.Outcome = "under"
			case "yes":
				q.Outcome = "over"
			case "no":
				q.Outcome = "under"
			default:
				return model.Quote{}, false
			}
		}
	default:
		return model.Quote{}, false
	}

	q.Price.Decimal = &price
	if american, ok := pmAmerican(price); ok {
		q.Price.American = &american
	}
	q.Price.Status = "open"
	if m.Closed || m.Archived {
		q.Price.Status = "closed"
	}
	q.Price.InPlay = ev.Start.Before(time.Now().UTC())
	q.Key = model.QuoteKey(q)
	return q, true
}

func pmMid(m pmMarket, index int) (float64, bool) {
	// Gamma's bestBid/bestAsk are for the first outcome token only. The second
	// side is the binary complement, not the same quote.
	if m.BestBid != nil && m.BestAsk != nil && *m.BestBid > 0 && *m.BestAsk > 0 && *m.BestAsk <= 1 {
		mid := (*m.BestBid + *m.BestAsk) / 2
		if index == 0 {
			return mid, true
		}
		if index == 1 {
			return 1 - mid, true
		}
	}
	sides, err := m.sides()
	if err != nil || index < 0 || index >= len(sides) || sides[index].Price <= 0 {
		return 0, false
	}
	return sides[index].Price, true
}

func pmAmerican(p float64) (int, bool) {
	if p <= 0 || p >= 1 {
		return 0, false
	}
	if p >= 0.5 {
		v := int(-100.0 / ((1.0 / p) - 1.0))
		if v > -100 {
			v = -100
		}
		return v, true
	}
	v := int((1.0/p - 1.0) * 100.0)
	if v < 100 {
		v = 100
	}
	return v, true
}

func pmPlayerFromQuestion(question string) string {
	q := strings.TrimSpace(question)
	if i := strings.Index(q, ":"); i > 0 {
		return strings.TrimSpace(q[:i])
	}
	return ""
}

func pmTeamFromQuestion(question string) string {
	q := strings.TrimSpace(question)
	if i := strings.Index(q, " Team Total"); i > 0 {
		return strings.TrimSpace(q[:i])
	}
	if i := strings.Index(q, " Alternate Total"); i > 0 {
		return strings.TrimSpace(q[:i])
	}
	return ""
}
