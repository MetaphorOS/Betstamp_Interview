// Package fanduel implements the FanDuel Ontario book: discovery boards for
// structure, AppSync graphql-ws push for prices, and the SMP price poll as the
// fallback and reconcile. Reconnect and backoff live in the supervisor.
package fanduel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
	"betstampodds/internal/taxonomy"
	"betstampodds/internal/teams"
)

const (
	Name = "fanduel"
	// DefaultAK is FanDuel's public frontend application key (not a
	// credential). It can rotate; FD_AK overrides it.
	DefaultAK        = "FhMFpcPWXMeyZxOx"
	DefaultPageURL   = "https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&includePrices=true&_ak=" + DefaultAK + "&customPageId="
	DefaultInPlayURL = "https://sbapi.on.sportsbook.fanduel.ca/api/in-play?_ak=" + DefaultAK + "&includeTabs=true"
	DefaultSMPURL    = "https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0"
	DefaultWSURL     = "wss://pir.on.sportsbook.fanduel.ca/graphql/realtime"
	DefaultHomeURL   = "https://on.sportsbook.fanduel.ca/"
	AppSyncHost      = "pir.on.sportsbook.fanduel.ca"
	// DefaultUA: SMP answers HTTP 500 with an empty body to Go's default UA.
	DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

	smpBatch = 70
)

// League is one FD league: its discovery page and how to recognise its
// events on the multi-sport in-play board.
type League struct {
	Sport       string
	PageID      string // content-managed-page customPageId
	EventTypeID string // FD sport id
	// The sport type alone is not enough: FanDuel files esports under the
	// same types (eFootball H2H GG League under 6423, eBasketball under
	// 7522), with real team names in the event name. An event must be in the
	// league's competition: by ID, or by the competition's attached name.
	CompetitionID   string
	CompetitionName string
}

// DefaultLeagues: NFL and NBA.
var DefaultLeagues = []League{
	{Sport: "nfl", PageID: "nfl", EventTypeID: "6423", CompetitionID: "12282733", CompetitionName: "NFL"},
	{Sport: "nba", PageID: "nba", EventTypeID: "7522", CompetitionID: "10547864", CompetitionName: "NBA"},
}

// sportOf classifies an event. Events without type info (trimmed fixtures)
// belong to the first league.
// sportOf classifies an event; reason says why it was refused. Events
// without type info (trimmed fixtures) belong to the first league.
func sportOf(leagues []League, e fdEvent, compNames map[string]string) (sport, reason string) {
	if e.EventTypeID == "" {
		return leagues[0].Sport, ""
	}
	reason = "other_sport"
	for _, l := range leagues {
		if e.EventTypeID.String() != l.EventTypeID {
			continue
		}
		cid := e.CompetitionID.String()
		if cid == "" || (l.CompetitionID == "" && l.CompetitionName == "") ||
			cid == l.CompetitionID || (l.CompetitionName != "" && compNames[cid] == l.CompetitionName) {
			return l.Sport, ""
		}
		reason = "other_competition" // right sport, wrong competition: esports, futures, specials
	}
	return "", reason
}

// Config is owned by this package.
type Config struct {
	Leagues    []League
	PageURL    string // discovery board URL prefix; the league's customPageId is appended
	InPlayURL  string // live board, merged into discovery; "-" disables
	Path       string // read one board from a file instead of the network (tests)
	SMPURL     string
	WSURL      string
	HomeURL    string // frontend page the JS bundle is discovered from
	BundlePath string // read-only bundle fallback; FD_MAIN_JS overrides
	APIKey     string
	Timeout    time.Duration
	UserAgent  string
}

type bookImpl struct {
	cfg     Config
	hc      *http.Client
	bundles *bundleProvider
}

func New(cfg Config) (book.Book, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("FD_AK")
	}
	if len(cfg.Leagues) == 0 {
		cfg.Leagues = DefaultLeagues
	}
	if cfg.InPlayURL == "" && cfg.Path == "" {
		cfg.InPlayURL = DefaultInPlayURL
	}
	if cfg.PageURL == "" {
		cfg.PageURL = DefaultPageURL
	}
	if cfg.SMPURL == "" {
		cfg.SMPURL = DefaultSMPURL
	}
	if cfg.WSURL == "" {
		cfg.WSURL = DefaultWSURL
	}
	if cfg.HomeURL == "" {
		cfg.HomeURL = DefaultHomeURL
	}
	if cfg.BundlePath == "" {
		cfg.BundlePath = os.Getenv("FD_MAIN_JS")
	}
	if cfg.BundlePath == "" {
		cfg.BundlePath = "captures/fd-main.js"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 25 * time.Second
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUA
	}
	hc := &http.Client{Timeout: cfg.Timeout}
	return &bookImpl{cfg: cfg, hc: hc, bundles: newBundleProvider(hc, cfg)}, nil
}

func (b *bookImpl) Descriptor() book.Descriptor {
	return book.Descriptor{Name: Name, DisplayName: "FanDuel", PriceKind: book.PriceAmerican}
}

// Tuning: discovery is cached (max-age=30, swr=60) and at most once per
// 2 min; SMP is no-cache and the frontend polls it every 5 s.
func (b *bookImpl) Tuning() book.Tuning {
	return book.Tuning{
		ReconcileEvery:  30 * time.Second,
		ResnapshotEvery: 2 * time.Minute,
		MinSnapshotGap:  2 * time.Minute,
		MinPollGap:      5 * time.Second,
	}
}

// ak is the app key for the REST boards and SMP. An explicit key (Config or
// FD_AK) wins; otherwise the one in FanDuel's current JS bundle, so a
// rotation is picked up without a human; the built-in key is the last resort.
func (b *bookImpl) ak() string {
	if b.cfg.APIKey != "" {
		return b.cfg.APIKey
	}
	if k := b.bundles.appKey(); k != "" {
		return k
	}
	return DefaultAK
}

// keyRejected: a discovery board answers a wrong app key with 404
// {"error":true} (SMP accepts any key, so the boards are the signal).
func keyRejected(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden)
}

// Snapshot reads every league's discovery board and merges the live board, so games
// that have kicked off (and dropped off discovery) are present. Both boards
// are CDN-cached (max-age=30, stale-while-revalidate=60), so they supply
// structure only: prices are then re-read from SMP, which is no-cache.
func (b *bookImpl) Snapshot(ctx context.Context) (*book.Snapshot, error) {
	asOf := time.Now()
	if b.cfg.Path != "" {
		raw, err := os.ReadFile(b.cfg.Path)
		if err != nil {
			return nil, err
		}
		var board fdBoard
		if err := json.Unmarshal(raw, &board); err != nil {
			return nil, fmt.Errorf("fanduel: decode board: %w", err)
		}
		return buildSnapshot(asOf, b.cfg.Leagues, board)
	}
	// Best effort: load the bundle first so its app key is used. Failure is
	// not fatal here; the stream reports it, and ak() falls back.
	_, _ = b.bundles.get(ctx)
	var boards []fdBoard
	for _, l := range b.cfg.Leagues {
		board, err := b.getBoard(ctx, withAK(b.cfg.PageURL+l.PageID, b.ak()))
		if err != nil {
			if keyRejected(err) {
				// Likely a rotated app key: re-read the bundle before the next try.
				slog.Warn("fanduel board rejected the app key; refreshing the bundle", "key", prefix(b.ak()), "error", err)
				b.bundles.invalidate()
			}
			// All leagues or none: a missing board would look like ended games.
			return nil, fmt.Errorf("fanduel: %s board: %w", l.Sport, err)
		}
		boards = append(boards, *board)
	}
	if b.cfg.InPlayURL != "" && b.cfg.InPlayURL != "-" {
		live, err := b.getBoard(ctx, withAK(b.cfg.InPlayURL, b.ak()))
		if err != nil {
			if keyRejected(err) {
				b.bundles.invalidate()
			}
			// Without the live board, live games would look removed.
			return nil, fmt.Errorf("fanduel: in-play board: %w", err)
		}
		boards = append(boards, *live)
	}
	snap, err := buildSnapshot(asOf, b.cfg.Leagues, boards...)
	if err != nil {
		return nil, err
	}
	if err := b.freshPrices(ctx, snap); err != nil {
		// Keep the cached prices, but date them back past the CDN window so
		// they never overwrite anything fresher on the board.
		slog.Warn("fanduel snapshot: SMP price refresh failed; using cached board prices", "error", err)
		snap.AsOf = snap.AsOf.Add(-cdnWindow)
	}
	return snap, nil
}

// cdnWindow is max-age + stale-while-revalidate on the discovery boards.
const cdnWindow = 90 * time.Second

// freshPrices overwrites snapshot prices and lines with an SMP read.
func (b *bookImpl) freshPrices(ctx context.Context, snap *book.Snapshot) error {
	sd := snap.Private.(*seed)
	idx := make(map[string]int, len(snap.Quotes))
	for i, q := range snap.Quotes {
		idx[q.BookSelectionKey] = i
	}
	for start := 0; start < len(sd.ids); start += smpBatch {
		markets, err := b.smp(ctx, sd.ids[start:min(start+smpBatch, len(sd.ids))])
		if err != nil {
			return err
		}
		for i := range markets {
			m := &markets[i]
			ms := sd.markets[m.MarketID]
			if ms == nil {
				continue
			}
			for _, r := range m.RunnerDetails {
				qi, ok := idx[m.MarketID+":"+r.SelectionID.String()]
				rs := ms.runners[r.SelectionID.String()]
				if !ok || rs == nil {
					continue
				}
				q := &snap.Quotes[qi]
				if v := r.WinRunnerOdds.American(); v != nil {
					q.Price.American = int(*v)
				}
				if v := r.WinRunnerOdds.Decimal(); v != nil {
					q.Price.Decimal = *v
				}
				q.Price.Status = fdStatus(m.MarketStatus, r.RunnerStatus)
				q.Price.InPlay = m.IsInPlay()
				if ms.family != model.FamilyMoneyline && r.Handicap != nil {
					q.Line = model.CopyLine(r.Handicap)
				}
				rs.line, rs.american, rs.decimal, rs.status = model.CopyLine(q.Line), q.Price.American, q.Price.Decimal, q.Price.Status
			}
		}
	}
	return nil
}

func (b *bookImpl) getBoard(ctx context.Context, target string) (*fdBoard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 48<<20))
	if err != nil {
		return nil, err
	}
	var board fdBoard
	if err := json.Unmarshal(raw, &board); err != nil {
		return nil, fmt.Errorf("fanduel: decode board: %w", err)
	}
	return &board, nil
}

// HTTPError carries the status and Retry-After so the supervisor can back off.
type HTTPError struct {
	Status     int
	RetryAfter string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("fanduel: HTTP %d", e.Status) }

func (e *HTTPError) RetryAfterDuration() time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(e.RetryAfter)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

func withAK(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("_ak", key)
	u.RawQuery = q.Encode()
	return u.String()
}

// ParseBoards builds a snapshot from raw board bodies (tests and replay).
func ParseBoards(asOf time.Time, raws ...[]byte) (*book.Snapshot, error) {
	boards := make([]fdBoard, 0, len(raws))
	for _, raw := range raws {
		var b fdBoard
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("fanduel: decode board: %w", err)
		}
		boards = append(boards, b)
	}
	return buildSnapshot(asOf, DefaultLeagues, boards...)
}

// ParseLeagueBoards is ParseBoards for a given league set.
func ParseLeagueBoards(asOf time.Time, leagues []League, raws ...[]byte) (*book.Snapshot, error) {
	boards := make([]fdBoard, 0, len(raws))
	for _, raw := range raws {
		var b fdBoard
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("fanduel: decode board: %w", err)
		}
		boards = append(boards, b)
	}
	return buildSnapshot(asOf, leagues, boards...)
}

func buildSnapshot(asOf time.Time, leagues []League, boards ...fdBoard) (*book.Snapshot, error) {
	skipped := map[string]int{}
	rawEvents := map[string]fdEvent{}
	rawMarkets := map[string]fdMarket{}
	compNames := map[string]string{}
	for _, b := range boards {
		for id, c := range b.Attachments.Competitions {
			if c.CompetitionID != "" {
				id = c.CompetitionID.String()
			}
			compNames[id] = c.Name
		}
		for _, e := range b.Attachments.Events {
			rawEvents[e.EventID.String()] = e
		}
		for _, m := range b.Attachments.Markets {
			// Later boards (in-play) win: they are fresher for live markets.
			rawMarkets[m.MarketID] = m
		}
	}
	marketIDs := make([]string, 0, len(rawMarkets))
	for id := range rawMarkets {
		marketIDs = append(marketIDs, id)
	}
	sort.Strings(marketIDs)

	resolved := map[string]fdEventResolved{}
	live := map[string]bool{}
	sd := &seed{markets: map[string]*mktSeed{}}
	var quotes []model.Quote
	for _, id := range marketIDs {
		m := rawMarkets[id]
		class, ok := classifyMain(m.MarketType)
		if !ok {
			skipped["unmapped_market"]++
			continue
		}
		if taxonomy.PeriodFromName(m.MarketName) != model.PeriodFull {
			skipped["non_full_game"]++
			continue
		}
		e, ok := rawEvents[m.EventID.String()]
		if !ok {
			skipped["unknown_event"]++
			continue
		}
		sport, reason := sportOf(leagues, e, compNames)
		if reason != "" {
			skipped[reason]++
			continue
		}
		ev, ok := resolved[m.EventID.String()]
		if !ok {
			var good bool
			ev, good = resolveEvent(sport, e, m)
			if !good {
				skipped["unknown_team"]++
				continue
			}
			resolved[m.EventID.String()] = ev
		}
		ms := &mktSeed{family: class.family, gameRef: ev.event.EventID.String(), runners: map[string]*runnerSeed{}}
		for _, r := range m.Runners {
			q, ok := makeQuote(ev, m, r, class)
			if !ok {
				skipped["unmapped_selection"]++
				continue
			}
			quotes = append(quotes, q)
			ms.runners[r.SelectionID.String()] = &runnerSeed{
				identity: model.IdentityKey(q), line: model.CopyLine(q.Line),
				american: q.Price.American, decimal: q.Price.Decimal, status: q.Price.Status,
			}
			if m.IsInPlay() {
				live[ms.gameRef] = true
			}
		}
		if len(ms.runners) > 0 {
			sd.markets[m.MarketID] = ms
			sd.ids = append(sd.ids, m.MarketID)
		}
	}
	games := make([]model.BookGame, 0, len(resolved))
	for id, ev := range resolved {
		games = append(games, model.BookGame{
			Book: Name, BookGameID: id, Sport: ev.sport, League: ev.sport,
			Name: ev.event.Name, Start: ev.start, Away: ev.away, Home: ev.home,
			Live: live[id], Status: ev.event.Status,
		})
	}
	sort.Slice(games, func(i, j int) bool { return games[i].BookGameID < games[j].BookGameID })
	sort.Strings(sd.ids)
	return &book.Snapshot{
		Games: games, Quotes: quotes, AsOf: asOf, Skipped: skipped,
		Subscription: strings.Join(sd.ids, ","), Private: sd,
	}, nil
}

func resolveEvent(sport string, e fdEvent, m fdMarket) (fdEventResolved, bool) {
	start, err := time.Parse(time.RFC3339, e.OpenDate)
	if err != nil {
		return fdEventResolved{}, false
	}
	var away, home model.Team
	for _, r := range m.Runners {
		if r.Result == nil {
			continue
		}
		t, ok := teams.Resolve(sport, stripLine(r.RunnerName))
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(r.Result.Type)) {
		case "HOME":
			home = t
		case "AWAY":
			away = t
		}
	}
	if home.ID == "" || away.ID == "" {
		// Totals runners are Over/Under; take the teams from "Away @ Home".
		if parts := strings.SplitN(e.Name, " @ ", 2); len(parts) == 2 {
			if t, ok := teams.Resolve(sport, strings.TrimSpace(parts[0])); ok {
				away = t
			}
			if t, ok := teams.Resolve(sport, strings.TrimSpace(parts[1])); ok {
				home = t
			}
		}
	}
	if home.ID == "" || away.ID == "" || home.ID == away.ID {
		return fdEventResolved{}, false
	}
	return fdEventResolved{event: e, away: away, home: home, start: start.UTC(), sport: sport}, true
}

// stripLine drops a trailing signed line from a spread runner name.
func stripLine(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, "+-"); i > 0 && name[i-1] == ' ' {
		if _, err := strconv.ParseFloat(name[i:], 64); err == nil {
			return strings.TrimSpace(name[:i])
		}
	}
	return name
}

type mainClass struct {
	family     string
	stat       string
	comparator string
}

func classifyMain(t string) (mainClass, bool) {
	switch t {
	case "MONEY_LINE":
		return mainClass{family: model.FamilyMoneyline}, true
	case "MATCH_HANDICAP_(2-WAY)":
		return mainClass{family: model.FamilySpread}, true
	case "TOTAL_POINTS_(OVER/UNDER)":
		return mainClass{family: model.FamilyTotal, stat: "points", comparator: model.ComparatorOU}, true
	default:
		return mainClass{}, false
	}
}

// fdStatus: anything but an OPEN market with an ACTIVE runner is suspended.
func fdStatus(marketStatus, runnerStatus string) model.Status {
	if !strings.EqualFold(marketStatus, "OPEN") {
		return model.StatusSuspended
	}
	if runnerStatus != "" && !strings.EqualFold(runnerStatus, "ACTIVE") {
		return model.StatusSuspended
	}
	return model.StatusOpen
}

func makeQuote(ev fdEventResolved, m fdMarket, r fdRunner, c mainClass) (model.Quote, bool) {
	am := r.WinRunnerOdds.American()
	if am == nil {
		return model.Quote{}, false
	}
	dec := 0.0
	if d := r.WinRunnerOdds.Decimal(); d != nil {
		dec = *d
	}
	q := model.Quote{
		Book: Name, BookGameID: ev.event.EventID.String(),
		BookMarketID: m.MarketID, BookSelectionKey: m.MarketID + ":" + r.SelectionID.String(),
		Sport: ev.sport, League: ev.sport, Family: c.family, Period: model.PeriodFull,
		Subject: "event", Stat: c.stat, Comparator: c.comparator, Line: model.CopyLine(r.Handicap),
		MarketName: m.MarketName, SelectionName: r.RunnerName,
		Price: model.Price{American: int(*am), Decimal: dec, Status: fdStatus(m.MarketStatus, r.RunnerStatus), InPlay: m.IsInPlay()},
	}
	side := ""
	if r.Result != nil {
		side = strings.ToUpper(strings.TrimSpace(r.Result.Type))
	}
	switch c.family {
	case model.FamilyMoneyline, model.FamilySpread:
		switch side {
		case "HOME":
			q.Outcome = ev.home.ID
		case "AWAY":
			q.Outcome = ev.away.ID
		default:
			return model.Quote{}, false
		}
		if c.family == model.FamilyMoneyline {
			q.Line = nil
		} else if q.Line == nil {
			if v, ok := taxonomy.SignedLine(r.RunnerName); ok {
				q.Line = v
			}
		}
	case model.FamilyTotal:
		low := strings.ToLower(strings.TrimSpace(r.RunnerName))
		switch {
		case side == "OVER" || strings.HasPrefix(low, "over"):
			q.Outcome = "over"
		case side == "UNDER" || strings.HasPrefix(low, "under"):
			q.Outcome = "under"
		default:
			return model.Quote{}, false
		}
		if q.Line == nil {
			if _, v, ok := taxonomy.OverUnder(r.RunnerName); ok {
				q.Line = v
			}
		}
	}
	if c.family != model.FamilyMoneyline && q.Line == nil {
		return model.Quote{}, false
	}
	return q, true
}

// ID accepts both "123" and 123; FanDuel mixes them.
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

type fdBoard struct {
	Attachments struct {
		Events       map[string]fdEvent  `json:"events"`
		Markets      map[string]fdMarket `json:"markets"`
		Competitions map[string]struct {
			CompetitionID ID     `json:"competitionId"`
			Name          string `json:"name"`
		} `json:"competitions"`
	} `json:"attachments"`
}
type fdEvent struct {
	EventID       ID     `json:"eventId"`
	EventTypeID   ID     `json:"eventTypeId"`
	CompetitionID ID     `json:"competitionId"`
	Name          string `json:"name"`
	OpenDate      string `json:"openDate"`
	Status        string `json:"status"`
}
type fdEventResolved struct {
	sport string
	event fdEvent
	away  model.Team
	home  model.Team
	start time.Time
}

// fdMarket covers discovery markets, SMP markets and push frames. Discovery
// spells it inPlay with runners; SMP and push spell it inplay with
// runnerDetails.
type fdMarket struct {
	MarketID      string     `json:"marketId"`
	EventID       ID         `json:"eventId"`
	MarketName    string     `json:"marketName"`
	MarketType    string     `json:"marketType"`
	MarketStatus  string     `json:"marketStatus"`
	InPlay        *bool      `json:"inPlay"`
	Inplay        *bool      `json:"inplay"`
	Runners       []fdRunner `json:"runners"`
	RunnerDetails []fdRunner `json:"runnerDetails"`
}

func (m *fdMarket) IsInPlay() bool {
	if m.InPlay != nil {
		return *m.InPlay
	}
	return m.Inplay != nil && *m.Inplay
}

type fdRunner struct {
	SelectionID   ID               `json:"selectionId"`
	Handicap      *float64         `json:"handicap"`
	RunnerName    string           `json:"runnerName"`
	RunnerStatus  string           `json:"runnerStatus"`
	Result        *fdResult        `json:"result"`
	WinRunnerOdds *fdWinRunnerOdds `json:"winRunnerOdds"`
}
type fdResult struct {
	Type string `json:"type"`
}
type fdWinRunnerOdds struct {
	AmericanDisplayOdds *struct {
		AmericanOddsInt *float64 `json:"americanOddsInt"`
	} `json:"americanDisplayOdds"`
	TrueOdds *struct {
		DecimalOdds *struct {
			DecimalOdds *float64 `json:"decimalOdds"`
		} `json:"decimalOdds"`
	} `json:"trueOdds"`
}

func (o *fdWinRunnerOdds) American() *float64 {
	if o == nil || o.AmericanDisplayOdds == nil {
		return nil
	}
	return o.AmericanDisplayOdds.AmericanOddsInt
}
func (o *fdWinRunnerOdds) Decimal() *float64 {
	if o == nil || o.TrueOdds == nil || o.TrueOdds.DecimalOdds == nil {
		return nil
	}
	return o.TrueOdds.DecimalOdds.DecimalOdds
}
