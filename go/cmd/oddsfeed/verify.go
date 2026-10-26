package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"sportsbookfeed/internal/dk"
	"sportsbookfeed/internal/fd"
	"sportsbookfeed/internal/jsonx"
)

// verifyReport is the one-line acceptance report from `oddsfeed verify`.
type verifyReport struct {
	DraftKings *dkVerify `json:"draftkings"`
	FanDuel    *fdVerify `json:"fanduel"`
	OK         bool      `json:"ok"`
}

type dkVerify struct {
	Snapshot      *dbCounts `json:"snapshot,omitempty"`
	SnapshotError string    `json:"snapshotError,omitempty"`
	WS            *wsCheck  `json:"ws,omitempty"`
}

type fdVerify struct {
	Discovery      *dbCounts  `json:"discovery,omitempty"`
	DiscoveryError string     `json:"discoveryError,omitempty"`
	GraphQL        *smpCounts `json:"graphql,omitempty"`
	GraphQLError   string     `json:"graphqlError,omitempty"`
	SMP            *smpCounts `json:"smp,omitempty"`
	SMPError       string     `json:"smpError,omitempty"`
	PriceParity    *parity    `json:"priceParity,omitempty"`
	WS             *wsCheck   `json:"ws,omitempty"`
}

type dbCounts struct {
	Events     int `json:"events"`
	Markets    int `json:"markets"`
	Selections int `json:"selections"`
}

type smpCounts struct {
	Markets int `json:"markets"`
	Runners int `json:"runners"`
}

type parity struct {
	MarketsCompared int      `json:"marketsCompared"`
	MarketsMatching int      `json:"marketsMatching"`
	Mismatches      []string `json:"mismatches,omitempty"`
}

type wsCheck struct {
	Seconds    int            `json:"seconds"`
	Received   int            `json:"received"`
	Events     map[string]int `json:"events"`
	Updates    int            `json:"updates,omitempty"`
	DataFrames int            `json:"dataFrames,omitempty"`
	Ack        bool           `json:"ack,omitempty"`
	Error      string         `json:"error,omitempty"`
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	seconds := fs.Int("seconds", envInt("CAPTURE_MS", 15000)/1000, "WebSocket observation window in seconds")
	skipWS := fs.Bool("skip-ws", false, "only check REST endpoints")
	bundlePath := fs.String("bundle", defaultBundlePath(), "FanDuel main bundle")
	if err := fs.Parse(args); err != nil {
		return err
	}

	report := &verifyReport{DraftKings: &dkVerify{}, FanDuel: &fdVerify{}}
	ok := true

	// DraftKings REST snapshot.
	snap, err := dk.FetchSnapshot(context.Background(), dk.FetchOptions{})
	if err != nil {
		report.DraftKings.SnapshotError = err.Error()
		ok = false
	} else {
		events, markets, selections := snap.Counts()
		report.DraftKings.Snapshot = &dbCounts{Events: events, Markets: markets, Selections: selections}
		if err := writeFile(filepath.Join("out", "dk-verify-snapshot.json"), snap.Raw); err != nil {
			return err
		}
	}

	if !*skipWS {
		check := probeDKWS(*seconds)
		report.DraftKings.WS = check
		if check.Error != "" {
			ok = false
		}
	}

	// FanDuel discovery, then prices for the first three main markets.
	board, err := fd.Discover(context.Background(), fd.BoardOptions{})
	if err != nil {
		report.FanDuel.DiscoveryError = err.Error()
		ok = false
	} else {
		rows, rowsErr := board.Board.Rows()
		if rowsErr != nil {
			report.FanDuel.DiscoveryError = rowsErr.Error()
			ok = false
		} else {
			games := map[string]struct{}{}
			markets := map[string]struct{}{}
			for _, r := range rows {
				games[r.EventID] = struct{}{}
				markets[r.MarketID] = struct{}{}
			}
			report.FanDuel.Discovery = &dbCounts{Events: len(games), Markets: len(markets), Selections: len(rows)}
			if err := writeFile(filepath.Join("out", "fd-verify-board.json"), board.Raw); err != nil {
				return err
			}
		}
	}

	bundle, err := fd.LoadBundle(*bundlePath)
	if err != nil {
		report.FanDuel.GraphQLError = err.Error()
		ok = false
	}

	var ids []string
	if board != nil {
		ids = mainMarketIDs(board.Board, 3)
	}
	if len(ids) > 0 && bundle != nil {
		gql, gqlErr := fd.FetchMarketPrices(context.Background(), fd.GraphQLOptions{
			Authorization: bundle.AppSyncClientID,
		}, bundle.SnapshotQuery, ids)
		smp, smpErr := fd.FetchMarketPricesSMP(context.Background(), fd.SMPOptions{}, ids)
		if gqlErr != nil {
			report.FanDuel.GraphQLError = gqlErr.Error()
			ok = false
		} else {
			report.FanDuel.GraphQL = marketCounts(gql.Markets)
		}
		if smpErr != nil {
			report.FanDuel.SMPError = smpErr.Error()
			ok = false
		} else {
			report.FanDuel.SMP = marketCounts(smp.Markets)
		}
		if gqlErr == nil && smpErr == nil {
			p := compareMarketPrices(gql.Markets, smp.Markets)
			report.FanDuel.PriceParity = p
			if p.MarketsMatching < p.MarketsCompared {
				ok = false
			}
		}

		if !*skipWS {
			check := probeFDWS(bundle, ids, *seconds)
			report.FanDuel.WS = check
			if check.Error != "" {
				ok = false
			}
		}
	}

	report.OK = ok
	line, err := jsonx.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Println(string(line))
	if !ok {
		return errors.New("verify: one or more checks failed")
	}
	return nil
}

func marketCounts(markets []fd.Market) *smpCounts {
	runners := 0
	for i := range markets {
		runners += len(markets[i].AllRunners())
	}
	return &smpCounts{Markets: len(markets), Runners: runners}
}

func probeDKWS(seconds int) *wsCheck {
	check := &wsCheck{Seconds: seconds, Events: map[string]int{}}
	ws, err := dk.Dial(context.Background(), dk.WSOptions{
		Header: http.Header{"User-Agent": {dk.DefaultUserAgent}},
	})
	if err != nil {
		check.Error = err.Error()
		return check
	}
	defer ws.Close()
	if err := ws.SendJSON(dk.NFLSubscribeMessage()); err != nil {
		check.Error = err.Error()
		return check
	}
	timer := time.AfterFunc(time.Duration(seconds)*time.Second, func() { _ = ws.Close() })
	defer timer.Stop()
	for {
		raw, err := ws.Read()
		if err != nil {
			if check.Received == 0 {
				check.Error = err.Error()
			}
			break
		}
		check.Received++
		msg, err := jsonx.DecodeObject(raw)
		if err != nil {
			continue
		}
		if event := jsonx.Str(msg, "event"); event != "" {
			count(check.Events, event)
			if event == "update" {
				check.Updates++
			}
		}
	}
	return check
}

func probeFDWS(bundle *fd.Bundle, ids []string, seconds int) *wsCheck {
	check := &wsCheck{Seconds: seconds, Events: map[string]int{}}
	auth := bundle.Auth()
	connectURL, err := fd.RealtimeURL(auth)
	if err != nil {
		check.Error = err.Error()
		return check
	}
	rt, err := fd.DialRealtime(context.Background(), connectURL, auth, 15*time.Second)
	if err != nil {
		check.Error = err.Error()
		return check
	}
	defer rt.Close()
	if err := rt.SendInit(); err != nil {
		check.Error = err.Error()
		return check
	}
	timer := time.AfterFunc(time.Duration(seconds)*time.Second, func() { _ = rt.Close() })
	defer timer.Stop()
	for {
		raw, err := rt.Read()
		if err != nil {
			if check.Error == "" && !check.Ack {
				check.Error = "no connection_ack: " + err.Error()
			}
			break
		}
		check.Received++
		frame, err := fd.ParseFrame(raw)
		if err != nil {
			continue
		}
		count(check.Events, frame.Type)
		switch frame.Type {
		case "connection_ack":
			check.Ack = true
			if err := rt.Start("verify", bundle.SubscriptionQuery, ids); err != nil {
				check.Error = err.Error()
				return check
			}
		case "data":
			if _, err := frame.Price(); err == nil {
				check.DataFrames++
			}
		}
	}
	if !check.Ack {
		check.Error = "no connection_ack within window"
	}
	return check
}

// compareMarketPrices compares market status and runner status/American odds
// between the GraphQL and SMP views. Decimal precision is intentionally not
// compared: the realtime socket rounds it, and the Node sanity check excludes it.
func compareMarketPrices(graphql, smp []fd.Market) *parity {
	byID := map[string]*fd.Market{}
	for i := range smp {
		byID[smp[i].MarketID] = &smp[i]
	}
	p := &parity{}
	for i := range graphql {
		g := &graphql[i]
		s, ok := byID[g.MarketID]
		if !ok {
			continue
		}
		p.MarketsCompared++
		if sameMarket(g, s) {
			p.MarketsMatching++
		} else if len(p.Mismatches) < 5 {
			p.Mismatches = append(p.Mismatches, g.MarketID)
		}
	}
	sort.Strings(p.Mismatches)
	return p
}

func sameMarket(a, b *fd.Market) bool {
	if a.MarketStatus != b.MarketStatus || a.IsInPlay() != b.IsInPlay() {
		return false
	}
	ar, br := a.AllRunners(), b.AllRunners()
	if len(ar) != len(br) {
		return false
	}
	bySel := map[string]fd.Runner{}
	for _, r := range br {
		bySel[r.SelectionID.String()] = r
	}
	for _, r := range ar {
		other, ok := bySel[r.SelectionID.String()]
		if !ok || r.RunnerStatus != other.RunnerStatus {
			return false
		}
		if !sameAmerican(r.WinRunnerOdds.American(), other.WinRunnerOdds.American()) {
			return false
		}
		if !sameFloat(r.Handicap, other.Handicap) {
			return false
		}
	}
	return true
}

func sameAmerican(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
