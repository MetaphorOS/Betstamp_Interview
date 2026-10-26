package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"betstampodds/internal/board"
	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// fakeSink records what the supervisor produced.
type fakeSink struct {
	mu        sync.Mutex
	snapshots map[string]int
	applies   map[string]int
	states    map[string][]board.HealthState
}

func newSink() *fakeSink {
	return &fakeSink{snapshots: map[string]int{}, applies: map[string]int{}, states: map[string][]board.HealthState{}}
}

func (s *fakeSink) ApplySnapshot(_ context.Context, name string, _ *book.Snapshot) error {
	s.mu.Lock()
	s.snapshots[name]++
	s.mu.Unlock()
	return nil
}
func (s *fakeSink) Apply(_ context.Context, u model.Update) error {
	s.mu.Lock()
	s.applies[u.Book]++
	s.mu.Unlock()
	return nil
}
func (s *fakeSink) SetHealth(_ context.Context, name string, h board.Health) error {
	s.mu.Lock()
	st := s.states[name]
	if len(st) == 0 || st[len(st)-1] != h.State {
		s.states[name] = append(st, h.State)
	}
	s.mu.Unlock()
	return nil
}
func (s *fakeSink) get(fn func(*fakeSink) int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s)
}
func (s *fakeSink) history(name string) []board.HealthState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]board.HealthState(nil), s.states[name]...)
}

// fakeBook runs one scripted plan per Stream call; the last plan repeats.
type fakeBook struct {
	name      string
	plans     []func(ctx context.Context, em book.Emitter) error
	runs      atomic.Int32
	snapshots atomic.Int32
	polls     atomic.Int32
	snapErr   atomic.Bool
}

type pollingBook struct{ *fakeBook }

func (p pollingBook) Poll(ctx context.Context, _ *book.Snapshot) ([]model.Update, error) {
	p.polls.Add(1)
	return []model.Update{{Book: p.name, Source: model.SourcePoll}}, nil
}

func (f *fakeBook) Descriptor() book.Descriptor {
	return book.Descriptor{Name: f.name, PriceKind: book.PriceAmerican}
}
func (f *fakeBook) Snapshot(ctx context.Context) (*book.Snapshot, error) {
	f.snapshots.Add(1)
	if f.snapErr.Load() {
		return nil, errors.New("snapshot down")
	}
	return &book.Snapshot{AsOf: time.Now(), Subscription: "s"}, nil
}
func (f *fakeBook) Stream(ctx context.Context, _ *book.Snapshot, em book.Emitter) error {
	i := int(f.runs.Add(1)) - 1
	if i >= len(f.plans) {
		i = len(f.plans) - 1
	}
	return f.plans[i](ctx, em)
}

// liveUntilCancel connects, emits, heartbeats, and runs until cancelled.
func liveUntilCancel(name string) func(ctx context.Context, em book.Emitter) error {
	return func(ctx context.Context, em book.Emitter) error {
		em.Connected()
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				em.Heartbeat()
				if err := em.Emit(ctx, model.Update{Book: name, Source: model.SourceStream}); err != nil {
					return err
				}
			}
		}
	}
}

func liveFor(name string, d time.Duration, err error) func(ctx context.Context, em book.Emitter) error {
	return func(ctx context.Context, em book.Emitter) error {
		c, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		_ = liveUntilCancel(name)(c, em)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}

func testConfig() Config {
	return Config{
		Tick: 10 * time.Millisecond, BackoffMin: 20 * time.Millisecond, BackoffMax: 40 * time.Millisecond,
		ReconcileEvery: time.Hour, HeartbeatTimeout: time.Second, StaleAfter: time.Second,
		ConnectReconcile: 10 * time.Millisecond, PollEvery: 20 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func run(t *testing.T, b book.Book, sink Sink, cfg Config) (*Supervisor, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := New(b, sink, cfg)
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	return s, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("supervisor did not stop within 2s")
		}
	}
}

func TestReconnectResnapshotsAndHealthRecovers(t *testing.T) {
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{
		liveFor("a", 50*time.Millisecond, errors.New("socket closed")),
		liveUntilCancel("a"),
	}}
	sink := newSink()
	_, stop := run(t, fb, sink, testConfig())
	defer stop()
	eventually(t, "second stream run", func() bool { return fb.runs.Load() >= 2 })
	eventually(t, "live again", func() bool {
		h := sink.history("a")
		return len(h) > 0 && h[len(h)-1] == board.HealthLive
	})
	if fb.snapshots.Load() < 2 || sink.get(func(s *fakeSink) int { return s.snapshots["a"] }) < 2 {
		t.Fatalf("snapshots = %d, want a fresh one per reconnect", fb.snapshots.Load())
	}
	h := sink.history("a")
	want := []board.HealthState{board.HealthLive, board.HealthDegraded, board.HealthLive}
	if !containsSeq(h, want) {
		t.Fatalf("health history %v does not contain %v", h, want)
	}
}

func containsSeq(h, want []board.HealthState) bool {
	j := 0
	for _, s := range h {
		if j < len(want) && s == want[j] {
			j++
		}
	}
	return j == len(want)
}

func TestResyncResnapshotsImmediately(t *testing.T) {
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{
		func(ctx context.Context, em book.Emitter) error { em.Connected(); return book.ErrResync },
		liveUntilCancel("a"),
	}}
	cfg := testConfig()
	cfg.BackoffMin, cfg.BackoffMax = 10*time.Second, 10*time.Second // a backoff would stall the test
	s, stop := run(t, fb, newSink(), cfg)
	defer stop()
	eventually(t, "resnapshot after resync", func() bool { return fb.snapshots.Load() >= 2 && fb.runs.Load() >= 2 })
	eventually(t, "resync counted", func() bool { return s.Health().Resyncs == 1 })
}

func TestHeartbeatTimeoutCancelsStream(t *testing.T) {
	cancelled := make(chan struct{}, 4)
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{
		func(ctx context.Context, em book.Emitter) error {
			em.Connected() // and then silence
			<-ctx.Done()
			cancelled <- struct{}{}
			return ctx.Err()
		},
		liveUntilCancel("a"),
	}}
	cfg := testConfig()
	cfg.HeartbeatTimeout = 100 * time.Millisecond
	_, stop := run(t, fb, newSink(), cfg)
	defer stop()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("silent stream was not cancelled")
	}
	eventually(t, "reconnect after timeout", func() bool { return fb.runs.Load() >= 2 })
}

func TestPollFallbackWhileStreamDown(t *testing.T) {
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{
		func(ctx context.Context, em book.Emitter) error { return errors.New("dial refused") },
	}}
	cfg := testConfig()
	cfg.BackoffMin, cfg.BackoffMax = 300*time.Millisecond, 300*time.Millisecond
	sink := newSink()
	s, stop := run(t, pollingBook{fb}, sink, cfg)
	defer stop()
	eventually(t, "polls while down", func() bool { return fb.polls.Load() >= 3 })
	if sink.get(func(s *fakeSink) int { return s.applies["a"] }) < 3 {
		t.Fatal("poll updates not applied")
	}
	eventually(t, "degraded", func() bool { return s.Health().State == board.HealthDegraded })
}

func TestPanicInOneBookDoesNotStopAnother(t *testing.T) {
	bad := &fakeBook{name: "bad", plans: []func(context.Context, book.Emitter) error{
		func(ctx context.Context, em book.Emitter) error { panic("boom") },
	}}
	good := &fakeBook{name: "good", plans: []func(context.Context, book.Emitter) error{liveUntilCancel("good")}}
	sink := newSink()
	_, stopBad := run(t, bad, sink, testConfig())
	defer stopBad()
	_, stopGood := run(t, good, sink, testConfig())
	defer stopGood()
	eventually(t, "bad book restarts after panics", func() bool { return bad.runs.Load() >= 3 })
	before := sink.get(func(s *fakeSink) int { return s.applies["good"] })
	eventually(t, "good book keeps updating", func() bool {
		return sink.get(func(s *fakeSink) int { return s.applies["good"] }) > before+5
	})
}

func TestSnapshotFailureIsDown(t *testing.T) {
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{liveUntilCancel("a")}}
	fb.snapErr.Store(true)
	cfg := testConfig()
	s, stop := run(t, fb, newSink(), cfg)
	defer stop()
	eventually(t, "down", func() bool { return s.Health().State == board.HealthDown })
	fb.snapErr.Store(false)
	eventually(t, "live after recovery", func() bool { return s.Health().State == board.HealthLive })
}

func TestKillStreamRecovers(t *testing.T) {
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{liveUntilCancel("a")}}
	s, stop := run(t, fb, newSink(), testConfig())
	defer stop()
	eventually(t, "live", func() bool { return s.Health().State == board.HealthLive })
	s.KillStream()
	eventually(t, "reconnected", func() bool { return fb.runs.Load() >= 2 && s.Health().State == board.HealthLive })
	if s.Health().Reconnects != 1 {
		t.Fatalf("reconnects = %d", s.Health().Reconnects)
	}
}

func TestDropReusesSnapshotWithinRateLimit(t *testing.T) {
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{
		liveFor("a", 30*time.Millisecond, errors.New("socket closed")),
		liveUntilCancel("a"),
	}}
	cfg := testConfig()
	cfg.MinSnapshotGap = time.Hour
	s, stop := run(t, fb, newSink(), cfg)
	defer stop()
	eventually(t, "reconnect", func() bool { return fb.runs.Load() >= 2 && s.Health().State == board.HealthLive })
	if n := fb.snapshots.Load(); n != 1 {
		t.Fatalf("snapshots = %d; a socket drop inside the rate limit must reuse the seed", n)
	}
}

func TestResyncPollsWhileRateLimited(t *testing.T) {
	fb := &fakeBook{name: "a", plans: []func(context.Context, book.Emitter) error{
		func(ctx context.Context, em book.Emitter) error { em.Connected(); return book.ErrResync },
		liveUntilCancel("a"),
	}}
	cfg := testConfig()
	cfg.MinSnapshotGap = 300 * time.Millisecond
	_, stop := run(t, pollingBook{fb}, newSink(), cfg)
	defer stop()
	eventually(t, "fresh snapshot after the gap", func() bool { return fb.snapshots.Load() >= 2 })
	if fb.polls.Load() == 0 {
		t.Fatal("no polls while waiting out the snapshot rate limit")
	}
}
