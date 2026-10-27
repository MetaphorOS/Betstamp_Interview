# Independent review of `app/`

Reviewed 2026-10-26 against **`HEAD 43b65b1`** (`Supervisor: REST work never blocks the control loop`). Scope: the Go services and book adapters, the board and matcher, the SSE fanout, the browser page, tests, and deployment configuration. No application code or tests were changed.

While I was reviewing, the tree moved from `741dc32` to `43b65b1`. The only source change was in `internal/supervisor` (plus its tests and the README); I re-read the new code and reviewed the fix, so the findings below are against the current tree.

This review is independent. I read the existing [`APP_CODE_QUALITY_REVIEW.md`](APP_CODE_QUALITY_REVIEW.md) after starting; where my conclusions overlap I re-derived and, where practical, reproduced the behaviour with a throwaway test. Those overlaps are marked **reproduced**. Findings that are not in that review are marked **new**.

## 1. Validation performed

| Check | Result |
|---|---|
| `go vet ./...` | clean |
| `go test -count=1 ./...` | all packages pass |
| `go test -race -count=1 ./...` | all packages pass (includes the new slow-reconcile watchdog test) |
| `node --check app/cmd/server/web/app.js` | clean |
| Coverage | board 88.6%, supervisor 86.9%, fanout 85.7%, DK 58.9%, FD 61.7%, PM 51.7%, model 35.0%; `clocksync` and all `cmd/` packages 0% |

I wrote and then removed eleven throwaway probe tests under the package directories. Ten failed for the reasons below (the eleventh, a no-op lifecycle check, passed):

- a stale poll reapplies a quote the stream already removed;
- a decimal-only change leaves the wrong best price selected;
- a stale book's open quote keeps a finished game open indefinitely;
- `Board.Snapshot(background)` can block forever after the board stopped;
- Polymarket emits nothing for a full book with no asks, and nothing for an ask-only book;
- reusing the supervisor's seed keeps health `degraded` instead of ageing to `stale`;
- a DraftKings `change` frame revives a tombstoned selection, including after its market was removed;
- DraftKings emits an open spread/total with a nil line.

I did **not** run the live books, a real browser, a blocked-socket SSE test, or a deployment. Those remain test gaps.

## 2. Verdict

The architecture is good and unusually well tested for a project this size. The boundaries hold: book packages only fetch/parse/map; the supervisor owns recovery; the board owns matching, best price and game state on one goroutine; the hub owns fanout. The replay, parity and supervisor tests are the strongest part of the codebase.

The real risks are concentrated in **data lifecycle** and **blocking I/O**:

1. Removed or unavailable quotes can come back because "removal" is represented by deleting an entry rather than recording a watermark.
2. The best-price comparator uses fields (`Decimal`, `UpdatedAt`) that the change detector ignores, so the selected best can silently diverge from the rule.
3. Polymarket treats "no executable ask" as "no news", which leaves the previous price on the board.
4. The browser and the fanout do not fully model removal/replay, which affects stale cells and the latency numbers the project is built around.
5. Health freshness can advance without data, and one lifecycle rule (finished games) trusts stale books.

The supervisor's synchronous-REST bug from the earlier review is **fixed** at this HEAD; I reviewed the replacement and list its residuals separately (`F15`).

## 3. Findings at a glance

| # | Severity | Area | Finding |
|---|---|---|---|
| F1 | High | Board | A REST result older than a stream removal can resurrect the removed quote |
| F2 | High | Board/best price | Decimal-only and freshness-only changes do not recompute best |
| F3 | High | Polymarket | A disappearing ask leaves the previous executable quote in place |
| F4 | Medium | Board | A stale/down book's open quote keeps a game open forever |
| F5 | Medium | Browser/SSE | Resumed backlog frames are counted as fresh latency samples |
| F6 | Medium | Browser | Game removal leaves old slots and `shown` entries behind; a revived game shows old prices |
| F7 | Medium | Fanout | SSE writes have no deadline; dropping the channel cannot interrupt a blocked write |
| F8 | Medium | Supervisor | Health freshness advances on seed reuse and on a skipped DK poll |
| F9 | Medium | Polymarket | Discovery is capped at one page of 100 events |
| F10 | Medium | Board API | `Snapshot`/`SubscribeWithState` can wait forever after the board stopped |
| F11 | Medium | FanDuel | Bundle refresh has no failure backoff and sits in the snapshot path |
| F12 | Medium-low | Extensibility | The arch test and page labels do not cover `polymarket`; "page reads books from the snapshot" is only partly true |
| F13 | Medium-low | DK state | A `change` frame can revive a tombstoned selection, even after its market was removed |
| F14 | Medium-low | DK/board | An open spread/total with a nil line is emitted and can rank as line 0 |
| F15 | Medium-low | Supervisor | A background resnapshot does not advance the seed used for reconnects |
| F16 | Low | Board/matcher | Game history and matcher state have no retention bound |

## 4. Findings in detail

### F1. A REST result older than a stream removal can resurrect the removed quote — High, reproduced

References: [`board.go:465`](app/internal/board/board.go#L465), [`board.go:532`](app/internal/board/board.go#L532), [`board.go:417`](app/internal/board/board.go#L417), [`supervisor.go:347`](app/internal/supervisor/supervisor.go#L347).

The stale-REST guard is:

```go
if had && u.Source != model.SourceStream && old.UpdatedAt.After(u.AsOf) {
    return
}
```

`removeEntry` deletes the entry. Once the entry is absent, `had` is false and the guard does not run, so a poll or snapshot that started **before** the removal and returns **after** it is accepted as a new quote. `applySnapshot` has the same shape: its quotes are applied with `snap.AsOf` as both `AsOf` and `RecvAt`, and entries absent from the board are created without checking whether a newer removal happened.

Concrete sequence:

1. A DK/FD/PM poll is sent at T1 (`AsOf = T1`), the REST response is slow.
2. The stream removes the selection at T2.
3. The poll returns at T3 carrying the T1 quote. The entry is gone, so the guard is skipped; the quote reappears and can become best again.

This is not theoretical: the stream goroutine and a reconcile poll run concurrently (`em.Emit` vs. `s.poll`), and FanDuel/Polymarket snapshots take multiple requests. A regression probe (open → remove at T2 → poll `AsOf=T1` at T3) keeps the slot.

**Fix:** keep a per-book, per-slot removal watermark (the `RecvAt`/`AsOf` of the removing update) and reject any non-stream update whose `AsOf` is not newer than the watermark **even when no entry exists**. Bound retention by the lifetime of outstanding requests (or a short window such as a few minutes). A tombstone entry left in place, rather than deleting the slot, is the simplest version; `Entry.Status == Removed` already has a wire shape.

**Regression test:** `TestStalePollCannotReviveRemovedEntry` and the equivalent for `ApplySnapshot`.

### F2. Decimal-only and freshness-only changes do not recompute best — High, reproduced

References: [`board.go:485`](app/internal/board/board.go#L485), [`board.go:527`](app/internal/board/board.go#L527), [`best.go:52`](app/internal/board/best.go#L52), [`best.go:73`](app/internal/board/best.go#L73).

`visibleChange` compares only line, American, status and in-play state. `Decimal` and `UpdatedAt` are inputs to the best-price comparator:

- `decimalOf` prefers the precise decimal, so a decimal change can change the winner while American stays the same;
- `better` uses `UpdatedAt` as the tie-break, so a confirmation that refreshes a timestamp can change a tie.

In that case `apply` silently updates `old.Decimal`/`old.UpdatedAt`, records a confirmation and returns **before** `pickBest`. The board can keep the wrong winner indefinitely, and a connected browser never receives the new decimal (it only sees it after a fresh snapshot). This also contradicts the documented "most recently updated book" tie-break.

Reproduced with two entries at `-110` and decimals `1.90`/`1.95`; a DK update to `2.00` with the same American leaves FD selected.

**Fix:** treat `Decimal` as a visible change, and recompute best whenever any comparator input changes. Separate "freshness only" (confirmation) from "price changed" (publish a delta, including the precise decimal). If a decimal-only change should not flash, publish it with a flag or ignore it for the flash but still update `best`.

**Regression tests:** decimals cross while American is unchanged; the losing book confirms an exact tie more recently.

### F3. A disappearing ask leaves the previous executable quote in place — High, reproduced

References: [`transport.go:217`](app/internal/books/polymarket/transport.go#L217), [`transport.go:248`](app/internal/books/polymarket/transport.go#L248), [`transport.go:192`](app/internal/books/polymarket/transport.go#L192).

`seed.update` returns `nil, nil` when `bid <= 0 || ask <= 0 || ask > 1 || bid > ask`. For a full `book` message with an empty asks array, `bestOf(asks)` returns 0, so no update is emitted. The previous open quote stays on the board, remains eligible for best price (the book is still healthy and heartbeating), and only a later snapshot/price change corrects it. The same path discards an ask-only book (`bid == 0`) even though the buy-side price is usable, and treats a crossed book as "no news" rather than "unusable".

Reproduced: open at ask 0.62, then a `book` with `asks: []`; zero updates emitted; the old quote is still the board's last state. Ask-only (`best_bid: 0`, `best_ask: 0.62`) also emits nothing.

**Fix:** distinguish "no executable ask" from "malformed/partial message". An absent ask should emit `Suspended` or `Removed` for that token (and should stop it being best); do not require a bid to price an ask-side execution; consider a crossed book suspended. Track the last status so a repeated empty book does not spam updates.

**Regression tests:** open then empty asks → old quote no longer eligible immediately; ask-only book → priced; crossed book → suspended, not silent.

### F4. A stale/down book's open quote keeps a game open forever — Medium, new, reproduced

References: [`board.go:179`](app/internal/board/board.go#L179), [`board.go:246`](app/internal/board/board.go#L246).

`gameStatus` decides "open" by scanning **all** entries regardless of book health:

```go
for _, e := range s.entries {
    if e.Status == model.StatusOpen { open = true }
}
```

If a book goes stale while holding an open quote on a game that has finished, `open` stays true and `gs.view.Live` is sticky, so the status is `live` (or `pregame` if the live flag was never set); `tick` never sees `closed`, never prunes, and the page shows a LIVE/Pregame card with grey stale cells forever (until that book recovers or a snapshot removes its entries). `lastOpenAt` is only consulted when `open` is false.

Reproduced: apply a live FD quote, mark FD stale, advance 4 hours; the game is still `live`.

This is partly a product decision — keeping a game visible while a book is down can be reasonable — but the current rule never ages out. A finished game can remain "live" indefinitely, which defeats the close/prune design in §8.3 of the README.

**Fix:** derive `open` from entries whose book is not stale/down (the same eligibility used by best price), or require an open quote confirmed within a bound. Keep the game visible as `closed`/stale rather than `live`. A short grace period (e.g., `CloseAfter` since the last eligible open quote) avoids closing a game during a brief outage.

**Regression test:** live open quote, book goes stale, advance past `CloseAfter` → `closed`, then pruned.

### F5. Resumed backlog frames are counted as fresh latency samples — Medium, new

References: [`hub.go:292`](app/internal/fanout/hub.go#L292), [`app.js:212`](app/cmd/server/web/app.js#L212), [`app.js:217`](app/cmd/server/web/app.js#L217).

On `Last-Event-ID` resume the hub writes the ring backlog first. Those frames keep their original `recv`/`bts` values and `src: "stream"`. The page records a latency sample for every slot delta with `src === "stream" && recv && e && bclock`:

```js
const now = serverNow();
const s2y = now - d.recv;
const e2e = ok ? now + state.clock.offsetMs - d.bts : null;
```

For a backlog frame delivered after a 30 s disconnect, `now - d.recv` is ~30 s, so `server→you` and `book→screen` get a huge sample. The per-phase arrays keep 300 samples, so a single resume can dominate p50/p95 for a long time. The same replay can flash cells with old moves (the rAF coalesces the final state, but a false move can still be shown).

**Fix:** mark backlog frames as replayed (a field in the SSE frame or the wire delta), or skip latency sampling when the frame's server encode time (`d.now`) is older than a small threshold (e.g., 1–2 s). Optionally clear `state.lat` on resume, or ignore replayed frames for flashing.

**Test:** connect, disconnect, publish while disconnected, resume; assert no sample with `s2y` above the threshold.

### F6. Game removal leaves old slots and `shown` entries; a revived game can show old prices — Medium, reproduced by inspection

References: [`app.js:191`](app/cmd/server/web/app.js#L191), [`app.js:199`](app/cmd/server/web/app.js#L199), [`app.js:305`](app/cmd/server/web/app.js#L305), [`board.go:246`](app/internal/board/board.go#L246).

The server prunes a game by deleting all its slots and sending only a `KindGame` delta with `Removed: true`. The page deletes the game from `state.games` but keeps every `state.slots` key and every `shown` key. Consequences:

- memory grows with every game ever seen (bounded per session, but unnecessary);
- if the game is revived by one open price, the server sends only that new entry, while the browser still has the other five old entries; `renderGame` renders them again as if current;
- ordinary slot removal deletes `sl.entries[book]` but leaves an empty slot object; because `hasPrices` checks only key existence, an empty slot still counts as "this game has a price";
- `shown` is never pruned for removed games.

**Fix:** on a game-removal delta, delete all `state.slots` whose key starts with `${gameId}|` and all `shown` keys for those slots. On a slot-removal delta, delete the slot when `Object.keys(entries).length === 0` and drop its `shown` entries. `hasPrices` should check that some slot has at least one entry.

**Regression test:** prune a game with six slots, revive it with one quote, verify only that quote renders; assert map sizes return to baseline.

### F7. SSE writes have no deadline; dropping the channel cannot interrupt a blocked write — Medium

References: [`hub.go:283`](app/internal/fanout/hub.go#L283), [`hub.go:337`](app/internal/fanout/hub.go#L337), [`main.go:183`](app/cmd/server/main.go#L183), [`hub_test.go:193`](app/internal/fanout/hub_test.go#L193).

The client queue is bounded, but `w.Write` and `fl.Flush()` have no write deadline. Closing `c.ch` cannot interrupt a handler already blocked writing to a peer that stopped reading; the goroutine and connection can outlive the hub's registration until the OS/TCP timeout. The current slow-client test fills an internal channel; it does not simulate a socket that stops consuming bytes. The server sets `ReadHeaderTimeout` but no per-write deadline (a global `WriteTimeout` would be wrong for SSE).

**Fix:** use `http.NewResponseController(w).SetWriteDeadline` before each write/flush with a bounded interval (e.g., 10–30 s), and treat a flush error as a disconnect. Alternatively run a per-client writer goroutine with a bounded queue and close the connection on overflow.

**Test:** connect a real client, stop reading until socket buffers fill, assert the handler exits within a bounded time while other clients keep receiving.

### F8. Health freshness advances without fresh data — Medium, reproduced

References: [`supervisor.go:190`](app/internal/supervisor/supervisor.go#L190), [`supervisor.go:211`](app/internal/supervisor/supervisor.go#L211), [`supervisor.go:215`](app/internal/supervisor/supervisor.go#L215), [`supervisor.go:347`](app/internal/supervisor/supervisor.go#L347), [`draftkings.go:135`](app/internal/books/draftkings/draftkings.go#L135).

Two paths refresh `lastData` without new data:

1. When the snapshot rate limit forbids a fresh fetch, `Run` reuses `s.seed`; applying that old snapshot still calls `s.markData()` (line 215). Repeated reconnects inside the gap keep the book "degraded" even though the seed may be minutes old.
2. DraftKings `Poll` returns `(nil, nil)` when its 15 s REST budget suppresses the poll. The supervisor counts that as a successful poll, sets `pollOKAt`, and calls `markData` with zero updates.

Health is not cosmetic: `excluded()` drives best-price eligibility and the page's stale styling. A book that is not actually receiving data can stay `degraded`/eligible.

Reproduced: one seed, `MinSnapshotGap = 1h`, a stream that always fails, `StaleAfter = 60ms`; after 300 ms health is still `degraded`.

**Fix:** separate "transport attempt" from "confirmed data". Do not `markData` when applying a reused seed; give `Poll` a way to say "skipped" (a sentinel error/boolean) and do not count it as success. Track data age from the original snapshot's `AsOf`/last confirmed frame.

**Regression test:** seed once, repeatedly fail sockets and suppress polls; health must reach `stale` after `StaleAfter`.

### F9. Polymarket discovery is capped at one page of 100 events — Medium

References: [`polymarket.go:32`](app/internal/books/polymarket/polymarket.go#L32), [`polymarket.go:122`](app/internal/books/polymarket/polymarket.go#L122), [`board.go:417`](app/internal/board/board.go#L417).

The Gamma URL is fixed at `limit=100` and the code makes one GET per series, with no pagination/offset and no check that the result reached the cap. If a series has more matching events, coverage silently stops. Because a snapshot is treated as authoritative, entries outside the returned page can be removed as if the source stopped offering them.

**Fix:** follow the upstream pagination contract (or bound by date and report truncation). Treat a capped/truncated result as incomplete, not as deletion evidence.

**Regression test:** a mock source with two pages; an event only on page two must remain discoverable.

### F10. `Snapshot`/`SubscribeWithState` can wait forever after the board stopped — Medium, reproduced

References: [`board.go:690`](app/internal/board/board.go#L690), [`board.go:717`](app/internal/board/board.go#L717), [`board.go:739`](app/internal/board/board.go#L739).

`do` returns after enqueueing a closure. If `Run` has stopped, `b.done` is closed, but `do`'s select can still choose the send to `b.inbox` when both are ready (the inbox is buffered at 4096). The closure is then never executed, and `Snapshot` waits only for its reply or the caller's context. A caller using `context.Background()` blocks forever. The same window can create a subscription whose cancellation function never reaches the caller; it lives until the board drops it or exits.

Reproduced: stop the board, then call `Snapshot(context.Background())` 100 times; the first attempt blocks.

**Fix:** after a successful enqueue, also select on `b.done` while waiting for the reply; check `b.done`/`ctx` before enqueueing; arrange subscription cleanup when the caller cancels between creation and reply.

**Regression tests:** stop the board after acceptance but before execution; cancel a subscription request during creation.

### F11. FanDuel bundle refresh has no failure backoff and sits in the snapshot path — Medium, new

References: [`bundle.go:117`](app/internal/books/fanduel/bundle.go#L117), [`fanduel.go:198`](app/internal/books/fanduel/fanduel.go#L198), [`supervisor.go:319`](app/internal/supervisor/supervisor.go#L319).

`bundleProvider.get` keeps last-good on a failed live fetch, but does **not** update `gotAt`. Every later `get` therefore retries the live fetch instead of waiting out the 6 h TTL. On a blocked/changed homepage this means:

- a live fetch (home page → `main.<hash>.js` → optional dependencies, each with a 25 s client timeout) at the start of every FD snapshot (every 2 min) and at every stream connect;
- a warning log per attempt;
- the fetch runs inside the supervisor's snapshot job, delaying price refresh (the new job architecture keeps the watchdog responsive, but the snapshot itself is late).

The README's degraded-homepage story is right in spirit, but without a retry backoff the fallback is noisier and slower than it needs to be.

**Fix:** record `lastAttemptAt` and apply a short backoff (e.g., 1–5 min) before retrying a failed live fetch while keeping last-good; cap the whole bundle fetch with a shorter timeout; keep bundle refresh off the price path when possible.

**Test:** fake home-page failure; assert the number of fetch attempts over N snapshots is bounded, not one per snapshot.

### F12. The extensibility guard and page labels do not cover the third book — Medium-low, new

References: [`archtest_test.go:16`](app/internal/archtest/archtest_test.go#L16), [`app.js:34`](app/cmd/server/web/app.js#L34), [`app.js:463`](app/cmd/server/web/app.js#L463), [`app.js:489`](app/cmd/server/web/app.js#L489).

The arch test bans only `draftkings` and `fanduel`, and only in `.go` files outside `internal/books` and `cmd`. `polymarket` is not banned; a reference in `board`/`supervisor`/`fanout` would pass. The page hardcodes book-specific presentation too:

- `noBookTime = b.name === "fanduel"` for the "no publish timestamp" row;
- the handshake/ping label tables (`HS`, `PING`) keyed by book name;
- `SHORT` names.

The README says a third book was added "without touching the framework" and "the page reads the list of books from the snapshot". The toggles and cards do appear dynamically, but a fourth book with different transport semantics would need JS edits. That is a smaller claim than "no re-architecture", but it is worth making explicit.

**Fix:** add `polymarket` to the arch test (or drive it from a list of registered descriptor names) and add a JS-grep test for book names. Better, put capability metadata on `book.Descriptor` (e.g., `HasBookTimestamp`, handshake/ping labels) so the page does not need a name switch.

### F13. A `change` frame can revive a tombstoned selection — Medium-low, new, reproduced

References: [`state.go:354`](app/internal/books/draftkings/state.go#L354), [`state.go:362`](app/internal/books/draftkings/state.go#L362), [`state.go:419`](app/internal/books/draftkings/state.go#L419), [`state.go:539`](app/internal/books/draftkings/state.go#L539).

The selection-resolution order says "tombstoned ID (same-ID re-add)", and the function has an `isAdd` flag for the replacement case, but case 2 does not check it:

```go
case s.tombs[id] != nil: // no isAdd check here
```

A `change` for a removed selection revives it. If this happens after `removeMarket` (kickoff teardown, market flap), the market is already gone, so `update()` cannot apply the market's suspended state and emits an **open** quote with the tombstone's old price. A change arriving after an event removal behaves similarly.

This is conditional on an out-of-order/duplicate/retried change frame; DK's WebSocket ordering normally prevents it. The captured replays do not exercise it (the revival counter does not distinguish adds from changes), but the code's own comment says only a re-add should revive, and market teardown is exactly when late frames are plausible.

**Fix:** require `isAdd` for tombstone revival (or hold changes for a tombstoned ID until the tombstone expires, then ignore). If a change arrives for a tombstoned selection whose market/event is gone, ignore it or resync; do not emit an open quote from a tombstone.

**Tests:** change after selection remove; change after market remove; change after event remove.

### F14. An open spread/total with a nil line is emitted and can rank as line 0 — Medium-low, new, reproduced

References: [`state.go:419`](app/internal/books/draftkings/state.go#L419), [`state.go:531`](app/internal/books/draftkings/state.go#L531), [`board.go:475`](app/internal/board/board.go#L475), [`best.go:35`](app/internal/board/best.go#L35).

DraftKings only sets a line when `Points != nil`; `emit` only checks `american != 0`. A new spread/total selection that arrives without `points` is emitted with `Line == nil`. The board accepts it, and `lineScore` returns 0 for a nil line, so a line-less spread/total can be ranked as if its line were 0 — potentially beating a real line in best price. The page renders it with no line.

The snapshot parser has the same gap (it copies `s.Points` unconditionally). The replay fixtures happen to always carry points, so the tests do not catch it. A partial add followed by a change would show a line-less quote briefly; a missing follow-up leaves it indefinitely.

**Fix:** treat "no line" as "not yet usable" for spread/total: do not emit the update until the line is known; and reject nil-line spread/total in `board.apply` (defense in depth). Moneyline is the only family where nil is valid.

**Test:** add a spread/total add without points; assert no open update with a nil line.

### F15. A background resnapshot does not advance the seed used for reconnects — Medium-low, new (residual of the supervisor fix)

References: [`supervisor.go:211`](app/internal/supervisor/supervisor.go#L211), [`supervisor.go:558`](app/internal/supervisor/supervisor.go#L558), [`supervisor.go:319`](app/internal/supervisor/supervisor.go#L319).

`resnapshot` runs as a background job, takes a fresh snapshot with `snapshotNow`, and applies it to the board. It does **not** update `s.seed`, which is owned by the `Run` goroutine and is only assigned when `Run` itself calls `snapshot` (line 211). If the stream drops inside `MinSnapshotGap` after a resnapshot, `Run` reconnects from the older seed.

For prices, entries that still exist survive (they are newer than the old `AsOf`), but a quote that the resnapshot **removed** can be resurrected by the old seed — the same root as F1. For **structure**, it is a regression: `ApplySnapshot(oldSeed)` replaces `gameRefs` with the old mapping, and the new stream subscribes from the old `Subscription`/market list. New markets/games discovered by the resnapshot (FanDuel in-play markets, Polymarket's new main line, a new DK event) are not streamed/polled until the next fresh snapshot. FanDuel can need up to 2 minutes; Polymarket up to 30 seconds.

This predates the async fix (the old synchronous resnapshot had the same seed behaviour), but the new job/seed split makes the invariant easy to miss.

**Fix:** publish the resnapshot's snapshot to the `Run` goroutine (or protect `s.seed` with a mutex) so the next reconnect uses the newest good snapshot. Alternatively, always take a fresh snapshot on a drop after a resnapshot (ignore the rate-limit reuse for that case).

**Test:** connect, run a resnapshot that changes the subscription/market set, drop the stream inside the rate limit; the next stream must use the new seed.

### F16. Game history and matcher state have no retention bound — Low

References: [`board.go:246`](app/internal/board/board.go#L246), [`board.go:596`](app/internal/board/board.go#L596), [`match.go:22`](app/internal/match/match.go#L22).

Pruning deletes slots but leaves the `gameState` in `c.games`; `removeEntry` also leaves a game with `slots == 0`. The matcher keeps every game in `byTeams` and `byID`. Every tick sorts all retained game IDs, and every `state()` sorts/copies all slots. Memory and tick work grow with process lifetime, not with active games. At NFL volumes this is small (tens of games per week), but it is an unbounded design in a service meant to stay up.

`gs.slots == 0` games are also skipped by `tick`, so they never get `pruned = true`; a later suspended quote can re-add one as a closed game.

**Fix:** coordinated retention for board and matcher records (e.g., keep a limited revival window, delete after it), and mark zero-slot games pruned so suspended quotes cannot revive them.

## 5. Smaller issues and code-quality notes

- **Debug endpoints are unauthenticated when enabled** ([`main.go:150`](app/cmd/server/main.go#L150)). `POST /debug/kill/{book}` and `/debug/pause/{book}` are on the public listener. They default off, and `fly.toml` does not enable them, but if `DEBUG_ENDPOINTS=1` reaches a public instance any caller (including a cross-site form POST) can disrupt ingestion. Bind them to an internal listener or require a token before public use.
- **SNTP response validation is incomplete** ([`clocksync.go:25`](app/internal/clocksync/clocksync.go#L25)). It checks only length and nonzero stratum; it does not verify response mode (4), LI, version, an echoed originate timestamp, or that RTT/offset are sane. A spoofed or broken reply can mark a bad offset valid and distort latency (not odds). `Best(server, 0, ...)` also returns `(0, 0, nil)` for zero samples. Add protocol-validation tests.
- **`boardctl` misattributes confirmation deltas** ([`boardctl/main.go:208`](app/cmd/boardctl/main.go#L208)). `KindConfirm` falls through to the slot path with key `"|"`, increments per-book delta counts and prints an empty slot. Game removal also leaves `p.games` cached. Handle `KindConfirm` explicitly and delete removed games.
- **`main` exits 0 if the listener fails** ([`main.go:192`](app/cmd/server/main.go#L192)). `ListenAndServe` errors are logged and `stop()` is called, but `main` then falls through to the normal shutdown path and returns nil. A port conflict in production would look like a clean exit. Return a non-zero status (or `os.Exit(1)`) so the platform restarts/alerts.
- **Health publishes can arrive out of order.** `evaluate` computes a state under `s.mu`, unlocks, then calls `SetHealth`. Two concurrent callers (health loop, background poll/resnapshot) can publish an older computed state after a newer one. The next 1 s tick corrects it, but the board/page can briefly show `live` after the stream dropped. Serialize publication or tag readings with a generation.
- **`healthLoop` can outlive `Run`.** If `Run` returns because `ApplySnapshot` failed while the parent context is still alive, the health goroutine keeps ticking until the process context ends. Low, but worth a `done` path.
- **Browser: no sequence-gap detection.** `onDelta` assigns `state.seq = d.seq` without checking `d.seq === state.seq + 1` ([`app.js:163`](app/cmd/server/web/app.js#L163)). A lost/corrupt delta silently leaves the page stale until a reconnect. A gap check can force a resync.
- **Browser: zero-American prices render as `0`.** A suspended Polymarket quote with `convert()` 0 (probability ≤0.001 or ≥0.999) reaches the cell as `American: 0`; `price(0)` shows `"0"` (or `"0.00"`), prefixed "suspended · ". Render `—` when `am === 0`.
- **Browser: health is rebuilt every second.** `setInterval` calls `renderHealth()` which replaces the innerHTML of all health cards ([`app.js:439`](app/cmd/server/web/app.js#L439)); it is cheap at three books but resets focus/hover and will grow with books. Prefer updating text nodes or only re-rendering on change.
- **Browser: `hasPrices`/`isLive` scan all slots per game per render** ([`app.js:298`](app/cmd/server/web/app.js#L298), [`app.js:305`](app/cmd/server/web/app.js#L305)). At 82 games/330 slots it is fine; it is O(games × slots) on every full render (game status delta, health flip, toggle). Keep a per-game index if the board grows.
- **Latency samples outlive a clock change.** The page keeps previously recorded `e2e` samples when the server NTP reading becomes invalid; it shows them with `±?` rather than clearing them, and old samples computed with a previous offset remain in p50. The README says the page records no corrected samples while invalid; recording does stop, but existing samples are still displayed. Consider clearing/bucketing on offset change.
- **FanDuel `sportOf` treats a missing event type as NFL** ([`fanduel.go:69`](app/internal/books/fanduel/fanduel.go#L69)). That is deliberate for trimmed fixtures, but a real event missing `eventTypeId` would be classified as the first league. It also accepts an empty competition ID, which is the one field that keeps esports out; if FanDuel ever omits it on an esports event, the filter passes. Prefer "unknown" over a default.
- **`/api/board`, `/healthz` and `/metrics` copy the full board state per request** ([`board.go:596`](app/internal/board/board.go#L596)). 141 KB raw/23 KB gzipped per request is fine at this scale; a scrape/health check cadence and many clients will multiply it. A cached serialized snapshot (invalidated on seq change) would be cheaper if the board grows.
- **DK `state.gone` is dead and unbounded** ([`state.go:62`](app/internal/books/draftkings/state.go#L62), [`state.go:516`](app/internal/books/draftkings/state.go#L516)). It is set on every expired tombstone, deleted when an ID reappears, and never read. Remove it or use it to suppress late frames. `ev.removed` is only read by the replay test.
- **Book constructors return errors that are ignored** ([`main.go:57`](app/cmd/server/main.go#L57), [`registry.go:19`](app/internal/books/registry.go#L19)). `draftkings.New`, `fanduel.New` and `polymarket.New` currently cannot fail, so `_` is harmless, but the signature invites a future validation to be silently dropped (and a nil book to panic).
- **HTTP server timeouts.** `ReadHeaderTimeout` is set, but there is no `IdleTimeout`/`MaxHeaderBytes` ([`main.go:183`](app/cmd/server/main.go#L183)). SSE must not have a global `WriteTimeout`, but the per-write deadline in F7 plus an idle timeout for non-SSE requests is worth adding. Shutdown waits for `wg.Wait()` with no bound; a book that ignores cancellation can hold SIGTERM until the platform kills it.
- **README/CSS nit:** [`app.css:177`](app/cmd/server/web/app.css#L177) still says "5+ minutes"; the code and README use 3 minutes. [`static.go:12`](app/cmd/server/static.go#L12) says the UI is "built in a later step".

## 6. What I checked and found sound

- **Boundaries and registry.** Book packages do not import the board/supervisor; the registry is the only wiring point; the arch test enforces the DK/FD part (see F12 for the gap).
- **Single-owner board.** All state changes go through `Apply`/`ApplySnapshot`/`SetHealth`; `-race` is clean and the seq is strictly increasing. The subscriber-drop and snapshot-then-deltas paths are tested.
- **Best-price rule (the parts that are reached).** The table tests cover line ordering, total direction, same-line decimal, suspended, stale, probability-only-on-a-book-line and deterministic ties. F2 is the missing input.
- **Replay tests.** DK connection 1 and the 6,849-frame kickoff capture, FD's 1,621 frames, and the pregame/live parity counts are genuinely valuable; the esports regression is real. The supervisor's new slow-reconcile test is a good addition.
- **Page escaping.** Every server-provided string I traced is escaped in HTML text or double-quoted attributes; I found no XSS path. No secrets are embedded (FanDuel's client ID/app key are public frontend values); TLS verification is not disabled.
- **Deployment.** Distroless non-root two-stage image with the static base (the right shape for a pure-Go binary), debug off by default, and a health check configured. The static egress IP is operational config, not code.
- **Error direction.** Adapters generally choose resync/refuse over guessing (unknown teams/markets/IDs), which is the right failure mode. The gaps above are about lifecycle, not parsing philosophy.

## 7. Test gaps and regression tests to add

Highest value, in the order I would add them:

1. `TestStalePollCannotReviveRemovedEntry` / `TestStaleSnapshotCannotRevive...` — F1.
2. `TestDecimalOnlyChangeUpdatesBestAndPage` — F2.
3. `TestEmptyAsksSuspendsPreviousQuote`, `TestAskOnlyQuotePrices` — F3.
4. `TestStaleBookDoesNotHoldGameOpen` — F4.
5. `TestResumeBacklogNotCountedAsLatency` (hub flag + page helper, or a small browser test) — F5.
6. `TestGameRemovalClearsBrowserSlots` — F6 (a DOM test or an extracted state reducer).
7. `TestSSEWriteDeadlineWithRealSlowSocket` — F7.
8. `TestSeedReuseDoesNotRefreshHealth`, `TestSkippedPollDoesNotRefreshHealth` — F8.
9. `TestPolymarketPagination` — F9.
10. `TestBoardSnapshotAfterStop` — F10.
11. `TestBundleFetchBackoff` — F11.
12. `TestChangeCannotReviveTombstone` (selection/market/event) — F13.
13. `TestDKRejectsNilLine` — F14.
14. `TestResnapshotAdvancesSeed` — F15.

Other gaps:

- `internal/clocksync` and every `cmd/` package have no tests; the SNTP validation issues are untested.
- No fuzz tests for the DK/FD/PM JSON decoders and the FanDuel bundle regexes (the most brittle parsing code in the project).
- No browser/E2E test; the page is 586 lines of stateful JS and the removal/replay paths are exactly where bugs live.
- The fanout slow-client test uses an internal channel, not a socket that stops reading.
- Existing tests use real timers/sleeps in a few places (e.g., the ring-rollover test sleeps 50 ms); they pass under `-race` but are candidates for `testing/synctest`-style control.
- Coverage is good where the design is shared (board/supervisor/fanout) and weaker at the transport boundaries (51–62% in the book packages), which matches the risk profile.

## 8. Suggested order of work

1. **F1, F2, F3** — these can show or select a price that is not actually offered.
2. **F4, F6, F5** — user-visible wrong game state, wrong cells, and wrong latency numbers.
3. **F7, F8, F10, F11** — recovery/staleness and blocked-resource issues.
4. **F9, F15, F16, F12** — coverage/retention/extensibility.
5. **F13, F14** and the smaller items — defensive state-machine and UI polish.
6. Add the regression tests above before changing any of the lifecycle rules; several of them fail today for exactly the reasons described.

## 9. Relation to `APP_CODE_QUALITY_REVIEW.md`

That review lists ten findings plus conditional concerns. I independently reached and (where possible) reproduced:

- its #1 = F1, #2 = F2, #3 = F3, #4 = F6, #5 = F7, #6 = F8, #8 = F9, #9 = F16, #10 = F10.

Its **#7** (REST reconciliation blocking the watchdog) was **fixed while I was reviewing**, in commit `4933776` (`Supervisor: REST work never blocks the control loop`) with a new `TestWatchdogFiresDuringSlowReconcile`; the README Section 8.1 documents the bug and the fix. I reviewed the replacement: the loop now only waits on channels, one background job at a time, the job shares the stream context, and the backoff poll is bounded by the backoff deadline. The residual is F15 (seed not advanced), plus the general "a job that ignores its context still delays the return" caveat and the health-publication ordering note.

Findings not in that review: **F4, F5, F11, F12, F13, F14, F15**, plus the browser seq-gap/zero-price notes, the `main` exit code, health-publication ordering, FanDuel's missing-event-type default, and the full-state-per-request copy. Its "Additional conditional and maintenance concerns" already flags the debug endpoints, SNTP validation, `boardctl` diagnostics and the command/browser test gap; I agree with those, so I have kept them brief.

I did not modify code. The temporary probe tests were deleted; the working tree contains only the untracked reviews.
