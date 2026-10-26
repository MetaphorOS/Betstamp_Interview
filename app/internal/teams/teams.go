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

// nba mascots are matched as whole words: "Hornets" contains "nets".
var nba = []teamDef{
	{"Hawks", "Atlanta", "ATL"},
	{"Celtics", "Boston", "BOS"},
	{"Nets", "Brooklyn", "BKN"},
	{"Hornets", "Charlotte", "CHA"},
	{"Bulls", "Chicago", "CHI"},
	{"Cavaliers", "Cleveland", "CLE"},
	{"Mavericks", "Dallas", "DAL"},
	{"Nuggets", "Denver", "DEN"},
	{"Pistons", "Detroit", "DET"},
	{"Warriors", "Golden State", "GS"},
	{"Rockets", "Houston", "HOU"},
	{"Pacers", "Indiana", "IND"},
	{"Clippers", "Los Angeles", "LAC"},
	{"Lakers", "Los Angeles", "LAL"},
	{"Grizzlies", "Memphis", "MEM"},
	{"Heat", "Miami", "MIA"},
	{"Bucks", "Milwaukee", "MIL"},
	{"Timberwolves", "Minnesota", "MIN"},
	{"Pelicans", "New Orleans", "NO"},
	{"Knicks", "New York", "NY"},
	{"Thunder", "Oklahoma City", "OKC"},
	{"Magic", "Orlando", "ORL"},
	{"76ers", "Philadelphia", "PHI"},
	{"Suns", "Phoenix", "PHX"},
	{"Trail Blazers", "Portland", "POR"},
	{"Kings", "Sacramento", "SAC"},
	{"Spurs", "San Antonio", "SA"},
	{"Raptors", "Toronto", "TOR"},
	{"Jazz", "Utah", "UTA"},
	{"Wizards", "Washington", "WAS"},
}

// Resolve returns a canonical team only when the name is in that league's
// registry. There is deliberately no "last token" fallback: Boston Red Sox
// and Chicago White Sox must not become the same team.
func Resolve(sport, name string) (model.Team, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Team{}, false
	}
	switch strings.ToLower(strings.TrimSpace(sport)) {
	case "nfl":
		return resolveNFL(name)
	case "nba":
		return resolveWords("nba", nba, name)
	}
	return model.Team{}, false
}

func resolveNFL(name string) (model.Team, bool) {
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

// resolveWords matches a mascot as a whole word sequence ("Trail Blazers"
// included), and failing that an exact abbreviation token.
func resolveWords(league string, defs []teamDef, name string) (model.Team, bool) {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == ' ' || r == '.' || r == ',' || r == '(' || r == ')' || r == '-'
	})
	has := func(seq []string) bool {
		for i := 0; i+len(seq) <= len(words); i++ {
			match := true
			for j, w := range seq {
				if words[i+j] != w {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
		return false
	}
	for _, t := range defs {
		if has(strings.Fields(strings.ToLower(t.Mascot))) {
			return teamIn(league, t), true
		}
	}
	for _, w := range words {
		for _, t := range defs {
			if w == strings.ToLower(t.Abbr) {
				return teamIn(league, t), true
			}
		}
	}
	return model.Team{}, false
}

func teamIn(league string, t teamDef) model.Team {
	return model.Team{ID: league + "-" + model.Slug(t.Mascot), Name: t.City + " " + t.Mascot, Abbr: t.Abbr}
}

// NFL returns a copy of the registry for tests and diagnostics.
func NFL() []model.Team {
	out := make([]model.Team, 0, len(nfl))
	for _, t := range nfl {
		out = append(out, team(t))
	}
	return out
}
