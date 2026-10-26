package match

import (
	"testing"
	"time"

	"betstampodds/internal/model"
)

func TestMatchAcrossUTCMidnight(t *testing.T) {
	m := New()
	away := model.Team{ID: "nfl-chiefs", Name: "Kansas City Chiefs"}
	home := model.Team{ID: "nfl-bills", Name: "Buffalo Bills"}
	// Same game, two books publishing on either side of UTC midnight.
	a := model.BookGame{Book: "book-a", BookGameID: "dk1", Sport: "nfl", League: "nfl",
		Start: time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC), Away: away, Home: home}
	b := model.BookGame{Book: "book-b", BookGameID: "fd1", Sport: "nfl", League: "nfl",
		Start: time.Date(2026, 11, 2, 0, 30, 0, 0, time.UTC), Away: away, Home: home}
	pa, ok := m.Resolve(a, 3*time.Hour)
	if !ok {
		t.Fatal("first resolve failed")
	}
	pb, ok := m.Resolve(b, 3*time.Hour)
	if !ok {
		t.Fatal("second resolve failed")
	}
	if pa.Game.ID != pb.Game.ID {
		t.Fatalf("same game got different canonical IDs: %q %q", pa.Game.ID, pb.Game.ID)
	}
	if pb.Delta != time.Hour {
		t.Fatalf("delta = %v", pb.Delta)
	}
}
