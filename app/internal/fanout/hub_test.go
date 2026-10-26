package fanout

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"betstampodds/internal/board"
	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

var t0 = time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)

type rig struct {
	b      *board.Board
	hub    *Hub
	srv    *httptest.Server
	cancel func()
	n      int
}

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b := board.New([]book.Descriptor{{Name: "a", PriceKind: book.PriceAmerican}}, board.Options{})
	go b.Run(ctx)
	g := model.BookGame{Book: "a", BookGameID: "g", Sport: "nfl", Start: t0,
		Home: model.Team{ID: "nfl-vikings"}, Away: model.Team{ID: "nfl-colts"}}
	if err := b.ApplySnapshot(ctx, "a", &book.Snapshot{Games: []model.BookGame{g}}); err != nil {
		t.Fatal(err)
	}
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(b, cfg)
	go h.Run(ctx)
	srv := httptest.NewServer(h)
	r := &rig{b: b, hub: h, srv: srv}
	r.cancel = func() { srv.CloseClientConnections(); srv.Close(); cancel() }
	t.Cleanup(r.cancel)
	return r
}

// push applies n visible price changes.
func (r *rig) push(t *testing.T, n int) {
	t.Helper()
	for range n {
		r.n++
		u := model.Update{Book: "a", GameRef: "g", Identity: model.MainIdentity(model.FamilyMoneyline, "nfl-vikings"),
			American: 100 + r.n, Status: model.StatusOpen, RecvAt: time.Now(), Source: model.SourceStream}
		if err := r.b.Apply(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
}

type event struct {
	id    uint64
	event string
	data  string
}

// open connects and returns a channel of parsed events.
func (r *rig) open(t *testing.T, lastID string) (<-chan event, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	out := make(chan event, 4096)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		var ev event
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.event != "" {
					out <- ev
				}
				ev = event{}
			case strings.HasPrefix(line, "id: "):
				ev.id, _ = strconv.ParseUint(line[4:], 10, 64)
			case strings.HasPrefix(line, "event: "):
				ev.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			}
		}
	}()
	return out, cancel
}

func next(t *testing.T, ch <-chan event, kind string) event {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed waiting for %s", kind)
			}
			if ev.event == kind {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func TestSnapshotThenDeltasWithoutGaps(t *testing.T) {
	r := newRig(t, Config{})
	r.push(t, 5)
	ch, stop := r.open(t, "")
	defer stop()
	snap := next(t, ch, "snapshot")
	var st WireState
	if err := json.Unmarshal([]byte(snap.data), &st); err != nil {
		t.Fatal(err)
	}
	if st.Seq != snap.id || len(st.Slots) != 1 || st.ServerTime == 0 {
		t.Fatalf("snapshot seq=%d id=%d slots=%d", st.Seq, snap.id, len(st.Slots))
	}
	r.push(t, 50)
	want := snap.id + 1
	for range 50 {
		ev := next(t, ch, "delta")
		if ev.id != want {
			t.Fatalf("delta id %d, want %d", ev.id, want)
		}
		var d WireDelta
		if err := json.Unmarshal([]byte(ev.data), &d); err != nil || d.Seq != ev.id || d.Kind != board.KindSlot || d.Entry == nil {
			t.Fatalf("delta %s: %v", ev.data, err)
		}
		want++
	}
}

func TestResumeFromRing(t *testing.T) {
	r := newRig(t, Config{})
	r.push(t, 3)
	ch, stop := r.open(t, "")
	snap := next(t, ch, "snapshot")
	r.push(t, 2)
	last := next(t, ch, "delta")
	last = next(t, ch, "delta")
	stop()
	r.push(t, 10) // missed while disconnected
	ch2, stop2 := r.open(t, strconv.FormatUint(last.id, 10))
	defer stop2()
	first := <-ch2
	if first.event != "delta" || first.id != last.id+1 {
		t.Fatalf("resume got %s id=%d, want delta %d (snapshot was %d)", first.event, first.id, last.id+1, snap.id)
	}
	if got := r.hub.Stats().Resumes; got != 1 {
		t.Fatalf("resumes = %d", got)
	}
}

func TestResumeTooOldGetsSnapshot(t *testing.T) {
	r := newRig(t, Config{RingSize: 8})
	r.push(t, 40)
	time.Sleep(50 * time.Millisecond) // let the hub ring roll over
	ch, stop := r.open(t, "1")
	defer stop()
	if ev := <-ch; ev.event != "snapshot" {
		t.Fatalf("got %s, want snapshot", ev.event)
	}
	if r.hub.Stats().Resyncs != 1 {
		t.Fatal("resync not counted")
	}
}

func TestSlowClientDroppedWithoutBlocking(t *testing.T) {
	r := newRig(t, Config{}) // default buffer for the real client; the slow one gets 4
	slow := &client{ch: make(chan []byte, 4)}
	r.hub.attach(slow, 0, false)
	fastCh, stop := r.open(t, "")
	defer stop()
	snap := next(t, fastCh, "snapshot")
	r.push(t, 100)
	for i := range 100 {
		if ev := next(t, fastCh, "delta"); ev.id != snap.id+uint64(i)+1 {
			t.Fatalf("fast client gap at %d", i)
		}
	}
	if !slow.dropped.Load() || r.hub.Stats().DroppedClients < 1 {
		t.Fatal("slow client not dropped")
	}
}

func TestPingCarriesServerTime(t *testing.T) {
	r := newRig(t, Config{PingEvery: 20 * time.Millisecond})
	ch, stop := r.open(t, "")
	defer stop()
	ev := next(t, ch, "ping")
	var p struct{ ServerTime int64 }
	if err := json.Unmarshal([]byte(ev.data), &p); err != nil || p.ServerTime == 0 {
		t.Fatalf("ping %q", ev.data)
	}
}
