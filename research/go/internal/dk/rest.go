package dk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"sportsbookfeed/internal/clock"
)

// FetchOptions controls FetchSnapshot. Zero values select the same defaults
// as dk-http.cjs, including the 15 s abort timeout.
type FetchOptions struct {
	URL       string
	UserAgent string
	Timeout   time.Duration
	Client    *http.Client
}

// Result is one snapshot plus the receipt metadata the probes log.
type Result struct {
	Data           *Data   `json:"-"`
	Raw            []byte  `json:"-"`
	Status         int     `json:"-"`
	ReceivedMonoMs float64 `json:"receivedMonoMs"`
	ReceivedAt     int64   `json:"receivedAt"`
	ElapsedMs      float64 `json:"elapsedMs"`
	CacheControl   string  `json:"cacheControl"`
	Age            *string `json:"age"`
}

// Counts returns the number of events, markets and selections.
func (r *Result) Counts() (events, markets, selections int) {
	return len(r.Data.Events), len(r.Data.Markets), len(r.Data.Selections)
}

// UserAgent resolves the same override chain as dk-http.cjs:
// explicit option, then DK_USER_AGENT, then the tested Chrome UA.
func (o FetchOptions) userAgent() string {
	if o.UserAgent != "" {
		return o.UserAgent
	}
	if ua := os.Getenv("DK_USER_AGENT"); ua != "" {
		return ua
	}
	return DefaultUserAgent
}

func (o FetchOptions) url() string {
	if o.URL != "" {
		return o.URL
	}
	return DefaultLeagueURL
}

func (o FetchOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 15 * time.Second
}

func (o FetchOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

// FetchSnapshot downloads and validates one DraftKings league snapshot.
//
// Validation mirrors dk-http.cjs: a non-2xx response fails with
// "DraftKings HTTP <status>: <first 160 bytes>", and events, markets and
// selections must each be JSON arrays.
func FetchSnapshot(ctx context.Context, opts FetchOptions) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.url(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", opts.userAgent())
	req.Header.Set("Accept", "application/json")

	startMono := clock.MonoMs()
	resp, err := opts.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	elapsed := clock.MonoMs() - startMono
	received := clock.EpochMs()
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("DraftKings HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
	}

	data, err := parseData(body)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Data:           data,
		Raw:            body,
		Status:         resp.StatusCode,
		ReceivedMonoMs: clock.MonoMs(),
		ReceivedAt:     received,
		ElapsedMs:      elapsed,
		CacheControl:   resp.Header.Get("Cache-Control"),
	}
	if age := resp.Header.Get("Age"); age != "" {
		res.Age = &age
	}
	return res, nil
}

// parseData enforces the snapshot schema and decodes the typed view.
func parseData(body []byte) (*Data, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	for _, field := range []string{"events", "markets", "selections"} {
		raw, ok := top[field]
		if !ok || !isArray(raw) {
			return nil, fmt.Errorf("Invalid snapshot: missing %s", field)
		}
	}
	var data Data
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func isArray(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "[")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SortedPartials returns the subscription partials keyed by the source
// subscription key, sorted so repeated runs build identical subscriptions.
func SortedPartials(m map[string]SubscriptionPartial) []SubscriptionPartial {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]SubscriptionPartial, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// ErrNoPartials is returned when a snapshot has no usable subscription filters.
var ErrNoPartials = errors.New("No subscription filters in snapshot")
