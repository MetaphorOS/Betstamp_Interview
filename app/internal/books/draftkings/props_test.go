package draftkings

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"betstampodds/internal/model"
)

// propsSnapshot parses dk-nfl-props.json: Panthers @ Packers game lines plus
// every prop subcategory (trim-props.cjs).
func propsSnapshot(t *testing.T) (*stateSnapshot, map[string]model.Quote) {
	t.Helper()
	raw, err := os.ReadFile(testdata("dk-nfl-props.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Main  json.RawMessage   `json:"main"`
		Props []json.RawMessage `json:"props"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	props := make([][]byte, len(f.Props))
	for i, p := range f.Props {
		props[i] = p
	}
	snap, err := ParseLeagueSnapshot(f.Main, DefaultLeagues[0], t0, props...)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]model.Quote{}
	for _, q := range snap.Quotes {
		byKey[model.QuoteKey(q)] = q
	}
	return &stateSnapshot{st: snap.Private.(*state), partials: len(snap.Private.(*state).partials), games: len(snap.Games), skipped: snap.Skipped}, byKey
}

type stateSnapshot struct {
	st       *state
	partials int
	games    int
	skipped  map[string]int
}

func propKey(player, stat, cmp, outcome string, line float64) string {
	return model.QuoteKey(model.PropQuote(model.Quote{}, player, stat, cmp, outcome, line))
}

func TestNFLPropsSnapshot(t *testing.T) {
	s, quotes := propsSnapshot(t)
	if s.games != 1 {
		t.Fatalf("games = %d", s.games)
	}
	// One partial for the game lines and one per prop subcategory.
	if s.partials != 1+len(NFLProps) {
		t.Fatalf("partials = %d, want %d", s.partials, 1+len(NFLProps))
	}
	counts := map[string]int{}
	for _, q := range quotes {
		if q.Family == model.FamilyPlayer {
			counts[q.Comparator]++
		}
	}
	t.Logf("o/u=%d atleast=%d skipped=%v", counts[model.ComparatorOU], counts[model.ComparatorAtLeast], s.skipped)
	if counts[model.ComparatorOU] == 0 || counts[model.ComparatorAtLeast] < 200 {
		t.Fatalf("prop counts %v", counts)
	}
	// Research row: Christian Watson receptions 4.5, DK -116 / -110.
	over, ok := quotes[propKey("Christian Watson", model.StatRec, model.ComparatorOU, "over", 4.5)]
	under, ok2 := quotes[propKey("Christian Watson", model.StatRec, model.ComparatorOU, "under", 4.5)]
	if !ok || !ok2 || over.Price.American != -116 || under.Price.American != -110 {
		t.Fatalf("watson rec 4.5: %+v %+v", over.Price, under.Price)
	}
	if _, ok := quotes[propKey("Josh Jacobs", model.StatTD, model.ComparatorAtLeast, "1", 1)]; !ok {
		t.Fatal("Josh Jacobs anytime TD missing")
	}
	for _, q := range quotes {
		if q.Family == model.FamilyPlayer && (strings.Contains(q.Subject, "d-st") || strings.Contains(q.Subject, "defense")) {
			t.Fatalf("team defense mapped as a player: %s", q.Subject)
		}
	}
}

// A milestone rung added by the stream builds its own identity from the
// selection; a price change on it then emits on that identity.
func TestStreamPropMilestone(t *testing.T) {
	s, _ := propsSnapshot(t)
	st := s.st.clone()
	var market string
	for id, ms := range st.markets {
		if ms.family == model.FamilyPlayer && ms.comparator == model.ComparatorAtLeast && ms.stat == model.StatRecYds {
			market = id
			break
		}
	}
	if market == "" {
		t.Fatal("no receiving-yards milestone market in the seed")
	}
	frame := func(kind string, sel map[string]any) []model.Update {
		raw, _ := json.Marshal(map[string]any{"event": "update", "data": map[string]any{"data": map[string]any{kind: map[string]any{"selections": []any{sel}}}}})
		res, err := st.apply(raw, t0)
		if err != nil {
			t.Fatal(err)
		}
		return res.updates
	}
	ups := frame("add", map[string]any{
		"id": "new-rung", "marketId": market, "label": "200+", "milestoneValue": 200,
		"displayOdds":  map[string]any{"american": "+2500"},
		"participants": []any{map[string]any{"name": "Test Player", "type": "Player"}},
	})
	want := model.IdentityKey(model.PropQuote(model.Quote{}, "Test Player", model.StatRecYds, model.ComparatorAtLeast, "200", 200))
	if len(ups) != 1 || ups[0].Identity != want || ups[0].American != 2500 || model.LineString(ups[0].Line) != "200" {
		t.Fatalf("add: %+v", ups)
	}
	ups = frame("change", map[string]any{"id": "new-rung", "displayOdds": map[string]any{"american": "+2200"}})
	if len(ups) != 1 || ups[0].Identity != want || ups[0].American != 2200 {
		t.Fatalf("change: %+v", ups)
	}
}
