package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sportsbookfeed/internal/fd"
	"sportsbookfeed/internal/jsonx"
)

func cmdFDevents(args []string) error {
	fs := flag.NewFlagSet("fd-events", flag.ContinueOnError)
	sport := fs.String("sport", "nba", "nba or nfl")
	outDir := fs.String("out", filepath.Join("out", "fd-events"), "output directory")
	maxEvents := fs.Int("max-events", 20, "maximum game events")
	ak := fs.String("ak", "", "FanDuel application key")
	timeout := fs.Duration("timeout", 25*time.Second, "request timeout")
	withPrices := fs.Bool("prices", true, "fetch SMP prices")
	if err := fs.Parse(args); err != nil {
		return err
	}
	akv := *ak
	if akv == "" {
		akv = fd.DefaultAK
	}
	boardURL := fmt.Sprintf("https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=%s&includePrices=true&_ak=%s", strings.ToLower(*sport), akv)
	disc, err := fd.Discover(context.Background(), fd.BoardOptions{URL: boardURL, AK: akv, Timeout: *timeout})
	if err != nil {
		return err
	}
	mainEvent := map[string]bool{}
	for _, m := range disc.Board.Attachments.Markets {
		if fd.IsMainMarketType(m.MarketType) {
			mainEvent[m.EventID.String()] = true
		}
	}
	var events []fd.Event
	for _, e := range disc.Board.Attachments.Events {
		if !mainEvent[e.EventID.String()] || !strings.Contains(e.Name, " @ ") || strings.Contains(e.Name, "(") {
			continue
		}
		events = append(events, e)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].OpenDate < events[j].OpenDate })
	if len(events) > *maxEvents {
		events = events[:*maxEvents]
	}
	if len(events) == 0 {
		return errors.New("no current game events found")
	}

	opts := fd.EventOptions{AK: akv, Timeout: *timeout}
	var boards []*fd.Board
	for _, e := range events {
		pop, err := fd.FetchEventPage(context.Background(), e.EventID.String(), "popular", opts)
		if err != nil {
			return fmt.Errorf("event %s popular: %w", e.EventID, err)
		}
		mga, err := fd.FetchMGAEvent(context.Background(), e.EventID.String(), opts)
		if err != nil {
			return fmt.Errorf("event %s mga: %w", e.EventID, err)
		}
		_ = writeFile(filepath.Join(*outDir, "event-"+e.EventID.String()+"-popular.json"), pop.Raw)
		_ = writeFile(filepath.Join(*outDir, "event-"+e.EventID.String()+"-mga.json"), mga.Raw)
		boards = append(boards, pop.Board, mga.Board)
	}
	merged := fd.MergeBoards(boards...)
	priced := 0
	if *withPrices {
		ids := make([]string, 0, len(merged.Attachments.Markets))
		for id := range merged.Attachments.Markets {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var prices []fd.Market
		for start := 0; start < len(ids); start += 70 {
			end := start + 70
			if end > len(ids) {
				end = len(ids)
			}
			res, err := fd.FetchMarketPricesSMP(context.Background(), fd.SMPOptions{Timeout: *timeout}, ids[start:end])
			if err != nil {
				return fmt.Errorf("SMP: %w", err)
			}
			prices = append(prices, res.Markets...)
		}
		raw, _ := jsonx.Marshal(prices)
		_ = writeFile(filepath.Join(*outDir, "prices.json"), raw)
		priced = fd.ApplyPrices(merged, prices)
	}
	raw, err := jsonx.Marshal(merged)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(*outDir, "board.json"), raw); err != nil {
		return err
	}
	eventIDs := make([]string, 0, len(events))
	for _, e := range events {
		eventIDs = append(eventIDs, e.EventID.String())
	}
	meta, _ := jsonx.Marshal(map[string]any{
		"sport": *sport, "events": eventIDs, "markets": len(merged.Attachments.Markets), "pricedMarkets": priced,
	})
	_ = writeFile(filepath.Join(*outDir, "meta.json"), meta)
	fmt.Printf("sport=%s events=%d markets=%d pricedMarkets=%d out=%s\n",
		*sport, len(events), len(merged.Attachments.Markets), priced, *outDir)
	return nil
}
