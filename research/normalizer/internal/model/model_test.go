package model

import (
	"testing"
	"time"
)

func TestScopeEventIgnoresHomeAwayOrder(t *testing.T) {
	start := time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)
	a := ScopeEvent("nfl", start, "nfl-buccaneers", "nfl-panthers")
	b := ScopeEvent("nfl", start, "nfl-panthers", "nfl-buccaneers")
	if a != b {
		t.Fatalf("scope depends on side order: %q vs %q", a, b)
	}
}

func TestIdentityKeyIgnoresLineOnly(t *testing.T) {
	line := 3.0
	q1 := Quote{
		Sport: "nfl", ScopeKey: "event|nfl|2026-10-25|nfl-a@nfl-b",
		Family: FamilySpread, Period: PeriodFull, Subject: "event",
		Comparator: ComparatorNone, Outcome: "nfl-a", Line: &line,
	}
	line2 := 3.5
	q2 := q1
	q2.Line = &line2
	if QuoteKey(q1) == QuoteKey(q2) {
		t.Fatal("quote keys must include the line")
	}
	if IdentityKey(q1) != IdentityKey(q2) {
		t.Fatal("identity keys must ignore the line")
	}
}

func TestLineString(t *testing.T) {
	v := -3.0
	if got := LineString(&v); got != "-3" {
		t.Fatalf("LineString = %q", got)
	}
	v = 44.5
	if got := LineString(&v); got != "44.5" {
		t.Fatalf("LineString = %q", got)
	}
	if got := LineString(nil); got != "" {
		t.Fatalf("nil line = %q", got)
	}
}

func TestRecomputePreservesDangling(t *testing.T) {
	b := BookData{Counts: Counts{DanglingSelections: 7}}
	b.Recompute()
	if b.Counts.DanglingSelections != 7 {
		t.Fatalf("dangling lost: %d", b.Counts.DanglingSelections)
	}
}
