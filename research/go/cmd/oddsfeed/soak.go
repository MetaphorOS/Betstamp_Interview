package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"sportsbookfeed/internal/clock"
	"sportsbookfeed/internal/jsonx"
)

// soakConfig controls the long-running stability run.
type soakConfig struct {
	Duration         time.Duration
	OutDir           string
	DKRestEvery      time.Duration
	FDPollEvery      time.Duration
	FDDiscoveryEvery time.Duration
	DKScope          string
	FDMarketLimit    int
	PingEvery        time.Duration
	StaleAfter       time.Duration
	ReconnectEvery   time.Duration
	Verbose          bool
}

func (c soakConfig) json() map[string]any {
	return map[string]any{
		"duration":         c.Duration.String(),
		"durationSeconds":  c.Duration.Seconds(),
		"dkRestEvery":      c.DKRestEvery.String(),
		"fdPollEvery":      c.FDPollEvery.String(),
		"fdDiscoveryEvery": c.FDDiscoveryEvery.String(),
		"dkScope":          c.DKScope,
		"fdMarketLimit":    c.FDMarketLimit,
		"pingEvery":        c.PingEvery.String(),
		"staleAfter":       c.StaleAfter.String(),
		"reconnectEvery":   c.ReconnectEvery.String(),
	}
}

// cmdSoak runs both books for a configured duration, saving every WebSocket
// frame, every REST body, reconciliation records and a final summary.
func cmdSoak(args []string) error {
	fs := flag.NewFlagSet("soak", flag.ContinueOnError)
	duration := fs.Duration("duration", time.Hour, "total run time, e.g. 1h or 3h")
	outDir := fs.String("out", "", "run directory (default out/soak-<UTC timestamp>)")
	dkRestEvery := fs.Duration("dk-rest-every", 30*time.Second, "DraftKings snapshot interval")
	fdPollEvery := fs.Duration("fd-poll-every", 30*time.Second, "FanDuel SMP poll interval")
	fdDiscoveryEvery := fs.Duration("fd-discovery-every", 5*time.Minute, "FanDuel discovery refresh interval")
	dkScope := fs.String("dk-scope", "partials", "partials (narrow snapshot subscriptions) or probe (broad NFL probe)")
	fdMarkets := fs.Int("fd-markets", 0, "limit FanDuel subscribed markets (0 = all main markets)")
	pingEvery := fs.Duration("ping-every", 30*time.Second, "WebSocket ping interval")
	staleAfter := fs.Duration("stale-after", 10*time.Minute, "reconnect when no frame/pong arrives for this long")
	reconnectEvery := fs.Duration("reconnect-every", 0, "force a reconnect at this interval (0 = never; for recovery testing)")
	verbose := fs.Bool("verbose", false, "print every event, including each reconciliation check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *duration < 10*time.Second {
		return errors.New("duration must be at least 10s")
	}
	if *dkRestEvery <= 0 || *fdPollEvery <= 0 || *fdDiscoveryEvery <= 0 {
		return errors.New("REST intervals must be positive")
	}
	if *pingEvery <= 0 || *staleAfter <= *pingEvery {
		return errors.New("stale-after must be greater than ping-every")
	}
	if *reconnectEvery < 0 {
		return errors.New("reconnect-every cannot be negative")
	}
	if *dkScope != "partials" && *dkScope != "probe" {
		return fmt.Errorf("unknown -dk-scope %q (want partials or probe)", *dkScope)
	}

	cfg := soakConfig{
		Duration:         *duration,
		OutDir:           *outDir,
		DKRestEvery:      *dkRestEvery,
		FDPollEvery:      *fdPollEvery,
		FDDiscoveryEvery: *fdDiscoveryEvery,
		DKScope:          *dkScope,
		FDMarketLimit:    *fdMarkets,
		PingEvery:        *pingEvery,
		StaleAfter:       *staleAfter,
		ReconnectEvery:   *reconnectEvery,
		Verbose:          *verbose,
	}
	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join("out", "soak-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	rd, err := newRunDir(cfg.OutDir)
	if err != nil {
		return err
	}
	events, err := newEventLog(rd.path("events.jsonl"), cfg.Verbose)
	if err != nil {
		return err
	}
	defer events.Close()

	started := time.Now()
	startedISO := clock.NowISO()
	if err := rd.writeJSON("run.json", map[string]any{
		"startedAt":   startedISO,
		"startedMono": clock.MonoMs(),
		"config":      cfg.json(),
		"goVersion":   runtime.Version(),
	}, true); err != nil {
		return err
	}
	events.log("run", "start", true, map[string]any{
		"outDir": rd.root,
		"config": cfg.json(),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(cfg.Duration, cancel)
	defer timer.Stop()

	dk, err := newDKSoak(cfg, rd, events, started)
	if err != nil {
		return err
	}
	fd, err := newFDSoak(cfg, rd, events, started)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); dk.run(runCtx) }()
	go func() { defer wg.Done(); fd.run(runCtx) }()
	wg.Wait()
	cancel()

	ended := time.Now()
	// The collectors have stopped, so the caches and reports are stable.
	if err := rd.writeJSON("dk/cache-final.json", dk.cache.Dump(), true); err != nil {
		events.log("draftkings", "final_cache_error", true, map[string]any{"error": err.Error()})
	}
	if err := rd.writeJSON("fd/state-final.json", fd.state.Dump(), true); err != nil {
		events.log("fanduel", "final_state_error", true, map[string]any{"error": err.Error()})
	}
	dkReport := dk.metrics.snapshot(started, ended)
	fdReport := fd.metrics.snapshot(started, ended)
	ok := dkReport.WS.Connections > 0 && fdReport.WS.Connections > 0 &&
		dkReport.REST.Requests > 0 && fdReport.REST.Requests > 0
	summary := map[string]any{
		"run": map[string]any{
			"startedAt":       startedISO,
			"endedAt":         clock.NowISO(),
			"durationSeconds": ended.Sub(started).Seconds(),
			"outDir":          rd.root,
			"config":          cfg.json(),
			"interrupted":     ctx.Err() != nil && ended.Sub(started) < cfg.Duration,
		},
		"draftkings": dkReport,
		"fanduel":    fdReport,
		"ok":         ok,
	}
	if err := rd.writeJSON("summary.json", summary, true); err != nil {
		return err
	}
	events.log("run", "end", true, map[string]any{
		"durationSeconds": ended.Sub(started).Seconds(),
		"ok":              ok,
		"outDir":          rd.root,
	})

	line, _ := jsonx.Marshal(summary)
	fmt.Println(string(line))
	fmt.Printf("soak complete in %.1fs -> %s\n", ended.Sub(started).Seconds(), rd.root)
	fmt.Printf("  draftkings: connections=%d reconnects=%d frames=%d uptime=%.3f%%\n",
		dkReport.WS.Connections, dkReport.WS.Reconnects, dkReport.WS.Frames, dkReport.WS.UptimePct)
	fmt.Printf("  fanduel:    connections=%d reconnects=%d frames=%d uptime=%.3f%%\n",
		fdReport.WS.Connections, fdReport.WS.Reconnects, fdReport.WS.Frames, fdReport.WS.UptimePct)
	if !ok {
		return errors.New("soak: at least one book never established a WebSocket connection")
	}
	return nil
}

// runDir is one soak run's output tree.
type runDir struct {
	root string
}

func newRunDir(root string) (*runDir, error) {
	for _, sub := range []string{"dk/rest", "fd/rest"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &runDir{root: root}, nil
}

func (r *runDir) path(rel string) string { return filepath.Join(r.root, rel) }

func (r *runDir) writeRaw(rel string, data []byte) error {
	path := r.path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (r *runDir) writeJSON(rel string, v any, indent bool) error {
	var (
		data []byte
		err  error
	)
	if indent {
		data, err = jsonx.MarshalIndent(v)
	} else {
		data, err = jsonx.Marshal(v)
	}
	if err != nil {
		return err
	}
	return r.writeRaw(rel, data)
}

// jsonlFile appends JSON records and keeps one file handle per stream.
type jsonlFile struct {
	mu sync.Mutex
	f  *os.File
}

func openJSONL(path string) (*jsonlFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &jsonlFile{f: f}, nil
}

func (j *jsonlFile) Write(v any) error {
	line, err := jsonx.Marshal(v)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, err = j.f.Write(append(line, '\n'))
	return err
}

func (j *jsonlFile) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// eventLog writes lifecycle/reconciliation events to events.jsonl and prints
// a concise subset to the console.
type eventLog struct {
	file    *jsonlFile
	console chan string
	done    chan struct{}
}

func newEventLog(path string, verbose bool) (*eventLog, error) {
	f, err := openJSONL(path)
	if err != nil {
		return nil, err
	}
	e := &eventLog{file: f, console: make(chan string, 1024), done: make(chan struct{})}
	go func() {
		defer close(e.done)
		for line := range e.console {
			fmt.Println(line)
		}
	}()
	_ = verbose
	// The verbosity decision is made by log, not by the printer.
	return e, nil
}

// log always writes to the file. Callers pass a console flag for events worth
// printing; reconciliation checks can be silenced.
func (e *eventLog) log(book, event string, console bool, details map[string]any) {
	rec := map[string]any{
		"at":     clock.NowISO(),
		"monoMs": clock.MonoMs(),
		"book":   book,
		"event":  event,
	}
	if len(details) > 0 {
		rec["details"] = details
	}
	if err := e.file.Write(rec); err != nil {
		return
	}
	if console {
		line, _ := jsonx.Marshal(rec)
		select {
		case e.console <- jsonx.Truncate(string(line), 600):
		default:
		}
	}
}

func (e *eventLog) Close() error {
	close(e.console)
	<-e.done
	return e.file.Close()
}

// bookReport is the per-book summary written into summary.json.
type bookReport struct {
	Book              string          `json:"book"`
	WS                wsTotals        `json:"websocket"`
	REST              restTotals      `json:"rest"`
	Reconcile         reconcileTotals `json:"reconcile"`
	MarketsSubscribed int             `json:"marketsSubscribed,omitempty"`
	DiscoveryChecks   int             `json:"discoveryChecks,omitempty"`
	DiscoveryChanges  int             `json:"discoveryChanges,omitempty"`
	LastError         string          `json:"lastError,omitempty"`
}

type wsTotals struct {
	Connections      int     `json:"connections"`
	Reconnects       int     `json:"reconnects"`
	Disconnects      int     `json:"disconnects"`
	Connected        bool    `json:"connected"`
	ConnectedSeconds float64 `json:"connectedSeconds"`
	DowntimeSeconds  float64 `json:"downtimeSeconds"`
	UptimePct        float64 `json:"uptimePct"`
	Frames           int64   `json:"frames"`
	Bytes            int64   `json:"bytes"`
	Updates          int64   `json:"updates"`
	UnknownChanges   int64   `json:"unknownChanges"`
	Pings            int64   `json:"pings"`
	Pongs            int64   `json:"pongs"`
	LastFrameAt      string  `json:"lastFrameAt,omitempty"`
}

type restTotals struct {
	Requests      int     `json:"requests"`
	Errors        int     `json:"errors"`
	LastElapsedMs float64 `json:"lastElapsedMs,omitempty"`
}

type reconcileTotals struct {
	Checks      int `json:"checks"`
	Compared    int `json:"compared"`
	Missing     int `json:"missing"`
	PriceDiffs  int `json:"priceDiffs"`
	StatusDiffs int `json:"statusDiffs"`
	LineDiffs   int `json:"lineDiffs"`
	Filled      int `json:"filled"`
}

// bookMetrics serialises report updates from the WS and REST goroutines.
type bookMetrics struct {
	mu        sync.Mutex
	rep       bookReport
	connStart time.Time
}

func newBookMetrics(book string) *bookMetrics {
	return &bookMetrics{rep: bookReport{Book: book}}
}

func (m *bookMetrics) connected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rep.WS.Connections > 0 {
		m.rep.WS.Reconnects++
	}
	m.rep.WS.Connections++
	m.rep.WS.Connected = true
	m.connStart = time.Now()
}

func (m *bookMetrics) disconnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rep.WS.Connected {
		m.rep.WS.ConnectedSeconds += time.Since(m.connStart).Seconds()
	}
	m.rep.WS.Connected = false
	m.rep.WS.Disconnects++
}

func (m *bookMetrics) frame(bytes int64, update, unknown bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rep.WS.Frames++
	m.rep.WS.Bytes += bytes
	if update {
		m.rep.WS.Updates++
	}
	if unknown {
		m.rep.WS.UnknownChanges++
	}
	m.rep.WS.LastFrameAt = clock.NowISO()
}

func (m *bookMetrics) ping() {
	m.mu.Lock()
	m.rep.WS.Pings++
	m.mu.Unlock()
}

func (m *bookMetrics) pong() {
	m.mu.Lock()
	m.rep.WS.Pongs++
	m.mu.Unlock()
}

func (m *bookMetrics) rest(elapsed float64) {
	m.mu.Lock()
	m.rep.REST.Requests++
	m.rep.REST.LastElapsedMs = elapsed
	m.mu.Unlock()
}

func (m *bookMetrics) restError(err error) {
	m.mu.Lock()
	m.rep.REST.Errors++
	m.rep.LastError = err.Error()
	m.mu.Unlock()
}

func (m *bookMetrics) reconcile(compared, missing, price, status, line, filled int) {
	m.mu.Lock()
	m.rep.Reconcile.Checks++
	m.rep.Reconcile.Compared += compared
	m.rep.Reconcile.Missing += missing
	m.rep.Reconcile.PriceDiffs += price
	m.rep.Reconcile.StatusDiffs += status
	m.rep.Reconcile.LineDiffs += line
	m.rep.Reconcile.Filled += filled
	m.mu.Unlock()
}

func (m *bookMetrics) discovery(changed bool) {
	m.mu.Lock()
	m.rep.DiscoveryChecks++
	if changed {
		m.rep.DiscoveryChanges++
	}
	m.mu.Unlock()
}

func (m *bookMetrics) subscribed(n int) {
	m.mu.Lock()
	m.rep.MarketsSubscribed = n
	m.mu.Unlock()
}

// snapshot returns a copy with uptime computed for the run window.
func (m *bookMetrics) snapshot(started, ended time.Time) bookReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := m.rep
	total := ended.Sub(started).Seconds()
	connected := rep.WS.ConnectedSeconds
	if rep.WS.Connected && !m.connStart.IsZero() {
		connected += time.Since(m.connStart).Seconds()
	}
	if total > 0 {
		rep.WS.UptimePct = connected / total * 100
		rep.WS.DowntimeSeconds = total - connected
		if rep.WS.DowntimeSeconds < 0 {
			rep.WS.DowntimeSeconds = 0
		}
	}
	return rep
}

// saveREST writes a REST body and its metadata sidecar, returning the relative
// base name for event logs.
func saveREST(rd *runDir, book, kind string, seq int, raw []byte, meta map[string]any) (string, error) {
	base := fmt.Sprintf("%s-%04d-%s", kind, seq, time.Now().UTC().Format("20060102T150405Z"))
	if err := rd.writeRaw(filepath.Join(book, "rest", base+".json"), raw); err != nil {
		return "", err
	}
	if err := rd.writeJSON(filepath.Join(book, "rest", base+".meta.json"), meta, true); err != nil {
		return "", err
	}
	return base, nil
}

// closeReason turns a read error into a WebSocket close code and text.
func closeReason(err error) (int, string) {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code, ce.Text
	}
	if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection") {
		return 1000, "local close"
	}
	return 1006, err.Error()
}

// americanValue converts DraftKings' rendered American string to an integer.
// It accepts the Unicode minus U+2212, plus signs and EVEN.
func americanValue(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	s = strings.ReplaceAll(s, "\u2212", "-")
	s = strings.ReplaceAll(s, "\u2013", "-")
	if strings.EqualFold(s, "EVEN") {
		return 100, true
	}
	v, err := strconv.Atoi(strings.TrimPrefix(s, "+"))
	if err != nil {
		return 0, false
	}
	return v, true
}

// floatValue converts a generic JSON number or numeric string.
func floatValue(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

// sleepCtx sleeps unless the context ends first. It returns false on cancel.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// jitter returns d with +/-20% randomness from the system clock.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	n := time.Now().UnixNano() % int64(d/5+1)
	return d - d/5 + time.Duration(n)
}
