package taxonomy

import "testing"

func TestPeriods(t *testing.T) {
	cases := map[string]string{
		"1st Half Spread": "h1",
		"2nd Half Total":  "h2",
		"1st Quarter":     "q1",
		"4th Quarter":     "q4",
		"1st Period":      "p1",
		"1st Inning":      "inning",
		"1st Set":         "set",
		"Regular Season":  "season",
		"1st Seed Winner": "full",
		"First Touchdown": "full",
	}
	for name, want := range cases {
		if got := PeriodFromName(name); got != want {
			t.Errorf("%q => %q, want %q", name, got, want)
		}
	}
}

func TestParsers(t *testing.T) {
	subject, line, ok := OverUnder("Over 44.5")
	if !ok || subject != "over" || line == nil || *line != 44.5 {
		t.Fatalf("OverUnder => %q %v %v", subject, line, ok)
	}
	subject, line, ok = AltYards("Michael Penix Jr. 150+ Yards")
	if !ok || subject != "Michael Penix Jr." || line == nil || *line != 150 {
		t.Fatalf("AltYards => %q %v %v", subject, line, ok)
	}
	subject, line, ok = PlusStat("Team 100+ Points")
	if !ok || subject != "Team" || line == nil || *line != 100 {
		t.Fatalf("PlusStat => %q %v %v", subject, line, ok)
	}
}

func TestSignedLine(t *testing.T) {
	v, ok := SignedLine("-3.5")
	if !ok || v == nil || *v != -3.5 {
		t.Fatalf("signed line => %v %v", v, ok)
	}
	if _, ok := SignedLine("3.5"); ok {
		t.Fatal("unsigned line should fail")
	}
}
