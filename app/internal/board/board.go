// Package board is the single-goroutine source of truth for what the page
// shows: canonical games, one slot per game/market/side, per-book entries,
// the best price, and per-book health. Every visible change becomes a Delta
// with a global, strictly increasing Seq.
package board

import (
	"context"
	"sort"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/match"
	"betstampodds/internal/model"
)

// HealthState of one book's data.
type HealthState string

const (
	HealthStarting HealthState = "starting"
	HealthLive     HealthState = "live"     // stream connected, recent heartbeat
	HealthDegraded HealthState = "degraded" // stream down, polls succeeding
	HealthStale    HealthState = "stale"    // no successful data for StaleAfter
	HealthDown     HealthState = "down"     // snapshots failing
)

// Health is published by the supervisor on every change.
type Health struct {
	State          HealthState `json:"state"`
	Since          time.Time   `json:"since"`
	LastDataAt     time.Time   `json:"lastDataAt,omitzero"`
	ConnectedSince time.Time   `json:"connectedSince,omitzero"`
	Reconnects     int64       `json:"reconnects"`
	Resyncs        int64       `json:"resyncs"`
	Frames         int64       `json:"frames"`
	Updates        int64       `json:"updates"`
	LastError      string      `json:"lastError,omitempty"`
}

// excluded reports whether a book in this state is shown greyed and never best.
func (h Health) excluded() bool { return h.State == HealthStale || h.State == HealthDown }

// Prev is the last visible price before the current one.
type Prev struct {
	Line     *float64 `json:"line,omitempty"`
	American int      `json:"american"`
}

// Entry is one book's quote in a slot.
type Entry struct {
	Line      *float64     `json:"line,omitempty"`
	American  int          `json:"american"`
	Decimal   float64      `json:"decimal,omitempty"`
	Status    model.Status `json:"status"`
	InPlay    bool         `json:"inPlay"`
	UpdatedAt time.Time    `json:"updatedAt"` // RecvAt of the update that set it
	BookTS    time.Time    `json:"bookTs,omitzero"`
	Prev      *Prev        `json:"prev,omitempty"`
}

// Best is the winning book for a slot.
type Best struct {
	Book     string   `json:"book"`
	Line     *float64 `json:"line,omitempty"`
	American int      `json:"american"`
}

// DeltaKind says what a Delta describes.
type DeltaKind string

const (
	KindSlot   DeltaKind = "slot"
	KindHealth DeltaKind = "health"
	KindGame   DeltaKind = "game"
)

// Delta is one visible change.
type Delta struct {
	Seq       uint64       `json:"seq"`
	Kind      DeltaKind    `json:"kind"`
	GameID    string       `json:"gameId,omitempty"`
	Identity  string       `json:"identity,omitempty"`
	Book      string       `json:"book,omitempty"`
	Entry     *Entry       `json:"entry,omitempty"`   // nil with Removed: the book left the slot
	Removed   bool         `json:"removed,omitempty"` // entry (slot) or game removed
	Best      *Best        `json:"best,omitempty"`    // nil: no eligible price
	Health    *Health      `json:"health,omitempty"`
	Game      *GameView    `json:"game,omitempty"`
	Source    model.Source `json:"source,omitempty"` // how the update that caused this arrived
	RecvAt    time.Time    `json:"recvAt,omitzero"`
	BookTS    time.Time    `json:"bookTs,omitzero"`
	AppliedAt time.Time    `json:"appliedAt"`
}

// GameView is a canonical game as the page shows it.
type GameView struct {
	ID    string     `json:"id"`
	Start time.Time  `json:"start"`
	Away  model.Team `json:"away"`
	Home  model.Team `json:"home"`
	Live  bool       `json:"live"`
}

// SlotView is one slot in a State.
type SlotView struct {
	GameID   string           `json:"gameId"`
	Identity string           `json:"identity"`
	Family   string           `json:"family"`
	Outcome  string           `json:"outcome"`
	Entries  map[string]Entry `json:"entries"`
	Best     *Best            `json:"best,omitempty"`
}

// State is a full board snapshot.
type State struct {
	Seq     uint64                  `json:"seq"`
	Books   []book.Descriptor       `json:"books"`
	Games   []GameView              `json:"games"`
	Slots   []SlotView              `json:"slots"`
	Health  map[string]Health       `json:"health"`
	Latency map[string]LatencyStats `json:"latency"`
	Dropped map[string]int          `json:"dropped,omitempty"` // updates for unknown games, per book
}

type slot struct {
	gameID, identity, family, outcome string
	entries                           map[string]Entry
	best                              *Best
}

type gameState struct {
	view  GameView
	slots int // slots with at least one entry
}

// core is the board logic, owned by one goroutine (or a test).
type core struct {
	now      func() time.Time
	matcher  *match.Matcher
	books    []book.Descriptor
	kind     map[string]book.PriceKind
	gameRefs map[string]map[string]string // book -> book game id -> canonical id
	games    map[string]*gameState
	slots    map[string]*slot
	health   map[string]Health
	latency  map[string]*latencyWindow
	dropped  map[string]int
	seq      uint64
	subs     map[int]chan Delta
	nextSub  int
}

func newCore(books []book.Descriptor, now func() time.Time) *core {
	c := &core{
		now: now, matcher: match.New(), books: books, kind: map[string]book.PriceKind{},
		gameRefs: map[string]map[string]string{}, games: map[string]*gameState{},
		slots: map[string]*slot{}, health: map[string]Health{}, latency: map[string]*latencyWindow{},
		dropped: map[string]int{}, subs: map[int]chan Delta{},
	}
	for _, d := range books {
		c.kind[d.Name] = d.PriceKind
		c.health[d.Name] = Health{State: HealthStarting, Since: now()}
		c.latency[d.Name] = newLatencyWindow(latencyWindowSize)
	}
	return c
}

// MatchTolerance is how far apart two books' start times may be for one game.
const MatchTolerance = 3 * time.Hour

func (c *core) eligible(name string) bool {
	return c.kind[name] == book.PriceAmerican && !c.health[name].excluded()
}

func (c *core) publish(d Delta) {
	c.seq++
	d.Seq = c.seq
	for id, ch := range c.subs {
		select {
		case ch <- d:
		default:
			// A full subscriber is dropped and must resync; it never blocks the board.
			close(ch)
			delete(c.subs, id)
		}
	}
}

// isMain: the board shows full-game main markets only.
func isMain(identity string) (model.IdentityParts, bool) {
	p, ok := model.ParseIdentity(identity)
	if !ok || p.Class != "main" || p.Period != model.PeriodFull {
		return p, false
	}
	switch p.Family {
	case model.FamilyMoneyline, model.FamilySpread, model.FamilyTotal:
		return p, true
	}
	return p, false
}

func (c *core) applySnapshot(name string, snap *book.Snapshot) {
	refs := map[string]string{}
	for _, g := range snap.Games {
		p, ok := c.matcher.Resolve(g, MatchTolerance)
		if !ok {
			continue
		}
		refs[g.BookGameID] = p.Game.ID
		gs := c.games[p.Game.ID]
		if gs == nil {
			gs = &gameState{view: GameView{ID: p.Game.ID, Start: p.Game.Start, Away: p.Game.Away, Home: p.Game.Home}}
			c.games[p.Game.ID] = gs
		}
		if g.Live && !gs.view.Live {
			gs.view.Live = true
			if gs.slots > 0 {
				v := gs.view
				c.publish(Delta{Kind: KindGame, GameID: v.ID, Game: &v, AppliedAt: c.now()})
			}
		}
	}
	c.gameRefs[name] = refs

	seen := map[string]bool{}
	recv := c.now()
	for _, q := range snap.Quotes {
		u := model.UpdateFromQuote(q, model.SourceSnapshot, snap.AsOf, snap.AsOf)
		if gid := refs[u.GameRef]; gid != "" {
			seen[gid+"|"+u.Identity] = true
		}
		c.apply(u, recv)
	}
	// Entries missing from the snapshot are gone, unless newer than it.
	keys := make([]string, 0)
	for key, s := range c.slots {
		if e, ok := s.entries[name]; ok && !seen[key] && e.UpdatedAt.Before(snap.AsOf) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		c.removeEntry(c.slots[key], name, Delta{AppliedAt: recv})
	}
}

func (c *core) apply(u model.Update, appliedAt time.Time) {
	gid := c.gameRefs[u.Book][u.GameRef]
	if gid == "" {
		c.dropped[u.Book]++
		return
	}
	p, ok := isMain(u.Identity)
	if !ok {
		return
	}
	if lw := c.latency[u.Book]; lw != nil && u.Source == model.SourceStream {
		lw.add(appliedAt.Sub(u.RecvAt), u.RecvAt, u.BookTS)
	}
	key := gid + "|" + u.Identity
	s := c.slots[key]
	if s == nil {
		if u.Status == model.StatusRemoved {
			return
		}
		s = &slot{gameID: gid, identity: u.Identity, family: p.Family, outcome: p.Outcome, entries: map[string]Entry{}}
		c.slots[key] = s
	}
	old, had := s.entries[u.Book]
	// Poll/snapshot data never overwrites an entry newer than its request.
	if had && u.Source != model.SourceStream && old.UpdatedAt.After(u.AsOf) {
		return
	}
	base := Delta{Source: u.Source, RecvAt: u.RecvAt, BookTS: u.BookTS, AppliedAt: appliedAt}
	if u.Status == model.StatusRemoved {
		if had {
			c.removeEntry(s, u.Book, base)
		}
		return
	}
	e := Entry{
		Line: model.CopyLine(u.Line), American: u.American, Decimal: u.Decimal,
		Status: u.Status, InPlay: u.InPlay, UpdatedAt: u.RecvAt, BookTS: u.BookTS,
	}
	if had {
		e.Prev = old.Prev
		if model.LineString(old.Line) != model.LineString(e.Line) || old.American != e.American {
			e.Prev = &Prev{Line: model.CopyLine(old.Line), American: old.American}
		}
		if !visibleChange(old, e) {
			// Same price: refresh freshness silently.
			if e.UpdatedAt.After(old.UpdatedAt) {
				old.UpdatedAt, old.BookTS, old.Decimal = e.UpdatedAt, e.BookTS, e.Decimal
				s.entries[u.Book] = old
			}
			return
		}
	} else if gs := c.games[gid]; gs != nil && len(s.entries) == 0 {
		// The slot gets its first entry; the game appears with its first slot.
		gs.slots++
		if gs.slots == 1 {
			v := gs.view
			c.publish(Delta{Kind: KindGame, GameID: gid, Game: &v, AppliedAt: appliedAt})
		}
	}
	s.entries[u.Book] = e
	s.best = PickBest(s.family, s.outcome, s.entries, c.eligible)
	ec := e
	base.Kind, base.GameID, base.Identity, base.Book, base.Entry, base.Best = KindSlot, gid, u.Identity, u.Book, &ec, copyBest(s.best)
	c.publish(base)
	if u.InPlay {
		if gs := c.games[gid]; gs != nil && !gs.view.Live {
			gs.view.Live = true
			v := gs.view
			c.publish(Delta{Kind: KindGame, GameID: gid, Game: &v, AppliedAt: appliedAt})
		}
	}
}

func visibleChange(a, b Entry) bool {
	return model.LineString(a.Line) != model.LineString(b.Line) || a.American != b.American ||
		a.Status != b.Status || a.InPlay != b.InPlay
}

func (c *core) removeEntry(s *slot, name string, base Delta) {
	delete(s.entries, name)
	s.best = PickBest(s.family, s.outcome, s.entries, c.eligible)
	base.Kind, base.GameID, base.Identity, base.Book, base.Removed, base.Best = KindSlot, s.gameID, s.identity, name, true, copyBest(s.best)
	c.publish(base)
	if len(s.entries) == 0 {
		delete(c.slots, s.gameID+"|"+s.identity)
		if gs := c.games[s.gameID]; gs != nil {
			gs.slots--
			if gs.slots == 0 {
				v := gs.view
				c.publish(Delta{Kind: KindGame, GameID: s.gameID, Game: &v, Removed: true, AppliedAt: base.AppliedAt})
			}
		}
	}
}

func copyBest(b *Best) *Best {
	if b == nil {
		return nil
	}
	cp := *b
	cp.Line = model.CopyLine(b.Line)
	return &cp
}

func (c *core) setHealth(name string, h Health) {
	old := c.health[name]
	c.health[name] = h
	if old.State == h.State && old.LastError == h.LastError && old.Reconnects == h.Reconnects && old.Resyncs == h.Resyncs {
		// Counters and timestamps move constantly; publish only state changes.
		return
	}
	now := c.now()
	hc := h
	c.publish(Delta{Kind: KindHealth, Book: name, Health: &hc, AppliedAt: now})
	if old.excluded() == h.excluded() {
		return
	}
	// Eligibility flipped: recompute every slot this book quotes.
	keys := make([]string, 0)
	for key, s := range c.slots {
		if _, ok := s.entries[name]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		s := c.slots[key]
		nb := PickBest(s.family, s.outcome, s.entries, c.eligible)
		if sameBest(nb, s.best) {
			continue
		}
		s.best = nb
		c.publish(Delta{Kind: KindSlot, GameID: s.gameID, Identity: s.identity, Best: copyBest(nb), AppliedAt: now})
	}
}

func (c *core) state() State {
	st := State{
		Seq: c.seq, Books: append([]book.Descriptor(nil), c.books...),
		Health: map[string]Health{}, Latency: map[string]LatencyStats{}, Dropped: map[string]int{},
	}
	for k, v := range c.health {
		st.Health[k] = v
	}
	for k, v := range c.latency {
		st.Latency[k] = v.stats()
	}
	for k, v := range c.dropped {
		st.Dropped[k] = v
	}
	for _, gs := range c.games {
		if gs.slots > 0 {
			st.Games = append(st.Games, gs.view)
		}
	}
	sort.Slice(st.Games, func(i, j int) bool {
		if !st.Games[i].Start.Equal(st.Games[j].Start) {
			return st.Games[i].Start.Before(st.Games[j].Start)
		}
		return st.Games[i].ID < st.Games[j].ID
	})
	for _, s := range c.slots {
		v := SlotView{GameID: s.gameID, Identity: s.identity, Family: s.family, Outcome: s.outcome, Best: copyBest(s.best), Entries: map[string]Entry{}}
		for k, e := range s.entries {
			v.Entries[k] = e
		}
		st.Slots = append(st.Slots, v)
	}
	sort.Slice(st.Slots, func(i, j int) bool {
		if st.Slots[i].GameID != st.Slots[j].GameID {
			return st.Slots[i].GameID < st.Slots[j].GameID
		}
		return st.Slots[i].Identity < st.Slots[j].Identity
	})
	return st
}

func (c *core) subscribe(buf int) (int, chan Delta) {
	c.nextSub++
	ch := make(chan Delta, buf)
	c.subs[c.nextSub] = ch
	return c.nextSub, ch
}

func (c *core) unsubscribe(id int) {
	if ch, ok := c.subs[id]; ok {
		close(ch)
		delete(c.subs, id)
	}
}

// Board runs core on one goroutine fed by a channel.
type Board struct {
	inbox chan func(*core)
	done  chan struct{}
	c     *core
}

// SubscriberBuffer is each subscriber's channel capacity.
const SubscriberBuffer = 1024

// New builds a board for the registered books. Call Run.
func New(books []book.Descriptor) *Board {
	return newBoard(books, func() time.Time { return time.Now() })
}

func newBoard(books []book.Descriptor, now func() time.Time) *Board {
	return &Board{inbox: make(chan func(*core), 4096), done: make(chan struct{}), c: newCore(books, now)}
}

// Run processes commands until ctx ends.
func (b *Board) Run(ctx context.Context) {
	defer close(b.done)
	for {
		select {
		case <-ctx.Done():
			for id := range b.c.subs {
				b.c.unsubscribe(id)
			}
			return
		case fn := <-b.inbox:
			fn(b.c)
		}
	}
}

func (b *Board) do(ctx context.Context, fn func(*core)) error {
	select {
	case b.inbox <- fn:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return context.Canceled
	}
}

// Apply queues one update; it blocks until accepted or ctx is done.
func (b *Board) Apply(ctx context.Context, u model.Update) error {
	return b.do(ctx, func(c *core) { c.apply(u, c.now()) })
}

// ApplySnapshot replaces one book's game mapping and reconciles its entries.
func (b *Board) ApplySnapshot(ctx context.Context, name string, snap *book.Snapshot) error {
	return b.do(ctx, func(c *core) { c.applySnapshot(name, snap) })
}

// SetHealth publishes a book's health.
func (b *Board) SetHealth(ctx context.Context, name string, h Health) error {
	return b.do(ctx, func(c *core) { c.setHealth(name, h) })
}

// Snapshot returns the current state; State.Seq is the last applied delta.
func (b *Board) Snapshot(ctx context.Context) (uint64, State, error) {
	reply := make(chan State, 1)
	if err := b.do(ctx, func(c *core) { reply <- c.state() }); err != nil {
		return 0, State{}, err
	}
	select {
	case st := <-reply:
		return st.Seq, st, nil
	case <-ctx.Done():
		return 0, State{}, ctx.Err()
	}
}

// Subscribe returns a delta channel and a cancel. The channel is closed if
// the subscriber falls SubscriberBuffer behind; it must then resync.
func (b *Board) Subscribe(ctx context.Context) (<-chan Delta, func(), error) {
	_, ch, cancel, err := b.SubscribeWithState(ctx)
	return ch, cancel, err
}

// SubscribeWithState atomically takes a snapshot and subscribes, so the
// first delta on the channel is exactly State.Seq+1.
func (b *Board) SubscribeWithState(ctx context.Context) (State, <-chan Delta, func(), error) {
	type res struct {
		st State
		id int
		ch chan Delta
	}
	reply := make(chan res, 1)
	if err := b.do(ctx, func(c *core) {
		id, ch := c.subscribe(SubscriberBuffer)
		reply <- res{st: c.state(), id: id, ch: ch}
	}); err != nil {
		return State{}, nil, nil, err
	}
	var r res
	select {
	case r = <-reply:
	case <-ctx.Done():
		return State{}, nil, nil, ctx.Err()
	}
	cancel := func() {
		// If the board has stopped, Run already closed every channel.
		select {
		case b.inbox <- func(c *core) { c.unsubscribe(r.id) }:
		case <-b.done:
		}
	}
	return r.st, r.ch, cancel, nil
}
