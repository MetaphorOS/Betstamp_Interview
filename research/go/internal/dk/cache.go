package dk

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"sportsbookfeed/internal/jsonx"
)

// Cache is the merged partial-state cache the WebSocket probes maintain.
// Entities are held as generic objects so a partial change can be merged
// field-by-field exactly like the `{...old, ...change}` spread in the Node
// scripts, including the nested displayOdds merge.
//
// The exported maps remain available for tests and one-shot probes, but a
// long-running consumer must use the methods: they take the internal lock.
type Cache struct {
	mu         sync.RWMutex
	Events     map[string]map[string]any
	Markets    map[string]map[string]any
	Selections map[string]map[string]any
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{
		Events:     map[string]map[string]any{},
		Markets:    map[string]map[string]any{},
		Selections: map[string]map[string]any{},
	}
}

// SeedRaw replaces the cache with a full snapshot body. Numbers are kept as
// json.Number so re-encoding does not add or drop precision.
func (c *Cache) SeedRaw(raw []byte) error {
	obj, err := jsonx.DecodeObject(raw)
	if err != nil {
		return err
	}
	events := map[string]map[string]any{}
	markets := map[string]map[string]any{}
	selections := map[string]map[string]any{}
	for _, entity := range []struct {
		name string
		dst  map[string]map[string]any
	}{
		{"events", events},
		{"markets", markets},
		{"selections", selections},
	} {
		items, ok := obj[entity.name].([]any)
		if !ok {
			return fmt.Errorf("snapshot: %s is not an array", entity.name)
		}
		for _, item := range items {
			rec, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if id := recordID(rec); id != "" {
				entity.dst[id] = rec
			}
		}
	}
	c.mu.Lock()
	c.Events, c.Markets, c.Selections = events, markets, selections
	c.mu.Unlock()
	return nil
}

// Seed loads the typed snapshot entities. It is a convenience for callers
// that do not keep the raw body; SeedRaw is lossless and preferred.
func (c *Cache) Seed(data *Data) error {
	events := map[string]map[string]any{}
	markets := map[string]map[string]any{}
	selections := map[string]map[string]any{}
	for _, e := range data.Events {
		rec, err := toRecord(e)
		if err != nil {
			return err
		}
		events[e.ID.String()] = rec
	}
	for _, m := range data.Markets {
		rec, err := toRecord(m)
		if err != nil {
			return err
		}
		markets[m.ID.String()] = rec
	}
	for _, s := range data.Selections {
		rec, err := toRecord(s)
		if err != nil {
			return err
		}
		selections[s.ID.String()] = rec
	}
	c.mu.Lock()
	c.Events, c.Markets, c.Selections = events, markets, selections
	c.mu.Unlock()
	return nil
}

func toRecord(v any) (map[string]any, error) {
	raw, err := jsonx.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonx.DecodeObject(raw)
}

// MergeStats counts what one update frame changed.
type MergeStats struct {
	Add     int
	Change  int
	Remove  int
	Unknown int
}

// Apply merges one `message.data.data` update object.
//
// Removals accept either an object with an id or a bare id. A change for an
// unknown id is counted as Unknown and skipped, matching the probes: guessing
// a join would be worse than requesting a resync.
func (c *Cache) Apply(update map[string]any) MergeStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	var stats MergeStats
	for _, op := range []string{"add", "change", "remove"} {
		entities := jsonx.Obj(update, op)
		for _, entity := range []struct {
			name string
			dst  map[string]map[string]any
		}{
			{"events", c.Events},
			{"markets", c.Markets},
			{"selections", c.Selections},
		} {
			for _, item := range jsonx.Arr(entities, entity.name) {
				switch op {
				case "remove":
					id := anyID(item)
					if id == "" {
						continue
					}
					delete(entity.dst, id)
					stats.Remove++
				case "add":
					rec, ok := item.(map[string]any)
					if !ok {
						continue
					}
					id := recordID(rec)
					if id == "" {
						continue
					}
					entity.dst[id] = rec
					stats.Add++
				case "change":
					rec, ok := item.(map[string]any)
					if !ok {
						continue
					}
					id := recordID(rec)
					if id == "" {
						continue
					}
					old, ok := entity.dst[id]
					if !ok {
						stats.Unknown++
						continue
					}
					entity.dst[id] = mergeRecord(old, rec)
					stats.Change++
				}
			}
		}
	}
	return stats
}

// ApplyMessage extracts message.data.data from a JSON-RPC update and applies it.
// It returns ok=false for frames that are not update frames.
func (c *Cache) ApplyMessage(msg map[string]any) (MergeStats, bool) {
	if jsonx.Str(msg, "event") != "update" {
		return MergeStats{}, false
	}
	outer := jsonx.Obj(msg, "data")
	update := jsonx.Obj(outer, "data")
	if update == nil {
		return MergeStats{}, false
	}
	return c.Apply(update), true
}

// EnsureRaw adds records from a snapshot that are missing from the cache but
// never overwrites an existing record: the WebSocket copy may be newer.
// It returns how many records were added.
func (c *Cache) EnsureRaw(raw []byte) (int, error) {
	obj, err := jsonx.DecodeObject(raw)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	added := 0
	for _, entity := range []struct {
		name string
		dst  map[string]map[string]any
	}{
		{"events", c.Events},
		{"markets", c.Markets},
		{"selections", c.Selections},
	} {
		items, ok := obj[entity.name].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			rec, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := recordID(rec)
			if id == "" {
				continue
			}
			if _, exists := entity.dst[id]; exists {
				continue
			}
			entity.dst[id] = rec
			added++
		}
	}
	return added, nil
}

// Snapshot returns shallow copies of the three entity maps. Records are shared
// and must be treated as read-only; the lock is held only while copying.
func (c *Cache) Snapshot() (events, markets, selections map[string]map[string]any) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	events = copyMap(c.Events)
	markets = copyMap(c.Markets)
	selections = copyMap(c.Selections)
	return events, markets, selections
}

// Counts returns the number of cached entities.
func (c *Cache) Counts() (events, markets, selections int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.Events), len(c.Markets), len(c.Selections)
}

// Dump serialises the cache as the snapshot shape, with entities sorted by id.
func (c *Cache) Dump() map[string]any {
	events, markets, selections := c.Snapshot()
	return map[string]any{
		"events":     sortedRecords(events),
		"markets":    sortedRecords(markets),
		"selections": sortedRecords(selections),
	}
}

func copyMap(src map[string]map[string]any) map[string]map[string]any {
	dst := make(map[string]map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func sortedRecords(src map[string]map[string]any) []any {
	ids := make([]string, 0, len(src))
	for id := range src {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, src[id])
	}
	return out
}

func mergeRecord(old, change map[string]any) map[string]any {
	merged := make(map[string]any, len(old)+len(change))
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range change {
		merged[k] = v
	}
	// DraftKings sends partial displayOdds; a shallow spread would drop fields.
	if newOD, ok := change["displayOdds"].(map[string]any); ok {
		if oldOD, ok := old["displayOdds"].(map[string]any); ok {
			od := make(map[string]any, len(oldOD)+len(newOD))
			for k, v := range oldOD {
				od[k] = v
			}
			for k, v := range newOD {
				od[k] = v
			}
			merged["displayOdds"] = od
		}
	}
	return merged
}

func recordID(rec map[string]any) string {
	return anyID(rec["id"])
}

func anyID(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return fmt.Sprintf("%.0f", t)
	case map[string]any:
		// Removals and adds may be full objects rather than bare ids.
		return anyID(t["id"])
	}
	return ""
}
