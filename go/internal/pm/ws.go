package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WS is a Polymarket CLOB market-channel connection.
type WS struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// Dial opens the public market channel. No authentication is required.
func Dial(ctx context.Context, wsURL string, timeout time.Duration) (*WS, error) {
	if wsURL == "" {
		wsURL = WSURL
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := websocket.Dialer{HandshakeTimeout: timeout}
	conn, resp, err := dialer.DialContext(ctx, wsURL, http.Header{
		"User-Agent": {DefaultUserAgent},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("polymarket websocket: %w (HTTP %d)", err, resp.StatusCode)
		}
		return nil, fmt.Errorf("polymarket websocket: %w", err)
	}
	return &WS{conn: conn}, nil
}

// Subscribe sends the initial market subscription. assetIDs are CLOB outcome
// token ids from market.clobTokenIds.
func (w *WS) Subscribe(assetIDs []string) error {
	msg := map[string]any{
		"assets_ids":             assetIDs,
		"type":                   "market",
		"custom_feature_enabled": true,
	}
	return w.SendJSON(msg)
}

// SendJSON writes one text frame.
func (w *WS) SendJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, raw)
}

// Ping sends the CLOB heartbeat. The server replies with PONG.
func (w *WS) Ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, []byte("PING"))
}

// Read returns the next text frame.
func (w *WS) Read() ([]byte, error) {
	_, raw, err := w.conn.ReadMessage()
	return raw, err
}

// Close closes the socket.
func (w *WS) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	return w.conn.Close()
}

// Level is one bid/ask level in a book message.
type Level struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

// PriceChange is one item in a price_change message.
type PriceChange struct {
	AssetID string `json:"asset_id"`
	Price   string `json:"price"`
	Size    string `json:"size"`
	Side    string `json:"side"`
	BestBid string `json:"best_bid"`
	BestAsk string `json:"best_ask"`
}

// Frame is a market-channel message.
type Frame struct {
	EventType      string        `json:"event_type"`
	AssetID        string        `json:"asset_id"`
	Market         string        `json:"market"`
	Bids           []Level       `json:"bids"`
	Asks           []Level       `json:"asks"`
	PriceChanges   []PriceChange `json:"price_changes"`
	BestBid        string        `json:"best_bid"`
	BestAsk        string        `json:"best_ask"`
	Price          string        `json:"price"`
	Side           string        `json:"side"`
	Size           string        `json:"size"`
	Timestamp      string        `json:"timestamp"`
	OldTickSize    string        `json:"old_tick_size"`
	NewTickSize    string        `json:"new_tick_size"`
	WinningAsset   string        `json:"winning_asset_id"`
	WinningOutcome string        `json:"winning_outcome"`
}

// ParseFrame decodes one market-channel message.
func ParseFrame(raw []byte) (*Frame, error) {
	var f Frame
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Best returns the top bid/ask from a book frame. Prices are decimal 0..1.
func (f *Frame) Best() (bid, ask float64, ok bool) {
	if len(f.Bids) == 0 || len(f.Asks) == 0 {
		return 0, 0, false
	}
	bid, err1 := strconv.ParseFloat(f.Bids[0].Price, 64)
	ask, err2 := strconv.ParseFloat(f.Asks[0].Price, 64)
	if err1 != nil || err2 != nil || bid <= 0 || ask <= 0 {
		return 0, 0, false
	}
	return bid, ask, true
}

// Mid returns the mid price from a book/price frame.
func (f *Frame) Mid() (float64, bool) {
	if bid, ask, ok := f.Best(); ok {
		return (bid + ask) / 2, true
	}
	if f.BestBid != "" && f.BestAsk != "" {
		bid, err1 := strconv.ParseFloat(f.BestBid, 64)
		ask, err2 := strconv.ParseFloat(f.BestAsk, 64)
		if err1 == nil && err2 == nil && bid > 0 && ask > 0 {
			return (bid + ask) / 2, true
		}
	}
	if f.Price != "" {
		if p, err := strconv.ParseFloat(f.Price, 64); err == nil && p > 0 {
			return p, true
		}
	}
	return 0, false
}
