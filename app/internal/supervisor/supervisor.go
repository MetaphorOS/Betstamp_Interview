// Package supervisor runs one goroutine tree per book: snapshot, stream,
// reconnect with backoff, heartbeat watchdog, reconcile and poll fallback,
// and health. All of that is written once here; book packages only fetch,
// parse and map.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"betstampodds/internal/board"
	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// Sink receives everything a supervisor produces. *board.Board implements it.
type Sink interface {
	ApplySnapshot(ctx context.Context, name string, snap *book.Snapshot) error
	Apply(ctx context.Context, u model.Update) error
	SetHealth(ctx context.Context, name string, h board.Health) error
}

// Config holds defaults; a book's Tuning overrides the timing fields.
type Config struct {
	ReconcileEvery   time.Duration // default 30s
	ResnapshotEvery  time.Duration // default 0 (off)
	MinSnapshotGap   time.Duration
	MinPollGap       time.Duration
	HeartbeatTimeout time.Duration // default 90s
	PollEvery        time.Duration // fallback poll while the stream is down; default 5s
	StaleAfter       time.Duration // default 30s
	BackoffMin       time.Duration // default 1s
	BackoffMax       time.Duration // default 30s
	BackoffReset     time.Duration // connected this long resets backoff; default 60s
	ConnectReconcile time.Duration // extra reconcile after Connected; default 2s
	Tick             time.Duration // watchdog/health tick; default 1s
	Logger           *slog.Logger
}

func (c Config) withDefaults(t book.Tuning) Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	over := func(v *time.Duration, d time.Duration) {
		if d != 0 {
			*v = d
		}
	}
	over(&c.ReconcileEvery, t.ReconcileEvery)
	over(&c.ResnapshotEvery, t.ResnapshotEvery)
	over(&c.MinSnapshotGap, t.MinSnapshotGap)
	over(&c.MinPollGap, t.MinPollGap)
	over(&c.HeartbeatTimeout, t.HeartbeatTimeout)
	def(&c.ReconcileEvery, 30*time.Second)
	def(&c.HeartbeatTimeout, 90*time.Second)
	def(&c.PollEvery, 5*time.Second)
	def(&c.StaleAfter, 30*time.Second)
	def(&c.BackoffMin, time.Second)
	def(&c.BackoffMax, 30*time.Second)
	def(&c.BackoffReset, 60*time.Second)
	def(&c.ConnectReconcile, 2*time.Second)
	def(&c.Tick, time.Second)
	c.PollEvery = max(c.PollEvery, c.MinPollGap)
	c.ReconcileEvery = max(c.ReconcileEvery, c.MinPollGap)
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Supervisor owns one book.
type Supervisor struct {
	b    book.Book
	name string
	cfg  Config
	sink Sink
	log  *slog.Logger

	mu             sync.Mutex
	health         board.Health
	connected      bool
	everConnected  bool
	snapFailing    bool
	lastSnapshotAt time.Time // last attempt (rate limiting)
	lastPollAt     time.Time // last attempt (rate limiting)
	snapOKAt       time.Time
	pollOKAt       time.Time
	snapshots      int64
	snapErrors     int64
	polls          int64
	pollErrors     int64
	lastPollErr    string
	rtts           []time.Duration // recent ping round trips to the book
	handshake      time.Duration   // last connect's app-level request -> reply
	handshakeAt    time.Time
	lastErr        string

	lastBeat atomic.Int64 // unix nanos
	frames   atomic.Int64
	updates  atomic.Int64
	lastData atomic.Int64

	kill        chan struct{}
	pausedUntil atomic.Int64   // unix nanos; demo/debug only
	seed        *book.Snapshot // last good snapshot; owned by the Run goroutine
}

// Pause takes the book offline for d: it kills the stream and suspends
// polls and snapshots, as if the book were unreachable. Debug/demo only; it
// is how the page's stale handling can be shown on demand.
func (s *Supervisor) Pause(d time.Duration) {
	s.pausedUntil.Store(time.Now().Add(d).UnixNano())
	s.KillStream()
}

func (s *Supervisor) paused() time.Duration {
	return time.Until(time.Unix(0, s.pausedUntil.Load()))
}

// New builds a supervisor for one book.
func New(b book.Book, sink Sink, cfg Config) *Supervisor {
	var t book.Tuning
	if tu, ok := b.(book.Tuner); ok {
		t = tu.Tuning()
	}
	cfg = cfg.withDefaults(t)
	name := b.Descriptor().Name
	return &Supervisor{
		b: b, name: name, cfg: cfg, sink: sink, log: cfg.Logger.With("book", name),
		health: board.Health{State: board.HealthStarting, Since: time.Now().UTC()},
		kill:   make(chan struct{}, 1),
	}
}

// KillStream cancels the current stream once, as if the connection failed.
func (s *Supervisor) KillStream() {
	select {
	case s.kill <- struct{}{}:
	default:
	}
}

// Health returns the current health.
func (s *Supervisor) Health() board.Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}

var (
	errHeartbeat = errors.New("supervisor: heartbeat timeout")
	errKilled    = errors.New("supervisor: stream killed")
)

// restart carries the fresh snapshot whose Subscription changed.
type restart struct{ snap *book.Snapshot }

func (r *restart) Error() string { return "supervisor: subscription changed" }

// Run supervises the book until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	s.log.Info("supervisor start")
	defer s.log.Info("supervisor stop")
	go s.healthLoop(ctx)

	backoff := s.cfg.BackoffMin
	var next *book.Snapshot
	needFresh := true // a resync needs a fresh snapshot; a socket drop does not
	for ctx.Err() == nil {
		if d := s.paused(); d > 0 {
			s.log.Info("paused", "for", d.Round(time.Second))
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
		}
		snap := next
		next = nil
		reused := false
		if snap == nil && !needFresh && s.seed != nil && !s.snapshotAllowed() {
			// The rate limit forbids a new snapshot yet: reconnect from the
			// last one. The post-connect reconcile covers the gap.
			s.log.Info("reconnecting from the last snapshot", "age", time.Since(s.seed.AsOf).Round(time.Second))
			snap, reused = s.seed, true
		}
		needFresh = false
		if snap == nil {
			var err error
			snap, err = s.snapshot(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.log.Warn("snapshot failed", "error", err, "backoff", backoff)
				s.wait(ctx, s.delay(backoff, err), s.seed)
				backoff = s.nextBackoff(backoff)
				needFresh = true
				continue
			}
		}
		s.seed = snap
		if err := s.sink.ApplySnapshot(ctx, s.name, snap); err != nil {
			return
		}
		if !reused {
			// A reused seed is old data: it must not make the book look fresh.
			s.markData()
		}

		connectedAt, err := s.runStream(ctx, snap)
		s.setConnected(false)
		if ctx.Err() != nil {
			return
		}
		var rs *restart
		switch {
		case errors.As(err, &rs):
			s.log.Info("subscription changed, restarting stream")
			next = rs.snap
			continue
		case errors.Is(err, book.ErrResync):
			s.bump(func(h *board.Health) { h.Resyncs++ })
			s.log.Warn("stream resync", "error", err)
			needFresh = true
			continue
		}
		s.mu.Lock()
		s.lastErr = errString(err)
		s.mu.Unlock()
		s.bump(func(h *board.Health) { h.Reconnects++ })
		if !connectedAt.IsZero() && time.Since(connectedAt) >= s.cfg.BackoffReset {
			backoff = s.cfg.BackoffMin
		}
		s.log.Warn("stream ended", "error", err, "backoff", backoff)
		s.wait(ctx, s.delay(backoff, err), s.seed)
		backoff = s.nextBackoff(backoff)
	}
}

func (s *Supervisor) snapshotAllowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastSnapshotAt) >= s.cfg.MinSnapshotGap
}

func msOf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Supervisor) nextBackoff(d time.Duration) time.Duration {
	return min(d*2, s.cfg.BackoffMax)
}

// delay is full-jitter backoff, but never shorter than a Retry-After.
func (s *Supervisor) delay(backoff time.Duration, err error) time.Duration {
	d := time.Duration(rand.Int64N(int64(backoff) + 1))
	var ra interface{ RetryAfterDuration() time.Duration }
	if errors.As(err, &ra) {
		d = max(d, ra.RetryAfterDuration())
	}
	return d
}

// wait sleeps for d, polling every PollEvery when the book can poll, so
// prices stay fresh while the stream is down.
func (s *Supervisor) wait(ctx context.Context, d time.Duration, seed *book.Snapshot) {
	// Polls made during the wait share its deadline: a slow or hung REST call
	// can never stretch a backoff (and so delay a reconnect) past d.
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	p, canPoll := s.b.(book.Poller)
	if !canPoll || seed == nil {
		<-wctx.Done()
		return
	}
	s.poll(wctx, p, seed)
	tick := time.NewTicker(s.cfg.PollEvery)
	defer tick.Stop()
	for {
		select {
		case <-wctx.Done():
			return
		case <-tick.C:
			s.poll(wctx, p, seed)
		}
	}
}

// snapshot waits out MinSnapshotGap (polling meanwhile), then takes one.
// Only the Run goroutine calls it: it reads the Run-owned seed.
func (s *Supervisor) snapshot(ctx context.Context) (*book.Snapshot, error) {
	s.mu.Lock()
	gap := s.cfg.MinSnapshotGap - time.Since(s.lastSnapshotAt)
	s.mu.Unlock()
	if gap > 0 {
		// Keep prices fresh by polling while the rate limit holds us back.
		s.wait(ctx, gap, s.seed)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return s.snapshotNow(ctx)
}

// snapshotNow takes a snapshot immediately and recovers panics. Background
// jobs call it after checking the rate limit themselves.
func (s *Supervisor) snapshotNow(ctx context.Context) (snap *book.Snapshot, err error) {
	s.mu.Lock()
	s.lastSnapshotAt = time.Now()
	s.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in %s Snapshot: %v", s.name, r)
			s.log.Error("book panic", "where", "snapshot", "panic", r, "stack", string(debug.Stack()))
		}
		s.mu.Lock()
		s.snapFailing = err != nil
		if err != nil {
			s.lastErr = err.Error()
			s.snapErrors++
		} else {
			s.snapshots++
			s.snapOKAt = time.Now().UTC()
		}
		s.mu.Unlock()
		s.evaluate(ctx)
	}()
	snap, err = s.b.Snapshot(ctx)
	if err == nil && snap == nil {
		err = errors.New("nil snapshot")
	}
	return snap, err
}

func (s *Supervisor) poll(ctx context.Context, p book.Poller, seed *book.Snapshot) {
	if s.paused() > 0 {
		return
	}
	s.mu.Lock()
	if time.Since(s.lastPollAt) < s.cfg.MinPollGap {
		s.mu.Unlock()
		return
	}
	s.lastPollAt = time.Now()
	s.mu.Unlock()
	ups, err := func() (ups []model.Update, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in %s Poll: %v", s.name, r)
				s.log.Error("book panic", "where", "poll", "panic", r)
			}
		}()
		return p.Poll(ctx, seed)
	}()
	if errors.Is(err, book.ErrSkipped) {
		return // the book chose not to fetch: neither data nor a failure
	}
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("poll failed", "error", err)
			s.mu.Lock()
			s.lastErr = err.Error()
			s.lastPollErr = err.Error()
			s.pollErrors++
			s.mu.Unlock()
		}
		s.evaluate(ctx)
		return
	}
	s.mu.Lock()
	s.polls++
	s.pollOKAt = time.Now().UTC()
	s.lastPollErr = ""
	s.mu.Unlock()
	for _, u := range ups {
		if err := s.sink.Apply(ctx, u); err != nil {
			return
		}
	}
	s.updates.Add(int64(len(ups)))
	s.markData()
	s.evaluate(ctx)
}

// emitter adapts a stream run to the sink.
type emitter struct {
	s         *Supervisor
	connected chan struct{}
	once      sync.Once
}

func (e *emitter) Emit(ctx context.Context, u model.Update) error {
	if err := e.s.sink.Apply(ctx, u); err != nil {
		return err
	}
	e.s.updates.Add(1)
	e.s.markData()
	return nil
}

func (e *emitter) Connected() { e.once.Do(func() { close(e.connected) }) }

func (e *emitter) Pong(rtt time.Duration) {
	e.s.mu.Lock()
	e.s.rtts = append(e.s.rtts, rtt)
	if len(e.s.rtts) > 20 {
		e.s.rtts = e.s.rtts[1:]
	}
	e.s.mu.Unlock()
}

func (e *emitter) Handshake(rtt time.Duration) {
	e.s.mu.Lock()
	e.s.handshake, e.s.handshakeAt = rtt, time.Now().UTC()
	e.s.mu.Unlock()
}

func (e *emitter) Heartbeat() {
	e.s.lastBeat.Store(time.Now().UnixNano())
	e.s.frames.Add(1)
}

func (s *Supervisor) markData() { s.lastData.Store(time.Now().UnixNano()) }

// runStream runs one Stream with its watchdog, reconcile and resnapshot
// tickers. It returns when the stream ends.
//
// REST work (reconcile polls, resnapshots) runs in its own goroutine, one job
// at a time, so this loop never waits on a slow request: the heartbeat
// watchdog, the kill switch and the stream ending are always handled
// promptly. Jobs use the stream's context, so stopping the stream also
// cancels an in-flight request, and the loop waits for a job before
// returning so two runs never overlap.
func (s *Supervisor) runStream(ctx context.Context, snap *book.Snapshot) (connectedAt time.Time, err error) {
	streamCtx, cancel := context.WithCancel(ctx)
	jobDone := make(chan jobResult, 1)
	jobRunning := false
	// cur is the newest good snapshot. A background resnapshot with the same
	// subscription advances it (and the reconnect seed) without a restart.
	cur := snap
	// A full refresh that comes due while another job runs waits here rather
	// than being lost: polls only update prices, so finding new games and
	// markets depends on these running.
	pendingFull := false
	defer func() {
		cancel()
		if jobRunning {
			<-jobDone
		}
	}()
	// startJob runs fn against cur in the background unless a job is
	// already running.
	startJob := func(what string, fn func(context.Context, *book.Snapshot) jobResult) bool {
		if jobRunning {
			return false
		}
		jobRunning = true
		seed := cur
		go func() {
			var res jobResult
			defer func() {
				if r := recover(); r != nil {
					s.log.Error("panic in background job", "job", what, "panic", r, "stack", string(debug.Stack()))
					res = jobResult{}
				}
				jobDone <- res
			}()
			res = fn(streamCtx, seed)
		}()
		return true
	}
	em := &emitter{s: s, connected: make(chan struct{})}
	s.lastBeat.Store(time.Now().UnixNano())
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("book panic", "where", "stream", "panic", r, "stack", string(debug.Stack()))
				done <- fmt.Errorf("panic in %s Stream: %v", s.name, r)
			}
		}()
		done <- s.b.Stream(streamCtx, snap, em)
	}()
	// stop cancels the stream and waits for it, so no two runs overlap.
	stop := func(reason error) error {
		cancel()
		<-done
		return reason
	}

	tick := time.NewTicker(s.cfg.Tick)
	defer tick.Stop()
	reconcile := time.NewTicker(s.cfg.ReconcileEvery)
	defer reconcile.Stop()
	var resnap <-chan time.Time
	if s.cfg.ResnapshotEvery > 0 {
		t := time.NewTicker(s.cfg.ResnapshotEvery)
		defer t.Stop()
		resnap = t.C
	}
	var afterConnect <-chan time.Time
	connected := em.connected
	for {
		select {
		case err := <-done:
			return connectedAt, err
		case <-s.kill:
			s.log.Info("stream killed on request")
			return connectedAt, stop(errKilled)
		case <-connected:
			connected = nil
			connectedAt = time.Now()
			s.setConnected(true)
			s.evaluate(ctx)
			s.log.Info("stream connected")
			// initialData is false on DK: nothing replays the gap between the
			// snapshot and the subscribe, so reconcile once shortly after.
			afterConnect = time.After(s.cfg.ConnectReconcile)
		case <-tick.C:
			last := time.Unix(0, s.lastBeat.Load())
			if time.Since(last) > s.cfg.HeartbeatTimeout {
				s.log.Warn("heartbeat timeout", "since", time.Since(last).Round(time.Millisecond))
				return connectedAt, stop(errHeartbeat)
			}
		case <-afterConnect:
			afterConnect = nil
			if !startJob("connect reconcile", s.reconcile) {
				// Another REST job is in flight; this one matters (it covers
				// the gap since the snapshot), so try again shortly.
				afterConnect = time.After(time.Second)
			}
		case <-reconcile.C:
			startJob("reconcile", s.reconcile) // skipped if one is still running
		case <-resnap:
			if !startJob("resnapshot", s.resnapshot) {
				pendingFull = true
			}
		case res := <-jobDone:
			jobRunning = false
			if res.restart {
				return connectedAt, stop(&restart{snap: res.snap})
			}
			if res.snap != nil {
				// Run owns the seed and is blocked in this call, so setting it
				// here is safe: the next reconnect uses the newest snapshot.
				cur, s.seed = res.snap, res.snap
			}
			if pendingFull {
				pendingFull = false
				startJob("resnapshot", s.resnapshot)
			}
		}
	}
}

// jobResult is what a background job hands back to the stream loop.
type jobResult struct {
	snap    *book.Snapshot // a newer good snapshot, or nil
	restart bool           // snap's subscription differs: restart the stream
}

// reconcile polls when it can, otherwise re-snapshots.
func (s *Supervisor) reconcile(ctx context.Context, cur *book.Snapshot) jobResult {
	if p, ok := s.b.(book.Poller); ok {
		s.poll(ctx, p, cur)
		return jobResult{}
	}
	return s.resnapshot(ctx, cur)
}

func (s *Supervisor) resnapshot(ctx context.Context, cur *book.Snapshot) jobResult {
	s.mu.Lock()
	tooSoon := time.Since(s.lastSnapshotAt) < s.cfg.MinSnapshotGap
	s.mu.Unlock()
	if tooSoon {
		return jobResult{}
	}
	snap, err := s.snapshotNow(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("reconcile snapshot failed", "error", err)
		}
		return jobResult{}
	}
	if snap.Subscription != cur.Subscription {
		return jobResult{snap: snap, restart: true}
	}
	if err := s.sink.ApplySnapshot(ctx, s.name, snap); err != nil {
		return jobResult{}
	}
	s.markData()
	return jobResult{snap: snap}
}

func (s *Supervisor) setConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	if v {
		s.everConnected = true
	}
	s.mu.Unlock()
}

func (s *Supervisor) bump(fn func(h *board.Health)) {
	s.mu.Lock()
	fn(&s.health)
	s.mu.Unlock()
	s.evaluate(context.Background())
}

func (s *Supervisor) healthLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.evaluate(ctx)
		}
	}
}

// evaluate derives the health state and publishes it.
//
//	live:     stream connected with a recent heartbeat
//	degraded: stream down but data (polls, snapshots) arriving
//	stale:    not live and no successful data for StaleAfter
//	down:     snapshots failing and no recent data
func (s *Supervisor) evaluate(ctx context.Context) {
	now := time.Now().UTC()
	lastData := time.Unix(0, s.lastData.Load()).UTC()
	recent := s.lastData.Load() != 0 && now.Sub(lastData) <= s.cfg.StaleAfter
	s.mu.Lock()
	var st board.HealthState
	switch {
	case s.connected:
		st = board.HealthLive
	case !s.everConnected && s.health.Reconnects == 0 && !s.snapFailing:
		st = board.HealthStarting
	case recent:
		st = board.HealthDegraded
	case s.snapFailing:
		st = board.HealthDown
	default:
		st = board.HealthStale
	}
	h := s.health
	if st != h.State {
		s.log.Info("health", "from", h.State, "to", st)
		h.State, h.Since = st, now
	}
	if s.lastData.Load() != 0 {
		h.LastDataAt = lastData
	}
	if s.connected {
		if h.ConnectedSince.IsZero() {
			h.ConnectedSince = now
		}
	} else {
		h.ConnectedSince = time.Time{}
	}
	h.Frames, h.Updates = s.frames.Load(), s.updates.Load()
	h.LastError = s.lastErr
	if b := s.lastBeat.Load(); b != 0 && s.everConnected {
		h.LastHeartbeatAt = time.Unix(0, b).UTC()
	}
	h.Polls, h.PollErrors, h.LastPollAt, h.LastPollError = s.polls, s.pollErrors, s.pollOKAt, s.lastPollErr
	h.Snapshots, h.SnapshotErrors, h.LastSnapshotAt = s.snapshots, s.snapErrors, s.snapOKAt
	h.HandshakeMs, h.HandshakeAt = msOf(s.handshake), s.handshakeAt
	h.PingLastMs, h.PingP50Ms = 0, 0
	if n := len(s.rtts); n > 0 {
		sorted := slices.Clone(s.rtts)
		slices.Sort(sorted)
		h.PingLastMs = msOf(s.rtts[n-1])
		h.PingP50Ms = msOf(sorted[n/2])
	}
	s.health = h
	s.mu.Unlock()
	_ = s.sink.SetHealth(ctx, s.name, h)
}
