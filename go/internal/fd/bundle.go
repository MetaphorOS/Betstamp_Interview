package fd

import (
	"fmt"
	"os"
	"regexp"
)

// Bundle is the rotating public configuration extracted from the FanDuel main
// JavaScript bundle: the AppSync client id and the two GraphQL documents.
//
// probe-ws.cjs and probe-fd-rest.cjs read these instead of hard-coding a
// private credential. The same extraction is used here so a bundle refresh is
// enough to survive a frontend deploy.
type Bundle struct {
	Path              string
	AppSyncClientID   string
	SubscriptionQuery string
	SnapshotQuery     string
}

var (
	appSyncClientIDRE = regexp.MustCompile(`aws_appsync_clientId:"([^"]+)"`)
	subscriptionRE    = regexp.MustCompile(`(?s)subscription OnUpdateMarketPrice\(\$ids: \[String!\]!\) \{.*?\n    \}`)
	snapshotRE        = regexp.MustCompile(`(?s)query GetMarketPrices\(\$ids: \[String\]!\) \{.*?\n    \}`)
)

// LoadBundle reads and parses a captured main bundle.
func LoadBundle(path string) (*Bundle, error) {
	if path == "" {
		path = DefaultBundlePath
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read FanDuel bundle %s: %w", path, err)
	}
	b, err := ParseBundle(string(source))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	b.Path = path
	return b, nil
}

// ParseBundle extracts the client id and both GraphQL documents.
func ParseBundle(source string) (*Bundle, error) {
	b := &Bundle{}
	if m := appSyncClientIDRE.FindStringSubmatch(source); len(m) == 2 {
		b.AppSyncClientID = m[1]
	} else {
		return nil, fmt.Errorf("missing aws_appsync_clientId")
	}
	if q := subscriptionRE.FindString(source); q != "" {
		b.SubscriptionQuery = q
	} else {
		return nil, fmt.Errorf("missing subscription OnUpdateMarketPrice")
	}
	if q := snapshotRE.FindString(source); q != "" {
		b.SnapshotQuery = q
	} else {
		return nil, fmt.Errorf("missing query GetMarketPrices")
	}
	return b, nil
}

// Auth returns the public authorization object the frontend publishes.
func (b *Bundle) Auth() Auth {
	return Auth{Authorization: b.AppSyncClientID, Host: Host}
}
