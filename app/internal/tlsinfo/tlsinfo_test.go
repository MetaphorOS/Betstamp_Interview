package tlsinfo

import "testing"

func TestCompatGODEBUG(t *testing.T) {
	for _, tc := range []struct {
		name    string
		godebug string
		want    string
	}{
		{"unset", "", "default"},
		{"other settings only", "http2client=0", "default"},
		{"the narrow setting", "tlssecpmlkem=0", "tlssecpmlkem=0"},
		{"the broad setting", "tlsmlkem=0", "tlsmlkem=0"},
		{"merged with other settings", "madvdontneed=1,tlssecpmlkem=0", "tlssecpmlkem=0"},
		{"both, in GODEBUG order", "tlsmlkem=0,tlssecpmlkem=0", "tlsmlkem=0,tlssecpmlkem=0"},
		{"enabled explicitly", "tlssecpmlkem=1", "default"},
		{"last field wins", "tlssecpmlkem=0,tlssecpmlkem=1", "default"},
		{"duplicate keeps position, last value", "tlssecpmlkem=1,tlsmlkem=0,tlssecpmlkem=0", "tlssecpmlkem=0,tlsmlkem=0"},
		{"empty field", "tlssecpmlkem=0,", "tlssecpmlkem=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GODEBUG", tc.godebug)
			if got := CompatGODEBUG(); got != tc.want {
				t.Errorf("CompatGODEBUG() with GODEBUG=%q = %q, want %q", tc.godebug, got, tc.want)
			}
		})
	}
}
