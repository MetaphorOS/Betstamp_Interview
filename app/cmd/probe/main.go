// Command probe checks, from wherever it runs, whether each book's real data
// path works: the REST snapshot (with every request it makes) and the push
// stream up to its first subscription ack. Deploy it to the target region
// before the real app to learn whether datacenter IPs are blocked.
//
//	probe                    # run once, print JSON, exit
//	probe -serve :8080       # also serve GET /probe (re-runs) and GET /healthz
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/books"
	"betstampodds/internal/model"
	"betstampodds/internal/tlsinfo"
)

type streamResult struct {
	Connected   bool    `json:"connected"`
	ConnectMs   float64 `json:"connectMs,omitempty"`
	Updates     int     `json:"updates"`
	Heartbeats  int     `json:"heartbeats"`
	Error       string  `json:"error,omitempty"`
	ListenedSec float64 `json:"listenedSec"`
}

type bookResult struct {
	Book       string         `json:"book"`
	SnapshotOK bool           `json:"snapshotOk"`
	SnapshotMs float64        `json:"snapshotMs"`
	Games      int            `json:"games"`
	Quotes     int            `json:"quotes"`
	Skipped    map[string]int `json:"skipped,omitempty"`
	Error      string         `json:"error,omitempty"`
	Stream     *streamResult  `json:"stream,omitempty"`
}

type report struct {
	At       time.Time    `json:"at"`
	Region   string       `json:"region,omitempty"`
	Host     string       `json:"host"`
	EgressIP string       `json:"egressIp,omitempty"`
	Books    []bookResult `json:"books"`
}

// emitter counts what a stream does during the probe window.
type emitter struct {
	mu        sync.Mutex
	start     time.Time
	connected time.Duration
	updates   int
	beats     int
}

func (e *emitter) Emit(context.Context, model.Update) error {
	e.mu.Lock()
	e.updates++
	e.mu.Unlock()
	return nil
}
func (e *emitter) Connected() {
	e.mu.Lock()
	if e.connected == 0 {
		e.connected = time.Since(e.start)
	}
	e.mu.Unlock()
}
func (e *emitter) Pong(time.Duration)      {}
func (e *emitter) Handshake(time.Duration) {}

func (e *emitter) Heartbeat() {
	e.mu.Lock()
	e.beats++
	e.mu.Unlock()
}

func probeBook(ctx context.Context, b book.Book, listen time.Duration) bookResult {
	r := bookResult{Book: b.Descriptor().Name}
	t0 := time.Now()
	sctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	snap, err := b.Snapshot(sctx)
	cancel()
	r.SnapshotMs = float64(time.Since(t0).Microseconds()) / 1000
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.SnapshotOK, r.Games, r.Quotes, r.Skipped = true, len(snap.Games), len(snap.Quotes), snap.Skipped
	em := &emitter{start: time.Now()}
	stctx, cancel := context.WithTimeout(ctx, listen)
	defer cancel()
	serr := b.Stream(stctx, snap, em)
	em.mu.Lock()
	defer em.mu.Unlock()
	sr := &streamResult{Connected: em.connected > 0, Updates: em.updates, Heartbeats: em.beats,
		ListenedSec: time.Since(em.start).Seconds()}
	if em.connected > 0 {
		sr.ConnectMs = float64(em.connected.Microseconds()) / 1000
	}
	if serr != nil && stctx.Err() == nil {
		sr.Error = serr.Error()
	}
	r.Stream = sr
	return r
}

func egressIP(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if net.ParseIP(string(raw)) == nil {
		return ""
	}
	return string(raw)
}

func run(ctx context.Context, listen time.Duration) report {
	host, _ := os.Hostname()
	rep := report{At: time.Now().UTC(), Region: os.Getenv("FLY_REGION"), Host: host, EgressIP: egressIP(ctx)}
	reg := books.Default(books.Options{FanDuelBundlePath: os.Getenv("FD_MAIN_JS")})
	var wg sync.WaitGroup
	results := make([]bookResult, len(reg.Books()))
	for i, b := range reg.Books() {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = probeBook(ctx, b, listen) }()
	}
	wg.Wait()
	rep.Books = results
	return rep
}

func main() {
	serve := flag.String("serve", "", "listen address; empty = run once and exit")
	listen := flag.Duration("listen", 15*time.Second, "how long to keep each stream open")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	// The probe exists to give a straight answer about an environment's
	// reachability, and the TLS ClientHello is part of that answer.
	slog.Info("tls compatibility", "godebug", tlsinfo.CompatGODEBUG())

	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	rep := run(context.Background(), *listen)
	_ = out.Encode(rep)
	if *serve == "" {
		return
	}
	var mu sync.Mutex
	http.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	http.HandleFunc("GET /probe", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock() // one probe at a time: keep request volume at the books low
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(run(r.Context(), *listen))
	})
	slog.Info("probe serving", "addr", *serve)
	if err := http.ListenAndServe(*serve, nil); err != nil {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}
