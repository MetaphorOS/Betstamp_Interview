package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"

	"sportsbookfeed/internal/clock"
	"sportsbookfeed/internal/dk"
	"sportsbookfeed/internal/jsonx"
)

// snapshotMeta matches the JSON printed by dk-http.cjs.
type snapshotMeta struct {
	ReceivedMonoMs float64 `json:"receivedMonoMs"`
	ReceivedAt     int64   `json:"receivedAt"`
	ElapsedMs      float64 `json:"elapsedMs"`
	CacheControl   string  `json:"cacheControl"`
	Age            *string `json:"age"`
	Events         int     `json:"events"`
	Markets        int     `json:"markets"`
	Selections     int     `json:"selections"`
}

func cmdDKsnapshot(args []string) error {
	fs := flag.NewFlagSet("dk-snapshot", flag.ContinueOnError)
	url := fs.String("url", "", "snapshot URL (default: legacy dkcaon NFL league route)")
	out := fs.String("out", "", "write the raw snapshot JSON to this file")
	timeout := fs.Duration("timeout", 15*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	res, err := dk.FetchSnapshot(context.Background(), dk.FetchOptions{URL: *url, Timeout: *timeout})
	if err != nil {
		return err
	}
	if *out != "" {
		if err := writeFile(*out, res.Raw); err != nil {
			return err
		}
	}
	events, markets, selections := res.Counts()
	line, err := jsonx.Marshal(snapshotMeta{
		ReceivedMonoMs: res.ReceivedMonoMs,
		ReceivedAt:     res.ReceivedAt,
		ElapsedMs:      res.ElapsedMs,
		CacheControl:   res.CacheControl,
		Age:            res.Age,
		Events:         events,
		Markets:        markets,
		Selections:     selections,
	})
	if err != nil {
		return err
	}
	fmt.Println(string(line))
	return nil
}

func cmdDKws(args []string) error {
	fs := flag.NewFlagSet("dk-ws", flag.ContinueOnError)
	url := fs.String("url", "", "WebSocket URL (default depends on -mode)")
	mode := fs.String("mode", "probe", "probe (hard-coded NFL filter) or partials (subscriptionPartials)")
	seconds := fs.Int("seconds", envInt("CAPTURE_MS", 45000)/1000, "observation window in seconds")
	logPath := fs.String("log", filepath.Join("out", "dk-ws.jsonl"), "JSONL capture file")
	snapshotURL := fs.String("snapshot-url", "", "snapshot URL used to bootstrap subscriptionPartials")
	bootstrapOut := fs.String("bootstrap-out", "", "write the bootstrap snapshot to this file")
	feature := fs.String("feature", "leagueSubcategory", "clientMetadata.feature for partials mode")
	version := fs.String("version", "2643.4.1.10", "clientMetadata X-Client-Version for partials mode")
	userAgent := fs.String("user-agent", "", "User-Agent header (default: measured Chrome UA, or DK_USER_AGENT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *seconds <= 0 {
		return errors.New("seconds must be positive")
	}

	var messages []map[string]any
	var bootstrap []byte
	if *mode == "partials" {
		snap, err := dk.FetchSnapshot(context.Background(), dk.FetchOptions{URL: *snapshotURL, UserAgent: *userAgent})
		if err != nil {
			return fmt.Errorf("bootstrap snapshot: %w", err)
		}
		if *bootstrapOut != "" {
			if err := writeFile(*bootstrapOut, snap.Raw); err != nil {
				return err
			}
		}
		bootstrap = snap.Raw
		partials := snap.Data.PartialList()
		if len(partials) == 0 {
			return dk.ErrNoPartials
		}
		messages = dk.PartialsSubscribeMessages(partials, *feature, *version)
	} else if *mode == "probe" {
		messages = []map[string]any{dk.NFLSubscribeMessage()}
	} else {
		return fmt.Errorf("unknown -mode %q (want probe or partials)", *mode)
	}

	logger, err := newJSONLLogger(*logPath, 3)
	if err != nil {
		return err
	}
	defer logger.Close()

	ua := *userAgent
	if ua == "" {
		ua = dk.DefaultUserAgent
	}
	wsURL := *url
	if wsURL == "" {
		if *mode == "partials" {
			wsURL = dk.DefaultWSURLCompare
		} else {
			wsURL = dk.DefaultWSURL
		}
	}
	ws, err := dk.Dial(context.Background(), dk.WSOptions{
		URL:     wsURL,
		Header:  http.Header{"User-Agent": {ua}},
		Timeout: 15 * time.Second,
	})
	if err != nil {
		return err
	}

	logger.log(logRecord{At: clock.NowISO(), Kind: "open"})
	for _, msg := range messages {
		logger.log(logRecord{At: clock.NowISO(), Kind: "sent", Payload: msg})
		if err := ws.SendJSON(msg); err != nil {
			return fmt.Errorf("subscribe: %w", err)
		}
	}

	sum := wsSummary{Book: "dk", Mode: *mode, Seconds: *seconds, Log: *logPath, Events: map[string]int{}}
	cache := dk.NewCache()
	if bootstrap != nil {
		if err := cache.SeedRaw(bootstrap); err != nil {
			return fmt.Errorf("seed cache: %w", err)
		}
	}
	timer := time.AfterFunc(time.Duration(*seconds)*time.Second, func() { _ = ws.Close() })
	defer timer.Stop()

	for {
		raw, readErr := ws.Read()
		if readErr != nil {
			logClose(logger, readErr)
			break
		}
		sum.Received++
		sum.Bytes += len(raw)
		logger.log(logRecord{At: clock.NowISO(), Kind: "received", Payload: string(raw)})
		msg, parseErr := jsonx.DecodeObject(raw)
		if parseErr != nil {
			continue
		}
		if event := jsonx.Str(msg, "event"); event != "" {
			count(sum.Events, event)
		}
		if *mode == "partials" {
			if stats, ok := cache.ApplyMessage(msg); ok {
				sum.Updates++
				sum.UnknownChanges += stats.Unknown
			}
		}
	}
	_ = ws.Close()

	line, err := jsonx.Marshal(sum)
	if err != nil {
		return err
	}
	fmt.Println(string(line))
	return nil
}

// wsSummary is the final one-line report for a WebSocket probe.
type wsSummary struct {
	Book            string         `json:"book"`
	Mode            string         `json:"mode,omitempty"`
	Subscription    string         `json:"subscription,omitempty"`
	Markets         int            `json:"markets,omitempty"`
	Seconds         int            `json:"seconds"`
	Received        int            `json:"received"`
	Bytes           int            `json:"bytes"`
	Updates         int            `json:"updates,omitempty"`
	UnknownChanges  int            `json:"unknownChanges,omitempty"`
	Events          map[string]int `json:"events,omitempty"`
	DataFrames      int            `json:"dataFrames,omitempty"`
	MarketsWithData int            `json:"marketsWithData,omitempty"`
	Log             string         `json:"log"`
}

// logClose records a close frame or a local timeout close.
func logClose(logger *jsonlLogger, err error) {
	code := 1000
	reason := ""
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		code = closeErr.Code
		reason = closeErr.Text
	}
	logger.log(logRecord{At: clock.NowISO(), Kind: "close", Code: code, Reason: reason})
}

func writeFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}
