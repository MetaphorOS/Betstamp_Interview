package fanduel

import (
	"encoding/json"
	"fmt"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// seed is the book-private part of a snapshot: the discovery mapping from
// market and runner IDs to identities. Push and SMP frames carry no runner
// names, so this mapping is the only way to place them.
type seed struct {
	markets map[string]*mktSeed
	ids     []string // sorted main market IDs; the subscription
}

type mktSeed struct {
	family  string
	gameRef string
	runners map[string]*runnerSeed // selectionId -> runner; IDs repeat across markets
}

type runnerSeed struct {
	identity string
	line     *float64
	american int
	decimal  float64
	status   model.Status
}

func (s *seed) clone() *seed {
	c := &seed{markets: make(map[string]*mktSeed, len(s.markets)), ids: append([]string(nil), s.ids...)}
	for id, m := range s.markets {
		cm := &mktSeed{family: m.family, gameRef: m.gameRef, runners: make(map[string]*runnerSeed, len(m.runners))}
		for sid, r := range m.runners {
			cr := *r
			cr.line = model.CopyLine(r.line)
			cm.runners[sid] = &cr
		}
		c.markets[id] = cm
	}
	return c
}

// mapMarket converts one full-state market (push frame or SMP entry) into
// updates. Each frame carries every runner with handicap, status and price.
// When remember is true the last line per runner is kept in the seed copy.
func (s *seed) mapMarket(m *fdMarket, src model.Source, recvAt time.Time, remember bool) ([]model.Update, error) {
	ms := s.markets[m.MarketID]
	if ms == nil {
		return nil, fmt.Errorf("%w: unknown market %s", book.ErrResync, m.MarketID)
	}
	runners := m.RunnerDetails
	if len(runners) == 0 {
		runners = m.Runners
	}
	out := make([]model.Update, 0, len(runners))
	for _, r := range runners {
		rs := ms.runners[r.SelectionID.String()]
		if rs == nil {
			return nil, fmt.Errorf("%w: unknown runner %s on market %s", book.ErrResync, r.SelectionID, m.MarketID)
		}
		line := rs.line
		if ms.family == model.FamilyMoneyline {
			line = nil
		} else if r.Handicap != nil {
			line = model.CopyLine(r.Handicap)
		}
		american, decimal := rs.american, rs.decimal
		if v := r.WinRunnerOdds.American(); v != nil {
			american = int(*v)
		}
		if v := r.WinRunnerOdds.Decimal(); v != nil {
			decimal = *v
		}
		status := fdStatus(m.MarketStatus, r.RunnerStatus)
		if remember {
			rs.line, rs.american, rs.decimal, rs.status = model.CopyLine(line), american, decimal, status
		}
		out = append(out, model.Update{
			Book: Name, GameRef: ms.gameRef, Identity: rs.identity,
			Line: model.CopyLine(line), American: american, Decimal: decimal,
			Status: status, InPlay: m.IsInPlay(), RecvAt: recvAt, Source: src,
		})
	}
	return out, nil
}

// graphql-ws frames.
type gqlFrame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type frameKind int

const (
	frameOther frameKind = iota
	frameConnAck
	frameStartAck
	frameKeepAlive
	frameData
	frameError
)

type frameResult struct {
	kind    frameKind
	updates []model.Update
	errMsg  string
}

// apply handles one AppSync frame. FD frames carry no book timestamp, so
// BookTS stays zero and FD latency is measured from RecvAt only.
func (s *seed) apply(raw []byte, recvAt time.Time) (frameResult, error) {
	var f gqlFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return frameResult{}, fmt.Errorf("fanduel: decode frame: %w", err)
	}
	switch f.Type {
	case "connection_ack":
		return frameResult{kind: frameConnAck}, nil
	case "start_ack":
		return frameResult{kind: frameStartAck}, nil
	case "ka":
		return frameResult{kind: frameKeepAlive}, nil
	case "error", "connection_error":
		return frameResult{kind: frameError, errMsg: string(f.Payload)}, nil
	case "data":
	default:
		return frameResult{}, nil
	}
	var p struct {
		Data struct {
			OnUpdateMarketPrice *fdMarket `json:"onUpdateMarketPrice"`
		} `json:"data"`
	}
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return frameResult{}, fmt.Errorf("fanduel: decode data: %w", err)
	}
	if p.Data.OnUpdateMarketPrice == nil {
		return frameResult{kind: frameData}, nil
	}
	ups, err := s.mapMarket(p.Data.OnUpdateMarketPrice, model.SourceStream, recvAt, true)
	if err != nil {
		return frameResult{}, err
	}
	return frameResult{kind: frameData, updates: ups}, nil
}
