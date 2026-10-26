// Package dk implements the DraftKings Ontario (dkcaon / CA-ON-SB) public
// snapshot REST client and JSON-RPC WebSocket subscription used by the
// JavaScript research probes (dk-http.cjs, probe-ws.cjs, compare-dk.cjs).
package dk

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	// DefaultUserAgent is the current-Chrome string isolated in DK-POLLING-FIX.md.
	// The Akamai edge rejects Go's default UA, the default Node UA and older
	// Chrome versions with HTTP 403. DK_USER_AGENT overrides it.
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

	// DefaultLeagueURL is the legacy league snapshot route from dk-http.cjs.
	DefaultLeagueURL = "https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808"

	// NFLLeagueID is the NFL league id used by every probe.
	NFLLeagueID = "88808"

	// DefaultWSURL is the public dkcaon JSON WebSocket endpoint used by
	// probe-ws.cjs (locale=en-US).
	DefaultWSURL = "wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en-US"

	// DefaultWSURLCompare is the endpoint compare-dk.cjs uses (locale=en).
	DefaultWSURLCompare = "wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en"
)

// ID is a string id that also tolerates a bare JSON number, because some
// DraftKings entities use numeric ids in one response and quoted ids in another.
type ID string

// UnmarshalJSON accepts "123" and 123.
func (id *ID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*id = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*id = ID(s)
		return nil
	}
	if !json.Valid(b) {
		return fmt.Errorf("dk: invalid id %q", b)
	}
	*id = ID(b)
	return nil
}

func (id ID) String() string { return string(id) }

// Participant is a team or competitor attached to an event or selection.
type Participant struct {
	ID        ID     `json:"id"`
	Name      string `json:"name"`
	VenueRole string `json:"venueRole"`
	Type      string `json:"type"`
	ShortName string `json:"-"`
}

// Event is the draft-kings event entity.
type Event struct {
	ID             ID            `json:"id"`
	Name           string        `json:"name"`
	StartEventDate string        `json:"startEventDate"`
	Status         string        `json:"status"`
	LeagueID       ID            `json:"leagueId"`
	SportID        ID            `json:"sportId"`
	Participants   []Participant `json:"participants"`
	Tags           []string      `json:"tags"`
	Subscription   string        `json:"subscriptionKey"`
}

// MarketType identifies the bet type, for example "Moneyline".
type MarketType struct {
	ID             ID     `json:"id"`
	BetOfferTypeID int    `json:"betOfferTypeId"`
	Name           string `json:"name"`
}

// Market is the draft-kings market entity.
type Market struct {
	ID              ID          `json:"id"`
	EventID         ID          `json:"eventId"`
	SportID         ID          `json:"sportId"`
	LeagueID        ID          `json:"leagueId"`
	Name            string      `json:"name"`
	SubcategoryID   ID          `json:"subcategoryId"`
	MarketType      *MarketType `json:"marketType"`
	Status          string      `json:"status,omitempty"`
	Tags            []string    `json:"tags"`
	Subscription    string      `json:"subscriptionKey"`
	DynamicMetadata any         `json:"dynamicMetadata"`
}

// DisplayOdds is the rendered price object. American is a string using the
// Unicode minus sign (U+2212), exactly as DraftKings sends it.
type DisplayOdds struct {
	American   string `json:"american"`
	Decimal    string `json:"decimal"`
	Fractional string `json:"fractional"`
	Percentage string `json:"percentage"`
	Multiplier string `json:"multiplier"`
}

// Selection is the draft-kings selection (runner) entity.
type Selection struct {
	ID           ID            `json:"id"`
	MarketID     ID            `json:"marketId"`
	Label        string        `json:"label"`
	DisplayOdds  DisplayOdds   `json:"displayOdds"`
	TrueOdds     float64       `json:"trueOdds"`
	OutcomeType  string        `json:"outcomeType"`
	Status       string        `json:"status,omitempty"`
	Points       *float64      `json:"points"`
	Participants []Participant `json:"participants"`
	Tags         []string      `json:"tags"`
}

// SubscriptionPartial is one entry of subscriptionPartials. Values are kept
// generic because the CLI forwards every key it does not understand.
type SubscriptionPartial map[string]any

// Entity returns the entity name ("events", "markets", ...).
func (p SubscriptionPartial) Entity() string {
	s, _ := p["entity"].(string)
	return s
}

// Data is the usable part of a snapshot response.
type Data struct {
	Events               []Event                        `json:"events"`
	Markets              []Market                       `json:"markets"`
	Selections           []Selection                    `json:"selections"`
	Categories           []Category                     `json:"categories"`
	Subcategories        []Subcategory                  `json:"subcategories"`
	SubscriptionPartials map[string]SubscriptionPartial `json:"subscriptionPartials"`
	Sports               json.RawMessage                `json:"sports"`
	Leagues              json.RawMessage                `json:"leagues"`
}

// Category is a DraftKings market category (e.g. Player Points).
type Category struct {
	ID   ID     `json:"id"`
	Name string `json:"name"`
}

// Subcategory is a DraftKings market subcategory (e.g. Points O/U).
type Subcategory struct {
	ID         ID     `json:"id"`
	CategoryID ID     `json:"categoryId"`
	Name       string `json:"name"`
}

// PartialList returns subscription partials in a deterministic order.
func (d *Data) PartialList() []SubscriptionPartial {
	return SortedPartials(d.SubscriptionPartials)
}
