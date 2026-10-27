// Command server is the whole app in one binary: one ingestion per book
// (supervisor), the board, and the SSE fanout to any number of browsers.
//
//	GET /stream      SSE: snapshot, then every delta immediately; Last-Event-ID resume
//	GET /api/board   full board JSON
//	GET /healthz     200 while the process serves; body has per-book health
//	GET /metrics     Prometheus text
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"betstampodds/internal/board"
	"betstampodds/internal/book"
	"betstampodds/internal/books"
	"betstampodds/internal/clocksync"
	"betstampodds/internal/fanout"
	"betstampodds/internal/supervisor"
	"betstampodds/internal/tlsinfo"
)

func main() {
	addr := flag.String("addr", ":"+envOr("PORT", "8080"), "listen address")
	bundle := flag.String("fd-bundle", os.Getenv("FD_MAIN_JS"), "captured FanDuel main.js fallback (default: research/captures/fd-main.js if present)")
	pmBest := flag.Bool("pm-best", os.Getenv("PM_IN_BEST") != "0", "let prediction markets (priced at the ask) win best price")
	ntp := flag.String("ntp", envOr("NTP_SERVER", "time.google.com"), "SNTP server for the clock-offset correction (empty = off)")
	debug := flag.Bool("debug", os.Getenv("DEBUG_ENDPOINTS") == "1", "enable POST /debug/kill/{book} and /debug/pause/{book}")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)

	// GODEBUG is fixed at init and chooses the key exchanges the TLS client
	// offers; one book's edge treats those shapes differently, so record which
	// compatibility settings are off instead of trusting the deploy config.
	log.Info("tls compatibility", "godebug", tlsinfo.CompatGODEBUG())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *bundle == "" {
		// Local runs: use the captured bundle as a fallback when it is there.
		for _, p := range []string{"research/captures/fd-main.js", "../research/captures/fd-main.js"} {
			if _, err := os.Stat(p); err == nil {
				*bundle = p
				break
			}
		}
	}
	reg := books.Default(books.Options{FanDuelBundlePath: *bundle})
	descs := make([]book.Descriptor, 0)
	for _, b := range reg.Books() {
		descs = append(descs, b.Descriptor())
	}
	bd := board.New(descs, board.Options{ProbabilityInBest: *pmBest})
	boardCtx, stopBoard := context.WithCancel(context.Background())
	boardDone := make(chan struct{})
	go func() { bd.Run(boardCtx); close(boardDone) }()

	hub := fanout.New(bd, fanout.Config{Logger: log})
	go hub.Run(ctx)

	var wg sync.WaitGroup
	sups := map[string]*supervisor.Supervisor{}
	for _, b := range reg.Books() {
		s := supervisor.New(b, bd, supervisor.Config{Logger: log})
		sups[b.Descriptor().Name] = s
		wg.Add(1)
		go func() { defer wg.Done(); s.Run(ctx) }()
	}

	// Books block by IP reputation (DK's Akamai 403s some datacenter IPs), and
	// a cloud machine's outgoing IP is its host's: log it so a block can be
	// tied to an address.
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			log.Info("egress ip", "ip", string(raw), "region", os.Getenv("FLY_REGION"), "machine", os.Getenv("FLY_MACHINE_ID"))
		}
	}()

	// Book-to-us latency subtracts a book's clock from ours, so measure how
	// far ours is from NTP and let the page correct for it.
	if *ntp != "" {
		go func() {
			for {
				// Every 60 s: well inside ClockMaxAge, so one failed
				// measurement does not invalidate the clock.
				if off, rtt, err := clocksync.Best(*ntp, 4, 2*time.Second); err == nil {
					hub.SetClock(off, rtt, time.Now())
					log.Info("clock offset", "server", *ntp, "offsetMs", float64(off.Microseconds())/1000,
						"rttMs", float64(rtt.Microseconds())/1000)
				} else {
					log.Warn("clock offset unavailable", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Minute):
				}
			}
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("GET /stream", hub)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		// The page times this round trip: browser <-> server latency.
		writeJSON(w, map[string]any{"serverTime": time.Now().UnixMilli()})
	})
	mux.HandleFunc("GET /api/board", func(w http.ResponseWriter, r *http.Request) {
		_, st, err := bd.Snapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		ws := fanout.WireStateOf(st)
		ws.Clock = hub.Clock()
		writeJSON(w, ws)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, st, err := bd.Snapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		// Always 200 while serving: one book being down is a page state,
		// not a reason for the platform to restart us.
		writeJSON(w, map[string]any{"ok": true, "books": st.Health, "hub": hub.Stats()})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		_, st, err := bd.Snapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeMetrics(w, st, hub.Stats(), hub.Clock())
	})
	mux.HandleFunc("GET /debug", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"enabled": *debug})
	})
	if *debug {
		// Demo controls: simulate a dropped socket, or a book unreachable for
		// a while (stream and polls both down), to show reconnect and stale.
		mux.HandleFunc("POST /debug/kill/{book}", func(w http.ResponseWriter, r *http.Request) {
			s := sups[r.PathValue("book")]
			if s == nil {
				http.NotFound(w, r)
				return
			}
			log.Info("debug kill", "book", r.PathValue("book"))
			s.KillStream()
			writeJSON(w, map[string]any{"ok": true})
		})
		mux.HandleFunc("POST /debug/pause/{book}", func(w http.ResponseWriter, r *http.Request) {
			s := sups[r.PathValue("book")]
			if s == nil {
				http.NotFound(w, r)
				return
			}
			d, err := time.ParseDuration(r.URL.Query().Get("for"))
			if err != nil || d <= 0 || d > 5*time.Minute {
				d = 45 * time.Second
			}
			log.Info("debug pause", "book", r.PathValue("book"), "for", d.String())
			s.Pause(d)
			writeJSON(w, map[string]any{"ok": true, "for": d.String()})
		})
	}
	mux.Handle("GET /", staticHandler())

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// SSE handlers stop when this context is cancelled.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		log.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	t := time.Now()
	shutCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	wg.Wait()
	stopBoard()
	<-boardDone
	log.Info("shutdown", "took", time.Since(t).String())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

var healthValue = map[board.HealthState]int{
	board.HealthStarting: 0, board.HealthLive: 1, board.HealthDegraded: 2, board.HealthStale: 3, board.HealthDown: 4,
}

func writeMetrics(w http.ResponseWriter, st board.State, hs fanout.Stats, clock fanout.Clock) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	names := make([]string, 0, len(st.Health))
	for n := range st.Health {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "# HELP odds_book_health 0=starting 1=live 2=degraded 3=stale 4=down")
	for _, n := range names {
		h := st.Health[n]
		split := st.Latency[n]
		fmt.Fprintf(w, "odds_book_health{book=%q} %d\n", n, healthValue[h.State])
		fmt.Fprintf(w, "odds_book_reconnects_total{book=%q} %d\n", n, h.Reconnects)
		fmt.Fprintf(w, "odds_book_resyncs_total{book=%q} %d\n", n, h.Resyncs)
		fmt.Fprintf(w, "odds_book_updates_total{book=%q} %d\n", n, h.Updates)
		fmt.Fprintf(w, "odds_book_ping_ms{book=%q} %g\n", n, h.PingLastMs)
		fmt.Fprintf(w, "odds_book_handshake_ms{book=%q} %g\n", n, h.HandshakeMs)
		for _, ph := range []struct {
			name string
			l    board.LatencyStats
		}{{"all", split.All}, {"pregame", split.Pregame}, {"live", split.Live}} {
			l := ph.l
			fmt.Fprintf(w, "odds_book_process_us{book=%q,phase=%q,q=\"0.5\"} %g\n", n, ph.name, l.ProcessP50us)
			fmt.Fprintf(w, "odds_book_process_us{book=%q,phase=%q,q=\"0.95\"} %g\n", n, ph.name, l.ProcessP95us)
			if l.BookSamples > 0 {
				// Raw: add odds_clock_offset_ms (only while odds_clock_valid
				// is 1) for the NTP-corrected value.
				fmt.Fprintf(w, "odds_book_to_us_ms{book=%q,phase=%q,q=\"0.5\"} %g\n", n, ph.name, l.BookP50ms)
				fmt.Fprintf(w, "odds_book_to_us_ms{book=%q,phase=%q,q=\"0.95\"} %g\n", n, ph.name, l.BookP95ms)
			}
		}
		fmt.Fprintf(w, "odds_board_dropped_updates_total{book=%q} %d\n", n, st.Dropped[n])
	}
	valid, age := 0, -1.0
	if clock.Valid {
		valid = 1
	}
	if clock.MeasuredAt != 0 {
		age = float64(time.Now().UnixMilli()-clock.MeasuredAt) / 1000
	}
	fmt.Fprintf(w, "odds_clock_offset_ms %g\n", clock.OffsetMs)
	fmt.Fprintf(w, "odds_clock_error_ms %g\n", clock.ErrMs)
	fmt.Fprintf(w, "odds_clock_valid %d\n", valid)
	fmt.Fprintf(w, "odds_clock_age_seconds %g\n", age)
	fmt.Fprintf(w, "odds_board_seq %d\n", st.Seq)
	fmt.Fprintf(w, "odds_board_slots %d\n", len(st.Slots))
	fmt.Fprintf(w, "odds_sse_clients %d\n", hs.Clients)
	fmt.Fprintf(w, "odds_sse_deltas_total %d\n", hs.Deltas)
	fmt.Fprintf(w, "odds_sse_bytes_total %d\n", hs.BytesOut)
	fmt.Fprintf(w, "odds_sse_dropped_clients_total %d\n", hs.DroppedClients)
	fmt.Fprintf(w, "odds_sse_resumes_total %d\n", hs.Resumes)
	fmt.Fprintf(w, "odds_sse_resyncs_total %d\n", hs.Resyncs)
	fmt.Fprintf(w, "odds_sse_encode_us_avg %g\n", hs.AvgEncodeUs)
}
