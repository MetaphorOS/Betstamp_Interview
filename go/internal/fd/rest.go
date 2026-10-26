package fd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"sportsbookfeed/internal/clock"
	"sportsbookfeed/internal/jsonx"
)

// BoardOptions configures discovery (content-managed-page or in-play).
type BoardOptions struct {
	URL     string
	AK      string
	Timeout time.Duration
	Client  *http.Client
}

// BoardResult is a discovery response plus receipt metadata.
type BoardResult struct {
	Board        *Board  `json:"-"`
	Raw          []byte  `json:"-"`
	Status       int     `json:"-"`
	ElapsedMs    float64 `json:"elapsedMs"`
	CacheControl string  `json:"cacheControl"`
	Age          *string `json:"age"`
}

func (o BoardOptions) url() string {
	if o.URL != "" {
		return o.URL
	}
	ak := o.AK
	if ak == "" {
		ak = DefaultAK
	}
	return "https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nfl&includePrices=true&_ak=" + ak
}

func (o BoardOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 20 * time.Second
}

func (o BoardOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

// Discover fetches a content-managed page (the NFL board by default).
//
// The response is cached for max-age=30 with stale-while-revalidate=60, so it
// is a structure/bootstrap source, not a price feed.
func Discover(ctx context.Context, opts BoardOptions) (*BoardResult, error) {
	return fetchBoard(ctx, opts)
}

// InPlay fetches the live board used by compare-fd.cjs.
func InPlay(ctx context.Context, ak string) (*BoardResult, error) {
	return fetchBoard(ctx, BoardOptions{URL: inPlayURL(ak)})
}

func inPlayURL(ak string) string {
	if ak == "" {
		ak = DefaultAK
	}
	return "https://sbapi.on.sportsbook.fanduel.ca/api/in-play?_ak=" + ak + "&includeTabs=true"
}

func fetchBoard(ctx context.Context, opts BoardOptions) (*BoardResult, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.url(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	start := clock.MonoMs()
	resp, err := opts.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	elapsed := clock.MonoMs() - start
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("FanDuel HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
	}

	var board Board
	if err := json.Unmarshal(body, &board); err != nil {
		return nil, fmt.Errorf("invalid FanDuel board: %w", err)
	}
	res := &BoardResult{
		Board:        &board,
		Raw:          body,
		Status:       resp.StatusCode,
		ElapsedMs:    elapsed,
		CacheControl: resp.Header.Get("Cache-Control"),
	}
	if age := resp.Header.Get("Age"); age != "" {
		res.Age = &age
	}
	return res, nil
}

// GraphQLOptions configures the price snapshot.
type GraphQLOptions struct {
	URL           string
	Authorization string
	Timeout       time.Duration
	Client        *http.Client
}

func (o GraphQLOptions) url() string {
	if o.URL != "" {
		return o.URL
	}
	return DefaultGraphQLURL
}

func (o GraphQLOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 20 * time.Second
}

func (o GraphQLOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

// PriceResult is a graphql or SMP price response plus receipt metadata.
type PriceResult struct {
	Markets   []Market `json:"-"`
	Raw       []byte   `json:"-"`
	Status    int      `json:"-"`
	ElapsedMs float64  `json:"elapsedMs"`
}

// GetMarketPrices runs the GetMarketPrices GraphQL snapshot and returns the
// decoded markets. The ids are sportsbook market ids from the discovery page.
func GetMarketPrices(ctx context.Context, opts GraphQLOptions, query string, ids []string) ([]Market, error) {
	res, err := FetchMarketPrices(ctx, opts, query, ids)
	if err != nil {
		return nil, err
	}
	return res.Markets, nil
}

// FetchMarketPrices is GetMarketPrices with the raw body and timing.
func FetchMarketPrices(ctx context.Context, opts GraphQLOptions, query string, ids []string) (*PriceResult, error) {
	if query == "" {
		return nil, fmt.Errorf("FanDuel GetMarketPrices query is empty")
	}
	body, err := jsonx.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{Query: query, Variables: map[string]any{"ids": ids}})
	if err != nil {
		return nil, err
	}

	start := clock.MonoMs()
	raw, status, err := postJSON(ctx, opts.client(), opts.url(), opts.timeout(), body, func(req *http.Request) {
		req.Header.Set("Authorization", opts.Authorization)
	})
	elapsed := clock.MonoMs() - start
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("FanDuel HTTP %d: %s", status, truncate(string(raw), 160))
	}

	var decoded struct {
		Data *struct {
			GetMarketPrices []Market `json:"getMarketPrices"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("invalid FanDuel GraphQL response: %w", err)
	}
	if decoded.Data == nil {
		if len(decoded.Errors) > 0 {
			return nil, fmt.Errorf("FanDuel GraphQL error: %s", decoded.Errors[0].Message)
		}
		return nil, fmt.Errorf("FanDuel GraphQL response has no data")
	}
	return &PriceResult{Markets: decoded.Data.GetMarketPrices, Raw: raw, Status: status, ElapsedMs: elapsed}, nil
}

// SMPOptions configures the fallback poll.
type SMPOptions struct {
	URL         string
	Application string
	Timeout     time.Duration
	Client      *http.Client
}

func (o SMPOptions) url() string {
	if o.URL != "" {
		return o.URL
	}
	return DefaultSMPURL
}

func (o SMPOptions) application() string {
	if o.Application != "" {
		return o.Application
	}
	return DefaultAK
}

func (o SMPOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 20 * time.Second
}

func (o SMPOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

// PollMarketPrices runs the SMP getMarketPrices poll. The response is
// Cache-Control:no-cache and the frontend batches up to 70 ids every 5 s.
func PollMarketPrices(ctx context.Context, opts SMPOptions, ids []string) ([]Market, error) {
	res, err := FetchMarketPricesSMP(ctx, opts, ids)
	if err != nil {
		return nil, err
	}
	return res.Markets, nil
}

// FetchMarketPricesSMP is PollMarketPrices with the raw body and timing.
func FetchMarketPricesSMP(ctx context.Context, opts SMPOptions, ids []string) (*PriceResult, error) {
	body, err := jsonx.Marshal(struct {
		MarketIDs []string `json:"marketIds"`
	}{MarketIDs: ids})
	if err != nil {
		return nil, err
	}

	start := clock.MonoMs()
	raw, status, err := postJSON(ctx, opts.client(), opts.url(), opts.timeout(), body, func(req *http.Request) {
		req.Header.Set("X-Application", opts.application())
	})
	elapsed := clock.MonoMs() - start
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("FanDuel HTTP %d: %s", status, truncate(string(raw), 160))
	}

	var markets []Market
	if err := json.Unmarshal(raw, &markets); err != nil {
		return nil, fmt.Errorf("invalid FanDuel SMP response: %w", err)
	}
	return &PriceResult{Markets: markets, Raw: raw, Status: status, ElapsedMs: elapsed}, nil
}

func postJSON(ctx context.Context, client *http.Client, url string, timeout time.Duration, body []byte, decorate func(*http.Request)) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Match the browser/Node fetch defaults closely enough for the CDN edge.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", DefaultUserAgent)
	if decorate != nil {
		decorate(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
