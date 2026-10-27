package betmgm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"betstampodds/internal/book"
)

// The server sends a SignalR ping ({"type":6}) about every 15 s; the client
// is expected to send its own on the same cadence.
const pingEvery = 15 * time.Second

// Stream connects to the SignalR hub, subscribes every fixture's "|all"
// topic in one invocation, and maps each whole-market update through a
// private copy of the seed. It returns on the first failure.
func (b *bookImpl) Stream(ctx context.Context, snap *book.Snapshot, em book.Emitter) error {
	sd, ok := snap.Private.(*state)
	if !ok || sd == nil {
		return fmt.Errorf("%w: betmgm seed missing", book.ErrResync)
	}
	st := sd.clone()
	if len(st.topics) == 0 {
		return errors.New("betmgm: no fixtures to subscribe to")
	}
	u := b.cfg.WSURL + "?" + url.Values{
		"lang": {"en-us"}, "country": {"CA"}, "x-bwin-accessId": {b.cfg.AccessID}, "appUpdates": {"false"},
	}.Encode()
	hdr := http.Header{}
	hdr.Set("User-Agent", b.cfg.UserAgent)
	hdr.Set("Origin", b.cfg.Base)
	conn, resp, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second}).DialContext(ctx, u, hdr)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("betmgm: ws dial: %w (HTTP %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("betmgm: ws dial: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var wmu sync.Mutex
	write := func(v string) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, []byte(v+string(recordSep)))
	}
	// SignalR's own ping ({"type":6}) has no reply, so the round trip is
	// timed with a WebSocket ping control frame, as for DraftKings and
	// FanDuel. Both are sent: the hub expects its keep-alive.
	var pingSent atomic.Int64
	conn.SetPongHandler(func(string) error {
		em.Heartbeat()
		if sent := pingSent.Swap(0); sent != 0 {
			em.Pong(time.Since(time.Unix(0, sent)))
		}
		return nil
	})
	sendPing := func() error {
		if err := write(`{"type":6}`); err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		pingSent.Store(time.Now().UnixNano())
		return conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
	}

	if err := write(`{"protocol":"json","version":1}`); err != nil {
		return fmt.Errorf("betmgm: handshake: %w", err)
	}
	sub, _ := json.Marshal(map[string]any{
		"type": 1, "target": "Subscribe", "invocationId": "1",
		"arguments": []any{map[string]any{"topics": st.topics}},
	})
	var subSent time.Time
	subscribed := false

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
			select {
			case frames <- frame{raw: raw, recvAt: time.Now(), err: err}:
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping.C:
			if err := sendPing(); err != nil {
				return fmt.Errorf("betmgm: ping: %w", err)
			}
		case f, ok := <-frames:
			if !ok {
				return ctx.Err()
			}
			if f.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("betmgm: ws read: %w", f.err)
			}
			em.Heartbeat()
			for _, msg := range splitFrame(f.raw) {
				res, err := st.apply(msg, f.recvAt)
				if err != nil {
					return err
				}
				switch res.kind {
				case msgHandshake:
					if subscribed {
						continue
					}
					subscribed, subSent = true, time.Now()
					if err := write(string(sub)); err != nil {
						return fmt.Errorf("betmgm: subscribe: %w", err)
					}
				case msgCompletion:
					if res.errMsg != "" {
						return fmt.Errorf("betmgm: subscribe rejected: %s", res.errMsg)
					}
					if !subSent.IsZero() {
						em.Handshake(f.recvAt.Sub(subSent))
						subSent = time.Time{}
					}
					em.Connected()
					if err := sendPing(); err != nil { // a first round trip right away
						return fmt.Errorf("betmgm: ping: %w", err)
					}
				case msgClose:
					return fmt.Errorf("betmgm: server closed the hub: %s", res.errMsg)
				}
				for _, u := range res.updates {
					if err := em.Emit(ctx, u); err != nil {
						return err
					}
				}
			}
		}
	}
}
