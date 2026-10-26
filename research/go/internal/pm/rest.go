package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

// FetchOptions configures Gamma REST calls.
type FetchOptions struct {
	BaseURL   string
	UserAgent string
	Timeout   time.Duration
}

func (o FetchOptions) base() string {
	if o.BaseURL != "" {
		return strings.TrimRight(o.BaseURL, "/")
	}
	return GammaBaseURL
}

func (o FetchOptions) ua() string {
	if o.UserAgent != "" {
		return o.UserAgent
	}
	return DefaultUserAgent
}

func (o FetchOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 20 * time.Second
}

// FetchEvents returns open events for a Gamma series. For NBA and NFL the
// series ids are NBASeries and NFLSeries.
func FetchEvents(ctx context.Context, seriesID string, limit int, opts FetchOptions) ([]Event, error) {
	if seriesID == "" {
		return nil, fmt.Errorf("series id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	u := fmt.Sprintf("%s/events?series_id=%s&closed=false&limit=%d",
		opts.base(), url.QueryEscape(seriesID), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", opts.ua())
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gamma events: HTTP %d", resp.StatusCode)
	}
	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	return events, nil
}

// FetchEvent returns one Gamma event by id, including all markets.
func FetchEvent(ctx context.Context, eventID string, opts FetchOptions) (*Event, error) {
	if eventID == "" {
		return nil, fmt.Errorf("event id is required")
	}
	u := fmt.Sprintf("%s/events/%s", opts.base(), url.PathEscape(eventID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", opts.ua())
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gamma event %s: HTTP %d", eventID, resp.StatusCode)
	}
	var event Event
	if err := json.NewDecoder(resp.Body).Decode(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

// BookLevel is one CLOB price level.
type BookLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

// Book is a CLOB order-book snapshot.
type Book struct {
	Market    string      `json:"market"`
	AssetID   string      `json:"asset_id"`
	Bids      []BookLevel `json:"bids"`
	Asks      []BookLevel `json:"asks"`
	Timestamp string      `json:"timestamp"`
	Hash      string      `json:"hash"`
}

// FetchBook returns the current CLOB book for one outcome token.
func FetchBook(ctx context.Context, tokenID string, timeout time.Duration) (*Book, error) {
	if tokenID == "" {
		return nil, fmt.Errorf("token id is required")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	u := CLOBBaseURL + "/book?token_id=" + url.QueryEscape(tokenID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clob book: HTTP %d", resp.StatusCode)
	}
	var book Book
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil {
		return nil, err
	}
	return &book, nil
}

// SeriesID maps a sport name to the Gamma series id used for current games.
func SeriesID(sport string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(sport)) {
	case "nba":
		return NBASeries, nil
	case "nfl":
		return NFLSeries, nil
	default:
		return "", fmt.Errorf("unsupported sport %q (want nba or nfl)", sport)
	}
}

// IsMainOrAltType reports whether a Gamma sportsMarketType is a full-game
// mainline or an alternate/period spread/total. Player props are intentionally
// excluded here; use IsPropType for those.
func IsMainOrAltType(t string) bool {
	switch t {
	case "moneyline", "spreads", "totals", "team_totals",
		"first_half_moneyline", "first_half_spreads", "first_half_totals",
		"first_half_team_totals", "second_half_moneyline",
		"second_half_spreads", "second_half_totals", "second_half_team_totals",
		"q1_moneyline", "q1_spreads", "q1_totals", "q1_team_totals",
		"q2_moneyline", "q2_spreads", "q2_totals", "q2_team_totals",
		"q3_moneyline", "q3_spreads", "q3_totals", "q3_team_totals",
		"q4_moneyline", "q4_spreads", "q4_totals", "q4_team_totals":
		return true
	}
	return false
}

// IsPropType reports whether a Gamma sportsMarketType is a player/team prop.
func IsPropType(t string) bool {
	switch t {
	case "points", "rebounds", "assists", "threes", "steals", "blocks",
		"turnovers", "passing_yards", "passing_touchdowns", "passing_attempts",
		"passing_completions", "rushing_yards", "rushing_touchdowns",
		"receiving_yards", "receptions", "anytime_touchdowns",
		"two_plus_touchdowns", "first_touchdowns", "longest_reception",
		"team_first_touchdowns":
		return true
	}
	return false
}
