package fanduel

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWithAKReplacesKey(t *testing.T) {
	got := withAK("https://example.test/page?_ak=old&x=1", "new")
	if got != "https://example.test/page?_ak=new&x=1" {
		t.Fatalf("url = %q", got)
	}
}

func TestLiveFixtureSetsBookGameLive(t *testing.T) {
	b, err := New(Config{Path: filepath.Join("..", "..", "..", "testdata", "fd-live.json")})
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, g := range s.Games {
		if g.Live {
			live++
		}
	}
	if live == 0 {
		t.Fatal("expected at least one live FanDuel game")
	}
}
