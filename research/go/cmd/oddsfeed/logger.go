package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"sportsbookfeed/internal/jsonx"
)

// logRecord is one JSONL line. Field order matches the Node probes:
// {"at":"...","kind":...}.
type logRecord struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Payload any    `json:"payload,omitempty"`
	Code    int    `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// jsonlLogger appends to a JSONL capture and mirrors the probes' console
// policy: every connection event is printed, but at most `limit` received
// frames.
type jsonlLogger struct {
	mu       sync.Mutex
	file     *os.File
	received int
	limit    int
}

func newJSONLLogger(path string, limit int) (*jsonlLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &jsonlLogger{file: f, limit: limit}, nil
}

func (l *jsonlLogger) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// log writes the record and prints it under the same truncation rule as the
// Node probes (1400 characters).
func (l *jsonlLogger) log(rec logRecord) {
	line, err := jsonx.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.Write(append(line, '\n'))
	}
	if rec.Kind == "received" {
		l.received++
		if l.received > l.limit {
			return
		}
	}
	fmt.Println(jsonx.Truncate(string(line), 1400))
}

// count is a helper for the summary maps.
func count(m map[string]int, key string) {
	m[key]++
}
