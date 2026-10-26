package identity

import (
	"testing"
	"time"

	"sportsbook-normalizer/internal/model"
)

func TestResolveTeamAliases(t *testing.T) {
	cases := []string{
		"TB Buccaneers", "Tampa Bay Buccaneers", "Buccaneers",
	}
	var first model.Team
	for i, name := range cases {
		team, ok := ResolveTeam("nfl", name)
		if !ok {
			t.Fatalf("resolve %q failed", name)
		}
		if i == 0 {
			first = team
		} else if team.ID != first.ID {
			t.Fatalf("%q = %q, want %q", name, team.ID, first.ID)
		}
	}
	for _, pair := range [][2]string{
		{"LA Rams", "Los Angeles Rams"},
		{"SF 49ers", "San Francisco 49ers"},
		{"NO Saints", "New Orleans Saints"},
	} {
		a, _ := ResolveTeam("nfl", pair[0])
		b, _ := ResolveTeam("nfl", pair[1])
		if a.ID == "" || a.ID != b.ID {
			t.Fatalf("%q (%q) != %q (%q)", pair[0], a.ID, pair[1], b.ID)
		}
	}
}

func TestResolveTeamMascotFallback(t *testing.T) {
	cases := [][3]string{
		{"nba", "LA Lakers", "Los Angeles Lakers"},
		{"nba", "POR Trail Blazers", "Portland Trail Blazers"},
		{"nhl", "TOR Maple Leafs", "Toronto Maple Leafs"},
		{"nhl", "FLA Panthers", "Florida Panthers"},
	}
	for _, c := range cases {
		a, okA := ResolveTeam(c[0], c[1])
		b, okB := ResolveTeam(c[0], c[2])
		if !okA || !okB || a.ID != b.ID {
			t.Fatalf("%s: %q (%q) vs %q (%q)", c[0], c[1], a.ID, c[2], b.ID)
		}
	}
}

func TestMatchEventsTimeTolerance(t *testing.T) {
	start := time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)
	dk := []model.Event{{
		Book: "draftkings", BookEventID: "1", Sport: "nfl", Start: start,
		Away: model.Team{ID: "nfl-buccaneers"}, Home: model.Team{ID: "nfl-panthers"},
		Key: model.ScopeEvent("nfl", start, "nfl-buccaneers", "nfl-panthers"),
	}}
	fdStart := start.Add(5 * time.Minute)
	fd := []model.Event{{
		Book: "fanduel", BookEventID: "2", Sport: "nfl", Start: fdStart,
		Away: model.Team{ID: "nfl-buccaneers"}, Home: model.Team{ID: "nfl-panthers"},
		Key: model.ScopeEvent("nfl", fdStart, "nfl-buccaneers", "nfl-panthers"),
	}}
	pairs, ua, ub := MatchEvents(dk, fd, 15*time.Minute)
	if len(pairs) != 1 || len(ua) != 0 || len(ub) != 0 {
		t.Fatalf("pairs=%d ua=%d ub=%d", len(pairs), len(ua), len(ub))
	}
	_, ua, ub = MatchEvents(dk, fd, time.Minute)
	if len(ua) != 1 || len(ub) != 1 {
		t.Fatalf("tolerance should reject: ua=%d ub=%d", len(ua), len(ub))
	}
}
