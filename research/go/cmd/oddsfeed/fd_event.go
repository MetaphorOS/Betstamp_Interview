package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"sportsbookfeed/internal/fd"
	"sportsbookfeed/internal/jsonx"
)

func cmdFDevent(args []string) error {
	fs := flag.NewFlagSet("fd-event", flag.ContinueOnError)
	eventID := fs.String("event", "", "FanDuel event id")
	mode := fs.String("mode", "both", "popular, mga, or both")
	tab := fs.String("tab", "popular", "FanDuel event-page tab")
	outDir := fs.String("out", filepath.Join("out", "fd-event"), "output directory")
	ak := fs.String("ak", "", "FanDuel application key")
	timeout := fs.Duration("timeout", 25*time.Second, "request timeout")
	withPrices := fs.Bool("prices", true, "fetch SMP prices for discovered market ids")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *eventID == "" {
		return errors.New("event id is required")
	}
	if *mode != "popular" && *mode != "mga" && *mode != "both" {
		return fmt.Errorf("unknown mode %q (want popular, mga or both)", *mode)
	}

	opts := fd.EventOptions{AK: *ak, Timeout: *timeout}
	var popular, mga *fd.EventResult
	var err error
	if *mode == "popular" || *mode == "both" {
		popular, err = fd.FetchEventPage(context.Background(), *eventID, *tab, opts)
		if err != nil {
			return fmt.Errorf("popular: %w", err)
		}
		if err := writeFile(filepath.Join(*outDir, "popular.json"), popular.Raw); err != nil {
			return err
		}
	}
	if *mode == "mga" || *mode == "both" {
		mga, err = fd.FetchMGAEvent(context.Background(), *eventID, opts)
		if err != nil {
			return fmt.Errorf("mga: %w", err)
		}
		if err := writeFile(filepath.Join(*outDir, "mga.json"), mga.Raw); err != nil {
			return err
		}
	}
	boards := make([]*fd.Board, 0, 2)
	if popular != nil {
		boards = append(boards, popular.Board)
	}
	if mga != nil {
		boards = append(boards, mga.Board)
	}
	board := fd.MergeEventBoards(boards...)
	if len(board.Attachments.Events) == 0 {
		return errors.New("event discovery returned no events")
	}

	priced := 0
	if *withPrices {
		ids := make([]string, 0, len(board.Attachments.Markets))
		for id := range board.Attachments.Markets {
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
				return fmt.Errorf("SMP prices: %w", err)
			}
			prices = append(prices, res.Markets...)
		}
		raw, _ := jsonx.Marshal(prices)
		if err := writeFile(filepath.Join(*outDir, "prices.json"), raw); err != nil {
			return err
		}
		priced = fd.ApplyPrices(board, prices)
	}

	rawBoard, err := jsonx.Marshal(board)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(*outDir, "board.json"), rawBoard); err != nil {
		return err
	}
	fmt.Printf("event=%s sources=%s events=%d markets=%d pricedMarkets=%d out=%s\n",
		*eventID, sources(popular, mga), len(board.Attachments.Events),
		len(board.Attachments.Markets), priced, *outDir)
	return nil
}

func sources(popular, mga *fd.EventResult) string {
	out := ""
	if popular != nil {
		out += "popular"
	}
	if mga != nil {
		if out != "" {
			out += "+"
		}
		out += "mga"
	}
	return out
}
