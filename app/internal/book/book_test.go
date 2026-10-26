package book

import (
	"context"
	"testing"
	"time"

	"betstampodds/internal/model"
)

type fakeBook struct{ name string }

func (f fakeBook) Descriptor() Descriptor {
	return Descriptor{Name: f.name, DisplayName: "Fake Book", PriceKind: PriceAmerican}
}
func (fakeBook) Snapshot(ctx context.Context) (*Snapshot, error) {
	return &Snapshot{AsOf: time.Now()}, nil
}
func (fakeBook) Stream(ctx context.Context, seed *Snapshot, em Emitter) error { return nil }
func (fakeBook) Poll(ctx context.Context, seed *Snapshot) ([]model.Update, error) {
	return nil, nil
}

func TestRegistryStoresConstructedBooks(t *testing.T) {
	var b Book = fakeBook{name: "fake"}
	r := NewRegistry()
	r.Register(b)
	got := r.Books()
	if len(got) != 1 || got[0].Descriptor().Name != "fake" {
		t.Fatalf("books = %+v", got)
	}
	got[0] = nil
	if r.Books()[0] == nil {
		t.Fatal("Books must return a copy")
	}
	if _, ok := b.(Poller); !ok {
		t.Fatal("fake should implement Poller")
	}
}

func TestRegistryPanics(t *testing.T) {
	for name, fn := range map[string]func(r *Registry){
		"nil":       func(r *Registry) { r.Register(nil) },
		"empty":     func(r *Registry) { r.Register(fakeBook{}) },
		"duplicate": func(r *Registry) { r.Register(fakeBook{name: "x"}); r.Register(fakeBook{name: "x"}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			fn(NewRegistry())
		})
	}
}
