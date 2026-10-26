package fanout

import (
	"time"

	"betstampodds/internal/board"
	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// The wire format is the board's, with times as epoch milliseconds (smaller
// than RFC 3339 and directly comparable with Date.now() in the page).

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

type WireEntry struct {
	Line      *float64     `json:"line,omitempty"`
	American  int          `json:"am"`
	Decimal   float64      `json:"dec,omitempty"`
	Status    model.Status `json:"st"`
	InPlay    bool         `json:"live,omitempty"`
	UpdatedAt int64        `json:"at"`
	BookTS    int64        `json:"bts,omitempty"`
	Prev      *board.Prev  `json:"prev,omitempty"`
}

func wireEntry(e *board.Entry) *WireEntry {
	if e == nil {
		return nil
	}
	return &WireEntry{Line: e.Line, American: e.American, Decimal: e.Decimal, Status: e.Status, InPlay: e.InPlay,
		UpdatedAt: ms(e.UpdatedAt), BookTS: ms(e.BookTS), Prev: e.Prev}
}

type WireHealth struct {
	State          board.HealthState `json:"state"`
	Since          int64             `json:"since"`
	LastDataAt     int64             `json:"lastDataAt,omitempty"`
	ConnectedSince int64             `json:"connectedSince,omitempty"`
	Reconnects     int64             `json:"reconnects"`
	Resyncs        int64             `json:"resyncs"`
	LastError      string            `json:"lastError,omitempty"`
	Frames         int64             `json:"frames"`
	Updates        int64             `json:"updates"`
	LastBeatAt     int64             `json:"lastBeatAt,omitempty"`
	Polls          int64             `json:"polls"`
	PollErrors     int64             `json:"pollErrors"`
	LastPollAt     int64             `json:"lastPollAt,omitempty"`
	LastPollError  string            `json:"lastPollError,omitempty"`
	Snapshots      int64             `json:"snapshots"`
	SnapshotErrors int64             `json:"snapshotErrors"`
	LastSnapAt     int64             `json:"lastSnapshotAt,omitempty"`
	PingLastMs     float64           `json:"pingMs,omitempty"`
	PingP50Ms      float64           `json:"pingP50Ms,omitempty"`
	HandshakeMs    float64           `json:"handshakeMs,omitempty"`
	HandshakeAt    int64             `json:"handshakeAt,omitempty"`
}

func wireHealth(h *board.Health) *WireHealth {
	if h == nil {
		return nil
	}
	return &WireHealth{State: h.State, Since: ms(h.Since), LastDataAt: ms(h.LastDataAt),
		ConnectedSince: ms(h.ConnectedSince), Reconnects: h.Reconnects, Resyncs: h.Resyncs, LastError: h.LastError,
		Frames: h.Frames, Updates: h.Updates, LastBeatAt: ms(h.LastHeartbeatAt),
		Polls: h.Polls, PollErrors: h.PollErrors, LastPollAt: ms(h.LastPollAt), LastPollError: h.LastPollError,
		Snapshots: h.Snapshots, SnapshotErrors: h.SnapshotErrors, LastSnapAt: ms(h.LastSnapshotAt),
		PingLastMs: h.PingLastMs, PingP50Ms: h.PingP50Ms, HandshakeMs: h.HandshakeMs, HandshakeAt: ms(h.HandshakeAt)}
}

type WireDelta struct {
	Seq        uint64           `json:"seq"`
	Kind       board.DeltaKind  `json:"k"`
	GameID     string           `json:"g,omitempty"`
	Identity   string           `json:"id,omitempty"`
	Book       string           `json:"b,omitempty"`
	Entry      *WireEntry       `json:"e,omitempty"`
	Removed    bool             `json:"rm,omitempty"`
	Best       *board.Best      `json:"best,omitempty"`
	Health     *WireHealth      `json:"h,omitempty"`
	Confirms   map[string]int64 `json:"c,omitempty"`
	Game       *WireGame        `json:"game,omitempty"`
	Source     model.Source     `json:"src,omitempty"`
	RecvAt     int64            `json:"recv,omitempty"`
	BookTS     int64            `json:"bts,omitempty"`
	AppliedAt  int64            `json:"applied"`
	ServerTime int64            `json:"now"` // when the hub encoded it
}

func wireDelta(d board.Delta) WireDelta {
	var confirms map[string]int64
	if len(d.Confirms) > 0 {
		confirms = make(map[string]int64, len(d.Confirms))
		for k, t := range d.Confirms {
			confirms[k] = ms(t)
		}
	}
	return WireDelta{Confirms: confirms, Seq: d.Seq, Kind: d.Kind, GameID: d.GameID, Identity: d.Identity, Book: d.Book,
		Entry: wireEntry(d.Entry), Removed: d.Removed, Best: d.Best, Health: wireHealth(d.Health),
		Game: wireGame(d.Game), Source: d.Source, RecvAt: ms(d.RecvAt), BookTS: ms(d.BookTS),
		AppliedAt: ms(d.AppliedAt), ServerTime: time.Now().UnixMilli()}
}

type WireGame struct {
	ID     string     `json:"id"`
	Sport  string     `json:"sport"`
	Status string     `json:"status"`
	Start  int64      `json:"start"`
	Away   model.Team `json:"away"`
	Home   model.Team `json:"home"`
	Live   bool       `json:"live,omitempty"`
}

func wireGame(g *board.GameView) *WireGame {
	if g == nil {
		return nil
	}
	return &WireGame{ID: g.ID, Sport: g.Sport, Status: g.Status, Start: ms(g.Start), Away: g.Away, Home: g.Home, Live: g.Live}
}

type WireSlot struct {
	GameID   string                `json:"g"`
	Identity string                `json:"id"`
	Family   string                `json:"family"`
	Outcome  string                `json:"outcome"`
	Entries  map[string]*WireEntry `json:"entries"`
	Best     *board.Best           `json:"best,omitempty"`
}

type WireState struct {
	Seq        uint64                        `json:"seq"`
	ServerTime int64                         `json:"serverTime"`
	Options    board.Options                 `json:"options"`
	Books      []book.Descriptor             `json:"books"`
	Games      []WireGame                    `json:"games"`
	Slots      []WireSlot                    `json:"slots"`
	Health     map[string]*WireHealth        `json:"health"`
	Latency    map[string]board.LatencySplit `json:"latency"`
	Clock      Clock                         `json:"clock"` // server clock vs NTP
}

func wireState(st board.State) WireState {
	w := WireState{Seq: st.Seq, ServerTime: time.Now().UnixMilli(), Options: st.Options, Books: st.Books,
		Games: make([]WireGame, 0, len(st.Games)), Slots: make([]WireSlot, 0, len(st.Slots)),
		Health: map[string]*WireHealth{}, Latency: st.Latency}
	for i := range st.Games {
		w.Games = append(w.Games, *wireGame(&st.Games[i]))
	}
	for _, s := range st.Slots {
		ws := WireSlot{GameID: s.GameID, Identity: s.Identity, Family: s.Family, Outcome: s.Outcome,
			Best: s.Best, Entries: map[string]*WireEntry{}}
		for b, e := range s.Entries {
			ws.Entries[b] = wireEntry(&e)
		}
		w.Slots = append(w.Slots, ws)
	}
	for k, h := range st.Health {
		w.Health[k] = wireHealth(&h)
	}
	return w
}

// WireStateOf is the JSON body for GET /api/board.
func WireStateOf(st board.State) WireState { return wireState(st) }
