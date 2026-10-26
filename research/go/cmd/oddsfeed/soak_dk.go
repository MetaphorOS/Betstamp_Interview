package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"sportsbookfeed/internal/clock"
	"sportsbookfeed/internal/dk"
	"sportsbookfeed/internal/jsonx"
)

// dkSoak keeps one DraftKings WebSocket alive for the whole run while a
// separate goroutine reconciles the merged cache against periodic snapshots.
type dkSoak struct {
	cfg     soakConfig
	dir     *runDir
	events  *eventLog
	metrics *bookMetrics
	cache   *dk.Cache
	started time.Time

	wsLog        *jsonlFile
	reconcileLog *jsonlFile

	mu              sync.Mutex
	partialMessages []map[string]any
	lastBootstrap   []byte
}

func newDKSoak(cfg soakConfig, rd *runDir, events *eventLog, started time.Time) (*dkSoak, error) {
	wsLog, err := openJSONL(rd.path("dk/ws.jsonl"))
	if err != nil {
		return nil, err
	}
	recLog, err := openJSONL(rd.path("dk/reconcile.jsonl"))
	if err != nil {
		wsLog.Close()
		return nil, err
	}
	return &dkSoak{
		cfg:          cfg,
		dir:          rd,
		events:       events,
		metrics:      newBookMetrics("draftkings"),
		cache:        dk.NewCache(),
		started:      started,
		wsLog:        wsLog,
		reconcileLog: recLog,
	}, nil
}

func (s *dkSoak) run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.wsLoop(ctx) }()
	go func() { defer wg.Done(); s.restLoop(ctx) }()
	go func() { defer wg.Done(); s.progressLoop(ctx) }()
	wg.Wait()
	_ = s.wsLog.Close()
	_ = s.reconcileLog.Close()
}

func (s *dkSoak) wsLoop(ctx context.Context) {
	backoff := time.Second
	for connNo := 1; ; connNo++ {
		if ctx.Err() != nil {
			return
		}
		raw, messages, fresh, err := s.bootstrap(ctx, connNo)
		if err != nil {
			s.events.log("draftkings", "bootstrap_error", true, map[string]any{
				"attempt": connNo,
				"error":   err.Error(),
			})
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		ws, err := dk.Dial(ctx, dk.WSOptions{
			URL:     s.wsURL(),
			Header:  http.Header{"User-Agent": {dk.DefaultUserAgent}},
			Timeout: 15 * time.Second,
		})
		if err != nil {
			s.events.log("draftkings", "ws_connect_error", true, map[string]any{
				"attempt": connNo,
				"error":   err.Error(),
			})
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		s.metrics.connected()
		s.events.log("draftkings", "ws_connected", true, map[string]any{
			"attempt":        connNo,
			"url":            s.wsURL(),
			"subscriptions":  len(messages),
			"freshBootstrap": fresh,
		})

		// A fresh snapshot replaces the cache; a fallback keeps the last state.
		if fresh && len(raw) > 0 {
			if err := s.cache.SeedRaw(raw); err != nil {
				s.events.log("draftkings", "resync_error", true, map[string]any{"error": err.Error()})
			} else {
				events, markets, selections := s.cache.Counts()
				s.events.log("draftkings", "resync", false, map[string]any{
					"events": events, "markets": markets, "selections": selections,
				})
			}
		}

		sendErr := error(nil)
		for _, msg := range messages {
			_ = s.wsLog.Write(map[string]any{
				"at": clock.NowISO(), "monoMs": clock.MonoMs(), "conn": connNo,
				"kind": "sent", "payload": msg,
			})
			if err := ws.SendJSON(msg); err != nil {
				sendErr = err
				break
			}
		}
		if sendErr != nil {
			_ = ws.Close()
			s.metrics.disconnected()
			s.events.log("draftkings", "ws_send_error", true, map[string]any{
				"attempt": connNo, "error": sendErr.Error(),
			})
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		connectedAt := time.Now()
		stop := make(chan struct{})
		var lastRead, lastPong atomic.Int64
		lastRead.Store(time.Now().UnixNano())
		lastPong.Store(time.Now().UnixNano())
		ws.OnPong(func() {
			lastPong.Store(time.Now().UnixNano())
			s.metrics.pong()
		})

		go s.pingLoop(ws, stop)
		go s.watchdog(ws, stop, &lastRead, &lastPong, connNo)
		go func() {
			select {
			case <-ctx.Done():
				_ = ws.Close()
			case <-stop:
			}
		}()
		if s.cfg.ReconnectEvery > 0 {
			go func() {
				timer := time.NewTimer(s.cfg.ReconnectEvery)
				defer timer.Stop()
				select {
				case <-timer.C:
					s.events.log("draftkings", "forced_reconnect", true, map[string]any{"attempt": connNo})
					_ = ws.Close()
				case <-stop:
				}
			}()
		}

		var readErr error
		frames := 0
		unknownLogged := 0
		updates := 0
		for {
			frame, err := ws.Read()
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

			update, unknown := false, false
			if msg, err := jsonx.DecodeObject(frame); err == nil {
				if stats, ok := s.cache.ApplyMessage(msg); ok {
					update = true
					updates++
					unknown = stats.Unknown > 0
					if unknown && unknownLogged < 3 {
						unknownLogged++
						s.events.log("draftkings", "unknown_change", true, map[string]any{
							"attempt": connNo, "unknown": stats.Unknown,
						})
					}
				}
			}
			s.metrics.frame(int64(len(frame)), update, unknown)
		}

		close(stop)
		_ = ws.Close()
		s.metrics.disconnected()
		code, reason := closeReason(readErr)
		if ctx.Err() != nil {
			code, reason = 1000, "run complete"
		}
		connSeconds := time.Since(connectedAt).Seconds()
		s.events.log("draftkings", "ws_disconnected", true, map[string]any{
			"attempt":          connNo,
			"code":             code,
			"reason":           reason,
			"connectedSeconds": connSeconds,
			"frames":           frames,
			"updates":          updates,
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

func (s *dkSoak) wsURL() string {
	if s.cfg.DKScope == "probe" {
		return dk.DefaultWSURL
	}
	return dk.DefaultWSURLCompare
}

// bootstrap fetches a snapshot and derives the subscription frames. On failure
// it falls back to the last successful subscriptions so a REST outage does not
// take down an otherwise healthy socket.
func (s *dkSoak) bootstrap(ctx context.Context, connNo int) (raw []byte, messages []map[string]any, fresh bool, err error) {
	snap, err := dk.FetchSnapshot(ctx, dk.FetchOptions{})
	if err != nil {
		s.mu.Lock()
		cached := s.partialMessages
		oldRaw := s.lastBootstrap
		s.mu.Unlock()
		if len(cached) == 0 {
			return nil, nil, false, err
		}
		s.events.log("draftkings", "bootstrap_fallback", true, map[string]any{
			"attempt": connNo, "error": err.Error(),
		})
		return oldRaw, cached, false, nil
	}
	s.metrics.rest(snap.ElapsedMs)

	var msgs []map[string]any
	if s.cfg.DKScope == "probe" {
		msgs = []map[string]any{dk.NFLSubscribeMessage()}
	} else {
		partials := snap.Data.PartialList()
		if len(partials) == 0 {
			return nil, nil, false, dk.ErrNoPartials
		}
		msgs = dk.PartialsSubscribeMessages(partials, "", "")
	}

	s.mu.Lock()
	s.partialMessages = msgs
	s.lastBootstrap = snap.Raw
	s.mu.Unlock()

	events, markets, selections := snap.Counts()
	base, saveErr := saveREST(s.dir, "dk", "bootstrap", connNo, snap.Raw, map[string]any{
		"url":          dk.DefaultLeagueURL,
		"status":       snap.Status,
		"elapsedMs":    snap.ElapsedMs,
		"bytes":        len(snap.Raw),
		"cacheControl": snap.CacheControl,
		"events":       events,
		"markets":      markets,
		"selections":   selections,
	})
	if saveErr != nil {
		s.events.log("draftkings", "bootstrap_save_error", true, map[string]any{"error": saveErr.Error()})
	} else {
		s.events.log("draftkings", "bootstrap", false, map[string]any{"file": base})
	}
	return snap.Raw, msgs, true, nil
}

func (s *dkSoak) pingLoop(ws *dk.WS, stop <-chan struct{}) {
	ticker := time.NewTicker(s.cfg.PingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := ws.Ping(); err != nil {
				_ = ws.Close()
				return
			}
			s.metrics.ping()
		}
	}
}

func (s *dkSoak) watchdog(ws *dk.WS, stop <-chan struct{}, lastRead, lastPong *atomic.Int64, connNo int) {
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
				s.events.log("draftkings", "watchdog_stale", true, map[string]any{
					"attempt": connNo,
					"idleFor": time.Since(time.Unix(0, last)).String(),
				})
				_ = ws.Close()
				return
			}
		}
	}
}

func (s *dkSoak) progressLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			events, markets, selections := s.cache.Counts()
			rep := s.metrics.snapshot(s.started, time.Now())
			s.events.log("draftkings", "progress", true, map[string]any{
				"connected":   rep.WS.Connected,
				"connections": rep.WS.Connections,
				"frames":      rep.WS.Frames,
				"updates":     rep.WS.Updates,
				"events":      events,
				"markets":     markets,
				"selections":  selections,
				"restErrors":  rep.REST.Errors,
				"checks":      rep.Reconcile.Checks,
			})
		}
	}
}

func (s *dkSoak) restLoop(ctx context.Context) {
	seq := 0
	s.reconcileOnce(ctx, &seq)
	ticker := time.NewTicker(s.cfg.DKRestEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOnce(ctx, &seq)
		}
	}
}

type dkReconcileRecord struct {
	At              string   `json:"at"`
	MonoMs          float64  `json:"monoMs"`
	Seq             int      `json:"seq"`
	ElapsedMs       float64  `json:"elapsedMs"`
	CacheControl    string   `json:"cacheControl"`
	Events          int      `json:"events"`
	Markets         int      `json:"markets"`
	Selections      int      `json:"selections"`
	MainSelections  int      `json:"mainSelections"`
	Compared        int      `json:"compared"`
	CacheEvents     int      `json:"cacheEvents"`
	CacheMarkets    int      `json:"cacheMarkets"`
	CacheSelections int      `json:"cacheSelections"`
	Baseline        bool     `json:"baseline,omitempty"`
	Missing         int      `json:"missing"`
	PriceDiffs      int      `json:"priceDiffs"`
	StatusDiffs     int      `json:"statusDiffs"`
	LineDiffs       int      `json:"lineDiffs"`
	Filled          int      `json:"filled"`
	Samples         []string `json:"samples,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func (s *dkSoak) reconcileOnce(ctx context.Context, seq *int) {
	if ctx.Err() != nil {
		return
	}
	*seq++
	n := *seq
	res, err := dk.FetchSnapshot(ctx, dk.FetchOptions{})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.metrics.restError(err)
		s.events.log("draftkings", "rest_error", true, map[string]any{"seq": n, "error": err.Error()})
		_ = s.reconcileLog.Write(dkReconcileRecord{
			At: clock.NowISO(), MonoMs: clock.MonoMs(), Seq: n, Error: err.Error(),
		})
		return
	}
	s.metrics.rest(res.ElapsedMs)

	beforeEvents, beforeMarkets, beforeSelections := s.cache.Counts()
	baseline := beforeSelections == 0
	var diff dkDiff
	if !baseline {
		diff = reconcileDK(s.cache, res.Data)
	}
	added, ensureErr := s.cache.EnsureRaw(res.Raw)
	if ensureErr != nil {
		s.events.log("draftkings", "fill_error", true, map[string]any{"error": ensureErr.Error()})
	}
	mainCount := mainSelectionCount(res.Data)

	record := dkReconcileRecord{
		At:              clock.NowISO(),
		MonoMs:          clock.MonoMs(),
		Seq:             n,
		ElapsedMs:       res.ElapsedMs,
		CacheControl:    res.CacheControl,
		Events:          len(res.Data.Events),
		Markets:         len(res.Data.Markets),
		Selections:      len(res.Data.Selections),
		MainSelections:  mainCount,
		Compared:        diff.Compared,
		CacheEvents:     beforeEvents,
		CacheMarkets:    beforeMarkets,
		CacheSelections: beforeSelections,
		Baseline:        baseline,
		Missing:         diff.Missing,
		PriceDiffs:      diff.PriceDiffs,
		StatusDiffs:     diff.StatusDiffs,
		LineDiffs:       diff.LineDiffs,
		Filled:          added,
		Samples:         diff.Samples,
	}
	base, saveErr := saveREST(s.dir, "dk", "snapshot", n, res.Raw, map[string]any{
		"url":          dk.DefaultLeagueURL,
		"status":       res.Status,
		"elapsedMs":    res.ElapsedMs,
		"bytes":        len(res.Raw),
		"cacheControl": res.CacheControl,
		"age":          res.Age,
		"events":       len(res.Data.Events),
		"markets":      len(res.Data.Markets),
		"selections":   len(res.Data.Selections),
	})
	if saveErr != nil {
		s.events.log("draftkings", "snapshot_save_error", true, map[string]any{"seq": n, "error": saveErr.Error()})
	}
	if err := s.reconcileLog.Write(record); err != nil {
		s.events.log("draftkings", "reconcile_write_error", true, map[string]any{"error": err.Error()})
	}
	s.metrics.reconcile(diff.Compared, diff.Missing, diff.PriceDiffs, diff.StatusDiffs, diff.LineDiffs, added)

	drift := diff.Missing + diff.PriceDiffs + diff.StatusDiffs + diff.LineDiffs
	if !baseline && drift > 0 {
		s.events.log("draftkings", "reconcile_drift", true, map[string]any{
			"seq":         n,
			"snapshot":    base,
			"missing":     diff.Missing,
			"priceDiffs":  diff.PriceDiffs,
			"statusDiffs": diff.StatusDiffs,
			"lineDiffs":   diff.LineDiffs,
			"filled":      added,
			"samples":     diff.Samples,
		})
	} else if s.cfg.Verbose {
		s.events.log("draftkings", "reconcile", false, map[string]any{"seq": n, "compared": diff.Compared})
	}
}

// dkDiff is one snapshot-vs-cache comparison.
type dkDiff struct {
	Compared    int
	Missing     int
	PriceDiffs  int
	StatusDiffs int
	LineDiffs   int
	Samples     []string
}

// reconcileDK compares the WebSocket cache with a full main-market snapshot.
// The snapshot may be a second or two older than the socket, so price diffs are
// reported as drift, not as errors; missing records are filled by the caller.
func reconcileDK(cache *dk.Cache, data *dk.Data) dkDiff {
	_, _, selections := cache.Snapshot()
	main := map[string]dk.Market{}
	for _, m := range data.Markets {
		name := m.Name
		if m.MarketType != nil && m.MarketType.Name != "" {
			name = m.MarketType.Name
		}
		switch name {
		case "Moneyline", "Spread", "Total":
			main[m.ID.String()] = m
		}
	}
	var diff dkDiff
	for _, sel := range data.Selections {
		if _, ok := main[sel.MarketID.String()]; !ok {
			continue
		}
		diff.Compared++
		rec, ok := selections[sel.ID.String()]
		if !ok {
			diff.Missing++
			diff.sample(fmt.Sprintf("missing %s (%s)", sel.ID, sel.Label))
			continue
		}
		cacheAmerican := jsonx.Str(jsonx.Obj(rec, "displayOdds"), "american")
		if a, okA := americanValue(sel.DisplayOdds.American); okA {
			if b, okB := americanValue(cacheAmerican); okB && a != b {
				diff.PriceDiffs++
				diff.sample(fmt.Sprintf("price %s snapshot=%d cache=%d", sel.ID, a, b))
			}
		}
		if sel.Points != nil {
			if v, ok := floatValue(rec["points"]); ok && v != *sel.Points {
				diff.LineDiffs++
				diff.sample(fmt.Sprintf("line %s snapshot=%g cache=%g", sel.ID, *sel.Points, v))
			}
		}
		if sel.Status != "" {
			if cached := jsonx.Str(rec, "status"); cached != "" && cached != sel.Status {
				diff.StatusDiffs++
				diff.sample(fmt.Sprintf("status %s snapshot=%s cache=%s", sel.ID, sel.Status, cached))
			}
		}
	}
	return diff
}

func (d *dkDiff) sample(s string) {
	if len(d.Samples) < 10 {
		d.Samples = append(d.Samples, s)
	}
}

func mainSelectionCount(data *dk.Data) int {
	main := map[string]struct{}{}
	for _, m := range data.Markets {
		name := m.Name
		if m.MarketType != nil && m.MarketType.Name != "" {
			name = m.MarketType.Name
		}
		switch name {
		case "Moneyline", "Spread", "Total":
			main[m.ID.String()] = struct{}{}
		}
	}
	n := 0
	for _, s := range data.Selections {
		if _, ok := main[s.MarketID.String()]; ok {
			n++
		}
	}
	return n
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}
