package books_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/books/draftkings"
	"betstampodds/internal/books/fanduel"
	"betstampodds/internal/match"
	"betstampodds/internal/model"
)

func load(t *testing.T, b book.Book) (*book.Snapshot, error) {
	t.Helper()
	return b.Snapshot(context.Background())
}

func mkBook(t *testing.T, path string, kind string) book.Book {
	t.Helper()
	var (
		b   book.Book
		err error
	)
	switch kind {
	case "dk":
		b, err = draftkings.New(draftkings.Config{Path: path})
	case "fd":
		b, err = fanduel.New(fanduel.Config{Path: path})
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type slot struct {
	line   *float64
	book   string
	family string
}

func parity(t *testing.T, dkPath, fdPath string) (games, slots, both, sameLine int) {
	t.Helper()
	dk := mkBook(t, dkPath, "dk")
	fd := mkBook(t, fdPath, "fd")
	ds, err := load(t, dk)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := load(t, fd)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*book.Snapshot{ds, fs} {
		if s.Private == nil || s.Subscription == "" {
			t.Fatalf("snapshot missing seed or subscription")
		}
		seen := map[string]bool{}
		for _, q := range s.Quotes {
			key := q.BookGameID + "|" + model.IdentityKey(q)
			if seen[key] {
				t.Fatalf("two quotes for one slot %s", key)
			}
			seen[key] = true
		}
	}
	m := match.New()
	slotsByKey := map[string]map[string]slot{}
	add := func(s *book.Snapshot, label string) int {
		byRef := map[string]string{}
		for _, g := range s.Games {
			p, ok := m.Resolve(g, 3*time.Hour)
			if !ok {
				continue
			}
			byRef[g.BookGameID] = p.Game.ID
		}
		for _, q := range s.Quotes {
			gid := byRef[q.BookGameID]
			if gid == "" {
				continue
			}
			q.GameID = gid
			key, err := model.BoardSlotKey(gid, q)
			if err != nil {
				t.Fatal(err)
			}
			if slotsByKey[key] == nil {
				slotsByKey[key] = map[string]slot{}
			}
			slotsByKey[key][label] = slot{line: q.Line, book: label, family: q.Family}
		}
		return len(byRef)
	}
	dkGames := add(ds, "dk")
	fdGames := add(fs, "fd")
	games = dkGames
	if fdGames < games {
		games = fdGames
	}
	byFamily := map[string][2]int{}
	for _, entries := range slotsByKey {
		slots++
		for _, e := range entries {
			v := byFamily[e.family]
			if e.book == "dk" {
				v[0]++
			} else {
				v[1]++
			}
			byFamily[e.family] = v
		}
		if _, ok := entries["dk"]; !ok {
			continue
		}
		if _, ok := entries["fd"]; !ok {
			continue
		}
		both++
		if model.LineString(entries["dk"].line) == model.LineString(entries["fd"].line) {
			sameLine++
		}
	}
	t.Logf("byFamily=%v", byFamily)
	return games, slots, both, sameLine
}

func TestPregameParity(t *testing.T) {
	games, slots, both, same := parity(t,
		filepath.Join("..", "..", "testdata", "dk-pregame.json"),
		filepath.Join("..", "..", "testdata", "fd-pregame.json"))
	t.Logf("pregame games=%d slots=%d both=%d sameLine=%d", games, slots, both, same)
	if games != 25 || slots != 150 || both != 150 || same != 124 {
		t.Fatalf("pregame parity %d/%d/%d/%d", games, slots, both, same)
	}
}

func TestLiveParity(t *testing.T) {
	games, slots, both, same := parity(t,
		filepath.Join("..", "..", "testdata", "dk-live.json"),
		filepath.Join("..", "..", "testdata", "fd-live.json"))
	t.Logf("live games=%d slots=%d both=%d sameLine=%d", games, slots, both, same)
	if games != 12 || slots != 72 || both != 72 || same != 58 {
		t.Fatalf("live parity %d/%d/%d/%d", games, slots, both, same)
	}
}
