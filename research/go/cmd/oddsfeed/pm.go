package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sportsbookfeed/internal/clock"
	"sportsbookfeed/internal/jsonx"
	"sportsbookfeed/internal/pm"
)

type pmEventFile struct {
	Events []pm.Event `json:"events"`
}

func cmdPMdiscover(args []string) error {
	fs := flag.NewFlagSet("pm-discover", flag.ContinueOnError)
	sport := fs.String("sport", "nba", "nba or nfl")
	out := fs.String("out", filepath.Join("out", "pm-nba.json"), "raw Gamma output")
	limit := fs.Int("limit", 100, "max events")
	series := fs.String("series", "", "override Gamma series id")
	eventID := fs.String("event", "", "optional single Gamma event id")
	timeout := fs.Duration("timeout", 30*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	seriesID := *series
	var events []pm.Event
	var err error
	if *eventID != "" {
		ev, err := pm.FetchEvent(context.Background(), *eventID, pm.FetchOptions{Timeout: *timeout})
		if err != nil {
			return err
		}
		events = []pm.Event{*ev}
	} else {
		if seriesID == "" {
			s, err := pm.SeriesID(*sport)
			if err != nil {
				return err
			}
			seriesID = s
		}
		events, err = pm.FetchEvents(context.Background(), seriesID, *limit, pm.FetchOptions{Timeout: *timeout})
		if err != nil {
			return err
		}
	}
	raw, err := jsonx.Marshal(events)
	if err != nil {
		return err
	}
	if err := writeFile(*out, raw); err != nil {
		return err
	}
	types := map[string]int{}
	markets := 0
	for _, e := range events {
		for _, m := range e.Markets {
			markets++
			types[m.SportsMarketType]++
		}
	}
	fmt.Printf("events=%d markets=%d series=%s out=%s\n", len(events), markets, seriesID, *out)
	fmt.Printf("types=%s\n", jsonx.Truncate(mustJSON(types), 1200))
	return nil
}

func cmdPMws(args []string) error {
	fs := flag.NewFlagSet("pm-ws", flag.ContinueOnError)
	eventID := fs.String("event", "", "Gamma event id")
	sport := fs.String("sport", "", "nba or nfl (used to pick an event when -event is empty)")
	idsFlag := fs.String("ids", "", "explicit CLOB asset/token ids")
	typesFlag := fs.String("types", "main,alt,props", "main, alt, props, all")
	seconds := fs.Int("seconds", 45, "observation window in seconds")
	logPath := fs.String("log", filepath.Join("out", "pm-ws.jsonl"), "JSONL capture file")
	wsURL := fs.String("url", pm.WSURL, "WebSocket URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *seconds <= 0 {
		return errors.New("seconds must be positive")
	}

	var assets []string
	var selected []pm.Market
	if explicit := splitIDs(*idsFlag); len(explicit) > 0 {
		assets = explicit
	} else {
		ev, err := pmEventFor(*eventID, *sport)
		if err != nil {
			return err
		}
		selected = selectPMMarkets(ev.Markets, *typesFlag)
		for _, m := range selected {
			outs, err := m.Sides()
			if err != nil {
				continue
			}
			for _, o := range outs {
				if o.Token != "" {
					assets = append(assets, o.Token)
				}
			}
		}
		fmt.Printf("event=%s title=%q start=%s markets=%d assets=%d\n",
			ev.ID, ev.Title, ev.Start().UTC().Format(time.RFC3339), len(selected), len(assets))
	}
	if len(assets) == 0 {
		return errors.New("no Polymarket asset ids selected")
	}

	logger, err := newJSONLLogger(*logPath, 5)
	if err != nil {
		return err
	}
	defer logger.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ws, err := pm.Dial(ctx, *wsURL, 15*time.Second)
	if err != nil {
		return err
	}
	defer ws.Close()
	logger.log(logRecord{At: clock.NowISO(), Kind: "open"})
	if err := ws.Subscribe(assets); err != nil {
		return err
	}
	logger.log(logRecord{At: clock.NowISO(), Kind: "sent", Payload: map[string]any{
		"type": "market", "assets_ids": len(assets),
	}})

	ping := time.NewTicker(10 * time.Second)
	defer ping.Stop()
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		for {
			select {
			case <-pingDone:
				return
			case <-ping.C:
				_ = ws.Ping()
			}
		}
	}()
	timer := time.AfterFunc(time.Duration(*seconds)*time.Second, func() { _ = ws.Close() })
	defer timer.Stop()

	summary := map[string]any{
		"book": "polymarket", "event": *eventID, "assets": len(assets),
		"markets": len(selected), "seconds": *seconds, "log": *logPath,
	}
	eventsByType := map[string]int{}
	assetsSeen := map[string]struct{}{}
	frames := 0
	for {
		raw, err := ws.Read()
		if err != nil {
			break
		}
		frames++
		logger.log(logRecord{At: clock.NowISO(), Kind: "received", Payload: jsonx.Truncate(string(raw), 1400)})
		f, err := pm.ParseFrame(raw)
		if err != nil {
			continue
		}
		count(eventsByType, f.EventType)
		if f.AssetID != "" {
			assetsSeen[f.AssetID] = struct{}{}
		}
		for _, pc := range f.PriceChanges {
			if pc.AssetID != "" {
				assetsSeen[pc.AssetID] = struct{}{}
			}
		}
	}
	summary["frames"] = frames
	summary["events"] = eventsByType
	summary["assetsWithData"] = len(assetsSeen)
	line, _ := jsonx.Marshal(summary)
	fmt.Println(string(line))
	return nil
}

func pmEventFor(eventID, sport string) (*pm.Event, error) {
	if eventID != "" {
		return pm.FetchEvent(context.Background(), eventID, pm.FetchOptions{})
	}
	s, err := pm.SeriesID(sport)
	if err != nil {
		return nil, err
	}
	events, err := pm.FetchEvents(context.Background(), s, 100, pm.FetchOptions{})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, errors.New("no open events returned")
	}
	return &events[0], nil
}

func selectPMMarkets(markets []pm.Market, typesFlag string) []pm.Market {
	want := map[string]bool{}
	for _, t := range strings.Split(typesFlag, ",") {
		want[strings.TrimSpace(strings.ToLower(t))] = true
	}
	all := want["all"]
	var out []pm.Market
	for _, m := range markets {
		isMain := m.SportsMarketType == "moneyline" || m.SportsMarketType == "spreads" || m.SportsMarketType == "totals"
		isAlt := pm.IsMainOrAltType(m.SportsMarketType) && !isMain
		isProp := pm.IsPropType(m.SportsMarketType)
		if all || (want["main"] && isMain) || (want["alt"] && isAlt) || (want["props"] && isProp) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func mustJSON(v any) string {
	b, _ := jsonx.Marshal(v)
	return string(b)
}

// keep os imported for future output handling and consistency with other cmds.
var _ = os.WriteFile
