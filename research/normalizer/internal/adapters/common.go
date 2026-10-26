// Package adapters parses book payloads into the canonical model. Adapters are
// deliberately dumb about identity/taxonomy: they only expose raw fields.
package adapters

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// flexID accepts either a JSON string or number.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexID(s)
		return nil
	}
	*f = flexID(string(b))
	return nil
}

func (f flexID) String() string { return string(f) }

// parseAmerican handles plus signs, EVEN and the Unicode minus used by DraftKings.
func parseAmerican(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\u2212", "-")
	s = strings.ReplaceAll(s, "\u2013", "-")
	s = strings.ReplaceAll(s, "\u2014", "-")
	if strings.EqualFold(s, "EVEN") {
		v := 100
		return &v
	}
	v, err := strconv.Atoi(strings.TrimPrefix(s, "+"))
	if err != nil {
		return nil
	}
	return &v
}

func parseDecimal(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02T15:04:05.9999999Z", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func statusOf(marketStatus, runnerStatus string) string {
	m := strings.ToUpper(strings.TrimSpace(marketStatus))
	r := strings.ToUpper(strings.TrimSpace(runnerStatus))
	if strings.Contains(r, "SUSPEND") || strings.Contains(m, "SUSPEND") {
		return "suspended"
	}
	if m == "OPEN" || m == "ACTIVE" {
		return "open"
	}
	return strings.ToLower(m)
}
