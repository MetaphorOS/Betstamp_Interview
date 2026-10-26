package polymarket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// The CLOB market channel expects a text "PING" about every 10 s.
const pingEvery = 10 * time.Second

// Stream subscribes to every main-market token and emits the token's best
// ask (the price to buy that outcome) on each book, price_change and
// best_bid_ask message. Port of go/internal/pm/ws.go.
func (b *bookImpl) Stream(ctx context.Context, snap *book.Snapshot, em book.Emitter) error {
	sd, ok := snap.Private.(*seed)
	if !ok || sd == nil {
		return fmt.Errorf("%w: polymarket seed missing", book.ErrResync)
	}
	if len(sd.ids) == 0 {
		return errors.New("polymarket: no main-market tokens")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	hdr := http.Header{}
	hdr.Set("User-Agent", b.cfg.UserAgent)
	conn, resp, err := dialer.DialContext(ctx, b.cfg.WSURL, hdr)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("polymarket: ws dial: %w (HTTP %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("polymarket: ws dial: %w", err)
	}
	var wmu sync.Mutex
	write := func(mt int, raw []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteMessage(mt, raw)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	sub, _ := json.Marshal(map[string]any{"assets_ids": sd.ids, "type": "market"})
	if err := write(websocket.TextMessage, sub); err != nil {
		return fmt.Errorf("polymarket: subscribe: %w", err)
	}

	type frame struct {
		raw    []byte
		recvAt time.Time
		err    error
	}
	frames := make(chan frame, 256)
	go func() {
		defer close(frames)
		for {
			_, raw, err := conn.ReadMessage()
			f := frame{raw: raw, recvAt: time.Now(), err: err}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	connected := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping.C:
			if err := write(websocket.TextMessage, []byte("PING")); err != nil {
				return fmt.Errorf("polymarket: ping: %w", err)
			}
		case f, ok := <-frames:
			if !ok {
				return ctx.Err()
			}
			if f.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("polymarket: ws read: %w", f.err)
			}
			em.Heartbeat()
			if !connected {
				// No ack on this channel: the first message (the book
				// snapshot for our tokens) means the subscription is live.
				connected = true
				em.Connected()
			}
			ups, err := sd.apply(f.raw, f.recvAt)
			if err != nil {
				return err
			}
			for _, u := range ups {
				if err := em.Emit(ctx, u); err != nil {
					return err
				}
			}
		}
	}
}

type level struct {
	Price string `json:"price"`
}

type priceChange struct {
	AssetID string `json:"asset_id"`
	BestBid string `json:"best_bid"`
	BestAsk string `json:"best_ask"`
}

type wsMsg struct {
	EventType    string        `json:"event_type"`
	AssetID      string        `json:"asset_id"`
	Bids         []level       `json:"bids"`
	Asks         []level       `json:"asks"`
	BestBid      string        `json:"best_bid"`
	BestAsk      string        `json:"best_ask"`
	PriceChanges []priceChange `json:"price_changes"`
	Timestamp    string        `json:"timestamp"` // unix ms: the book publish time
}

// apply maps one message (an object or an array of them). It only reads the
// seed, so it is safe to share between runs.
func (sd *seed) apply(raw []byte, recvAt time.Time) ([]model.Update, error) {
	if len(raw) == 0 || raw[0] == 'P' { // PONG
		return nil, nil
	}
	var msgs []wsMsg
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return nil, fmt.Errorf("polymarket: decode: %w", err)
		}
	} else {
		var m wsMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("polymarket: decode: %w", err)
		}
		msgs = []wsMsg{m}
	}
	var out []model.Update
	for _, m := range msgs {
		bookTS := msTime(m.Timestamp)
		switch m.EventType {
		case "book":
			// A book message is current state; its timestamp is the last
			// change, possibly hours ago, so it is not a publish time.
			bid, ask := bestOf(m.Bids, true), bestOf(m.Asks, false)
			u, err := sd.update(m.AssetID, bid, ask, recvAt, time.Time{})
			if err != nil {
				return nil, err
			}
			out = append(out, u...)
		case "best_bid_ask":
			u, err := sd.update(m.AssetID, num(m.BestBid), num(m.BestAsk), recvAt, bookTS)
			if err != nil {
				return nil, err
			}
			out = append(out, u...)
		case "price_change":
			for _, pc := range m.PriceChanges {
				u, err := sd.update(pc.AssetID, num(pc.BestBid), num(pc.BestAsk), recvAt, bookTS)
				if err != nil {
					return nil, err
				}
				out = append(out, u...)
			}
		}
	}
	return out, nil
}

func (sd *seed) update(asset string, bid, ask float64, recvAt, bookTS time.Time) ([]model.Update, error) {
	t := sd.tokens[asset]
	if t == nil {
		return nil, fmt.Errorf("%w: unknown token %s", book.ErrResync, asset)
	}
	if bid <= 0 || ask <= 0 || ask > 1 || bid > ask {
		return nil, nil
	}
	am, dec := convert(ask)
	status := model.StatusOpen
	if am == 0 || t.closed {
		status = model.StatusSuspended
	}
	line := model.CopyLine(t.line)
	if t.family == model.FamilyMoneyline {
		line = nil
	}
	return []model.Update{{
		Book: Name, GameRef: t.gameRef, Identity: t.identity, Line: line,
		American: am, Decimal: dec, Status: status, InPlay: t.live,
		BookTS: bookTS, RecvAt: recvAt, Source: model.SourceStream,
	}}, nil
}

// bestOf scans levels; book messages are not guaranteed best-first.
func bestOf(levels []level, isBid bool) float64 {
	best := 0.0
	for _, l := range levels {
		p := num(l.Price)
		if p <= 0 {
			continue
		}
		if best == 0 || (isBid && p > best) || (!isBid && p < best) {
			best = p
		}
	}
	return best
}

func num(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func msTime(s string) time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
