package fd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const bundleJS = "var x={aws_appsync_clientId:\"NfxZUKb5R+do8pGKXq27wPTO3JHUlUmn\"};\n" +
	"subscription OnUpdateMarketPrice($ids: [String!]!) {\n" +
	"        onUpdateMarketPrice(ids: $ids) {\n            id\n        }\n    }\n" +
	"query GetMarketPrices($ids: [String]!) {\n" +
	"        getMarketPrices(ids: $ids) {\n            id\n        }\n    }\n"

func TestParseBundle(t *testing.T) {
	b, err := ParseBundle(bundleJS)
	if err != nil {
		t.Fatalf("ParseBundle: %v", err)
	}
	if b.AppSyncClientID != "NfxZUKb5R+do8pGKXq27wPTO3JHUlUmn" {
		t.Fatalf("client id = %q", b.AppSyncClientID)
	}
	if !strings.HasPrefix(b.SubscriptionQuery, "subscription OnUpdateMarketPrice") ||
		!strings.HasSuffix(b.SubscriptionQuery, "\n    }") {
		t.Fatalf("bad subscription query: %q", b.SubscriptionQuery)
	}
	if !strings.HasPrefix(b.SnapshotQuery, "query GetMarketPrices") {
		t.Fatalf("bad snapshot query: %q", b.SnapshotQuery)
	}
}

func TestParseBundleMissingParts(t *testing.T) {
	if _, err := ParseBundle("nothing here"); err == nil || !strings.Contains(err.Error(), "aws_appsync_clientId") {
		t.Fatalf("err = %v", err)
	}
}

func TestRealtimeURL(t *testing.T) {
	auth := Auth{Authorization: "KEY+abc=", Host: Host}
	got, err := RealtimeURL(auth)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = DefaultRealtimeURL + "?header="
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, "&payload=e30=") {
		t.Fatalf("url = %q", got)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(got, prefix), "&payload=e30=")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("header is not standard base64: %v", err)
	}
	want := `{"Authorization":"KEY+abc=","host":"pir.on.sportsbook.fanduel.ca"}`
	if string(raw) != want {
		t.Fatalf("decoded header = %s, want %s", raw, want)
	}
}

const boardJSON = `{
  "attachments": {
    "events": {"100": {"eventId": 100, "name": "AWAY @ HOME", "openDate": "2026-10-25T17:00:00.000Z"}},
    "markets": {
      "801.1": {"marketId": "801.1", "eventId": 100, "marketName": "Moneyline", "marketType": "MONEY_LINE", "inPlay": false, "marketStatus": "OPEN", "runners": [
        {"selectionId": 1, "handicap": 0, "runnerName": "AWAY", "result": {"type": "AWAY"}, "runnerStatus": "ACTIVE", "winRunnerOdds": {"americanDisplayOdds": {"americanOdds": 120, "americanOddsInt": 120}, "trueOdds": {"decimalOdds": {"decimalOdds": 2.2}}}},
        {"selectionId": 2, "handicap": 0, "runnerName": "HOME", "result": {"type": "HOME"}, "runnerStatus": "ACTIVE", "winRunnerOdds": {"americanDisplayOdds": {"americanOdds": -140, "americanOddsInt": -140}, "trueOdds": {"decimalOdds": {"decimalOdds": 1.714}}}}
      ]},
      "801.2": {"marketId": "801.2", "eventId": 100, "marketName": "Spread", "marketType": "MATCH_HANDICAP_(2-WAY)", "inPlay": true, "marketStatus": "OPEN", "runners": [
        {"selectionId": 3, "handicap": -2.5, "runnerName": "AWAY", "result": {"type": "AWAY"}, "runnerStatus": "ACTIVE", "winRunnerOdds": {"americanDisplayOdds": {"americanOdds": -110, "americanOddsInt": -110}, "trueOdds": {"decimalOdds": {"decimalOdds": 1.91}}}}
      ]},
      "801.3": {"marketId": "801.3", "eventId": 100, "marketName": "Total", "marketType": "TOTAL_POINTS_(OVER/UNDER)", "inPlay": false, "marketStatus": "OPEN", "runners": [
        {"selectionId": 4, "handicap": 44.5, "runnerName": "OVER", "result": {"type": "OVER"}, "runnerStatus": "SUSPENDED", "winRunnerOdds": {}}
      ]},
      "801.4": {"marketId": "801.4", "eventId": 100, "marketName": "Player Points", "marketType": "PLAYER_POINTS", "inPlay": false, "marketStatus": "OPEN", "runners": []}
    }
  }
}`

func TestBoardRows(t *testing.T) {
	var board Board
	if err := json.Unmarshal([]byte(boardJSON), &board); err != nil {
		t.Fatal(err)
	}
	rows, err := board.Rows()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (unsupported type filtered)", len(rows))
	}
	ml := rows[0]
	if ml.Market != "moneyline" || ml.Line != nil {
		t.Fatalf("moneyline row = %+v", ml)
	}
	if ml.American == nil || *ml.American != 120 || ml.Decimal == nil || *ml.Decimal != 2.2 {
		t.Fatalf("moneyline price = %+v", ml)
	}
	if ml.EventID != "100" || ml.Event != "AWAY @ HOME" || ml.Side != "AWAY" {
		t.Fatalf("moneyline identity = %+v", ml)
	}
	var spread, total *Row
	for i := range rows {
		switch rows[i].Market {
		case "spread":
			spread = &rows[i]
		case "total":
			total = &rows[i]
		}
	}
	if spread == nil || spread.Line == nil || *spread.Line != -2.5 || !spread.InPlay {
		t.Fatalf("spread row = %+v", spread)
	}
	if total == nil || total.American != nil || total.RunnerStatus != "SUSPENDED" {
		t.Fatalf("total row = %+v", total)
	}
}

func TestBoardRowsMissingEvent(t *testing.T) {
	var board Board
	if err := json.Unmarshal([]byte(`{"attachments":{"events":{},"markets":{"m":{"marketId":"m","eventId":9,"marketType":"MONEY_LINE"}}}}`), &board); err != nil {
		t.Fatal(err)
	}
	if _, err := board.Rows(); err == nil || !strings.Contains(err.Error(), "Missing event 9") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchMarketPricesGraphQL(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		fmt.Fprint(w, `{"data":{"getMarketPrices":[{"marketId":"801.1","marketStatus":"OPEN","inplay":false,"runnerDetails":[{"selectionId":1,"handicap":0,"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":120},"trueOdds":{"decimalOdds":{"decimalOdds":2.2}}}}]}]}}`)
	}))
	defer srv.Close()

	res, err := FetchMarketPrices(context.Background(), GraphQLOptions{URL: srv.URL, Authorization: "KEY"}, "query GetMarketPrices($ids: [String]!) { getMarketPrices(ids: $ids) { marketId } }", []string{"801.1"})
	if err != nil {
		t.Fatalf("FetchMarketPrices: %v", err)
	}
	if gotAuth != "KEY" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	ids := gotBody["variables"].(map[string]any)["ids"].([]any)
	if len(ids) != 1 || ids[0] != "801.1" {
		t.Fatalf("variables = %v", gotBody)
	}
	if res.Status != 200 || len(res.Markets) != 1 || res.ElapsedMs < 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := res.Markets[0].AllRunners()[0].WinRunnerOdds.American(); got == nil || *got != 120 {
		t.Fatalf("american = %v", got)
	}
	if len(res.Raw) == 0 {
		t.Fatal("raw body missing")
	}
}

func TestFetchMarketPricesGraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"errors":[{"message":"Unauthorized"}]}`)
	}))
	defer srv.Close()

	_, err := FetchMarketPrices(context.Background(), GraphQLOptions{URL: srv.URL}, "query { x }", []string{"1"})
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchMarketPricesSMP(t *testing.T) {
	var gotApp, gotCT, gotUA string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotApp = r.Header.Get("X-Application")
		gotCT = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		fmt.Fprint(w, `[{"marketId":"801.1","marketStatus":"OPEN","inplay":true,"runnerDetails":[{"selectionId":7,"handicap":3,"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-105}}}]}]`)
	}))
	defer srv.Close()

	res, err := FetchMarketPricesSMP(context.Background(), SMPOptions{URL: srv.URL, Application: "APP"}, []string{"801.1"})
	if err != nil {
		t.Fatalf("FetchMarketPricesSMP: %v", err)
	}
	if gotApp != "APP" || gotCT != "application/json" {
		t.Fatalf("headers app=%q content-type=%q", gotApp, gotCT)
	}
	if gotUA != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want browser UA (Go default is rejected with HTTP 500)", gotUA)
	}
	ids := gotBody["marketIds"].([]any)
	if len(ids) != 1 || ids[0] != "801.1" {
		t.Fatalf("body = %v", gotBody)
	}
	if len(res.Markets) != 1 || !res.Markets[0].IsInPlay() {
		t.Fatalf("result = %+v", res)
	}
	if got := res.Markets[0].AllRunners()[0].WinRunnerOdds.American(); got == nil || *got != -105 {
		t.Fatalf("american = %v", got)
	}
}

func TestStartMessage(t *testing.T) {
	rt := &Realtime{auth: Auth{Authorization: "KEY", Host: Host}}
	msg, err := rt.StartMessage("nfl-prices", "subscription X { y }", []string{"1", "2"})
	if err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "start" || msg["id"] != "nfl-prices" {
		t.Fatalf("msg = %v", msg)
	}
	payload := msg["payload"].(map[string]any)
	data, ok := payload["data"].(string)
	if !ok {
		t.Fatalf("payload.data must be a JSON string: %T", payload["data"])
	}
	var decoded struct {
		Query     string `json:"query"`
		Variables struct {
			IDs []string `json:"ids"`
		} `json:"variables"`
	}
	if err := json.Unmarshal([]byte(data), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Query != "subscription X { y }" || len(decoded.Variables.IDs) != 2 {
		t.Fatalf("decoded = %+v", decoded)
	}
	ext := payload["extensions"].(map[string]any)["authorization"].(Auth)
	if ext.Authorization != "KEY" {
		t.Fatalf("authorization = %+v", ext)
	}
}

func TestFramePrice(t *testing.T) {
	raw := []byte(`{"id":"nfl-prices","type":"data","payload":{"data":{"onUpdateMarketPrice":{"id":"801.1","inplay":true,"marketId":"801.1","marketStatus":"OPEN","runnerDetails":[{"selectionId":5,"handicap":-1,"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":155}}}]}}}}`)
	frame, err := ParseFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != "data" {
		t.Fatalf("type = %q", frame.Type)
	}
	market, err := frame.Price()
	if err != nil {
		t.Fatal(err)
	}
	if market.MarketID != "801.1" || !market.IsInPlay() {
		t.Fatalf("market = %+v", market)
	}
	if got := market.AllRunners()[0].WinRunnerOdds.American(); got == nil || *got != 155 {
		t.Fatalf("american = %v", got)
	}
}
