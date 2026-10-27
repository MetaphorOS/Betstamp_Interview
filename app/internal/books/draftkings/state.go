package draftkings

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// Tombstone grace periods. A line move is an add of a new selection ID plus a
// remove of the old one in separate frames, in either order, 94% within 1 s.
// A market or event teardown (kickoff, live flapping) is re-added 3-7 s later.
const (
	selectionGrace = 2 * time.Second
	structureGrace = 15 * time.Second
)

type evState struct {
	homeID, awayID, sport string
	live, removed         bool
}

type mktState struct {
	family, stat, comparator, eventID string
	threshold                         float64 // TD scorer markets
	suspended                         bool
	sels                              map[string]bool
}

func (ms *mktState) class() mainClass {
	return mainClass{family: ms.family, stat: ms.stat, comparator: ms.comparator, threshold: ms.threshold}
}

type selState struct {
	identity, gameRef, marketID, family string
	line                                *float64
	american                            int
	decimal                             float64
	suspended                           bool
}

type tomb struct {
	sel    selState
	expiry time.Time
}

// Stats are per-stream-run counters, for logs and tests.
type Stats struct {
	Frames, Acks, Updates, LineMoves, Revived, Removed, SkippedSelections, Ignored int
}

// state is the DK stream state machine. It is seeded from a snapshot,
// deep-copied per stream run, and driven only through apply and expire, so it
// is deterministic under a fake clock.
type state struct {
	events        map[string]*evState
	ignoredEvents map[string]bool // events whose teams we cannot resolve
	markets       map[string]*mktState
	ignored       map[string]bool // known non-main markets: their selections are skipped
	goneMarkets   map[string]bool // removed main markets, until re-added
	sels          map[string]*selState
	skipSels      map[string]bool // selections deliberately not mapped
	gone          map[string]bool // tombstones that expired
	owner         map[string]string
	tombs         map[string]*tomb
	partials      []map[string]any
	leagueSport   map[string]string // DK leagueId -> sport
	stats         Stats
}

func newState() *state {
	return &state{
		events: map[string]*evState{}, ignoredEvents: map[string]bool{},
		markets: map[string]*mktState{}, ignored: map[string]bool{}, goneMarkets: map[string]bool{},
		sels: map[string]*selState{}, skipSels: map[string]bool{}, gone: map[string]bool{},
		owner: map[string]string{}, tombs: map[string]*tomb{}, leagueSport: map[string]string{},
	}
}

// clone deep-copies the seed so a stream run never mutates the snapshot.
func (s *state) clone() *state {
	c := newState()
	for k, v := range s.events {
		cp := *v
		c.events[k] = &cp
	}
	for k, v := range s.markets {
		cp := *v
		cp.sels = make(map[string]bool, len(v.sels))
		for id := range v.sels {
			cp.sels[id] = true
		}
		c.markets[k] = &cp
	}
	for k, v := range s.sels {
		cp := *v
		cp.line = model.CopyLine(v.line)
		c.sels[k] = &cp
	}
	for k, v := range s.tombs {
		cp := *v
		cp.sel.line = model.CopyLine(v.sel.line)
		c.tombs[k] = &cp
	}
	copySet(c.ignoredEvents, s.ignoredEvents)
	copySet(c.ignored, s.ignored)
	copySet(c.goneMarkets, s.goneMarkets)
	copySet(c.skipSels, s.skipSels)
	copySet(c.gone, s.gone)
	for k, v := range s.owner {
		c.owner[k] = v
	}
	c.partials = s.partials
	for k, v := range s.leagueSport {
		c.leagueSport[k] = v
	}
	return c
}

func copySet(dst, src map[string]bool) {
	for k := range src {
		dst[k] = true
	}
}

func slotOwnerKey(gameRef, identity string) string { return gameRef + "|" + identity }

// Wire shapes for the WebSocket. Removes are arrays of ID strings.
type wsFrame struct {
	ID                        string          `json:"id"`
	Event                     string          `json:"event"`
	Data                      json.RawMessage `json:"data"`
	WebsocketPublishTimestamp string          `json:"websocketPublishTimestamp"`
}

type wsEntities struct {
	Events     []dkEvent     `json:"events"`
	Markets    []dkMarket    `json:"markets"`
	Selections []dkSelection `json:"selections"`
}

type wsRemove struct {
	Events     []string `json:"events"`
	Markets    []string `json:"markets"`
	Selections []string `json:"selections"`
}

type wsUpdate struct {
	Data struct {
		Add    wsEntities `json:"add"`
		Change wsEntities `json:"change"`
		Remove wsRemove   `json:"remove"`
	} `json:"data"`
	Metadata struct {
		CreatedTime  string `json:"createdTime"`
		ReceivedTime string `json:"receivedTime"`
	} `json:"metadata"`
}

// frameResult is everything apply learned from one frame.
type frameResult struct {
	updates []model.Update
	ack     bool
	bookTS  time.Time
}

// apply processes one raw frame read at recvAt.
func (s *state) apply(raw []byte, recvAt time.Time) (frameResult, error) {
	var f wsFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return frameResult{}, fmt.Errorf("draftkings: decode frame: %w", err)
	}
	s.stats.Frames++
	switch f.Event {
	case "subscribed":
		s.stats.Acks++
		return frameResult{ack: true}, nil
	case "update":
	default:
		return frameResult{}, nil
	}
	var u wsUpdate
	if err := json.Unmarshal(f.Data, &u); err != nil {
		return frameResult{}, fmt.Errorf("draftkings: decode update: %w", err)
	}
	bookTS, _ := time.Parse(time.RFC3339Nano, f.WebsocketPublishTimestamp)
	a := applier{s: s, recvAt: recvAt, bookTS: bookTS.UTC()}
	if err := a.run(&u); err != nil {
		return frameResult{}, err
	}
	s.stats.Updates += len(a.out)
	return frameResult{updates: a.out, bookTS: a.bookTS}, nil
}

type applier struct {
	s      *state
	recvAt time.Time
	bookTS time.Time
	out    []model.Update
}

func (a *applier) run(u *wsUpdate) error {
	d := &u.Data
	// 1. events
	for _, e := range d.Add.Events {
		if err := a.upsertEvent(e, true); err != nil {
			return err
		}
	}
	for _, e := range d.Change.Events {
		if err := a.upsertEvent(e, false); err != nil {
			return err
		}
	}
	// 2. markets
	for _, m := range d.Add.Markets {
		a.addMarket(m)
	}
	for _, m := range d.Change.Markets {
		if err := a.changeMarket(m); err != nil {
			return err
		}
	}
	// 3. selections, adds first
	for _, sel := range d.Add.Selections {
		if err := a.applySelection(sel, true); err != nil {
			return err
		}
	}
	for _, sel := range d.Change.Selections {
		if err := a.applySelection(sel, false); err != nil {
			return err
		}
	}
	// 4. removes
	for _, id := range d.Remove.Selections {
		a.s.tombstone(id, a.recvAt.Add(selectionGrace))
	}
	for _, id := range d.Remove.Markets {
		a.removeMarket(id)
	}
	for _, id := range d.Remove.Events {
		a.removeEvent(id)
	}
	return nil
}

func (a *applier) upsertEvent(e dkEvent, isAdd bool) error {
	s := a.s
	id := e.ID.String()
	ev := s.events[id]
	if ev == nil {
		if s.ignoredEvents[id] {
			return nil
		}
		home, away, ok := s.resolveNew(e)
		if !ok {
			// Not an NFL pairing we can map; never guess.
			s.ignoredEvents[id] = true
			s.stats.Ignored++
			return nil
		}
		// A new game: the board cannot place it without a snapshot.
		return fmt.Errorf("%w: new event %s (%s @ %s)", book.ErrResync, id, away.ID, home.ID)
	}
	if home, away, ok := resolveTeams(ev.sport, e.Participants); ok {
		ev.homeID, ev.awayID = home.ID, away.ID
	}
	if isAdd {
		ev.removed = false
	}
	if e.Status != "" {
		if live := isLive(e.Status); live != ev.live {
			ev.live = live
			for _, sid := range s.sortedSels(func(sel *selState) bool { return sel.gameRef == id }) {
				a.emit(sid)
			}
		}
	}
	return nil
}

// resolveNew resolves an event we have not seen, by its league when DK says
// which one, otherwise against every league we subscribe to.
func (s *state) resolveNew(e dkEvent) (home, away model.Team, ok bool) {
	if sport := s.leagueSport[e.LeagueID.String()]; sport != "" {
		return resolveTeams(sport, e.Participants)
	}
	for _, sport := range s.leagueSport {
		if h, a, ok := resolveTeams(sport, e.Participants); ok {
			return h, a, true
		}
	}
	return model.Team{}, model.Team{}, false
}

func (a *applier) addMarket(m dkMarket) {
	s := a.s
	id := m.ID.String()
	c, ok := classify(m)
	if !ok || s.events[m.EventID.String()] == nil {
		s.ignored[id] = true
		return
	}
	delete(s.ignored, id)
	delete(s.goneMarkets, id)
	ms := s.markets[id]
	if ms == nil {
		ms = &mktState{sels: map[string]bool{}}
		// Selections revived before their market was re-added rejoin it.
		for sid, sel := range s.sels {
			if sel.marketID == id {
				ms.sels[sid] = true
			}
		}
		s.markets[id] = ms
	}
	ms.family, ms.stat, ms.comparator, ms.threshold, ms.eventID = c.family, c.stat, c.comparator, c.threshold, m.EventID.String()
	if m.IsSuspended != nil && *m.IsSuspended != ms.suspended {
		ms.suspended = *m.IsSuspended
		a.emitMarket(ms)
	}
}

func (a *applier) changeMarket(m dkMarket) error {
	s := a.s
	id := m.ID.String()
	ms := s.markets[id]
	if ms == nil {
		if s.ignored[id] || s.goneMarkets[id] {
			return nil
		}
		return fmt.Errorf("%w: change for unknown market %s", book.ErrResync, id)
	}
	if m.IsSuspended != nil && *m.IsSuspended != ms.suspended {
		ms.suspended = *m.IsSuspended
		a.emitMarket(ms)
	}
	return nil
}

func (a *applier) emitMarket(ms *mktState) {
	ids := make([]string, 0, len(ms.sels))
	for id := range ms.sels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a.emit(id)
	}
}

// applySelection resolves a selection in the order the spec fixes:
// live ID, tombstoned ID (same-ID re-add), replaced ID (line move, adds only),
// known main market, ignored market, otherwise resync.
func (a *applier) applySelection(d dkSelection, isAdd bool) error {
	s := a.s
	id := d.ID.String()
	marketID := d.MarketID.String()
	var cur *selState
	switch {
	case s.sels[id] != nil:
		cur = s.sels[id]
	case s.tombs[id] != nil:
		cp := s.tombs[id].sel
		cur = &cp
		delete(s.tombs, id)
		s.sels[id] = cur
		s.stats.Revived++
	case isAdd && d.ReplacedSelectionID != "" && (s.sels[d.ReplacedSelectionID] != nil || s.tombs[d.ReplacedSelectionID] != nil):
		old := d.ReplacedSelectionID
		var base selState
		if o := s.sels[old]; o != nil {
			base = *o
		} else {
			base = s.tombs[old].sel
		}
		if ms := s.markets[base.marketID]; ms != nil {
			delete(ms.sels, old)
		}
		delete(s.sels, old)
		delete(s.tombs, old)
		cur = &base
		s.sels[id] = cur
		s.stats.LineMoves++
	case marketID != "" && s.markets[marketID] != nil:
		ms := s.markets[marketID]
		ev := s.events[ms.eventID]
		var identity string
		var line *float64
		if ms.family == model.FamilyPlayer {
			player, outcome, ln, ok := propFacets(ms.class(), d)
			if !ok {
				if d.Participants != nil && (len(d.Participants) == 0 || d.Participants[0].Type != "Player") {
					// A team (D/ST) on a TD scorer list: never mapped.
					s.skipSels[id] = true
					s.stats.SkippedSelections++
					return nil
				}
				return fmt.Errorf("%w: prop selection %s in market %s has no player or line", book.ErrResync, id, marketID)
			}
			q := model.PropQuote(model.Quote{}, player, ms.stat, ms.comparator, outcome, ln)
			identity, line = model.IdentityKey(q), q.Line
		} else {
			outcome, ok := outcomeFor(ms.family, d.OutcomeType, ev.homeID, ev.awayID)
			if !ok {
				return fmt.Errorf("%w: selection %s outcome %q in market %s", book.ErrResync, id, d.OutcomeType, marketID)
			}
			identity = model.IdentityKey(model.Quote{
				Family: ms.family, Period: model.PeriodFull, Subject: "event",
				Stat: ms.stat, Comparator: ms.comparator, Outcome: outcome,
			})
		}
		cur = &selState{identity: identity, gameRef: ms.eventID, marketID: marketID, family: ms.family, line: line}
		s.sels[id] = cur
	case marketID != "" && s.ignored[marketID]:
		s.skipSels[id] = true
		s.stats.SkippedSelections++
		return nil
	case s.skipSels[id]:
		s.stats.SkippedSelections++
		return nil
	default:
		return fmt.Errorf("%w: unknown selection %s (market %q, replaces %q)", book.ErrResync, id, marketID, d.ReplacedSelectionID)
	}
	delete(s.gone, id)

	// Merge partial fields: absent means unchanged.
	if marketID != "" && marketID != cur.marketID {
		if ms := s.markets[cur.marketID]; ms != nil {
			delete(ms.sels, id)
		}
		cur.marketID = marketID
	}
	if ms := s.markets[cur.marketID]; ms != nil {
		ms.sels[id] = true
	}
	if d.Points != nil && cur.family != model.FamilyMoneyline {
		cur.line = model.CopyLine(d.Points)
	}
	if d.DisplayOdds != nil {
		if v, err := parseAmerican(d.DisplayOdds.American); err == nil {
			cur.american = v
		}
	}
	if d.TrueOdds != nil {
		cur.decimal = *d.TrueOdds
	}
	if d.Status != "" {
		cur.suspended = selSuspended(d.Status)
	}

	key := slotOwnerKey(cur.gameRef, cur.identity)
	if prev := s.owner[key]; prev != "" && prev != id && s.sels[prev] != nil {
		// A second live selection took this slot without naming the first as
		// replaced. Retire the old one quietly; the owner check keeps it from
		// ever emitting Removed over the new one.
		s.tombstone(prev, a.recvAt.Add(selectionGrace))
	}
	s.owner[key] = id
	a.emit(id)
	return nil
}

func (a *applier) removeMarket(id string) {
	s := a.s
	ms := s.markets[id]
	if ms == nil {
		return
	}
	exp := a.recvAt.Add(structureGrace)
	for sid := range ms.sels {
		s.tombstone(sid, exp)
	}
	for _, t := range s.tombs {
		if t.sel.marketID == id && t.expiry.Before(exp) {
			t.expiry = exp
		}
	}
	delete(s.markets, id)
	s.goneMarkets[id] = true
}

func (a *applier) removeEvent(id string) {
	s := a.s
	ev := s.events[id]
	if ev == nil {
		return
	}
	ev.removed = true
	exp := a.recvAt.Add(structureGrace)
	for _, sid := range s.sortedSels(func(sel *selState) bool { return sel.gameRef == id }) {
		s.tombstone(sid, exp)
	}
	for _, t := range s.tombs {
		if t.sel.gameRef == id && t.expiry.Before(exp) {
			t.expiry = exp
		}
	}
}

// tombstone moves a live selection to the tombstones, or extends an existing
// tombstone. It never shortens an expiry.
func (s *state) tombstone(id string, expiry time.Time) {
	if sel := s.sels[id]; sel != nil {
		if ms := s.markets[sel.marketID]; ms != nil {
			delete(ms.sels, id)
		}
		delete(s.sels, id)
		s.tombs[id] = &tomb{sel: *sel, expiry: expiry}
		return
	}
	if t := s.tombs[id]; t != nil && t.expiry.Before(expiry) {
		t.expiry = expiry
	}
}

// expire deletes tombstones past their expiry and emits Removed for each one
// that still owns its slot, so an old ID never wipes its replacement.
func (s *state) expire(now time.Time) []model.Update {
	var ids []string
	for id, t := range s.tombs {
		if !now.Before(t.expiry) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	var out []model.Update
	for _, id := range ids {
		t := s.tombs[id]
		delete(s.tombs, id)
		s.gone[id] = true
		key := slotOwnerKey(t.sel.gameRef, t.sel.identity)
		if o := s.owner[key]; o != "" && o != id {
			continue
		}
		delete(s.owner, key)
		u := s.update(&t.sel, now, time.Time{})
		u.Status = model.StatusRemoved
		out = append(out, u)
		s.stats.Removed++
	}
	s.stats.Updates += len(out)
	return out
}

func (a *applier) emit(id string) {
	sel := a.s.sels[id]
	if sel == nil || sel.american == 0 {
		return
	}
	a.out = append(a.out, a.s.update(sel, a.recvAt, a.bookTS))
}

func (s *state) update(sel *selState, recvAt, bookTS time.Time) model.Update {
	status := model.StatusOpen
	if sel.suspended {
		status = model.StatusSuspended
	} else if ms := s.markets[sel.marketID]; ms != nil && ms.suspended {
		status = model.StatusSuspended
	}
	live := false
	if ev := s.events[sel.gameRef]; ev != nil {
		live = ev.live
	}
	return model.Update{
		Book: Name, GameRef: sel.gameRef, Identity: sel.identity,
		Line: model.CopyLine(sel.line), American: sel.american, Decimal: sel.decimal,
		Status: status, InPlay: live, BookTS: bookTS, RecvAt: recvAt, Source: model.SourceStream,
	}
}

func (s *state) sortedSels(pred func(*selState) bool) []string {
	var ids []string
	for id, sel := range s.sels {
		if pred(sel) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
