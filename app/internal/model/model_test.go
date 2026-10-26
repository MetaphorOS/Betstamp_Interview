package model

import (
	"testing"
	"time"
)

func baseQuote() Quote {
	line := 49.5
	return Quote{
		Book: "book-a", BookGameID: "g1", GameID: "canonical",
		Sport: "nfl", League: "nfl", Family: FamilySpread,
		Period: PeriodFull, Subject: "event", Comparator: ComparatorNone,
		Outcome: "home", Line: &line,
	}
}

func TestMainAndAlternateKeysDoNotCollide(t *testing.T) {
	q := baseQuote()
	alt := q
	alt.Alt = true
	if IdentityKey(q) == IdentityKey(alt) {
		t.Fatal("main and alternate identities collided")
	}
	if QuoteKey(q) == QuoteKey(alt) {
		t.Fatal("main and alternate quote keys collided")
	}
}

func TestIdentityExcludesLineAndQuoteKeyIncludesIt(t *testing.T) {
	q := baseQuote()
	other := q
	v := 50.5
	other.Line = &v
	if IdentityKey(q) != IdentityKey(other) {
		t.Fatal("identity must ignore line")
	}
	if QuoteKey(q) == QuoteKey(other) {
		t.Fatal("quote key must include line")
	}
}

func TestGameKeyIsOrderIndependent(t *testing.T) {
	start := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)
	a := GameKey("nfl", start, "nfl-away", "nfl-home")
	b := GameKey("nfl", start, "nfl-home", "nfl-away")
	if a != b {
		t.Fatalf("game keys differ: %q %q", a, b)
	}
}

func TestUpdateCarriesLineMove(t *testing.T) {
	open := 3.5
	moved := 3.0
	first := Update{Book: "book-a", GameRef: "dk-game", Identity: "spread|full|event||none|main|home", Line: &open, American: -110, Status: StatusOpen}
	second := first
	second.Line = &moved
	second.American = -105
	if first.Identity != second.Identity {
		t.Fatal("line move changed identity")
	}
	if LineString(first.Line) == LineString(second.Line) {
		t.Fatal("line move was not carried")
	}
	if second.GameRef != "dk-game" {
		t.Fatalf("GameRef = %q", second.GameRef)
	}
}

func TestBoardSlotKeyRefusesMissingGame(t *testing.T) {
	q := baseQuote()
	q.BookGameID = "book-game-must-not-be-used"
	if _, err := BoardSlotKey("", q); err == nil {
		t.Fatal("expected missing canonical game error")
	}
	key, err := BoardSlotKey("canonical", q)
	if err != nil || key == "" {
		t.Fatalf("slot = %q, %v", key, err)
	}
	slot, err := slotKey("", q)
	if err == nil || slot != "" {
		t.Fatalf("slotKey fallback must fail, got %q %v", slot, err)
	}
}
