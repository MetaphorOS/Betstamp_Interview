package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"sportsbook-normalizer/internal/model"
)

const dkFixture = `{
  "events": [{"id":"1","name":"TB Buccaneers @ CAR Panthers","startEventDate":"2026-10-25T17:05:00Z","participants":[
    {"name":"TB Buccaneers","venueRole":"Away"},{"name":"CAR Panthers","venueRole":"Home"}]}],
  "markets": [
    {"id":"m1","eventId":"1","name":"Moneyline","marketType":{"name":"Moneyline"}},
    {"id":"m2","eventId":"1","name":"Spread","marketType":{"name":"Spread"}},
    {"id":"m3","eventId":"1","name":"Total","marketType":{"name":"Total"}}
  ],
  "selections": [
    {"id":"s1","marketId":"m1","label":"TB Buccaneers","outcomeType":"Away","displayOdds":{"american":"+140","decimal":"2.40"}},
    {"id":"s2","marketId":"m1","label":"CAR Panthers","outcomeType":"Home","displayOdds":{"american":"-166","decimal":"1.60"}},
    {"id":"s3","marketId":"m2","label":"TB Buccaneers","outcomeType":"Away","points":3,"displayOdds":{"american":"-110","decimal":"1.90"}},
    {"id":"s4","marketId":"m2","label":"CAR Panthers","outcomeType":"Home","points":-3,"displayOdds":{"american":"-110","decimal":"1.90"}},
    {"id":"s5","marketId":"m3","label":"Over","outcomeType":"Over","points":44.5,"displayOdds":{"american":"-110","decimal":"1.90"}},
    {"id":"s6","marketId":"m3","label":"Under","outcomeType":"Under","points":44.5,"displayOdds":{"american":"-110","decimal":"1.90"}}
  ]
}`

const fdFixture = `{
  "attachments": {
    "events": {"e1":{"eventId":1,"name":"Tampa Bay Buccaneers @ Carolina Panthers","openDate":"2026-10-25T17:01:00Z"}},
    "markets": {
      "fm1":{"marketId":"fm1","eventId":1,"marketName":"Moneyline","marketType":"MONEY_LINE","marketStatus":"OPEN","runners":[
        {"selectionId":1,"runnerName":"Tampa Bay Buccaneers","result":{"type":"AWAY"},"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":145},"trueOdds":{"decimalOdds":{"decimalOdds":2.45}}}},
        {"selectionId":2,"runnerName":"Carolina Panthers","result":{"type":"HOME"},"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-165},"trueOdds":{"decimalOdds":{"decimalOdds":1.606}}}}
      ]},
      "fm2":{"marketId":"fm2","eventId":1,"marketName":"Spread","marketType":"MATCH_HANDICAP_(2-WAY)","marketStatus":"OPEN","runners":[
        {"selectionId":3,"runnerName":"Tampa Bay Buccaneers","result":{"type":"AWAY"},"handicap":3,"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-105},"trueOdds":{"decimalOdds":{"decimalOdds":1.95}}}},
        {"selectionId":4,"runnerName":"Carolina Panthers","result":{"type":"HOME"},"handicap":-3,"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-115},"trueOdds":{"decimalOdds":{"decimalOdds":1.87}}}}
      ]},
      "fm3":{"marketId":"fm3","eventId":1,"marketName":"Total Points","marketType":"TOTAL_POINTS_(OVER/UNDER)","marketStatus":"OPEN","runners":[
        {"selectionId":5,"runnerName":"Over","result":{"type":"OVER"},"handicap":44.5,"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-110},"trueOdds":{"decimalOdds":{"decimalOdds":1.91}}}},
        {"selectionId":6,"runnerName":"Under","result":{"type":"UNDER"},"handicap":44.5,"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-110},"trueOdds":{"decimalOdds":{"decimalOdds":1.91}}}}
      ]},
      "fm4":{"marketId":"fm4","eventId":1,"marketName":"Any Time Touchdown Scorer","marketType":"ANY_TIME_TOUCHDOWN_SCORER","marketStatus":"OPEN","runners":[
        {"selectionId":7,"runnerName":"Bijan Robinson","result":{},"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":125}}},
        {"selectionId":8,"runnerName":"Drake London","result":{},"runnerStatus":"ACTIVE","winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":210}}}
      ]}
    }
  }
}`

func writeFixture(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCrossBookMainQuoteKeys(t *testing.T) {
	dk, err := DK(writeFixture(t, "dk.json", dkFixture), "nfl")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := FD(writeFixture(t, "fd.json", fdFixture), "nfl")
	if err != nil {
		t.Fatal(err)
	}
	if len(dk.Events) != 1 || len(fd.Events) != 1 {
		t.Fatalf("events dk=%d fd=%d", len(dk.Events), len(fd.Events))
	}
	if dk.Events[0].Key != fd.Events[0].Key {
		t.Fatalf("event keys differ:\n%s\n%s", dk.Events[0].Key, fd.Events[0].Key)
	}
	dkMain := map[string]bool{}
	for _, q := range dk.Quotes {
		if q.Comparable {
			dkMain[q.Key] = true
		}
	}
	fdMain := map[string]bool{}
	for _, q := range fd.Quotes {
		if q.Comparable {
			fdMain[q.Key] = true
		}
	}
	if len(dkMain) != 6 || len(fdMain) != 6 {
		t.Fatalf("comparable quotes dk=%d fd=%d", len(dkMain), len(fdMain))
	}
	for k := range dkMain {
		if !fdMain[k] {
			t.Errorf("DK key missing from FD: %s", k)
		}
	}
}

func TestFDAltQuotesAreNormalizedButNotComparable(t *testing.T) {
	fd, err := FD(writeFixture(t, "fd.json", fdFixture), "nfl")
	if err != nil {
		t.Fatal(err)
	}
	var alt []model.Quote
	for _, q := range fd.Quotes {
		if q.Family == model.FamilyPlayerProp {
			alt = append(alt, q)
		}
	}
	if len(alt) != 2 {
		t.Fatalf("alt quotes = %d, want 2", len(alt))
	}
	for _, q := range alt {
		if q.Comparable {
			t.Errorf("alt quote marked comparable: %s", q.Key)
		}
		if q.Stat != "anytime_touchdown" || q.Comparator != model.ComparatorYesNo || q.Outcome != "yes" {
			t.Errorf("bad alt facets: %+v", q)
		}
	}
}

const pmFixture = `[
  {
    "id": "pm-game",
    "title": "Lakers vs. Jazz",
    "endDate": "2026-10-25T21:00:00Z",
    "teams": [
      {"name": "Lakers", "ordering": "away"},
      {"name": "Jazz", "ordering": "home"}
    ],
    "markets": [
      {"id": "pm-ml", "question": "Lakers vs. Jazz", "sportsMarketType": "moneyline",
       "outcomes": "[\"Lakers\", \"Jazz\"]", "outcomePrices": "[\"0.585\", \"0.415\"]",
       "clobTokenIds": "[\"ml-a\", \"ml-b\"]", "volume": "1000"},
      {"id": "pm-spread", "question": "Spread: Lakers (-4.5)", "line": -4.5,
       "sportsMarketType": "spreads", "outcomes": "[\"Lakers\", \"Jazz\"]",
       "outcomePrices": "[\"0.5\", \"0.5\"]", "clobTokenIds": "[\"sp-a\", \"sp-b\"]", "volume": "800"},
      {"id": "pm-total", "question": "Lakers vs. Jazz: O/U 237.5", "line": 237.5,
       "sportsMarketType": "totals", "outcomes": "[\"Over\", \"Under\"]",
       "outcomePrices": "[\"0.53\", \"0.47\"]", "clobTokenIds": "[\"to-a\", \"to-b\"]", "volume": "700"},
      {"id": "pm-prop", "question": "Luka Doncic: Points O/U 25.5", "line": 25.5,
       "sportsMarketType": "points", "outcomes": "[\"Yes\", \"No\"]",
       "outcomePrices": "[\"0.6\", \"0.4\"]", "clobTokenIds": "[\"pr-a\", \"pr-b\"]", "volume": "100"}
    ]
  }
]`

func TestPMBinaryMarketsNormalize(t *testing.T) {
	pm, err := PM(writeFixture(t, "pm.json", pmFixture), "nba")
	if err != nil {
		t.Fatal(err)
	}
	if len(pm.Events) != 1 || pm.Events[0].Key != "event|nba|2026-10-25|nba-jazz@nba-lakers" {
		t.Fatalf("event = %+v", pm.Events)
	}
	if len(pm.Quotes) != 8 {
		t.Fatalf("quotes = %d, want 8", len(pm.Quotes))
	}
	byFamily := map[string][]model.Quote{}
	for _, q := range pm.Quotes {
		byFamily[q.Family] = append(byFamily[q.Family], q)
	}
	if len(byFamily[model.FamilyMoneyline]) != 2 ||
		len(byFamily[model.FamilySpread]) != 2 ||
		len(byFamily[model.FamilyTotal]) != 2 ||
		len(byFamily[model.FamilyPlayerProp]) != 2 {
		t.Fatalf("families = %+v", byFamily)
	}
	ml := byFamily[model.FamilyMoneyline]
	if ml[0].Price.Decimal == nil || ml[1].Price.Decimal == nil ||
		*ml[0].Price.Decimal == *ml[1].Price.Decimal {
		t.Fatalf("moneyline prices not complemented: %+v", ml)
	}
	var jazzSpread, overQuote, propOver bool
	for _, q := range byFamily[model.FamilySpread] {
		if q.Outcome == "nba-jazz" && q.Line != nil && *q.Line == 4.5 {
			jazzSpread = true
		}
	}
	for _, q := range byFamily[model.FamilyTotal] {
		if q.Outcome == "over" && q.Line != nil && *q.Line == 237.5 {
			overQuote = true
		}
	}
	for _, q := range byFamily[model.FamilyPlayerProp] {
		if q.Outcome == "over" && q.Subject == "player:luka-doncic" {
			propOver = true
		}
	}
	if !jazzSpread || !overQuote || !propOver {
		t.Fatalf("missing normalized alt/prop quote: spread=%v total=%v prop=%v", jazzSpread, overQuote, propOver)
	}
}

const fdEventFixture = `{
  "attachments": {
    "events": {"e1":{"eventId":1,"name":"Tampa Bay Buccaneers @ Carolina Panthers","openDate":"2026-10-25T17:01:00Z"}},
    "markets": {
      "main":{"marketId":"main","eventId":1,"marketName":"Moneyline","marketType":"MONEY_LINE","marketStatus":"OPEN","runners":[
        {"selectionId":1,"runnerName":"Tampa Bay Buccaneers","result":{"type":"AWAY"},"winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":145}}},
        {"selectionId":2,"runnerName":"Carolina Panthers","result":{"type":"HOME"},"winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-165}}}
      ]},
      "altsp":{"marketId":"altsp","eventId":1,"marketName":"Alternate Spread","marketType":"ALTERNATE_HANDICAPS","marketStatus":"OPEN","runners":[
        {"selectionId":3,"runnerName":"Tampa Bay Buccaneers +4.5","result":{"type":"AWAY"},"handicap":0,"winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-110}}},
        {"selectionId":4,"runnerName":"Carolina Panthers -4.5","result":{"type":"HOME"},"handicap":0,"winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-110}}}
      ]},
      "alttot":{"marketId":"alttot","eventId":1,"marketName":"Alternate Total Points","marketType":"ALTERNATE_TOTAL","marketStatus":"OPEN","runners":[
        {"selectionId":5,"runnerName":"Over 44.5","result":{"type":"OVER"},"handicap":0,"winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-105}}},
        {"selectionId":6,"runnerName":"Under 44.5","result":{"type":"UNDER"},"handicap":0,"winRunnerOdds":{"americanDisplayOdds":{"americanOddsInt":-115}}}
      ]}
    }
  }
}`

func TestFDEventAltLines(t *testing.T) {
	fd, err := FD(writeFixture(t, "fd-event.json", fdEventFixture), "nfl")
	if err != nil {
		t.Fatal(err)
	}
	var spreadLines, totalLines []float64
	for _, q := range fd.Quotes {
		if q.Family == model.FamilySpread && q.Line != nil {
			spreadLines = append(spreadLines, *q.Line)
		}
		if q.Family == model.FamilyTotal && q.Line != nil {
			totalLines = append(totalLines, *q.Line)
		}
	}
	if len(spreadLines) != 2 || spreadLines[0] != 4.5 || spreadLines[1] != -4.5 {
		t.Fatalf("alt spread lines = %v", spreadLines)
	}
	if len(totalLines) != 2 || totalLines[0] != 44.5 || totalLines[1] != 44.5 {
		t.Fatalf("alt total lines = %v", totalLines)
	}
}

func TestDKPropClassificationAndPlayer(t *testing.T) {
	class := classifyDK("nba", "Points O/U", "Tyrese Maxey Points O/U", "scope")
	if class.Family != model.FamilyPlayerProp || class.Stat != "points" ||
		class.Comparator != model.ComparatorOU || !class.Alt || !class.Comparable {
		t.Fatalf("class = %+v", class)
	}
	if got := dkPlayerFromName("Over", "Tyrese Maxey Points O/U"); got != "Tyrese Maxey" {
		t.Fatalf("player = %q", got)
	}
	total := classifyDK("nfl", "Pass Yards O/U", "Josh Allen Pass Yards O/U", "scope")
	if total.Stat != "passing_yards" || !total.Alt {
		t.Fatalf("total class = %+v", total)
	}
}
