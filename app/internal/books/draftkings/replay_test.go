package draftkings

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

func testdata(name string) string { return filepath.Join("..", "..", "..", "testdata", name) }

type replayFrame struct {
	At      time.Time       `json:"at"`
	Conn    int             `json:"conn"`
	Payload json.RawMessage `json:"payload"`
}

func loadFrames(t *testing.T, conn int) []replayFrame {
	t.Helper()
	f, err := os.Open(testdata("soak-dk-ws.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []replayFrame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var fr replayFrame
		if err := json.Unmarshal(sc.Bytes(), &fr); err != nil {
			t.Fatal(err)
		}
		if fr.Conn == conn {
			out = append(out, fr)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadSeed(t *testing.T, name string) (*book.Snapshot, *state) {
	t.Helper()
	raw, err := os.ReadFile(testdata(name))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseSnapshot(raw, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return snap, snap.Private.(*state).clone()
}

type replayResult struct {
	updates  []model.Update
	resyncs  []string
	frames   int
	byStatus map[model.Status]int
	st       *state
}

// replay drives the state machine with a fake clock set to each frame's
// capture time, calling expire before every frame, then flushes tombstones.
func replay(t *testing.T, seedFile string, conn int) replayResult {
	t.Helper()
	_, st := loadSeed(t, seedFile)
	frames := loadFrames(t, conn)
	r := replayResult{byStatus: map[model.Status]int{}, st: st}
	var last time.Time
	for _, fr := range frames {
		r.updates = append(r.updates, st.expire(fr.At)...)
		res, err := st.apply(fr.Payload, fr.At)
		if err != nil {
			if !errors.Is(err, book.ErrResync) {
				t.Fatalf("frame at %s: %v", fr.At, err)
			}
			r.resyncs = append(r.resyncs, fr.At.Format(time.RFC3339Nano)+" "+err.Error())
			continue
		}
		r.frames++
		r.updates = append(r.updates, res.updates...)
		last = fr.At
	}
	r.updates = append(r.updates, st.expire(last.Add(time.Minute))...)
	for _, u := range r.updates {
		r.byStatus[u.Status]++
	}
	return r
}

func checkLines(t *testing.T, ups []model.Update) {
	t.Helper()
	nil_ := 0
	for _, u := range ups {
		p, ok := model.ParseIdentity(u.Identity)
		if !ok {
			t.Fatalf("bad identity %q", u.Identity)
		}
		if p.Family != model.FamilyMoneyline && u.Line == nil {
			nil_++
		}
		if p.Family == model.FamilyMoneyline && u.Line != nil {
			t.Fatalf("moneyline with line: %+v", u)
		}
	}
	if nil_ > 0 {
		t.Fatalf("%d spread/total updates without a line", nil_)
	}
}

func TestReplayConn1(t *testing.T) {
	r := replay(t, "dk-bootstrap-0001.json", 1)
	t.Logf("conn1: frames=%d updates=%d byStatus=%v resyncs=%d stats=%+v", r.frames, len(r.updates), r.byStatus, len(r.resyncs), r.st.stats)
	for _, s := range r.resyncs {
		t.Log("resync:", s)
	}
	if len(r.resyncs) != 0 {
		t.Fatalf("conn1 resyncs = %d", len(r.resyncs))
	}
	checkLines(t, r.updates)
	inPlay := 0
	for _, u := range r.updates {
		if u.InPlay {
			inPlay++
		}
	}
	if inPlay == 0 {
		t.Fatal("expected in-play updates on conn1")
	}
}

func TestReplayConn2Kickoff(t *testing.T) {
	r := replay(t, "dk-bootstrap-0002.json", 2)
	t.Logf("conn2: frames=%d updates=%d byStatus=%v resyncs=%d stats=%+v", r.frames, len(r.updates), r.byStatus, len(r.resyncs), r.st.stats)
	for i, s := range r.resyncs {
		if i < 20 {
			t.Log("resync:", s)
		}
	}
	if len(r.resyncs) != 0 {
		t.Fatalf("conn2 resyncs = %d", len(r.resyncs))
	}
	checkLines(t, r.updates)

	// No Removed for a slot that later comes back (replaced or re-added).
	removedAt := map[string]int{}
	removedByGame := map[string]int{}
	for i, u := range r.updates {
		key := u.GameRef + "|" + u.Identity
		if u.Status == model.StatusRemoved {
			removedAt[key] = i
			removedByGame[u.GameRef]++
			continue
		}
		if j, ok := removedAt[key]; ok && j < i {
			t.Errorf("slot %s removed at update %d but back at %d", key, j, i)
		}
	}

	// Removed slots must belong to events DK pulled and never re-added.
	_, seed := loadSeed(t, "dk-bootstrap-0002.json")
	_ = seed
	removedEvents := map[string]bool{}
	for id, ev := range r.st.events {
		if ev.removed {
			removedEvents[id] = true
		}
	}
	var games []string
	total := 0
	for g, n := range removedByGame {
		games = append(games, g)
		total += n
		if !removedEvents[g] {
			t.Errorf("Removed for game %s that is still listed", g)
		}
	}
	sort.Strings(games)
	t.Logf("Removed slots=%d across %d events: %v", total, len(games), games)
	if total != 78 || len(games) != 13 {
		t.Errorf("expected 78 Removed slots across the 13 next-week events, got %d across %d", total, len(games))
	}

	// Every game that kicked off ends in-play.
	lastInPlay := map[string]bool{}
	everLive := map[string]bool{}
	for _, u := range r.updates {
		if u.Status == model.StatusRemoved {
			continue
		}
		lastInPlay[u.GameRef] = u.InPlay
		if u.InPlay {
			everLive[u.GameRef] = true
		}
	}
	kicked := 0
	for g := range everLive {
		kicked++
		if !lastInPlay[g] {
			t.Errorf("game %s went live but its last update is not in-play", g)
		}
	}
	t.Logf("games in-play by the end: %d", kicked)
	if kicked < 7 {
		t.Errorf("expected the 7 kickoffs in the capture to be in-play, got %d", kicked)
	}
}
