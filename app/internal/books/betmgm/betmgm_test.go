package betmgm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

var t0 = time.Date(2026, 10, 27, 19, 45, 0, 0, time.UTC)

func testdata(name string) string { return filepath.Join("..", "..", "..", "testdata", name) }

func read(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(testdata(name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func quoteMap(s *book.Snapshot) map[string]model.Quote {
	out := map[string]model.Quote{}
	for _, q := range s.Quotes {
		out[q.BookGameID+"|"+model.IdentityKey(q)] = q
	}
	return out
}

// NBA fixtures are V1: one main market per family, teams by participantId,
// home/away from "Away at Home".
func TestNBAGameLines(t *testing.T) {
	snap, err := ParseFixtures(t0, DefaultLeagues[1], read(t, "mgm-nba-fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Games) != 4 || len(snap.Quotes) != 24 {
		t.Fatalf("games=%d quotes=%d skipped=%v, want 4 / 24", len(snap.Games), len(snap.Quotes), snap.Skipped)
	}
	g := snap.Games[0]
	if g.BookGameID != "20030815" || g.Home.ID != "nba-celtics" || g.Away.ID != "nba-nets" {
		t.Fatalf("game %+v", g)
	}
	qs := quoteMap(snap)
	ml := qs["20030815|"+model.MainIdentity(model.FamilyMoneyline, "nba-nets")]
	sp := qs["20030815|"+model.MainIdentity(model.FamilySpread, "nba-nets")]
	tot := qs["20030815|"+model.MainIdentity(model.FamilyTotal, "over")]
	if ml.Price.American != 450 || sp.Price.American != -110 || model.LineString(sp.Line) != "12.5" || model.LineString(tot.Line) != "222.5" {
		t.Fatalf("ml %+v spread %+v %s total %s", ml.Price, sp.Price, model.LineString(sp.Line), model.LineString(tot.Line))
	}
	if !strings.Contains(snap.Subscription, "v1|en-us|20030815|all") {
		t.Fatalf("subscription %s", snap.Subscription)
	}
}

func nflSnapshot(t *testing.T) *book.Snapshot {
	t.Helper()
	snap, err := ParseFixtures(t0, DefaultLeagues[0], read(t, "mgm-nfl-view.json"))
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// NFL fixtures are V2 and list every alternate spread and total; the main
// one is the most balanced. Player props match the research comparison.
func TestNFLViewMainLinesAndProps(t *testing.T) {
	snap := nflSnapshot(t)
	qs := quoteMap(snap)
	gb := "6:48482|"
	sp := qs[gb+model.MainIdentity(model.FamilySpread, "nfl-packers")]
	if sp.Line == nil || sp.Price.American == 0 {
		t.Fatalf("packers spread missing: %+v", sp)
	}
	fx := snap.Private.(*state).fixtures["6:48482"]
	if n := len(fx.groups[model.FamilySpread]); n < 10 {
		t.Fatalf("expected the alternate spreads in the group, got %d", n)
	}
	t.Logf("main spread %s %d, total %s", model.LineString(sp.Line), sp.Price.American, model.LineString(qs[gb+model.MainIdentity(model.FamilyTotal, "over")].Line))

	ou, rungs := 0, 0
	for _, q := range snap.Quotes {
		if q.Family != model.FamilyPlayer {
			continue
		}
		if q.Comparator == model.ComparatorOU {
			ou++
		} else {
			rungs++
		}
	}
	// Research: 262 BetMGM rows for this game (O/U pairs + milestone rungs).
	if ou/2+rungs != 262 {
		t.Fatalf("prop rows = %d (o/u %d, rungs %d), research has 262", ou/2+rungs, ou, rungs)
	}
	over := qs[gb+model.IdentityKey(model.PropQuote(model.Quote{}, "Christian Watson", model.StatRec, model.ComparatorOU, "over", 4.5))]
	under := qs[gb+model.IdentityKey(model.PropQuote(model.Quote{}, "Christian Watson", model.StatRec, model.ComparatorOU, "under", 4.5))]
	if over.Price.American != -125 || under.Price.American != -105 {
		t.Fatalf("watson rec 4.5 = %d / %d, research -125 / -105", over.Price.American, under.Price.American)
	}
	// "3+ TDs" is encoded DecimalValue 2.5.
	if _, ok := qs[gb+model.IdentityKey(model.PropQuote(model.Quote{}, "Christian Watson", model.StatTD, model.ComparatorAtLeast, "3", 3))]; !ok {
		t.Fatal("watson 3+ TDs missing")
	}
}

// replay feeds the captured push frames through the seed.
func TestReplayPush(t *testing.T) {
	nba, err := ParseFixtures(t0, DefaultLeagues[1], read(t, "mgm-nba-fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	st := nba.Private.(*state).clone()
	for id, fx := range nflSnapshot(t).Private.(*state).fixtures {
		st.fixtures[id] = fx.clone()
	}
	f, err := os.Open(testdata("mgm-ws.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	kinds := map[msgKind]int{}
	var ups []model.Update
	for sc.Scan() {
		res, err := st.apply(sc.Bytes(), t0)
		if err != nil {
			t.Fatal(err)
		}
		kinds[res.kind]++
		ups = append(ups, res.updates...)
	}
	t.Logf("kinds=%v updates=%d stats=%+v", kinds, len(ups), st.stats)
	if kinds[msgHandshake] != 1 || kinds[msgCompletion] != 1 || kinds[msgPing] == 0 || len(ups) == 0 {
		t.Fatalf("kinds=%v updates=%d", kinds, len(ups))
	}
	// The NBA total stays on the most balanced market, however many
	// alternates the topic streamed.
	fx := st.fixtures["20030815"]
	main := fx.chosen[model.FamilyTotal]
	for _, m := range fx.groups[model.FamilyTotal] {
		if better(m, main) {
			t.Fatalf("total main %s (%.2f) but %s is better (%.2f)", main.id, main.balance, m.id, m.balance)
		}
	}
	if len(fx.groups[model.FamilyTotal]) < 5 {
		t.Fatalf("alternate totals not collected: %d", len(fx.groups[model.FamilyTotal]))
	}
}

func TestSplitFrameAndControlMessages(t *testing.T) {
	msgs := splitFrame([]byte("{}\x1e{\"type\":6}\x1e{\"type\":3,\"invocationId\":\"1\",\"result\":null}\x1e"))
	if len(msgs) != 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	st := newState()
	want := []msgKind{msgHandshake, msgPing, msgCompletion}
	for i, m := range msgs {
		res, err := st.apply(m, t0)
		if err != nil || res.kind != want[i] {
			t.Fatalf("%s: kind %v err %v", m, res.kind, err)
		}
	}
	res, _ := st.apply([]byte(`{"type":7,"error":"bye"}`), t0)
	if res.kind != msgClose {
		t.Fatal("close not recognised")
	}
}

func push(msgType, payload string) []byte {
	return []byte(`{"type":1,"target":"Receive","arguments":[{"producer":"v2","messageType":"` + msgType + `","payload":` + payload + `,"topic":"x"},0]}`)
}

func TestMarketSuspensionAndMainRepick(t *testing.T) {
	snap := nflSnapshot(t)
	st := snap.Private.(*state).clone()
	fx := st.fixtures["6:48482"]
	main := fx.chosen[model.FamilySpread]

	// Market-level suspension wins over visible options.
	ups := apply(t, st, push("OptionMarketUpdate", `{"fixtureId":"6:48482","optionMarket":`+marketJSON(t, snap, main.id, "Suspended")+`}`))
	if fx.chosen[model.FamilySpread].id == main.id {
		// A suspended market loses to an open alternate.
		t.Fatal("suspended main kept over open alternates")
	}
	if len(ups) != 2 || ups[0].Status != model.StatusOpen {
		t.Fatalf("re-pick should emit the new open main: %+v", ups)
	}

	// Deleting the new main moves to the next one; deleting every spread
	// removes the slot.
	next := fx.chosen[model.FamilySpread].id
	ups = apply(t, st, push("OptionMarketDelete", `{"fixtureId":"6:48482","marketId":`+next+`}`))
	if len(ups) != 2 || fx.chosen[model.FamilySpread].id == next {
		t.Fatalf("delete of main: %+v", ups)
	}
	var last []model.Update
	for id := range fx.groups[model.FamilySpread] {
		last = apply(t, st, push("OptionMarketDelete", `{"fixtureId":"6:48482","marketId":`+id+`}`))
	}
	if len(last) != 2 || last[0].Status != model.StatusRemoved {
		t.Fatalf("last spread delete: %+v", last)
	}
}

func TestMainToLive(t *testing.T) {
	st := nflSnapshot(t).Private.(*state).clone()
	if _, err := st.apply(push("MainToLiveUpdate", `{"switchedFixtures":[{"preMatchId":"0","inPlayId":"20053779"}]}`), t0); err != nil {
		t.Fatalf("someone else's fixture: %v", err)
	}
	_, err := st.apply(push("MainToLiveUpdate", `{"switchedFixtures":[{"preMatchId":"6:48482","inPlayId":"6:99999"}]}`), t0)
	if !errors.Is(err, book.ErrResync) {
		t.Fatalf("our fixture went live: %v", err)
	}
}

func apply(t *testing.T, st *state, raw []byte) []model.Update {
	t.Helper()
	res, err := st.apply(raw, t0)
	if err != nil {
		t.Fatal(err)
	}
	return res.updates
}

// marketJSON re-serializes one market of the NFL fixture with a new status.
func marketJSON(t *testing.T, snap *book.Snapshot, id, status string) string {
	t.Helper()
	raw := string(read(t, "mgm-nfl-view.json"))
	i := strings.Index(raw, `{"id":`+id+`,`)
	if i < 0 {
		t.Fatalf("market %s not in fixture", id)
	}
	depth, j := 0, i
	for ; j < len(raw); j++ {
		switch raw[j] {
		case '{':
			depth++
		case '}':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	m := raw[i : j+1]
	return strings.Replace(m, `"status":"Visible"`, `"status":"`+status+`"`, 1)
}

// BookTS is the push server's send time: the whole-second timestamp plus
// latencyMs.
func TestPushSendTime(t *testing.T) {
	st := nflSnapshot(t).Private.(*state).clone()
	fx := st.fixtures["6:48482"]
	main := fx.chosen[model.FamilySpread]
	snap := nflSnapshot(t)
	raw := []byte(`{"type":1,"target":"Receive","arguments":[{"producer":"v2","messageType":"OptionMarketUpdate","timestamp":"2026-10-27T19:45:33Z","latencyMs":412,"payload":{"fixtureId":"6:48482","optionMarket":` +
		marketJSON(t, snap, main.id, "Visible") + `},"topic":"x"},0]}`)
	recv := time.Date(2026, 10, 27, 19, 45, 33, 450e6, time.UTC)
	res, err := st.apply(raw, recv)
	if err != nil || len(res.updates) == 0 {
		t.Fatalf("updates %v err %v", res.updates, err)
	}
	want := time.Date(2026, 10, 27, 19, 45, 33, 412e6, time.UTC)
	for _, u := range res.updates {
		if !u.BookTS.Equal(want) {
			t.Fatalf("BookTS = %s, want %s", u.BookTS, want)
		}
	}
	// A send time well after receipt is not trusted.
	res, _ = st.apply([]byte(strings.Replace(string(raw), `"latencyMs":412`, `"latencyMs":5000`, 1)), recv)
	for _, u := range res.updates {
		if !u.BookTS.IsZero() {
			t.Fatalf("implausible send time kept: %s", u.BookTS)
		}
	}
}

// A refused fixture read costs only that game's props: the snapshot keeps
// the listed game lines, and a later refusal reuses the last good read.
func TestRefusedFixtureRead(t *testing.T) {
	view := read(t, "mgm-nfl-view.json")
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cds-api/bettingoffer/fixtures":
			// The listing: the same fixture with its game lines only.
			var v struct{ Fixture mgmFixture }
			json.Unmarshal(view, &v)
			var lines []mgmMarket
			for _, m := range v.Fixture.OptionMarkets {
				if m.FixtureParticipantID == "" {
					lines = append(lines, m)
				}
			}
			v.Fixture.OptionMarkets = lines
			json.NewEncoder(w).Encode(map[string]any{"fixtures": []mgmFixture{v.Fixture}, "totalCount": 1})
		case "/cds-api/bettingoffer/fixture-view":
			if refuse {
				http.Error(w, "<html>blocked</html>", http.StatusForbidden)
				return
			}
			w.Write(view)
		}
	}))
	defer srv.Close()
	b, _ := New(Config{Base: srv.URL, Leagues: DefaultLeagues[:1], ViewGap: time.Millisecond, RetryWait: time.Millisecond})
	props := func(s *book.Snapshot) (n int) {
		for _, q := range s.Quotes {
			if q.Family == model.FamilyPlayer {
				n++
			}
		}
		return n
	}
	refuse = true
	snap, err := b.Snapshot(context.Background())
	if err != nil || props(snap) != 0 || len(snap.Quotes) == 0 || snap.Skipped["props_unavailable"] != 1 {
		t.Fatalf("refused, no cache: err=%v props=%d quotes=%d skipped=%v", err, props(snap), len(snap.Quotes), snap.Skipped)
	}
	refuse = false
	if snap, err = b.Snapshot(context.Background()); err != nil || props(snap) == 0 {
		t.Fatalf("allowed: err=%v props=%d", err, props(snap))
	}
	want := props(snap)
	refuse = true
	if snap, err = b.Snapshot(context.Background()); err != nil || props(snap) != want || snap.Skipped["props_cached"] != 1 {
		t.Fatalf("refused, cached: err=%v props=%d want %d skipped=%v", err, props(snap), want, snap.Skipped)
	}
}
