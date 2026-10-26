// Package polymarket implements Polymarket NFL game markets: Gamma REST for
// structure and prices, the CLOB market WebSocket for push. It is a
// prediction market, so its prices are probabilities: the board shows them
// (converted to implied american odds) but never picks them as best.
//
// It exists to prove a third book needs no framework change: this package,
// its fixtures, and one Register line.
package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
	"betstampodds/internal/teams"
)

const (
	Name          = "polymarket"
	NFLSeries     = "12185"
	DefaultURL    = "https://gamma-api.polymarket.com/events?series_id=" + NFLSeries + "&closed=false&limit=100"
	DefaultWSURL  = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
	DefaultUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	typeMoneyline = "moneyline"
	typeSpreads   = "spreads"
	typeTotals    = "totals"
)

// Config is owned by this package.
type Config struct {
	URL       string
	Path      string // read Gamma events from a file (tests)
	WSURL     string
	Timeout   time.Duration
	UserAgent string
}

type bookImpl struct {
	cfg Config
	hc  *http.Client
}

func New(cfg Config) (book.Book, error) {
	if cfg.URL == "" && cfg.Path == "" {
		cfg.URL = DefaultURL
	}
	if cfg.WSURL == "" {
		cfg.WSURL = DefaultWSURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUA
	}
	return &bookImpl{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout}}, nil
}

func (b *bookImpl) Descriptor() book.Descriptor {
	return book.Descriptor{Name: Name, DisplayName: "Polymarket", PriceKind: book.PriceProbability}
}

func (b *bookImpl) Tuning() book.Tuning {
	return book.Tuning{ReconcileEvery: time.Minute, MinSnapshotGap: 30 * time.Second, MinPollGap: 30 * time.Second}
}

func (b *bookImpl) Snapshot(ctx context.Context) (*book.Snapshot, error) {
	asOf := time.Now()
	raw, err := b.read(ctx)
	if err != nil {
		return nil, err
	}
	return ParseEvents(raw, asOf)
}

// Poll re-reads Gamma; it is the only REST price source.
func (b *bookImpl) Poll(ctx context.Context, _ *book.Snapshot) ([]model.Update, error) {
	snap, err := b.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	recv := time.Now()
	out := make([]model.Update, 0, len(snap.Quotes))
	for _, q := range snap.Quotes {
		out = append(out, model.UpdateFromQuote(q, model.SourcePoll, snap.AsOf, recv))
	}
	return out, nil
}

func (b *bookImpl) read(ctx context.Context) ([]byte, error) {
	if b.cfg.Path != "" {
		return os.ReadFile(b.cfg.Path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", b.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("polymarket: gamma HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// token is one outcome token of a main market.
type token struct {
	gameRef, identity, family string
	line                      *float64
	closed                    bool
	live                      bool
}

// seed maps CLOB token ids to slots.
type seed struct {
	tokens map[string]*token
	ids    []string
}

// ParseEvents builds a snapshot from a Gamma events body.
func ParseEvents(raw []byte, asOf time.Time) (*book.Snapshot, error) {
	var events []gEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		var wrap struct {
			Events []gEvent `json:"events"`
		}
		if err2 := json.Unmarshal(raw, &wrap); err2 != nil {
			return nil, fmt.Errorf("polymarket: decode events: %w", err)
		}
		events = wrap.Events
	}
	skipped := map[string]int{}
	sd := &seed{tokens: map[string]*token{}}
	var games []model.BookGame
	var quotes []model.Quote
	for _, e := range events {
		if e.Closed || e.Ended {
			skipped["closed_event"]++
			continue
		}
		away, home, ok := e.teams()
		start := e.start()
		if !ok || start.IsZero() {
			skipped["unknown_team"]++
			continue
		}
		g := model.BookGame{Book: Name, BookGameID: e.ID, Sport: "nfl", League: "nfl", Name: e.Title,
			Start: start, Away: away, Home: home, Live: e.Live}
		n := 0
		for _, m := range mainMarkets(e.Markets) {
			qs, ok := marketQuotes(g, m, e.Live)
			if !ok {
				skipped["unmapped_market"]++
				continue
			}
			for _, q := range qs {
				quotes = append(quotes, q)
				sd.tokens[q.BookSelectionKey] = &token{gameRef: e.ID, identity: model.IdentityKey(q), family: q.Family,
					line: model.CopyLine(q.Line), closed: m.Closed || m.Archived, live: e.Live}
				sd.ids = append(sd.ids, q.BookSelectionKey)
				n++
			}
		}
		if n > 0 {
			games = append(games, g)
		}
	}
	sort.Strings(sd.ids)
	return &book.Snapshot{Games: games, Quotes: quotes, AsOf: asOf, Skipped: skipped,
		Subscription: strings.Join(sd.ids, ","), Private: sd}, nil
}

// mainMarkets keeps the moneyline and the most liquid full-game spread and
// total; every other line is an alternate.
func mainMarkets(ms []gMarket) []gMarket {
	var out []gMarket
	best := map[string]int{}
	for i, m := range ms {
		if m.Closed || m.Archived {
			continue
		}
		switch m.SportsMarketType {
		case typeMoneyline:
			out = append(out, m)
		case typeSpreads, typeTotals:
			if j, ok := best[m.SportsMarketType]; !ok || m.weight() > ms[j].weight() {
				best[m.SportsMarketType] = i
			}
		}
	}
	for _, t := range []string{typeSpreads, typeTotals} {
		if j, ok := best[t]; ok {
			out = append(out, ms[j])
		}
	}
	return out
}

func marketQuotes(g model.BookGame, m gMarket, live bool) ([]model.Quote, bool) {
	names, err1 := stringArray(m.Outcomes)
	tokens, err2 := stringArray(m.ClobTokenIDs)
	if err1 != nil || err2 != nil || len(names) != 2 || len(tokens) != 2 {
		return nil, false
	}
	out := make([]model.Quote, 0, 2)
	for i := range 2 {
		p, ok := m.prob(i)
		if !ok {
			return nil, false
		}
		q := model.Quote{Book: Name, BookGameID: g.BookGameID, BookMarketID: m.ID, BookSelectionKey: tokens[i],
			Sport: "nfl", League: "nfl", Period: model.PeriodFull, Subject: "event",
			MarketName: m.Question, SelectionName: names[i]}
		switch m.SportsMarketType {
		case typeMoneyline, typeSpreads:
			q.Family = model.FamilyMoneyline
			if m.SportsMarketType == typeSpreads {
				q.Family = model.FamilySpread
				if m.Line == nil {
					return nil, false
				}
				// line is quoted for the first outcome; the second is its mirror.
				l := *m.Line
				if i == 1 {
					l = -l
				}
				q.Line = &l
			}
			t, ok := sideTeam(names[i], g)
			if !ok {
				return nil, false
			}
			q.Outcome = t
		case typeTotals:
			q.Family, q.Stat, q.Comparator = model.FamilyTotal, "points", model.ComparatorOU
			if m.Line == nil {
				return nil, false
			}
			q.Line = model.CopyLine(m.Line)
			switch strings.ToLower(strings.TrimSpace(names[i])) {
			case "over":
				q.Outcome = "over"
			case "under":
				q.Outcome = "under"
			default:
				return nil, false
			}
		}
		am, dec := convert(p)
		status := model.StatusOpen
		if am == 0 {
			status = model.StatusSuspended
		}
		q.Price = model.Price{American: am, Decimal: dec, Status: status, InPlay: live}
		out = append(out, q)
	}
	return out, true
}

// sideTeam maps an outcome name ("Seahawks") to one of the game's teams.
func sideTeam(name string, g model.BookGame) (string, bool) {
	t, ok := teams.Resolve("nfl", name)
	if !ok || (t.ID != g.Home.ID && t.ID != g.Away.ID) {
		return "", false
	}
	return t.ID, true
}

// convert turns a probability into implied american and decimal odds. An
// unusable probability returns zeros (shown as suspended).
func convert(p float64) (int, float64) {
	if p <= 0.001 || p >= 0.999 {
		return 0, 0
	}
	dec := 1 / p
	var am int
	if p > 0.5 {
		am = int(math.Round(-100 / (dec - 1)))
	} else {
		am = int(math.Round((dec - 1) * 100))
	}
	if am > -100 && am < 100 {
		am = 100
	}
	return am, dec
}

type gTeam struct {
	Name     string `json:"name"`
	Ordering string `json:"ordering"`
}

type gEvent struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	EndDate   string    `json:"endDate"`
	StartTime string    `json:"startTime"`
	Live      bool      `json:"live"`
	Ended     bool      `json:"ended"`
	Closed    bool      `json:"closed"`
	Teams     []gTeam   `json:"teams"`
	Markets   []gMarket `json:"markets"`
}

// start: for a Gamma game event, endDate is the game start.
func (e gEvent) start() time.Time {
	for _, s := range []string{e.StartTime, e.EndDate} {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (e gEvent) teams() (away, home model.Team, ok bool) {
	var a, h string
	for _, t := range e.Teams {
		switch strings.ToLower(t.Ordering) {
		case "away":
			a = t.Name
		case "home":
			h = t.Name
		}
	}
	if a == "" || h == "" {
		if parts := strings.Split(e.Title, " vs. "); len(parts) == 2 {
			a, h = parts[0], parts[1]
		}
	}
	away, okA := teams.Resolve("nfl", a)
	home, okH := teams.Resolve("nfl", h)
	return away, home, okA && okH && away.ID != home.ID
}

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	*f = flexFloat(v)
	return err
}

// gMarket: Outcomes, OutcomePrices and ClobTokenIds are JSON-encoded strings.
type gMarket struct {
	ID               string    `json:"id"`
	Question         string    `json:"question"`
	Line             *float64  `json:"line"`
	Outcomes         string    `json:"outcomes"`
	OutcomePrices    string    `json:"outcomePrices"`
	ClobTokenIDs     string    `json:"clobTokenIds"`
	SportsMarketType string    `json:"sportsMarketType"`
	BestBid          *float64  `json:"bestBid"`
	BestAsk          *float64  `json:"bestAsk"`
	Closed           bool      `json:"closed"`
	Archived         bool      `json:"archived"`
	Volume           flexFloat `json:"volume"`
	Liquidity        flexFloat `json:"liquidity"`
}

func (m gMarket) weight() float64 {
	if m.Volume > 0 {
		return float64(m.Volume)
	}
	return float64(m.Liquidity)
}

// prob is the top-of-book mid when there is one, else Gamma's outcome price.
// bestBid/bestAsk are for the first token; the second is the complement.
func (m gMarket) prob(i int) (float64, bool) {
	if m.BestBid != nil && m.BestAsk != nil && *m.BestBid > 0 && *m.BestAsk > 0 && *m.BestAsk <= 1 {
		mid := (*m.BestBid + *m.BestAsk) / 2
		if i == 1 {
			mid = 1 - mid
		}
		return mid, true
	}
	ps, err := stringArray(m.OutcomePrices)
	if err != nil || i >= len(ps) {
		return 0, false
	}
	p, err := strconv.ParseFloat(ps[i], 64)
	return p, err == nil
}

func stringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var out []string
	return out, json.Unmarshal([]byte(raw), &out)
}
