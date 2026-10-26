// Package taxonomy maps book market types/names into canonical family, period,
// stat and comparator facets. Classification is separate from comparability:
// a market can be understood (e.g. a future) without being safe to compare
// across books.
package taxonomy

import (
	"regexp"
	"strconv"
	"strings"

	"sportsbook-normalizer/internal/model"
)

// Classification is the facet set inferred for a book market.
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
	reAltYards      = regexp.MustCompile(`(?i)^(.+?)\s+(\d+(?:\.\d+)?)\+\s*yards?$`)
	reOverUnder     = regexp.MustCompile(`(?i)\b(over|under)\s+(\d+(?:\.\d+)?)`)
	rePlusStat      = regexp.MustCompile(`(?i)^(.+?)\s+(\d+(?:\.\d+)?)\+\s*(.*)$`)
	reRecordPlus    = regexp.MustCompile(`(?i)PLAYER_TO_RECORD_(\d+)\+\s*_?([A-Z_]+)`)
	reFDPlayerTotal = regexp.MustCompile(`^PLAYER_[A-Z0-9]+_(TOTAL_|1ST_QUARTER_)(POINTS|REBOUNDS|ASSISTS|MADE_3_POINT_FIELD_GOALS|STEALS|BLOCKS|STEALS_\+_BLOCKS)$`)
)

func fdPlayerTotal(t string) (stat, period string, ok bool) {
	m := reFDPlayerTotal.MatchString(t)
	if !m {
		return "", "", false
	}
	parts := strings.SplitN(t, "_", 3)
	if len(parts) < 3 {
		return "", "", false
	}
	rest := parts[2]
	switch {
	case strings.Contains(rest, "1ST_QUARTER"):
		period = model.PeriodQ1
	case strings.Contains(rest, "TOTAL"):
		period = model.PeriodFull
	default:
		return "", "", false
	}
	switch {
	case strings.HasSuffix(rest, "STEALS_+_BLOCKS"):
		return "steals_blocks", period, true
	case strings.HasSuffix(rest, "MADE_3_POINT_FIELD_GOALS"):
		return "threes", period, true
	case strings.HasSuffix(rest, "POINTS"):
		return "points", period, true
	case strings.HasSuffix(rest, "REBOUNDS"):
		return "rebounds", period, true
	case strings.HasSuffix(rest, "ASSISTS"):
		return "assists", period, true
	case strings.HasSuffix(rest, "STEALS"):
		return "steals", period, true
	case strings.HasSuffix(rest, "BLOCKS"):
		return "blocks", period, true
	}
	return "", "", false
}

// IsMain reports whether the type is one of the three full-game main markets.
func IsMain(marketType string) bool {
	switch strings.ToUpper(marketType) {
	case "MONEY_LINE", "MATCH_HANDICAP_(2-WAY)", "TOTAL_POINTS_(OVER/UNDER)":
		return true
	}
	return false
}

// ClassifyFD classifies a FanDuel market type/name. Unknown types stay unknown
// and are surfaced by the report instead of being compared silently.
func ClassifyFD(sport, marketType, marketName string) Classification {
	t := strings.ToUpper(strings.TrimSpace(marketType))
	period := PeriodFromName(marketName)
	base := Classification{
		Family:      model.FamilyUnknown,
		Period:      period,
		Comparator:  model.ComparatorNone,
		SubjectKind: "event",
		Rule:        "unknown",
		Confidence:  0,
	}

	switch t {
	case "MONEY_LINE":
		return Classification{Family: model.FamilyMoneyline, Period: period, Comparator: model.ComparatorNone, SubjectKind: "event", Rule: "fd.main.moneyline", Confidence: 1, Comparable: true}
	case "MATCH_HANDICAP_(2-WAY)":
		return Classification{Family: model.FamilySpread, Period: period, Comparator: model.ComparatorNone, SubjectKind: "event", Rule: "fd.main.spread", Confidence: 1, Comparable: true}
	case "ALTERNATE_HANDICAP", "ALTERNATE_HANDICAPS":
		return Classification{Family: model.FamilySpread, Period: period, Comparator: model.ComparatorNone, SubjectKind: "event", Rule: "fd.alt.spread", Confidence: 0.95, Comparable: true, Alt: true}
	case "TOTAL_POINTS_(OVER/UNDER)":
		return Classification{Family: model.FamilyTotal, Period: period, Stat: TotalStat(sport), Comparator: model.ComparatorOU, SubjectKind: "event", Rule: "fd.main.total", Confidence: 1, Comparable: true}
	case "ALTERNATE_TOTAL", "ALTERNATE_TOTAL_POINTS":
		return Classification{Family: model.FamilyTotal, Period: period, Stat: TotalStat(sport), Comparator: model.ComparatorOU, SubjectKind: "event", Rule: "fd.alt.total", Confidence: 0.95, Comparable: true, Alt: true}
	case "ANY_TIME_TOUCHDOWN_SCORER":
		return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: "anytime_touchdown", Comparator: model.ComparatorYesNo, SubjectKind: "player", Rule: "fd.prop.anytime_td", Confidence: 0.95}
	case "FIRST_GOAL_SCORER":
		return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: "first_goal", Comparator: model.ComparatorYesNo, SubjectKind: "player", Rule: "fd.prop.first_goal", Confidence: 0.95}
	case "ANY_TIME_GOAL_SCORER":
		return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: "anytime_goal", Comparator: model.ComparatorYesNo, SubjectKind: "player", Rule: "fd.prop.anytime_goal", Confidence: 0.95}
	case "PLAYER_X_ALT_PASSING_YARDS_HIGH":
		return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: "passing_yards", Comparator: model.ComparatorAtLeast, SubjectKind: "player", Rule: "fd.prop.alt_passing_yards", Confidence: 0.9, Alt: true}
	case "PLAYER_X_ALT_RECEIVING_YARDS_HIGH":
		return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: "receiving_yards", Comparator: model.ComparatorAtLeast, SubjectKind: "player", Rule: "fd.prop.alt_receiving_yards", Confidence: 0.9, Alt: true}
	case "TEAM_TOTAL_POINTS_SGP":
		return Classification{Family: model.FamilyTeamProp, Period: period, Stat: "points", Comparator: model.ComparatorOU, SubjectKind: "team", Rule: "fd.prop.team_total_points", Confidence: 0.9}
	case "TEAM_X+_POINTS_SGP":
		return Classification{Family: model.FamilyTeamProp, Period: period, Stat: "points", Comparator: model.ComparatorAtLeast, SubjectKind: "team", Rule: "fd.prop.team_x_points", Confidence: 0.9, Alt: true}
	case "REGULAR_SEASON_WINS_SGP", "NBA_REGULAR_SEASON_WINS_O/U", "CFB_REGULAR_SEASON_WINS_TIER_1_SGP", "CFB_REGULAR_SEASON_WINS_TIER_2_SGP", "CFB_REGULAR_SEASON_WINS_H2H_SGP":
		return Classification{Family: model.FamilyFutures, Period: model.PeriodSeason, Stat: "wins", Comparator: model.ComparatorOU, SubjectKind: "team", Rule: "fd.futures.regular_season_wins", Confidence: 0.9}
	}
	if strings.Contains(t, "ALTERNATE_HANDICAP") {
		return Classification{Family: model.FamilySpread, Period: period, Comparator: model.ComparatorNone, SubjectKind: "event", Rule: "fd.alt.spread", Confidence: 0.95, Comparable: true, Alt: true}
	}
	if strings.Contains(t, "ALTERNATE_TOTAL") && !strings.Contains(t, "TEAM_ALTERNATE") {
		return Classification{Family: model.FamilyTotal, Period: period, Stat: TotalStat(sport), Comparator: model.ComparatorOU, SubjectKind: "event", Rule: "fd.alt.total", Confidence: 0.95, Comparable: true, Alt: true}
	}
	if strings.HasPrefix(t, "TO_RECORD_") || strings.HasPrefix(t, "TO_SCORE_") {
		return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: "milestone", Comparator: model.ComparatorAtLeast, SubjectKind: "player", Rule: "fd.prop.milestone", Confidence: 0.7, Comparable: false, Alt: true}
	}
	if strings.Contains(t, "WIN_MARGIN") || strings.Contains(t, "WINNING_MARGIN") {
		return Classification{Family: model.FamilySpecial, Period: period, Comparator: model.ComparatorNone, SubjectKind: "event", Rule: "fd.special.margin", Confidence: 0.6, Comparable: false}
	}
	if stat, p, ok := fdPlayerTotal(t); ok {
		return Classification{Family: model.FamilyPlayerProp, Period: p, Stat: stat, Comparator: model.ComparatorOU, SubjectKind: "player", Rule: "fd.prop.player_total", Confidence: 0.85, Comparable: true, Alt: true}
	}
	if m := reRecordPlus.FindStringSubmatch(t); len(m) == 3 {
		return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: strings.ToLower(m[2]), Comparator: model.ComparatorAtLeast, SubjectKind: "player", Rule: "fd.prop.record_plus", Confidence: 0.8, Alt: true}
	}
	if strings.Contains(t, "X+_REGULAR_SEASON_WINS") {
		return Classification{Family: model.FamilyFutures, Period: model.PeriodSeason, Stat: "wins", Comparator: model.ComparatorAtLeast, SubjectKind: "team", Rule: "fd.futures.x_wins", Confidence: 0.85}
	}
	if strings.Contains(t, "CONSECUTIVE_WINS") {
		return Classification{Family: model.FamilyFutures, Period: model.PeriodSeason, Stat: "consecutive_wins", Comparator: model.ComparatorAtLeast, SubjectKind: "team", Rule: "fd.futures.consecutive_wins", Confidence: 0.8}
	}
	// Futures/season/awards/specials get a family so coverage is measurable,
	// but they are explicitly not cross-book comparable without a rule pack.
	for _, kw := range []string{"SEED", "PLACE", "PLAYOFF", "CHAMPION", "MVP", "AWARD", "DRAFT", "STANDING", "ELIMINATION", "CUP", "RECORD", "WINS", "DIVISION", "CONFERENCE", "PLAYOFFS", "TO MAKE", "MISS", "OF_THE_YEAR", "MOST_IMPROVED", "CLUTCH", "SIXTH_MAN", "ROOKIE", "COACH", "WINNER", "FINALS", "PLAY_IN", "MATCHUP", "TROPHY", "PRESIDENTS", "PICK"} {
		if strings.Contains(t, kw) {
			return Classification{Family: model.FamilyFutures, Period: model.PeriodSeason, Stat: strings.ToLower(kw), Comparator: model.ComparatorNone, SubjectKind: "team", Rule: "fd.futures.heuristic", Confidence: 0.45}
		}
	}
	for _, kw := range []string{"SPECIAL", "HIGHEST", "LOWEST", "CLOSEST", "BLOWOUT", "MOST_", "FIRST_TOUCHDOWN", "MATCH_SPECIALS"} {
		if strings.Contains(t, kw) {
			return Classification{Family: model.FamilySpecial, Period: period, Comparator: model.ComparatorNone, SubjectKind: "event", Rule: "fd.special.heuristic", Confidence: 0.5}
		}
	}
	for _, kw := range []string{"PLAYER", "SCORER", "PASSING", "RECEIVING", "RUSHING", "ASSIST", "REBOUND", "STRIKE", "HIT", "POINT", "GOAL", "SAVE", "SHOT"} {
		if strings.Contains(t, kw) {
			return Classification{Family: model.FamilyPlayerProp, Period: period, Stat: strings.ToLower(kw), Comparator: model.ComparatorNone, SubjectKind: "player", Rule: "fd.prop.heuristic", Confidence: 0.4}
		}
	}
	return base
}

// PeriodFromName extracts a period/segment from a market name. It is
// deliberately phrase-based so "1st Seed" or "First Touchdown" are not periods.
func PeriodFromName(name string) string {
	n := strings.ToLower(name)
	switch {
	case regexp.MustCompile(`\b(1st|first)\s+half\b`).MatchString(n):
		return model.PeriodH1
	case regexp.MustCompile(`\b(2nd|second)\s+half\b`).MatchString(n):
		return model.PeriodH2
	case regexp.MustCompile(`\b(1st|first)\s+quarter\b`).MatchString(n):
		return model.PeriodQ1
	case regexp.MustCompile(`\b(2nd|second)\s+quarter\b`).MatchString(n):
		return model.PeriodQ2
	case regexp.MustCompile(`\b(3rd|third)\s+quarter\b`).MatchString(n):
		return model.PeriodQ3
	case regexp.MustCompile(`\b(4th|fourth)\s+quarter\b`).MatchString(n):
		return model.PeriodQ4
	case regexp.MustCompile(`\b(1st|first)\s+period\b`).MatchString(n):
		return model.PeriodP1
	case regexp.MustCompile(`\b(2nd|second)\s+period\b`).MatchString(n):
		return model.PeriodP2
	case regexp.MustCompile(`\b(3rd|third)\s+period\b`).MatchString(n):
		return model.PeriodP3
	case regexp.MustCompile(`\b(1st|first)\s+inning\b`).MatchString(n):
		return model.PeriodInning
	case regexp.MustCompile(`\b(1st|first)\s+set\b`).MatchString(n):
		return model.PeriodSet
	case strings.Contains(n, "season"):
		return model.PeriodSeason
	}
	return model.PeriodFull
}

// TotalStat maps a sport to the unit used by its total market.
func TotalStat(sport string) string {
	switch strings.ToLower(sport) {
	case "nhl":
		return "goals"
	case "mlb":
		return "runs"
	case "soccer":
		return "goals"
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

// FuturesSubject extracts the subject from a futures market name. Rules are
// intentionally conservative; failures fall back to the runner name.
func FuturesSubject(marketName string) string {
	n := strings.TrimSpace(marketName)
	if i := strings.Index(n, " - "); i > 0 {
		return strings.TrimSpace(n[:i])
	}
	// 26-27 NBA Oklahoma City Thunder Regular Season Wins
	re := regexp.MustCompile(`(?i)^\d{2}-\d{2}\s+\w+\s+(.+?)\s+(Regular Season|To Finish|Seed|Make|Miss|Division|Conference|Stage|Stanley|NBA|NFL)`)
	if m := re.FindStringSubmatch(n); len(m) == 3 {
		return strings.TrimSpace(m[1])
	}
	return n
}
