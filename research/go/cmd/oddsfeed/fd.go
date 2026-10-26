package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"sportsbookfeed/internal/clock"
	"sportsbookfeed/internal/fd"
	"sportsbookfeed/internal/jsonx"
)

func cmdFDdiscover(args []string) error {
	fs := flag.NewFlagSet("fd-discover", flag.ContinueOnError)
	ak := fs.String("ak", "", "public frontend application key (default: captured value)")
	url := fs.String("url", "", "override the content-managed-page URL")
	out := fs.String("out", filepath.Join("out", "fd-nfl.json"), "raw board output file")
	normalized := fs.String("normalized", filepath.Join("out", "fd-nfl-normalized.json"), "normalized rows output file")
	timeout := fs.Duration("timeout", 20*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	res, err := fd.Discover(context.Background(), fd.BoardOptions{URL: *url, AK: *ak, Timeout: *timeout})
	if err != nil {
		return err
	}
	if err := writeFile(*out, res.Raw); err != nil {
		return err
	}

	rows, err := res.Board.Rows()
	if err != nil {
		return err
	}
	normalizedJSON, err := jsonx.MarshalIndent(rows)
	if err != nil {
		return err
	}
	if err := writeFile(*normalized, normalizedJSON); err != nil {
		return err
	}

	games := map[string]struct{}{}
	markets := map[string]struct{}{}
	missing := 0
	for _, r := range rows {
		games[r.EventID] = struct{}{}
		markets[r.MarketID] = struct{}{}
		if r.American == nil {
			missing++
		}
	}
	line, err := jsonx.Marshal(struct {
		Games         int `json:"games"`
		Markets       int `json:"markets"`
		Selections    int `json:"selections"`
		MissingPrices int `json:"missingPrices"`
	}{Games: len(games), Markets: len(markets), Selections: len(rows), MissingPrices: missing})
	if err != nil {
		return err
	}
	fmt.Println(string(line))
	return nil
}

// priceMeta is the per-request report printed by fd-rest.
type priceMeta struct {
	Mode      string   `json:"mode"`
	URL       string   `json:"url"`
	Status    int      `json:"status"`
	ElapsedMs float64  `json:"elapsedMs"`
	Bytes     int      `json:"bytes"`
	IDs       []string `json:"ids"`
}

func cmdFDrest(args []string) error {
	fs := flag.NewFlagSet("fd-rest", flag.ContinueOnError)
	bundlePath := fs.String("bundle", defaultBundlePath(), "FanDuel main bundle to extract the AppSync id and queries")
	nflPath := fs.String("nfl", filepath.Join("out", "fd-nfl.json"), "discovery board used to choose market ids")
	idsFlag := fs.String("ids", "", "explicit comma-separated market ids")
	mode := fs.String("mode", "both", "both, graphql or smp")
	limit := fs.Int("limit", 3, "number of discovery markets to query when -ids is empty")
	outDir := fs.String("out", "out", "directory for raw response bodies")
	timeout := fs.Duration("timeout", 20*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	bundle, err := fd.LoadBundle(*bundlePath)
	if err != nil {
		return err
	}
	ids := splitIDs(*idsFlag)
	if len(ids) == 0 {
		ids, err = discoveryIDs(*nflPath, *limit)
		if err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return errors.New("no market ids: pass -ids, or run fd-discover first")
	}
	idsLine, _ := jsonx.Marshal(struct {
		IDs []string `json:"ids"`
	}{IDs: ids})
	fmt.Println(string(idsLine))

	if *mode == "both" || *mode == "graphql" {
		res, err := fd.FetchMarketPrices(context.Background(), fd.GraphQLOptions{
			Authorization: bundle.AppSyncClientID,
			Timeout:       *timeout,
		}, bundle.SnapshotQuery, ids)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*outDir, "fd-graphql.json"), res.Raw); err != nil {
			return err
		}
		printPriceMeta("graphql", fd.DefaultGraphQLURL, res.Status, res.ElapsedMs, len(res.Raw), ids)
		fmt.Println(jsonx.Truncate(string(res.Raw), 800))
		printPriceCounts("graphql", res.Markets)
	}
	if *mode == "both" || *mode == "smp" {
		res, err := fd.FetchMarketPricesSMP(context.Background(), fd.SMPOptions{Timeout: *timeout}, ids)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(*outDir, "fd-smp.json"), res.Raw); err != nil {
			return err
		}
		printPriceMeta("smp", fd.DefaultSMPURL, res.Status, res.ElapsedMs, len(res.Raw), ids)
		fmt.Println(jsonx.Truncate(string(res.Raw), 800))
		printPriceCounts("smp", res.Markets)
	}
	if *mode != "both" && *mode != "graphql" && *mode != "smp" {
		return fmt.Errorf("unknown -mode %q (want both, graphql or smp)", *mode)
	}
	return nil
}

func printPriceMeta(mode, url string, status int, elapsed float64, bytes int, ids []string) {
	line, _ := jsonx.Marshal(priceMeta{Mode: mode, URL: url, Status: status, ElapsedMs: elapsed, Bytes: bytes, IDs: ids})
	fmt.Println(string(line))
}

func printPriceCounts(mode string, markets []fd.Market) {
	runners := 0
	for i := range markets {
		runners += len(markets[i].AllRunners())
	}
	fmt.Printf("%s decoded markets=%d runners=%d\n", mode, len(markets), runners)
}

func cmdFDws(args []string) error {
	fs := flag.NewFlagSet("fd-ws", flag.ContinueOnError)
	bundlePath := fs.String("bundle", defaultBundlePath(), "FanDuel main bundle to extract the AppSync id and queries")
	nflPath := fs.String("nfl", filepath.Join("out", "fd-nfl.json"), "discovery board used to choose market ids")
	idsFlag := fs.String("ids", "", "explicit comma-separated market ids")
	live := fs.Bool("live", false, "use live in-play main markets instead of the NFL board")
	limit := fs.Int("limit", 18, "max live markets to subscribe to")
	seconds := fs.Int("seconds", envInt("CAPTURE_MS", 45000)/1000, "observation window in seconds")
	logPath := fs.String("log", filepath.Join("out", "fd-ws.jsonl"), "JSONL capture file")
	subID := fs.String("id", "nfl-prices", "graphql-ws subscription id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *seconds <= 0 {
		return errors.New("seconds must be positive")
	}

	bundle, err := fd.LoadBundle(*bundlePath)
	if err != nil {
		return err
	}

	var ids []string
	if explicit := splitIDs(*idsFlag); len(explicit) > 0 {
		ids = explicit
	} else if *live {
		board, err := fd.InPlay(context.Background(), "")
		if err != nil {
			return err
		}
		ids = mainMarketIDs(board.Board, *limit)
	} else {
		ids, err = discoveryIDs(*nflPath, 0)
		if err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return errors.New("no market ids: pass -ids, run fd-discover, or pass -live")
	}
	idsLine, _ := jsonx.Marshal(struct {
		Markets int      `json:"markets"`
		IDs     []string `json:"ids"`
	}{Markets: len(ids), IDs: ids})
	fmt.Println(string(idsLine))

	auth := bundle.Auth()
	connectURL, err := fd.RealtimeURL(auth)
	if err != nil {
		return err
	}
	logger, err := newJSONLLogger(*logPath, 3)
	if err != nil {
		return err
	}
	defer logger.Close()

	rt, err := fd.DialRealtime(context.Background(), connectURL, auth, 15*time.Second)
	if err != nil {
		return err
	}
	if proto := rt.Subprotocol(); proto != fd.Subprotocol {
		fmt.Fprintf(os.Stderr, "warning: negotiated subprotocol %q, expected %q\n", proto, fd.Subprotocol)
	}

	logger.log(logRecord{At: clock.NowISO(), Kind: "open"})
	init := map[string]any{"type": "connection_init"}
	logger.log(logRecord{At: clock.NowISO(), Kind: "sent", Payload: init})
	if err := rt.SendJSON(init); err != nil {
		return err
	}

	sum := wsSummary{Book: "fanduel", Subscription: *subID, Markets: len(ids), Seconds: *seconds, Log: *logPath, Events: map[string]int{}}
	dataMarkets := map[string]struct{}{}
	timer := time.AfterFunc(time.Duration(*seconds)*time.Second, func() { _ = rt.Close() })
	defer timer.Stop()

	for {
		raw, readErr := rt.Read()
		if readErr != nil {
			logClose(logger, readErr)
			break
		}
		sum.Received++
		sum.Bytes += len(raw)
		logger.log(logRecord{At: clock.NowISO(), Kind: "received", Payload: string(raw)})
		frame, parseErr := fd.ParseFrame(raw)
		if parseErr != nil {
			continue
		}
		count(sum.Events, frame.Type)
		switch frame.Type {
		case "connection_ack":
			startMsg, err := rt.StartMessage(*subID, bundle.SubscriptionQuery, ids)
			if err != nil {
				return err
			}
			logger.log(logRecord{At: clock.NowISO(), Kind: "sent", Payload: startMsg})
			if err := rt.SendJSON(startMsg); err != nil {
				return fmt.Errorf("start subscription: %w", err)
			}
		case "data":
			market, err := frame.Price()
			if err != nil {
				continue
			}
			sum.DataFrames++
			dataMarkets[market.MarketID] = struct{}{}
		}
	}
	_ = rt.Close()
	sum.MarketsWithData = len(dataMarkets)

	line, err := jsonx.Marshal(sum)
	if err != nil {
		return err
	}
	fmt.Println(string(line))
	return nil
}

func defaultBundlePath() string {
	if p := os.Getenv("FD_MAIN_JS"); p != "" {
		return p
	}
	for _, p := range []string{fd.DefaultBundlePath, filepath.Join("..", fd.DefaultBundlePath)} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return fd.DefaultBundlePath
}

// discoveryIDs loads a saved discovery board and returns main-market ids.
// limit <= 0 means all of them.
func discoveryIDs(path string, limit int) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read discovery board %s: %w", path, err)
	}
	var board fd.Board
	if err := json.Unmarshal(raw, &board); err != nil {
		return nil, fmt.Errorf("parse discovery board %s: %w", path, err)
	}
	return mainMarketIDs(&board, limit), nil
}

// mainMarketIDs returns sorted moneyline/spread/total market ids.
func mainMarketIDs(board *fd.Board, limit int) []string {
	keys := make([]string, 0, len(board.Attachments.Markets))
	for k := range board.Attachments.Markets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessNumericID(keys[i], keys[j]) })
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		m := board.Attachments.Markets[k]
		if !fd.IsMainMarketType(m.MarketType) {
			continue
		}
		ids = append(ids, m.MarketID)
		if limit > 0 && len(ids) >= limit {
			break
		}
	}
	return ids
}

func lessNumericID(a, b string) bool {
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea == nil && eb == nil {
		return fa < fb
	}
	return a < b
}
