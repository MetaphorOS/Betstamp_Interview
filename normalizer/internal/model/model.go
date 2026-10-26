// Package model defines the canonical, book-independent entities used by the
// normalizer. The atomic unit is a Quote: one side of one market at one line.
// Quotes are the join key across books; markets and events are groupings.
package model

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Families are deliberately coarse. Sport- and book-specific detail lives in
// Stat/Period/Comparator/Subject and in the rule that produced the quote.
const (
	FamilyMoneyline  = "moneyline"
	FamilySpread     = "spread"
	FamilyTotal      = "total"
	FamilyTeamTotal  = "team_total"
	FamilyPlayerProp = "player_prop"
	FamilyTeamProp   = "team_prop"
	FamilyFutures    = "futures"
	FamilySpecial    = "special"
	FamilyUnknown    = "unknown"
)

const (
	PeriodFull   = "full"
	PeriodH1     = "h1"
	PeriodH2     = "h2"
	PeriodQ1     = "q1"
	PeriodQ2     = "q2"
	PeriodQ3     = "q3"
	PeriodQ4     = "q4"
	PeriodP1     = "p1"
	PeriodP2     = "p2"
	PeriodP3     = "p3"
	PeriodInning = "inning"
	PeriodSet    = "set"
	PeriodSeason = "season"
)

const (
	ComparatorNone    = "none"
	ComparatorOU      = "ou"
	ComparatorExact   = "exact"
	ComparatorAtLeast = "atleast"
	ComparatorYesNo   = "yesno"
)

// Team is a canonical participant. ID is stable across books; Name is for display.
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Abbr string `json:"abbr,omitempty"`
}

// Event is a game. Futures/competition markets are not Events; they use a
// competition scope on the Quote.
type Event struct {
	Book        string    `json:"book"`
	BookEventID string    `json:"bookEventId"`
	Sport       string    `json:"sport"`
	League      string    `json:"league"`
	Name        string    `json:"name"`
	Start       time.Time `json:"start"`
	Away        Team      `json:"away"`
	Home        Team      `json:"home"`
	Key         string    `json:"key"`
}

// Price is one normalized quote. Decimal is the canonical numeric price when
// available; American is retained because both feeds publish it and it is the
// field their own UIs compare.
type Price struct {
	American *int     `json:"american,omitempty"`
	Decimal  *float64 `json:"decimal,omitempty"`
	Status   string   `json:"status,omitempty"`
	InPlay   bool     `json:"inPlay"`
}

// Quote is the atomic join unit across books.
//
// ScopeKey is either an event key (sport|date|teamA@teamB) or a competition
// scope (comp|sport|competition). Subject is "event", a team ID, or a
// player:<slug>; Outcome is a team ID, over/under/draw/yes/no. Home/away is
// intentionally not used as the outcome because neutral-site and book-side
// conventions differ.
type Quote struct {
	Key          string   `json:"key"`
	Book         string   `json:"book"`
	BookMarketID string   `json:"bookMarketId,omitempty"`
	BookSelectID string   `json:"bookSelectionId,omitempty"`
	Sport        string   `json:"sport"`
	League       string   `json:"league"`
	ScopeKey     string   `json:"scopeKey"`
	Family       string   `json:"family"`
	Period       string   `json:"period"`
	Subject      string   `json:"subject"`
	Stat         string   `json:"stat,omitempty"`
	Comparator   string   `json:"comparator"`
	Outcome      string   `json:"outcome"`
	Line         *float64 `json:"line,omitempty"`
	LineUnit     string   `json:"lineUnit,omitempty"`
	Alt          bool     `json:"alt,omitempty"`
	Comparable   bool     `json:"comparable"`
	Confidence   float64  `json:"confidence"`
	Rule         string   `json:"rule"`
	MarketName   string   `json:"marketName,omitempty"`
	MarketType   string   `json:"marketType,omitempty"`
	Selection    string   `json:"selection,omitempty"`
	Price        Price    `json:"price"`
}

// MarketClass is the classification result for one book market, including
// markets that cannot produce comparable quotes (futures/specials/unknowns).
type MarketClass struct {
	Book         string  `json:"book"`
	BookMarketID string  `json:"bookMarketId"`
	Sport        string  `json:"sport"`
	ScopeKey     string  `json:"scopeKey"`
	Family       string  `json:"family"`
	Period       string  `json:"period"`
	Stat         string  `json:"stat,omitempty"`
	Comparator   string  `json:"comparator"`
	SubjectKind  string  `json:"subjectKind"`
	MarketName   string  `json:"marketName"`
	MarketType   string  `json:"marketType"`
	Rule         string  `json:"rule"`
	Confidence   float64 `json:"confidence"`
	Comparable   bool    `json:"comparable"`
	Alt          bool    `json:"alt,omitempty"`
	Selections   int     `json:"selections"`
}

// Counts summarizes one book's normalized view.
type Counts struct {
	Events             int            `json:"events"`
	GameEvents         int            `json:"gameEvents"`
	Markets            int            `json:"markets"`
	Quotes             int            `json:"quotes"`
	ComparableQuotes   int            `json:"comparableQuotes"`
	ClassifiedMarkets  int            `json:"classifiedMarkets"`
	HighConfidence     int            `json:"highConfidenceMarkets"`
	UnknownMarkets     int            `json:"unknownMarkets"`
	DanglingSelections int            `json:"danglingSelections"`
	FamilyCounts       map[string]int `json:"familyCounts"`
	UnknownTypes       map[string]int `json:"unknownTypes,omitempty"`
}

// BookData is one normalized book/sport snapshot.
type BookData struct {
	Book    string        `json:"book"`
	Sport   string        `json:"sport"`
	League  string        `json:"league"`
	Events  []Event       `json:"events"`
	Markets []MarketClass `json:"markets"`
	Quotes  []Quote       `json:"quotes"`
	Counts  Counts        `json:"counts"`
}

// Recompute fills the summary counters from the slices.
func (b *BookData) Recompute() {
	dangling := b.Counts.DanglingSelections
	c := Counts{FamilyCounts: map[string]int{}, UnknownTypes: map[string]int{}}
	c.Events = len(b.Events)
	for _, e := range b.Events {
		if e.Home.ID != "" && e.Away.ID != "" {
			c.GameEvents++
		}
	}
	c.Markets = len(b.Markets)
	for _, m := range b.Markets {
		c.FamilyCounts[m.Family]++
		if m.Family == FamilyUnknown {
			c.UnknownMarkets++
			c.UnknownTypes[m.MarketType]++
			continue
		}
		c.ClassifiedMarkets++
		if m.Confidence >= 0.9 {
			c.HighConfidence++
		}
	}
	c.Quotes = len(b.Quotes)
	for _, q := range b.Quotes {
		if q.Comparable {
			c.ComparableQuotes++
		}
	}
	c.DanglingSelections = dangling
	b.Counts = c
}

// ScopeEvent builds the canonical event scope. Team IDs are sorted so home/away
// convention or a neutral-site declaration cannot break identity.
func ScopeEvent(sport string, start time.Time, teamA, teamB string) string {
	ids := []string{teamA, teamB}
	sort.Strings(ids)
	return strings.Join([]string{
		"event", strings.ToLower(sport), start.UTC().Format("2006-01-02"), ids[0] + "@" + ids[1],
	}, "|")
}

// ScopeCompetition builds the scope for futures/season markets.
func ScopeCompetition(sport, competition string) string {
	return "comp|" + strings.ToLower(sport) + "|" + Slug(competition)
}

// QuoteKey is the cross-book join key. It intentionally excludes book IDs,
// prices, display names and status.
func QuoteKey(q Quote) string {
	return quoteKey(q, true)
}

// IdentityKey is the quote identity without the line. It answers "same market
// and outcome?" even when the two books quote different lines.
func IdentityKey(q Quote) string {
	return quoteKey(q, false)
}

func quoteKey(q Quote, withLine bool) string {
	line := ""
	if withLine {
		line = LineString(q.Line)
	}
	return strings.Join([]string{
		strings.ToLower(q.Sport),
		q.ScopeKey,
		q.Family,
		q.Period,
		q.Subject,
		q.Stat,
		q.Comparator,
		line,
		q.Outcome,
	}, "|")
}

// LineString formats a line without trailing zeros ("3", "-3", "44.5").
func LineString(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// Slug is a dependency-free identifier slug used for players, competitions and
// non-registry teams. It is stable for the same spelling but is not an alias
// resolver; player/team aliases belong in the identity registry.
func Slug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash && b.Len() > 0:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ParseLine parses a decimal/integer line from a name or field.
func ParseLine(s string) (*float64, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "+"))
	if s == "" {
		return nil, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, false
	}
	return &v, true
}

// PlayerSubject builds a provisional canonical player subject. Cross-book player
// identity requires a registry; this is the fallback.
func PlayerSubject(name string) string {
	return "player:" + Slug(name)
}

// TeamSubject builds a canonical team subject from a team ID.
func TeamSubject(id string) string { return "team:" + id }

// DescribeScope is used in reports.
func DescribeScope(s string) string {
	parts := strings.Split(s, "|")
	if len(parts) >= 3 {
		return parts[len(parts)-1]
	}
	return fmt.Sprint(s)
}
