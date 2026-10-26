// Package fanout pushes the board to browsers over Server-Sent Events. It
// holds ONE board subscription, encodes each delta once, keeps a ring of
// recent encoded frames for Last-Event-ID resume, and copies bytes to every
// client. A client that falls behind is dropped and resumes on reconnect.
package fanout

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"betstampodds/internal/board"
)

// Source is what the hub needs from the board.
type Source interface {
	SubscribeWithState(ctx context.Context) (board.State, <-chan board.Delta, func(), error)
	Snapshot(ctx context.Context) (uint64, board.State, error)
}

// Config tunes the hub; zero values take defaults.
type Config struct {
	RingSize     int           // recent frames kept for resume; default 8192
	ClientBuffer int           // frames queued per client before it is dropped; default 512
	PingEvery    time.Duration // default 15s
	Logger       *slog.Logger
}

type frame struct {
	seq uint64
	b   []byte // a complete SSE event
}

type client struct {
	ch      chan []byte
	dropped atomic.Bool
}

// Hub is the SSE fanout.
type Hub struct {
	src Source
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	ring    []frame // oldest first
	last    uint64  // last seq received from the board
	clients map[*client]struct{}
	ready   chan struct{}

	// metrics
	deltas      atomic.Int64
	bytesOut    atomic.Int64
	dropped     atomic.Int64
	connects    atomic.Int64
	resumes     atomic.Int64
	resyncs     atomic.Int64
	encodeNanos atomic.Int64
}

// New builds a hub; call Run.
func New(src Source, cfg Config) *Hub {
	if cfg.RingSize == 0 {
		cfg.RingSize = 8192
	}
	if cfg.ClientBuffer == 0 {
		cfg.ClientBuffer = 512
	}
	if cfg.PingEvery == 0 {
		cfg.PingEvery = 15 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Hub{src: src, cfg: cfg, log: cfg.Logger.With("component", "fanout"),
		clients: map[*client]struct{}{}, ready: make(chan struct{})}
}

// Run consumes the board until ctx ends, resubscribing if the board drops us.
func (h *Hub) Run(ctx context.Context) {
	first := true
	for ctx.Err() == nil {
		st, deltas, cancel, err := h.src.SubscribeWithState(ctx)
		if err != nil {
			return
		}
		h.mu.Lock()
		if !first && st.Seq != h.last {
			// We missed deltas: no ring entry can bridge the gap, so every
			// client must take a fresh snapshot.
			h.log.Warn("board subscription lost; resetting clients", "from", h.last, "to", st.Seq)
			h.ring = h.ring[:0]
			for c := range h.clients {
				h.dropLocked(c)
			}
		}
		h.last = st.Seq
		h.mu.Unlock()
		if first {
			close(h.ready)
			first = false
		}
		for d := range deltas {
			h.publish(d)
		}
		cancel()
	}
}

func (h *Hub) publish(d board.Delta) {
	t := time.Now()
	data, err := json.Marshal(wireDelta(d))
	if err != nil {
		h.log.Error("encode delta", "error", err)
		return
	}
	b := sseEvent(d.Seq, "delta", data)
	h.encodeNanos.Add(int64(time.Since(t)))
	h.deltas.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = d.Seq
	if len(h.ring) >= h.cfg.RingSize {
		n := copy(h.ring, h.ring[len(h.ring)/4:]) // drop the oldest quarter at once
		h.ring = h.ring[:n]
	}
	h.ring = append(h.ring, frame{seq: d.Seq, b: b})
	for c := range h.clients {
		select {
		case c.ch <- b:
		default:
			h.dropLocked(c)
		}
	}
}

func (h *Hub) dropLocked(c *client) {
	if c.dropped.CompareAndSwap(false, true) {
		close(c.ch)
		delete(h.clients, c)
		h.dropped.Add(1)
	}
}

func sseEvent(seq uint64, event string, data []byte) []byte {
	b := make([]byte, 0, len(data)+48)
	if seq > 0 {
		b = append(b, "id: "...)
		b = strconv.AppendUint(b, seq, 10)
		b = append(b, '\n')
	}
	b = append(b, "event: "...)
	b = append(b, event...)
	b = append(b, "\ndata: "...)
	b = append(b, data...)
	return append(b, "\n\n"...)
}

// attach registers a client and returns what to send first: either the ring
// frames after resumeFrom, or (when that gap is not in the ring) nil, which
// means "send a snapshot". It also returns the seq the backlog ends at.
func (h *Hub) attach(c *client, resumeFrom uint64, haveResume bool) (backlog [][]byte, resumed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
	if !haveResume {
		return nil, false
	}
	if resumeFrom == h.last {
		return [][]byte{}, true
	}
	if len(h.ring) == 0 || resumeFrom+1 < h.ring[0].seq || resumeFrom > h.last {
		return nil, false
	}
	for _, f := range h.ring {
		if f.seq > resumeFrom {
			backlog = append(backlog, f.b)
		}
	}
	return backlog, true
}

func (h *Hub) detach(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		if c.dropped.CompareAndSwap(false, true) {
			close(c.ch)
		}
	}
}

// ServeHTTP is GET /stream: snapshot-then-deltas, or resume from
// Last-Event-ID. Deltas go out immediately: no batching.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	select {
	case <-h.ready:
	case <-r.Context().Done():
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache, no-transform")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	h.connects.Add(1)

	resumeFrom, haveResume := uint64(0), false
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			resumeFrom, haveResume = n, true
		}
	} else if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			resumeFrom, haveResume = n, true
		}
	}

	c := &client{ch: make(chan []byte, h.cfg.ClientBuffer)}
	backlog, resumed := h.attach(c, resumeFrom, haveResume)
	defer h.detach(c)

	write := func(b []byte) bool {
		n, err := w.Write(b)
		h.bytesOut.Add(int64(n))
		return err == nil
	}
	if !write([]byte("retry: 1000\n\n")) {
		return
	}
	var after uint64 // drop live frames at or before what the client already has
	if resumed {
		h.resumes.Add(1)
		for _, b := range backlog {
			if !write(b) {
				return
			}
		}
		after = resumeFrom
		if len(backlog) > 0 {
			after = lastSeqOf(backlog)
		}
	} else {
		if haveResume {
			h.resyncs.Add(1)
		}
		seq, st, err := h.src.Snapshot(r.Context())
		if err != nil {
			return
		}
		data, err := json.Marshal(wireState(st))
		if err != nil {
			return
		}
		if !write(sseEvent(seq, "snapshot", data)) {
			return
		}
		after = seq
	}
	fl.Flush()

	ping := time.NewTicker(h.cfg.PingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			// A named event, not a comment, so the page can read serverTime.
			if !write(sseEvent(0, "ping", fmt.Appendf(nil, `{"serverTime":%d}`, time.Now().UnixMilli()))) {
				return
			}
			fl.Flush()
		case b, ok := <-c.ch:
			if !ok {
				// Dropped for being slow. Closing the response makes the
				// browser reconnect with Last-Event-ID and resume or resync.
				return
			}
			if seqOf(b) <= after {
				continue
			}
			if !write(b) {
				return
			}
			// Drain whatever else is queued before one flush: this
			// coalesces syscalls under load without delaying anything.
			for drained := false; !drained; {
				select {
				case b, ok := <-c.ch:
					if !ok {
						fl.Flush()
						return
					}
					if seqOf(b) > after && !write(b) {
						return
					}
				default:
					drained = true
				}
			}
			fl.Flush()
		}
	}
}

func seqOf(b []byte) uint64 {
	// frames start with "id: <seq>\n"
	if len(b) < 5 || string(b[:4]) != "id: " {
		return 0
	}
	var n uint64
	for _, c := range b[4:] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

func lastSeqOf(frames [][]byte) uint64 { return seqOf(frames[len(frames)-1]) }

// Stats is the hub's /metrics view.
type Stats struct {
	Clients        int     `json:"clients"`
	Deltas         int64   `json:"deltas"`
	BytesOut       int64   `json:"bytesOut"`
	DroppedClients int64   `json:"droppedClients"`
	Connects       int64   `json:"connects"`
	Resumes        int64   `json:"resumes"`
	Resyncs        int64   `json:"resyncs"`
	RingFrames     int     `json:"ringFrames"`
	LastSeq        uint64  `json:"lastSeq"`
	AvgEncodeUs    float64 `json:"avgEncodeUs"`
}

// Stats reports hub counters.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	clients, ring, last := len(h.clients), len(h.ring), h.last
	h.mu.Unlock()
	s := Stats{Clients: clients, Deltas: h.deltas.Load(), BytesOut: h.bytesOut.Load(),
		DroppedClients: h.dropped.Load(), Connects: h.connects.Load(), Resumes: h.resumes.Load(),
		Resyncs: h.resyncs.Load(), RingFrames: ring, LastSeq: last}
	if s.Deltas > 0 {
		s.AvgEncodeUs = float64(h.encodeNanos.Load()) / float64(s.Deltas) / 1000
	}
	return s
}
