// Package polymarket implements Polymarket NFL game markets: Gamma REST for
// structure and prices, the CLOB market WebSocket for push. It is a
// prediction market: an outcome token costs p dollars and pays $1. Its price
// here is the best ASK (what buying that outcome costs right now), converted
// to american/decimal odds, so it is comparable with a sportsbook price.
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
	"sync/atomic"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
	"betstampodds/internal/teams"
)

const (
	Name          = "polymarket"
	EventsURL     = "https://gamma-api.polymarket.com/events?closed=false&limit=100&series_id="
	DefaultWSURL  = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
	DefaultUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	typeMoneyline = "moneyline"
	typeSpreads   = "spreads"
	typeTotals    = "totals"
)

// Series is one Gamma series (a league).
type Series struct {
	Sport string
	ID    string
}

// DefaultSeries: NFL and NBA.
var DefaultSeries = []Series{{Sport: "nfl", ID: "12185"}, {Sport: "nba", ID: "10345"}}

// Config is owned by this package.
type Config struct {
	Series    []Series
	Path      string // read NFL Gamma events from a file (tests)
	WSURL     string
	Timeout   time.Duration
	UserAgent string
}

type bookImpl struct {
	cfg Config
	hc  *http.Client
	// live is game -> in play, from the most recent snapshot. The stream
	// reads it on every update: its own seed is fixed at connect, and the
	// stream only restarts when the token set changes, so a game that kicks
	// off would otherwise keep streaming as pregame.
	live atomic.Pointer[map[string]bool]
}

// publishLive swaps in the latest snapshot's in-play flags.
func (b *bookImpl) publishLive(snap *book.Snapshot) {
	m := make(map[string]bool, len(snap.Games))
	for _, g := range snap.Games {
		m[g.BookGameID] = g.Live
	}
	b.live.Store(&m)
}

// liveOf reports a game's latest in-play flag, if a snapshot has seen it.
func (b *bookImpl) liveOf(gameRef string) (bool, bool) {
	m := b.live.Load()
	if m == nil {
		return false, false
	}
	v, ok := (*m)[gameRef]
	return v, ok
}

func New(cfg Config) (book.Book, error) {
	if len(cfg.Series) == 0 {
		cfg.Series = DefaultSeries
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
	// Resnapshot every minute: the main spread/total line moves during a game,
	// and a new main line means new tokens, so the stream must resubscribe.
	// No separate live poll: it reads the same Gamma endpoint as the refresh.
	return book.Tuning{ReconcileEvery: 24 * time.Hour, ResnapshotEvery: time.Minute,
		MinSnapshotGap: 30 * time.Second, MinPollGap: 30 * time.Second}
}

func (b *bookImpl) Snapshot(ctx context.Context) (*book.Snapshot, error) {
	snap, err := b.snapshot(ctx)
	if err == nil {
		b.publishLive(snap)
	}
	return snap, err
}

func (b *bookImpl) snapshot(ctx context.Context) (*book.Snapshot, error) {
	asOf := time.Now()
	if b.cfg.Path != "" {
		raw, err := os.ReadFile(b.cfg.Path)
		if err != nil {
			return nil, err
		}
		return ParseEvents(raw, asOf)
	}
	var docs []seriesDoc
	for _, sr := range b.cfg.Series {
		raw, err := b.read(ctx, EventsURL+sr.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sr.Sport, err)
		}
		events, err := decodeEvents(raw)
		if err != nil {
			return nil, err
		}
		docs = append(docs, seriesDoc{sport: sr.Sport, events: events})
	}
	return buildSnapshot(asOf, docs...), nil
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

func (b *bookImpl) read(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

// ParseEvents builds a snapshot from an NFL Gamma events body.
func ParseEvents(raw []byte, asOf time.Time) (*book.Snapshot, error) {
	return ParseSeriesEvents(raw, "nfl", asOf)
}

// ParseSeriesEvents builds a snapshot from one series' Gamma events body.
func ParseSeriesEvents(raw []byte, sport string, asOf time.Time) (*book.Snapshot, error) {
	events, err := decodeEvents(raw)
	if err != nil {
		return nil, err
	}
	return buildSnapshot(asOf, seriesDoc{sport: sport, events: events}), nil
}

func decodeEvents(raw []byte) ([]gEvent, error) {
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
	return events, nil
}

type seriesDoc struct {
	sport  string
	events []gEvent
}

func buildSnapshot(asOf time.Time, docs ...seriesDoc) *book.Snapshot {
	skipped := map[string]int{}
	sd := &seed{tokens: map[string]*token{}}
	var games []model.BookGame
	var quotes []model.Quote
	for _, doc := range docs {
		g, q := addSeries(sd, doc, skipped)
		games = append(games, g...)
		quotes = append(quotes, q...)
	}
	sort.Strings(sd.ids)
	return &book.Snapshot{Games: games, Quotes: quotes, AsOf: asOf, Skipped: skipped,
		Subscription: strings.Join(sd.ids, ","), Private: sd}
}

func addSeries(sd *seed, doc seriesDoc, skipped map[string]int) ([]model.BookGame, []model.Quote) {
	sport := doc.sport
	var games []model.BookGame
	var quotes []model.Quote
	for _, e := range doc.events {
		if e.Closed || e.Ended {
			skipped["closed_event"]++
			continue
		}
		away, home, ok := e.teams(sport)
		start := e.start()
		if !ok || start.IsZero() {
			skipped["unknown_team"]++
			continue
		}
		g := model.BookGame{Book: Name, BookGameID: e.ID, Sport: sport, League: sport, Name: e.Title,
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
	return games, quotes
}

// mainMarkets keeps the moneyline and, for spread and total, the most
// balanced line: the one whose price is closest to 50/50, as a sportsbook's
// main line is. Volume is only a tiebreak; it accumulates on the opening line
// and points at a stale line once a game is under way.
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
			if m.imbalance() > 0.5 {
				continue // no live book
			}
			j, ok := best[m.SportsMarketType]
			if !ok || m.imbalance() < ms[j].imbalance()-1e-9 ||
				(m.imbalance() < ms[j].imbalance()+1e-9 && m.weight() > ms[j].weight()) {
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
		p, executable, ok := m.prob(i)
		if !ok {
			return nil, false
		}
		q := model.Quote{Book: Name, BookGameID: g.BookGameID, BookMarketID: m.ID, BookSelectionKey: tokens[i],
			Sport: g.Sport, League: g.Sport, Period: model.PeriodFull, Subject: "event",
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
		if am == 0 || !executable {
			status = model.StatusSuspended
		}
		q.Price = model.Price{American: am, Decimal: dec, Status: status, InPlay: live}
		out = append(out, q)
	}
	return out, true
}

// sideTeam maps an outcome name ("Seahawks") to one of the game's teams.
func sideTeam(name string, g model.BookGame) (string, bool) {
	t, ok := teams.Resolve(g.Sport, name)
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

func (e gEvent) teams(sport string) (away, home model.Team, ok bool) {
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
	away, okA := teams.Resolve(sport, a)
	home, okH := teams.Resolve(sport, h)
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

// imbalance is |mid - 0.5| of the first token, or 1 without a live book.
func (m gMarket) imbalance() float64 {
	if m.BestBid == nil || m.BestAsk == nil || *m.BestBid <= 0 || *m.BestAsk <= 0 || *m.BestAsk > 1 {
		return 1
	}
	return math.Abs((*m.BestBid+*m.BestAsk)/2 - 0.5)
}

func (m gMarket) weight() float64 {
	if m.Volume > 0 {
		return float64(m.Volume)
	}
	return float64(m.Liquidity)
}

// prob is the cost of buying outcome i now. Gamma's bestBid/bestAsk belong
// to the first token, so buying the second costs 1 - bestBid. Without a book
// it falls back to Gamma's last outcome price, which is not executable, so
// executable is false and the quote shows as suspended (never best).
func (m gMarket) prob(i int) (p float64, executable, ok bool) {
	if m.BestBid != nil && m.BestAsk != nil && *m.BestBid > 0 && *m.BestAsk > 0 && *m.BestAsk <= 1 {
		if i == 0 {
			return *m.BestAsk, true, true
		}
		return 1 - *m.BestBid, true, true
	}
	ps, err := stringArray(m.OutcomePrices)
	if err != nil || i >= len(ps) {
		return 0, false, false
	}
	p, err = strconv.ParseFloat(ps[i], 64)
	return p, false, err == nil
}

func stringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var out []string
	return out, json.Unmarshal([]byte(raw), &out)
}
