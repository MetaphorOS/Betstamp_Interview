package fanduel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

const (
	pingEvery    = 30 * time.Second
	ackTimeout   = 15 * time.Second
	subscriberID = "board-prices"
)

type appSyncAuth struct {
	Authorization string `json:"Authorization"`
	Host          string `json:"host"`
}

// Stream connects to AppSync (graphql-ws), subscribes to every main market
// in the seed with one start, and emits each data frame's runners. Port of
// go/internal/fd/ws.go. It returns on the first failure.
func (b *bookImpl) Stream(ctx context.Context, snap *book.Snapshot, em book.Emitter) error {
	sd, ok := snap.Private.(*seed)
	if !ok || sd == nil {
		return fmt.Errorf("%w: fanduel seed missing", book.ErrResync)
	}
	st := sd.clone()
	if len(st.ids) == 0 {
		return errors.New("fanduel: no main markets to subscribe to")
	}
	bundle, err := b.bundles.get(ctx)
	if err != nil {
		return err
	}
	auth := appSyncAuth{Authorization: bundle.ClientID, Host: AppSyncHost}
	authJSON, _ := json.Marshal(auth)
	connectURL := b.cfg.WSURL + "?header=" + base64.StdEncoding.EncodeToString(authJSON) + "&payload=e30="

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Subprotocols: []string{"graphql-ws"}}
	conn, resp, err := dialer.DialContext(ctx, connectURL, http.Header{})
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				b.bundles.invalidate()
				return fmt.Errorf("%w: fanduel ws dial HTTP %d", book.ErrAuth, resp.StatusCode)
			}
			return fmt.Errorf("fanduel: ws dial: %w (HTTP %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("fanduel: ws dial: %w", err)
	}
	var wmu sync.Mutex
	write := func(fn func() error) error {
		wmu.Lock()
		defer wmu.Unlock()
		return fn()
	}
	sendJSON := func(v any) error {
		raw, err := marshalNoEscape(v)
		if err != nil {
			return err
		}
		return write(func() error { return conn.WriteMessage(websocket.TextMessage, raw) })
	}
	defer func() {
		_ = write(func() error {
			return conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		})
		conn.Close()
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetPongHandler(func(string) error { em.Heartbeat(); return nil })

	if err := sendJSON(map[string]any{"type": "connection_init"}); err != nil {
		return fmt.Errorf("fanduel: connection_init: %w", err)
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
	ackDeadline := time.NewTimer(ackTimeout)
	defer ackDeadline.Stop()
	started := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ackDeadline.C:
			if !started {
				return errors.New("fanduel: no connection_ack/start_ack in time")
			}
		case <-ping.C:
			if err := write(func() error {
				return conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			}); err != nil {
				return fmt.Errorf("fanduel: ping: %w", err)
			}
		case f, ok := <-frames:
			if !ok {
				return ctx.Err()
			}
			if f.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("fanduel: ws read: %w", f.err)
			}
			em.Heartbeat()
			res, err := st.apply(f.raw, f.recvAt)
			if err != nil {
				return err
			}
			switch res.kind {
			case frameConnAck:
				msg, err := startMessage(auth, bundle.SubscriptionQuery, st.ids)
				if err != nil {
					return err
				}
				if err := sendJSON(msg); err != nil {
					return fmt.Errorf("fanduel: start: %w", err)
				}
			case frameStartAck:
				started = true
				em.Connected()
			case frameError:
				b.bundles.invalidate()
				return fmt.Errorf("%w: fanduel appsync error: %s", book.ErrAuth, truncate(res.errMsg, 300))
			case frameData:
				for _, u := range res.updates {
					if err := em.Emit(ctx, u); err != nil {
						return err
					}
				}
			}
		}
	}
}

// startMessage: payload.data is a JSON *string*, exactly as the frontend
// sends it.
func startMessage(auth appSyncAuth, query string, ids []string) (map[string]any, error) {
	data, err := marshalNoEscape(map[string]any{"query": query, "variables": map[string]any{"ids": ids}})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":   subscriberID,
		"type": "start",
		"payload": map[string]any{
			"data":       string(data),
			"extensions": map[string]any{"authorization": auth},
		},
	}, nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Poll reads current prices for every seeded market from SMP, in batches of
// 70, with a browser UA (SMP returns 500 to Go's default UA).
func (b *bookImpl) Poll(ctx context.Context, snap *book.Snapshot) ([]model.Update, error) {
	sd, ok := snap.Private.(*seed)
	if !ok || sd == nil {
		return nil, errors.New("fanduel: seed missing")
	}
	var out []model.Update
	for start := 0; start < len(sd.ids); start += smpBatch {
		end := min(start+smpBatch, len(sd.ids))
		asOf := time.Now()
		markets, err := b.smp(ctx, sd.ids[start:end])
		if err != nil {
			return nil, err
		}
		recv := time.Now()
		ups, err := mapSMP(sd, markets, asOf, recv)
		if err != nil {
			return nil, err
		}
		out = append(out, ups...)
	}
	return out, nil
}

// mapSMP maps SMP markets read-only against the seed. Markets SMP returns
// that we never subscribed to are ignored; poll results must not resync.
func mapSMP(sd *seed, markets []fdMarket, asOf, recv time.Time) ([]model.Update, error) {
	var out []model.Update
	for i := range markets {
		if sd.markets[markets[i].MarketID] == nil {
			continue
		}
		ups, err := sd.mapMarket(&markets[i], model.SourcePoll, recv, false)
		if err != nil {
			return nil, err
		}
		for j := range ups {
			ups[j].AsOf = asOf
		}
		out = append(out, ups...)
	}
	return out, nil
}

func (b *bookImpl) smp(ctx context.Context, ids []string) ([]fdMarket, error) {
	body, _ := json.Marshal(map[string]any{"marketIds": ids})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.SMPURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", b.cfg.UserAgent)
	req.Header.Set("X-Application", b.ak())
	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	var markets []fdMarket
	if err := json.Unmarshal(raw, &markets); err != nil {
		return nil, fmt.Errorf("fanduel: decode SMP: %w", err)
	}
	return markets, nil
}
