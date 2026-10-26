// Package draftkings implements the DraftKings Ontario book: a REST snapshot
// of the NFL game-lines subcategory and a WebSocket delta stream. Reconnect
// and backoff live in the supervisor; this package only fetches, parses and
// maps.
package draftkings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
	"betstampodds/internal/taxonomy"
	"betstampodds/internal/teams"
)

const (
	Name      = "draftkings"
	LeagueURL = "https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/"
	// DefaultUA must look like a current Chrome: Akamai returns 403 for Go's
	// default UA, an older Chrome UA, or no headers at all.
	DefaultUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
	DefaultWSURL = "wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en-US"
)

// League is one DK league whose game-lines snapshot and partials we read.
type League struct {
	Sport string // canonical sport, also the team registry
	ID    string // DK leagueId
}

// DefaultLeagues: NFL and NBA game lines.
var DefaultLeagues = []League{{Sport: "nfl", ID: "88808"}, {Sport: "nba", ID: "42648"}}

// Config is owned by this book package, not by the framework.
type Config struct {
	Leagues   []League
	Path      string // read one NFL snapshot from a file instead of the network (tests)
	WSURL     string
	Timeout   time.Duration
	UserAgent string // DK_USER_AGENT overrides the default
}

type bookImpl struct {
	cfg Config
	hc  *http.Client

	mu       sync.Mutex
	lastRead time.Time // Snapshot and Poll share one REST endpoint and one budget
}

// restGap is the DK REST budget: one read per league per 15 s across
// Snapshot and Poll.
const restGap = 15 * time.Second

// New constructs the book.
func New(cfg Config) (book.Book, error) {
	if len(cfg.Leagues) == 0 {
		cfg.Leagues = DefaultLeagues
	}
	if cfg.WSURL == "" {
		cfg.WSURL = DefaultWSURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = os.Getenv("DK_USER_AGENT")
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUA
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &bookImpl{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout}}, nil
}

func (b *bookImpl) Descriptor() book.Descriptor {
	return book.Descriptor{Name: Name, DisplayName: "DraftKings", PriceKind: book.PriceAmerican}
}

// Tuning: the snapshot is the only REST read DK has, so it doubles as the
// poll. It is Cache-Control max-age=1, but we keep it to one per 15 s.
func (b *bookImpl) Tuning() book.Tuning {
	return book.Tuning{
		ReconcileEvery: 30 * time.Second,
		MinSnapshotGap: restGap,
		MinPollGap:     restGap,
	}
}

// Snapshot fetches structure and current prices for every league. All
// leagues must succeed: a missing league would look like its games ended.
func (b *bookImpl) Snapshot(ctx context.Context) (*book.Snapshot, error) {
	asOf := time.Now()
	if b.cfg.Path != "" {
		raw, err := os.ReadFile(b.cfg.Path)
		if err != nil {
			return nil, err
		}
		return ParseSnapshot(raw, asOf)
	}
	b.mu.Lock()
	b.lastRead = time.Now()
	b.mu.Unlock()
	docs := make([]leagueDoc, 0, len(b.cfg.Leagues))
	for _, l := range b.cfg.Leagues {
		raw, err := b.read(ctx, LeagueURL+l.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.Sport, err)
		}
		var doc dkDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("draftkings: decode %s snapshot: %w", l.Sport, err)
		}
		docs = append(docs, leagueDoc{league: l, doc: doc})
	}
	return buildSnapshot(asOf, docs...)
}

// Poll re-reads the snapshot and returns its quotes as poll updates. DK has no
// cheaper price-only endpoint.
func (b *bookImpl) Poll(ctx context.Context, _ *book.Snapshot) ([]model.Update, error) {
	b.mu.Lock()
	recent := time.Since(b.lastRead) < restGap
	b.mu.Unlock()
	if recent {
		// A snapshot just read the same endpoint; nothing newer to fetch.
		return nil, nil
	}
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
		return nil, &HTTPError{Status: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// HTTPError carries the status and Retry-After so the supervisor can back off.
type HTTPError struct {
	Status     int
	RetryAfter string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("draftkings: HTTP %d", e.Status) }

// RetryAfterDuration parses Retry-After seconds; zero when absent.
func (e *HTTPError) RetryAfterDuration() time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(e.RetryAfter)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// ParseSnapshot builds the snapshot and the stream seed from an NFL body.
func ParseSnapshot(raw []byte, asOf time.Time) (*book.Snapshot, error) {
	return ParseLeagueSnapshot(raw, DefaultLeagues[0], asOf)
}

// ParseLeagueSnapshot builds a snapshot from one league's REST body.
func ParseLeagueSnapshot(raw []byte, l League, asOf time.Time) (*book.Snapshot, error) {
	var doc dkDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("draftkings: decode snapshot: %w", err)
	}
	return buildSnapshot(asOf, leagueDoc{league: l, doc: doc})
}

type leagueDoc struct {
	league League
	doc    dkDoc
}

func buildSnapshot(asOf time.Time, docs ...leagueDoc) (*book.Snapshot, error) {
	skipped := map[string]int{}
	sd := newState()
	var games []model.BookGame
	var quotes []model.Quote
	var partials []map[string]any
	for _, ld := range docs {
		sd.leagueSport[ld.league.ID] = ld.league.Sport
		g, q := addLeague(sd, ld.league.Sport, ld.doc, skipped)
		games = append(games, g...)
		quotes = append(quotes, q...)
		partials = append(partials, entityPartials(ld.doc.SubscriptionPartials)...)
	}
	sd.partials = partials
	return &book.Snapshot{
		Games: games, Quotes: quotes, AsOf: asOf, Skipped: skipped,
		Subscription: subscriptionKey(sd.partials),
		Private:      sd,
	}, nil
}

func addLeague(sd *state, sport string, doc dkDoc, skipped map[string]int) ([]model.BookGame, []model.Quote) {
	events := map[string]dkEvent{}
	games := make([]model.BookGame, 0, len(doc.Events))
	for _, e := range doc.Events {
		home, away, ok := resolveTeams(sport, e.Participants)
		if !ok {
			skipped["unknown_team"]++
			sd.ignoredEvents[e.ID.String()] = true
			continue
		}
		start, err := time.Parse(time.RFC3339, e.StartEventDate)
		if err != nil {
			skipped["bad_time"]++
			sd.ignoredEvents[e.ID.String()] = true
			continue
		}
		live := isLive(e.Status)
		g := model.BookGame{
			Book: Name, BookGameID: e.ID.String(), Sport: sport, League: sport,
			Name: e.Name, Start: start.UTC(), Away: away, Home: home, Live: live, Status: e.Status,
		}
		e.homeID, e.awayID, e.sport = home.ID, away.ID, sport
		events[g.BookGameID] = e
		sd.events[g.BookGameID] = &evState{homeID: home.ID, awayID: away.ID, live: live, sport: sport}
		games = append(games, g)
	}

	marketsByID := make(map[string]dkMarket, len(doc.Markets))
	for _, m := range doc.Markets {
		marketsByID[m.ID.String()] = m
		c, ok := classifyMain(m)
		if !ok {
			sd.ignored[m.ID.String()] = true
			continue
		}
		if _, ok := events[m.EventID.String()]; !ok {
			sd.ignored[m.ID.String()] = true
			continue
		}
		sd.markets[m.ID.String()] = &mktState{
			family: c.family, stat: c.stat, comparator: c.comparator,
			eventID: m.EventID.String(), suspended: m.suspended(), sels: map[string]bool{},
		}
	}

	quotes := make([]model.Quote, 0, len(doc.Selections))
	for _, s := range doc.Selections {
		m, ok := marketsByID[s.MarketID.String()]
		if !ok {
			skipped["unknown_market"]++
			sd.skipSels[s.ID.String()] = true
			continue
		}
		c, ok := classifyMain(m)
		if !ok {
			skipped["unmapped_market"]++
			sd.skipSels[s.ID.String()] = true
			continue
		}
		e, ok := events[m.EventID.String()]
		if !ok {
			skipped["unknown_event"]++
			sd.skipSels[s.ID.String()] = true
			continue
		}
		q, ok := makeQuote(e, m, s, c)
		if !ok {
			skipped["unmapped_selection"]++
			sd.skipSels[s.ID.String()] = true
			continue
		}
		quotes = append(quotes, q)
		ms := sd.markets[q.BookMarketID]
		ms.sels[q.BookSelectionKey] = true
		key := slotOwnerKey(q.BookGameID, model.IdentityKey(q))
		sd.sels[q.BookSelectionKey] = &selState{
			identity: model.IdentityKey(q), gameRef: q.BookGameID, marketID: q.BookMarketID, family: q.Family,
			line: model.CopyLine(q.Line), american: q.Price.American, decimal: q.Price.Decimal,
			suspended: selSuspended(s.Status),
		}
		sd.owner[key] = q.BookSelectionKey
	}
	return games, quotes
}

// entityPartials keeps only the "events" partials. The content-card partials
// are not needed for game lines.
func entityPartials(all map[string]map[string]any) []map[string]any {
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []map[string]any
	for _, k := range keys {
		p := all[k]
		if ent, _ := p["entity"].(string); ent == "events" {
			out = append(out, p)
		}
	}
	return out
}

func subscriptionKey(partials []map[string]any) string {
	raw, _ := json.Marshal(partials)
	return string(raw)
}

func resolveTeams(sport string, ps []dkParticipant) (home, away model.Team, ok bool) {
	homeName, awayName := "", ""
	for _, p := range ps {
		switch strings.ToLower(p.VenueRole) {
		case "home":
			homeName = p.Name
		case "away":
			awayName = p.Name
		}
	}
	h, okH := teams.Resolve(sport, homeName)
	a, okA := teams.Resolve(sport, awayName)
	return h, a, okH && okA
}

func isLive(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "STARTED", "IN_PROGRESS", "LIVE":
		return true
	}
	return false
}

func selSuspended(status string) bool {
	return strings.EqualFold(status, "Suspended") || strings.EqualFold(status, "Closed")
}

type mainClass struct {
	family     string
	stat       string
	comparator string
}

// classifyMain uses the market type name plus a full-game period check. DK
// marks alternates main:true too, so that flag is never used.
func classifyMain(m dkMarket) (mainClass, bool) {
	if taxonomy.PeriodFromName(m.Name) != model.PeriodFull {
		return mainClass{}, false
	}
	switch m.MarketType.Name {
	case "Moneyline":
		return mainClass{family: model.FamilyMoneyline}, true
	case "Spread":
		return mainClass{family: model.FamilySpread}, true
	case "Total":
		return mainClass{family: model.FamilyTotal, stat: "points", comparator: model.ComparatorOU}, true
	default:
		return mainClass{}, false
	}
}

// outcomeFor maps a DK outcomeType to the identity outcome: team ID for
// moneyline and spread, over/under for totals.
func outcomeFor(family, outcomeType, homeID, awayID string) (string, bool) {
	ot := strings.ToLower(strings.TrimSpace(outcomeType))
	switch family {
	case model.FamilyMoneyline, model.FamilySpread:
		switch ot {
		case "home":
			return homeID, homeID != ""
		case "away":
			return awayID, awayID != ""
		}
	case model.FamilyTotal:
		if ot == "over" || ot == "under" {
			return ot, true
		}
	}
	return "", false
}

func makeQuote(e dkEvent, m dkMarket, s dkSelection, c mainClass) (model.Quote, bool) {
	if s.DisplayOdds == nil {
		return model.Quote{}, false
	}
	price, err := parseAmerican(s.DisplayOdds.American)
	if err != nil {
		return model.Quote{}, false
	}
	outcome, ok := outcomeFor(c.family, s.OutcomeType, e.homeID, e.awayID)
	if !ok {
		return model.Quote{}, false
	}
	status := model.StatusOpen
	if m.suspended() || selSuspended(s.Status) {
		status = model.StatusSuspended
	}
	line := s.Points
	if c.family == model.FamilyMoneyline {
		line = nil
	}
	dec := 0.0
	if s.TrueOdds != nil {
		dec = *s.TrueOdds
	}
	return model.Quote{
		Book: Name, BookGameID: e.ID.String(), BookMarketID: m.ID.String(),
		BookSelectionKey: s.ID.String(), Sport: e.sport, League: e.sport,
		Family: c.family, Period: model.PeriodFull, Subject: "event",
		Stat: c.stat, Comparator: c.comparator, Outcome: outcome, Line: model.CopyLine(line),
		MarketName: m.Name, SelectionName: s.Label,
		Price: model.Price{American: price, Decimal: dec, Status: status, InPlay: isLive(e.Status)},
	}, true
}

// parseAmerican accepts DK's Unicode minus and EVEN.
func parseAmerican(s string) (int, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "−", "-"))
	if s == "" {
		return 0, errors.New("empty american")
	}
	if strings.EqualFold(s, "EVEN") {
		return 100, nil
	}
	return strconv.Atoi(strings.TrimPrefix(s, "+"))
}

// ID tolerates quoted and numeric ids.
type ID string

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
	*id = ID(b)
	return nil
}
func (id ID) String() string { return string(id) }

type dkParticipant struct {
	Name      string `json:"name"`
	VenueRole string `json:"venueRole"`
}
type dkEvent struct {
	ID             ID              `json:"id"`
	Name           string          `json:"name"`
	StartEventDate string          `json:"startEventDate"`
	Status         string          `json:"status"`
	Participants   []dkParticipant `json:"participants"`
	LeagueID       ID              `json:"leagueId"`
	homeID         string
	awayID         string
	sport          string
}
type dkMarketType struct {
	Name string `json:"name"`
}

// dkMarket serves both the snapshot and deltas. IsSuspended is a pointer
// because a change.markets delta without it means "unchanged".
type dkMarket struct {
	ID          ID           `json:"id"`
	EventID     ID           `json:"eventId"`
	Name        string       `json:"name"`
	MarketType  dkMarketType `json:"marketType"`
	IsSuspended *bool        `json:"isSuspended"`
}

func (m dkMarket) suspended() bool { return m.IsSuspended != nil && *m.IsSuspended }

type dkOdds struct {
	American string `json:"american"`
}

// dkSelection serves both the snapshot and deltas. Change deltas are
// partial, so every price/line field is a pointer: nil means "unchanged".
type dkSelection struct {
	ID                  ID       `json:"id"`
	MarketID            ID       `json:"marketId"`
	ReplacedSelectionID string   `json:"replacedSelectionId"`
	Label               string   `json:"label"`
	OutcomeType         string   `json:"outcomeType"`
	Points              *float64 `json:"points"`
	DisplayOdds         *dkOdds  `json:"displayOdds"`
	TrueOdds            *float64 `json:"trueOdds"`
	Status              string   `json:"status"`
}
type dkDoc struct {
	Events               []dkEvent                 `json:"events"`
	Markets              []dkMarket                `json:"markets"`
	Selections           []dkSelection             `json:"selections"`
	SubscriptionPartials map[string]map[string]any `json:"subscriptionPartials"`
}
