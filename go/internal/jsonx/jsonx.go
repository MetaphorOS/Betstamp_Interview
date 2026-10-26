// Package jsonx holds the small JSON helpers shared by the odds-feed clients.
//
// The JavaScript probes use JSON.stringify/JSON.parse. Go's encoding/json
// differs in two ways that matter for byte-for-byte behaviour:
//
//   - its encoder escapes <, > and & as \u003c, \u003e and \u0026 by default;
//   - float64 decoding loses the original spelling of large integers.
//
// Marshal disables HTML escaping and Decode keeps numbers as json.Number so
// re-encoded values stay identical to what a sportsbook sent.
package jsonx

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Marshal encodes v like JSON.stringify: no HTML escaping, no trailing newline.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// MarshalIndent is Marshal with two-space indentation, matching JSON.stringify(v, null, 2).
func MarshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Decode parses JSON preserving numbers as json.Number and unknown fields.
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// Reject trailing garbage, like JSON.parse does.
	if _, err := dec.Token(); err != io.EOF {
		return nil, &json.SyntaxError{Offset: int64(len(data))}
	}
	return v, nil
}

// DecodeObject parses a JSON object into map[string]any.
func DecodeObject(data []byte) (map[string]any, error) {
	v, err := Decode(data)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, &json.UnmarshalTypeError{Value: "non-object", Offset: int64(len(data))}
	}
	return obj, nil
}

// Truncate cuts s to n bytes at a rune boundary. It mirrors the
// JSON.stringify(record).slice(0, n) console limit in the Node probes.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// Str returns obj[key] as a string ("" when absent or not a string).
func Str(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

// Bool returns obj[key] as a bool.
func Bool(obj map[string]any, key string) bool {
	b, _ := obj[key].(bool)
	return b
}

// Obj returns obj[key] as an object.
func Obj(obj map[string]any, key string) map[string]any {
	m, _ := obj[key].(map[string]any)
	return m
}

// Arr returns obj[key] as an array.
func Arr(obj map[string]any, key string) []any {
	a, _ := obj[key].([]any)
	return a
}

// Number returns obj[key] as a json.Number, preserving its literal spelling.
func Number(obj map[string]any, key string) json.Number {
	n, _ := obj[key].(json.Number)
	return n
}

// TrimPrefix is a tiny helper for readable option defaults.
func TrimPrefix(s, prefix string) string { return strings.TrimPrefix(s, prefix) }
