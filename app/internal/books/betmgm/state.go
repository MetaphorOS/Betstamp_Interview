package betmgm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// state is the stream seed: every fixture with its grouped markets. It is
// deep-copied per stream run (markets are immutable, so they are shared).
type state struct {
	fixtures map[string]*fixture
	topics   []string // "<fixture context>|all", sorted
	stats    Stats
}

// Stats are per-stream-run counters, for logs and tests.
type Stats struct {
	Messages, MarketUpdates, MarketDeletes, Ignored, Unknown int
}

func newState() *state { return &state{fixtures: map[string]*fixture{}} }

func (s *state) clone() *state {
	c := &state{fixtures: make(map[string]*fixture, len(s.fixtures)), topics: s.topics}
	for id, fx := range s.fixtures {
		c.fixtures[id] = fx.clone()
	}
	return c
}

// SignalR JSON hub protocol: messages end with 0x1e and one WebSocket frame
// may carry several.
const recordSep = '\x1e'

type hubMessage struct {
	Type         int               `json:"type"`
	Target       string            `json:"target"`
	InvocationID string            `json:"invocationId"`
	Error        string            `json:"error"`
	Arguments    []json.RawMessage `json:"arguments"`
}

type pushMessage struct {
	MessageType string          `json:"messageType"`
	Payload     json.RawMessage `json:"payload"`
	Topic       string          `json:"topic"`
	Timestamp   string          `json:"timestamp"` // event time, truncated to the second
	LatencyMs   int64           `json:"latencyMs"` // push send time minus Timestamp
}

// sentAt is when BetMGM's push server sent the message. Timestamp alone
// is whole seconds, but latencyMs is measured from that same truncated
// value: in captures, receipt - (timestamp + latencyMs) held at 28-75 ms
// (the network hop) while latencyMs ranged 0-950. The sum is a millisecond
// send time, comparable to DraftKings' websocketPublishTimestamp; it says
// nothing about time spent upstream of the push server. Zero when absent or
// implausible (the first frames after subscribing can carry stale values).
func (p pushMessage) sentAt(recvAt time.Time) time.Time {
	ts, err := time.Parse(time.RFC3339Nano, p.Timestamp)
	if err != nil || p.LatencyMs <= 0 {
		return time.Time{}
	}
	sent := ts.Add(time.Duration(p.LatencyMs) * time.Millisecond)
	if sent.After(recvAt.Add(time.Second)) || sent.Before(recvAt.Add(-time.Minute)) {
		return time.Time{}
	}
	return sent
}

// msgKind is what one hub message meant to the transport.
type msgKind int

const (
	msgOther msgKind = iota
	msgHandshake
	msgCompletion
	msgPing
	msgClose
	msgData
)

type msgResult struct {
	kind    msgKind
	updates []model.Update
	errMsg  string
}

// splitFrame returns the hub messages in one WebSocket frame.
func splitFrame(raw []byte) [][]byte {
	var out [][]byte
	for _, part := range strings.Split(string(raw), string(recordSep)) {
		if strings.TrimSpace(part) != "" {
			out = append(out, []byte(part))
		}
	}
	return out
}

// apply handles one hub message read at recvAt.
func (s *state) apply(raw []byte, recvAt time.Time) (msgResult, error) {
	var m hubMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return msgResult{}, fmt.Errorf("betmgm: decode message: %w", err)
	}
	s.stats.Messages++
	switch m.Type {
	case 0:
		// The handshake reply is "{}"; an error there is fatal.
		if m.Error != "" {
			return msgResult{kind: msgClose, errMsg: m.Error}, nil
		}
		return msgResult{kind: msgHandshake}, nil
	case 3:
		return msgResult{kind: msgCompletion, errMsg: m.Error}, nil
	case 6:
		return msgResult{kind: msgPing}, nil
	case 7:
		return msgResult{kind: msgClose, errMsg: m.Error}, nil
	case 1:
	default:
		return msgResult{}, nil
	}
	if m.Target != "Receive" || len(m.Arguments) == 0 {
		return msgResult{}, nil
	}
	var p pushMessage
	if err := json.Unmarshal(m.Arguments[0], &p); err != nil {
		return msgResult{}, fmt.Errorf("betmgm: decode push: %w", err)
	}
	ups, err := s.push(p, recvAt)
	if err != nil {
		return msgResult{}, err
	}
	if sent := p.sentAt(recvAt); !sent.IsZero() {
		for i := range ups {
			ups[i].BookTS = sent
		}
	}
	return msgResult{kind: msgData, updates: ups}, nil
}

func (s *state) push(p pushMessage, recvAt time.Time) ([]model.Update, error) {
	switch p.MessageType {
	case "OptionMarketUpdate":
		var pl struct {
			FixtureID    ID        `json:"fixtureId"`
			OptionMarket mgmMarket `json:"optionMarket"`
		}
		if err := json.Unmarshal(p.Payload, &pl); err != nil {
			return nil, fmt.Errorf("betmgm: decode market update: %w", err)
		}
		fx := s.fixtures[pl.FixtureID.String()]
		if fx == nil {
			s.stats.Ignored++
			return nil, nil
		}
		s.stats.MarketUpdates++
		m := classify(fx, pl.OptionMarket)
		if m == nil {
			// A market we map can become one we do not (a line we cannot
			// read): it leaves its group.
			return s.removeMarket(fx, pl.OptionMarket.ID.String(), recvAt), nil
		}
		return s.putMarket(fx, m, recvAt), nil
	case "OptionMarketDelete":
		var pl struct {
			FixtureID ID `json:"fixtureId"`
			MarketID  ID `json:"marketId"`
		}
		if err := json.Unmarshal(p.Payload, &pl); err != nil {
			return nil, fmt.Errorf("betmgm: decode market delete: %w", err)
		}
		fx := s.fixtures[pl.FixtureID.String()]
		if fx == nil {
			s.stats.Ignored++
			return nil, nil
		}
		s.stats.MarketDeletes++
		return s.removeMarket(fx, pl.MarketID.String(), recvAt), nil
	case "FixtureUpdate":
		var pl struct {
			FixtureID        ID     `json:"fixtureId"`
			Stage            string `json:"stage"`
			IsOpenForBetting *bool  `json:"isOpenForBetting"`
		}
		if err := json.Unmarshal(p.Payload, &pl); err != nil {
			return nil, fmt.Errorf("betmgm: decode fixture update: %w", err)
		}
		fx := s.fixtures[pl.FixtureID.String()]
		if fx == nil {
			return nil, nil
		}
		live, closed := fx.live, fx.closed
		if pl.Stage != "" {
			live = strings.EqualFold(pl.Stage, "Live")
		}
		if pl.IsOpenForBetting != nil {
			closed = !*pl.IsOpenForBetting
		}
		if live == fx.live && closed == fx.closed {
			return nil, nil
		}
		fx.live, fx.closed = live, closed
		return updates(fx.quotes(), recvAt), nil
	case "MainToLiveUpdate":
		// Broadcast to every client for every sport. Only a switch away
		// from one of our fixtures matters: its live id is a new fixture.
		var pl struct {
			SwitchedFixtures []struct {
				PreMatchID ID `json:"preMatchId"`
				InPlayID   ID `json:"inPlayId"`
			} `json:"switchedFixtures"`
		}
		if err := json.Unmarshal(p.Payload, &pl); err != nil {
			return nil, fmt.Errorf("betmgm: decode live switch: %w", err)
		}
		for _, sw := range pl.SwitchedFixtures {
			if s.fixtures[sw.PreMatchID.String()] != nil && sw.InPlayID != sw.PreMatchID {
				return nil, fmt.Errorf("%w: fixture %s went live as %s", book.ErrResync, sw.PreMatchID, sw.InPlayID)
			}
		}
		return nil, nil
	case "ConnectionAck":
		return nil, nil
	}
	s.stats.Unknown++
	return nil, nil
}

// putMarket stores a market and emits its group when the group's main
// market or its quotes changed.
func (s *state) putMarket(fx *fixture, m *market, recvAt time.Time) []model.Update {
	var out []model.Update
	if old := fx.marketGroup[m.id]; old != "" && old != m.group {
		out = s.removeMarket(fx, m.id, recvAt) // it left its old group
	}
	g := m.group
	prev := fx.chosen[g]
	fx.put(m)
	if cur := fx.chosen[g]; cur != m && cur == prev {
		return out // an alternate line changed; the main one did not
	}
	return append(out, s.emitGroup(fx, g, prev, recvAt)...)
}

func (s *state) removeMarket(fx *fixture, id string, recvAt time.Time) []model.Update {
	g := fx.marketGroup[id]
	if g == "" {
		return nil
	}
	prev := fx.chosen[g]
	fx.remove(id)
	if fx.chosen[g] == prev {
		return nil
	}
	return s.emitGroup(fx, g, prev, recvAt)
}

// emitGroup emits the group's current main quotes, and Removed for any
// identity the previous main market had that the current one does not.
func (s *state) emitGroup(fx *fixture, g string, prev *market, recvAt time.Time) []model.Update {
	cur := fx.groupQuotes(g)
	out := updates(cur, recvAt)
	if prev == nil {
		return out
	}
	have := map[string]bool{}
	for _, q := range cur {
		have[model.IdentityKey(q)] = true
	}
	for _, q := range prev.quotes {
		id := model.IdentityKey(q)
		if have[id] {
			continue
		}
		have[id] = true
		u := model.UpdateFromQuote(q, model.SourceStream, time.Time{}, recvAt)
		u.Status = model.StatusRemoved
		out = append(out, u)
	}
	return out
}

func updates(qs []model.Quote, recvAt time.Time) []model.Update {
	out := make([]model.Update, 0, len(qs))
	for _, q := range qs {
		out = append(out, model.UpdateFromQuote(q, model.SourceStream, time.Time{}, recvAt))
	}
	return out
}
