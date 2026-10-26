// Command pmctl normalizes Polymarket Gamma events and measures overlap with a
// DraftKings or FanDuel board. Price comparison uses de-vigged fair
// probabilities and an explicit capture-time skew guard.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sportsbook-normalizer/internal/adapters"
	"sportsbook-normalizer/internal/identity"
	"sportsbook-normalizer/internal/model"
	"sportsbook-normalizer/internal/price"
)

type report struct {
	GeneratedAt  string            `json:"generatedAt"`
	Sport        string            `json:"sport"`
	PMFile       string            `json:"pmFile"`
	DKFile       string            `json:"dkFile,omitempty"`
	FDFile       string            `json:"fdFile,omitempty"`
	Times        map[string]string `json:"times,omitempty"`
	SkewSeconds  float64           `json:"skewSeconds"`
	Synchronized bool              `json:"synchronized"`
	PM           *model.BookData   `json:"polymarket"`
	DK           *model.BookData   `json:"draftkings,omitempty"`
	FD           *model.BookData   `json:"fanduel,omitempty"`
	Match        map[string]any    `json:"match,omitempty"`
	Notes        []string          `json:"notes,omitempty"`
}

func main() {
	pmPath := flag.String("pm", "out/pm-nba-events.json", "Polymarket Gamma event file")
	sport := flag.String("sport", "nba", "nba or nfl")
	dkPath := flag.String("dk", "", "optional DraftKings board to compare")
	fdPath := flag.String("fd", "", "optional FanDuel event board to compare")
	outDir := flag.String("out", "out/pm", "output directory")
	maxSkew := flag.Duration("max-skew", 2*time.Minute, "maximum capture-time skew")
	pmAt := flag.String("pm-at", "", "RFC3339 PM capture time (default: file mtime)")
	dkAt := flag.String("dk-at", "", "RFC3339 DK capture time (default: file mtime)")
	fdAt := flag.String("fd-at", "", "RFC3339 FD capture time (default: file mtime)")
	dump := flag.Bool("dump", false, "dump normalized Polymarket quotes")
	flag.Parse()

	pmData, err := adapters.PM(*pmPath, *sport)
	if err != nil {
		fmt.Fprintln(os.Stderr, "polymarket:", err)
		os.Exit(1)
	}
	doc := report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Sport:       *sport, PMFile: *pmPath, PM: pmData,
		Match: map[string]any{}, Times: map[string]string{},
	}

	fmt.Printf("polymarket/%s: events=%d gameEvents=%d markets=%d quotes=%d comparable=%d\n",
		*sport, pmData.Counts.Events, pmData.Counts.GameEvents, pmData.Counts.Markets,
		pmData.Counts.Quotes, pmData.Counts.ComparableQuotes)
	fmt.Printf("families=%s\n", jsonString(pmData.Counts.FamilyCounts))
	main, alt := 0, 0
	for _, q := range pmData.Quotes {
		if q.Comparable {
			if q.Alt {
				alt++
			} else {
				main++
			}
		}
	}
	fmt.Printf("comparable main/alt: %d/%d\n", main, alt)

	times := map[string]time.Time{}
	if t, ok := captureTime(*pmPath, *pmAt); ok {
		times["pm"] = t
		doc.Times["pm"] = t.Format(time.RFC3339)
	}
	if *dkPath != "" {
		dkData, err := adapters.DK(*dkPath, *sport)
		if err != nil {
			fmt.Fprintln(os.Stderr, "draftkings:", err)
			os.Exit(1)
		}
		doc.DKFile, doc.DK = *dkPath, dkData
		doc.Match["draftkings"] = compare(pmData, dkData, "draftkings")
		if t, ok := captureTime(*dkPath, *dkAt); ok {
			times["dk"] = t
			doc.Times["dk"] = t.Format(time.RFC3339)
		}
		fmt.Printf("draftkings/%s: events=%d markets=%d comparable=%d\n",
			*sport, dkData.Counts.Events, dkData.Counts.Markets, dkData.Counts.ComparableQuotes)
	}
	if *fdPath != "" {
		fdData, err := adapters.FD(*fdPath, *sport)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fanduel:", err)
			os.Exit(1)
		}
		doc.FDFile, doc.FD = *fdPath, fdData
		doc.Match["fanduel"] = compare(pmData, fdData, "fanduel")
		if t, ok := captureTime(*fdPath, *fdAt); ok {
			times["fd"] = t
			doc.Times["fd"] = t.Format(time.RFC3339)
		}
		fmt.Printf("fanduel/%s: events=%d markets=%d comparable=%d\n",
			*sport, fdData.Counts.Events, fdData.Counts.Markets, fdData.Counts.ComparableQuotes)
	}
	doc.SkewSeconds, doc.Synchronized = skew(times, *maxSkew)
	if !doc.Synchronized && len(times) > 1 {
		doc.Notes = append(doc.Notes, fmt.Sprintf("capture skew %.1fs exceeds %s; results are not synchronized", doc.SkewSeconds, *maxSkew))
		fmt.Printf("WARNING: capture skew %.1fs exceeds %s\n", doc.SkewSeconds, *maxSkew)
	}
	for name, m := range doc.Match {
		fmt.Printf("%s match: %s\n", name, jsonString(m))
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(filepath.Join(*outDir, "report.json"), b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *dump {
		qb, _ := json.MarshalIndent(pmData.Quotes, "", "  ")
		_ = os.WriteFile(filepath.Join(*outDir, "pm-quotes.json"), qb, 0o644)
	}
	fmt.Printf("wrote %s\n", filepath.Join(*outDir, "report.json"))
}

func compare(pmData, book *model.BookData, name string) map[string]any {
	pairs, _, _ := identity.MatchEvents(pmData.Events, book.Events, 15*time.Minute)
	pmQuotes := comparableMap(pmData)
	bookQuotes := comparableMap(book)
	exact := 0
	for k := range pmQuotes {
		if _, ok := bookQuotes[k]; ok {
			exact++
		}
	}
	pmIdentity := map[string]bool{}
	bookIdentity := map[string]bool{}
	for _, q := range pmQuotes {
		pmIdentity[model.IdentityKey(q)] = true
	}
	for _, q := range bookQuotes {
		bookIdentity[model.IdentityKey(q)] = true
	}
	both := 0
	for k := range pmIdentity {
		if bookIdentity[k] {
			both++
		}
	}
	stats := price.Compare(pmData, book)
	return map[string]any{
		"book":                 name,
		"eventMatches":         len(pairs),
		"pmEvents":             len(pmData.Events),
		"bookEvents":           len(book.Events),
		"pmComparableQuotes":   len(pmQuotes),
		"bookComparableQuotes": len(bookQuotes),
		"identityMatches":      both,
		"exactLineMatches":     exact,
		"price":                stats,
		"pmFamilies":           pmData.Counts.FamilyCounts,
		"bookFamilies":         book.Counts.FamilyCounts,
	}
}

func comparableMap(book *model.BookData) map[string]model.Quote {
	out := map[string]model.Quote{}
	for _, q := range book.Quotes {
		if q.Comparable {
			out[q.Key] = q
		}
	}
	return out
}

func captureTime(path, explicit string) (time.Time, bool) {
	if explicit != "" {
		t, err := time.Parse(time.RFC3339, explicit)
		if err == nil {
			return t.UTC(), true
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime().UTC(), true
}

func skew(times map[string]time.Time, max time.Duration) (float64, bool) {
	if len(times) < 2 {
		return 0, true
	}
	var min, maxT time.Time
	for _, t := range times {
		if min.IsZero() || t.Before(min) {
			min = t
		}
		if maxT.IsZero() || t.After(maxT) {
			maxT = t
		}
	}
	secs := maxT.Sub(min).Seconds()
	return secs, maxT.Sub(min) <= max
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
