package dk

import "testing"

func TestMergeDataDedupes(t *testing.T) {
	main := &Data{
		Events:     []Event{{ID: "e1"}},
		Markets:    []Market{{ID: "m1"}},
		Selections: []Selection{{ID: "s1"}},
	}
	alt := &Data{
		Events:     []Event{{ID: "e1"}, {ID: "e2"}},
		Markets:    []Market{{ID: "m1"}, {ID: "m2"}},
		Selections: []Selection{{ID: "s1"}, {ID: "s2"}},
	}
	merged := MergeData(main, alt)
	if len(merged.Events) != 2 || len(merged.Markets) != 2 || len(merged.Selections) != 2 {
		t.Fatalf("merged = %d/%d/%d", len(merged.Events), len(merged.Markets), len(merged.Selections))
	}
	if merged.Markets[0].ID != "m1" || merged.Markets[1].ID != "m2" {
		t.Fatalf("markets = %+v", merged.Markets)
	}
}
