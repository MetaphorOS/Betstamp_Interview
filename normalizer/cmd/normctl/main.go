// Command normctl replays captured sportsbook payloads through the normalization
// framework and writes a measurement report.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sportsbook-normalizer/internal/adapters"
	"sportsbook-normalizer/internal/model"
	"sportsbook-normalizer/internal/report"
)

func main() {
	root := flag.String("root", ".", "repository root")
	pregameDK := flag.String("pregame-dk", "out/soak-1h/dk/rest/snapshot-0071-20261025T165528Z.json", "pregame DraftKings snapshot")
	pregameFD := flag.String("pregame-fd", "out/soak-1h/fd/rest/discovery-0008-20261025T165528Z.json", "pregame FanDuel board")
	liveDK := flag.String("live-dk", "out/soak-1h/dk/rest/snapshot-0111-20261025T171528Z.json", "live DraftKings snapshot")
	liveFD := flag.String("live-fd", "out/soak-1h/fd/rest/discovery-0012-20261025T171528Z.json", "live FanDuel board")
	fdNFL := flag.String("fd-nfl", "out/soak-1h/fd/rest/discovery-0008-20261025T165528Z.json", "FanDuel NFL board for taxonomy")
	fdNBA := flag.String("fd-nba", "out/norm/fd-nba-now.json", "FanDuel NBA board")
	fdNHL := flag.String("fd-nhl", "out/norm/fd-nhl.json", "FanDuel NHL board")
	dkNBA := flag.String("dk-nba", "out/norm/dk-nba.json", "DraftKings NBA board")
	dkPartial := flag.String("dk-partial", "captures/dk-partial-cache.json", "DraftKings broad partial cache")
	pmNBA := flag.String("pm-nba", "out/pm-nba-events.json", "Polymarket NBA Gamma events")
	pmNFL := flag.String("pm-nfl", "out/pm-nfl-events.json", "Polymarket NFL Gamma events")
	fdEvent := flag.String("fd-event", "out/fd-event-lakers/board.json", "FanDuel event-level board from fd-event")
	fdEventSport := flag.String("fd-event-sport", "nba", "sport for the fd-event board")
	outDir := flag.String("out", "out/norm", "output directory")
	dump := flag.Bool("dump", false, "write normalized quotes for each pair fixture")
	flag.Parse()

	doc := report.Document{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	path := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(*root, p)
	}
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	type pairFixture struct {
		sport string
		dk    string
		fd    string
		label string
	}
	eventDK := *dkNBA
	if strings.EqualFold(*fdEventSport, "nfl") {
		eventDK = *pregameDK
	}
	fixtures := []pairFixture{
		{"nfl", *pregameDK, *pregameFD, "pregame"},
		{"nfl", *liveDK, *liveFD, "live"},
		{"nba", *dkNBA, *fdNBA, "nba"},
		{*fdEventSport, eventDK, *fdEvent, "fd-event"},
	}
	for _, f := range fixtures {
		dkPath, fdPath := path(f.dk), path(f.fd)
		if !exists(dkPath) || !exists(fdPath) {
			doc.Notes = append(doc.Notes, fmt.Sprintf("%s pair skipped: missing %s or %s", f.label, f.dk, f.fd))
			continue
		}
		dk, err := adapters.DK(dkPath, f.sport)
		if err != nil {
			doc.Notes = append(doc.Notes, fmt.Sprintf("%s DK parse failed: %v", f.label, err))
			continue
		}
		fd, err := adapters.FD(fdPath, f.sport)
		if err != nil {
			doc.Notes = append(doc.Notes, fmt.Sprintf("%s FD parse failed: %v", f.label, err))
			continue
		}
		doc.Pairs = append(doc.Pairs, report.BuildPair(f.sport, f.dk, f.fd, dk, fd))
		if *dump {
			for _, side := range []struct {
				name string
				data interface{}
			}{{"dk", dk}, {"fd", fd}} {
				b, err := json.MarshalIndent(side.data, "", "  ")
				if err != nil {
					doc.Notes = append(doc.Notes, fmt.Sprintf("%s %s dump failed: %v", f.label, side.name, err))
					continue
				}
				_ = os.WriteFile(filepath.Join(*outDir, f.label+"-"+side.name+"-quotes.json"), b, 0o644)
			}
		}
	}

	taxFixtures := []struct {
		book, sport, file string
	}{
		{"fanduel", "nfl", *fdNFL},
		{"fanduel", "nba", *fdNBA},
		{"fanduel", "nhl", *fdNHL},
		{"draftkings", "nba", *dkNBA},
		{"draftkings", "nfl", *dkPartial},
		{"polymarket", "nba", *pmNBA},
		{"polymarket", "nfl", *pmNFL},
		{"fanduel-event", *fdEventSport, *fdEvent},
	}
	for _, t := range taxFixtures {
		p := path(t.file)
		if !exists(p) {
			doc.Notes = append(doc.Notes, fmt.Sprintf("taxonomy skipped: %s/%s missing %s", t.book, t.sport, t.file))
			continue
		}
		var data *model.BookData
		var err error
		if t.book == "fanduel" || t.book == "fanduel-event" {
			data, err = adapters.FD(p, t.sport)
		} else if t.book == "polymarket" {
			data, err = adapters.PM(p, t.sport)
		} else {
			data, err = adapters.DK(p, t.sport)
		}
		if err != nil {
			doc.Notes = append(doc.Notes, fmt.Sprintf("taxonomy %s/%s parse failed: %v", t.book, t.sport, err))
			continue
		}
		doc.Taxonomies = append(doc.Taxonomies, report.BuildTaxonomy(t.book, t.sport, t.file, data))
		if *dump {
			b, err := json.MarshalIndent(data, "", "  ")
			if err == nil {
				_ = os.WriteFile(filepath.Join(*outDir, "tax-"+t.book+"-"+t.sport+".json"), b, 0o644)
			}
		}
	}

	doc.Notes = append(doc.Notes,
		"Only comparable quotes are cross-book matched. FanDuel props/futures/specials are classified but not compared until a settlement/player-identity rule pack exists.",
		"DraftKings partial caches can contain selections without marketId; those are counted as dangling and are not guessed into a market.",
		"Player subjects are provisional name slugs; a player alias registry is required for cross-book prop matching.",
		"Futures scope currently uses the competition/event name; season separation needs a registry.",
		"The NBA pair uses the current DK main board and FD league board; league boards expose very few NBA alt markets, so alt overlap is reported as coverage rather than price comparison.",
		"Polymarket rows are Gamma binary markets normalized from outcome-token prices. Main/alt identity is comparable, but prices are probabilities and must be de-vigged into fair probabilities before comparison with sportsbook American odds. Use pmctl for a PM-vs-DK overlap report.",
	)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	jsonPath := filepath.Join(*outDir, "report.json")
	mdPath := filepath.Join(*outDir, "REPORT.md")
	if err := doc.WriteJSON(jsonPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := doc.WriteMarkdown(mdPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s and %s\n", jsonPath, mdPath)
	for _, p := range doc.Pairs {
		fmt.Printf("%s pair: events %d/%d matched %d (max %.1f%%, union %.1f%%); conditional identity %.1f%%, exact-line %.1f%%; exact-line %d/%d; line mismatches %d; price same %d/%d\n",
			p.Sport, p.EventsA, p.EventsB, p.EventMatches, p.EventMatchRate, p.EventOverlapUnion,
			p.MatchedIdentityPct, p.MatchedLinePct, p.QuoteMatches, p.QuotesA, p.LineMismatches, p.Price.Exact, p.Price.Compared)
	}
	for _, t := range doc.Taxonomies {
		fmt.Printf("%s/%s: markets %d, quotes %d, classified %.1f%%, high-confidence %.1f%%, unknown %d, dangling %d\n",
			t.Book, t.Sport, t.Markets, t.Quotes, t.ClassifiedPct, t.HighConfidencePct, t.UnknownMarkets, t.DanglingSelections)
	}
}
