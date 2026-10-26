package adapters

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"

	"sportsbook-normalizer/internal/identity"
	"sportsbook-normalizer/internal/model"
	"sportsbook-normalizer/internal/taxonomy"
)

var fdSignedLineRE = regexp.MustCompile(`[+-]\d+(?:\.\d+)?`)

func fdSignedLine(name string) *float64 {
	matches := fdSignedLineRE.FindAllString(name, -1)
	if len(matches) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(matches[len(matches)-1], 64)
	if err != nil {
		return nil
	}
	return &v
}

func isAlternateFDType(t string) bool {
	return strings.Contains(strings.ToUpper(t), "ALTERNATE")
}

type fdEvent struct {
	EventID       flexID `json:"eventId"`
	Name          string `json:"name"`
	OpenDate      string `json:"openDate"`
	EventTypeID   flexID `json:"eventTypeId"`
	CompetitionID flexID `json:"competitionId"`
}

type fdResult struct {
	Type string `json:"type"`
}

type fdRunner struct {
	SelectionID   flexID   `json:"selectionId"`
	RunnerName    string   `json:"runnerName"`
	Handicap      *float64 `json:"handicap"`
	RunnerStatus  string   `json:"runnerStatus"`
	Result        fdResult `json:"result"`
	WinRunnerOdds struct {
		AmericanDisplayOdds struct {
			AmericanOdds    *float64 `json:"americanOdds"`
			AmericanOddsInt *int     `json:"americanOddsInt"`
		} `json:"americanDisplayOdds"`
		TrueOdds struct {
			DecimalOdds struct {
				DecimalOdds *float64 `json:"decimalOdds"`
			} `json:"decimalOdds"`
		} `json:"trueOdds"`
	} `json:"winRunnerOdds"`
}

type fdMarket struct {
	MarketID        flexID     `json:"marketId"`
	EventID         flexID     `json:"eventId"`
	MarketName      string     `json:"marketName"`
	MarketType      string     `json:"marketType"`
	MarketStatus    string     `json:"marketStatus"`
	InPlay          bool       `json:"inPlay"`
	Runners         []fdRunner `json:"runners"`
	NumberOfRunners int        `json:"numberOfRunners"`
}

type fdBoard struct {
	Attachments struct {
		Events  map[string]fdEvent  `json:"events"`
		Markets map[string]fdMarket `json:"markets"`
	} `json:"attachments"`
}

// FD parses a FanDuel content-managed-page/in-play board into canonical entities.
//
// FanDuel puts a lot of semantics in names: main markets are typed, but player,
// alt-line and futures subjects/lines must be parsed from marketName and
// runnerName. Unknown types produce a MarketClass but no comparable quote.
func FD(path, sport string) (*model.BookData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var board fdBoard
	if err := json.Unmarshal(raw, &board); err != nil {
		return nil, err
	}
	book := &model.BookData{Book: "fanduel", Sport: sport, League: sport}

	// First pass: find game events from their main markets.
	gameEvents := map[string]model.Event{}
	for key, ev := range board.Attachments.Events {
		var home, away string
		for _, m := range board.Attachments.Markets {
			if m.EventID.String() != ev.EventID.String() && m.EventID.String() != key {
				continue
			}
			if !taxonomy.IsMain(m.MarketType) {
				continue
			}
			for _, r := range m.Runners {
				switch strings.ToUpper(r.Result.Type) {
				case "HOME":
					home = r.RunnerName
				case "AWAY":
					away = r.RunnerName
				}
			}
			if home != "" && away != "" {
				break
			}
		}
		ht, okH := identity.ResolveTeam(sport, home)
		at, okA := identity.ResolveTeam(sport, away)
		if !okH || !okA {
			continue
		}
		id := ev.EventID.String()
		if id == "" {
			id = key
		}
		e := model.Event{
			Book: "fanduel", BookEventID: id, Sport: sport, League: sport,
			Name: ev.Name, Start: parseTime(ev.OpenDate), Home: ht, Away: at,
		}
		e.Key = model.ScopeEvent(sport, e.Start, at.ID, ht.ID)
		gameEvents[id] = e
		if key != id {
			gameEvents[key] = e
		}
		book.Events = append(book.Events, e)
	}

	for _, m := range board.Attachments.Markets {
		scope := ""
		if e, ok := gameEvents[m.EventID.String()]; ok {
			scope = e.Key
		} else {
			scope = model.ScopeCompetition(sport, futuresEventName(board, m.EventID.String()))
		}
		class := taxonomy.ClassifyFD(sport, m.MarketType, m.MarketName)
		mc := model.MarketClass{
			Book: "fanduel", BookMarketID: m.MarketID.String(), Sport: sport,
			ScopeKey: scope, Family: class.Family, Period: class.Period, Stat: class.Stat,
			Comparator: class.Comparator, SubjectKind: class.SubjectKind,
			MarketName: m.MarketName, MarketType: m.MarketType, Rule: class.Rule,
			Confidence: class.Confidence, Comparable: class.Comparable, Alt: class.Alt,
			Selections: len(m.Runners),
		}
		book.Markets = append(book.Markets, mc)

		ev, hasEvent := gameEvents[m.EventID.String()]
		for _, r := range m.Runners {
			q, ok := fdQuote(book, ev, hasEvent, scope, m, r, class)
			if ok {
				book.Quotes = append(book.Quotes, q)
			}
		}
	}
	book.Recompute()
	return book, nil
}

func futuresEventName(b fdBoard, eventID string) string {
	if e, ok := b.Attachments.Events[eventID]; ok && e.Name != "" {
		return e.Name
	}
	return "unknown"
}

func fdQuote(book *model.BookData, ev model.Event, hasEvent bool, scope string, m fdMarket, r fdRunner, class taxonomy.Classification) (model.Quote, bool) {
	q := model.Quote{
		Book: "fanduel", BookMarketID: m.MarketID.String(), BookSelectID: r.SelectionID.String(),
		Sport: book.Sport, League: book.Sport, Family: class.Family, Period: class.Period,
		Stat: class.Stat, Comparator: class.Comparator, MarketName: m.MarketName,
		MarketType: m.MarketType, Selection: r.RunnerName, Confidence: class.Confidence,
		Rule: class.Rule, Comparable: class.Comparable, Alt: class.Alt,
	}
	if hasEvent {
		q.ScopeKey = ev.Key
	} else {
		q.ScopeKey = scope
	}
	switch class.Family {
	case model.FamilyMoneyline, model.FamilySpread:
		if !hasEvent {
			return model.Quote{}, false
		}
		q.Subject = "event"
		switch strings.ToUpper(r.Result.Type) {
		case "HOME":
			q.Outcome = ev.Home.ID
		case "AWAY":
			q.Outcome = ev.Away.ID
		case "DRAW":
			q.Outcome = "draw"
		default:
			return model.Quote{}, false
		}
		if class.Family == model.FamilySpread {
			line := r.Handicap
			if (line == nil || *line == 0) && isAlternateFDType(m.MarketType) {
				line = fdSignedLine(r.RunnerName)
			}
			if line == nil {
				return model.Quote{}, false
			}
			q.Line = line
		}
	case model.FamilyTotal:
		if !hasEvent {
			return model.Quote{}, false
		}
		q.Subject = "event"
		line := r.Handicap
		outcome := strings.ToLower(r.Result.Type)
		if line == nil || *line == 0 || (outcome != "over" && outcome != "under") {
			if o, l, ok := taxonomy.OverUnder(r.RunnerName); ok {
				outcome, line = o, l
			}
		}
		if line == nil || (outcome != "over" && outcome != "under") {
			return model.Quote{}, false
		}
		q.Line = line
		q.Outcome = outcome
		q.LineUnit = class.Stat
	case model.FamilyPlayerProp:
		if class.Comparator == model.ComparatorYesNo {
			q.Subject = model.PlayerSubject(r.RunnerName)
			q.Outcome = "yes"
			break
		}
		player, line, ok := taxonomy.AltYards(r.RunnerName)
		if ok {
			q.Subject = model.PlayerSubject(player)
			q.Line = line
			q.Outcome = "yes"
			q.Alt = true
			break
		}
		// Standard over/under player prop. MGA runners put the line in the
		// numeric handicap and only "Over"/"Under" in runnerName.
		outcome, line2, ok := taxonomy.OverUnder(r.RunnerName)
		if !ok {
			outcome = strings.ToLower(r.Result.Type)
			if r.Handicap != nil {
				v := *r.Handicap
				line2 = &v
				ok = true
			}
		}
		if !ok || (outcome != "over" && outcome != "under") {
			return model.Quote{}, false
		}
		q.Subject = model.PlayerSubject(futuresSubject(m.MarketName))
		q.Outcome = outcome
		q.Line = line2
	case model.FamilyTeamProp:
		if class.Comparator == model.ComparatorAtLeast {
			name, line, ok := taxonomy.PlusStat(r.RunnerName)
			if !ok {
				return model.Quote{}, false
			}
			team, ok := identity.ResolveTeam(book.Sport, name)
			if !ok {
				return model.Quote{}, false
			}
			q.Subject = model.TeamSubject(team.ID)
			q.Line = line
			q.Outcome = "yes"
			break
		}
		outcome, line, ok := taxonomy.OverUnder(r.RunnerName)
		if !ok {
			return model.Quote{}, false
		}
		team, ok := identity.ResolveTeam(book.Sport, futuresSubject(m.MarketName))
		if !ok {
			return model.Quote{}, false
		}
		q.Subject = model.TeamSubject(team.ID)
		q.Outcome = outcome
		q.Line = line
	case model.FamilyFutures:
		if class.Stat != "wins" || class.Comparator != model.ComparatorOU {
			return model.Quote{}, false
		}
		outcome, line, ok := taxonomy.OverUnder(r.RunnerName)
		if !ok {
			return model.Quote{}, false
		}
		team, ok := identity.ResolveTeam(book.Sport, futuresSubject(m.MarketName))
		if !ok {
			return model.Quote{}, false
		}
		q.Subject = model.TeamSubject(team.ID)
		q.Outcome = outcome
		q.Line = line
	default:
		return model.Quote{}, false
	}
	if q.Outcome == "" {
		return model.Quote{}, false
	}
	q.Price.American = r.WinRunnerOdds.AmericanDisplayOdds.AmericanOddsInt
	if q.Price.American == nil && r.WinRunnerOdds.AmericanDisplayOdds.AmericanOdds != nil {
		v := int(*r.WinRunnerOdds.AmericanDisplayOdds.AmericanOdds)
		q.Price.American = &v
	}
	if r.WinRunnerOdds.TrueOdds.DecimalOdds.DecimalOdds != nil {
		q.Price.Decimal = r.WinRunnerOdds.TrueOdds.DecimalOdds.DecimalOdds
	}
	q.Price.Status = statusOf(m.MarketStatus, r.RunnerStatus)
	q.Price.InPlay = m.InPlay
	q.Key = model.QuoteKey(q)
	return q, true
}

func futuresSubject(marketName string) string {
	return taxonomy.FuturesSubject(marketName)
}
