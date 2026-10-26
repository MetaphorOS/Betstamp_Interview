package teams

import "testing"

func TestNFLAliases(t *testing.T) {
	cases := []string{"Kansas City Chiefs", "KC Chiefs", "Chiefs"}
	for _, name := range cases {
		team, ok := Resolve("nfl", name)
		if !ok || team.ID != "nfl-chiefs" {
			t.Fatalf("%q => %+v %v", name, team, ok)
		}
	}
}

func TestRefusesSoxCollision(t *testing.T) {
	for _, name := range []string{"Boston Red Sox", "Chicago White Sox", "New York Yankees"} {
		if team, ok := Resolve("mlb", name); ok {
			t.Fatalf("%q must not resolve, got %+v", name, team)
		}
		if team, ok := Resolve("nfl", name); ok {
			t.Fatalf("%q must not resolve as NFL, got %+v", name, team)
		}
	}
}

func TestRefusesUnknown(t *testing.T) {
	if team, ok := Resolve("nfl", "Springfield Atoms"); ok {
		t.Fatalf("unknown resolved: %+v", team)
	}
	if _, ok := Resolve("nfl", ""); ok {
		t.Fatal("empty resolved")
	}
}

func TestNBAWholeWords(t *testing.T) {
	cases := map[string]string{
		"Charlotte Hornets": "nba-hornets", "BKN Nets": "nba-nets", "Brooklyn Nets": "nba-nets",
		"LA Lakers": "nba-lakers", "LA Clippers": "nba-clippers", "Portland Trail Blazers": "nba-trail-blazers",
		"PHI 76ers": "nba-76ers", "GS Warriors": "nba-warriors", "Jazz": "nba-jazz",
		"Sacramento Kings": "nba-kings", "OKC Thunder": "nba-thunder",
	}
	for name, want := range cases {
		got, ok := Resolve("nba", name)
		if !ok || got.ID != want {
			t.Errorf("%q => %+v %v, want %s", name, got, ok, want)
		}
	}
	if _, ok := Resolve("nba", "Kansas City Chiefs"); ok {
		t.Error("NFL team resolved as NBA")
	}
	if got, ok := Resolve("nfl", "Sacramento Kings"); ok {
		t.Errorf("NBA team resolved as NFL: %+v", got)
	}
}
