package fanduel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

// DefaultMGAURL is the facet search the event page uses for its full market
// list. The league boards carry main lines only, so player props come from
// here, one POST per event. It returns structure without prices; SMP prices
// them like every other market.
const DefaultMGAURL = "https://scan.on.sportsbook.fanduel.ca/api/sports/navigation/facet/v1.0/search"

type propClass struct {
	stat, comparator string
	threshold        float64 // TD scorer markets: the N in "N+ touchdowns"
	ladder           bool    // one runner per rung, threshold in the runner name
}

// propStats maps the stat part of PLAYER_<slot>_[ALT_]<STAT>[_<tier>].
var propStats = map[string]string{
	"PASSING_YARDS":             model.StatPassYds,
	"PASSING_TOUCHDOWNS":        model.StatPassTD,
	"PASS_ATTEMPTS":             model.StatPassAtt,
	"PASS_COMPLETIONS":          model.StatPassCmp,
	"PASSING_+_RUSHING_YARDS":   model.StatPassRushYds,
	"RUSHING_YARDS":             model.StatRushYds,
	"RUSH_ATTEMPTS":             model.StatRushAtt,
	"RUSHING_+_RECEIVING_YARDS": model.StatRushRecYds,
	"RECEIVING_YARDS":           model.StatRecYds,
	"RECEPTIONS":                model.StatRec,
	"INTERCEPTIONS":             model.StatInt,
	"PASSING_INTERCEPTIONS":     model.StatInt,
	"INTERCEPTIONS_THROWN":      model.StatInt,
}

var tdMarkets = map[string]float64{
	"ANY_TIME_TOUCHDOWN_SCORER": 1,
	"TO_SCORE_2+_TOUCHDOWNS":    2,
	"TO_SCORE_3+_TOUCHDOWNS":    3,
}

// The letters after PLAYER_ are a per-game slot and _HIGH/_MEDIUM/_LOW a
// player tier; neither names the stat or the player, so both are stripped.
var (
	rePropType = regexp.MustCompile(`^PLAYER_[A-Z]+_(ALT_)?(.+?)(_HIGH|_MEDIUM|_LOW)?$`)
	reRung     = regexp.MustCompile(` (\d+)\+(?: |$)`)
)

func classifyProp(marketType string) (propClass, bool) {
	if n, ok := tdMarkets[marketType]; ok {
		return propClass{stat: model.StatTD, comparator: model.ComparatorAtLeast, threshold: n}, true
	}
	m := rePropType.FindStringSubmatch(marketType)
	if m == nil {
		return propClass{}, false
	}
	stat, ok := propStats[m[2]] // quarter, half and "longest" markets have no entry
	if !ok {
		return propClass{}, false
	}
	if m[1] != "" {
		return propClass{stat: stat, comparator: model.ComparatorAtLeast, ladder: true}, true
	}
	return propClass{stat: stat, comparator: model.ComparatorOU}, true
}

// propRunner places one runner: player, outcome and line.
func propRunner(c propClass, m fdMarket, r fdRunner) (player, outcome string, line float64, ok bool) {
	switch {
	case c.threshold > 0:
		name := strings.TrimSpace(r.RunnerName)
		if name == "" || strings.Contains(name, "D/ST") || strings.HasSuffix(name, " Defense") {
			return "", "", 0, false
		}
		return name, model.MilestoneOutcome(c.threshold), c.threshold, true
	case c.ladder:
		rm := reRung.FindStringSubmatch(r.RunnerName)
		if rm == nil {
			return "", "", 0, false
		}
		n, _ := strconv.ParseFloat(rm[1], 64)
		player = strings.TrimSpace(strings.SplitN(m.MarketName, " - ", 2)[0])
		return player, model.MilestoneOutcome(n), n, player != ""
	default:
		player = strings.TrimSpace(strings.SplitN(m.MarketName, " - ", 2)[0])
		side := ""
		if r.Result != nil {
			side = strings.ToLower(strings.TrimSpace(r.Result.Type))
		}
		if player == "" || (side != "over" && side != "under") || r.Handicap == nil {
			return "", "", 0, false
		}
		return player, side, *r.Handicap, true
	}
}

type fdMGA struct {
	Attachments struct {
		SportsBookMarkets map[string]fdMarket `json:"sportsBookMarkets"`
	} `json:"attachments"`
}

// AddProps adds one event's player props from an MGA response to a
// snapshot and its seed. Markets the boards already carry are skipped.
// Prices stay unset until SMP fills them; a runner SMP never prices is
// dropped by freshPrices' caller.
func AddProps(snap *book.Snapshot, g model.BookGame, raw []byte) error {
	var doc fdMGA
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("fanduel: decode MGA %s: %w", g.BookGameID, err)
	}
	sd := snap.Private.(*seed)
	if snap.Skipped == nil {
		snap.Skipped = map[string]int{}
	}
	ids := make([]string, 0, len(doc.Attachments.SportsBookMarkets))
	for _, m := range doc.Attachments.SportsBookMarkets {
		ids = append(ids, m.MarketID)
	}
	sort.Strings(ids)
	byID := make(map[string]fdMarket, len(ids))
	for _, m := range doc.Attachments.SportsBookMarkets {
		byID[m.MarketID] = m
	}
	for _, id := range ids {
		m := byID[id]
		if sd.markets[id] != nil || m.EventID.String() != g.BookGameID {
			continue
		}
		c, ok := classifyProp(m.MarketType)
		if !ok {
			continue
		}
		ms := &mktSeed{family: model.FamilyPlayer, gameRef: g.BookGameID, fixedLine: c.comparator == model.ComparatorAtLeast, runners: map[string]*runnerSeed{}}
		for _, r := range m.Runners {
			player, outcome, line, ok := propRunner(c, m, r)
			if !ok {
				snap.Skipped["unmapped_prop_runner"]++
				if ms.skip == nil {
					ms.skip = map[string]bool{}
				}
				ms.skip[r.SelectionID.String()] = true
				continue
			}
			q := model.Quote{
				Book: Name, BookGameID: g.BookGameID, BookMarketID: m.MarketID,
				BookSelectionKey: m.MarketID + ":" + r.SelectionID.String(),
				Sport:            g.Sport, League: g.League, MarketName: m.MarketName, SelectionName: r.RunnerName,
				Price: model.Price{Status: fdStatus(m.MarketStatus, r.RunnerStatus), InPlay: m.IsInPlay()},
			}
			q = model.PropQuote(q, player, c.stat, c.comparator, outcome, line)
			snap.Quotes = append(snap.Quotes, q)
			ms.runners[r.SelectionID.String()] = &runnerSeed{identity: model.IdentityKey(q), line: model.CopyLine(q.Line), status: q.Price.Status}
		}
		if len(ms.runners) > 0 {
			sd.markets[id] = ms
			sd.ids = append(sd.ids, id)
		}
	}
	sort.Strings(sd.ids)
	snap.Subscription = strings.Join(sd.ids, ",")
	return nil
}

// dropUnpriced removes quotes SMP never priced (American 0): a prop whose
// market SMP does not return is not on offer.
func dropUnpriced(snap *book.Snapshot) {
	out := snap.Quotes[:0]
	for _, q := range snap.Quotes {
		if q.Price.American != 0 {
			out = append(out, q)
		} else {
			snap.Skipped["unpriced_prop"]++
		}
	}
	snap.Quotes = out
}

// mga fetches one event's full market list.
func (b *bookImpl) mga(ctx context.Context, eventID string) ([]byte, error) {
	var ids any = []string{eventID}
	if n, err := strconv.ParseInt(eventID, 10, 64); err == nil {
		ids = []int64{n}
	}
	body, _ := json.Marshal(map[string]any{
		"filter": map[string]any{
			"eventIds":     ids,
			"contentGroup": map[string]any{"language": "en", "regionCode": "NAMERICA"},
			"maxResults":   0,
			"selectBy":     "RANK",
		},
		"facets":       []map[string]any{{"type": "EVENT"}, {"type": "MARKET"}},
		"currencyCode": "USD",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.MGAURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", b.cfg.UserAgent)
	req.Header.Set("X-Application", b.ak())
	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}
