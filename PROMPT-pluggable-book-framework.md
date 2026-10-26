# Task: refactor the sportsbook research code into a pluggable book framework with a live board

You are working in a Go repository on Windows at `D:\Projects\BitStamp_Interview`. It is the research phase of a take-home for Betstamp (an odds-comparison company). The research works. Your job is to turn it into the **core of the real app**: a framework where each sportsbook is a self-contained plugin, plus a board that holds normalized odds from every book and computes the best price.

Read this whole prompt before writing code. Work in the phases below, in order, and finish each phase's acceptance checks before starting the next one.

---

## 1. Background: what the product is

The take-home asks for a web app that:

- pulls NFL **pregame and live** odds (moneyline, spread, total) from **DraftKings** and **FanDuel** (Ontario sites);
- normalizes both into one model, so the same game, market and side line up across books;
- shows a board with the **best available price** for each game/market/side and the book name under it, plus a toggle between Best Price, DraftKings and FanDuel;
- **pushes updates** to the browser as lines move (no refresh button);
- keeps working when one book is down, and makes it clear which data is stale;
- is as close to real time as possible end to end (book → backend → browser). Latency is what the reviewers care about most;
- runs **one ingestion per book**, not one per browser tab. It must scale to many viewers, and **adding a third book must not require re-architecting.**

This prompt covers the backend core: book plugins, a shared supervisor, normalization, matching, the board store and best price. **HTTP/SSE fanout, the browser UI and deployment are out of scope.** They come in the next task. Design the board's output so that a fanout hub can consume it directly.

---

## 2. What already exists (read these first)

```
D:\Projects\BitStamp_Interview\
  go\                      module "sportsbookfeed" (go 1.27.3, dep: github.com/gorilla/websocket v1.5.3)
    cmd\oddsfeed\          CLI: dk-snapshot, dk-ws, fd-discover, fd-rest, fd-ws, fd-event, pm-*, verify, soak
      soak.go, soak_dk.go, soak_fd.go   long-running harness: reconnect, ping, watchdog, REST reconciliation
    internal\dk\           DraftKings REST snapshot, WebSocket, subscriptions, merge Cache (add/change/remove)
    internal\fd\           FanDuel discovery board, AppSync realtime WS, GraphQL + SMP price polling, bundle parsing
    internal\pm\           Polymarket Gamma REST + CLOB WebSocket
    internal\clock, jsonx  helpers
    README.md, SOAK-1H-RESULTS.md
  normalizer\              separate module "sportsbook-normalizer" (go 1.27.3, no deps)
    internal\model\        canonical Event/Quote/Price, QuoteKey, IdentityKey, ScopeEvent, Slug
    internal\identity\     NFL team registry (mascot-based), MatchEvents (pairwise)
    internal\taxonomy\     ClassifyFD, PeriodFromName, OverUnder, AltYards, ...
    internal\adapters\     DK(path,sport), FD(path,sport), PM(path,sport): snapshot file → BookData
    internal\report\       pairwise DK-vs-FD measurement
    cmd\normctl, cmd\pmctl
    DESIGN.md, REPORT.md, README.md
  journal.md, folder-summary.md, README.md, NBA-*.md, TRANSPORT-SPEED.md, DK-POLLING-FIX.md
  out\                     gitignored captured data (soak runs, normalized dumps)
  captures\                gitignored raw captures. READ-ONLY on this machine; never write here
```

Read `journal.md`, `go/README.md`, `normalizer/DESIGN.md` and `normalizer/REPORT.md` before starting. They hold the protocol findings you must keep.

### Verified protocol facts (do not re-research; keep this behavior)

**DraftKings (Ontario, `dkcaon`, NFL leagueId `88808`)**
- Snapshot: `GET https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808`. Akamai returns 403 unless a **current Chrome User-Agent** is sent (implemented in `internal/dk/rest.go`; `DK_USER_AGENT` overrides it). No cookies needed. The response includes `subscriptionPartials`, which feed the WS subscription.
- WebSocket: `wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json`, JSON-RPC `subscribe`. Updates arrive at `message.data.data.{add,change,remove}.{events,markets,selections}` as **partial deltas merged by ID** (`internal/dk/cache.go`). Many selection deltas **do not include `marketId`**, so you must map them by selection ID through an index built from the snapshot. An unknown ID means you need a resync.
- DK closes the socket at about **30 minutes** (code 1006). Reconnect is routine, not an error.
- Selections have `points`, `outcomeType` (Home/Away/Over/Under), `displayOdds.{american,decimal}` (the decimal is rounded) and `trueOdds` (precise). American odds can use a Unicode minus sign.

**FanDuel (Ontario)**
- Discovery: `sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nfl&includePrices=true&_ak=<appkey>`. It is cached (max-age 30), so use it for **structure only**. Main market types: `MONEY_LINE`, `MATCH_HANDICAP_(2-WAY)`, `TOTAL_POINTS_(OVER/UNDER)`. Live board: `/api/in-play`.
- Push: AppSync `wss://pir.on.sportsbook.fanduel.ca/graphql/realtime`, subprotocol `graphql-ws`, `connection_init` → `start` with `subscription OnUpdateMarketPrice($ids)`. Auth is the public `aws_appsync_clientId`, **extracted from FanDuel's main JS bundle** (`internal/fd/bundle.go`). Today it is read from a saved file `captures/fd-main.js`.
- Poll fallback: SMP `POST smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0` with header `X-Application: <appkey>`, batches of 70. It **returns 500 with Go's default User-Agent**; send a browser UA.
- Price frames carry selection IDs and odds but **no runner names**. Keep the mapping from discovery. Selection IDs repeat across markets, so the stable key is `marketId + selectionId`.
- Alt-ladder runners have `handicap: 0`, and the line is only in `runnerName` (`"Los Angeles Lakers +24.5"`). FD alt ladders **include** the main line; DK alt ladders don't.

**Measured results to keep reproducible:** on the pregame NFL fixture (`out/soak-1h/dk/rest/snapshot-0071-20261025T165528Z.json` vs `out/soak-1h/fd/rest/discovery-0008-20261025T165528Z.json`), the current normalizer produces 25/25 matched events, 150 comparable quotes per book, 100% identity coverage and 124/150 exact-line matches.

---

## 3. Problems you are fixing

### 3a. The code is not pluggable (main problem)

Adding Polymarket needed edits in five places: a new feed package (`go/internal/pm`, fine), new CLI commands in `oddsfeed/pm.go` plus a `main.go` switch, a new adapter (fine), a whole new `cmd/pmctl` because the report only handles two books, and an `if t.book == "polymarket"` branch in `normctl`. Specific causes:

1. No shared `Book` interface. Adapters are separate functions with the book name hard-coded, picked by string `if`/`else`.
2. Reporting and matching only compare two books (`report.Pair` has `DKFile`/`FDFile` and A/B fields; `identity.MatchEvents` takes two slices).
3. Classification is split inconsistently: `ClassifyFD` is in the shared `taxonomy` package, but `classifyDK` is inside `adapters/dk.go`.
4. Connection management is copied per book. `soak_dk.go` and `soak_fd.go` each implement their own reconnect loop, capped exponential backoff with jitter, ping loop, watchdog and progress loop.
5. `dk.WS`, `fd.Realtime` and `pm.WS` all have `Read/Ping/Close`, but nothing ties them together.

### 3b. Normalization bugs (confirmed with tests)

1. **`adapters/dk.go` line ~169 (`classifyDK`):** the case `strings.Contains(t, " o/u")` hard-codes `Stat = "assists"`, so every DK over/under prop (points, rebounds, passing yards) is labeled assists. Props are out of scope for the board, but fix it or remove the case; don't carry it over.
2. **Main and alt quotes collide:** `Alt` is not part of `QuoteKey`, and FD alt ladders include the main line. So FD main −6.5 and FD alt −6.5 get the same key, and `report.comparableMap` keeps whichever comes first in Go's random map order. The results are non-deterministic.
3. **The start-time tolerance doesn't work:** `model.ScopeEvent` puts the UTC **date** in the event key, and `MatchEvents` only applies the ±15 min tolerance to events whose keys already match. Two books listing the same game 4 minutes apart on either side of UTC midnight don't match.
4. **The non-NFL team fallback** takes the last word as the mascot, so `Boston Red Sox` and `Chicago White Sox` both become `mlb-sox`. Only the NFL is in scope; make the fallback refuse to guess, not collide.
5. **Live and suspended status is lopsided:** DK never sets `Price.InPlay` and ignores event status (`STARTED`). It only checks market-level `isSuspended`, never selection-level status. FD sets both. The board needs the same live/suspended semantics from every book.
6. **DK decimal uses the rounded `displayOdds.decimal`** and falls back to `trueOdds` only when the display value is missing. Use `trueOdds` for decimal and keep American for display.
7. `taxonomy.PeriodFromName` compiles 12 regexes on every call, and `FuturesSubject` compiles one. Precompile them at package level.

### 3c. Normalization is batch-only

`adapters.DK(path)` and `FD(path)` read whole files and re-classify everything. The live app gets small updates (DK partial frames without market IDs, FD per-market price frames without runner names), so it needs:

- **Structure path (snapshot/bootstrap/resync):** classify every market once and build an index `book + book selection ID (FD: marketId+selectionId)` → board slot.
- **Price path (every update):** look up the slot and update line/price/status. **Never re-classify on the hot path.** If a market or selection is new or unknown, ask the supervisor for a resync rather than guessing.

### 3d. The FanDuel token doesn't refresh itself

The AppSync client ID comes from a saved local bundle. When FanDuel deploys a new frontend, the ID rotates and the system needs a human. Implement automatic refresh: on auth failure, or on a timer, fetch FanDuel's current main bundle (find the script URL from the FanDuel sportsbook HTML the way the browser loads it), run `ParseBundle` on it, and swap in the new auth. Fall back to the last good bundle, then to the saved file. Log every rotation.

---

## 4. Target design

Create a **new Go module** at `D:\Projects\BitStamp_Interview\app` (module path `betstampodds`). **Copy code into it; don't import from `go/` or `normalizer/`.** Leave those two modules untouched so the old and new outputs can be compared. Dependency: `github.com/gorilla/websocket` only. Standard library for everything else.

```
app/
  go.mod
  internal/
    model/          canonical types: Game, Quote, Update, Price, keys (from normalizer/internal/model)
    teams/          NFL registry + resolver (from normalizer/internal/identity), returns ok=false instead of guessing
    taxonomy/       BOOK-NEUTRAL helpers only: PeriodFromName, OverUnder, line parsing (precompiled regex)
    book/           Book interface, Descriptor, Update, Health types, Registry
    supervisor/     one generic runner for every book: backoff, ping, watchdog, resync, health, metrics
    match/          canonical game matching (N books, not pairwise)
    board/          state store, best price, per-book health, deltas with sequence numbers
    books/
      draftkings/   transport (from go/internal/dk) + DK classification + snapshot index + delta → Update
      fanduel/      transport (from go/internal/fd) + FD classification + bundle auto-refresh + SMP fallback
  cmd/
    boardctl/       runs the registry live for N seconds and prints board deltas + health (manual verification)
  testdata/         small golden fixtures copied from out/ (out/ is gitignored, so tests must not depend on it)
```

### 4a. The `Book` contract (adjust names if needed, keep the shape)

```go
package book

type PriceKind string
const (
    PriceAmerican    PriceKind = "american"    // sportsbook odds; eligible for best price
    PriceProbability PriceKind = "probability" // exchange/prediction market; displayed, excluded from best by default
)

type Descriptor struct {
    Name        string    // stable id: "draftkings"
    DisplayName string    // "DraftKings"
    PriceKind   PriceKind
}

// Snapshot is the full structure + current prices. Called at start and on every resync.
type Snapshot struct {
    Games  []model.BookGame   // book's own game records (teams, start, live state)
    Quotes []model.Quote      // canonical quotes with BookSelectionKey set
    AsOf   time.Time
}

type Book interface {
    Descriptor() Descriptor
    // Snapshot fetches structure and prices over REST.
    Snapshot(ctx context.Context) (*Snapshot, error)
    // Stream connects to the book's push channel and sends canonical updates until
    // ctx ends or the connection fails. It returns ErrResync when it sees an ID it
    // cannot map. It must NOT implement its own reconnect/backoff loop; the
    // supervisor does that.
    Stream(ctx context.Context, idx SelectionIndex, out chan<- model.Update) error
}

// Optional interfaces a book MAY implement; the supervisor detects them:
type Poller interface { Poll(ctx context.Context, idx SelectionIndex) ([]model.Update, error) } // fallback/reconcile
type Pinger interface { PingInterval() time.Duration }
```

`model.Update` is the only type that crosses from a book into the board:

```go
type Update struct {
    Book         string
    GameRef      string    // book's game id; board maps it to canonical game
    Identity     string    // IdentityKey (no line): family|period|subject|stat|comparator|outcome
    Line         *float64
    American     int
    Decimal      float64   // precise (DK trueOdds, FD trueOdds)
    Status       Status    // Open, Suspended, Removed
    InPlay       bool
    BookTS       time.Time // book-provided timestamp if the frame has one, else zero
    RecvAt       time.Time // when our process read the frame (monotonic-safe)
}
```

### 4b. Supervisor (one implementation, used by every book)

For each registered book, the supervisor:

1. calls `Snapshot`, loads it into the board, and builds the `SelectionIndex`;
2. calls `Stream`, passing updates to the board as they arrive. **No batching and no buffering delay.** Every update goes through immediately;
3. on stream error: marks the book `degraded`, reconnects with capped exponential backoff and jitter (1 s → 30 s, reset after a healthy period), and **re-snapshots on every reconnect**. Subscribe-and-buffer: start the stream, buffer updates while the snapshot loads, apply the snapshot, then replay the buffer, dropping updates older than the snapshot;
4. on `ErrResync`: re-snapshot without dropping the connection if possible, otherwise reconnect;
5. runs a watchdog. If no frame or pong arrives for `staleAfter`, it closes the connection and marks the book `stale`. Pregame NFL markets can go quiet for minutes, so separate "socket silent but pongs fine" from "socket dead";
6. if the book implements `Poller`, polls on an interval as a **fallback while the stream is down** and as a **periodic reconcile** while it's up. Reconcile fills gaps but never overwrites a newer pushed value;
7. publishes health per book: `live`, `degraded` (stream down, poll working), `stale` (no fresh data beyond threshold), `down`, with `LastUpdateAt`, `ConnectedSince`, reconnect count and frame counters;
8. recovers from panics in book code, so one book crashing never stops another.

The DK and FD packages must contain **no** reconnect, backoff or watchdog code after this phase. Port the behavior from `soak_dk.go` and `soak_fd.go` into `supervisor/` once.

### 4c. Canonical game matching (N books)

- The board owns the canonical game list. A canonical game is `{ID, Away, Home, Start}`, where team IDs come from `teams/`.
- Match each incoming `BookGame` to an existing canonical game by **unordered team pair + nearest start time within ±3 hours**, with **no date in the match key**. If nothing matches, create a new canonical game.
- Canonical game ID: deterministic, e.g. `nfl:<sorted team ids>:<start rounded to the hour in UTC>`, assigned on first sight and then **stable** even if a book later shifts the start by a few minutes.
- Home/away disagreement between books: keep the first book's orientation and log it. Outcomes are team IDs, not "home"/"away", so quotes still line up.
- Unresolvable team names: skip the game, count it in health/metrics, and never guess.

### 4d. Board store and best price

- Slot key: `canonicalGameID | IdentityKey`. Inside each slot, one entry per book: `{Line, American, Decimal, Status, InPlay, UpdatedAt, RecvAt, Prev (previous American and line)}`.
- **The line is a value, not part of the slot key.** That way a main-line move (−3 → −3.5) updates the same slot.
- **The board holds main markets only** (moneyline, spread, total; full game). Classification marks alt markets, and they are dropped before reaching the board. This also removes the main/alt collision.
- **Best price rule:**
  1. Eligible = status `Open`, the book's health is not `stale`/`down`, and `Descriptor.PriceKind == american`.
  2. **The better line for the bettor wins:** spread → higher signed line (+3.5 beats +3; −3 beats −3.5); over → lower total; under → higher total; moneyline has no line.
  3. Same line → higher decimal odds wins.
  4. Exact tie → the book that updated most recently, then book name alphabetically, so the result is deterministic.
  Document the rule in a comment, including the brief's example: `−3 −110` beats `−3.5 +100` for the favorite.
- Every applied update that changes **any** book entry or the best pick produces a `board.Delta` with a **global monotonically increasing `Seq`**, the slot key, the changed book entry, the new best (book, line, American) and `RecvAt`/`AppliedAt` timestamps. Expose `Subscribe() (<-chan Delta, cancel func)` and `Snapshot() (seq uint64, state)`, so a future SSE hub can send snapshot-then-deltas and resume from a `Seq`.
- Health changes produce deltas too. When a book goes stale, every slot where it was best is recomputed and emits a delta.
- Concurrency: one board goroutine owns the state and reads updates from a channel. No global mutex on the hot path. Subscribers get buffered channels, and a slow subscriber is **dropped** (and must resync) rather than blocking the board.

### 4e. Per-book packages

`books/draftkings`:
- transport and merge cache copied from `go/internal/dk`;
- DK classification moved here (from `normalizer/internal/adapters/dk.go`, minus the props bug);
- snapshot → `Snapshot` with the selection index (selection ID → identity, game, market);
- WS delta → `Update`: use the selection index when `marketId` is missing; map event status `STARTED` (and any in-play field) to `InPlay`; use selection-level suspension as well as market-level; American from `displayOdds.american` (handle the Unicode minus), Decimal from `trueOdds`;
- `Poller` = periodic snapshot reconcile (the existing 30 s pattern);
- narrow the WS subscription to NFL main markets if the subscription partials allow it, and document what you did.

`books/fanduel`:
- transport copied from `go/internal/fd`;
- FD classification moved here (from `normalizer/internal/taxonomy` `ClassifyFD` and `adapters/fd.go`);
- discovery + in-play → `Snapshot`, with index keyed by `marketId+selectionId`, keeping runner names from discovery;
- AppSync frame → `Update`s;
- `Poller` = SMP `getMarketPrices` (batches of 70, browser UA) as the fallback and reconcile;
- bundle auto-refresh as described in 3d;
- rediscover every few minutes; if the market set changed, trigger a resubscribe (existing behavior in `soak_fd.go`).

### 4f. Registry

```go
reg := book.NewRegistry()
reg.Register(draftkings.New(draftkings.Config{...}))
reg.Register(fanduel.New(fanduel.Config{...}))
```

**Adding a book must mean adding one package under `internal/books/` and one `Register` line. Nothing else changes.** No `switch` or `if` on book names anywhere outside `internal/books/<name>/`. Enforce this with a test that greps the non-book packages for the literal strings `draftkings` and `fanduel` and fails if it finds any (the registry wiring in `cmd/` is exempt).

---

## 5. Phases and acceptance checks

### Phase 1: module skeleton, model, teams, taxonomy, bug fixes
- Create `app/` with `go.mod`, `model/`, `teams/`, `taxonomy/`.
- Fix bugs 3b.2–3b.7 in the ported code, each with a unit test that fails on the old behavior: UTC-midnight matching, main/alt key separation, Sox non-collision (refuse instead of collide), precompiled regex.
- Accept: `go vet ./...` and `go test ./...` pass.

### Phase 2: book packages (structure path)
- Port DK and FD transport and classification into `books/draftkings` and `books/fanduel`, implementing `Snapshot`.
- Copy **small** golden fixtures into `app/testdata/` (trim them if they're large): the pregame and live DK/FD pairs listed in section 2 (live: `snapshot-0111-20261025T171528Z.json` / `discovery-0012-20261025T171528Z.json`), plus a few DK WS frames and FD AppSync frames pulled from `out/soak-1h/dk/ws.jsonl` and `out/soak-1h/fd/ws.jsonl`.
- Accept (golden tests): on the pregame pair, 25/25 games match to canonical games, 75 main slots exist (25 × ML/spread/total), both books fill 150 book entries, and 124 of those pairs have identical lines (parity with the old normalizer; if you get a different number, explain exactly why in your report). Live pair: 12 games, 36 main slots.

### Phase 3: price path, supervisor, board
- Implement `Stream` for both books, the `SelectionIndex`, the supervisor, matching and the board with best price and deltas.
- Unit tests:
  - DK partial frame without `marketId` updates the right slot via the index;
  - an unknown selection ID returns `ErrResync`;
  - FD price frame without runner names updates the right slot;
  - best-price table test covering every rule in 4d (including −3 −110 vs −3.5 +100, over/under direction, a suspended book, a stale book, a probability-priced book being ignored, deterministic ties);
  - a line move updates the same slot and records `Prev`;
  - a book going stale recomputes best and emits deltas;
  - a slow subscriber gets dropped without blocking;
  - the supervisor reconnects after a fake book's `Stream` fails, re-snapshots, and replays buffered updates in order, dropping ones older than the snapshot. Use a **fake in-memory book** for these tests, not the network;
  - a panic in one fake book does not stop another.
- Run tests with `-race` if the toolchain supports it on this machine. If cgo isn't available, say so and skip it.

### Phase 4: live verification
- `cmd/boardctl -seconds 120` runs the real registry against live endpoints and prints: each delta (seq, game, market, side, per-book line/odds, best book, `AppliedAt − RecvAt` in microseconds), per-book health transitions, and a final summary (games, slots, deltas per book, reconnects, resyncs, eligible-best counts per book).
- Run it at least once and include the summary in your report. If no games are live, say so; pregame movement is still a valid test.
- Force a reconnect (add a `-kill-after 40s` flag that cancels one book's stream) and show the book going `degraded` → `live` with a re-snapshot, while the other book keeps producing deltas.

### Phase 5: extensibility proof
- Add `books/polymarket` (port from `go/internal/pm` and `normalizer/internal/adapters/pm.go`) with `PriceKind = probability`, mains only.
- Accept: the diff for this phase touches **only** `internal/books/polymarket/**`, its tests/fixtures, and **one** `Register` line in `cmd/boardctl`. Polymarket quotes appear in board slots but never as best. If anything outside its package had to change, that's a framework bug: fix the framework in a separate commit and explain it.

---

## 6. Constraints

- **Build environment (this Windows machine):** the sandbox can't write Go's default caches. Before building or testing, run in PowerShell:
  ```powershell
  $env:GOPATH     = 'D:\Projects\BitStamp_Interview\.gopath'
  $env:GOMODCACHE = 'D:\Projects\BitStamp_Interview\.gopath\pkg\mod'
  $env:GOCACHE    = 'D:\Projects\BitStamp_Interview\.gocache'
  $env:TMP        = 'D:\Projects\BitStamp_Interview\app\.gotmp'
  $env:TEMP       = $env:TMP
  New-Item -ItemType Directory -Force $env:TMP | Out-Null
  ```
  Use the same `go` directive as the existing modules (`go 1.27.3`). Add `app/.gotmp/` and any built `.exe` to `.gitignore`.
- **Never** disable TLS verification, use logins, solve CAPTCHAs, use proxies, or spoof location. Public endpoints only, exactly as the research does.
- **Never write to `captures/`** (it's read-only here). Don't modify or delete anything in `go/`, `normalizer/`, `out/` or the root research scripts.
- Rate limits: at most one DK snapshot every 15 s per process, FD discovery every 2 min at most, SMP polls every 5 s at most. Respect `Retry-After` and back off on 403/429.
- No new dependencies beyond `gorilla/websocket`. No global mutable state outside the registry.
- Everything goes through `context.Context`. `Ctrl+C` shuts down cleanly within 2 s.
- Logs: structured `log/slog` JSON lines, one per lifecycle event (connect, disconnect, resync, health change, bundle rotation). No per-frame logging by default.
- Keep functions small, comment the *why* for anything protocol-specific, and give every exported identifier a doc comment.

## 7. What not to do

- Don't build HTTP/SSE, the UI, Docker or deployment. That's the next task.
- Don't add props, alt lines, NBA/NHL or futures to the board. Main NFL markets only.
- Don't add batching, debouncing or tick timers on the update path. Every update is applied and emitted immediately.
- Don't add reconnect logic inside a book package.
- Don't guess unknown IDs, teams or markets into existing slots. Count them, log them, and resync.
- Don't "fix" parity numbers by changing tests. If a number moves, explain why.

## 8. Report back

When you're done, reply with:

1. A short summary of what you built, per phase, and which acceptance checks passed.
2. The final `app/` file tree.
3. Full `go vet ./...` and `go test ./...` output (and `-race` output or the reason it was skipped).
4. The `boardctl` live run summary, plus the forced-reconnect run summary.
5. The file list for the Phase 5 diff, to prove extensibility.
6. Any deviation from this prompt and why.
7. Open issues and what you'd do next, especially anything that affects latency or what happens when a book is down.
