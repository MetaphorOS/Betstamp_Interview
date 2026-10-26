// Package teams resolves book-provided participant names to canonical teams.
// It refuses to guess: unknown names return ok=false so the supervisor can
// count them, skip the game and resync instead of collapsing two teams.
package teams

import (
	"strings"

	"betstampodds/internal/model"
)

type teamDef struct {
	Mascot string
	City   string
	Abbr   string
}

var nfl = []teamDef{
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

func team(t teamDef) model.Team {
	return model.Team{
		ID:   "nfl-" + model.Slug(t.Mascot),
		Name: t.City + " " + t.Mascot,
		Abbr: t.Abbr,
	}
}

// Resolve returns a canonical team only when the name is in the NFL registry.
// There is deliberately no "last token" fallback: Boston Red Sox and Chicago
// White Sox must not become the same team.
func Resolve(sport, name string) (model.Team, bool) {
	if !strings.EqualFold(strings.TrimSpace(sport), "nfl") {
		return model.Team{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Team{}, false
	}
	low := strings.ToLower(name)
	for _, t := range nfl {
		if strings.Contains(low, strings.ToLower(t.Mascot)) {
			return team(t), true
		}
	}
	for _, tok := range strings.Fields(low) {
		tok = strings.Trim(tok, ".,()")
		for _, t := range nfl {
			if tok == strings.ToLower(t.Abbr) {
				return team(t), true
			}
		}
	}
	return model.Team{}, false
}

// NFL returns a copy of the registry for tests and diagnostics.
func NFL() []model.Team {
	out := make([]model.Team, 0, len(nfl))
	for _, t := range nfl {
		out = append(out, team(t))
	}
	return out
}
