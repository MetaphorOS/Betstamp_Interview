package adapters

import (
	"encoding/json"
	"os"
	"strings"

	"sportsbook-normalizer/internal/identity"
	"sportsbook-normalizer/internal/model"
	"sportsbook-normalizer/internal/taxonomy"
)

type dkParticipant struct {
	Name      string `json:"name"`
	VenueRole string `json:"venueRole"`
}

type dkEvent struct {
	ID             flexID          `json:"id"`
	Name           string          `json:"name"`
	StartEventDate string          `json:"startEventDate"`
	Participants   []dkParticipant `json:"participants"`
	Status         string          `json:"status"`
}

type dkMarketType struct {
	Name string `json:"name"`
}

type dkMarket struct {
	ID                     flexID       `json:"id"`
	EventID                flexID       `json:"eventId"`
	Name                   string       `json:"name"`
	MarketType             dkMarketType `json:"marketType"`
	IsSuspended            bool         `json:"isSuspended"`
	SubcategoryID          flexID       `json:"subcategoryId"`
	MarketIdentifierString string       `json:"marketIdentifierString"`
}

type dkSelection struct {
	ID          flexID   `json:"id"`
	MarketID    flexID   `json:"marketId"`
	Label       string   `json:"label"`
	OutcomeType string   `json:"outcomeType"`
	Points      *float64 `json:"points"`
	DisplayOdds struct {
		American string `json:"american"`
		Decimal  string `json:"decimal"`
	} `json:"displayOdds"`
	TrueOdds float64 `json:"trueOdds"`
}

type dkSnapshot struct {
	Events     []dkEvent     `json:"events"`
	Markets    []dkMarket    `json:"markets"`
	Selections []dkSelection `json:"selections"`
}

// DK parses a DraftKings snapshot or partial cache into canonical entities.
// Partial caches can contain selections without marketId (dangling); those are
// counted, never guessed into a market.
func DK(path, sport string) (*model.BookData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap dkSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	book := &model.BookData{Book: "draftkings", Sport: sport, League: sport}

	events := map[string]model.Event{}
	for _, e := range snap.Events {
		var home, away string
		for _, p := range e.Participants {
			switch strings.ToLower(p.VenueRole) {
			case "home":
				home = p.Name
			case "away":
				away = p.Name
			}
		}
		ht, okH := identity.ResolveTeam(sport, home)
		at, okA := identity.ResolveTeam(sport, away)
		if !okH || !okA {
			continue
		}
		ev := model.Event{
			Book: "draftkings", BookEventID: e.ID.String(), Sport: sport, League: sport,
			Name: e.Name, Start: parseTime(e.StartEventDate), Home: ht, Away: at,
		}
		ev.Key = model.ScopeEvent(sport, ev.Start, at.ID, ht.ID)
		events[ev.BookEventID] = ev
		book.Events = append(book.Events, ev)
	}

	marketRaw := map[string]dkMarket{}
	classes := map[string]model.MarketClass{}
	for _, m := range snap.Markets {
		id := m.ID.String()
		marketRaw[id] = m
		ev, hasEvent := events[m.EventID.String()]
		scope := ""
		if hasEvent {
			scope = ev.Key
		} else {
			scope = model.ScopeCompetition(sport, m.Name)
		}
		class := classifyDK(sport, m.MarketType.Name, m.Name, scope)
		class.BookMarketID = id
		classes[id] = class
		book.Markets = append(book.Markets, class)
	}

	for _, s := range snap.Selections {
		mid := s.MarketID.String()
		if mid == "" {
			book.Counts.DanglingSelections++
			continue
		}
		m, ok := marketRaw[mid]
		if !ok {
			book.Counts.DanglingSelections++
			continue
		}
		ev, hasEvent := events[m.EventID.String()]
		if !hasEvent {
			book.Counts.DanglingSelections++
			continue
		}
		class := classes[mid]
		q, ok := dkQuote(sport, ev, m, s, class)
		if !ok {
			continue
		}
		book.Quotes = append(book.Quotes, q)
	}
	book.Recompute()
	return book, nil
}

func classifyDK(sport, marketType, marketName, scope string) model.MarketClass {
	mt := strings.TrimSpace(marketType)
	mn := strings.TrimSpace(marketName)
	t := strings.ToLower(mt + " " + mn)
	period := taxonomy.PeriodFromName(marketName)
	class := model.MarketClass{
		Book: "draftkings", Sport: sport, ScopeKey: scope, MarketName: marketName,
		MarketType: marketType, Period: period, Rule: "unknown", Confidence: 0,
		Family: model.FamilyUnknown, Comparator: model.ComparatorNone, SubjectKind: "event",
	}
	isMain := func(name string) bool {
		return strings.EqualFold(mt, name) || (mt == "" && strings.EqualFold(mn, name))
	}
	switch {
	case isMain("Moneyline"):
		class.Family, class.Comparator, class.Rule, class.Confidence, class.Comparable = model.FamilyMoneyline, model.ComparatorNone, "dk.main.moneyline", 1, true
	case isMain("Spread"):
		class.Family, class.Comparator, class.Rule, class.Confidence, class.Comparable = model.FamilySpread, model.ComparatorNone, "dk.main.spread", 1, true
	case strings.EqualFold(mt, "Spread Alternate"):
		class.Family, class.Comparator, class.Alt, class.Rule, class.Confidence, class.Comparable = model.FamilySpread, model.ComparatorNone, true, "dk.alt.spread", 0.95, true
	case isMain("Total"):
		class.Family, class.Stat, class.Comparator, class.Rule, class.Confidence, class.Comparable = model.FamilyTotal, taxonomy.TotalStat(sport), model.ComparatorOU, "dk.main.total", 1, true
	case strings.EqualFold(mt, "Total Alternate"):
		class.Family, class.Stat, class.Comparator, class.Alt, class.Rule, class.Confidence, class.Comparable = model.FamilyTotal, taxonomy.TotalStat(sport), model.ComparatorOU, true, "dk.alt.total", 0.95, true
	case strings.Contains(t, "regular season wins"):
		class.Family, class.Period, class.Stat, class.Comparator, class.SubjectKind, class.Rule, class.Confidence = model.FamilyFutures, model.PeriodSeason, "wins", model.ComparatorOU, "team", "dk.futures.wins", 0.9
	case strings.Contains(t, " o/u"):
		if stat, ok := dkPropStat(t); ok {
			class.Family, class.Stat, class.Comparator, class.SubjectKind, class.Rule, class.Confidence, class.Comparable, class.Alt = model.FamilyPlayerProp, stat, model.ComparatorOU, "player", "dk.prop.ou", 0.85, true, true
		} else {
			class.Family, class.Stat, class.Comparator, class.SubjectKind, class.Rule, class.Confidence, class.Comparable, class.Alt = model.FamilyPlayerProp, "unknown_prop", model.ComparatorOU, "player", "dk.prop.ou.unknown", 0.5, false, true
		}
	case strings.Contains(t, "score 2 or more"), strings.Contains(t, "2+ tds"):
		class.Family, class.Stat, class.Comparator, class.SubjectKind, class.Rule, class.Confidence, class.Comparable, class.Alt = model.FamilyPlayerProp, "two_plus_touchdowns", model.ComparatorYesNo, "player", "dk.prop.td2", 0.8, true, true
	case strings.Contains(t, "touchdown"), strings.Contains(t, "td scorer"):
		class.Family, class.Stat, class.Comparator, class.SubjectKind, class.Rule, class.Confidence, class.Comparable, class.Alt = model.FamilyPlayerProp, "anytime_touchdown", model.ComparatorYesNo, "player", "dk.prop.td", 0.8, true, true
	}
	return class
}

// dkPropStat maps DK prop market text to a canonical stat. Longest phrases are
// checked first so "steals + blocks" does not collapse to "steals".
func dkPropStat(t string) (string, bool) {
	phrases := []struct {
		needle string
		stat   string
	}{
		{"steals + blocks", "steals_blocks"},
		{"tackles + assists", "tackles_assists"},
		{"three pointers made", "threes"},
		{"threes", "threes"},
		{"points", "points"},
		{"rebounds", "rebounds"},
		{"assists", "assists"},
		{"steals", "steals"},
		{"blocks", "blocks"},
		{"pass yards", "passing_yards"},
		{"passing yards", "passing_yards"},
		{"passing + rushing yards", "passing_rushing_yards"},
		{"pass tds", "passing_touchdowns"},
		{"passing touchdowns", "passing_touchdowns"},
		{"pass attempts", "passing_attempts"},
		{"passing attempts", "passing_attempts"},
		{"completions", "completions"},
		{"interceptions", "interceptions"},
		{"rush + rec yards", "rushing_receiving_yards"},
		{"rushing + receiving yards", "rushing_receiving_yards"},
		{"rec yards", "receiving_yards"},
		{"receiving yards", "receiving_yards"},
		{"receptions", "receptions"},
		{"rush yards", "rushing_yards"},
		{"rushing yards", "rushing_yards"},
		{"rush attempts", "rushing_attempts"},
		{"rushing attempts", "rushing_attempts"},
		{"tackles", "tackles"},
		{"sacks", "sacks"},
		{"longest reception", "longest_reception"},
		{"longest completion", "longest_completion"},
		{"longest passing completion", "longest_completion"},
		{"longest rush", "longest_rush"},
	}
	for _, p := range phrases {
		if strings.Contains(t, p.needle) {
			return p.stat, true
		}
	}
	return "", false
}

func dkQuote(sport string, ev model.Event, m dkMarket, s dkSelection, class model.MarketClass) (model.Quote, bool) {
	q := model.Quote{
		Book: "draftkings", BookMarketID: m.ID.String(), BookSelectID: s.ID.String(),
		Sport: sport, League: sport, ScopeKey: ev.Key, Family: class.Family,
		Period: class.Period, Stat: class.Stat, Comparator: class.Comparator,
		MarketName: m.Name, MarketType: m.MarketType.Name, Selection: s.Label,
		Confidence: class.Confidence, Rule: class.Rule, Comparable: class.Comparable,
		Alt: class.Alt,
	}
	outcome := strings.ToLower(s.OutcomeType)
	switch class.Family {
	case model.FamilyMoneyline:
		switch outcome {
		case "home":
			q.Outcome = ev.Home.ID
		case "away":
			q.Outcome = ev.Away.ID
		default:
			return model.Quote{}, false
		}
		q.Subject = "event"
	case model.FamilySpread:
		switch outcome {
		case "home":
			q.Outcome = ev.Home.ID
		case "away":
			q.Outcome = ev.Away.ID
		default:
			return model.Quote{}, false
		}
		q.Subject = "event"
		q.Line = s.Points
	case model.FamilyTotal:
		q.Subject = "event"
		q.Line = s.Points
		q.Outcome = outcome
		q.LineUnit = class.Stat
	case model.FamilyPlayerProp:
		q.Subject = model.PlayerSubject(dkPlayerFromName(s.Label, m.Name))
		q.Line = s.Points
		q.Outcome = outcome
		if class.Comparator == model.ComparatorYesNo {
			q.Outcome = "yes"
		}
	default:
		return model.Quote{}, false
	}
	if q.Outcome == "" {
		return model.Quote{}, false
	}
	q.Price.American = parseAmerican(s.DisplayOdds.American)
	q.Price.Decimal = parseDecimal(s.DisplayOdds.Decimal)
	if q.Price.Decimal == nil && s.TrueOdds > 0 {
		v := s.TrueOdds
		q.Price.Decimal = &v
	}
	q.Price.Status = "open"
	if m.IsSuspended {
		q.Price.Status = "suspended"
	}
	q.Key = model.QuoteKey(q)
	return q, true
}

func dkPlayerFromName(label, marketName string) string {
	if i := strings.Index(marketName, " - "); i > 0 {
		return strings.TrimSpace(marketName[:i])
	}
	name := strings.TrimSpace(marketName)
	lower := strings.ToLower(name)
	suffixes := []string{
		" points o/u", " rebounds o/u", " assists o/u", " three pointers made o/u",
		" threes o/u", " steals + blocks o/u", " steals o/u", " blocks o/u",
		" pass yards o/u", " pass tds o/u", " pass attempts o/u", " completions o/u",
		" passing attempts o/u", " passing + rushing yards o/u",
		" interceptions o/u", " interceptions thrown o/u",
		" rec yards o/u", " receptions o/u",
		" rush + rec yards o/u", " rushing + receiving yards o/u",
		" rush yards o/u", " rush attempts o/u",
		" rushing attempts o/u",
		" tackles + assists o/u", " tackles o/u", " sacks o/u",
		" longest reception o/u", " longest completion o/u",
		" longest passing completion o/u", " longest rush o/u",
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(lower, suffix) {
			name = strings.TrimSpace(name[:len(name)-len(suffix)])
			break
		}
	}
	if name == "" || strings.EqualFold(name, "over") || strings.EqualFold(name, "under") {
		return strings.TrimSpace(label)
	}
	return name
}
