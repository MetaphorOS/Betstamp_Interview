package fanduel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

func testdata(name string) string { return filepath.Join("..", "..", "..", "testdata", name) }

func loadBoardSeed(t *testing.T, name string) (*book.Snapshot, *seed) {
	t.Helper()
	raw, err := os.ReadFile(testdata(name))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseBoards(time.Time{}, raw)
	if err != nil {
		t.Fatal(err)
	}
	return snap, snap.Private.(*seed)
}

type fdReplayFrame struct {
	At      time.Time       `json:"at"`
	Conn    int             `json:"conn"`
	Payload json.RawMessage `json:"payload"`
}

// TestReplaySoak replays every data frame of the 1 h soak against the
// discovery board that FD connection started from.
func TestReplaySoak(t *testing.T) {
	f, err := os.Open(testdata("soak-fd-ws.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	byConn := map[int][]fdReplayFrame{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var fr fdReplayFrame
		if err := json.Unmarshal(sc.Bytes(), &fr); err != nil {
			t.Fatal(err)
		}
		byConn[fr.Conn] = append(byConn[fr.Conn], fr)
	}
	totalData := 0
	for conn := 1; conn <= 3; conn++ {
		snap, sd := loadBoardSeed(t, fmt.Sprintf("soak-fd-bootstrap-%d.json", conn))
		st := sd.clone()
		data, unknown, acks, kas := 0, 0, 0, 0
		byStatus := map[model.Status]int{}
		var ups []model.Update
		for _, fr := range byConn[conn] {
			res, err := st.apply(fr.Payload, fr.At)
			if err != nil {
				if errors.Is(err, book.ErrResync) {
					unknown++
					continue
				}
				t.Fatal(err)
			}
			switch res.kind {
			case frameData:
				data++
			case frameStartAck:
				acks++
			case frameKeepAlive:
				kas++
			}
			ups = append(ups, res.updates...)
		}
		for _, u := range ups {
			byStatus[u.Status]++
			p, _ := model.ParseIdentity(u.Identity)
			if p.Family == model.FamilyMoneyline && u.Line != nil {
				t.Fatalf("moneyline with a line: %+v", u)
			}
			if p.Family != model.FamilyMoneyline && u.Line == nil {
				t.Fatalf("spread/total without a line: %+v", u)
			}
			if !u.BookTS.IsZero() || u.Source != model.SourceStream {
				t.Fatalf("bad stamp %+v", u)
			}
		}
		t.Logf("conn%d: seed markets=%d skipped=%v data=%d unknown=%d start_ack=%d ka=%d updates=%d byStatus=%v",
			conn, len(sd.ids), snap.Skipped, data, unknown, acks, kas, len(ups), byStatus)
		if unknown != 0 {
			t.Errorf("conn%d: %d frames for markets missing from its board", conn, unknown)
		}
		if acks != 1 {
			t.Errorf("conn%d: start_ack = %d", conn, acks)
		}
		totalData += data
	}
	if totalData != 176+51+1394 {
		t.Fatalf("data frames = %d", totalData)
	}
}

func TestSMPMapsLikeStream(t *testing.T) {
	snap, sd := loadBoardSeed(t, "soak-fd-bootstrap-1.json")
	raw, err := os.ReadFile(testdata("soak-fd-smp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var markets []fdMarket
	if err := json.Unmarshal(raw, &markets); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 10, 25, 16, 20, 28, 0, time.UTC)
	ups, err := mapSMP(sd, markets, asOf, asOf.Add(80*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) == 0 {
		t.Fatal("no SMP updates")
	}
	for _, u := range ups {
		if u.Source != model.SourcePoll || !u.AsOf.Equal(asOf) {
			t.Fatalf("bad poll stamp %+v", u)
		}
	}
	// Read-only: polling must not change the seed the snapshot holds.
	if snap.Private.(*seed) != sd {
		t.Fatal("seed swapped")
	}
	t.Logf("SMP batch: %d markets -> %d updates", len(markets), len(ups))
}

func TestBundleParse(t *testing.T) {
	src := `x={aws_appsync_clientId:"abc123XYZ"};const q=` + "`" + `subscription OnUpdateMarketPrice($ids: [String!]!) {
        onUpdateMarketPrice(ids: $ids) {
            id
        }
    }` + "`"
	b, err := ParseBundle(src)
	if err != nil {
		t.Fatal(err)
	}
	if b.ClientID != "abc123XYZ" || !strings.HasPrefix(b.SubscriptionQuery, "subscription OnUpdateMarketPrice") {
		t.Fatalf("bundle = %+v", b)
	}
	if _, err := ParseBundle("nothing here"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCapturedBundleParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "captures", "fd-main.js"))
	if err != nil {
		t.Skip("captures/fd-main.js not present:", err)
	}
	b, err := ParseBundle(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b.SnapshotQuery == "" {
		t.Fatal("snapshot query missing")
	}
}

func TestUnknownRunnerResyncs(t *testing.T) {
	_, sd := loadBoardSeed(t, "soak-fd-bootstrap-1.json")
	id := sd.ids[0]
	raw, _ := json.Marshal(map[string]any{"type": "data", "payload": map[string]any{"data": map[string]any{
		"onUpdateMarketPrice": map[string]any{"marketId": id, "marketStatus": "OPEN", "runnerDetails": []any{
			map[string]any{"selectionId": 999999999, "runnerStatus": "ACTIVE"}}}}}})
	if _, err := sd.clone().apply(raw, time.Now()); !errors.Is(err, book.ErrResync) {
		t.Fatalf("err = %v", err)
	}
}

func TestSnapshotPricesComeFromSMP(t *testing.T) {
	smp, err := os.ReadFile(testdata("soak-fd-smp.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Application") == "" || !strings.HasPrefix(r.Header.Get("User-Agent"), "Mozilla/") {
			http.Error(w, "", http.StatusInternalServerError) // what SMP does to Go's UA
			return
		}
		w.Write(smp)
	}))
	defer srv.Close()
	b, _ := New(Config{Path: "unused", SMPURL: srv.URL})
	snap, sd := loadBoardSeed(t, "soak-fd-bootstrap-1.json")
	var markets []fdMarket
	if err := json.Unmarshal(smp, &markets); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{}
	for _, m := range markets {
		for _, r := range m.RunnerDetails {
			if v := r.WinRunnerOdds.American(); v != nil {
				want[m.MarketID+":"+r.SelectionID.String()] = int(*v)
			}
		}
	}
	if err := b.(*bookImpl).freshPrices(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, q := range snap.Quotes {
		if am, ok := want[q.BookSelectionKey]; ok {
			checked++
			if q.Price.American != am {
				t.Fatalf("%s: american %d, SMP says %d", q.BookSelectionKey, q.Price.American, am)
			}
			if r := sd.markets[q.BookMarketID].runners[strings.SplitN(q.BookSelectionKey, ":", 2)[1]]; r.american != am {
				t.Fatalf("seed not refreshed for %s", q.BookSelectionKey)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no quotes matched the SMP fixture")
	}
	t.Logf("refreshed %d quotes from SMP", checked)
}

func TestNBABoardFiltersCompetition(t *testing.T) {
	raw, err := os.ReadFile(testdata("fd-nba.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseLeagueBoards(time.Now(), DefaultLeagues, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nba games=%d quotes=%d skipped=%v", len(snap.Games), len(snap.Quotes), snap.Skipped)
	// The research filter (competition 10547864) gave 7 events / 42 selections;
	// the other basketball competition on the board must not leak in.
	if len(snap.Games) != 7 || len(snap.Quotes) != 42 {
		t.Fatalf("games=%d quotes=%d, want 7 / 42", len(snap.Games), len(snap.Quotes))
	}
	for _, g := range snap.Games {
		if g.Sport != "nba" || !strings.HasPrefix(g.Home.ID, "nba-") {
			t.Fatalf("bad game %+v", g)
		}
	}
}
