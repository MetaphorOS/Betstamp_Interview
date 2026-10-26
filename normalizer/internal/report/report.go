// Package report measures normalization quality: event matching, quote-key
// coverage, price comparability and taxonomy coverage.
package report

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sportsbook-normalizer/internal/identity"
	"sportsbook-normalizer/internal/model"
)

type EventDelta struct {
	Count    int   `json:"count"`
	MinMs    int64 `json:"minMs"`
	MedianMs int64 `json:"medianMs"`
	MaxMs    int64 `json:"maxMs"`
}

type PriceStats struct {
	Compared          int      `json:"compared"`
	Exact             int      `json:"exact"`
	Different         int      `json:"different"`
	MedianAbsAmerican float64  `json:"medianAbsAmerican,omitempty"`
	Samples           []string `json:"samples,omitempty"`
}

type FamilyStats struct {
	QuotesA    int `json:"quotesA"`
	QuotesB    int `json:"quotesB"`
	Matched    int `json:"matched"`
	PriceExact int `json:"priceExact"`
	PriceDiff  int `json:"priceDiff"`
}

type Pair struct {
	Sport               string                 `json:"sport"`
	DKFile              string                 `json:"dkFile"`
	FDFile              string                 `json:"fdFile"`
	EventsA             int                    `json:"eventsA"`
	EventsB             int                    `json:"eventsB"`
	EventMatches        int                    `json:"eventMatches"`
	EventMatchRate      float64                `json:"eventMatchRate"`
	EventOverlapUnion   float64                `json:"eventOverlapUnion"`
	UnmatchedA          []string               `json:"unmatchedA,omitempty"`
	UnmatchedB          []string               `json:"unmatchedB,omitempty"`
	EventDelta          EventDelta             `json:"eventDelta"`
	QuotesA             int                    `json:"quotesA"`
	QuotesB             int                    `json:"quotesB"`
	QuoteMatches        int                    `json:"quoteMatches"`
	QuoteMatchUnion     float64                `json:"quoteMatchRateUnion"`
	QuoteCoverageA      float64                `json:"quoteCoverageA"`
	QuoteCoverageB      float64                `json:"quoteCoverageB"`
	MatchedQuotesA      int                    `json:"matchedEventQuotesA"`
	MatchedQuotesB      int                    `json:"matchedEventQuotesB"`
	MatchedIdentity     int                    `json:"matchedEventIdentity"`
	MatchedExact        int                    `json:"matchedEventExactLine"`
	MatchedIdentityPct  float64                `json:"matchedEventIdentityPct"`
	MatchedLinePct      float64                `json:"matchedEventLinePct"`
	IdentityMatches     int                    `json:"identityMatches"`
	IdentityUnion       float64                `json:"identityCoverageUnion"`
	LineMismatches      int                    `json:"lineMismatches"`
	LineMismatchSamples []string               `json:"lineMismatchSamples,omitempty"`
	UnmatchedQuotes     []string               `json:"unmatchedQuotes,omitempty"`
	Price               PriceStats             `json:"price"`
	Families            map[string]FamilyStats `json:"families"`
}

type Taxonomy struct {
	Book               string         `json:"book"`
	Sport              string         `json:"sport"`
	File               string         `json:"file"`
	Events             int            `json:"events"`
	GameEvents         int            `json:"gameEvents"`
	Markets            int            `json:"markets"`
	Quotes             int            `json:"quotes"`
	ComparableQuotes   int            `json:"comparableQuotes"`
	ClassifiedMarkets  int            `json:"classifiedMarkets"`
	HighConfidence     int            `json:"highConfidence"`
	UnknownMarkets     int            `json:"unknownMarkets"`
	ClassifiedPct      float64        `json:"classifiedPct"`
	HighConfidencePct  float64        `json:"highConfidencePct"`
	FamilyCounts       map[string]int `json:"familyCounts"`
	UnknownTypes       []UnknownType  `json:"unknownTypes,omitempty"`
	DanglingSelections int            `json:"danglingSelections,omitempty"`
}

type UnknownType struct {
	Type    string `json:"type"`
	Count   int    `json:"count"`
	Example string `json:"example,omitempty"`
}

type Document struct {
	GeneratedAt string     `json:"generatedAt"`
	Pairs       []Pair     `json:"pairs"`
	Taxonomies  []Taxonomy `json:"taxonomies"`
	Notes       []string   `json:"notes,omitempty"`
}

// BuildPair measures two normalized books for the same sport.
func BuildPair(sport, dkFile, fdFile string, a, b *model.BookData) Pair {
	p := Pair{Sport: sport, DKFile: dkFile, FDFile: fdFile, Families: map[string]FamilyStats{}}
	p.EventsA, p.EventsB = len(a.Events), len(b.Events)
	pairs, ua, ub := identity.MatchEvents(a.Events, b.Events, 15*time.Minute)
	p.EventMatches = len(pairs)
	if max := maxInt(p.EventsA, p.EventsB); max > 0 {
		p.EventMatchRate = float64(len(pairs)) / float64(max) * 100
	}
	if union := p.EventsA + p.EventsB - len(pairs); union > 0 {
		p.EventOverlapUnion = float64(len(pairs)) / float64(union) * 100
	}
	p.UnmatchedA = eventNames(ua, 10)
	p.UnmatchedB = eventNames(ub, 10)
	p.EventDelta = eventDeltas(pairs)

	qa := comparableMap(a.Quotes)
	qb := comparableMap(b.Quotes)
	p.QuotesA, p.QuotesB = len(qa), len(qb)
	for _, q := range qa {
		s := p.Families[q.Family]
		s.QuotesA++
		p.Families[q.Family] = s
	}
	for _, q := range qb {
		s := p.Families[q.Family]
		s.QuotesB++
		p.Families[q.Family] = s
	}
	matched := 0
	union := map[string]bool{}
	for k := range qa {
		union[k] = true
	}
	for k := range qb {
		union[k] = true
	}
	var samples []string
	for k, va := range qa {
		vb, ok := qb[k]
		if !ok {
			if len(p.UnmatchedQuotes) < 10 {
				p.UnmatchedQuotes = append(p.UnmatchedQuotes, k)
			}
			continue
		}
		matched++
		stats := p.Families[va.Family]
		stats.Matched++
		if priceEqual(va, vb) {
			p.Price.Exact++
			stats.PriceExact++
		} else {
			p.Price.Different++
			stats.PriceDiff++
			if len(samples) < 10 {
				samples = append(samples, fmt.Sprintf("%s  dk=%s fd=%s", k, priceString(va), priceString(vb)))
			}
		}
		p.Families[va.Family] = stats
	}
	p.QuoteMatches = matched
	if len(union) > 0 {
		p.QuoteMatchUnion = float64(matched) / float64(len(union)) * 100
	}
	if p.QuotesA > 0 {
		p.QuoteCoverageA = float64(matched) / float64(p.QuotesA) * 100
	}
	if p.QuotesB > 0 {
		p.QuoteCoverageB = float64(matched) / float64(p.QuotesB) * 100
	}

	// Identity coverage ignores the line: it answers "is this the same market
	// and outcome?" Line mismatches are reported separately because a 10 vs
	// 10.5 spread is not a directly comparable price.
	qaID := groupByIdentity(qa)
	qbID := groupByIdentity(qb)
	idUnion := map[string]bool{}
	for k := range qaID {
		idUnion[k] = true
	}
	for k := range qbID {
		idUnion[k] = true
	}
	for id, av := range qaID {
		bv, ok := qbID[id]
		if !ok {
			continue
		}
		p.IdentityMatches++
		aKeys := map[string]bool{}
		for _, q := range av {
			aKeys[q.Key] = true
		}
		hasExactLine := false
		for _, q := range bv {
			if aKeys[q.Key] {
				hasExactLine = true
				break
			}
		}
		if !hasExactLine {
			p.LineMismatches++
			if len(p.LineMismatchSamples) < 10 {
				p.LineMismatchSamples = append(p.LineMismatchSamples,
					fmt.Sprintf("%s  dk lines=%s fd lines=%s", id, lineList(av), lineList(bv)))
			}
		}
	}
	if len(idUnion) > 0 {
		p.IdentityUnion = float64(p.IdentityMatches) / float64(len(idUnion)) * 100
	}

	// Conditional on events present in both books: this separates normalization
	// quality from slate/board differences.
	matchedScopes := map[string]bool{}
	for _, pair := range pairs {
		matchedScopes[pair.A.Key] = true
	}
	qaMatched := filterScope(qa, matchedScopes)
	qbMatched := filterScope(qb, matchedScopes)
	p.MatchedQuotesA, p.MatchedQuotesB = len(qaMatched), len(qbMatched)
	cond := measureCoverage(qaMatched, qbMatched)
	p.MatchedIdentity = cond.identity
	p.MatchedExact = cond.exact
	if denom := maxInt(p.MatchedQuotesA, p.MatchedQuotesB); denom > 0 {
		p.MatchedIdentityPct = float64(cond.identity) / float64(denom) * 100
		p.MatchedLinePct = float64(cond.exact) / float64(denom) * 100
	}

	p.Price.Compared = p.Price.Exact + p.Price.Different
	if p.Price.Different > 0 {
		diffs := make([]float64, 0, p.Price.Different)
		for k, va := range qa {
			vb, ok := qb[k]
			if ok && !priceEqual(va, vb) {
				diffs = append(diffs, math.Abs(float64(americanOf(va)-americanOf(vb))))
			}
		}
		sort.Float64s(diffs)
		p.Price.MedianAbsAmerican = median(diffs)
	}
	p.Price.Samples = samples
	return p
}

// BuildTaxonomy measures one book/sport file without requiring a counterpart.
func BuildTaxonomy(book, sport, file string, data *model.BookData) Taxonomy {
	t := Taxonomy{
		Book: book, Sport: sport, File: file,
		Events: len(data.Events), GameEvents: data.Counts.GameEvents,
		Markets: data.Counts.Markets, Quotes: data.Counts.Quotes,
		ComparableQuotes:   data.Counts.ComparableQuotes,
		ClassifiedMarkets:  data.Counts.ClassifiedMarkets,
		HighConfidence:     data.Counts.HighConfidence,
		UnknownMarkets:     data.Counts.UnknownMarkets,
		FamilyCounts:       data.Counts.FamilyCounts,
		DanglingSelections: data.Counts.DanglingSelections,
	}
	if t.Markets > 0 {
		t.ClassifiedPct = float64(t.ClassifiedMarkets) / float64(t.Markets) * 100
		t.HighConfidencePct = float64(t.HighConfidence) / float64(t.Markets) * 100
	}
	examples := map[string]string{}
	for _, m := range data.Markets {
		if m.Family == model.FamilyUnknown {
			if _, ok := examples[m.MarketType]; !ok {
				examples[m.MarketType] = m.MarketName
			}
		}
	}
	for typ, n := range data.Counts.UnknownTypes {
		t.UnknownTypes = append(t.UnknownTypes, UnknownType{Type: typ, Count: n, Example: examples[typ]})
	}
	sort.Slice(t.UnknownTypes, func(i, j int) bool {
		if t.UnknownTypes[i].Count != t.UnknownTypes[j].Count {
			return t.UnknownTypes[i].Count > t.UnknownTypes[j].Count
		}
		return t.UnknownTypes[i].Type < t.UnknownTypes[j].Type
	})
	return t
}

func eventDeltas(pairs []identity.EventPair) EventDelta {
	d := EventDelta{Count: len(pairs)}
	if len(pairs) == 0 {
		return d
	}
	vals := make([]int64, 0, len(pairs))
	for _, p := range pairs {
		vals = append(vals, p.Delta.Milliseconds())
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	d.MinMs, d.MaxMs = vals[0], vals[len(vals)-1]
	d.MedianMs = vals[len(vals)/2]
	return d
}

func comparableMap(qs []model.Quote) map[string]model.Quote {
	m := map[string]model.Quote{}
	for _, q := range qs {
		if !q.Comparable || q.Key == "" {
			continue
		}
		if _, exists := m[q.Key]; !exists {
			m[q.Key] = q
		}
	}
	return m
}

func filterScope(m map[string]model.Quote, scopes map[string]bool) map[string]model.Quote {
	out := map[string]model.Quote{}
	for k, q := range m {
		if scopes[q.ScopeKey] {
			out[k] = q
		}
	}
	return out
}

type coverageStats struct {
	exact, identity, lineMismatch int
	exactUnion, identityUnion     float64
}

func measureCoverage(qa, qb map[string]model.Quote) coverageStats {
	var s coverageStats
	union := map[string]bool{}
	for k := range qa {
		union[k] = true
	}
	for k := range qb {
		union[k] = true
	}
	for k := range qa {
		if _, ok := qb[k]; ok {
			s.exact++
		}
	}
	if len(union) > 0 {
		s.exactUnion = float64(s.exact) / float64(len(union)) * 100
	}
	ia, ib := groupByIdentity(qa), groupByIdentity(qb)
	idUnion := map[string]bool{}
	for k := range ia {
		idUnion[k] = true
	}
	for k := range ib {
		idUnion[k] = true
	}
	for id, av := range ia {
		bv, ok := ib[id]
		if !ok {
			continue
		}
		s.identity++
		aKeys := map[string]bool{}
		for _, q := range av {
			aKeys[q.Key] = true
		}
		exactLine := false
		for _, q := range bv {
			if aKeys[q.Key] {
				exactLine = true
				break
			}
		}
		if !exactLine {
			s.lineMismatch++
		}
	}
	if len(idUnion) > 0 {
		s.identityUnion = float64(s.identity) / float64(len(idUnion)) * 100
	}
	return s
}

func groupByIdentity(m map[string]model.Quote) map[string][]model.Quote {
	out := map[string][]model.Quote{}
	for _, q := range m {
		id := model.IdentityKey(q)
		out[id] = append(out[id], q)
	}
	return out
}

func lineList(qs []model.Quote) string {
	seen := map[string]bool{}
	var out []string
	for _, q := range qs {
		s := model.LineString(q.Line)
		if s == "" {
			s = "null"
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func priceEqual(a, b model.Quote) bool {
	if a.Price.American != nil && b.Price.American != nil {
		return *a.Price.American == *b.Price.American
	}
	if a.Price.Decimal != nil && b.Price.Decimal != nil {
		return math.Abs(*a.Price.Decimal-*b.Price.Decimal) < 1e-6
	}
	return false
}

func americanOf(q model.Quote) int {
	if q.Price.American == nil {
		return 0
	}
	return *q.Price.American
}

func priceString(q model.Quote) string {
	if q.Price.American != nil {
		return fmt.Sprintf("%+d", *q.Price.American)
	}
	if q.Price.Decimal != nil {
		return fmt.Sprintf("%.4f", *q.Price.Decimal)
	}
	return "?"
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)/2]
}

func eventNames(events []model.Event, limit int) []string {
	var out []string
	for i, e := range events {
		if i >= limit {
			break
		}
		out = append(out, fmt.Sprintf("%s %s @ %s (%s)", e.Sport, e.Away.Name, e.Home.Name, e.Start.Format(time.RFC3339)))
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func marshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// WriteJSON writes the document.
func (d *Document) WriteJSON(path string) error {
	data, err := marshalIndent(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// WriteMarkdown writes a human-readable report.
func (d *Document) WriteMarkdown(path string) error {
	var b strings.Builder
	b.WriteString("# Cross-book normalization report\n\n")
	b.WriteString("Generated " + d.GeneratedAt + "\n\n")
	b.WriteString("Method: atomic quote keys `sport | event-scope | family | period | subject | stat | comparator | line | outcome`. ")
	b.WriteString("Event scope uses a sorted team pair and UTC date, so home/away or neutral-site conventions cannot break identity. ")
	b.WriteString("Only `comparable` quotes are matched across books; props/futures/specials are classified but excluded until a settlement rule pack exists.\n\n")

	for _, p := range d.Pairs {
		b.WriteString("## " + strings.ToUpper(p.Sport) + " pair\n\n")
		fmt.Fprintf(&b, "- Events: DK %d, FD %d, matched **%d** (max %.1f%%, union %.1f%%)\n", p.EventsA, p.EventsB, p.EventMatches, p.EventMatchRate, p.EventOverlapUnion)
		fmt.Fprintf(&b, "- Median event start skew: %d ms\n", p.EventDelta.MedianMs)
		fmt.Fprintf(&b, "- Comparable quotes: DK %d, FD %d, matched **%d** (union %.1f%%, DK coverage %.1f%%, FD coverage %.1f%%)\n",
			p.QuotesA, p.QuotesB, p.QuoteMatches, p.QuoteMatchUnion, p.QuoteCoverageA, p.QuoteCoverageB)
		fmt.Fprintf(&b, "- Identity coverage ignoring line: **%.1f%%** (%d identities); line mismatches: %d\n",
			p.IdentityUnion, p.IdentityMatches, p.LineMismatches)
		fmt.Fprintf(&b, "- **Conditional on shared events:** DK %d / FD %d quotes, identity **%.1f%%** (%d), exact-line **%.1f%%** (%d)\n",
			p.MatchedQuotesA, p.MatchedQuotesB, p.MatchedIdentityPct, p.MatchedIdentity, p.MatchedLinePct, p.MatchedExact)
		fmt.Fprintf(&b, "- Prices on same-line quotes: compared %d, same **%d**, different %d (cross-book differences are expected)",
			p.Price.Compared, p.Price.Exact, p.Price.Different)
		if p.Price.Different > 0 {
			fmt.Fprintf(&b, ", median |American diff| %.1f", p.Price.MedianAbsAmerican)
		}
		b.WriteString("\n")
		if len(p.UnmatchedA) > 0 || len(p.UnmatchedB) > 0 {
			fmt.Fprintf(&b, "- Unmatched events: DK %v; FD %v\n", p.UnmatchedA, p.UnmatchedB)
		}
		if len(p.LineMismatchSamples) > 0 {
			b.WriteString("\nSame market/outcome but different line (not price-comparable):\n\n```text\n")
			for _, s := range p.LineMismatchSamples {
				b.WriteString(s + "\n")
			}
			b.WriteString("```\n")
		}
		if len(p.Price.Samples) > 0 {
			b.WriteString("\nPrice differences on identical lines (sample; expected across books):\n\n```text\n")
			for _, s := range p.Price.Samples {
				b.WriteString(s + "\n")
			}
			b.WriteString("```\n")
		}
		if len(p.Families) > 0 {
			b.WriteString("\n| family | DK quotes | FD quotes | matched | price exact | price diff |\n|---|---:|---:|---:|---:|---:|\n")
			fams := make([]string, 0, len(p.Families))
			for f := range p.Families {
				fams = append(fams, f)
			}
			sort.Strings(fams)
			for _, f := range fams {
				s := p.Families[f]
				fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n", f, s.QuotesA, s.QuotesB, s.Matched, s.PriceExact, s.PriceDiff)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("## Taxonomy coverage\n\n")
	b.WriteString("| book | sport | events | markets | quotes | comparable | classified | high-confidence | unknown | dangling |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, t := range d.Taxonomies {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %.1f%% | %.1f%% | %d | %d |\n",
			t.Book, t.Sport, t.Events, t.Markets, t.Quotes, t.ComparableQuotes, t.ClassifiedPct, t.HighConfidencePct, t.UnknownMarkets, t.DanglingSelections)
	}
	for _, t := range d.Taxonomies {
		if len(t.UnknownTypes) > 0 {
			b.WriteString("\nUnknown types — " + t.Book + "/" + t.Sport + ":\n\n```text\n")
			for _, u := range t.UnknownTypes {
				fmt.Fprintf(&b, "%-48s %4d  %s\n", u.Type, u.Count, u.Example)
			}
			b.WriteString("```\n")
		}
	}
	if len(d.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, n := range d.Notes {
			b.WriteString("- " + n + "\n")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
