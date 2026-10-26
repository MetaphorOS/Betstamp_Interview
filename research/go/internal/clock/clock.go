// Package clock provides the timing conventions the Node probes use:
// a process-relative monotonic millisecond clock (performance.now) and
// UTC ISO-8601 timestamps with millisecond precision (Date.toISOString).
package clock

import "time"

var start = time.Now()

// MonoMs is process-relative monotonic milliseconds, like performance.now().
func MonoMs() float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

// NowISO formats the current UTC time like new Date().toISOString():
// 2026-10-25T15:04:05.123Z.
func NowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// EpochMs is Date.now().
func EpochMs() int64 { return time.Now().UnixMilli() }
