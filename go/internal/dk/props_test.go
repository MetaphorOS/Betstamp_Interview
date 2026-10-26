package dk

import "testing"

func TestAutoPropSubcategories(t *testing.T) {
	data := &Data{
		Subcategories: []Subcategory{
			{ID: "12488", CategoryID: "1215", Name: "Points O/U"},
			{ID: "13009", CategoryID: "1215", Name: "Race to X Points"},
			{ID: "12492", CategoryID: "1216", Name: "Rebounds O/U"},
			{ID: "12497", CategoryID: "1218", Name: "Threes O/U"},
			{ID: "9999", CategoryID: "9999", Name: "Points O/U"},
		},
	}
	got := AutoPropSubcategories("nba", data)
	if len(got) != 3 {
		t.Fatalf("got %d subcategories: %+v", len(got), got)
	}
	for _, s := range got {
		if s.ID == "13009" || s.ID == "9999" {
			t.Errorf("unexpected subcategory %+v", s)
		}
	}
}

func TestAutoPropSubcategoriesNFLTD(t *testing.T) {
	data := &Data{Subcategories: []Subcategory{
		{ID: "12438", CategoryID: "1003", Name: "TD Scorer"},
		{ID: "15960", CategoryID: "1003", Name: "Player to Not Score"},
		{ID: "9524", CategoryID: "1000", Name: "Pass Yards O/U"},
	}}
	got := AutoPropSubcategories("nfl", data)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}
