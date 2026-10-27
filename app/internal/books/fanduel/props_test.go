package fanduel

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"betstampodds/internal/model"
)

// propsSeed loads Panthers @ Packers props from MGA (fd-nfl-mga.json) and
// prices them with the SMP capture (fd-nfl-smp.json), as Snapshot does.
func propsSeed(t *testing.T) map[string]model.Update {
	t.Helper()
	snap, err := ParseBoards(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(testdata("fd-nfl-mga.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := model.BookGame{Book: Name, BookGameID: "36215311", Sport: "nfl", League: "nfl", Name: "Carolina Panthers @ Green Bay Packers"}
	if err := AddProps(snap, g, raw); err != nil {
		t.Fatal(err)
	}
	sd := snap.Private.(*seed)
	if len(sd.mainIDs) != 0 || len(sd.ids) == 0 || snap.Subscription == "" {
		t.Fatalf("props must join the subscription but not the poll: ids=%d main=%d", len(sd.ids), len(sd.mainIDs))
	}
	var markets []fdMarket
	rawSMP, err := os.ReadFile(testdata("fd-nfl-smp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawSMP, &markets); err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Update{}
	for i := range markets {
		if sd.markets[markets[i].MarketID] == nil {
			continue
		}
		ups, err := sd.mapMarket(&markets[i], model.SourceStream, time.Now(), true)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range ups {
			out[u.Identity+"|"+model.LineString(u.Line)] = u
		}
	}
	t.Logf("fd prop quotes=%d priced=%d skipped=%v", len(snap.Quotes), len(out), snap.Skipped)
	return out
}

func propUpdateKey(player, stat, cmp, outcome string, line float64) string {
	q := model.PropQuote(model.Quote{}, player, stat, cmp, outcome, line)
	return model.IdentityKey(q) + "|" + model.LineString(q.Line)
}

func TestNFLPropsFromMGAAndSMP(t *testing.T) {
	ups := propsSeed(t)
	// Research count for this game at FD: 370 rows (O/U pairs + rungs + TD),
	// of which 5 are team defenses on the TD lists, which are not players.
	ou, rungs := 0, 0
	for _, u := range ups {
		p, _ := model.ParseIdentity(u.Identity)
		if p.Comparator == model.ComparatorOU {
			ou++
		} else {
			rungs++
		}
	}
	if ou/2+rungs != 365 {
		t.Fatalf("rows = %d (o/u %d, rungs %d), research has 370 - 5 defenses", ou/2+rungs, ou, rungs)
	}
	// Research row: Christian Watson receptions 4.5, FD -106 / -125.
	o := ups[propUpdateKey("Christian Watson", model.StatRec, model.ComparatorOU, "over", 4.5)]
	u := ups[propUpdateKey("Christian Watson", model.StatRec, model.ComparatorOU, "under", 4.5)]
	if o.American != -106 || u.American != -125 {
		t.Fatalf("watson rec 4.5 = %d / %d", o.American, u.American)
	}
	// Ladder rungs keep their threshold although SMP sends handicap 0.
	for k, up := range ups {
		p, _ := model.ParseIdentity(up.Identity)
		if p.Comparator == model.ComparatorAtLeast && model.LineString(up.Line) != p.Outcome {
			t.Fatalf("%s: line %s, want %s", k, model.LineString(up.Line), p.Outcome)
		}
	}
}

func TestClassifyProp(t *testing.T) {
	cases := map[string]propClass{
		"PLAYER_X_RECEIVING_YARDS_HIGH":    {stat: model.StatRecYds, comparator: model.ComparatorOU},
		"PLAYER_AB_ALT_RECEPTIONS":         {stat: model.StatRec, comparator: model.ComparatorAtLeast, ladder: true},
		"PLAYER_B_PASSING_+_RUSHING_YARDS": {stat: model.StatPassRushYds, comparator: model.ComparatorOU},
		"TO_SCORE_2+_TOUCHDOWNS":           {stat: model.StatTD, comparator: model.ComparatorAtLeast, threshold: 2},
	}
	for mt, want := range cases {
		if got, ok := classifyProp(mt); !ok || got != want {
			t.Errorf("%s = %+v %v", mt, got, ok)
		}
	}
	for _, mt := range []string{"PLAYER_X_2ND_QUARTER_ALT_RECEIVING_YARDS", "PLAYER_X_LONGEST_RECEPTION", "MONEY_LINE"} {
		if _, ok := classifyProp(mt); ok {
			t.Errorf("%s must not classify", mt)
		}
	}
}

// A rung added to a prop ladder between snapshots is skipped, not a resync.
func TestNewPropRungIsNotAResync(t *testing.T) {
	snap, _ := ParseBoards(time.Now())
	raw, _ := os.ReadFile(testdata("fd-nfl-mga.json"))
	if err := AddProps(snap, model.BookGame{BookGameID: "36215311", Sport: "nfl", League: "nfl"}, raw); err != nil {
		t.Fatal(err)
	}
	sd := snap.Private.(*seed)
	var id string
	for mid, ms := range sd.markets {
		if ms.fixedLine {
			id = mid
			break
		}
	}
	m := fdMarket{MarketID: id, MarketStatus: "OPEN", RunnerDetails: []fdRunner{{SelectionID: "999999999"}}}
	if _, err := sd.mapMarket(&m, model.SourceStream, time.Now(), true); err != nil {
		t.Fatalf("new rung: %v", err)
	}
}
