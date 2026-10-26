package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"sportsbookfeed/internal/clock"
	"sportsbookfeed/internal/fd"
)

// fdRunnerView is the comparable subset of a FanDuel runner.
type fdRunnerView struct {
	SelectionID string   `json:"selectionId"`
	Status      string   `json:"status"`
	Line        *float64 `json:"line,omitempty"`
	American    *float64 `json:"american,omitempty"`
	Decimal     *float64 `json:"decimal,omitempty"`
}

// fdMarketView is the comparable subset of a FanDuel market.
type fdMarketView struct {
	MarketID string                  `json:"marketId"`
	Status   string                  `json:"status"`
	InPlay   bool                    `json:"inplay"`
	Runners  map[string]fdRunnerView `json:"runners"`
}

// fdState is the latest pushed state per market.
type fdState struct {
	mu      sync.RWMutex
	markets map[string]fdMarketView
}

func newFDState() *fdState {
	return &fdState{markets: map[string]fdMarketView{}}
}

func (s *fdState) update(m *fd.Market) {
	view := viewOfMarket(m)
	s.mu.Lock()
	s.markets[m.MarketID] = view
	s.mu.Unlock()
}

func (s *fdState) replace(markets []fd.Market) {
	next := make(map[string]fdMarketView, len(markets))
	for i := range markets {
		next[markets[i].MarketID] = viewOfMarket(&markets[i])
	}
	s.mu.Lock()
	s.markets = next
	s.mu.Unlock()
}

func (s *fdState) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.markets)
}

func (s *fdState) Dump() map[string]fdMarketView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]fdMarketView, len(s.markets))
	for id, v := range s.markets {
		runners := make(map[string]fdRunnerView, len(v.Runners))
		for rid, r := range v.Runners {
			runners[rid] = r
		}
		v.Runners = runners
		out[id] = v
	}
	return out
}

func viewOfMarket(m *fd.Market) fdMarketView {
	view := fdMarketView{
		MarketID: m.MarketID,
		Status:   m.MarketStatus,
		InPlay:   m.IsInPlay(),
		Runners:  map[string]fdRunnerView{},
	}
	for _, r := range m.AllRunners() {
		view.Runners[r.SelectionID.String()] = fdRunnerView{
			SelectionID: r.SelectionID.String(),
			Status:      r.RunnerStatus,
			Line:        copyFloat(r.Handicap),
			American:    copyFloat(r.WinRunnerOdds.American()),
			Decimal:     copyFloat(r.WinRunnerOdds.Decimal()),
		}
	}
	return view
}

func copyFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// fdSoak keeps the AppSync subscription alive while polling SMP and refreshing
// discovery for reconciliation.
type fdSoak struct {
	cfg     soakConfig
	dir     *runDir
	events  *eventLog
	metrics *bookMetrics
	state   *fdState
	started time.Time

	wsLog        *jsonlFile
	reconcileLog *jsonlFile
	discoveryLog *jsonlFile

	mu          sync.Mutex
	ids         []string
	bundle      *fd.Bundle
	resubscribe chan struct{}
}

func newFDSoak(cfg soakConfig, rd *runDir, events *eventLog, started time.Time) (*fdSoak, error) {
	wsLog, err := openJSONL(rd.path("fd/ws.jsonl"))
	if err != nil {
		return nil, err
	}
	recLog, err := openJSONL(rd.path("fd/reconcile.jsonl"))
	if err != nil {
		wsLog.Close()
		return nil, err
	}
	discLog, err := openJSONL(rd.path("fd/discovery.jsonl"))
	if err != nil {
		wsLog.Close()
		recLog.Close()
		return nil, err
	}
	bundle, err := fd.LoadBundle(defaultBundlePath())
	if err != nil {
		wsLog.Close()
		recLog.Close()
		discLog.Close()
		return nil, err
	}
	return &fdSoak{
		cfg:          cfg,
		dir:          rd,
		events:       events,
		metrics:      newBookMetrics("fanduel"),
		state:        newFDState(),
		started:      started,
		wsLog:        wsLog,
		reconcileLog: recLog,
		discoveryLog: discLog,
		bundle:       bundle,
		resubscribe:  make(chan struct{}, 1),
	}, nil
}

func (s *fdSoak) run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.wsLoop(ctx) }()
	go func() { defer wg.Done(); s.restLoop(ctx) }()
	go func() { defer wg.Done(); s.progressLoop(ctx) }()
	wg.Wait()
	_ = s.wsLog.Close()
	_ = s.reconcileLog.Close()
	_ = s.discoveryLog.Close()
}

func (s *fdSoak) wsLoop(ctx context.Context) {
	backoff := time.Second
	for connNo := 1; ; connNo++ {
		if ctx.Err() != nil {
			return
		}
		ids, fresh, err := s.bootstrap(ctx, connNo)
		if err != nil {
			s.events.log("fanduel", "bootstrap_error", true, map[string]any{"attempt": connNo, "error": err.Error()})
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		bundle, err := s.currentBundle()
		if err != nil {
			s.events.log("fanduel", "bundle_error", true, map[string]any{"attempt": connNo, "error": err.Error()})
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		auth := bundle.Auth()
		connectURL, err := fd.RealtimeURL(auth)
		if err != nil {
			s.events.log("fanduel", "realtime_url_error", true, map[string]any{"error": err.Error()})
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		rt, err := fd.DialRealtime(ctx, connectURL, auth, 15*time.Second)
		if err != nil {
			s.events.log("fanduel", "ws_connect_error", true, map[string]any{"attempt": connNo, "error": err.Error()})
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		s.metrics.connected()
		connectedAt := time.Now()
		s.events.log("fanduel", "ws_connected", true, map[string]any{
			"attempt":        connNo,
			"markets":        len(ids),
			"freshBootstrap": fresh,
		})

		// Drain any stale resubscribe request before arming this connection.
		select {
		case <-s.resubscribe:
		default:
		}

		// Baseline for reconciliation before the first push arrives.
		s.resync(ctx, connNo, ids)

		stop := make(chan struct{})
		var lastRead, lastPong atomic.Int64
		lastRead.Store(time.Now().UnixNano())
		lastPong.Store(time.Now().UnixNano())
		rt.OnPong(func() {
			lastPong.Store(time.Now().UnixNano())
			s.metrics.pong()
		})
		go s.pingLoop(rt, stop)
		go s.watchdog(rt, stop, &lastRead, &lastPong, connNo)
		if s.cfg.ReconnectEvery > 0 {
			go func() {
				timer := time.NewTimer(s.cfg.ReconnectEvery)
				defer timer.Stop()
				select {
				case <-timer.C:
					s.events.log("fanduel", "forced_reconnect", true, map[string]any{"attempt": connNo})
					_ = rt.Close()
				case <-stop:
				}
			}()
		}
		go func() {
			select {
			case <-ctx.Done():
				_ = rt.Close()
			case <-stop:
			}
		}()
		go func() {
			select {
			case <-ctx.Done():
			case <-s.resubscribe:
				s.events.log("fanduel", "resubscribe_requested", true, map[string]any{"attempt": connNo})
				_ = rt.Close()
			case <-stop:
			}
		}()

		init := map[string]any{"type": "connection_init"}
		_ = s.wsLog.Write(map[string]any{
			"at": clock.NowISO(), "monoMs": clock.MonoMs(), "conn": connNo,
			"kind": "sent", "payload": init,
		})
		if err := rt.SendJSON(init); err != nil {
			s.events.log("fanduel", "ws_init_error", true, map[string]any{"attempt": connNo, "error": err.Error()})
			close(stop)
			_ = rt.Close()
			s.metrics.disconnected()
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		var readErr error
		frames, dataFrames := 0, 0
		started := false
		startAck := false
		for {
			frame, err := rt.Read()
			if err != nil {
				readErr = err
				break
			}
			frames++
			lastRead.Store(time.Now().UnixNano())
			_ = s.wsLog.Write(map[string]any{
				"at": clock.NowISO(), "monoMs": clock.MonoMs(), "conn": connNo,
				"kind": "received", "payload": string(frame),
			})
			parsed, perr := fd.ParseFrame(frame)
			if perr != nil {
				s.metrics.frame(int64(len(frame)), false, false)
				continue
			}
			update := false
			switch parsed.Type {
			case "connection_ack":
				if !started {
					started = true
					msg, err := rt.StartMessage("soak-prices", bundle.SubscriptionQuery, ids)
					if err != nil {
						s.events.log("fanduel", "start_build_error", true, map[string]any{"error": err.Error()})
						_ = rt.Close()
						break
					}
					_ = s.wsLog.Write(map[string]any{
						"at": clock.NowISO(), "monoMs": clock.MonoMs(), "conn": connNo,
						"kind": "sent", "payload": msg,
					})
					if err := rt.SendJSON(msg); err != nil {
						s.events.log("fanduel", "start_send_error", true, map[string]any{"error": err.Error()})
						_ = rt.Close()
					}
				}
			case "start_ack":
				if !startAck {
					startAck = true
					s.events.log("fanduel", "subscription_ack", true, map[string]any{
						"attempt": connNo, "markets": len(ids),
					})
				}
			case "data":
				if market, err := parsed.Price(); err == nil {
					s.state.update(market)
					update = true
					dataFrames++
				}
			case "connection_error", "error":
				s.events.log("fanduel", "subscription_error", true, map[string]any{
					"attempt": connNo, "payload": string(parsed.Payload),
				})
				_ = rt.Close()
			}
			s.metrics.frame(int64(len(frame)), update, false)
		}

		close(stop)
		_ = rt.Close()
		s.metrics.disconnected()
		code, reason := closeReason(readErr)
		if ctx.Err() != nil {
			code, reason = 1000, "run complete"
		}
		s.events.log("fanduel", "ws_disconnected", true, map[string]any{
			"attempt": connNo, "code": code, "reason": reason,
			"connectedSeconds": time.Since(connectedAt).Seconds(),
			"frames":           frames, "dataFrames": dataFrames, "startAck": startAck,
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(connectedAt) > time.Minute {
			backoff = time.Second
		} else {
			backoff = nextBackoff(backoff)
		}
		if !sleepCtx(ctx, jitter(backoff)) {
			return
		}
	}
}

func (s *fdSoak) currentBundle() (*fd.Bundle, error) {
	b, err := fd.LoadBundle(defaultBundlePath())
	if err != nil {
		s.mu.Lock()
		cached := s.bundle
		s.mu.Unlock()
		if cached != nil {
			return cached, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.bundle = b
	s.mu.Unlock()
	return b, nil
}

func (s *fdSoak) bootstrap(ctx context.Context, connNo int) ([]string, bool, error) {
	res, err := fd.Discover(ctx, fd.BoardOptions{})
	if err != nil {
		s.mu.Lock()
		cached := append([]string(nil), s.ids...)
		s.mu.Unlock()
		if len(cached) == 0 {
			return nil, false, err
		}
		s.events.log("fanduel", "bootstrap_fallback", true, map[string]any{
			"attempt": connNo, "error": err.Error(), "markets": len(cached),
		})
		return cached, false, nil
	}
	s.metrics.rest(res.ElapsedMs)
	ids := mainMarketIDs(res.Board, s.cfg.FDMarketLimit)
	if len(ids) == 0 {
		return nil, false, fmt.Errorf("no FanDuel main markets in discovery")
	}
	events := len(res.Board.Attachments.Events)
	markets := len(res.Board.Attachments.Markets)
	base, saveErr := saveREST(s.dir, "fd", "bootstrap", connNo, res.Raw, map[string]any{
		"status":       res.Status,
		"elapsedMs":    res.ElapsedMs,
		"bytes":        len(res.Raw),
		"cacheControl": res.CacheControl,
		"events":       events,
		"markets":      markets,
		"mainMarkets":  len(ids),
	})
	if saveErr != nil {
		s.events.log("fanduel", "bootstrap_save_error", true, map[string]any{"error": saveErr.Error()})
	} else {
		s.events.log("fanduel", "bootstrap", false, map[string]any{"file": base, "markets": len(ids)})
	}
	s.mu.Lock()
	s.ids = ids
	s.mu.Unlock()
	s.metrics.subscribed(len(ids))
	return ids, true, nil
}

func (s *fdSoak) pingLoop(rt *fd.Realtime, stop <-chan struct{}) {
	ticker := time.NewTicker(s.cfg.PingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := rt.Ping(); err != nil {
				_ = rt.Close()
				return
			}
			s.metrics.ping()
		}
	}
}

func (s *fdSoak) watchdog(rt *fd.Realtime, stop <-chan struct{}, lastRead, lastPong *atomic.Int64, connNo int) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			last := lastRead.Load()
			if p := lastPong.Load(); p > last {
				last = p
			}
			if time.Since(time.Unix(0, last)) > s.cfg.StaleAfter {
				s.events.log("fanduel", "watchdog_stale", true, map[string]any{
					"attempt": connNo, "idleFor": time.Since(time.Unix(0, last)).String(),
				})
				_ = rt.Close()
				return
			}
		}
	}
}

func (s *fdSoak) progressLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			markets := len(s.ids)
			s.mu.Unlock()
			rep := s.metrics.snapshot(s.started, time.Now())
			s.events.log("fanduel", "progress", true, map[string]any{
				"connected":   rep.WS.Connected,
				"connections": rep.WS.Connections,
				"frames":      rep.WS.Frames,
				"updates":     rep.WS.Updates,
				"subscribed":  markets,
				"withData":    s.state.Count(),
				"restErrors":  rep.REST.Errors,
				"checks":      rep.Reconcile.Checks,
			})
		}
	}
}

func (s *fdSoak) restLoop(ctx context.Context) {
	pollSeq, discSeq := 0, 0
	discSeq++
	s.discoveryOnce(ctx, discSeq)
	pollSeq++
	s.pollOnce(ctx, pollSeq)
	poll := time.NewTicker(s.cfg.FDPollEvery)
	defer poll.Stop()
	disc := time.NewTicker(s.cfg.FDDiscoveryEvery)
	defer disc.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			pollSeq++
			s.pollOnce(ctx, pollSeq)
		case <-disc.C:
			discSeq++
			s.discoveryOnce(ctx, discSeq)
		}
	}
}

func (s *fdSoak) idsCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ids...)
}

func (s *fdSoak) resync(ctx context.Context, connNo int, ids []string) {
	var all []fd.Market
	for i, batch := range batchIDs(ids, 70) {
		res, err := fd.FetchMarketPricesSMP(ctx, fd.SMPOptions{}, batch)
		if err != nil {
			s.metrics.restError(err)
			s.events.log("fanduel", "resync_error", true, map[string]any{
				"attempt": connNo, "batch": i + 1, "error": err.Error(),
			})
			continue
		}
		s.metrics.rest(res.ElapsedMs)
		_, _ = saveREST(s.dir, "fd", fmt.Sprintf("resync-c%d-b%d", connNo, i+1), connNo, res.Raw, map[string]any{
			"status": res.Status, "elapsedMs": res.ElapsedMs, "bytes": len(res.Raw), "markets": len(res.Markets),
		})
		all = append(all, res.Markets...)
	}
	if len(all) > 0 {
		s.state.replace(all)
		s.events.log("fanduel", "resync", false, map[string]any{
			"attempt": connNo, "markets": len(all),
		})
	}
}

type fdPollRecord struct {
	At           string   `json:"at"`
	MonoMs       float64  `json:"monoMs"`
	Seq          int      `json:"seq"`
	Batches      int      `json:"batches"`
	ElapsedMs    float64  `json:"elapsedMs"`
	Markets      int      `json:"markets"`
	WSMarkets    int      `json:"wsMarkets"`
	Baseline     bool     `json:"baseline,omitempty"`
	Compared     int      `json:"compared"`
	Exact        int      `json:"exact"`
	MissingInWS  int      `json:"missingInWS"`
	MissingInSMP int      `json:"missingInSMP"`
	PriceDiffs   int      `json:"priceDiffs"`
	StatusDiffs  int      `json:"statusDiffs"`
	LineDiffs    int      `json:"lineDiffs"`
	Samples      []string `json:"samples,omitempty"`
	Error        string   `json:"error,omitempty"`
}

func (s *fdSoak) pollOnce(ctx context.Context, seq int) {
	if ctx.Err() != nil {
		return
	}
	ids := s.idsCopy()
	if len(ids) == 0 {
		return
	}
	var all []fd.Market
	batches := 0
	elapsed := 0.0
	for i, batch := range batchIDs(ids, 70) {
		res, err := fd.FetchMarketPricesSMP(ctx, fd.SMPOptions{}, batch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.metrics.restError(err)
			s.events.log("fanduel", "poll_error", true, map[string]any{"batch": i + 1, "error": err.Error()})
			continue
		}
		batches++
		elapsed += res.ElapsedMs
		s.metrics.rest(res.ElapsedMs)
		_, saveErr := saveREST(s.dir, "fd", fmt.Sprintf("smp-b%d", i+1), seq, res.Raw, map[string]any{
			"status": res.Status, "elapsedMs": res.ElapsedMs, "bytes": len(res.Raw), "markets": len(res.Markets),
		})
		if saveErr != nil {
			s.events.log("fanduel", "poll_save_error", true, map[string]any{"error": saveErr.Error()})
		}
		all = append(all, res.Markets...)
	}
	if batches == 0 {
		return
	}
	wsMarkets := s.state.Count()
	baseline := wsMarkets == 0
	var diff fdDiff
	if !baseline {
		diff = reconcileFD(s.state, all)
	}
	record := fdPollRecord{
		At: clock.NowISO(), MonoMs: clock.MonoMs(), Seq: seq, Batches: batches,
		ElapsedMs: elapsed, Markets: len(all), WSMarkets: wsMarkets, Baseline: baseline,
		Compared: diff.Compared, Exact: diff.Exact,
		MissingInWS: diff.MissingInWS, MissingInSMP: diff.MissingInSMP,
		PriceDiffs: diff.PriceDiffs, StatusDiffs: diff.StatusDiffs, LineDiffs: diff.LineDiffs,
		Samples: diff.Samples,
	}
	if err := s.reconcileLog.Write(record); err != nil {
		s.events.log("fanduel", "reconcile_write_error", true, map[string]any{"error": err.Error()})
	}
	s.metrics.reconcile(diff.Compared, diff.MissingInWS, diff.PriceDiffs, diff.StatusDiffs, diff.LineDiffs, 0)

	drift := diff.MissingInWS + diff.PriceDiffs + diff.StatusDiffs + diff.LineDiffs
	if !baseline && drift > 0 {
		s.events.log("fanduel", "reconcile_drift", true, map[string]any{
			"seq": seq, "markets": len(all), "missingInWS": diff.MissingInWS,
			"priceDiffs": diff.PriceDiffs, "statusDiffs": diff.StatusDiffs, "lineDiffs": diff.LineDiffs,
			"samples": diff.Samples,
		})
	} else if s.cfg.Verbose {
		s.events.log("fanduel", "reconcile", false, map[string]any{"seq": seq, "compared": diff.Compared})
	}
}

type fdDiff struct {
	WSMarkets    int
	Compared     int
	Exact        int
	MissingInWS  int
	MissingInSMP int
	PriceDiffs   int
	StatusDiffs  int
	LineDiffs    int
	Samples      []string
}

// reconcileFD compares the pushed state with an SMP poll. The poll may be a
// few seconds newer or older than the last push, so diffs are drift, not errors.
func reconcileFD(state *fdState, smp []fd.Market) fdDiff {
	ws := state.Dump()
	smpMap := make(map[string]fd.Market, len(smp))
	for _, m := range smp {
		smpMap[m.MarketID] = m
	}
	diff := fdDiff{WSMarkets: len(ws)}
	for id, m := range smpMap {
		wv, ok := ws[id]
		if !ok {
			diff.MissingInWS++
			diff.sample("missingInWS " + id)
			continue
		}
		diff.Compared++
		exact := true
		if wv.Status != m.MarketStatus {
			diff.StatusDiffs++
			exact = false
			diff.sample(fmt.Sprintf("status %s push=%s poll=%s", id, wv.Status, m.MarketStatus))
		}
		if wv.InPlay != m.IsInPlay() {
			diff.StatusDiffs++
			exact = false
			diff.sample(fmt.Sprintf("inplay %s push=%v poll=%v", id, wv.InPlay, m.IsInPlay()))
		}
		runners := m.AllRunners()
		if len(runners) != len(wv.Runners) {
			diff.StatusDiffs++
			exact = false
			diff.sample(fmt.Sprintf("runners %s push=%d poll=%d", id, len(wv.Runners), len(runners)))
		}
		for _, r := range runners {
			rv, ok := wv.Runners[r.SelectionID.String()]
			if !ok {
				diff.StatusDiffs++
				exact = false
				diff.sample(fmt.Sprintf("missingRunner %s/%s", id, r.SelectionID))
				continue
			}
			if rv.Status != r.RunnerStatus {
				diff.StatusDiffs++
				exact = false
				diff.sample(fmt.Sprintf("runnerStatus %s/%s push=%s poll=%s", id, r.SelectionID, rv.Status, r.RunnerStatus))
			}
			if !sameFloatPtr(rv.American, r.WinRunnerOdds.American()) {
				diff.PriceDiffs++
				exact = false
				diff.sample(fmt.Sprintf("price %s/%s push=%v poll=%v", id, r.SelectionID, fmtPtr(rv.American), fmtPtr(r.WinRunnerOdds.American())))
			}
			if !sameFloatPtr(rv.Line, r.Handicap) {
				diff.LineDiffs++
				exact = false
				diff.sample(fmt.Sprintf("line %s/%s push=%v poll=%v", id, r.SelectionID, fmtPtr(rv.Line), fmtPtr(r.Handicap)))
			}
		}
		if exact {
			diff.Exact++
		}
	}
	for id := range ws {
		if _, ok := smpMap[id]; !ok {
			diff.MissingInSMP++
		}
	}
	return diff
}

func (d *fdDiff) sample(s string) {
	if len(d.Samples) < 10 {
		d.Samples = append(d.Samples, s)
	}
}

func sameFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func fmtPtr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

type fdDiscoveryRecord struct {
	At          string   `json:"at"`
	MonoMs      float64  `json:"monoMs"`
	Seq         int      `json:"seq"`
	ElapsedMs   float64  `json:"elapsedMs"`
	Events      int      `json:"events"`
	Markets     int      `json:"markets"`
	MainMarkets int      `json:"mainMarkets"`
	Added       int      `json:"added"`
	Removed     int      `json:"removed"`
	AddedIDs    []string `json:"addedIds,omitempty"`
	RemovedIDs  []string `json:"removedIds,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func (s *fdSoak) discoveryOnce(ctx context.Context, seq int) {
	if ctx.Err() != nil {
		return
	}
	res, err := fd.Discover(ctx, fd.BoardOptions{})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.metrics.restError(err)
		s.metrics.discovery(false)
		s.events.log("fanduel", "discovery_error", true, map[string]any{"error": err.Error()})
		_ = s.discoveryLog.Write(fdDiscoveryRecord{At: clock.NowISO(), MonoMs: clock.MonoMs(), Seq: seq, Error: err.Error()})
		return
	}
	s.metrics.rest(res.ElapsedMs)
	ids := mainMarketIDs(res.Board, s.cfg.FDMarketLimit)
	_, saveErr := saveREST(s.dir, "fd", "discovery", seq, res.Raw, map[string]any{
		"status":       res.Status,
		"elapsedMs":    res.ElapsedMs,
		"bytes":        len(res.Raw),
		"cacheControl": res.CacheControl,
		"events":       len(res.Board.Attachments.Events),
		"markets":      len(res.Board.Attachments.Markets),
		"mainMarkets":  len(ids),
	})
	if saveErr != nil {
		s.events.log("fanduel", "discovery_save_error", true, map[string]any{"error": saveErr.Error()})
	}

	old := s.idsCopy()
	first := len(old) == 0
	added, removed := setDiff(old, ids)
	changed := len(added) > 0 || len(removed) > 0
	s.mu.Lock()
	s.ids = ids
	s.mu.Unlock()
	s.metrics.subscribed(len(ids))
	s.metrics.discovery(changed && !first)
	record := fdDiscoveryRecord{
		At: clock.NowISO(), MonoMs: clock.MonoMs(), Seq: seq, ElapsedMs: res.ElapsedMs,
		Events: len(res.Board.Attachments.Events), Markets: len(res.Board.Attachments.Markets),
		MainMarkets: len(ids), Added: len(added), Removed: len(removed),
		AddedIDs: added, RemovedIDs: removed,
	}
	if err := s.discoveryLog.Write(record); err != nil {
		s.events.log("fanduel", "discovery_write_error", true, map[string]any{"error": err.Error()})
	}
	if changed && !first {
		s.events.log("fanduel", "discovery_change", true, map[string]any{
			"added": added, "removed": removed, "markets": len(ids),
		})
		select {
		case s.resubscribe <- struct{}{}:
		default:
		}
	} else if s.cfg.Verbose {
		s.events.log("fanduel", "discovery", false, map[string]any{"markets": len(ids)})
	}
}

func setDiff(old, next []string) (added, removed []string) {
	oldSet := make(map[string]struct{}, len(old))
	for _, id := range old {
		oldSet[id] = struct{}{}
	}
	nextSet := make(map[string]struct{}, len(next))
	for _, id := range next {
		nextSet[id] = struct{}{}
	}
	for id := range nextSet {
		if _, ok := oldSet[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range oldSet {
		if _, ok := nextSet[id]; !ok {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func batchIDs(ids []string, size int) [][]string {
	var batches [][]string
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		batches = append(batches, ids[start:end])
	}
	return batches
}
