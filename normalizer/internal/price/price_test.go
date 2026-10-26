package price

import (
	"math"
	"testing"

	"sportsbook-normalizer/internal/model"
)

func quote(book, key, outcome string, american *int, decimal *float64) model.Quote {
	return model.Quote{
		Key: key, Book: book, ScopeKey: "event|nba|2026-10-25|a@b",
		Family: model.FamilyMoneyline, Period: model.PeriodFull,
		Subject: "event", Comparator: model.ComparatorNone, Outcome: outcome,
		Comparable: true,
		Price:      model.Price{American: american, Decimal: decimal},
	}
}

func TestDewigRemovesVig(t *testing.T) {
	pm := &model.BookData{Book: "polymarket", Quotes: []model.Quote{
		quote("polymarket", "k1", "a", nil, fptr(0.50)),
		quote("polymarket", "k2", "b", nil, fptr(0.50)),
	}}
	dk := &model.BookData{Book: "draftkings", Quotes: []model.Quote{
		quote("draftkings", "k1", "a", iptr(-110), nil),
		quote("draftkings", "k2", "b", iptr(-110), nil),
	}}
	stats := Compare(pm, dk)
	if stats.Common != 2 || math.Abs(stats.MedianAbsFairPct) > 1e-9 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestDewigFindsProbabilityGap(t *testing.T) {
	pm := &model.BookData{Book: "polymarket", Quotes: []model.Quote{
		quote("polymarket", "k1", "a", nil, fptr(0.55)),
		quote("polymarket", "k2", "b", nil, fptr(0.45)),
	}}
	dk := &model.BookData{Book: "draftkings", Quotes: []model.Quote{
		quote("draftkings", "k1", "a", iptr(-110), nil),
		quote("draftkings", "k2", "b", iptr(-110), nil),
	}}
	stats := Compare(pm, dk)
	if stats.Common != 2 || math.Abs(stats.MedianAbsFairPct-5) > 1e-9 {
		t.Fatalf("stats = %+v", stats)
	}
}

func fptr(v float64) *float64 { return &v }

func iptr(v int) *int { return &v }
