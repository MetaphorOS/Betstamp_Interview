// Package books wires the concrete book packages into a registry. Adding a
// book is one Register line here.
package books

import (
	"betstampodds/internal/book"
	"betstampodds/internal/books/draftkings"
	"betstampodds/internal/books/fanduel"
	"betstampodds/internal/books/polymarket"
)

// Options carries the few per-book settings commands expose.
type Options struct {
	FanDuelBundlePath string
}

// Default registers every live book.
func Default(opt Options) *book.Registry {
	r := book.NewRegistry()
	dk, _ := draftkings.New(draftkings.Config{})
	r.Register(dk)
	fd, _ := fanduel.New(fanduel.Config{BundlePath: opt.FanDuelBundlePath})
	r.Register(fd)
	pm, _ := polymarket.New(polymarket.Config{})
	r.Register(pm)
	return r
}
