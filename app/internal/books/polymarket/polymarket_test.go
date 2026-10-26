package polymarket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"betstampodds/internal/model"
)

func loadSeed(t *testing.T) (*seed, []model.Quote, map[string]int, int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "pm-nfl-events.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseEvents(raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return snap.Private.(*seed), snap.Quotes, snap.Skipped, len(snap.Games)
}

func TestSnapshotMainsOnly(t *testing.T) {
	sd, quotes, skipped, games := loadSeed(t)
	t.Logf("games=%d quotes=%d tokens=%d skipped=%v", games, len(quotes), len(sd.ids), skipped)
	if games == 0 || len(quotes) == 0 {
		t.Fatal("no games")
	}
	perGame := map[string]map[string]int{}
	for _, q := range quotes {
		if q.Alt || q.Period != model.PeriodFull {
			t.Fatalf("non-main quote %+v", q)
		}
		if q.Family != model.FamilyMoneyline && q.Line == nil {
			t.Fatalf("missing line %+v", q)
		}
		if perGame[q.BookGameID] == nil {
			perGame[q.BookGameID] = map[string]int{}
		}
		perGame[q.BookGameID][model.IdentityKey(q)]++
	}
	for g, ids := range perGame {
		for id, n := range ids {
			if n != 1 {
				t.Fatalf("game %s identity %s has %d quotes", g, id, n)
			}
		}
	}
}

func TestSpreadSidesMirror(t *testing.T) {
	_, quotes, _, _ := loadSeed(t)
	byMarket := map[string][]model.Quote{}
	for _, q := range quotes {
		if q.Family == model.FamilySpread {
			byMarket[q.BookMarketID] = append(byMarket[q.BookMarketID], q)
		}
	}
	for id, qs := range byMarket {
		if len(qs) != 2 || *qs[0].Line != -*qs[1].Line || qs[0].Outcome == qs[1].Outcome {
			t.Fatalf("market %s sides %+v", id, qs)
		}
	}
}

func TestStreamMessages(t *testing.T) {
	sd, _, _, _ := loadSeed(t)
	tok := sd.ids[0]
	frames := []any{
		map[string]any{"event_type": "book", "asset_id": tok, "timestamp": "1792966298031",
			"bids": []any{map[string]any{"price": "0.01"}, map[string]any{"price": "0.55"}},
			"asks": []any{map[string]any{"price": "0.99"}, map[string]any{"price": "0.57"}}},
		[]any{map[string]any{"event_type": "price_change", "timestamp": "1792966298032",
			"price_changes": []any{map[string]any{"asset_id": tok, "best_bid": "0.60", "best_ask": "0.62"}}}},
	}
	var got []model.Update
	for _, f := range frames {
		raw, _ := json.Marshal(f)
		ups, err := sd.apply(raw, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ups...)
	}
	if len(got) != 2 {
		t.Fatalf("updates = %+v", got)
	}
	// priced at the ask (cost to buy): 0.57 -> -133; 0.62 -> -163
	if got[0].American != -133 || got[1].American != -163 || !got[0].BookTS.IsZero() || got[1].BookTS.IsZero() {
		t.Fatalf("prices %+v", got)
	}
	if ups, err := sd.apply([]byte("PONG"), time.Now()); err != nil || ups != nil {
		t.Fatal("PONG must be ignored")
	}
}

func TestConvert(t *testing.T) {
	for _, c := range []struct {
		p  float64
		am int
	}{{0.5, 100}, {0.25, 300}, {0.8, -400}, {0.0001, 0}} {
		if am, _ := convert(c.p); am != c.am {
			t.Fatalf("convert(%v) = %d, want %d", c.p, am, c.am)
		}
	}
}

func TestNBASeries(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "pm-nba-events.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseSeriesEvents(raw, "nba", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nba games=%d quotes=%d skipped=%v", len(snap.Games), len(snap.Quotes), snap.Skipped)
	if len(snap.Games) == 0 {
		t.Fatal("no NBA games")
	}
	for _, g := range snap.Games {
		if g.Sport != "nba" || !strings.HasPrefix(g.Home.ID, "nba-") || !strings.HasPrefix(g.Away.ID, "nba-") {
			t.Fatalf("bad game %+v", g)
		}
	}
}
