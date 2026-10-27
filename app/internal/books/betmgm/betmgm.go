// Package betmgm implements the BetMGM Ontario book: the public sportsbook
// REST API (cds-api) for structure and prices, and its SignalR push channel
// for whole-market updates. NFL fixtures are "V2" (6:48482), NBA fixtures
// "V1" (20030815); both arrive in the same optionMarkets shape over REST and
// over the "<context>|all" push topic. Reconnect and backoff live in the
// supervisor; this package only fetches, parses and maps.
package betmgm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

const (
	Name        = "betmgm"
	DefaultBase = "https://www.betmgm.ca"
	DefaultWS   = "wss://cds-push.betmgm.ca/ws-1-0"
	// DefaultAccessID is the frontend's public access id (sent by every
	// logged-out page load, not a credential). BETMGM_ACCESS_ID overrides it.
	DefaultAccessID = "ZjNmOGM4OTAtZmVhOS00ZTZiLWE5MDQtNGVjNWM5MDkxNjQ1"
	// DefaultUA: the REST API answers curl's or Go's UA with an HTML block
	// page; a current Chrome UA alone is enough.
	DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

	pageSize = 50
)

// League is one BetMGM competition.
type League struct {
	Sport         string // canonical sport, also the team registry
	SportID       string
	CompetitionID string
	// Props: read each pregame fixture's full market list (fixture-view)
	// for player props. The league listing has game lines only.
	Props bool
}

// DefaultLeagues: NFL (game lines and player props) and NBA game lines.
var DefaultLeagues = []League{
	{Sport: "nfl", SportID: "11", CompetitionID: "35", Props: true},
	{Sport: "nba", SportID: "7", CompetitionID: "6004"},
}

// Config is owned by this package.
type Config struct {
	Leagues   []League
	Base      string
	WSURL     string
	AccessID  string
	UserAgent string
	Timeout   time.Duration
	// ViewGap paces the per-fixture reads: back-to-back requests draw an
	// occasional 403 that passes on retry.
	ViewGap time.Duration
	// RetryWait is the pause before retrying a refused fixture read.
	RetryWait time.Duration
}

type bookImpl struct {
	cfg Config
	hc  *http.Client

	mu    sync.Mutex
	views map[string]cachedView // fixture id -> last full read, for when a read is refused
}

type cachedView struct {
	at time.Time
	fx mgmFixture
}

// viewKeep: a refused fixture read reuses the last good one this long, so
// one 403 does not take a game's props off the board until the next
// snapshot.
const viewKeep = 20 * time.Minute

// New constructs the book.
func New(cfg Config) (book.Book, error) {
	if len(cfg.Leagues) == 0 {
		cfg.Leagues = DefaultLeagues
	}
	if cfg.Base == "" {
		cfg.Base = DefaultBase
	}
	if cfg.WSURL == "" {
		cfg.WSURL = DefaultWS
	}
	if cfg.AccessID == "" {
		cfg.AccessID = os.Getenv("BETMGM_ACCESS_ID")
	}
	if cfg.AccessID == "" {
		cfg.AccessID = DefaultAccessID
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUA
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 25 * time.Second
	}
	if cfg.ViewGap == 0 {
		cfg.ViewGap = 2 * time.Second
	}
	if cfg.RetryWait == 0 {
		cfg.RetryWait = 20 * time.Second // a 403 under load passed on a retry 20 s later
	}
	return &bookImpl{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout}, views: map[string]cachedView{}}, nil
}

func (b *bookImpl) Descriptor() book.Descriptor {
	return book.Descriptor{Name: Name, DisplayName: "BetMGM", PriceKind: book.PriceAmerican}
}

// Tuning: the league listings are two cheap reads and serve as the poll; a
// full snapshot reads every pregame NFL fixture at one per second.
func (b *bookImpl) Tuning() book.Tuning {
	return book.Tuning{
		ReconcileEvery:  30 * time.Second,
		ResnapshotEvery: 5 * time.Minute,
		MinSnapshotGap:  2 * time.Minute,
		MinPollGap:      15 * time.Second,
	}
}

// Snapshot lists every league's fixtures and, for prop leagues, reads each
// pregame fixture's full market list. The listings must succeed: a missing
// fixture would look like its game ended. A refused fixture read only costs
// that game's props: the last good read is reused for viewKeep, otherwise
// the game keeps its listed game lines and its props wait for the next
// snapshot. Cloudflare starts refusing after a burst of reads, so after two
// refusals in a row the rest of this snapshot does the same without asking.
func (b *bookImpl) Snapshot(ctx context.Context) (*book.Snapshot, error) {
	asOf := time.Now()
	var all []leagueFixtures
	stale, missing := 0, 0
	for _, l := range b.cfg.Leagues {
		fxs, err := b.listFixtures(ctx, l)
		if err != nil {
			return nil, err
		}
		if l.Props {
			refused := 0
			for i := range fxs {
				if !strings.EqualFold(fxs[i].Stage, "PreMatch") || len(fxs[i].Participants) == 0 {
					continue
				}
				id := fxs[i].ID.String()
				var full *mgmFixture
				if refused < 2 {
					full, err = b.fixtureView(ctx, id)
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
				}
				b.mu.Lock()
				if full != nil {
					refused = 0
					b.views[id] = cachedView{at: time.Now(), fx: *full}
				} else {
					refused++
					if c, ok := b.views[id]; ok && time.Since(c.at) < viewKeep {
						full = &c.fx
						stale++
					} else {
						missing++
					}
				}
				b.mu.Unlock()
				if full != nil {
					fxs[i].OptionMarkets = full.OptionMarkets
					fxs[i].Participants = full.Participants
				}
			}
		}
		all = append(all, leagueFixtures{league: l, fixtures: fxs})
	}
	snap, err := buildSnapshot(asOf, all...)
	if err != nil {
		return nil, err
	}
	if stale+missing > 0 {
		snap.Skipped["props_cached"] += stale
		snap.Skipped["props_unavailable"] += missing
		slog.Warn("betmgm: fixture reads refused", "reusedLastRead", stale, "gameLinesOnly", missing)
	}
	return snap, nil
}

// Poll re-reads the league listings: game lines only. Props ride the
// stream and the 5-minute resnapshot.
func (b *bookImpl) Poll(ctx context.Context, seed *book.Snapshot) ([]model.Update, error) {
	sd, ok := seed.Private.(*state)
	if !ok || sd == nil {
		return nil, fmt.Errorf("betmgm: seed missing")
	}
	asOf := time.Now()
	var all []leagueFixtures
	for _, l := range b.cfg.Leagues {
		fxs, err := b.listFixtures(ctx, l)
		if err != nil {
			return nil, err
		}
		all = append(all, leagueFixtures{league: l, fixtures: fxs})
	}
	snap, err := buildSnapshot(asOf, all...)
	if err != nil {
		return nil, err
	}
	recv := time.Now()
	var out []model.Update
	for _, q := range snap.Quotes {
		// Only fixtures the stream seed knows; a new one waits for a snapshot.
		if sd.fixtures[q.BookGameID] == nil || q.Family == model.FamilyPlayer {
			continue
		}
		out = append(out, model.UpdateFromQuote(q, model.SourcePoll, asOf, recv))
	}
	return out, nil
}

type leagueFixtures struct {
	league   League
	fixtures []mgmFixture
}

func (b *bookImpl) query(extra url.Values) string {
	q := url.Values{
		"x-bwin-accessid": {b.cfg.AccessID},
		"lang":            {"en-us"},
		"country":         {"CA"},
		"userCountry":     {"CA"},
		"subdivision":     {"CA-Ontario"},
	}
	for k, v := range extra {
		q[k] = v
	}
	return q.Encode()
}

// listFixtures pages through one competition's fixtures. All pages or none.
func (b *bookImpl) listFixtures(ctx context.Context, l League) ([]mgmFixture, error) {
	var out []mgmFixture
	seen := map[string]bool{}
	for skip := 0; ; skip += pageSize {
		u := b.cfg.Base + "/cds-api/bettingoffer/fixtures?" + b.query(url.Values{
			"fixtureTypes":      {"Standard"},
			"state":             {"Latest"},
			"offerMapping":      {"Filtered"},
			"offerCategories":   {"Gridable"},
			"fixtureCategories": {"Gridable,NonGridable,Other"},
			"sportIds":          {l.SportID},
			"competitionIds":    {l.CompetitionID},
			"isPriceBoost":      {"false"},
			"statisticsModes":   {"None"},
			"skip":              {strconv.Itoa(skip)},
			"take":              {strconv.Itoa(pageSize)},
			"sortBy":            {"Tags"},
		})
		var page struct {
			Fixtures   []mgmFixture `json:"fixtures"`
			TotalCount int          `json:"totalCount"`
		}
		if err := b.getJSON(ctx, u, &page); err != nil {
			return nil, fmt.Errorf("betmgm: %s fixtures: %w", l.Sport, err)
		}
		for _, f := range page.Fixtures {
			if !seen[f.ID.String()] {
				seen[f.ID.String()] = true
				out = append(out, f)
			}
		}
		if len(page.Fixtures) < pageSize || skip+pageSize >= page.TotalCount {
			return out, nil
		}
	}
}

// fixtureView reads one fixture with every market group.
func (b *bookImpl) fixtureView(ctx context.Context, id string) (*mgmFixture, error) {
	u := b.cfg.Base + "/cds-api/bettingoffer/fixture-view?" + b.query(url.Values{
		"offerMapping":                 {"Filtered"},
		"scoreboardMode":               {"None"},
		"fixtureIds":                   {id},
		"state":                        {"Latest"},
		"includePrecreatedBetBuilder":  {"false"},
		"supportVirtual":               {"false"},
		"useRegionalisedConfiguration": {"true"},
		"includeRelatedFixtures":       {"false"},
		"statisticsModes":              {"None"},
		"firstMarketGroupOnly":         {"false"},
	})
	var doc struct {
		Fixture *mgmFixture `json:"fixture"`
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 || b.cfg.ViewGap > 0 {
			wait := b.cfg.ViewGap
			if attempt > 0 {
				wait = b.cfg.RetryWait
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err = b.getJSON(ctx, u, &doc); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("betmgm: fixture %s: %w", id, err)
	}
	if doc.Fixture == nil {
		return nil, fmt.Errorf("betmgm: fixture %s: empty view", id)
	}
	return doc.Fixture, nil
}

func (b *bookImpl) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", b.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := b.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if len(raw) > 0 && raw[0] == '<' {
		return fmt.Errorf("betmgm: HTML block page instead of JSON")
	}
	return json.Unmarshal(raw, v)
}

// HTTPError carries the status and Retry-After so the supervisor can back off.
type HTTPError struct {
	Status     int
	RetryAfter string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("betmgm: HTTP %d", e.Status) }

// RetryAfterDuration parses Retry-After seconds; zero when absent.
func (e *HTTPError) RetryAfterDuration() time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(e.RetryAfter)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// ParseFixtures builds a snapshot from REST bodies (tests and replay). Each
// body is a fixtures listing ({"fixtures":[...]}) or a fixture-view
// ({"fixture":{...}}); a view replaces the listed fixture with the same id.
func ParseFixtures(asOf time.Time, l League, raws ...[]byte) (*book.Snapshot, error) {
	lf := leagueFixtures{league: l}
	idx := map[string]int{}
	for _, raw := range raws {
		var doc struct {
			Fixtures []mgmFixture `json:"fixtures"`
			Fixture  *mgmFixture  `json:"fixture"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("betmgm: decode: %w", err)
		}
		fxs := doc.Fixtures
		if doc.Fixture != nil {
			fxs = append(fxs, *doc.Fixture)
		}
		for _, f := range fxs {
			if i, ok := idx[f.ID.String()]; ok {
				lf.fixtures[i] = f
				continue
			}
			idx[f.ID.String()] = len(lf.fixtures)
			lf.fixtures = append(lf.fixtures, f)
		}
	}
	return buildSnapshot(asOf, lf)
}

func buildSnapshot(asOf time.Time, leagues ...leagueFixtures) (*book.Snapshot, error) {
	sd := newState()
	skipped := map[string]int{}
	var games []model.BookGame
	var quotes []model.Quote
	for _, lf := range leagues {
		for _, f := range lf.fixtures {
			fx, ok := newFixture(lf.league.Sport, f)
			if !ok {
				skipped["unmapped_fixture"]++
				continue
			}
			for _, m := range f.OptionMarkets {
				if !fx.put(classify(fx, m)) {
					skipped["unmapped_market"]++
				}
			}
			sd.fixtures[fx.id] = fx
			sd.topics = append(sd.topics, f.Context+"|all")
			games = append(games, model.BookGame{
				Book: Name, BookGameID: fx.id, Sport: fx.sport, League: fx.sport, Name: fx.name,
				Start: fx.start, Away: fx.away, Home: fx.home, Live: fx.live, Status: f.Stage,
			})
			quotes = append(quotes, fx.quotes()...)
		}
	}
	sort.Strings(sd.topics)
	return &book.Snapshot{
		Games: games, Quotes: quotes, AsOf: asOf, Skipped: skipped,
		Subscription: strings.Join(sd.topics, ","), Private: sd,
	}, nil
}
