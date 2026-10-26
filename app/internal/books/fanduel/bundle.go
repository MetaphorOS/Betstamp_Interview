package fanduel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sync"
	"time"
)

// Bundle is the rotating public configuration in FanDuel's frontend JS: the
// AppSync client id, the subscription document, and the app key (_ak) the
// REST boards require.
type Bundle struct {
	Source            string // URL or file path it came from
	ClientID          string
	AppKey            string // "" when not found; callers fall back
	SubscriptionQuery string
	SnapshotQuery     string
}

var (
	clientIDRE     = regexp.MustCompile(`aws_appsync_clientId:"([^"]+)"`)
	subscriptionRE = regexp.MustCompile(`(?s)subscription OnUpdateMarketPrice\(\$ids: \[String!\]!\) \{.*?\n    \}`)
	snapshotRE     = regexp.MustCompile(`(?s)query GetMarketPrices\(\$ids: \[String\]!\) \{.*?\n    \}`)
	mainJSRE       = regexp.MustCompile(`/static/js/main\.[0-9a-f]+\.js`)
	depsJSRE       = regexp.MustCompile(`/static/js/dependencies\.[0-9a-f]+\.js`)
	// The app key is a minified constant declared right after the
	// environment enum (...SB_STUBS_REMOTE="SB_STUBS_REMOTE";let g="<16>").
	// The variable name changes every build, so anchor on the enum, not it.
	appKeyRE = regexp.MustCompile(`"SB_STUBS_REMOTE";let [\w$]+="([A-Za-z0-9]{16})"`)
	// Looser fallback: the first 16-character literal shortly after the enum.
	appKeyNearRE = regexp.MustCompile(`SB_STUBS_REMOTE[^;]*;[^"]{0,40}"([A-Za-z0-9]{16})"`)
)

// parseBundle fills whatever parts the source contains.
func parseBundle(b *Bundle, source string) {
	if b.ClientID == "" {
		if m := clientIDRE.FindStringSubmatch(source); len(m) == 2 {
			b.ClientID = m[1]
		}
	}
	if b.SubscriptionQuery == "" {
		b.SubscriptionQuery = subscriptionRE.FindString(source)
	}
	if b.SnapshotQuery == "" {
		b.SnapshotQuery = snapshotRE.FindString(source)
	}
	if b.AppKey == "" {
		if m := appKeyRE.FindStringSubmatch(source); len(m) == 2 {
			b.AppKey = m[1]
		} else if m := appKeyNearRE.FindStringSubmatch(source); len(m) == 2 {
			b.AppKey = m[1]
		}
	}
}

func (b *Bundle) complete() bool { return b.ClientID != "" && b.SubscriptionQuery != "" }

// ParseBundle extracts the client id and GraphQL documents from one source.
func ParseBundle(source string) (*Bundle, error) {
	b := &Bundle{}
	parseBundle(b, source)
	if b.ClientID == "" {
		return nil, errors.New("fanduel: bundle missing aws_appsync_clientId")
	}
	if b.SubscriptionQuery == "" {
		return nil, errors.New("fanduel: bundle missing subscription OnUpdateMarketPrice")
	}
	return b, nil
}

const bundleMaxAge = 6 * time.Hour

// bundleProvider resolves the bundle: in-memory last-good, then a live fetch
// of the frontend, then the read-only captured file.
type bundleProvider struct {
	hc       *http.Client
	homeURL  string
	path     string
	ua       string
	mu       sync.Mutex
	cur      *Bundle
	gotAt    time.Time
	invalid  bool
	lastLive error
}

func newBundleProvider(hc *http.Client, cfg Config) *bundleProvider {
	return &bundleProvider{hc: hc, homeURL: cfg.HomeURL, path: cfg.BundlePath, ua: cfg.UserAgent}
}

// appKey is the current bundle's app key without fetching, or "".
func (p *bundleProvider) appKey() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil {
		return ""
	}
	return p.cur.AppKey
}

// invalidate forces a refresh before the next connect (after ErrAuth) or
// the next snapshot (after the boards reject the app key).
func (p *bundleProvider) invalidate() {
	p.mu.Lock()
	p.invalid = true
	p.mu.Unlock()
}

func (p *bundleProvider) get(ctx context.Context) (*Bundle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil && !p.invalid && time.Since(p.gotAt) < bundleMaxAge {
		return p.cur, nil
	}
	b, err := p.fetchLive(ctx)
	p.lastLive = err
	if err != nil {
		slog.Warn("fanduel bundle live fetch failed", "error", err)
		if p.cur != nil && !p.invalid {
			// Keep last-good; try again on the next connect.
			return p.cur, nil
		}
		b, err = p.readFile()
		if err != nil {
			return nil, fmt.Errorf("fanduel: no bundle (live: %v; file: %w)", p.lastLive, err)
		}
	}
	if p.cur == nil || p.cur.ClientID != b.ClientID || p.cur.AppKey != b.AppKey {
		old := &Bundle{}
		if p.cur != nil {
			old = p.cur
		}
		slog.Info("fanduel bundle loaded", "source", b.Source,
			"oldClientID", prefix(old.ClientID), "newClientID", prefix(b.ClientID),
			"oldAppKey", prefix(old.AppKey), "newAppKey", prefix(b.AppKey))
	}
	p.cur, p.gotAt, p.invalid = b, time.Now(), false
	return b, nil
}

func clientIDOf(b *Bundle) string {
	if b == nil {
		return ""
	}
	return b.ClientID
}

func prefix(s string) string {
	if len(s) > 6 {
		return s[:6] + "…"
	}
	return s
}

func (p *bundleProvider) readFile() (*Bundle, error) {
	if p.path == "" || p.path == "-" {
		return nil, errors.New("no bundle file configured (set FD_MAIN_JS)")
	}
	raw, err := os.ReadFile(p.path)
	if err != nil {
		return nil, err
	}
	b, err := ParseBundle(string(raw))
	if err != nil {
		return nil, err
	}
	b.Source = p.path
	return b, nil
}

// fetchLive reads the frontend HTML, finds the main bundle and parses it,
// falling back to the dependencies bundle for whatever main lacks.
func (p *bundleProvider) fetchLive(ctx context.Context) (*Bundle, error) {
	if p.homeURL == "" || p.homeURL == "-" {
		return nil, errors.New("live bundle fetch disabled")
	}
	html, err := p.getText(ctx, p.homeURL)
	if err != nil {
		return nil, fmt.Errorf("home page: %w", err)
	}
	mainPath := mainJSRE.FindString(html)
	if mainPath == "" {
		return nil, errors.New("home page has no main.<hash>.js (bot protection or layout change)")
	}
	mainURL := resolveRef(p.homeURL, mainPath)
	src, err := p.getText(ctx, mainURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", mainURL, err)
	}
	b := &Bundle{Source: mainURL}
	parseBundle(b, src)
	if !b.complete() || b.AppKey == "" {
		if dep := depsJSRE.FindString(html); dep != "" {
			if depSrc, err := p.getText(ctx, resolveRef(p.homeURL, dep)); err == nil {
				parseBundle(b, depSrc)
			}
		}
	}
	if !b.complete() {
		return nil, fmt.Errorf("%s: client id or subscription not found", mainURL)
	}
	return b, nil
}

func resolveRef(base, ref string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := u.Parse(ref)
	if err != nil {
		return ref
	}
	return r.String()
}

func (p *bundleProvider) getText(ctx context.Context, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", p.ua)
	req.Header.Set("Accept", "text/html,application/javascript,*/*")
	resp, err := p.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return string(raw), err
}
