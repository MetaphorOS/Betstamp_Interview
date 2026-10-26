package board

import (
	"math"

	"betstampodds/internal/model"
)

// Best price rule.
//
//  1. Eligible quotes: status open, the book's health is not stale/down, and
//     the book quotes sportsbook (american) odds. Prediction-market prices are
//     shown but never best.
//  2. The better line for the bettor wins:
//     - spread: higher signed line (+3.5 beats +3; -3 beats -3.5);
//     - over: lower total;
//     - under: higher total;
//     - moneyline has no line.
//     That is why -3 -110 beats -3.5 +100 for the favorite: a half point
//     through 3 is worth more than 10 cents in the NFL.
//  3. Same line: higher decimal odds.
//  4. Exact tie: most recently updated book, then book name, so the result is
//     deterministic.

// candidate is one eligible entry.
type candidate struct {
	book  string
	entry Entry
}

// lineScore orients the line so that higher is always better for the bettor.
func lineScore(family, outcome string, line *float64) float64 {
	if line == nil {
		return 0
	}
	switch family {
	case model.FamilySpread:
		return *line
	case model.FamilyTotal:
		if outcome == "over" {
			return -*line
		}
		return *line
	}
	return 0
}

// decimalOf prefers the precise decimal and falls back to american.
func decimalOf(e Entry) float64 {
	if e.Decimal > 1 {
		return e.Decimal
	}
	return AmericanToDecimal(e.American)
}

// AmericanToDecimal converts american odds to decimal.
func AmericanToDecimal(a int) float64 {
	switch {
	case a >= 100:
		return 1 + float64(a)/100
	case a <= -100:
		return 1 + 100/float64(-a)
	}
	return 0
}

const priceEpsilon = 1e-9

// better reports whether a beats b under the rule above.
func better(family, outcome string, a, b candidate) bool {
	la, lb := lineScore(family, outcome, a.entry.Line), lineScore(family, outcome, b.entry.Line)
	if math.Abs(la-lb) > priceEpsilon {
		return la > lb
	}
	da, db := decimalOf(a.entry), decimalOf(b.entry)
	if math.Abs(da-db) > priceEpsilon {
		return da > db
	}
	if !a.entry.UpdatedAt.Equal(b.entry.UpdatedAt) {
		return a.entry.UpdatedAt.After(b.entry.UpdatedAt)
	}
	return a.book < b.book
}

// PickBest returns the best eligible entry, or nil when none is eligible.
func PickBest(family, outcome string, entries map[string]Entry, eligible func(book string) bool) *Best {
	var best *candidate
	for name, e := range entries {
		if e.Status != model.StatusOpen || !eligible(name) {
			continue
		}
		c := candidate{book: name, entry: e}
		if best == nil || better(family, outcome, c, *best) {
			cp := c
			best = &cp
		}
	}
	if best == nil {
		return nil
	}
	return &Best{Book: best.book, Line: model.CopyLine(best.entry.Line), American: best.entry.American}
}

func sameBest(a, b *Best) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Book == b.Book && a.American == b.American && model.LineString(a.Line) == model.LineString(b.Line)
}
