package dk

import (
	"sort"
	"strings"
)

// propCategoryIDs is the allowlist of DraftKings player-prop categories.
var propCategoryIDs = map[string]map[string]bool{
	"nba": {
		"1215": true, // Player Points
		"1216": true, // Player Rebounds
		"1217": true, // Player Assists
		"1218": true, // Player Threes
		"1293": true, // Player Defense
	},
	"nfl": {
		"1000": true, // Passing Props
		"1342": true, // Receiving Props
		"1001": true, // Rushing Props
		"1744": true, // Defensive Props
		"1003": true, // TD Scorers
	},
}

// AutoPropSubcategories returns the prop subcategories in the sport's allowlist
// that are either an over/under prop or the full-game anytime TD scorer.
func AutoPropSubcategories(sport string, data *Data) []Subcategory {
	cats := propCategoryIDs[strings.ToLower(sport)]
	if cats == nil || data == nil {
		return nil
	}
	var out []Subcategory
	for _, s := range data.Subcategories {
		if !cats[s.CategoryID.String()] {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(s.Name))
		if strings.Contains(name, "o/u") || name == "td scorer" {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CategoryID != out[j].CategoryID {
			return out[i].CategoryID < out[j].CategoryID
		}
		return out[i].ID < out[j].ID
	})
	return out
}
