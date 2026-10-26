package fd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"sportsbookfeed/internal/clock"
)

// EventOptions configures event-level discovery.
type EventOptions struct {
	BaseURL string
	MGAURL  string
	AK      string
	Timeout time.Duration
	Client  *http.Client
	Region  string
}

func (o EventOptions) ak() string {
	if o.AK != "" {
		return o.AK
	}
	return DefaultAK
}

func (o EventOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 20 * time.Second
}

func (o EventOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

// EventResult is one event discovery response.
type EventResult struct {
	Board     *Board
	Raw       []byte
	Source    string
	Status    int
	ElapsedMs float64
}

const DefaultMGAURL = "https://scan.on.sportsbook.fanduel.ca/api/sports/navigation/facet/v1.0/search"

// FetchEventPage fetches the event page Popular tab. The default event response
// advertises alternates in layout cards but does not load the ladders; the
// Popular tab is the one that carries the priced runners.
func FetchEventPage(ctx context.Context, eventID, tab string, opts EventOptions) (*EventResult, error) {
	if eventID == "" {
		return nil, fmt.Errorf("event id is required")
	}
	if tab == "" {
		tab = "popular"
	}
	base := opts.BaseURL
	if base == "" {
		base = "https://sbapi.on.sportsbook.fanduel.ca/api"
	}
	u := fmt.Sprintf("%s/event-page?_ak=%s&eventId=%s&tab=%s",
		base, url.QueryEscape(opts.ak()), url.QueryEscape(eventID), url.QueryEscape(tab))
	return fetchEventBoard(ctx, u, "popular", opts)
}

// FetchMGAEvent fetches the complete MGA MARKET facet for one event. It
// returns market metadata and runners but not prices; SMP supplies prices.
func FetchMGAEvent(ctx context.Context, eventID string, opts EventOptions) (*EventResult, error) {
	if eventID == "" {
		return nil, fmt.Errorf("event id is required")
	}
	mgaURL := opts.MGAURL
	if mgaURL == "" {
		mgaURL = DefaultMGAURL
	}
	region := opts.Region
	if region == "" {
		region = "NAMERICA"
	}
	eventIDs := any([]string{eventID})
	if id, err := strconv.ParseInt(eventID, 10, 64); err == nil {
		eventIDs = []int64{id}
	}
	body := map[string]any{
		"filter": map[string]any{
			"eventIds":     eventIDs,
			"contentGroup": map[string]any{"language": "en", "regionCode": region},
			"maxResults":   0,
			"selectBy":     "RANK",
		},
		"facets":       []map[string]any{{"type": "EVENT"}, {"type": "MARKET"}},
		"currencyCode": "USD",
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mgaURL, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("X-Application", opts.ak())

	start := clock.MonoMs()
	resp, err := opts.client().Do(req)
	elapsed := clock.MonoMs() - start
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("FanDuel MGA HTTP %d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	var decoded struct {
		Attachments struct {
			Events            map[string]Event  `json:"events"`
			SportsBookMarkets map[string]Market `json:"sportsBookMarkets"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("invalid FanDuel MGA response: %w", err)
	}
	if decoded.Attachments.SportsBookMarkets == nil {
		decoded.Attachments.SportsBookMarkets = map[string]Market{}
	}
	board := &Board{Attachments: Attachments{
		Events:  decoded.Attachments.Events,
		Markets: decoded.Attachments.SportsBookMarkets,
	}}
	return &EventResult{Board: board, Raw: raw, Source: "mga", Status: resp.StatusCode, ElapsedMs: elapsed}, nil
}

func fetchEventBoard(ctx context.Context, u, source string, opts EventOptions) (*EventResult, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	start := clock.MonoMs()
	resp, err := opts.client().Do(req)
	elapsed := clock.MonoMs() - start
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("FanDuel event page HTTP %d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	var board Board
	if err := json.Unmarshal(raw, &board); err != nil {
		return nil, fmt.Errorf("invalid FanDuel event page: %w", err)
	}
	return &EventResult{Board: &board, Raw: raw, Source: source, Status: resp.StatusCode, ElapsedMs: elapsed}, nil
}

// MergeEventBoards combines discovery boards by market id. Markets from the
// first board win for descriptive fields; missing markets are added from later
// boards.
func MergeEventBoards(boards ...*Board) *Board {
	out := &Board{Attachments: Attachments{Events: map[string]Event{}, Markets: map[string]Market{}}}
	for _, b := range boards {
		if b == nil {
			continue
		}
		for k, e := range b.Attachments.Events {
			if _, ok := out.Attachments.Events[k]; !ok {
				out.Attachments.Events[k] = e
			}
		}
		for k, m := range b.Attachments.Markets {
			if _, ok := out.Attachments.Markets[k]; !ok {
				out.Attachments.Markets[k] = m
			}
		}
	}
	return out
}

// MergeBoards is the multi-event alias used by the batch event collector.
func MergeBoards(boards ...*Board) *Board { return MergeEventBoards(boards...) }

// ApplyPrices copies SMP runner prices onto discovery markets by
// marketId+selectionId. Discovery and SMP use different runner fields, so this
// keeps the market/name mapping while filling the missing odds.
func ApplyPrices(board *Board, prices []Market) int {
	byID := map[string]Market{}
	for _, p := range prices {
		byID[p.MarketID] = p
	}
	filled := 0
	for id, m := range board.Attachments.Markets {
		pm, ok := byID[id]
		if !ok {
			continue
		}
		runPrices := map[string]Runner{}
		for _, r := range pm.AllRunners() {
			runPrices[r.SelectionID.String()] = r
		}
		changed := false
		for i := range m.Runners {
			if rp, ok := runPrices[m.Runners[i].SelectionID.String()]; ok {
				m.Runners[i].WinRunnerOdds = rp.WinRunnerOdds
				changed = true
			}
		}
		if changed {
			board.Attachments.Markets[id] = m
			filled++
		}
	}
	return filled
}
