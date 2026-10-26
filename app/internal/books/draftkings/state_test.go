package draftkings

import (
	"encoding/json"
	"testing"
	"time"

	"betstampodds/internal/model"
)

var t0 = time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// seedState: MIN Vikings (home) vs IND Colts (away), pregame, one market of
// each main type.
func seedState(t *testing.T) *state {
	t.Helper()
	sel := func(id, market, outcome string, points *float64, american string) dkSelection {
		return dkSelection{ID: ID(id), MarketID: ID(market), OutcomeType: outcome, Points: points,
			DisplayOdds: &dkOdds{American: american}, TrueOdds: ptr(1.9)}
	}
	doc := dkDoc{
		Events: []dkEvent{{
			ID: "e1", Name: "IND Colts @ MIN Vikings", StartEventDate: "2026-10-25T17:00:00Z", Status: "NOT_STARTED",
			Participants: []dkParticipant{{Name: "MIN Vikings", VenueRole: "Home"}, {Name: "IND Colts", VenueRole: "Away"}},
		}},
		Markets: []dkMarket{
			{ID: "1_m", EventID: "e1", Name: "Moneyline", MarketType: dkMarketType{Name: "Moneyline"}},
			{ID: "2_m", EventID: "e1", Name: "Spread", MarketType: dkMarketType{Name: "Spread"}},
			{ID: "3_m", EventID: "e1", Name: "Total", MarketType: dkMarketType{Name: "Total"}},
		},
		Selections: []dkSelection{
			sel("ML_1", "1_m", "Home", nil, "−250"),
			sel("ML_3", "1_m", "Away", nil, "+200"),
			sel("HC_N650_1", "2_m", "Home", ptr(-6.5), "−110"),
			sel("HC_P650_3", "2_m", "Away", ptr(6.5), "−110"),
			sel("OU_O4150_1", "3_m", "Over", ptr(41.5), "−110"),
			sel("OU_U4150_3", "3_m", "Under", ptr(41.5), "−110"),
		},
	}
	snap, err := buildSnapshot(t0, leagueDoc{league: DefaultLeagues[0], doc: doc})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Quotes) != 6 {
		t.Fatalf("quotes = %d", len(snap.Quotes))
	}
	return snap.Private.(*state).clone()
}

type sim struct {
	t   *testing.T
	st  *state
	out []model.Update
}

func newSim(t *testing.T) *sim { return &sim{t: t, st: seedState(t)} }

// frame applies one update frame at t0+at, expiring first like the transport.
func (s *sim) frame(at time.Duration, data map[string]any) {
	s.t.Helper()
	now := t0.Add(at)
	s.out = append(s.out, s.st.expire(now)...)
	raw, _ := json.Marshal(map[string]any{
		"id": "race-0", "event": "update", "websocketPublishTimestamp": now.Add(-50 * time.Millisecond).Format(time.RFC3339Nano),
		"data": map[string]any{"data": data},
	})
	res, err := s.st.apply(raw, now)
	if err != nil {
		s.t.Fatalf("apply at %s: %v", at, err)
	}
	s.out = append(s.out, res.updates...)
}

func (s *sim) expire(at time.Duration) { s.out = append(s.out, s.st.expire(t0.Add(at))...) }

func (s *sim) count(status model.Status) int {
	n := 0
	for _, u := range s.out {
		if u.Status == status {
			n++
		}
	}
	return n
}

func addSel(id, market, outcome string, points *float64, american, replaces string) map[string]any {
	m := map[string]any{"id": id, "marketId": market, "outcomeType": outcome,
		"displayOdds": map[string]any{"american": american}, "trueOdds": 1.95}
	if points != nil {
		m["points"] = *points
	}
	if replaces != "" {
		m["replacedSelectionId"] = replaces
	}
	return m
}

func adds(sels ...map[string]any) map[string]any {
	return map[string]any{"add": map[string]any{"selections": sels}}
}
func removeSels(ids ...string) map[string]any {
	return map[string]any{"remove": map[string]any{"selections": ids}}
}

func overIdentity() string  { return model.MainIdentity(model.FamilyTotal, "over") }
func homeSpread() string    { return model.MainIdentity(model.FamilySpread, "nfl-vikings") }
func homeMoneyline() string { return model.MainIdentity(model.FamilyMoneyline, "nfl-vikings") }

func TestMainIdentityMatchesSnapshot(t *testing.T) {
	st := seedState(t)
	if st.sels["OU_O4150_1"].identity != overIdentity() || st.sels["HC_N650_1"].identity != homeSpread() {
		t.Fatalf("identities %q %q", st.sels["OU_O4150_1"].identity, st.sels["HC_N650_1"].identity)
	}
}

func TestLineMoveAddThenRemove(t *testing.T) {
	s := newSim(t)
	s.frame(0, adds(addSel("OU_O4250_1", "3_m", "Over", ptr(42.5), "−105", "OU_O4150_1")))
	s.frame(100*time.Millisecond, removeSels("OU_O4150_1"))
	s.expire(time.Minute)
	if len(s.out) != 1 || s.count(model.StatusRemoved) != 0 {
		t.Fatalf("updates = %+v", s.out)
	}
	if u := s.out[0]; *u.Line != 42.5 || u.Identity != overIdentity() || u.American != -105 {
		t.Fatalf("update = %+v", u)
	}
}

func TestLineMoveRemoveThenAdd(t *testing.T) {
	s := newSim(t)
	s.frame(0, removeSels("OU_O4150_1"))
	s.frame(1500*time.Millisecond, adds(addSel("OU_O4250_1", "3_m", "Over", ptr(42.5), "−105", "OU_O4150_1")))
	s.expire(time.Minute)
	if len(s.out) != 1 || s.count(model.StatusRemoved) != 0 || *s.out[0].Line != 42.5 {
		t.Fatalf("updates = %+v", s.out)
	}
}

func TestRemoveWithoutAddEmitsRemovedAfterGrace(t *testing.T) {
	s := newSim(t)
	s.frame(0, removeSels("OU_O4150_1"))
	s.expire(1999 * time.Millisecond)
	if len(s.out) != 0 {
		t.Fatalf("removed too early: %+v", s.out)
	}
	s.expire(2 * time.Second)
	if len(s.out) != 1 || s.out[0].Status != model.StatusRemoved || *s.out[0].Line != 41.5 {
		t.Fatalf("updates = %+v", s.out)
	}
}

func TestMarketFlapSameIDsRevives(t *testing.T) {
	s := newSim(t)
	s.frame(0, map[string]any{"remove": map[string]any{"markets": []string{"3_m"}}})
	s.frame(150*time.Millisecond, removeSels("OU_O4150_1", "OU_U4150_3"))
	s.frame(5*time.Second, map[string]any{"add": map[string]any{
		"markets":    []any{map[string]any{"id": "3_m", "eventId": "e1", "name": "Total", "marketType": map[string]any{"name": "Total"}, "isSuspended": false}},
		"selections": []any{addSel("OU_O4150_1", "3_m", "Over", ptr(41.5), "−115", "OU_O4050_1"), addSel("OU_U4150_3", "3_m", "Under", ptr(41.5), "−105", "")},
	}})
	s.expire(time.Minute)
	if s.count(model.StatusRemoved) != 0 || len(s.out) != 2 {
		t.Fatalf("updates = %+v", s.out)
	}
	if s.st.stats.Revived != 2 || !s.st.markets["3_m"].sels["OU_O4150_1"] {
		t.Fatalf("stats %+v sels %v", s.st.stats, s.st.markets["3_m"].sels)
	}
}

func TestKickoffEventReAddWithNewMarkets(t *testing.T) {
	s := newSim(t)
	s.frame(0, map[string]any{"remove": map[string]any{"events": []string{"e1"}}})
	s.frame(500*time.Millisecond, map[string]any{"remove": map[string]any{"markets": []string{"2_m"}}})
	// A selection-level remove must not shorten the 15 s event tombstone.
	s.frame(4*time.Second, removeSels("HC_N650_1"))
	s.expire(8 * time.Second)
	if len(s.out) != 0 {
		t.Fatalf("event tombstone shortened: %+v", s.out)
	}
	s.frame(6*time.Second, map[string]any{"add": map[string]any{
		"events": []any{map[string]any{"id": "e1", "status": "STARTED", "participants": []any{
			map[string]any{"name": "MIN Vikings", "venueRole": "Home"}, map[string]any{"name": "IND Colts", "venueRole": "Away"}}}},
		"markets": []any{map[string]any{"id": "2_live", "eventId": "e1", "name": "Spread", "marketType": map[string]any{"name": "Spread"}, "isSuspended": false}},
	}})
	s.frame(6500*time.Millisecond, adds(addSel("HC_live_N650_1", "2_live", "Home", ptr(-6.5), "−120", "HC_N650_1")))
	s.expire(time.Minute)
	removed := 0
	for _, u := range s.out {
		if u.Status == model.StatusRemoved {
			removed++
			if u.Identity == homeSpread() {
				t.Fatalf("replaced selection removed: %+v", u)
			}
		}
	}
	// Five pregame selections were never re-added in this test, so they go.
	if removed != 5 {
		t.Fatalf("removed = %d, updates %+v", removed, s.out)
	}
	var live *model.Update
	for i := range s.out {
		if s.out[i].Identity == homeSpread() {
			live = &s.out[i]
		}
	}
	if live == nil || !live.InPlay || live.GameRef != "e1" || *live.Line != -6.5 || live.Status != model.StatusOpen {
		t.Fatalf("live update = %+v", live)
	}
}

func TestMoneylineCrossMarketReplacement(t *testing.T) {
	s := newSim(t)
	s.frame(0, adds(addSel("ML_new_1", "1_other", "Home", nil, "−260", "ML_1")))
	if len(s.out) != 1 {
		t.Fatalf("updates = %+v", s.out)
	}
	u := s.out[0]
	if u.Identity != homeMoneyline() || u.GameRef != "e1" || u.Line != nil || u.American != -260 {
		t.Fatalf("update = %+v", u)
	}
}

func TestChangeMarketsSuspendsLiveMarket(t *testing.T) {
	s := newSim(t)
	s.frame(0, map[string]any{"add": map[string]any{
		"markets":    []any{map[string]any{"id": "3_live", "eventId": "e1", "name": "Total", "marketType": map[string]any{"name": "Total"}, "isSuspended": false}},
		"selections": []any{addSel("OU_live_O4450_1", "3_live", "Over", ptr(44.5), "−110", ""), addSel("OU_live_U4450_3", "3_live", "Under", ptr(44.5), "−110", "")},
	}})
	s.out = nil
	s.frame(time.Second, map[string]any{"change": map[string]any{"markets": []any{map[string]any{"id": "3_live", "isSuspended": true}}}})
	if len(s.out) != 2 || s.count(model.StatusSuspended) != 2 {
		t.Fatalf("updates = %+v", s.out)
	}
	for _, u := range s.out {
		if *u.Line != 44.5 {
			t.Fatalf("line lost: %+v", u)
		}
	}
	// A partial change without isSuspended leaves it suspended.
	s.out = nil
	s.frame(2*time.Second, map[string]any{"change": map[string]any{"markets": []any{map[string]any{"id": "3_live"}}}})
	if len(s.out) != 0 {
		t.Fatalf("unexpected updates %+v", s.out)
	}
}

func TestPartialChangeKeepsLine(t *testing.T) {
	s := newSim(t)
	s.frame(0, map[string]any{"change": map[string]any{"selections": []any{
		map[string]any{"id": "HC_N650_1", "displayOdds": map[string]any{"american": "−125"}, "trueOdds": 1.8, "replacedSelectionId": "HC_N550_1"},
	}}})
	if len(s.out) != 1 || *s.out[0].Line != -6.5 || s.out[0].American != -125 || s.out[0].Decimal != 1.8 {
		t.Fatalf("updates = %+v", s.out)
	}
}

func TestStaleTombstoneCannotWipeReplacement(t *testing.T) {
	s := newSim(t)
	s.frame(0, removeSels("OU_O4150_1"))
	// A new selection takes the slot through its market, naming no replacement.
	s.frame(500*time.Millisecond, adds(addSel("OU_O4250_1", "3_m", "Over", ptr(42.5), "−110", "")))
	s.expire(time.Minute)
	if s.count(model.StatusRemoved) != 0 {
		t.Fatalf("old tombstone wiped its replacement: %+v", s.out)
	}
}

func TestUnknownSelectionResyncs(t *testing.T) {
	st := seedState(t)
	raw, _ := json.Marshal(map[string]any{"event": "update", "data": map[string]any{"data": map[string]any{
		"change": map[string]any{"selections": []any{map[string]any{"id": "nope", "displayOdds": map[string]any{"american": "+100"}}}},
	}}})
	if _, err := st.apply(raw, t0); err == nil {
		t.Fatal("expected resync")
	}
}

func TestStreamDoesNotMutateSeed(t *testing.T) {
	seed := seedState(t)
	run := seed.clone()
	raw, _ := json.Marshal(map[string]any{"event": "update", "data": map[string]any{"data": removeSels("OU_O4150_1")}})
	if _, err := run.apply(raw, t0); err != nil {
		t.Fatal(err)
	}
	if seed.sels["OU_O4150_1"] == nil || len(seed.tombs) != 0 {
		t.Fatal("seed mutated")
	}
}
