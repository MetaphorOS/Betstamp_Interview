// Package taxonomy contains book-neutral market parsing helpers. Book packages
// own their own market-type tables; this package only handles shared text
// forms. All regular expressions are compiled once at package init.
package taxonomy

import (
	"regexp"
	"strconv"
	"strings"

	"betstampodds/internal/model"
)

// Classification is the canonical facet set inferred from a market.
type Classification struct {
	Family      string  `json:"family"`
	Period      string  `json:"period"`
	Stat        string  `json:"stat,omitempty"`
	SubjectKind string  `json:"subjectKind"`
	Comparator  string  `json:"comparator"`
	Rule        string  `json:"rule"`
	Confidence  float64 `json:"confidence"`
	Comparable  bool    `json:"comparable"`
	Alt         bool    `json:"alt"`
}

var (
	reH1        = regexp.MustCompile(`\b(1st|first)\s+half\b`)
	reH2        = regexp.MustCompile(`\b(2nd|second)\s+half\b`)
	reQ1        = regexp.MustCompile(`\b(1st|first)\s+quarter\b`)
	reQ2        = regexp.MustCompile(`\b(2nd|second)\s+quarter\b`)
	reQ3        = regexp.MustCompile(`\b(3rd|third)\s+quarter\b`)
	reQ4        = regexp.MustCompile(`\b(4th|fourth)\s+quarter\b`)
	reP1        = regexp.MustCompile(`\b(1st|first)\s+period\b`)
	reP2        = regexp.MustCompile(`\b(2nd|second)\s+period\b`)
	reP3        = regexp.MustCompile(`\b(3rd|third)\s+period\b`)
	reInning    = regexp.MustCompile(`\b(1st|first)\s+inning\b`)
	reSet       = regexp.MustCompile(`\b(1st|first)\s+set\b`)
	reAltYards  = regexp.MustCompile(`(?i)^(.+?)\s+(\d+(?:\.\d+)?)\+\s*yards?$`)
	reOverUnder = regexp.MustCompile(`(?i)\b(over|under)\s+(\d+(?:\.\d+)?)`)
	rePlusStat  = regexp.MustCompile(`(?i)^(.+?)\s+(\d+(?:\.\d+)?)\+\s*(.*)$`)
	reFutures   = regexp.MustCompile(`(?i)^\d{2}-\d{2}\s+\w+\s+(.+?)\s+(Regular Season|To Finish|Seed|Make|Miss|Division|Conference|Stage|Stanley|NBA|NFL)`)
)

// PeriodFromName extracts a period/segment from a market name. It is
// phrase-based so "1st Seed" and "First Touchdown" are not periods.
func PeriodFromName(name string) string {
	n := strings.ToLower(name)
	switch {
	case reH1.MatchString(n):
		return model.PeriodH1
	case reH2.MatchString(n):
		return model.PeriodH2
	case reQ1.MatchString(n):
		return model.PeriodQ1
	case reQ2.MatchString(n):
		return model.PeriodQ2
	case reQ3.MatchString(n):
		return model.PeriodQ3
	case reQ4.MatchString(n):
		return model.PeriodQ4
	case reP1.MatchString(n):
		return model.PeriodP1
	case reP2.MatchString(n):
		return model.PeriodP2
	case reP3.MatchString(n):
		return model.PeriodP3
	case reInning.MatchString(n):
		return model.PeriodInning
	case reSet.MatchString(n):
		return model.PeriodSet
	case strings.Contains(n, "season"):
		return model.PeriodSeason
	}
	return model.PeriodFull
}

// TotalStat maps a sport to the unit used by its total market.
func TotalStat(sport string) string {
	switch strings.ToLower(sport) {
	case "nhl", "soccer":
		return "goals"
	case "mlb":
		return "runs"
	default:
		return "points"
	}
}

// AltYards parses "Michael Penix Jr. 150+ Yards" into subject and line.
func AltYards(runner string) (string, *float64, bool) {
	m := reAltYards.FindStringSubmatch(strings.TrimSpace(runner))
	if len(m) != 3 {
		return "", nil, false
	}
	v, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return "", nil, false
	}
	return strings.TrimSpace(m[1]), &v, true
}

// PlusStat parses "Team 100+ Points" or "Player 2+ Goals".
func PlusStat(runner string) (string, *float64, bool) {
	m := rePlusStat.FindStringSubmatch(strings.TrimSpace(runner))
	if len(m) != 4 {
		return "", nil, false
	}
	v, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return "", nil, false
	}
	return strings.TrimSpace(m[1]), &v, true
}

// OverUnder parses "Over 3.5 Wins" into outcome and line.
func OverUnder(runner string) (string, *float64, bool) {
	m := reOverUnder.FindStringSubmatch(strings.TrimSpace(runner))
	if len(m) != 3 {
		return "", nil, false
	}
	v, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return "", nil, false
	}
	return strings.ToLower(m[1]), &v, true
}

// SignedLine parses a leading signed handicap such as "-3.5" or "+7".
func SignedLine(s string) (*float64, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || (s[0] != '+' && s[0] != '-') {
		return nil, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, false
	}
	return &v, true
}

// FuturesSubject extracts a subject from a futures market name.
func FuturesSubject(marketName string) string {
	n := strings.TrimSpace(marketName)
	if i := strings.Index(n, " - "); i > 0 {
		return strings.TrimSpace(n[:i])
	}
	if m := reFutures.FindStringSubmatch(n); len(m) == 3 {
		return strings.TrimSpace(m[1])
	}
	return n
}
