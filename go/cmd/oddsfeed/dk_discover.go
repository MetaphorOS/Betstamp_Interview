package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"sportsbookfeed/internal/dk"
	"sportsbookfeed/internal/jsonx"
)

const dkSportsBase = "https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1"

type dkSource struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

func cmdDKdiscover(args []string) error {
	fs := flag.NewFlagSet("dk-discover", flag.ContinueOnError)
	sport := fs.String("sport", "nba", "nba or nfl")
	outDir := fs.String("out", filepath.Join("out", "dk-nba-discover"), "output directory")
	props := fs.String("props", "", "explicit prop subcategory ids (overrides auto-discovery)")
	noProps := fs.Bool("no-props", false, "skip player-prop subcategories")
	timeout := fs.Duration("timeout", 20*time.Second, "per-request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base, league := dkBaseSources(*sport)
	if len(base) == 0 {
		return errors.New("sport must be nba or nfl")
	}

	// Main snapshot is required first: it carries the category/subcategory
	// catalog used to select props automatically.
	mainRes, err := dk.FetchSnapshot(context.Background(), dk.FetchOptions{URL: base[0].URL, Timeout: *timeout})
	if err != nil {
		return fmt.Errorf("%s: %w", base[0].Label, err)
	}
	if err := writeFile(filepath.Join(*outDir, base[0].Label+".json"), mainRes.Raw); err != nil {
		return err
	}
	datas := []*dk.Data{mainRes.Data}

	subs := dk.AutoPropSubcategories(*sport, mainRes.Data)
	if explicit := splitIDs(*props); len(explicit) > 0 {
		subs = nil
		for _, id := range explicit {
			if s, ok := findSubcategory(mainRes.Data, id); ok {
				subs = append(subs, s)
			} else {
				subs = append(subs, dk.Subcategory{ID: dk.ID(id)})
			}
		}
	}
	if *noProps {
		subs = nil
	}

	sources := append([]dkSource{}, base...)
	for _, s := range subs {
		category := s.CategoryID.String()
		if category == "" {
			category = defaultPropCategory(*sport)
		}
		sources = append(sources, dkSource{
			Label: "prop-" + s.ID.String(),
			URL:   fmt.Sprintf("%s/leagues/%s/categories/%s/subcategories/%s", dkSportsBase, league, category, s.ID.String()),
		})
	}

	for _, src := range sources[1:] {
		res, err := dk.FetchSnapshot(context.Background(), dk.FetchOptions{URL: src.URL, Timeout: *timeout})
		if err != nil {
			return fmt.Errorf("%s: %w", src.Label, err)
		}
		if err := writeFile(filepath.Join(*outDir, src.Label+".json"), res.Raw); err != nil {
			return err
		}
		datas = append(datas, res.Data)
	}

	merged := dk.MergeData(datas...)
	board := struct {
		Events     []dk.Event     `json:"events"`
		Markets    []dk.Market    `json:"markets"`
		Selections []dk.Selection `json:"selections"`
	}{merged.Events, merged.Markets, merged.Selections}
	raw, err := jsonx.Marshal(board)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(*outDir, "board.json"), raw); err != nil {
		return err
	}
	propMeta := make([]map[string]string, 0, len(subs))
	for _, s := range subs {
		propMeta = append(propMeta, map[string]string{"id": s.ID.String(), "categoryId": s.CategoryID.String(), "name": s.Name})
	}
	meta, _ := jsonx.Marshal(map[string]any{
		"sport": *sport, "sources": sources, "autoProps": propMeta,
		"events": len(merged.Events), "markets": len(merged.Markets), "selections": len(merged.Selections),
	})
	_ = writeFile(filepath.Join(*outDir, "meta.json"), meta)
	fmt.Printf("sport=%s sources=%d props=%d events=%d markets=%d selections=%d out=%s\n",
		*sport, len(sources), len(subs), len(merged.Events), len(merged.Markets), len(merged.Selections), *outDir)
	return nil
}

func dkBaseSources(sport string) ([]dkSource, string) {
	switch strings.ToLower(strings.TrimSpace(sport)) {
	case "nba":
		return []dkSource{
			{"main", dkSportsBase + "/leagues/42648"},
			{"alt-spread", dkSportsBase + "/leagues/42648/categories/487/subcategories/13202"},
			{"alt-total", dkSportsBase + "/leagues/42648/categories/487/subcategories/13201"},
		}, "42648"
	case "nfl":
		return []dkSource{
			{"main", dkSportsBase + "/leagues/88808"},
			{"alt-spread", dkSportsBase + "/leagues/88808/categories/492/subcategories/13195"},
			{"alt-total", dkSportsBase + "/leagues/88808/categories/492/subcategories/13196"},
		}, "88808"
	}
	return nil, ""
}

func defaultPropCategory(sport string) string {
	if strings.EqualFold(sport, "nfl") {
		return "492"
	}
	return "487"
}

func findSubcategory(data *dk.Data, id string) (dk.Subcategory, bool) {
	for _, s := range data.Subcategories {
		if s.ID.String() == id {
			return s, true
		}
	}
	return dk.Subcategory{}, false
}
