package board

import (
	"sort"
	"time"
)

const latencyWindowSize = 4096

// LatencyStats summarises a book's recent stream updates.
//   - Process: AppliedAt - RecvAt, our own processing, in microseconds.
//   - BookToUs: RecvAt - BookTS, book publish to our socket read, in
//     milliseconds; only for books whose feed carries a publish time.
type LatencyStats struct {
	Samples      int     `json:"samples"`
	ProcessP50us float64 `json:"processP50us"`
	ProcessP95us float64 `json:"processP95us"`
	ProcessMaxus float64 `json:"processMaxus"`
	BookSamples  int     `json:"bookSamples"`
	BookP50ms    float64 `json:"bookP50ms"`
	BookP95ms    float64 `json:"bookP95ms"`
	BookMaxms    float64 `json:"bookMaxms"`
}

// LatencySplit is a book's latency over all stream updates, and separately
// for pregame and in-play quotes, which move at very different rates.
type LatencySplit struct {
	All     LatencyStats `json:"all"`
	Pregame LatencyStats `json:"pregame"`
	Live    LatencyStats `json:"live"`
}

type splitWindow struct{ all, pre, live *latencyWindow }

func newSplitWindow() *splitWindow {
	return &splitWindow{newLatencyWindow(latencyWindowSize), newLatencyWindow(latencyWindowSize), newLatencyWindow(latencyWindowSize)}
}

func (w *splitWindow) add(process time.Duration, recvAt, bookTS time.Time, inPlay bool) {
	w.all.add(process, recvAt, bookTS)
	if inPlay {
		w.live.add(process, recvAt, bookTS)
	} else {
		w.pre.add(process, recvAt, bookTS)
	}
}

func (w *splitWindow) stats() LatencySplit {
	return LatencySplit{All: w.all.stats(), Pregame: w.pre.stats(), Live: w.live.stats()}
}

// latencyWindow is a fixed-size ring of the most recent samples.
type latencyWindow struct {
	process []float64
	bookLag []float64
	pi, bi  int
	pn, bn  int
}

func newLatencyWindow(n int) *latencyWindow {
	return &latencyWindow{process: make([]float64, n), bookLag: make([]float64, n)}
}

func (w *latencyWindow) add(process time.Duration, recvAt, bookTS time.Time) {
	w.process[w.pi] = float64(process.Microseconds())
	w.pi = (w.pi + 1) % len(w.process)
	w.pn = min(w.pn+1, len(w.process))
	if !bookTS.IsZero() {
		w.bookLag[w.bi] = float64(recvAt.Sub(bookTS).Microseconds()) / 1000
		w.bi = (w.bi + 1) % len(w.bookLag)
		w.bn = min(w.bn+1, len(w.bookLag))
	}
}

func (w *latencyWindow) stats() LatencyStats {
	s := LatencyStats{Samples: w.pn, BookSamples: w.bn}
	s.ProcessP50us, s.ProcessP95us, s.ProcessMaxus = quantiles(w.process[:w.pn])
	s.BookP50ms, s.BookP95ms, s.BookMaxms = quantiles(w.bookLag[:w.bn])
	return s
}

func quantiles(v []float64) (p50, p95, max float64) {
	if len(v) == 0 {
		return 0, 0, 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	at := func(q float64) float64 { return s[min(len(s)-1, int(q*float64(len(s))))] }
	return at(0.5), at(0.95), s[len(s)-1]
}
