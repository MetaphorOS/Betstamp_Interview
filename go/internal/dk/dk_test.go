package dk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const minimalSnapshot = `{
  "sports": [{"id": "3"}],
  "leagues": [{"id": "88808"}],
  "events": [{"id": "1", "name": "A @ B", "status": "STARTED"}],
  "markets": [{"id": "m1", "eventId": "1", "name": "Moneyline", "marketType": {"name": "Moneyline"}}],
  "selections": [{"id": "s1", "marketId": "m1", "label": "A", "displayOdds": {"american": "-110", "decimal": "1.91"}, "outcomeType": "Away"}],
  "subscriptionPartials": {"events-1": {"entity": "events", "query": "$filter=leagueId eq '88808'", "includeMarkets": "$filter=tags/all(t: t ne 'SportcastBetBuilder')", "locale": "en-US"}}
}`

func TestFetchSnapshot(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Cache-Control", "public,max-age=1")
		fmt.Fprint(w, minimalSnapshot)
	}))
	defer srv.Close()

	res, err := FetchSnapshot(context.Background(), FetchOptions{URL: srv.URL})
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if gotUA != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want the measured Chrome UA", gotUA)
	}
	events, markets, selections := res.Counts()
	if events != 1 || markets != 1 || selections != 1 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/1", events, markets, selections)
	}
	if res.CacheControl != "public,max-age=1" {
		t.Fatalf("CacheControl = %q", res.CacheControl)
	}
	if res.ReceivedAt == 0 || res.ElapsedMs < 0 || res.ReceivedMonoMs <= 0 {
		t.Fatalf("bad timing metadata: %+v", res)
	}
	if len(res.Raw) == 0 {
		t.Fatal("raw body was not retained")
	}
}

func TestFetchSnapshotRejectsMissingArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"events":[],"markets":[]}`)
	}))
	defer srv.Close()

	_, err := FetchSnapshot(context.Background(), FetchOptions{URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "Invalid snapshot: missing selections") {
		t.Fatalf("err = %v, want missing selections", err)
	}
}

func TestFetchSnapshotHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, strings.Repeat("x", 500))
	}))
	defer srv.Close()

	_, err := FetchSnapshot(context.Background(), FetchOptions{URL: srv.URL})
	if err == nil || !strings.HasPrefix(err.Error(), "DraftKings HTTP 403: ") {
		t.Fatalf("err = %v, want DraftKings HTTP 403", err)
	}
	if len(err.Error()) > len("DraftKings HTTP 403: ")+161 {
		t.Fatalf("error body was not truncated: %d bytes", len(err.Error()))
	}
}

func TestCacheMergesPartialSelection(t *testing.T) {
	c := NewCache()
	if err := c.SeedRaw([]byte(minimalSnapshot)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	update := map[string]any{
		"change": map[string]any{
			"selections": []any{
				map[string]any{
					"id":          "s1",
					"displayOdds": map[string]any{"american": "+120"},
				},
				map[string]any{"id": "unknown", "label": "nope"},
			},
		},
	}
	stats := c.Apply(update)
	if stats.Change != 1 || stats.Unknown != 1 {
		t.Fatalf("stats = %+v, want 1 change and 1 unknown", stats)
	}
	rec := c.Selections["s1"]
	if rec["label"] != "A" {
		t.Fatalf("label lost on partial merge: %v", rec)
	}
	odds := rec["displayOdds"].(map[string]any)
	if odds["american"] != "+120" {
		t.Fatalf("american = %v, want +120", odds["american"])
	}
	if odds["decimal"] != "1.91" {
		t.Fatalf("decimal field dropped on partial displayOdds merge: %v", odds)
	}
	if _, ok := c.Selections["unknown"]; ok {
		t.Fatal("unknown change must not create an entity")
	}
}

func TestCacheRemoveAndAdd(t *testing.T) {
	c := NewCache()
	if err := c.SeedRaw([]byte(minimalSnapshot)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c.Apply(map[string]any{
		"remove": map[string]any{"selections": []any{map[string]any{"id": "s1"}}},
		"add":    map[string]any{"selections": []any{map[string]any{"id": "s2", "label": "B"}}},
	})
	if _, ok := c.Selections["s1"]; ok {
		t.Fatal("remove by object id failed")
	}
	if c.Selections["s2"]["label"] != "B" {
		t.Fatal("add failed")
	}

	// The probes also accept a bare id in remove arrays.
	c.Apply(map[string]any{"remove": map[string]any{"selections": []any{"s2"}}})
	if _, ok := c.Selections["s2"]; ok {
		t.Fatal("remove by bare id failed")
	}
}

func TestNFLSubscribeMessage(t *testing.T) {
	msg := NFLSubscribeMessage()
	if msg["method"] != "subscribe" || msg["id"] != "nfl-events" {
		t.Fatalf("bad top level: %v", msg)
	}
	params := msg["params"].(map[string]any)
	if params["entity"] != "events" || params["siteName"] != "dkcaon" {
		t.Fatalf("bad params: %v", params)
	}
	qp := params["queryParams"].(map[string]any)
	if qp["query"] != "$filter=leagueId eq '88808'" || qp["initialData"] != false {
		t.Fatalf("bad queryParams: %v", qp)
	}
}

func TestPartialsSubscribeMessages(t *testing.T) {
	partials := []SubscriptionPartial{{
		"entity":         "events",
		"query":          "$filter=leagueId eq '88808' AND clientMetadata/Subcategories/any(s: s/Id eq '4518')",
		"includeMarkets": "$filter=clientMetadata/subCategoryId eq '4518'",
		"locale":         "en",
		"unknownKey":     "forwarded",
	}}
	msgs := PartialsSubscribeMessages(partials, "", "")
	if len(msgs) != 1 {
		t.Fatalf("got %d messages", len(msgs))
	}
	if msgs[0]["id"] != "race-0" {
		t.Fatalf("id = %v", msgs[0]["id"])
	}
	params := msgs[0]["params"].(map[string]any)
	qp := params["queryParams"].(map[string]any)
	if qp["initialData"] != false || qp["projection"] != "sportsbook" {
		t.Fatalf("overrides missing: %v", qp)
	}
	if qp["unknownKey"] != "forwarded" {
		t.Fatalf("unknown partial key was dropped: %v", qp)
	}
	meta := params["clientMetadata"].(map[string]any)
	if meta["feature"] != "leagueSubcategory" || meta["X-Client-Version"] != "2643.4.1.10" {
		t.Fatalf("bad clientMetadata: %v", meta)
	}
	if _, ok := partials[0]["entity"]; !ok {
		t.Fatal("input partial was mutated")
	}
}

func TestIDUnmarshal(t *testing.T) {
	var v struct {
		A ID `json:"a"`
		B ID `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":123,"b":"x1"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "123" || v.B != "x1" {
		t.Fatalf("ids = %q %q", v.A, v.B)
	}
}
