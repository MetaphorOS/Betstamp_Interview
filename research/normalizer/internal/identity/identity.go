// Package identity resolves book-specific participants to canonical teams and
// matches events across books. The prototype carries an NFL registry; the same
// interface is intended to be backed by a database for many sports/books.
package identity

import (
	"sort"
	"strings"
	"time"

	"sportsbook-normalizer/internal/model"
)

type teamDef struct {
	Mascot string
	City   string
	Abbr   string
}

// NFL registry. Mascots are unique, which makes "TB Buccaneers",
// "Tampa Bay Buccaneers" and "Buccaneers" collapse to the same canonical team.
var nflTeams = []teamDef{
	{"Cardinals", "Arizona", "ARI"},
	{"Falcons", "Atlanta", "ATL"},
	{"Ravens", "Baltimore", "BAL"},
	{"Bills", "Buffalo", "BUF"},
	{"Panthers", "Carolina", "CAR"},
	{"Bears", "Chicago", "CHI"},
	{"Bengals", "Cincinnati", "CIN"},
	{"Browns", "Cleveland", "CLE"},
	{"Cowboys", "Dallas", "DAL"},
	{"Broncos", "Denver", "DEN"},
	{"Lions", "Detroit", "DET"},
	{"Packers", "Green Bay", "GB"},
	{"Texans", "Houston", "HOU"},
	{"Colts", "Indianapolis", "IND"},
	{"Jaguars", "Jacksonville", "JAX"},
	{"Chiefs", "Kansas City", "KC"},
	{"Chargers", "Los Angeles", "LAC"},
	{"Rams", "Los Angeles", "LAR"},
	{"Raiders", "Las Vegas", "LV"},
	{"Dolphins", "Miami", "MIA"},
	{"Vikings", "Minnesota", "MIN"},
	{"Patriots", "New England", "NE"},
	{"Saints", "New Orleans", "NO"},
	{"Giants", "New York", "NYG"},
	{"Jets", "New York", "NYJ"},
	{"Eagles", "Philadelphia", "PHI"},
	{"Steelers", "Pittsburgh", "PIT"},
	{"Seahawks", "Seattle", "SEA"},
	{"49ers", "San Francisco", "SF"},
	{"Buccaneers", "Tampa Bay", "TB"},
	{"Titans", "Tennessee", "TEN"},
	{"Commanders", "Washington", "WAS"},
}

// ResolveTeam returns the canonical team for a book-provided name.
// For sports without a registry, a slug of the full name is used; that gives
// within-book identity but not cross-book alias resolution.
func ResolveTeam(sport, name string) (model.Team, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Team{}, false
	}
	if strings.EqualFold(sport, "nfl") {
		low := strings.ToLower(name)
		for _, t := range nflTeams {
			mascot := strings.ToLower(t.Mascot)
			if strings.Contains(low, mascot) {
				return model.Team{
					ID:   "nfl-" + model.Slug(t.Mascot),
					Name: t.City + " " + t.Mascot,
					Abbr: t.Abbr,
				}, true
			}
		}
		// Fall back to an abbreviation token only when it is unambiguous.
		for _, tok := range strings.Fields(low) {
			for _, t := range nflTeams {
				if tok == strings.ToLower(t.Abbr) {
					return model.Team{ID: "nfl-" + model.Slug(t.Mascot), Name: t.City + " " + t.Mascot, Abbr: t.Abbr}, true
				}
			}
		}
		return model.Team{}, false
	}
	slug := model.Slug(name)
	if slug == "" {
		return model.Team{}, false
	}
	// For sports without a full registry, the final token is the mascot for
	// both book naming styles ("LA Lakers" / "Los Angeles Lakers",
	// "TOR Maple Leafs" / "Toronto Maple Leafs"). Mascots are unique within a
	// league, so this gives within- and cross-book identity for the sampled
	// NBA/NHL/MLB boards. A real registry replaces this for ambiguous sports.
	tokens := strings.Fields(name)
	mascot := tokens[len(tokens)-1]
	return model.Team{ID: strings.ToLower(sport) + "-" + model.Slug(mascot), Name: name}, true
}

// EventPair is one cross-book event match.
type EventPair struct {
	A     model.Event   `json:"a"`
	B     model.Event   `json:"b"`
	Delta time.Duration `json:"deltaMs"`
}

// MatchEvents pairs events by canonical key (sorted team pair + UTC date) and
// verifies the start-time delta. The tolerance absorbs publish-time skew.
func MatchEvents(a, b []model.Event, tolerance time.Duration) (pairs []EventPair, unmatchedA, unmatchedB []model.Event) {
	index := map[string][]model.Event{}
	for _, e := range b {
		if e.Key != "" {
			index[e.Key] = append(index[e.Key], e)
		}
	}
	used := map[string]bool{}
	for _, ea := range a {
		cands := index[ea.Key]
		var best *model.Event
		var bestDelta time.Duration
		for i := range cands {
			d := ea.Start.Sub(cands[i].Start)
			if d < 0 {
				d = -d
			}
			if d > tolerance {
				continue
			}
			if best == nil || d < bestDelta {
				best = &cands[i]
				bestDelta = d
			}
		}
		if best == nil {
			unmatchedA = append(unmatchedA, ea)
			continue
		}
		used[best.BookEventID] = true
		pairs = append(pairs, EventPair{A: ea, B: *best, Delta: bestDelta})
	}
	for _, eb := range b {
		if !used[eb.BookEventID] {
			unmatchedB = append(unmatchedB, eb)
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].A.Key < pairs[j].A.Key })
	return pairs, unmatchedA, unmatchedB
}

// TeamForSide maps a book's home/away side to the canonical team ID.
func TeamForSide(e model.Event, side string) string {
	switch strings.ToLower(side) {
	case "home", "h":
		return e.Home.ID
	case "away", "a":
		return e.Away.ID
	}
	return ""
}
