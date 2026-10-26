// Package fd implements the FanDuel Ontario public feed: the content-managed
// page used for discovery, the GraphQL price snapshot, the SMP getMarketPrices
// fallback poll, and the AppSync graphql-ws realtime subscription.
//
// It mirrors extract-fd-nfl.cjs, probe-fd-rest.cjs, compare-fd.cjs and
// probe-ws.cjs. The public frontend application key is a client identifier,
// not a user account token; it rotates and is read from the current bundle.
package fd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// Market types normalised by every script.
const (
	MarketMoneyline = "MONEY_LINE"
	MarketSpread    = "MATCH_HANDICAP_(2-WAY)"
	MarketTotal     = "TOTAL_POINTS_(OVER/UNDER)"

	Book   = "fanduel"
	Region = "ON"

	// DefaultAK is the public frontend application key used by the captured
	// probes. It is an app key, not a credential; rediscover it from the bundle.
	DefaultAK = "FhMFpcPWXMeyZxOx"

	// DefaultBundlePath is the captured main frontend bundle.
	DefaultBundlePath = "captures/fd-main.js"

	// DefaultUserAgent is the browser UA the frontend sends. Go's default
	// Go-http-client UA is not used for the WebSocket handshake.
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

	// Host is the AppSync host baked into the realtime URL.
	Host = "pir.on.sportsbook.fanduel.ca"

	DefaultDiscoveryURL = "https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nfl&includePrices=true&_ak=" + DefaultAK
	DefaultInPlayURL    = "https://sbapi.on.sportsbook.fanduel.ca/api/in-play?_ak=" + DefaultAK + "&includeTabs=true"
	DefaultGraphQLURL   = "https://pir.on.sportsbook.fanduel.ca/graphql"
	DefaultSMPURL       = "https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0"
	DefaultRealtimeURL  = "wss://pir.on.sportsbook.fanduel.ca/graphql/realtime"
)

// ID is a string id that also accepts a bare JSON number. FanDuel mixes both:
// content-managed-page event ids are numbers while market ids are strings.
type ID string

// UnmarshalJSON accepts "123" and 123.
func (id *ID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*id = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*id = ID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = ID(n.String())
	return nil
}

func (id ID) String() string { return string(id) }

// Float converts a numeric id, for compatibility with the JS String() joins.
func (id ID) Float() float64 {
	f, _ := strconv.ParseFloat(string(id), 64)
	return f
}

// AmericanOdds carries both renderings of a price.
type AmericanOdds struct {
	AmericanOdds    *float64 `json:"americanOdds"`
	AmericanOddsInt *float64 `json:"americanOddsInt"`
}

// DecimalOdds is the nested true decimal price.
type DecimalOdds struct {
	DecimalOdds *float64 `json:"decimalOdds"`
}

// TrueOdds is the trueOdds object on a runner.
type TrueOdds struct {
	DecimalOdds *DecimalOdds `json:"decimalOdds"`
}

// WinRunnerOdds is the price block. Different endpoints fill different parts:
// discovery has americanDisplayOdds and trueOdds, SMP adds decimalDisplayOdds.
type WinRunnerOdds struct {
	AmericanDisplayOdds *AmericanOdds `json:"americanDisplayOdds"`
	DecimalDisplayOdds  *DecimalOdds  `json:"decimalDisplayOdds"`
	TrueOdds            *TrueOdds     `json:"trueOdds"`
}

// American returns the American price the scripts compare on, or nil.
func (o *WinRunnerOdds) American() *float64 {
	if o == nil || o.AmericanDisplayOdds == nil {
		return nil
	}
	return o.AmericanDisplayOdds.AmericanOddsInt
}

// Decimal returns the true decimal price, or nil.
func (o *WinRunnerOdds) Decimal() *float64 {
	if o == nil || o.TrueOdds == nil || o.TrueOdds.DecimalOdds == nil {
		return nil
	}
	return o.TrueOdds.DecimalOdds.DecimalOdds
}

// Result marks a runner as HOME/AWAY.
type Result struct {
	Type string `json:"type"`
}

// Runner is either a discovery runner (selectionId, runnerName, result,
// handicap) or a price runner (selectionId, handicap, runnerStatus).
type Runner struct {
	SelectionID   ID             `json:"selectionId"`
	Handicap      *float64       `json:"handicap"`
	RunnerName    string         `json:"runnerName"`
	SortPriority  int            `json:"sortPriority"`
	Result        *Result        `json:"result"`
	RunnerStatus  string         `json:"runnerStatus"`
	WinRunnerOdds *WinRunnerOdds `json:"winRunnerOdds"`
}

// Market models both attachments.markets and getMarketPrices entries.
type Market struct {
	MarketID        string   `json:"marketId"`
	EventID         ID       `json:"eventId"`
	MarketName      string   `json:"marketName"`
	MarketType      string   `json:"marketType"`
	MarketTime      string   `json:"marketTime"`
	MarketStatus    string   `json:"marketStatus"`
	InPlay          *bool    `json:"inPlay"`
	Inplay          *bool    `json:"inplay"`
	BettingType     string   `json:"bettingType"`
	NumberOfRunners int      `json:"numberOfRunners"`
	Runners         []Runner `json:"runners"`
	RunnerDetails   []Runner `json:"runnerDetails"`
}

// IsInPlay reports either spelling, defaulting to false.
func (m *Market) IsInPlay() bool {
	if m.InPlay != nil {
		return *m.InPlay
	}
	if m.Inplay != nil {
		return *m.Inplay
	}
	return false
}

// AllRunners returns runners from either endpoint shape.
func (m *Market) AllRunners() []Runner {
	if len(m.RunnerDetails) > 0 {
		return m.RunnerDetails
	}
	return m.Runners
}

// Event is a discovery event.
type Event struct {
	EventID     ID     `json:"eventId"`
	Name        string `json:"name"`
	EventTypeID ID     `json:"eventTypeId"`
	OpenDate    string `json:"openDate"`
}

// Attachments is the content-managed-page payload.
type Attachments struct {
	Events  map[string]Event  `json:"events"`
	Markets map[string]Market `json:"markets"`
}

// Board is one content-managed-page or in-play response.
type Board struct {
	Attachments Attachments `json:"attachments"`
}

// MainMarketTypes maps the three supported source types to display names.
var MainMarketTypes = map[string]string{
	MarketMoneyline: "moneyline",
	MarketSpread:    "spread",
	MarketTotal:     "total",
}

// IsMainMarketType reports whether a source market type is compared.
func IsMainMarketType(t string) bool {
	_, ok := MainMarketTypes[t]
	return ok
}

// Rows filters discovery markets to the three supported main types and
// flattens them into one row per selection, exactly like extract-fd-nfl.cjs.
func (b *Board) Rows() ([]Row, error) {
	marketIDs := make([]string, 0, len(b.Attachments.Markets))
	for id := range b.Attachments.Markets {
		marketIDs = append(marketIDs, id)
	}
	sort.Slice(marketIDs, func(i, j int) bool { return lessNumeric(marketIDs[i], marketIDs[j]) })

	rows := make([]Row, 0, len(marketIDs)*2)
	for _, key := range marketIDs {
		m := b.Attachments.Markets[key]
		normalized, ok := MainMarketTypes[m.MarketType]
		if !ok {
			continue
		}
		event, ok := b.Attachments.Events[m.EventID.String()]
		if !ok {
			return nil, fmt.Errorf("Missing event %s", m.EventID)
		}
		for _, r := range m.Runners {
			row := Row{
				Book:         Book,
				Region:       Region,
				EventID:      event.EventID.String(),
				Event:        event.Name,
				Start:        event.OpenDate,
				MarketID:     m.MarketID,
				Market:       normalized,
				SelectionID:  r.SelectionID.String(),
				Label:        r.RunnerName,
				InPlay:       m.IsInPlay(),
				MarketStatus: m.MarketStatus,
				RunnerStatus: r.RunnerStatus,
				Line:         r.Handicap,
			}
			if normalized == "moneyline" {
				row.Line = nil
			}
			if r.Result != nil {
				row.Side = r.Result.Type
			}
			row.American = r.WinRunnerOdds.American()
			row.Decimal = r.WinRunnerOdds.Decimal()
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// Row is the normalized selection row emitted by extract-fd-nfl.cjs.
type Row struct {
	Book         string   `json:"book"`
	Region       string   `json:"region"`
	EventID      string   `json:"eventId"`
	Event        string   `json:"event"`
	Start        string   `json:"start"`
	MarketID     string   `json:"marketId"`
	Market       string   `json:"market"`
	SelectionID  string   `json:"selectionId"`
	Side         string   `json:"side"`
	Label        string   `json:"label"`
	Line         *float64 `json:"line"`
	American     *float64 `json:"american"`
	Decimal      *float64 `json:"decimal"`
	InPlay       bool     `json:"inPlay"`
	MarketStatus string   `json:"marketStatus"`
	RunnerStatus string   `json:"runnerStatus"`
}

func lessNumeric(a, b string) bool {
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea == nil && eb == nil {
		return fa < fb
	}
	return a < b
}
