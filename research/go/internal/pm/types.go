// Package pm implements Polymarket's public Gamma REST API and CLOB market
// WebSocket. Polymarket is a prediction market: each outcome token trades in
// the 0..1 probability range, and a single game has one binary market per
// moneyline outcome, spread line or total line.
package pm

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	GammaBaseURL = "https://gamma-api.polymarket.com"
	CLOBBaseURL  = "https://clob.polymarket.com"
	WSURL        = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
	SportsWSURL  = "wss://sports-api.polymarket.com/ws"

	NBASeries = "10345"
	NFLSeries = "12185"
)

// Team is the Gamma event team object.
type Team struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Alias        string `json:"alias"`
	Abbreviation string `json:"abbreviation"`
	League       string `json:"league"`
	Ordering     string `json:"ordering"`
	Record       string `json:"record"`
}

// Market is one Gamma binary market. Outcomes, OutcomePrices and ClobTokenIds
// are JSON-encoded strings in Gamma, not arrays.
type Market struct {
	ID               string    `json:"id"`
	Question         string    `json:"question"`
	GroupItemTitle   string    `json:"groupItemTitle"`
	Line             *float64  `json:"line"`
	Outcomes         string    `json:"outcomes"`
	OutcomePrices    string    `json:"outcomePrices"`
	ClobTokenIDs     string    `json:"clobTokenIds"`
	SportsMarketType string    `json:"sportsMarketType"`
	BestBid          *float64  `json:"bestBid"`
	BestAsk          *float64  `json:"bestAsk"`
	LastTradePrice   *float64  `json:"lastTradePrice"`
	GameStartTime    string    `json:"gameStartTime"`
	Closed           bool      `json:"closed"`
	Active           bool      `json:"active"`
	Archived         bool      `json:"archived"`
	Volume           FlexFloat `json:"volume"`
	Liquidity        FlexFloat `json:"liquidity"`
}

// FlexFloat accepts Gamma numbers encoded as either JSON numbers or strings.
type FlexFloat float64

func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = FlexFloat(v)
	return nil
}

// Event is one Gamma game event.
type Event struct {
	ID          string   `json:"id"`
	Ticker      string   `json:"ticker"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	StartDate   string   `json:"startDate"`
	EndDate     string   `json:"endDate"`
	StartTime   string   `json:"startTime"`
	EventDate   string   `json:"eventDate"`
	Active      bool     `json:"active"`
	Closed      bool     `json:"closed"`
	Archived    bool     `json:"archived"`
	Live        bool     `json:"live"`
	Ended       bool     `json:"ended"`
	Sport       any      `json:"sport"`
	GameID      any      `json:"gameId"`
	Teams       []Team   `json:"teams"`
	Markets     []Market `json:"markets"`
}

// Outcome is a parsed binary market side.
type Outcome struct {
	Name  string
	Price float64
	Token string
}

func parseStringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseFloatArray(raw string) ([]float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var values []any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(values))
	for _, v := range values {
		switch x := v.(type) {
		case float64:
			out = append(out, x)
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		default:
			return nil, fmt.Errorf("unsupported price value %T", v)
		}
	}
	return out, nil
}

// Outcomes returns the market sides and their prices/tokens.
func (m Market) Sides() ([]Outcome, error) {
	names, err := parseStringArray(m.Outcomes)
	if err != nil {
		return nil, fmt.Errorf("outcomes: %w", err)
	}
	prices, err := parseFloatArray(m.OutcomePrices)
	if err != nil {
		return nil, fmt.Errorf("outcomePrices: %w", err)
	}
	tokens, err := parseStringArray(m.ClobTokenIDs)
	if err != nil {
		return nil, fmt.Errorf("clobTokenIds: %w", err)
	}
	out := make([]Outcome, 0, len(names))
	for i, name := range names {
		o := Outcome{Name: name}
		if i < len(prices) {
			o.Price = prices[i]
		}
		if i < len(tokens) {
			o.Token = tokens[i]
		}
		out = append(out, o)
	}
	return out, nil
}

// Mid returns the top-of-book mid if available, else the Gamma outcome price.
func (m Market) Mid(index int) (float64, bool) {
	if m.BestBid != nil && m.BestAsk != nil && *m.BestBid > 0 && *m.BestAsk > 0 && *m.BestAsk <= 1 {
		mid := (*m.BestBid + *m.BestAsk) / 2
		if index == 0 {
			return mid, true
		}
		if index == 1 {
			return 1 - mid, true
		}
	}
	outs, err := m.Sides()
	if err != nil || index < 0 || index >= len(outs) || outs[index].Price <= 0 {
		return 0, false
	}
	return outs[index].Price, true
}

// Start returns the game start time. Gamma's endDate is the game start for a
// game event; market.gameStartTime is a fallback.
func (e Event) Start() time.Time {
	for _, s := range []string{e.EndDate, e.StartTime, e.EventDate} {
		if t := parseTime(s); !t.IsZero() {
			return t
		}
	}
	for _, m := range e.Markets {
		if t := parseTime(m.GameStartTime); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05-07",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// AwayHome returns the team names by Gamma ordering, falling back to the
// event title ("Away vs. Home").
func (e Event) AwayHome() (away, home string) {
	for _, t := range e.Teams {
		switch strings.ToLower(t.Ordering) {
		case "away":
			away = t.Name
		case "home":
			home = t.Name
		}
	}
	if away == "" || home == "" {
		parts := strings.Split(e.Title, " vs. ")
		if len(parts) == 2 {
			if away == "" {
				away = strings.TrimSpace(parts[0])
			}
			if home == "" {
				home = strings.TrimSpace(parts[1])
			}
		}
	}
	return away, home
}

// DecimalToAmerican converts a 0..1 probability price to American odds.
func DecimalToAmerican(p float64) (int, bool) {
	if p <= 0 || p >= 1 {
		return 0, false
	}
	if p == 0.5 {
		return 100, true
	}
	if p > 0.5 {
		// decimal = 1/p; American = -100/(decimal-1)
		v := int(math.Round(-100.0 / ((1.0 / p) - 1.0)))
		if v > -100 {
			v = -100
		}
		return v, true
	}
	v := int(math.Round((1.0/p - 1.0) * 100.0))
	if v < 100 {
		v = 100
	}
	return v, true
}

// ParseAmerican is a small helper for tests and logs.
func ParseAmerican(s string) (int, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\u2212", "-"))
	s = strings.TrimPrefix(s, "+")
	return strconv.Atoi(s)
}
