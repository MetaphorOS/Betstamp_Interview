package dk

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"sportsbookfeed/internal/jsonx"
)

// WSOptions configures Dial.
type WSOptions struct {
	URL          string
	Subprotocols []string
	Header       http.Header
	Timeout      time.Duration
}

func (o WSOptions) url() string {
	if o.URL != "" {
		return o.URL
	}
	return DefaultWSURL
}

func (o WSOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 15 * time.Second
}

// WS is a small concurrency-safe wrapper around one gorilla connection.
type WS struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// Dial opens the DraftKings JSON WebSocket. No subprotocol is offered; the
// public endpoint speaks JSON-RPC over plain WebSocket.
func Dial(ctx context.Context, opts WSOptions) (*WS, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: opts.timeout(),
		Subprotocols:     opts.Subprotocols,
	}
	conn, resp, err := dialer.DialContext(ctx, opts.url(), opts.Header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("websocket dial: %w (HTTP %d)", err, resp.StatusCode)
		}
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	return &WS{conn: conn}, nil
}

// SendJSON marshals v without HTML escaping and writes one text frame.
func (w *WS) SendJSON(v any) error {
	payload, err := jsonx.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, payload)
}

// Read returns the next message. Binary frames are returned as-is; the
// format=json endpoint never sends them, but a future msgpack mode would.
func (w *WS) Read() ([]byte, error) {
	_, payload, err := w.conn.ReadMessage()
	return payload, err
}

// Close sends a normal closure when possible, then closes the socket.
func (w *WS) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	return w.conn.Close()
}

// Ping sends a WebSocket control ping. Pong frames are handled by Read and
// can be observed with OnPong.
func (w *WS) Ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}

// OnPong installs a pong handler. Call it before the read loop starts.
func (w *WS) OnPong(fn func()) {
	w.conn.SetPongHandler(func(string) error {
		fn()
		return nil
	})
}
