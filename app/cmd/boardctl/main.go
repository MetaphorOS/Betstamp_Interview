// Command boardctl runs the real books into a board for a fixed time and
// prints every delta, every health transition, and a per-book summary.
//
//	boardctl -seconds 120
//	boardctl -seconds 120 -kill-after 40s          # kill one stream once, watch recovery
//	boardctl -book draftkings -seconds 60
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"betstampodds/internal/board"
	"betstampodds/internal/book"
	"betstampodds/internal/books"
	"betstampodds/internal/clocksync"
	"betstampodds/internal/model"
	"betstampodds/internal/supervisor"
)

func main() {
	seconds := flag.Int("seconds", 120, "how long to run")
	killAfter := flag.Duration("kill-after", 0, "cancel one book's stream once after this long (0 = never)")
	killBook := flag.String("kill-book", "", "book to kill (default: the first one running)")
	only := flag.String("book", "", "run only this book")
	quiet := flag.Bool("quiet", false, "do not print each delta")
	bundle := flag.String("fd-bundle", "", "path to the captured FanDuel main.js fallback")
	ntp := flag.String("ntp", "time.google.com", "SNTP server for the clock-offset correction (empty = skip)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if *bundle == "" {
		for _, p := range []string{"captures/fd-main.js", "../captures/fd-main.js"} {
			if _, err := os.Stat(p); err == nil {
				*bundle = p
				break
			}
		}
	}
	var clockOffset time.Duration
	if *ntp != "" {
		if off, rtt, err := clocksync.Best(*ntp, 4, 2*time.Second); err == nil {
			clockOffset = off
			fmt.Printf("== clock offset vs %s: %+.1fms (rtt %.1fms); book→us is corrected by it\n", *ntp,
				float64(off.Microseconds())/1000, float64(rtt.Microseconds())/1000)
		} else {
			fmt.Printf("== clock offset unknown (%v); book→us is raw\n", err)
		}
	}
	reg := books.Default(books.Options{FanDuelBundlePath: *bundle})
	var run []book.Book
	for _, b := range reg.Books() {
		if *only == "" || b.Descriptor().Name == *only {
			run = append(run, b)
		}
	}
	if len(run) == 0 {
		fmt.Fprintf(os.Stderr, "no book named %q\n", *only)
		os.Exit(2)
	}
	descs := make([]book.Descriptor, len(run))
	for i, b := range run {
		descs[i] = b.Descriptor()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(*seconds)*time.Second)
	defer cancel()

	bd := board.New(descs)
	boardCtx, stopBoard := context.WithCancel(context.Background())
	go bd.Run(boardCtx)
	defer stopBoard()

	pr := newPrinter(!*quiet, clockOffset)
	st, deltas, unsub, err := bd.SubscribeWithState(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pr.load(st)
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		for d := range deltas {
			pr.delta(d)
		}
	}()

	sups := map[string]*supervisor.Supervisor{}
	var wg sync.WaitGroup
	started := time.Now()
	for _, b := range run {
		s := supervisor.New(b, bd, supervisor.Config{Logger: log})
		sups[b.Descriptor().Name] = s
		wg.Add(1)
		go func() { defer wg.Done(); s.Run(ctx) }()
	}
	if *killAfter > 0 {
		target := *killBook
		if target == "" {
			target = run[0].Descriptor().Name
		}
		go func() {
			select {
			case <-ctx.Done():
			case <-time.After(*killAfter):
				if s := sups[target]; s != nil {
					fmt.Printf("== %s killing %s stream\n", time.Now().UTC().Format("15:04:05.000"), target)
					s.KillStream()
				}
			}
		}()
	}

	<-ctx.Done()
	shutdown := time.Now()
	wg.Wait()
	_, final, err := bd.Snapshot(context.Background())
	unsub()
	<-printed
	fmt.Fprintf(os.Stderr, "shutdown took %s\n", time.Since(shutdown).Round(time.Millisecond))
	if err == nil {
		pr.summary(final, sups, time.Since(started))
	}
}

type printer struct {
	verbose bool
	offset  time.Duration // server - local
	mu      sync.Mutex
	games   map[string]board.GameView
	slots   map[string]map[string]board.Entry
	deltas  map[string]int
	health  map[string][]string
}

func newPrinter(verbose bool, offset time.Duration) *printer {
	return &printer{verbose: verbose, offset: offset, games: map[string]board.GameView{}, slots: map[string]map[string]board.Entry{},
		deltas: map[string]int{}, health: map[string][]string{}}
}

func (p *printer) load(st board.State) {
	for _, g := range st.Games {
		p.games[g.ID] = g
	}
	for _, s := range st.Slots {
		p.slots[s.GameID+"|"+s.Identity] = s.Entries
	}
}

func gameName(g board.GameView) string {
	short := func(t model.Team) string {
		if t.Abbr != "" {
			return t.Abbr
		}
		return strings.TrimPrefix(t.ID, "nfl-")
	}
	return short(g.Away) + "@" + short(g.Home)
}

func side(identity string) string {
	p, _ := model.ParseIdentity(identity)
	return p.Family + " " + strings.TrimPrefix(p.Outcome, "nfl-")
}

func price(e board.Entry) string {
	s := fmt.Sprintf("%+d", e.American)
	if e.Line != nil {
		s = fmt.Sprintf("%+g %s", *e.Line, s)
	}
	if e.Status != model.StatusOpen {
		s += " (" + string(e.Status) + ")"
	}
	if e.InPlay {
		s += " live"
	}
	return s
}

func (p *printer) delta(d board.Delta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UTC().Format("15:04:05.000")
	switch d.Kind {
	case board.KindHealth:
		p.health[d.Book] = append(p.health[d.Book], fmt.Sprintf("%s %s", now, d.Health.State))
		fmt.Printf("== %s health %-10s %-8s reconnects=%d resyncs=%d err=%q\n", now, d.Book, d.Health.State, d.Health.Reconnects, d.Health.Resyncs, d.Health.LastError)
		return
	case board.KindGame:
		if d.Game != nil {
			p.games[d.GameID] = *d.Game
		}
		return
	}
	key := d.GameID + "|" + d.Identity
	if d.Book != "" {
		p.deltas[d.Book]++
		if p.slots[key] == nil {
			p.slots[key] = map[string]board.Entry{}
		}
		if d.Removed {
			delete(p.slots[key], d.Book)
		} else if d.Entry != nil {
			p.slots[key][d.Book] = *d.Entry
		}
	}
	if !p.verbose {
		return
	}
	var parts []string
	names := make([]string, 0, len(p.slots[key]))
	for n := range p.slots[key] {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%s", n, price(p.slots[key][n])))
	}
	best := "-"
	if d.Best != nil {
		best = d.Best.Book
	}
	lat := ""
	if d.Source == model.SourceStream {
		lat = fmt.Sprintf(" proc=%dus", d.AppliedAt.Sub(d.RecvAt).Microseconds())
	} else if d.Source != "" {
		lat = " via " + string(d.Source)
	}
	if d.Source == model.SourceStream && !d.BookTS.IsZero() {
		lat += fmt.Sprintf(" book→us=%.1fms", p.ms(d.RecvAt.Sub(d.BookTS)))
	}
	what := d.Book
	if d.Removed {
		what += " REMOVED"
	} else if d.Book == "" {
		what = "best-only"
	}
	fmt.Printf("%6d %s %-9s %-22s [%s] best=%s (%s)%s\n", d.Seq, now, gameName(p.games[d.GameID]), side(d.Identity), strings.Join(parts, " "), best, what, lat)
}

// ms converts a local-clock difference against a book timestamp into
// corrected milliseconds: recv(local) + offset = recv(server).
func (p *printer) ms(d time.Duration) float64 {
	return float64((d + p.offset).Microseconds()) / 1000
}

func (p *printer) summary(st board.State, sups map[string]*supervisor.Supervisor, ran time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Printf("\n==== summary after %s (seq=%d)\n", ran.Round(time.Second), st.Seq)
	both := 0
	for _, s := range st.Slots {
		if len(s.Entries) > 1 {
			both++
		}
	}
	fmt.Printf("board: games=%d slots=%d slots-with-2+-books=%d\n", len(st.Games), len(st.Slots), both)
	for _, d := range st.Books {
		games := map[string]bool{}
		slots, live := 0, 0
		for _, s := range st.Slots {
			if e, ok := s.Entries[d.Name]; ok {
				slots++
				games[s.GameID] = true
				if e.InPlay {
					live++
				}
			}
		}
		h := st.Health[d.Name]
		l := st.Latency[d.Name]
		fmt.Printf("%-10s health=%s games=%d slots=%d in-play-slots=%d deltas=%d reconnects=%d resyncs=%d frames=%d updates=%d dropped=%d\n",
			d.Name, h.State, len(games), slots, live, p.deltas[d.Name], h.Reconnects, h.Resyncs, h.Frames, h.Updates, st.Dropped[d.Name])
		fmt.Printf("%-10s latency: process p50=%.0fus p95=%.0fus max=%.0fus (n=%d)", "", l.ProcessP50us, l.ProcessP95us, l.ProcessMaxus, l.Samples)
		if l.BookSamples > 0 {
			off := float64(p.offset.Microseconds()) / 1000
			fmt.Printf("  book→us p50=%.1fms p95=%.1fms max=%.1fms (n=%d, clock-corrected)", l.BookP50ms+off, l.BookP95ms+off, l.BookMaxms+off, l.BookSamples)
		} else {
			fmt.Printf("  book→us n/a (feed has no publish timestamp)")
		}
		fmt.Println()
		if hs := p.health[d.Name]; len(hs) > 0 {
			fmt.Printf("%-10s health transitions: %s\n", "", strings.Join(hs, " → "))
		}
		if h.LastError != "" {
			fmt.Printf("%-10s last error: %s\n", "", h.LastError)
		}
	}
}
