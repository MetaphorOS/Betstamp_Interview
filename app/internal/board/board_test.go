package board

import (
	"context"
	"testing"
	"time"

	"betstampodds/internal/book"
	"betstampodds/internal/model"
)

var t0 = time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func TestBestPriceTable(t *testing.T) {
	all := func(string) bool { return true }
	open := func(line *float64, am int) Entry {
		return Entry{Line: line, American: am, Decimal: AmericanToDecimal(am), Status: model.StatusOpen, UpdatedAt: t0}
	}
	cases := []struct {
		name     string
		family   string
		outcome  string
		entries  map[string]Entry
		eligible func(string) bool
		want     string // "" = none
	}{
		{"favorite: -3 -110 beats -3.5 +100", model.FamilySpread, "nfl-vikings",
			map[string]Entry{"a": open(f(-3), -110), "b": open(f(-3.5), 100)}, all, "a"},
		{"dog: +3.5 beats +3", model.FamilySpread, "nfl-colts",
			map[string]Entry{"a": open(f(3), 100), "b": open(f(3.5), -120)}, all, "b"},
		{"over: lower total wins", model.FamilyTotal, "over",
			map[string]Entry{"a": open(f(44.5), -110), "b": open(f(45), 105)}, all, "a"},
		{"under: higher total wins", model.FamilyTotal, "under",
			map[string]Entry{"a": open(f(44.5), 105), "b": open(f(45), -115)}, all, "b"},
		{"same line: better price", model.FamilySpread, "nfl-vikings",
			map[string]Entry{"a": open(f(-3), -115), "b": open(f(-3), -105)}, all, "b"},
		{"moneyline: better price", model.FamilyMoneyline, "nfl-vikings",
			map[string]Entry{"a": open(nil, 150), "b": open(nil, 145)}, all, "a"},
		{"decimal breaks a rounded-american tie", model.FamilyMoneyline, "nfl-vikings",
			map[string]Entry{
				"a": {American: -110, Decimal: 1.905, Status: model.StatusOpen, UpdatedAt: t0},
				"b": {American: -110, Decimal: 1.909, Status: model.StatusOpen, UpdatedAt: t0},
			}, all, "b"},
		{"suspended is never best", model.FamilySpread, "nfl-vikings",
			map[string]Entry{"a": {Line: f(-2.5), American: 200, Status: model.StatusSuspended, UpdatedAt: t0}, "b": open(f(-3.5), -110)}, all, "b"},
		{"stale book is never best", model.FamilySpread, "nfl-vikings",
			map[string]Entry{"a": open(f(-2.5), 200), "b": open(f(-3.5), -110)}, func(n string) bool { return n != "a" }, "b"},
		{"nothing eligible", model.FamilyMoneyline, "nfl-vikings",
			map[string]Entry{"a": {American: 100, Status: model.StatusSuspended}}, all, ""},
		{"exact tie: most recent", model.FamilyMoneyline, "nfl-vikings",
			map[string]Entry{
				"a": {American: 120, Status: model.StatusOpen, UpdatedAt: t0},
				"b": {American: 120, Status: model.StatusOpen, UpdatedAt: t0.Add(time.Second)},
			}, all, "b"},
		{"exact tie, same time: book name", model.FamilyMoneyline, "nfl-vikings",
			map[string]Entry{"zeta": open(nil, 120), "alpha": open(nil, 120)}, all, "alpha"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i := 0; i < 20; i++ { // map order must not matter
				got := PickBest(c.family, c.outcome, c.entries, c.eligible)
				name := ""
				if got != nil {
					name = got.Book
				}
				if name != c.want {
					t.Fatalf("best = %q, want %q", name, c.want)
				}
			}
		})
	}
}

func TestProbabilityBookNeverBest(t *testing.T) {
	c := newTestCore(book.Descriptor{Name: "pm", PriceKind: book.PriceProbability})
	c.apply(upd("dk", "dk-g", ml("nfl-vikings"), nil, -110, t0), t0)
	c.apply(upd("pm", "pm-g", ml("nfl-vikings"), nil, 500, t0), t0)
	s := c.slots[slotKey(c, "dk", "dk-g", ml("nfl-vikings"))]
	if len(s.entries) != 2 || s.best == nil || s.best.Book != "dk" {
		t.Fatalf("slot = %+v best=%+v", s.entries, s.best)
	}
}

// --- board behavior ---

func ml(team string) string  { return model.MainIdentity(model.FamilyMoneyline, team) }
func spr(team string) string { return model.MainIdentity(model.FamilySpread, team) }

func game(bookName, id string) model.BookGame {
	return model.BookGame{
		Book: bookName, BookGameID: id, Sport: "nfl", League: "nfl", Start: t0,
		Home: model.Team{ID: "nfl-vikings", Name: "Minnesota Vikings"}, Away: model.Team{ID: "nfl-colts", Name: "Indianapolis Colts"},
	}
}

func newTestCore(extra ...book.Descriptor) *core {
	descs := append([]book.Descriptor{
		{Name: "dk", PriceKind: book.PriceAmerican}, {Name: "fd", PriceKind: book.PriceAmerican},
	}, extra...)
	now := t0
	c := newCore(descs, func() time.Time { return now })
	for _, d := range descs {
		c.applySnapshot(d.Name, &book.Snapshot{Games: []model.BookGame{game(d.Name, d.Name+"-g")}, AsOf: t0.Add(-time.Hour)})
		c.setHealth(d.Name, Health{State: HealthLive})
	}
	return c
}

func upd(bookName, ref, identity string, line *float64, am int, recv time.Time) model.Update {
	return model.Update{Book: bookName, GameRef: ref, Identity: identity, Line: line, American: am,
		Decimal: AmericanToDecimal(am), Status: model.StatusOpen, RecvAt: recv, Source: model.SourceStream}
}

func slotKey(c *core, bookName, ref, identity string) string {
	return c.gameRefs[bookName][ref] + "|" + identity
}

func collect(c *core) (chan Delta, func() []Delta) {
	_, ch := c.subscribe(1024)
	return ch, func() []Delta {
		var out []Delta
		for {
			select {
			case d := <-ch:
				out = append(out, d)
			default:
				return out
			}
		}
	}
}

func TestLineMoveUpdatesSameSlotAndRecordsPrev(t *testing.T) {
	c := newTestCore()
	_, drain := collect(c)
	id := spr("nfl-vikings")
	c.apply(upd("dk", "dk-g", id, f(-3), -110, t0), t0)
	c.apply(upd("dk", "dk-g", id, f(-3.5), 100, t0.Add(time.Second)), t0.Add(time.Second))
	if len(c.slots) != 1 {
		t.Fatalf("slots = %d", len(c.slots))
	}
	e := c.slots[slotKey(c, "dk", "dk-g", id)].entries["dk"]
	if *e.Line != -3.5 || e.Prev == nil || *e.Prev.Line != -3 || e.Prev.American != -110 {
		t.Fatalf("entry = %+v prev=%+v", e, e.Prev)
	}
	ds := drain()
	slots := 0
	for _, d := range ds {
		if d.Kind == KindSlot {
			slots++
		}
	}
	if slots != 2 {
		t.Fatalf("slot deltas = %d (%+v)", slots, ds)
	}
	// Same price again: silent.
	c.apply(upd("dk", "dk-g", id, f(-3.5), 100, t0.Add(2*time.Second)), t0.Add(2*time.Second))
	if ds := drain(); len(ds) != 0 {
		t.Fatalf("unchanged price emitted %+v", ds)
	}
	if got := c.slots[slotKey(c, "dk", "dk-g", id)].entries["dk"].UpdatedAt; !got.Equal(t0.Add(2 * time.Second)) {
		t.Fatalf("freshness not refreshed: %s", got)
	}
}

func TestOldPollDoesNotOverwriteStream(t *testing.T) {
	c := newTestCore()
	id := ml("nfl-vikings")
	c.apply(upd("fd", "fd-g", id, nil, -120, t0.Add(10*time.Second)), t0.Add(10*time.Second))
	poll := upd("fd", "fd-g", id, nil, -150, t0.Add(11*time.Second))
	poll.Source, poll.AsOf = model.SourcePoll, t0.Add(5*time.Second) // request sent before the stream update
	c.apply(poll, t0.Add(11*time.Second))
	if e := c.slots[slotKey(c, "fd", "fd-g", id)].entries["fd"]; e.American != -120 {
		t.Fatalf("poll overwrote newer stream data: %+v", e)
	}
	poll.AsOf = t0.Add(12 * time.Second)
	c.apply(poll, t0.Add(13*time.Second))
	if e := c.slots[slotKey(c, "fd", "fd-g", id)].entries["fd"]; e.American != -150 {
		t.Fatalf("newer poll ignored: %+v", e)
	}
}

func TestStaleBookRecomputesBest(t *testing.T) {
	c := newTestCore()
	id := ml("nfl-vikings")
	c.apply(upd("dk", "dk-g", id, nil, 150, t0), t0)
	c.apply(upd("fd", "fd-g", id, nil, 140, t0), t0)
	key := slotKey(c, "dk", "dk-g", id)
	if c.slots[key].best.Book != "dk" {
		t.Fatalf("best = %+v", c.slots[key].best)
	}
	_, drain := collect(c)
	c.setHealth("dk", Health{State: HealthStale})
	ds := drain()
	if len(ds) != 2 || ds[0].Kind != KindHealth || ds[1].Kind != KindSlot || ds[1].Best.Book != "fd" {
		t.Fatalf("deltas = %+v", ds)
	}
	c.setHealth("dk", Health{State: HealthLive})
	ds = drain()
	if len(ds) != 2 || ds[1].Best.Book != "dk" {
		t.Fatalf("recovery deltas = %+v", ds)
	}
}

func TestSnapshotRemovesMissingEntries(t *testing.T) {
	c := newTestCore()
	id := ml("nfl-vikings")
	c.apply(upd("dk", "dk-g", id, nil, 150, t0), t0)
	c.applySnapshot("dk", &book.Snapshot{Games: []model.BookGame{game("dk", "dk-g")}, AsOf: t0.Add(time.Second)})
	if len(c.slots) != 0 {
		t.Fatalf("stale entry kept: %+v", c.slots)
	}
	// An entry newer than the snapshot request survives it.
	c.apply(upd("dk", "dk-g", id, nil, 150, t0.Add(5*time.Second)), t0.Add(5*time.Second))
	c.applySnapshot("dk", &book.Snapshot{Games: []model.BookGame{game("dk", "dk-g")}, AsOf: t0.Add(4 * time.Second)})
	if len(c.slots) != 1 {
		t.Fatal("newer entry removed by an older snapshot")
	}
}

func TestUnknownGameDropped(t *testing.T) {
	c := newTestCore()
	c.apply(upd("dk", "nope", ml("nfl-vikings"), nil, 100, t0), t0)
	if len(c.slots) != 0 || c.dropped["dk"] != 1 {
		t.Fatalf("slots=%d dropped=%v", len(c.slots), c.dropped)
	}
}

func TestBooksMatchToOneCanonicalGame(t *testing.T) {
	c := newTestCore()
	c.apply(upd("dk", "dk-g", ml("nfl-vikings"), nil, 100, t0), t0)
	c.apply(upd("fd", "fd-g", ml("nfl-vikings"), nil, 105, t0), t0)
	if len(c.slots) != 1 || len(c.state().Games) != 1 {
		t.Fatalf("slots=%d games=%d", len(c.slots), len(c.state().Games))
	}
}

func TestSlowSubscriberDroppedAndSeqIncreases(t *testing.T) {
	b := newBoard([]book.Descriptor{{Name: "dk", PriceKind: book.PriceAmerican}}, func() time.Time { return t0 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	if err := b.ApplySnapshot(ctx, "dk", &book.Snapshot{Games: []model.BookGame{game("dk", "dk-g")}}); err != nil {
		t.Fatal(err)
	}
	st, slow, _, err := b.SubscribeWithState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fast, stop, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	n := SubscriberBuffer + 50
	done := make(chan []uint64)
	go func() {
		var seqs []uint64
		for d := range fast {
			seqs = append(seqs, d.Seq)
			if len(seqs) >= n {
				break
			}
		}
		done <- seqs
	}()
	for i := 0; i < n; i++ {
		// Alternate prices so every update is visible.
		am := 100 + i%2
		if err := b.Apply(ctx, upd("dk", "dk-g", ml("nfl-vikings"), nil, am, t0.Add(time.Duration(i)*time.Millisecond))); err != nil {
			t.Fatal(err)
		}
	}
	var seqs []uint64
	select {
	case seqs = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fast subscriber blocked")
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq not increasing at %d: %d then %d", i, seqs[i-1], seqs[i])
		}
	}
	if seqs[0] <= st.Seq {
		t.Fatalf("first delta %d not after snapshot seq %d", seqs[0], st.Seq)
	}
	// The slow subscriber never read: it must have been closed, not block.
	got := 0
	for range slow {
		got++
	}
	if got != SubscriberBuffer {
		t.Fatalf("slow subscriber buffered %d", got)
	}
	if _, _, err := b.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
}
