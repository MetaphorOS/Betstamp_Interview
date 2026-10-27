package betmgm

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"betstampodds/internal/model"
	"betstampodds/internal/taxonomy"
	"betstampodds/internal/teams"
)

// ID tolerates quoted and numeric ids. V2 fixture ids ("6:48482") are
// strings, market and option ids are numbers.
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

type mgmName struct {
	Value string `json:"value"`
}

type mgmParticipant struct {
	ID            ID      `json:"id"`            // V2: the fixture participant id options point at
	ParticipantID ID      `json:"participantId"` // V1: the id options point at
	Name          mgmName `json:"name"`
	Properties    *struct {
		Type string `json:"type"` // HomeTeam | AwayTeam | Player (V2); V1 teams have none
	} `json:"properties"`
}

func (p mgmParticipant) kind() string {
	if p.Properties == nil {
		return ""
	}
	return p.Properties.Type
}

type mgmOption struct {
	ID            ID      `json:"id"`
	Status        string  `json:"status"`
	Name          mgmName `json:"name"`
	Attr          string  `json:"attr"`         // V1 spread: "+12.5"
	TotalsPrefix  string  `json:"totalsPrefix"` // V1 totals: Over | Under
	ParticipantID ID      `json:"participantId"`
	Price         struct {
		Odds         float64 `json:"odds"`
		AmericanOdds int     `json:"americanOdds"`
	} `json:"price"`
	// V2 option parameters are an object; market parameters are an array.
	Parameters struct {
		OptionTypes        []string `json:"optionTypes"`
		FixtureParticipant ID       `json:"fixtureParticipant"`
	} `json:"parameters"`
}

type mgmParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type mgmMarket struct {
	ID                   ID          `json:"id"`
	Name                 mgmName     `json:"name"`
	Status               string      `json:"status"`
	Attr                 string      `json:"attr"`
	Spread               *float64    `json:"spread"` // the book's price imbalance: ~0 on the main line
	FixtureParticipantID ID          `json:"fixtureParticipantId"`
	Parameters           []mgmParam  `json:"parameters"`
	Options              []mgmOption `json:"options"`
}

func (m mgmMarket) param(key string) string {
	for _, p := range m.Parameters {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

type mgmFixture struct {
	ID               ID               `json:"id"`
	Name             mgmName          `json:"name"`
	Source           string           `json:"source"`
	Context          string           `json:"context"` // push topic prefix: v2|en-us|6:48482_101_any
	Stage            string           `json:"stage"`   // PreMatch | Live
	StartDate        string           `json:"startDate"`
	IsOpenForBetting *bool            `json:"isOpenForBetting"`
	Participants     []mgmParticipant `json:"participants"`
	OptionMarkets    []mgmMarket      `json:"optionMarkets"`
}

// market is one parsed book market. It is never mutated after classify:
// an update replaces it, so clones may share it.
type market struct {
	id      string
	group   string  // competing markets for the same quotes (alternate lines)
	balance float64 // lower is the book's main line
	open    bool
	quotes  []model.Quote
}

// fixture is one game: its teams, its players and its markets grouped so
// the book's alternate spreads and totals collapse onto the main one.
type fixture struct {
	id, sport, name string
	start           time.Time
	home, away      model.Team
	live, closed    bool
	teams           map[string]string // participant id -> canonical team id
	players         map[string]string // fixture participant id -> player name
	groups          map[string]map[string]*market
	chosen          map[string]*market // group -> market on the board
	marketGroup     map[string]string  // market id -> group
}

func newFixture(sport string, f mgmFixture) (*fixture, bool) {
	start, err := time.Parse(time.RFC3339, f.StartDate)
	if err != nil {
		return nil, false
	}
	fx := &fixture{
		id: f.ID.String(), sport: sport, name: f.Name.Value, start: start.UTC(),
		live:  strings.EqualFold(f.Stage, "Live"),
		teams: map[string]string{}, players: map[string]string{},
		groups: map[string]map[string]*market{}, chosen: map[string]*market{}, marketGroup: map[string]string{},
	}
	if f.IsOpenForBetting != nil {
		fx.closed = !*f.IsOpenForBetting
	}
	// V1 names read "Away at Home", V2 "Away @ Home".
	var awayName, homeName string
	for _, sep := range []string{" @ ", " at "} {
		if parts := strings.SplitN(fx.name, sep, 2); len(parts) == 2 {
			awayName, homeName = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			break
		}
	}
	for _, p := range f.Participants {
		switch p.kind() {
		case "Player":
			fx.players[p.ID.String()] = p.Name.Value
			continue
		case "HomeTeam":
			homeName = p.Name.Value
		case "AwayTeam":
			awayName = p.Name.Value
		}
		if t, ok := teams.Resolve(sport, p.Name.Value); ok {
			for _, id := range []ID{p.ID, p.ParticipantID} {
				if id != "" {
					fx.teams[id.String()] = t.ID
				}
			}
		}
	}
	h, okH := teams.Resolve(sport, homeName)
	a, okA := teams.Resolve(sport, awayName)
	if !okH || !okA || h.ID == a.ID {
		return nil, false
	}
	fx.home, fx.away = h, a
	return fx, true
}

func (fx *fixture) clone() *fixture {
	c := *fx
	c.teams, c.players = fx.teams, fx.players // read-only after newFixture
	c.groups = make(map[string]map[string]*market, len(fx.groups))
	for g, ms := range fx.groups {
		cm := make(map[string]*market, len(ms))
		for id, m := range ms {
			cm[id] = m
		}
		c.groups[g] = cm
	}
	c.chosen = make(map[string]*market, len(fx.chosen))
	for g, m := range fx.chosen {
		c.chosen[g] = m
	}
	c.marketGroup = make(map[string]string, len(fx.marketGroup))
	for id, g := range fx.marketGroup {
		c.marketGroup[id] = g
	}
	return &c
}

// Prop stats by BetMGM's Happening parameter.
var propStats = map[string]string{
	"PassingYards":          model.StatPassYds,
	"TouchdownPass":         model.StatPassTD,
	"PassAttempt":           model.StatPassAtt,
	"PassCompleted":         model.StatPassCmp,
	"PassingRushingYards":   model.StatPassRushYds,
	"RushingYards":          model.StatRushYds,
	"RushingAttempt":        model.StatRushAtt,
	"RushingReceivingYards": model.StatRushRecYds,
	"ReceivingYards":        model.StatRecYds,
	"Reception":             model.StatRec,
	"InterceptionThrown":    model.StatInt,
	"Touchdown":             model.StatTD,
}

// classify parses one market into its board quotes, or nil when it is not
// a full-game main line or player prop.
func classify(fx *fixture, m mgmMarket) *market {
	if period := m.param("Period"); period != "" && period != "FullTime" {
		return nil
	}
	out := &market{id: m.ID.String(), open: strings.EqualFold(m.Status, "Visible"), balance: math.Inf(1)}
	if m.Spread != nil {
		out.balance = *m.Spread
	}
	base := func(o mgmOption) model.Quote {
		status := model.StatusOpen
		if !out.open || !strings.EqualFold(o.Status, "Visible") {
			status = model.StatusSuspended
		}
		return model.Quote{
			Book: Name, BookGameID: fx.id, BookMarketID: out.id, BookSelectionKey: out.id + ":" + o.ID.String(),
			Sport: fx.sport, League: fx.sport, Period: model.PeriodFull, Subject: "event",
			MarketName: m.Name.Value, SelectionName: o.Name.Value,
			Price: model.Price{American: o.Price.AmericanOdds, Decimal: o.Price.Odds, Status: status, InPlay: fx.live},
		}
	}
	if m.FixtureParticipantID != "" {
		return classifyProp(fx, m, out, base)
	}
	if len(m.Options) != 2 {
		return nil
	}
	switch m.Name.Value {
	case "Moneyline":
		out.group = model.FamilyMoneyline
		for _, o := range m.Options {
			team := fx.optionTeam(o)
			if team == "" {
				return nil
			}
			q := base(o)
			q.Family, q.Outcome = model.FamilyMoneyline, team
			out.quotes = append(out.quotes, q)
		}
	case "Spread":
		out.group = model.FamilySpread
		for _, o := range m.Options {
			team := fx.optionTeam(o)
			line, ok := taxonomy.SignedLine(o.Attr)
			if !ok {
				line, ok = trailingLine(o.Name.Value)
			}
			if team == "" || !ok {
				return nil
			}
			q := base(o)
			q.Family, q.Outcome, q.Line = model.FamilySpread, team, line
			out.quotes = append(out.quotes, q)
		}
	case "Totals":
		out.group = model.FamilyTotal
		line, err := strconv.ParseFloat(firstNonEmpty(m.Attr, m.param("DecimalValue")), 64)
		if err != nil {
			return nil
		}
		for _, o := range m.Options {
			side := overUnder(o)
			if side == "" {
				return nil
			}
			q := base(o)
			q.Family, q.Stat, q.Comparator, q.Outcome, q.Line = model.FamilyTotal, "points", model.ComparatorOU, side, &line
			out.quotes = append(out.quotes, q)
		}
	default:
		return nil
	}
	return out
}

// classifyProp handles a market tied to a player. Team props carry a team
// participant and are skipped. Two options are an over/under line; one
// option is a milestone whose threshold is ceil(DecimalValue): "3+ TDs" is
// encoded 2.5 while "50+ receiving yards" is encoded 50.
func classifyProp(fx *fixture, m mgmMarket, out *market, base func(mgmOption) model.Quote) *market {
	player := fx.players[m.FixtureParticipantID.String()]
	stat := propStats[m.param("Happening")]
	if player == "" || stat == "" || fx.sport != "nfl" {
		return nil
	}
	dv, err := strconv.ParseFloat(m.param("DecimalValue"), 64)
	if err != nil {
		return nil
	}
	switch len(m.Options) {
	case 2:
		for _, o := range m.Options {
			side := overUnder(o)
			if side == "" {
				return nil
			}
			out.quotes = append(out.quotes, model.PropQuote(base(o), player, stat, model.ComparatorOU, side, dv))
		}
		out.group = "prop|" + model.PlayerSubject(player) + "|" + stat + "|ou"
	case 1:
		if overUnder(m.Options[0]) != "over" {
			return nil
		}
		n := math.Ceil(dv)
		q := model.PropQuote(base(m.Options[0]), player, stat, model.ComparatorAtLeast, model.MilestoneOutcome(n), n)
		out.quotes = append(out.quotes, q)
		out.group = model.IdentityKey(q)
	default:
		return nil
	}
	return out
}

func overUnder(o mgmOption) string {
	for _, s := range append([]string{o.TotalsPrefix}, o.Parameters.OptionTypes...) {
		switch strings.ToLower(s) {
		case "over":
			return "over"
		case "under":
			return "under"
		}
	}
	low := strings.ToLower(o.Name.Value)
	switch {
	case strings.HasPrefix(low, "over"):
		return "over"
	case strings.HasPrefix(low, "under"):
		return "under"
	}
	return ""
}

// optionTeam maps an option to a team: V2 through the fixture participant
// parameter, V1 through participantId.
func (fx *fixture) optionTeam(o mgmOption) string {
	for _, id := range []ID{o.Parameters.FixtureParticipant, o.ParticipantID} {
		if t := fx.teams[id.String()]; id != "" && t != "" {
			return t
		}
	}
	return ""
}

// trailingLine parses "Carolina Panthers +3.5".
func trailingLine(name string) (*float64, bool) {
	i := strings.LastIndex(name, " ")
	if i < 0 {
		return nil, false
	}
	return taxonomy.SignedLine(name[i+1:])
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// put adds or replaces a market and re-picks its group's main market. It
// reports whether the market was mapped.
func (fx *fixture) put(m *market) bool {
	if m == nil {
		return false
	}
	if old := fx.marketGroup[m.id]; old != "" && old != m.group {
		delete(fx.groups[old], m.id)
	}
	if fx.groups[m.group] == nil {
		fx.groups[m.group] = map[string]*market{}
	}
	fx.groups[m.group][m.id] = m
	fx.marketGroup[m.id] = m.group
	fx.chosen[m.group] = pick(fx.groups[m.group])
	return true
}

// remove drops a market; it reports the market's group, or "".
func (fx *fixture) remove(id string) string {
	g := fx.marketGroup[id]
	if g == "" {
		return ""
	}
	delete(fx.marketGroup, id)
	delete(fx.groups[g], id)
	if len(fx.groups[g]) == 0 {
		delete(fx.groups, g)
		delete(fx.chosen, g)
	} else {
		fx.chosen[g] = pick(fx.groups[g])
	}
	return g
}

// pick is the main line: an open market before a suspended one, then the
// book's smallest price imbalance (0.03 on a -110/-108 line), then id.
func pick(ms map[string]*market) *market {
	var best *market
	for _, m := range ms {
		if best == nil || better(m, best) {
			best = m
		}
	}
	return best
}

func better(a, b *market) bool {
	if a.open != b.open {
		return a.open
	}
	if a.balance != b.balance {
		return a.balance < b.balance
	}
	return a.id < b.id
}

// quotes lists the board quotes of every group's main market, in a stable
// order.
func (fx *fixture) quotes() []model.Quote {
	groups := make([]string, 0, len(fx.chosen))
	for g := range fx.chosen {
		groups = append(groups, g)
	}
	slices.Sort(groups)
	var out []model.Quote
	for _, g := range groups {
		out = append(out, fx.groupQuotes(g)...)
	}
	return out
}

// groupQuotes applies fixture state (live, closed) to a group's main quotes.
func (fx *fixture) groupQuotes(g string) []model.Quote {
	m := fx.chosen[g]
	if m == nil {
		return nil
	}
	out := make([]model.Quote, 0, len(m.quotes))
	for _, q := range m.quotes {
		if q.Price.American == 0 {
			continue
		}
		q.Price.InPlay = fx.live
		if fx.closed {
			q.Price.Status = model.StatusSuspended
		}
		q.Line = model.CopyLine(q.Line)
		out = append(out, q)
	}
	return out
}
