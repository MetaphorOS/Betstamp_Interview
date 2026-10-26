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
