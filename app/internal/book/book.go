// Package book defines the only interface the supervisor and board know about
// a sportsbook. A new book is one package plus one Register call.
package book

import (
	"context"
	"errors"
	"time"

	"betstampodds/internal/model"
)

// PriceKind distinguishes sportsbook odds from prediction-market prices.
type PriceKind string

const (
	PriceAmerican    PriceKind = "american"    // sportsbook odds; eligible for best price
	PriceProbability PriceKind = "probability" // exchange/prediction market; shown, never best
)

// Descriptor is stable book metadata.
type Descriptor struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	PriceKind   PriceKind `json:"priceKind"`
}

// Snapshot is full structure plus prices over REST. It is also the seed a
// Stream starts from.
type Snapshot struct {
	Games  []model.BookGame `json:"games"`
	Quotes []model.Quote    `json:"quotes"`
	// AsOf is when the request was SENT; anything received after it is newer.
	AsOf time.Time `json:"asOf"`
	// Skipped counts every unmapped record. Books never guess.
	Skipped map[string]int `json:"skipped,omitempty"`
	// Subscription is what the stream subscribes to. When it changes between
	// snapshots the supervisor restarts the stream with the new seed.
	Subscription string `json:"-"`
	// Private is the book-owned seed (indexes, raw subscription data). It is
	// opaque to everyone else and must never be mutated after Snapshot returns:
	// each Stream run deep-copies what it needs.
	Private any `json:"-"`
}

// Emitter is how a Stream hands updates and liveness to the supervisor.
type Emitter interface {
	// Emit blocks until the update is accepted or ctx is done.
	Emit(ctx context.Context, u model.Update) error
	// Connected reports that the push channel is subscribed and live.
	Connected()
	// Heartbeat reports any sign of life: a frame, a keep-alive, a pong.
	Heartbeat()
	// Pong reports the round trip of one ping to the book's server.
	Pong(rtt time.Duration)
	// Handshake reports the app-level request -> reply time at connect
	// (DK subscribe -> subscribed, FD connection_init -> connection_ack,
	// PM PING -> PONG once subscribed). Same idea for every book.
	Handshake(rtt time.Duration)
}

// Book is implemented by each sportsbook package. A book package fetches,
// parses and maps only; reconnect, backoff and watchdogs live in the
// supervisor.
type Book interface {
	Descriptor() Descriptor
	Snapshot(ctx context.Context) (*Snapshot, error)
	// Stream emits until ctx ends or the connection fails. It returns
	// ErrResync on anything it cannot map. It MUST NOT reconnect or back off.
	Stream(ctx context.Context, seed *Snapshot, em Emitter) error
}

// Poller is an optional cheap price-only REST read, used as the fallback
// while the stream is down and as the periodic reconcile.
type Poller interface {
	Poll(ctx context.Context, seed *Snapshot) ([]model.Update, error)
}

// Tuning is optional per-book timing; zero values mean supervisor defaults.
type Tuning struct {
	ReconcileEvery   time.Duration
	ResnapshotEvery  time.Duration
	MinSnapshotGap   time.Duration
	MinPollGap       time.Duration
	HeartbeatTimeout time.Duration
}

// Tuner is implemented by books that need non-default timing.
type Tuner interface{ Tuning() Tuning }

// ErrResync means the stream saw something it cannot map. The supervisor
// takes a fresh snapshot and restarts the stream from it.
var ErrResync = errors.New("book: resync required")

// ErrAuth means the push channel rejected our credentials. Books that derive
// credentials from a rotating source refresh it before the next connect.
var ErrAuth = errors.New("book: auth rejected")

// Registry stores constructed books. It has no shared Config and no switch on
// book names; each book package owns its own Config and New.
type Registry struct {
	books []Book
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds an already-constructed book. Nil, unnamed and duplicate books
// are programmer errors and panic at init time.
func (r *Registry) Register(b Book) {
	if b == nil {
		panic("book: nil book")
	}
	name := b.Descriptor().Name
	if name == "" {
		panic("book: empty name")
	}
	for _, existing := range r.books {
		if existing.Descriptor().Name == name {
			panic("book: duplicate registration " + name)
		}
	}
	r.books = append(r.books, b)
}

// Books returns registered books in registration order.
func (r *Registry) Books() []Book {
	out := make([]Book, len(r.books))
	copy(out, r.books)
	return out
}
