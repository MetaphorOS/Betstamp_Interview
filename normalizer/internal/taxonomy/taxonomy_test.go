package taxonomy

import (
	"testing"

	"sportsbook-normalizer/internal/model"
)

func TestClassifyMain(t *testing.T) {
	c := ClassifyFD("nfl", "MONEY_LINE", "Moneyline")
	if c.Family != model.FamilyMoneyline || !c.Comparable {
		t.Fatalf("moneyline = %+v", c)
	}
	c = ClassifyFD("nfl", "TOTAL_POINTS_(OVER/UNDER)", "Total Points")
	if c.Family != model.FamilyTotal || c.Stat != "points" || !c.Comparable {
		t.Fatalf("total = %+v", c)
	}
	c = ClassifyFD("nhl", "TOTAL_POINTS_(OVER/UNDER)", "Total Goals")
	if c.Stat != "goals" {
		t.Fatalf("nhl total = %+v", c)
	}
}

func TestClassifyAlt(t *testing.T) {
	c := ClassifyFD("nfl", "ANY_TIME_TOUCHDOWN_SCORER", "Any Time Touchdown Scorer")
	if c.Family != model.FamilyPlayerProp || c.Comparator != model.ComparatorYesNo || c.Comparable {
		t.Fatalf("td = %+v", c)
	}
	c = ClassifyFD("nfl", "PLAYER_X_ALT_PASSING_YARDS_HIGH", "Brock Purdy - Alt Passing Yds")
	if c.Family != model.FamilyPlayerProp || c.Stat != "passing_yards" || !c.Alt || c.Comparable {
		t.Fatalf("alt yards = %+v", c)
	}
	c = ClassifyFD("nfl", "REGULAR_SEASON_WINS_SGP", "Arizona Cardinals - Regular Season Wins 2026-27")
	if c.Family != model.FamilyFutures || c.Stat != "wins" {
		t.Fatalf("futures = %+v", c)
	}
	c = ClassifyFD("nba", "NBA_DEFENSIVE_PLAYER_OF_THE_YEAR.", "26-27 NBA Defensive Player of the Year")
	if c.Family != model.FamilyFutures {
		t.Fatalf("award must be futures, got %+v", c)
	}
	c = ClassifyFD("nfl", "SOMETHING_NEW", "Something New")
	if c.Family != model.FamilyUnknown || c.Comparable {
		t.Fatalf("unknown = %+v", c)
	}
}

func TestPeriodFromName(t *testing.T) {
	cases := map[string]string{
		"Moneyline":                  model.PeriodFull,
		"1st Half Moneyline":         model.PeriodH1,
		"2nd Quarter Spread":         model.PeriodQ2,
		"1st Period Total Goals":     model.PeriodP1,
		"1st Inning Total Runs":      model.PeriodInning,
		"26-27 NBA Eastern 1st Seed": model.PeriodFull,
		"First Touchdown Scorer":     model.PeriodFull,
	}
	for name, want := range cases {
		if got := PeriodFromName(name); got != want {
			t.Errorf("PeriodFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestNameParsers(t *testing.T) {
	player, line, ok := AltYards("Michael Penix Jr. 150+ Yards")
	if !ok || player != "Michael Penix Jr." || line == nil || *line != 150 {
		t.Fatalf("AltYards = %q %v %v", player, line, ok)
	}
	outcome, line, ok := OverUnder("Arizona Cardinals Over 3.5 Wins")
	if !ok || outcome != "over" || line == nil || *line != 3.5 {
		t.Fatalf("OverUnder = %q %v %v", outcome, line, ok)
	}
	outcome, line, ok = OverUnder("Under 44.5")
	if !ok || outcome != "under" || line == nil || *line != 44.5 {
		t.Fatalf("OverUnder short = %q %v %v", outcome, line, ok)
	}
	subject := FuturesSubject("Arizona Cardinals - Regular Season Wins 2026-27")
	if subject != "Arizona Cardinals" {
		t.Fatalf("FuturesSubject = %q", subject)
	}
	subject = FuturesSubject("26-27 NBA Oklahoma City Thunder Regular Season Wins")
	if subject != "Oklahoma City Thunder" {
		t.Fatalf("NBA FuturesSubject = %q", subject)
	}
}
