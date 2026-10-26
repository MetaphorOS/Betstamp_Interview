package fd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"sportsbookfeed/internal/jsonx"
)

// Subprotocol is the graphql-ws subprotocol AppSync expects.
const Subprotocol = "graphql-ws"

// Auth is the public client authorization object embedded in the connect URL
// and in every start payload. Field order matters only for byte-level
// comparison with the frontend, not for correctness.
type Auth struct {
	Authorization string `json:"Authorization"`
	Host          string `json:"host"`
}

// RealtimeURL builds the AppSync connect URL.
//
// The JavaScript uses Buffer.from(JSON.stringify(auth)).toString('base64')
// and appends it as a raw query value, so the same standard base64 spelling
// (including + and =) is used here.
func RealtimeURL(auth Auth) (string, error) {
	raw, err := jsonx.Marshal(auth)
	if err != nil {
		return "", err
	}
	header := base64.StdEncoding.EncodeToString(raw)
	return DefaultRealtimeURL + "?header=" + header + "&payload=e30=", nil
}

// Realtime is the AppSync graphql-ws connection.
type Realtime struct {
	conn *websocket.Conn
	mu   sync.Mutex
	auth Auth
}

// DialRealtime connects with the graphql-ws subprotocol.
func DialRealtime(ctx context.Context, connectURL string, auth Auth, timeout time.Duration) (*Realtime, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
		Subprotocols:     []string{Subprotocol},
	}
	conn, resp, err := dialer.DialContext(ctx, connectURL, http.Header{})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("FanDuel realtime dial: %w (HTTP %d)", err, resp.StatusCode)
		}
		return nil, fmt.Errorf("FanDuel realtime dial: %w", err)
	}
	return &Realtime{conn: conn, auth: auth}, nil
}

// Subprotocol returns the negotiated subprotocol.
func (r *Realtime) Subprotocol() string { return r.conn.Subprotocol() }

// SendJSON writes one text frame without HTML escaping.
func (r *Realtime) SendJSON(v any) error {
	payload, err := jsonx.Marshal(v)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn.WriteMessage(websocket.TextMessage, payload)
}

// SendInit writes the graphql-ws connection_init frame.
func (r *Realtime) SendInit() error {
	return r.SendJSON(map[string]any{"type": "connection_init"})
}

// Start subscribes to OnUpdateMarketPrice for the discovered market ids.
//
// The payload's data field is a JSON *string*, not an object; AppSync expects
// the frontend's exact shape.
func (r *Realtime) Start(id, query string, ids []string) error {
	msg, err := r.StartMessage(id, query, ids)
	if err != nil {
		return err
	}
	return r.SendJSON(msg)
}

// StartMessage builds the start frame without sending it, so callers can log
// the exact payload first.
func (r *Realtime) StartMessage(id, query string, ids []string) (map[string]any, error) {
	if query == "" {
		return nil, fmt.Errorf("FanDuel subscription query is empty")
	}
	data, err := jsonx.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{Query: query, Variables: map[string]any{"ids": ids}})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":   id,
		"type": "start",
		"payload": map[string]any{
			"data": string(data),
			"extensions": map[string]any{
				"authorization": r.auth,
			},
		},
	}, nil
}

// Read returns the next frame.
func (r *Realtime) Read() ([]byte, error) {
	_, payload, err := r.conn.ReadMessage()
	return payload, err
}

// Close closes the socket; graphql-ws has no explicit stop required here.
func (r *Realtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	return r.conn.Close()
}

// Ping sends a WebSocket control ping. Pong frames are handled by Read and
// can be observed with OnPong.
func (r *Realtime) Ping() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}

// OnPong installs a pong handler. Call it before the read loop starts.
func (r *Realtime) OnPong(fn func()) {
	r.conn.SetPongHandler(func(string) error {
		fn()
		return nil
	})
}

// Frame is an incoming graphql-ws message.
type Frame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ParseFrame decodes one incoming text frame.
func ParseFrame(raw []byte) (*Frame, error) {
	var f Frame
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Price returns the market carried by a data frame.
func (f *Frame) Price() (*Market, error) {
	var payload struct {
		Data struct {
			OnUpdateMarketPrice *Market `json:"onUpdateMarketPrice"`
		} `json:"data"`
	}
	if err := json.Unmarshal(f.Payload, &payload); err != nil {
		return nil, err
	}
	if payload.Data.OnUpdateMarketPrice == nil {
		return nil, fmt.Errorf("data frame has no onUpdateMarketPrice")
	}
	return payload.Data.OnUpdateMarketPrice, nil
}
