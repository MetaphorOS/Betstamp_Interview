// Package price implements the fair-probability comparison used for
// Polymarket vs sportsbook quotes. Sportsbook American/decimal prices include
// vig; Polymarket outcome prices are probabilities. Dewig puts both on a 100%
// probability basis before comparing exact-line quotes.
package price

import (
	"math"
	"sort"
	"strconv"

	"sportsbook-normalizer/internal/model"
)

// Stats summarizes fair-probability differences on exact-line matches.
type Stats struct {
	Common           int                    `json:"common"`
	MedianAbsFairPct float64                `json:"medianAbsFairPct"`
	MeanAbsFairPct   float64                `json:"meanAbsFairPct"`
	MaxAbsFairPct    float64                `json:"maxAbsFairPct"`
	Within1Pct       int                    `json:"within1Pct"`
	Within2Pct       int                    `json:"within2Pct"`
	Within5Pct       int                    `json:"within5Pct"`
	ByFamily         map[string]FamilyStats `json:"byFamily,omitempty"`
}

// FamilyStats is the same summary for one market family.
type FamilyStats struct {
	Common       int     `json:"common"`
	MedianAbsPct float64 `json:"medianAbsFairPct"`
	MaxAbsPct    float64 `json:"maxAbsFairPct"`
}

// Implied converts a normalized quote to an implied probability. Polymarket's
// Decimal is already a probability; sportsbook Decimal is decimal odds.
func Implied(q model.Quote) (float64, bool) {
	if q.Book == "polymarket" {
		if q.Price.Decimal != nil && *q.Price.Decimal > 0 && *q.Price.Decimal <= 1 {
			return *q.Price.Decimal, true
		}
	}
	if q.Price.American != nil {
		a := *q.Price.American
		if a > 0 {
			return 100 / (float64(a) + 100), true
		}
		if a < 0 {
			return float64(-a) / (float64(-a) + 100), true
		}
	}
	if q.Price.Decimal != nil && *q.Price.Decimal > 1 {
		return 1 / *q.Price.Decimal, true
	}
	return 0, false
}

// Fair returns de-vigged probabilities keyed by quote key. Only two-outcome
// groups are normalized; three-way/push markets are skipped until their
// settlement states are modeled.
func Fair(book *model.BookData) map[string]float64 {
	groups := map[string][]model.Quote{}
	for _, q := range book.Quotes {
		if !q.Comparable {
			continue
		}
		groups[groupKey(q)] = append(groups[groupKey(q)], q)
	}
	out := map[string]float64{}
	for _, qs := range groups {
		if len(qs) != 2 {
			continue
		}
		implied := make([]float64, len(qs))
		sum := 0.0
		ok := true
		for i, q := range qs {
			p, valid := Implied(q)
			if !valid {
				ok = false
				break
			}
			implied[i] = p
			sum += p
		}
		if !ok || sum <= 0 {
			continue
		}
		for i, q := range qs {
			out[q.Key] = implied[i] / sum
		}
	}
	return out
}

// Compare returns exact-line fair-probability differences between PM and a
// sportsbook.
func Compare(pm, book *model.BookData) Stats {
	pmFair := Fair(pm)
	bookFair := Fair(book)
	var diffs []float64
	byFamily := map[string][]float64{}
	for k, p := range pmFair {
		b, ok := bookFair[k]
		if !ok {
			continue
		}
		d := math.Abs(p-b) * 100
		diffs = append(diffs, d)
		q := findQuote(pm, k)
		byFamily[q.Family] = append(byFamily[q.Family], d)
	}
	s := Stats{Common: len(diffs), ByFamily: map[string]FamilyStats{}}
	if len(diffs) == 0 {
		return s
	}
	sort.Float64s(diffs)
	s.MedianAbsFairPct = diffs[len(diffs)/2]
	sum := 0.0
	for _, d := range diffs {
		sum += d
		if d <= 1 {
			s.Within1Pct++
		}
		if d <= 2 {
			s.Within2Pct++
		}
		if d <= 5 {
			s.Within5Pct++
		}
	}
	s.MeanAbsFairPct = sum / float64(len(diffs))
	s.MaxAbsFairPct = diffs[len(diffs)-1]
	for fam, ds := range byFamily {
		sort.Float64s(ds)
		fs := FamilyStats{Common: len(ds), MedianAbsPct: ds[len(ds)/2], MaxAbsPct: ds[len(ds)-1]}
		s.ByFamily[fam] = fs
	}
	return s
}

func findQuote(book *model.BookData, key string) model.Quote {
	for _, q := range book.Quotes {
		if q.Key == key {
			return q
		}
	}
	return model.Quote{}
}

func groupKey(q model.Quote) string {
	line := ""
	if q.Line != nil {
		// A spread's two sides have opposite signed lines; normalize by abs.
		line = strconv.FormatFloat(math.Abs(*q.Line), 'f', -1, 64)
	}
	return q.ScopeKey + "|" + q.Family + "|" + q.Period + "|" + q.Subject + "|" + q.Stat + "|" + q.Comparator + "|" + line + "|" + q.LineUnit
}
