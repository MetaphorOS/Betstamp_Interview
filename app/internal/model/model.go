// Package model defines the book-independent entities shared by every book
// plugin, the matcher, the board and (later) the fanout hub.
package model

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Market families. The board intentionally supports only the main NFL three,
// but the model keeps the classification vocabulary needed by every book.
const (
	FamilyMoneyline = "moneyline"
	FamilySpread    = "spread"
	FamilyTotal     = "total"
	FamilyTeamTotal = "team_total"
	FamilyPlayer    = "player_prop"
	FamilyTeamProp  = "team_prop"
	FamilyFutures   = "futures"
	FamilySpecial   = "special"
	FamilyUnknown   = "unknown"
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

// Status is the executable state of a quote. REMOVED is terminal until a new
// quote arrives for the same selection.
type Status string

const (
	StatusOpen      Status = "open"
	StatusSuspended Status = "suspended"
	StatusRemoved   Status = "removed"
)

// Team is a canonical participant. ID is stable across books.
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Abbr string `json:"abbr,omitempty"`
}

// Game is a canonical game. It is never created by a book plugin; the matcher
// assigns it.
type Game struct {
	ID     string    `json:"id"`
	Sport  string    `json:"sport"`
	League string    `json:"league"`
	Start  time.Time `json:"start"`
	Away   Team      `json:"away"`
	Home   Team      `json:"home"`
}

// BookGame is a book's own game record before canonical matching.
type BookGame struct {
	Book       string    `json:"book"`
	BookGameID string    `json:"bookGameId"`
	Sport      string    `json:"sport"`
	League     string    `json:"league"`
	Name       string    `json:"name"`
	Start      time.Time `json:"start"`
	Away       Team      `json:"away"`
	Home       Team      `json:"home"`
	Live       bool      `json:"live"`
	Status     string    `json:"status,omitempty"`
}

// Price is one executable quote. American is the canonical field for the
// sportsbook board; Decimal is retained for prediction markets and de-vig.
type Price struct {
	American int     `json:"american"`
	Decimal  float64 `json:"decimal,omitempty"`
	Status   Status  `json:"status"`
	InPlay   bool    `json:"inPlay"`
}

// Quote is one side of one market. GameID is the canonical game ID assigned by
// the matcher. BookGameID/BookMarketID/BookSelectionKey are source identity and
// must remain stable across resyncs.
type Quote struct {
	Book             string   `json:"book"`
	BookGameID       string   `json:"bookGameId"`
	BookMarketID     string   `json:"bookMarketId"`
	BookSelectionKey string   `json:"bookSelectionKey"`
	GameID           string   `json:"gameId,omitempty"`
	Sport            string   `json:"sport"`
	League           string   `json:"league"`
	Family           string   `json:"family"`
	Period           string   `json:"period"`
	Subject          string   `json:"subject"`
	Stat             string   `json:"stat,omitempty"`
	Comparator       string   `json:"comparator"`
	Outcome          string   `json:"outcome"`
	Line             *float64 `json:"line,omitempty"`
	LineUnit         string   `json:"lineUnit,omitempty"`
	Alt              bool     `json:"alt,omitempty"`
	MarketName       string   `json:"marketName,omitempty"`
	SelectionName    string   `json:"selectionName,omitempty"`
	Rule             string   `json:"rule,omitempty"`
	Confidence       float64  `json:"confidence,omitempty"`
	Price            Price    `json:"price"`
}

// Source says how an update reached us. The board uses it to decide whether an
// update may overwrite newer stream data.
type Source string

const (
	SourceStream   Source = "stream"
	SourcePoll     Source = "poll"
	SourceSnapshot Source = "snapshot"
)

// Update is the normalized unit emitted by a book stream or poll and applied
// by the board. Identity excludes line; the board combines canonical game id
// and identity into a slot. GameRef is the BOOK's game id, not a canonical id.
type Update struct {
	Book     string    `json:"book"`
	GameRef  string    `json:"gameRef"`
	Identity string    `json:"identity"`
	Line     *float64  `json:"line,omitempty"`
	American int       `json:"american"`
	Decimal  float64   `json:"decimal,omitempty"`
	Status   Status    `json:"status"`
	InPlay   bool      `json:"inPlay"`
	BookTS   time.Time `json:"bookTs,omitempty"` // book publish time, when the feed has one
	RecvAt   time.Time `json:"recvAt"`           // when our process read the frame or response
	Source   Source    `json:"source"`
	AsOf     time.Time `json:"asOf,omitempty"` // poll/snapshot only: request send time
}

// UpdateFromQuote converts a snapshot or poll quote into an Update.
func UpdateFromQuote(q Quote, src Source, asOf, recvAt time.Time) Update {
	return Update{
		Book: q.Book, GameRef: q.BookGameID, Identity: IdentityKey(q),
		Line: CopyLine(q.Line), American: q.Price.American, Decimal: q.Price.Decimal,
		Status: q.Price.Status, InPlay: q.Price.InPlay,
		RecvAt: recvAt, Source: src, AsOf: asOf,
	}
}

// IdentityParts is IdentityKey split back into its facets.
type IdentityParts struct {
	Family, Period, Subject, Stat, Comparator, Class, Outcome string
}

// ParseIdentity reverses IdentityKey.
func ParseIdentity(identity string) (IdentityParts, bool) {
	f := strings.Split(identity, "|")
	if len(f) != 7 {
		return IdentityParts{}, false
	}
	return IdentityParts{Family: f[0], Period: f[1], Subject: f[2], Stat: f[3], Comparator: f[4], Class: f[5], Outcome: f[6]}, true
}

// MainIdentity is the identity of a full-game main quote. Outcome is a team ID
// for moneyline and spread and "over"/"under" for totals.
func MainIdentity(family, outcome string) string {
	q := Quote{Family: family, Period: PeriodFull, Subject: "event", Outcome: outcome}
	if family == FamilyTotal {
		q.Stat, q.Comparator = "points", ComparatorOU
	}
	return IdentityKey(q)
}

// CopyLine returns an independent copy of a line pointer.
func CopyLine(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// GameKey is the canonical game identifier used on the board. The team IDs are
// sorted so home/away spelling cannot change it. The UTC hour keeps the ID
// stable when a source moves its start time by a few minutes.
func GameKey(sport string, start time.Time, teamA, teamB string) string {
	ids := []string{teamA, teamB}
	sort.Strings(ids)
	return strings.Join([]string{
		strings.ToLower(sport),
		start.UTC().Format("20060102T15"),
		ids[0] + "@" + ids[1],
	}, "|")
}

// IdentityKey is the canonical slot facet set. It excludes the line and the
// canonical game; the board composes canonicalGameID + IdentityKey. The
// main/alternate marker is included so those markets can never collide.
func IdentityKey(q Quote) string {
	class := "main"
	if q.Alt {
		class = "alt"
	}
	return strings.Join([]string{
		q.Family,
		q.Period,
		q.Subject,
		q.Stat,
		q.Comparator,
		class,
		q.Outcome,
	}, "|")
}

// QuoteKey is the exact-line join key. It is book-local; it does not include a
// canonical game id.
func QuoteKey(q Quote) string {
	return strings.Join([]string{
		IdentityKey(q),
		LineString(q.Line),
	}, "|")
}

// BoardSlotKey composes the canonical game with IdentityKey. It refuses to
// build a slot when the canonical game is missing; BookGameID must never leak
// into the board namespace as a fallback.
func BoardSlotKey(canonicalGameID string, q Quote) (string, error) {
	if canonicalGameID == "" {
		return "", ErrMissingCanonicalGame
	}
	return canonicalGameID + "|" + IdentityKey(q), nil
}

// ErrMissingCanonicalGame is returned when a quote without a matched canonical
// game is offered to the board.
var ErrMissingCanonicalGame = errors.New("model: missing canonical game id")

// slotKey is the unexported board composer used by tests and future board code.
func slotKey(canonicalGameID string, q Quote) (string, error) {
	return BoardSlotKey(canonicalGameID, q)
}

// LineString formats a line without trailing zeros.
func LineString(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// PlayerSubject is a provisional player identity until a player registry is
// added. It is intentionally a single function so the registry can replace it.
func PlayerSubject(name string) string {
	return "player:" + Slug(PlayerName(name))
}

// PlayerName normalizes a player name so the books agree: lower case, no
// diacritics or punctuation, no generational suffix. "Amon-Ra St. Brown",
// "John Metchie III" and "Kenneth Walker" match their spellings at every book
// this way (checked across three books' NFL props on 2026-10-27).
func PlayerName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if base, ok := foldDiacritic[r]; ok {
			r = base
		}
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r == '-' || unicode.IsSpace(r):
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	out := words[:0]
	for _, w := range words {
		switch w {
		case "jr", "sr", "ii", "iii", "iv", "v":
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

var foldDiacritic = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ä': 'a', 'ã': 'a', 'å': 'a', 'ç': 'c',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e', 'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n', 'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o', 'ú': 'u', 'ù': 'u',
	'û': 'u', 'ü': 'u', 'ý': 'y', 'ÿ': 'y', 'š': 's', 'ž': 'z', 'č': 'c', 'ć': 'c',
}

// NFL player prop stats. Every book's own stat names map onto these.
const (
	StatPassYds     = "pass_yds"
	StatPassTD      = "pass_td"
	StatPassAtt     = "pass_att"
	StatPassCmp     = "pass_cmp"
	StatPassRushYds = "pass_rush_yds"
	StatRushYds     = "rush_yds"
	StatRushAtt     = "rush_att"
	StatRushRecYds  = "rush_rec_yds"
	StatRecYds      = "rec_yds"
	StatRec         = "rec"
	StatInt         = "int"
	StatTD          = "td" // touchdowns scored: anytime (1+), 2+, 3+
)

// PropQuote fills the identity facets of a full-game player prop.
//
// Two-way props use ComparatorOU with outcome "over"/"under"; the line may
// differ by book and the best-price rule treats it like a game total.
// Milestones ("25+ receiving yards", "anytime TD") use ComparatorAtLeast with
// the threshold as the outcome and the line, so every rung of a ladder is its
// own slot and rungs never overwrite each other.
func PropQuote(q Quote, player, stat, comparator, outcome string, line float64) Quote {
	q.Family, q.Period, q.Subject, q.Stat, q.Comparator, q.Outcome = FamilyPlayer, PeriodFull, PlayerSubject(player), stat, comparator, outcome
	q.Line = &line
	return q
}

// MilestoneOutcome is the outcome of an at-least prop with threshold n.
func MilestoneOutcome(n float64) string {
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// TeamSubject is the canonical team subject.
func TeamSubject(id string) string {
	return "team:" + id
}

// Slug is a dependency-free identifier slug. It is not an alias resolver.
func Slug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case lastDash:
			continue
		default:
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
