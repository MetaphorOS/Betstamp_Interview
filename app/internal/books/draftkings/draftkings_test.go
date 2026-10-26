package draftkings

import (
	"os"
	"strings"
	"testing"
	"time"

	"betstampodds/internal/model"
)

func TestParseAmericanUnicodeMinus(t *testing.T) {
	v, err := parseAmerican("\u2212110")
	if err != nil || v != -110 {
		t.Fatalf("american = %d, %v", v, err)
	}
	v, err = parseAmerican("EVEN")
	if err != nil || v != 100 {
		t.Fatalf("EVEN = %d, %v", v, err)
	}
}

func TestSnapshotUsesTrueOddsAndLiveState(t *testing.T) {
	trueOdds := 1.91
	doc := dkDoc{
		Events: []dkEvent{{
			ID: "e1", Name: "Kansas City Chiefs @ Buffalo Bills",
			StartEventDate: "2026-11-01T18:00:00Z", Status: "STARTED",
			Participants: []dkParticipant{{Name: "Buffalo Bills", VenueRole: "home"}, {Name: "Kansas City Chiefs", VenueRole: "away"}},
		}},
		Markets: []dkMarket{{
			ID: "m1", EventID: "e1", Name: "Moneyline", MarketType: dkMarketType{Name: "Moneyline"},
		}},
		Selections: []dkSelection{{
			ID: "s1", MarketID: "m1", Label: "Chiefs", OutcomeType: "Away",
			DisplayOdds: &dkOdds{American: "\u2212110"},
			TrueOdds:    &trueOdds, Status: "Suspended",
		}},
	}
	snap, err := buildSnapshot(time.Now(), leagueDoc{league: DefaultLeagues[0], doc: doc})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Quotes) != 1 {
		t.Fatalf("quotes = %d", len(snap.Quotes))
	}
	q := snap.Quotes[0]
	if q.Price.American != -110 || q.Price.Decimal != 1.91 {
		t.Fatalf("price = %+v", q.Price)
	}
	if !q.Price.InPlay {
		t.Fatal("STARTED event must set InPlay")
	}
	if q.Price.Status != model.StatusSuspended {
		t.Fatalf("status = %s", q.Price.Status)
	}
	if _, ok := snap.Private.(*state).sels[q.BookSelectionKey]; !ok {
		t.Fatal("seed missing selection")
	}
}

func TestNBASnapshot(t *testing.T) {
	raw, err := os.ReadFile(testdata("dk-nba.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseLeagueSnapshot(raw, League{Sport: "nba", ID: "42648"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nba games=%d quotes=%d skipped=%v", len(snap.Games), len(snap.Quotes), snap.Skipped)
	if len(snap.Games) != 12 || len(snap.Quotes) != 72 {
		t.Fatalf("games=%d quotes=%d, research capture has 12 / 72", len(snap.Games), len(snap.Quotes))
	}
	for _, q := range snap.Quotes {
		if q.Sport != "nba" || !strings.HasPrefix(q.Outcome, "nba-") && q.Family != model.FamilyTotal {
			t.Fatalf("bad quote %+v", q)
		}
	}
	if !strings.Contains(snap.Subscription, "42648") {
		t.Fatalf("subscription %s", snap.Subscription)
	}
}
