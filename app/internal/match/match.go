// Package match performs N-book canonical game matching. Matching uses the
// sorted canonical team pair and a start-time tolerance, never a UTC calendar
// date, because a Sunday-night game can sit on two UTC dates across books.
package match

import (
	"sort"
	"time"

	"betstampodds/internal/model"
)

// Pair is one book game resolved to a canonical game.
type Pair struct {
	Game     model.Game
	BookGame model.BookGame
	Delta    time.Duration
}

// Matcher holds canonical games and assigns stable IDs on first sight.
type Matcher struct {
	byTeams map[string][]model.Game
	byID    map[string]model.Game
}

// New returns an empty matcher.
func New() *Matcher {
	return &Matcher{byTeams: map[string][]model.Game{}, byID: map[string]model.Game{}}
}

// Resolve matches a book game or creates a canonical game. The returned bool is
// false only for invalid input; unknown teams should already have been refused
// by the book plugin.
func (m *Matcher) Resolve(bg model.BookGame, tolerance time.Duration) (Pair, bool) {
	if bg.Away.ID == "" || bg.Home.ID == "" || bg.Start.IsZero() {
		return Pair{}, false
	}
	key := teamPair(bg.Away.ID, bg.Home.ID)
	var best *model.Game
	var bestDelta time.Duration
	for i := range m.byTeams[key] {
		g := m.byTeams[key][i]
		d := bg.Start.Sub(g.Start)
		if d < 0 {
			d = -d
		}
		if d > tolerance {
			continue
		}
		if best == nil || d < bestDelta {
			cp := g
			best = &cp
			bestDelta = d
		}
	}
	if best != nil {
		return Pair{Game: *best, BookGame: bg, Delta: bestDelta}, true
	}
	id := model.GameKey(bg.Sport, bg.Start, bg.Away.ID, bg.Home.ID)
	g := model.Game{
		ID: id, Sport: bg.Sport, League: bg.League,
		Start: bg.Start.UTC(), Away: bg.Away, Home: bg.Home,
	}
	m.byTeams[key] = append(m.byTeams[key], g)
	m.byID[id] = g
	return Pair{Game: g, BookGame: bg}, true
}

// Game returns a canonical game by ID.
func (m *Matcher) Game(id string) (model.Game, bool) {
	g, ok := m.byID[id]
	return g, ok
}

// Games returns all known canonical games in deterministic order.
func (m *Matcher) Games() []model.Game {
	out := make([]model.Game, 0, len(m.byID))
	for _, g := range m.byID {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func teamPair(a, b string) string {
	ids := []string{a, b}
	sort.Strings(ids)
	return ids[0] + "@" + ids[1]
}
