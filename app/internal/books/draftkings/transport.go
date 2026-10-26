package draftkings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"betstampodds/internal/book"
)

const (
	pingEvery     = 30 * time.Second
	expireEvery   = 250 * time.Millisecond
	clientVersion = "2643.4.1.10"
)

// Stream dials the DK WebSocket, subscribes to the snapshot's "events"
// partials and feeds every frame through a private copy of the seed state.
// It returns on the first read error; the supervisor reconnects.
func (b *bookImpl) Stream(ctx context.Context, seed *book.Snapshot, em book.Emitter) error {
	sd, ok := seed.Private.(*state)
	if !ok || sd == nil {
		return fmt.Errorf("%w: draftkings seed missing", book.ErrResync)
	}
	st := sd.clone()
	if len(st.partials) == 0 {
		return errors.New("draftkings: snapshot has no events subscription partials")
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	hdr := http.Header{}
	hdr.Set("User-Agent", b.cfg.UserAgent)
	conn, resp, err := dialer.DialContext(ctx, b.cfg.WSURL, hdr)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("draftkings: ws dial: %w (HTTP %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("draftkings: ws dial: %w", err)
	}
	var wmu sync.Mutex
	write := func(fn func() error) error {
		wmu.Lock()
		defer wmu.Unlock()
		return fn()
	}
	defer func() {
		_ = write(func() error {
			return conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		})
		conn.Close()
	}()
	// Closing the socket is how ctx cancellation unblocks ReadMessage.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetPongHandler(func(string) error { em.Heartbeat(); return nil })

	for _, msg := range subscribeMessages(st.partials) {
		raw, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if err := write(func() error { return conn.WriteMessage(websocket.TextMessage, raw) }); err != nil {
			return fmt.Errorf("draftkings: subscribe: %w", err)
		}
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
			// RecvAt is stamped once, at socket read. No .UTC(): that strips the
			// monotonic reading, and latency is a monotonic difference.
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
	tick := time.NewTicker(expireEvery)
	defer tick.Stop()
	connected := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping.C:
			if err := write(func() error {
				return conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			}); err != nil {
				return fmt.Errorf("draftkings: ping: %w", err)
			}
		case now := <-tick.C:
			for _, u := range st.expire(now.UTC()) {
				if err := em.Emit(ctx, u); err != nil {
					return err
				}
			}
		case f, ok := <-frames:
			if !ok {
				return ctx.Err()
			}
			if f.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("draftkings: ws read: %w", f.err)
			}
			em.Heartbeat()
			res, err := st.apply(f.raw, f.recvAt)
			if err != nil {
				return err
			}
			if res.ack && !connected {
				connected = true
				em.Connected()
			}
			for _, u := range res.updates {
				if err := em.Emit(ctx, u); err != nil {
					return err
				}
			}
		}
	}
}

// subscribeMessages builds one JSON-RPC subscribe per partial, forwarding
// every key except entity (see go/internal/dk/subscribe.go). initialData is
// false: DK does not replay, so the supervisor reconciles after Connected.
func subscribeMessages(partials []map[string]any) []map[string]any {
	msgs := make([]map[string]any, 0, len(partials))
	for i, p := range partials {
		qp := make(map[string]any, len(p)+2)
		for k, v := range p {
			if k != "entity" {
				qp[k] = v
			}
		}
		qp["initialData"] = false
		qp["projection"] = "sportsbook"
		msgs = append(msgs, map[string]any{
			"jsonrpc": "2.0",
			"method":  "subscribe",
			"id":      fmt.Sprintf("race-%d", i),
			"params": map[string]any{
				"entity":           p["entity"],
				"queryParams":      qp,
				"forwardedHeaders": map[string]any{},
				"clientMetadata": map[string]any{
					"feature":          "leagueSubcategory",
					"X-Client-Name":    "web",
					"X-Client-Version": clientVersion,
				},
				"jwt":      "",
				"siteName": "dkcaon",
			},
		})
	}
	return msgs
}
