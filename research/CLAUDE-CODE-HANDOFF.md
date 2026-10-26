# Handoff: Betstamp take-home, DraftKings vs FanDuel live NFL odds board

You are picking up a project in progress. It lives at `D:\Projects\BitStamp_Interview` on a Windows machine. Read this whole document before you touch code. Everything here comes from inspecting the code and replaying real captured data in an earlier session. Where a number appears, it was measured, not estimated.

**Rule zero: verify against real data, not against summaries.** A previous agent (DeepSeek) twice reported work as done when the code didn't contain it, and once claimed a frame type didn't exist when there were 825 of them. The acceptance criteria in section 11 are replay tests over real captures. "Tests pass" only counts if those replay tests pass.

---

## Contents

1. What the take-home asks for
2. Environment and hard rules
3. Repo map
4. Current state, and what is wrong with it
5. Verified protocol facts: DraftKings
6. Verified protocol facts: FanDuel
7. Target architecture and the `book` contract
8. DraftKings stream state machine spec
9. FanDuel stream, poller and bundle refresh spec
10. Supervisor and board spec (including best price)
11. Acceptance tests and fixtures
12. Order of work
13. After this: fanout, UI, deploy, README
14. Reporting rules

---

## 1. What the take-home asks for

The take-home is from Betstamp: "DraftKings vs FanDuel". Requirements:

- Pull NFL **pregame and live** odds (moneyline, spread, total) from **DraftKings** and **FanDuel**. Use the Ontario sites, since that's where we are.
- Normalize both into one model: game, market, side, line, odds, book.
- Show a board like the Betstamp app. For each game/market/side, show the **best available price with the book name under it**. A toggle at the top switches between **Best Price / DraftKings / FanDuel**.
- **Push updates to the browser** on their own. No refresh button.
- **Be live at a public URL**, plus a GitHub repo.
- **Keep working when one book is down** or returns something unexpected. The UI must make stale data obvious.
- **Latency is what they care about most:** how old is the number on screen? Measure end to end (book → backend → browser), per book, pregame and live. Defend every choice: transport, snapshot vs delta handling, push method, payload size, batching, hosting location.
- One ingestion per book, not one per browser tab. It has to hold up with many viewers, and **a third book must not require re-architecting.**
- Define "best price" when the books are on different lines (e.g. −3 −110 vs −3.5 +100) and explain the rule.
- README: how to run locally, an architecture diagram, how each book's data is fetched and why, measured latency per book (pregame and live) and how it was measured, what was hit (geo, bot protection, rate limits) and how it was handled, and what's next.
- Extras: flash or show the previous price on change, show per-book freshness on the page, reconnects and stale UI, props/alts/other leagues, what changes for 10 books, and where AI could help.

**Our choices:** Go backend; SSE to browsers; host on Fly.io in Toronto (yyz) near the books, **but first verify that Fly datacenter IPs aren't blocked**. The research was all done from a home Ontario IP. The fallback is collectors on an Ontario machine pushing to a cloud relay.

---

## 2. Environment and hard rules

- **OS:** Windows. **Go:** the `go.mod` files say `go 1.27.3`.
- The sandbox can't write Go's default caches. Before building or testing, in PowerShell:
  ```powershell
  $env:GOPATH     = 'D:\Projects\BitStamp_Interview\.gopath'
  $env:GOMODCACHE = 'D:\Projects\BitStamp_Interview\.gopath\pkg\mod'
  $env:GOCACHE    = 'D:\Projects\BitStamp_Interview\.gocache'
  $env:TMP        = 'D:\Projects\BitStamp_Interview\app\.gotmp'
  $env:TEMP       = $env:TMP
  New-Item -ItemType Directory -Force $env:TMP | Out-Null
  ```
  `github.com/gorilla/websocket v1.5.3` should already be in that module cache, because the `go/` module uses it.
- **Never** disable TLS verification, log in, solve CAPTCHAs, use proxies or spoof location. Public endpoints only.
- **Never write to `captures/`.** It's read-only on this machine. Don't modify `go/`, `normalizer/`, `out/` or the root `.cjs` research scripts. They're the reference implementation and the evidence.
- Rate limits: DK snapshot at most once per 15 s; FD discovery at most once per 2 min; FD SMP poll at most once per 5 s. Honor `Retry-After`, and back off on 403/429.
- Only dependency: `gorilla/websocket`. Standard library otherwise. `log/slog` for structured logs, one line per lifecycle event and no per-frame logging by default.
- Everything takes `context.Context`. Ctrl+C shuts down within 2 s.
- The project isn't a git repo yet. `git init` before you start, and commit per phase. `.gitignore` already covers `captures/`, `out/`, `work/` and the Go caches. Also add `app/.gotmp/` and `*.exe`.

---

## 3. Repo map

```
D:\Projects\BitStamp_Interview\
  go\                 module "sportsbookfeed": research Go clients, soak-tested for 3h. REFERENCE CODE.
    cmd\oddsfeed\     CLI: dk-snapshot, dk-ws, fd-discover, fd-rest, fd-ws, fd-event, pm-*, verify, soak
      soak_dk.go      DK reconnect/ping/watchdog/REST-reconcile loop  <- logic to port into supervisor
      soak_fd.go      FD equivalent, incl. bundle loading, rediscovery, resubscribe
    internal\dk\      ws.go (Dial/Read/Ping/OnPong), subscribe.go (PartialsSubscribeMessages), rest.go, types.go, cache.go
    internal\fd\      ws.go (AppSync graphql-ws: RealtimeURL, DialRealtime, SendInit, Start, ParseFrame),
                      rest.go (Discover, InPlay, GraphQL GetMarketPrices, SMP PollMarketPrices), bundle.go, types.go
    internal\pm\      Polymarket (for the "third book" demo later)
  normalizer\         module "sportsbook-normalizer": offline normalization prototype + measurement report
  app\                module "betstampodds": THE APP BEING BUILT (see section 4)
  out\soak-1h\        1h soak capture, 2026-10-25 16:20-17:20Z, spans NFL 1pm kickoff. KEY TEST DATA.
    dk\ws.jsonl       every DK frame (13.5 MB): {"at","monoMs","conn","kind":"sent|received","payload":"<json string>"}
    dk\rest\          bootstrap-0001-20261025T162028Z.json (conn 1), bootstrap-0002-20261025T165029Z.json (conn 2),
                      snapshot-NNNN-*.json every 30s
    fd\ws.jsonl       every FD AppSync frame (1.8 MB)
    fd\rest\          discovery-*.json, SMP batches
  out\soak-3h\        3h soak, same layout
  journal.md          detailed research log (read sections 1-3)
  README.md           protocol overview (its Oct 24 "DK blocker" section is superseded: the fix was the User-Agent)
  PROMPT-pluggable-book-framework.md   the spec given to DeepSeek (this document supersedes it)
```

---

## 4. Current state, and what is wrong with it

`app/` was written by DeepSeek. Treat it as a draft: keep what's verified good, and replace the rest.

**Good, keep:**
- `internal/teams`: NFL registry with no last-word guessing (the old fallback turned Red Sox and White Sox into one team), with tests.
- `internal/match`: canonical game matching by unordered team pair + nearest start within a tolerance, with no date in the key. The UTC-midnight test passes.
- `internal/taxonomy`: book-neutral helpers with regexes precompiled.
- `internal/model`: `IdentityKey` (no line; includes main/alt class, so main and alt quotes can't collide), `QuoteKey`, `BoardSlotKey` (refuses an empty canonical game), `GameKey`.
- DK and FD **REST snapshot** parsing. Parity with the old normalizer is exact: pregame 25 games / 150 slots / 150 both books / 124 same line; live 12 / 72 / 72 / 58 (`internal/books/parity_test.go`). FD uses `_ak` in the discovery URL, gets home/away from runner `result.type`, has `Skipped` counters and a full-game guard.

**Wrong or missing:**
1. **`Stream` is a stub in both books.** There's no WebSocket code at all, and `gorilla/websocket` isn't in `app/go.mod`.
2. **The DK stream state machine (`internal/books/draftkings/stream.go`) breaks at kickoff.** Replaying the full second soak connection (6,849 update frames against `bootstrap-0002`) gives **168 resyncs**. The causes:
   - `add.markets` and `add.events` are parsed but **never handled**. `apply()` only reads `change.events`, `change.markets` and selections.
   - `selectionsByMarket` is never updated after the snapshot, so suspensions and removes on **live markets** (every market after kickoff) do nothing.
   - The lookup never checks `tombstones[sel.ID]`, so a market that's pulled and re-added with the **same** selection IDs causes a resync.
   - DeepSeek's report claimed `add.markets`/`add.events` were implemented. They aren't.
3. Its kickoff regression test only replays frames 260–340 of connection 2, which is one game, and it skips `Removed` updates in its assertions. It isn't sufficient.
4. No supervisor, no board, no `cmd/boardctl`, no FD SMP poller, no FD bundle refresh.
5. The `book.Book` interface passes a bare `SelectionIndex` to `Stream`. That isn't enough: DK needs event, market and selection state. Replace it with the contract in section 7.

**Recommendation:** keep `teams`, `match`, `taxonomy`, `model` (extended as in section 7) and the snapshot parsing. Rewrite `internal/book`, the DK stream, and everything else, following this document.

---

## 5. Verified protocol facts: DraftKings (Ontario, `dkcaon`, NFL leagueId 88808)

### REST snapshot
- `GET https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808`
- **Akamai returns 403 unless the User-Agent is a current Chrome.** `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36` returns 200. Go's default UA, a Chrome 142 UA and no headers all return 403. No cookies needed. `DK_USER_AGENT` overrides it. `Cache-Control: public,max-age=1`, so it's fresh and cheap enough to use for reconcile.
- The body has `events`, `markets`, `selections`, `categories`, `subcategories` and **`subscriptionPartials`**. In the soak:
  ```json
  {"league-events-88808": {"entity":"events",
     "query":"$filter=leagueId eq '88808' and clientMetadata/Subcategories/any(s: s/Id eq '4518')&$orderBy=startEventDate asc",
     "includeMarkets":"$filter=tags/all(t: t ne 'SportcastBetBuilder') and clientMetadata/subCategoryId eq '4518'"},
   "lcc-event-": {"entity":"LiveContentCardEvent"},
   "cc-event-": {"contentCardType":"All","entity":"ContentCardEvent"}}
  ```
  Subcategory **4518 is game lines**, so the `league-events-88808` partial is already narrowed to main markets. **Subscribe only to partials with `entity == "events"`.** The content-card ones aren't needed.
- Main market type names are exactly `Moneyline`, `Spread` and `Total`. The NFL board had 25 of each. Alternates also carry `main:true`, so classify by type name plus a full-game period check, never by that flag.
- Selections: `id`, `marketId`, `label`, `outcomeType` (Home/Away/Over/Under), `points`, `displayOdds.american` (may use the **Unicode minus `−`**, or `EVEN`), `displayOdds.decimal` (rounded), `trueOdds` (precise; use this for decimal), `tags`.

### WebSocket
- `wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en-US`, no subprotocol.
- Subscribe frame (one per partial; see `go/internal/dk/subscribe.go` `PartialsSubscribeMessages`): JSON-RPC `{"jsonrpc":"2.0","method":"subscribe","id":"race-N","params":{"entity":..,"queryParams":{...partial minus entity..., "initialData":false,"projection":"sportsbook"},"forwardedHeaders":{},"clientMetadata":{"feature":"leagueSubcategory","X-Client-Name":"web","X-Client-Version":"2643.4.1.10"},"jwt":"","siteName":"dkcaon"}}`.
- Acks: `{"id":"race-0","event":"subscribed",...}`. Updates: `{"id":..,"event":"update","data":{"data":{"add":{events,markets,selections,...},"change":{...},"remove":{...}},"metadata":{"createdTime":..,"receivedTime":..}},"websocketPublishTimestamp":"2026-10-25T16:20:28.1589391+00:00"}`.
- **Use `websocketPublishTimestamp` as `Update.BookTS`.** That gives true book → backend latency. `data.metadata.createdTime`/`receivedTime` are also there and worth logging for the latency write-up.
- **DK closes the socket at about 30 minutes** (code 1006). Reconnect is routine; the soak reconnected in 1.6 s.
- Change deltas are **partial**. In the soak, every `change.selections` had `displayOdds` and `trueOdds`, but **1,291 of 3,162 had no `points`**. Keep the last known line per selection.
- **Removes are arrays of ID strings**, not objects: `"remove":{"events":[],"markets":[],"selections":["0OU86921430O2950_1"]}`. The 1 h soak had **825 frames with non-empty removes** (738 selections, 66 markets, 21 events).

### Line moves: the most important DK fact
**DK selection IDs encode the line**: `0OU86921430O2950_1` is Over 29.5, and `0HC86921430N650_3` is −6.5. In 7,279 frames **no selection ID ever changed its `points`**. A line move arrives as:
- `add.selections`: a **new** selection with `marketId`, `outcomeType`, `points`, `trueOdds`, `tags` (`MainPointLine`) and **`replacedSelectionId` = the old ID** (all 654 adds in the soak had it), and
- `remove.selections: ["<old id>"]`, in a **separate frame**.

Ordering, measured over all 654 replacements: the add and remove are **never in the same frame**. 616 of 654 (94%) were within 1 s; the median gap is about 0, and the 10th percentile is the remove arriving 135 ms *before* the add. The order is a coin flip: **remove first 321 times, add first 333 times**. Every replaced ID was eventually removed. `change.selections` frames also carry `replacedSelectionId` (2,791 of 3,162) as history; it isn't a new replacement there.

Moneyline replacements can land under a **different market ID** (e.g. `0ML86921438_1` replacing `0ML84695606_1`). So `replacedSelectionId` must be checked before any market lookup.

### Kickoff: the second most important DK fact
At kickoff, DK **tears down the pregame event and re-adds it as live with new market IDs**. Traced for event 34118173:
```
17:02:28.977  remove.events    34118173
17:02:29.453  remove.markets   2_84695606            (pregame spread)
17:02:33.210  remove.selections 0HC84695606N650_1
17:02:35.162  add.events       34118173 status=STARTED   (SAME event id; startEventDate becomes actual kickoff 17:02:25Z)
17:02:35.285  add.markets      1_86921438, 2_86921438, 3_86921438   (NEW market ids)
17:02:35.478  add.selections   0HC86921438N650_1  replacedSelectionId=0HC84695606N650_1
```
Across the 7 kickoffs in the capture, the `remove.events` → `add.events` gap was **3.3, 5.4, 6.0, 6.1, 6.2, 6.4 and 6.8 s**. `add.events` carries full `participants` (with `venueRole`), `status`, `liveGameState` and `eventScorecard`. `add.markets` carries `id`, `eventId`, `name`, `isSuspended`, `marketType.name`, and `marketIdentifier.isLive`.

### Live market flapping
Live markets get pulled and re-added with the **same IDs**:
```
17:03:53.883 remove.markets 3_86921422
17:03:54.037 remove.selections 0OU86921422O4450_1, 0OU86921422U4450_3
17:03:54.166 add.markets 3_86921422                          (same id)
17:03:54.166 add.selections 0OU86921422O4450_1  replacedSelectionId=0OU86921422O4350_1   (same selection id re-added)
```
Live suspensions: the soak had **649 `change.markets`** frames, 79 on main markets, 53 with `isSuspended:true`. There were also **1,952 `change.events`** frames carrying live status.

### Events pulled for real
**13 events were removed and never re-added: they're next week's games** (start times Oct 30 to Nov 3), pulled between 17:01:13 and 17:01:43 as the 1 p.m. games went live. They're also missing from the later REST snapshots. Emitting `Removed` for them after the grace window is correct. No resubscribe is needed.

---

## 6. Verified protocol facts: FanDuel (Ontario)

- **App key** (public frontend key, not a credential; it can rotate): `_ak=FhMFpcPWXMeyZxOx`.
- **Discovery:** `GET https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nfl&includePrices=true&_ak=<ak>`. It's cached (`max-age=30`, `swr=60`), so use it for **structure only**. Live board: `GET https://sbapi.on.sportsbook.fanduel.ca/api/in-play?_ak=<ak>&includeTabs=true`. Main types: `MONEY_LINE`, `MATCH_HANDICAP_(2-WAY)`, `TOTAL_POINTS_(OVER/UNDER)`. Shape: `attachments.events{eventId,name,openDate}`, `attachments.markets{marketId,eventId,marketName,marketType,marketStatus,inPlay,runners[{selectionId,runnerName,handicap,runnerStatus,result{type: HOME|AWAY|OVER|UNDER},winRunnerOdds{americanDisplayOdds{americanOddsInt},trueOdds{decimalOdds{decimalOdds}}}}]}`.
- **Push:** AppSync `wss://pir.on.sportsbook.fanduel.ca/graphql/realtime?header=<base64({"Authorization":<clientId>,"host":"pir.on.sportsbook.fanduel.ca"})>&payload=e30=`, subprotocol `graphql-ws`. Send `{"type":"connection_init"}`, wait for `connection_ack` (it says `connectionTimeoutMs: 300000`), then send `start`: `{"id":"..","type":"start","payload":{"data":"<JSON STRING of {query, variables:{ids:[marketIds]}}>","extensions":{"authorization":{"Authorization":<clientId>,"host":..}}}}`. Then expect `start_ack`, with `ka` keep-alives along the way. Port this from `go/internal/fd/ws.go`; it works.
- **Data frame** (real):
  ```json
  {"id":"soak-prices","type":"data","payload":{"data":{"onUpdateMarketPrice":{
    "id":"801.191654450","inplay":false,"marketId":"801.191654450","marketStatus":"OPEN",
    "runnerDetails":[{"handicap":0.0,"runnerStatus":"ACTIVE","selectionId":50201,
       "winRunnerOdds":{"americanDisplayOdds":{"americanOdds":160.0,"americanOddsInt":160},
                        "decimalDisplayOdds":{"decimalOdds":2.6},
                        "trueOdds":{"decimalOdds":{"decimalOdds":2.6}}}}, ...]}}}}
  ```
  Each frame is the **full market state**: all runners, with handicap (the line for spread and total; 0 for moneyline, which should become nil), status and price. There are **no runner names**, so keep the discovery mapping. **Selection IDs repeat across markets**; the stable key is `marketId + ":" + selectionId`. The frames have no book timestamp, so FD latency is measured only from `RecvAt` onward. Say so in the README.
- **Auth:** the `aws_appsync_clientId` and both GraphQL documents (`subscription OnUpdateMarketPrice($ids: [String!]!) {...}` and `query GetMarketPrices($ids: [String]!) {...}`) are extracted from FanDuel's **main JS bundle** using the regexes in `go/internal/fd/bundle.go`. Today they're read from `captures/fd-main.js`. The bundle URL looks like `https://on.sportsbook.fanduel.ca/static/js/main.<hash>.js`; the hash changes on each frontend deploy (it was `main.ea04e06bc3f1ff8b.js`).
- **SMP poll** (fallback and reconcile): `POST https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0`, body `{"marketIds":[...]}` in **batches of 70**, header `X-Application: <ak>`, `Content-Type: application/json`, `Accept: */*`. **It returns HTTP 500 with an empty body if the User-Agent is Go's default**, so send the Chrome UA. The response is `no-cache`, a JSON array of markets with `runnerDetails`. The frontend polls every 5 s.
- **GraphQL snapshot:** `POST https://pir.on.sportsbook.fanduel.ca/graphql`, `GetMarketPrices` with the `Authorization` header. It also works.
- **Measured push vs poll** (FD, 2 min, 18 live markets, 5 s polling): the socket was first on **76 of 76** changes, median lead **1,803.5 ms**, p95 **4,079.6 ms**. DK: the socket was first on 5 of 6, median lead 2,214.5 ms vs 1 s polling. These are the research numbers for the README.
- **Soak:** 3 h, with **99.917% DK / 99.951% FD** uptime and 0 REST errors. FD resubscribed when its discovery market set changed. That logic is in `soak_fd.go`.

---

## 7. Target architecture and the `book` contract

```
DraftKings ──WS deltas + REST──▶ DK book ─┐
                                          ├─▶ supervisor (one per book: snapshot, stream, reconnect,
FanDuel ──AppSync push + SMP poll──▶ FD book ─┘    backoff, heartbeat watchdog, poll fallback, health)
                                                  │  model.Update
                                                  ▼
                                    board (single goroutine): canonical game matching,
                                    slot state, best price, per-book health, Delta{Seq}
                                                  │
                                                  ▼
                                    fanout hub (next task): SSE snapshot-then-deltas to N browsers
```

All of this runs as goroutines in **one binary**. A book's package does fetching, parsing and mapping **only**. All reconnect/backoff/watchdog logic is written **once**, in the supervisor.

### `internal/book/book.go` (use this exact shape)

```go
package book

type PriceKind string
const (
    PriceAmerican    PriceKind = "american"    // sportsbook odds; eligible for best price
    PriceProbability PriceKind = "probability" // exchange/prediction market; shown, never best
)

type Descriptor struct { Name, DisplayName string; PriceKind PriceKind }

// Snapshot: full structure + prices over REST. Also the seed a Stream starts from.
type Snapshot struct {
    Games        []model.BookGame
    Quotes       []model.Quote
    AsOf         time.Time       // when the request was SENT; anything received after it is newer
    Skipped      map[string]int  // every unmapped record, counted; books never guess
    Subscription string          // what the stream subscribes to; if it changes, supervisor restarts the stream
    Private      any             // book-owned seed (indexes, raw subscription data); opaque to everyone else
}

type Emitter interface {
    Emit(ctx context.Context, u model.Update) error // blocks until accepted or ctx done
    Connected()                                    // push channel subscribed and live
    Heartbeat()                                    // any sign of life: frame, ka, pong
}

type Book interface {
    Descriptor() Descriptor
    Snapshot(ctx context.Context) (*Snapshot, error)
    // Stream emits until ctx ends or the connection fails. Returns ErrResync on anything it cannot map.
    // MUST NOT reconnect or back off.
    Stream(ctx context.Context, seed *Snapshot, em Emitter) error
}

// Optional: cheap price-only REST read, used as the fallback while the stream is down and as periodic reconcile.
type Poller interface { Poll(ctx context.Context, seed *Snapshot) ([]model.Update, error) }

// Optional per-book timing; zero values = supervisor defaults.
type Tuning struct { ReconcileEvery, ResnapshotEvery, MinSnapshotGap, HeartbeatTimeout time.Duration }
type Tuner interface { Tuning() Tuning }

var ErrResync = errors.New("book: resync required")

type Registry struct{ books []Book }   // Register(b Book) panics on nil/empty/duplicate name; Books() returns a copy
```

The `Stream` must **not mutate the seed**. Each stream run deep-copies `Private` into its own state. Snapshots are taken again during reconcile while a stream is running, so sharing state would be a data race.

### `model.Update` (extend the existing type)

```go
type Source string
const ( SourceStream Source = "stream"; SourcePoll Source = "poll"; SourceSnapshot Source = "snapshot" )

type Update struct {
    Book     string
    GameRef  string    // the BOOK's game id (board maps it to the canonical game)
    Identity string    // model.IdentityKey: family|period|subject|stat|comparator|class|outcome (no line)
    Line     *float64  // nil for moneyline
    American int
    Decimal  float64   // precise (DK trueOdds, FD trueOdds)
    Status   Status    // open | suspended | removed
    InPlay   bool
    BookTS   time.Time // book publish time if the feed has one (DK yes, FD no)
    RecvAt   time.Time // when OUR process read the frame/response; stamp ONCE per frame at socket read
    Source   Source
    AsOf     time.Time // poll/snapshot only: request send time
}
func UpdateFromQuote(q Quote, src Source, asOf, recvAt time.Time) Update
func ParseIdentity(identity string) (IdentityParts, bool)  // split IdentityKey back into facets (board needs family/outcome)
func MainIdentity(family, outcome string) string           // identity of a full-game main quote
```

Identity outcomes: moneyline and spread use the **team ID** (`nfl-vikings`); totals use `over`/`under`. Never use "home"/"away" as the outcome.

Enforce extensibility with a test. Grep every non-book package (everything except `internal/books/**` and `cmd/**`) for the strings `draftkings` and `fanduel`, and fail if either appears.

---

## 8. DraftKings stream state machine spec

Put this in `internal/books/draftkings/stream.go` as a pure state machine with an **injectable clock**, driven by `apply(ctx, msg, recvAt)`. The transport (`transport.go`) dials, subscribes, reads, stamps `RecvAt` at read time, calls `apply`, runs a **250 ms ticker** that calls `expire`, pings every 30 s, and calls `em.Heartbeat()` on every frame and pong and `em.Connected()` after the first `subscribed` ack. Port dial/ping/pong from `go/internal/dk/ws.go`. On read error, return the error; the supervisor reconnects.

### State (seeded from the snapshot, deep-copied per stream run)
```
events  map[eventID] {homeID, awayID string; live bool; removed bool}
markets map[marketID]{family, stat, comparator, eventID string; suspended bool; sels set[selID]}
ignored set[marketID]                 // known non-main markets: their selections are skipped, never resync
sels    map[selID]  {identity, gameRef, marketID string; line *float64; american int; decimal float64; selSuspended bool}
owner   map[gameRef|identity] selID   // which live selection currently owns a board slot
tombs   map[selID]  {sel; expiry time.Time}
```
The emitted `Status` is `suspended` if the market **or** the selection is suspended, otherwise `open`. `InPlay` is `events[gameRef].live`.

### Per frame, in this order
1. **`add.events`, `change.events`:** upsert the event. Resolve teams from `participants[].venueRole`; if the participants are missing, keep the existing teams. `live = status in {STARTED, IN_PROGRESS, LIVE}` (case-insensitive). If `live` flipped, re-emit every selection of that event so `InPlay` updates. A re-added event uses the **same ID**, so it keeps the same `gameRef` and the canonical game doesn't change. An `add.events` for an event never seen before, with resolvable teams, is a new game, and the board can't place it without a snapshot. Return `ErrResync`; that's rare and correct.
2. **`add.markets`:** classify with `marketType.name` ∈ {Moneyline, Spread, Total} **and** `PeriodFromName(name) == full`. If it's main and its event is known, upsert the market (keep the existing `sels` set if it's a re-add). Otherwise add it to `ignored`.
   **`change.markets`:** update `suspended` if `isSuspended` is present (it's a pointer; absent means unchanged). If it changed, re-emit every selection in the market.
3. **`add.selections` + `change.selections`** (adds first): resolve the selection in this order:
   1. `sels[sel.id]` → a normal price/status change.
   2. `tombs[sel.id]` → the **same-ID re-add after a market or event flap**. Revive it.
   3. `sels[replacedSelectionId]`, then `tombs[replacedSelectionId]` → a **line move**. Inherit identity and `gameRef`, and remove the old ID from `sels`, `tombs` and its market's set. Skip this step for `change.selections`, where `replacedSelectionId` is only history.
   4. `markets[sel.marketId]` + `outcomeType` (HOME/AWAY → team ID, OVER/UNDER → over/under) → a new selection in a known main market.
   5. `ignored[sel.marketId]` → skip.
   6. Otherwise → `ErrResync`.

   **Merge partial fields:** a nil `points` keeps the last line, a missing `displayOdds` keeps the price, a nil `trueOdds` keeps the decimal, and an empty `status` keeps the selection status. Update `markets[marketId].sels` (a selection's `marketId` can change on a moneyline replacement). Set `owner[gameRef|identity] = sel.id`. Emit.
4. **`remove.selections`:** tombstone with a **2 s** grace. **Never shorten an existing tombstone's expiry**: keep the max.
   **`remove.markets`:** tombstone every selection in the market with **15 s** grace, then delete the market (`add.markets` recreates it).
   **`remove.events`:** tombstone every selection of the event with **15 s** grace, and mark the event removed (keep its teams).
5. **`expire(now)`:** for each tombstone past its expiry, delete it. Emit `Removed` (with the last line) **only if** `owner[gameRef|identity]` is empty or still equals this ID, so an old ID never wipes a slot its replacement now owns.

Ignore any frame whose `event` isn't `update`. Count `subscribed` acks.

**Seed lines from the snapshot**, so a `Removed` or price-only update for a spread or total always carries a line.

---

## 9. FanDuel stream, poller and bundle refresh spec

### Seed (from discovery)
- `markets map[marketId]{family, gameRef, inPlay bool, runners map[selectionId]{identity, outcome}}`, plus the last line and price per runner.
- `Snapshot.Subscription` = sorted, joined main market IDs. If rediscovery changes the set, the supervisor restarts the stream with the new seed. That's the existing `soak_fd.go` behavior.
- Discovery is pregame-heavy and cached. Also fetch `/api/in-play` and merge it, so live games are present.

### Stream (`transport.go`)
- Get auth from the bundle provider (below), then dial, `connection_init`, wait for `connection_ack`, `start` with all main market IDs (one subscription; it handled about 80 IDs fine in the soak), and wait for `start_ack` → `em.Connected()`.
- On every frame, `ka` and pong → `em.Heartbeat()`. Ping every 30 s.
- On `data`: for each runner in `onUpdateMarketPrice.runnerDetails`, look up `markets[marketId].runners[selectionId]`.
  - Unknown market → `ErrResync`. Unknown runner on a known market → `ErrResync`.
  - Line = `handicap` for spread/total; nil for moneyline.
  - Status: market `SUSPENDED` (or anything other than `OPEN`) or runner not `ACTIVE` → suspended.
  - `InPlay` = the frame's `inplay`.
  - Emit with `BookTS` zero and `RecvAt` = read time.
- On `error` or `connection_error` frames (including auth failures): return an error that wraps `ErrAuth`, so the supervisor forces a bundle refresh before the next connect.

### Poller (SMP)
`Poll` POSTs SMP in batches of 70 with the Chrome UA, maps the runners exactly like stream frames, and returns updates with `Source=poll` and `AsOf` = request send time.

### Bundle provider (fixes the "needs a human when the token rotates" problem)
- Order: in-memory last-good → **live fetch** → `captures/fd-main.js` (read-only fallback; `FD_MAIN_JS` overrides the path).
- Live fetch: `GET https://on.sportsbook.fanduel.ca/` with the Chrome UA, find `/static/js/main\.[0-9a-f]+\.js` in the HTML, GET it, and run `ParseBundle`. If the client ID isn't found, also try the `dependencies.<hash>.js` bundle.
- Refresh on `ErrAuth` and every 6 h. Log every rotation (old and new client-ID prefix, bundle URL).
- **This hasn't been verified live.** The homepage may be bot-protected; Playwright capture was blocked earlier. If it is, report that clearly, and keep the file fallback plus a clear stale/down health state.

---

## 10. Supervisor and board spec

### Supervisor (`internal/supervisor`), one goroutine tree per book
```
loop:
  snap := Snapshot()           // respect MinSnapshotGap; on error: health=down (or degraded if Poller works), backoff
  board.ApplySnapshot(book, snap)
  run Stream(streamCtx, snap, emitter) in a goroutine, plus:
    - heartbeat watchdog: no Heartbeat for HeartbeatTimeout (default 90s) -> cancel streamCtx
    - reconcile ticker (ReconcileEvery, default 30s): Poll() if Poller else Snapshot();
      a reconcile Snapshot also goes to board.ApplySnapshot; if snap.Subscription changed -> restart the stream
    - resnapshot ticker (ResnapshotEvery, 0 = off): structure refresh, same restart rule
    - one extra reconcile ~2s after Connected(), to cover frames missed between snapshot and subscribe
      (initialData:false on DK means there is no replay)
  on Stream return:
    ErrResync     -> immediate resnapshot (still rate-limited by MinSnapshotGap)
    other error   -> health=degraded; backoff 1s -> 30s, x2, full jitter; reset after 60s connected
    while waiting -> if Poller: poll every max(5s, MinPoll) and apply, so prices stay fresh
  recover() panics in book code -> log, treat as a stream error
```
Health per book: `live` (connected, recent heartbeat), `degraded` (stream down but polls succeeding), `stale` (no successful data for more than `StaleAfter`, default 30 s, while not live), `down` (snapshots failing). Publish `{state, since, lastDataAt, connectedSince, reconnects, resyncs, frames, updates, lastError}` to the board on every change.

### Board (`internal/board`), single goroutine fed by a channel
- **Matching:** on `ApplySnapshot`, resolve each `BookGame` through `match.Matcher` (tolerance ±3 h) to a canonical game, and remember `(book, BookGameID) → canonicalID`. Updates whose `GameRef` is unknown are counted and dropped; the next snapshot fixes them.
- **Slot key:** `canonicalGameID | Identity`. Each slot holds one entry per book: `{Line, American, Decimal, Status, InPlay, UpdatedAt (=RecvAt), BookTS, Prev{Line, American}}`. **Main markets only.** The line is a value, not part of the key, so a line move updates the same slot.
- **Applying an update:**
  - Poll/snapshot updates **never overwrite** an entry whose `UpdatedAt` is after their `AsOf`.
  - If nothing visible changed (line, price, status, in-play), refresh the timestamps silently and emit no delta.
  - Otherwise set `Prev` and recompute best for the slot.
- **ApplySnapshot** also removes that book's entries that are missing from the snapshot, but only if they're older than `snap.AsOf`.
- **Best price** (put this comment in the code):
  1. Eligible: status `open`, the book's health is not `stale`/`down`, and `PriceKind == american`.
  2. **The better line for the bettor wins:**
     - spread → higher signed line (+3.5 beats +3; −3 beats −3.5);
     - over → lower total;
     - under → higher total;
     - moneyline has no line.

     That's why `−3 −110` beats `−3.5 +100` for the favorite: a half point through 3 is worth more than 10 cents in the NFL.
  3. Same line → higher decimal odds.
  4. Exact tie → most recently updated book, then book name, so the result is deterministic.
- **Deltas:** `Delta{Seq (global, monotonic), Kind (slot|health|game), GameID, Identity, Book, Entry, Best{Book, Line, American} or none, RecvAt, BookTS, AppliedAt}`. Emit one whenever any entry or the best pick changes. A health change to stale/down recomputes every slot where that book was best and emits deltas.
- **API:** `Subscribe() (<-chan Delta, cancel)` and `Snapshot() (seq, State)`, so the SSE hub can send snapshot-then-deltas and resume from a `Seq`. Subscriber channels are buffered (e.g. 1024). **A full subscriber is dropped** (its channel is closed and it must resync); it never blocks the board.
- **Latency stats per book:**
  - `AppliedAt − RecvAt` (our processing, in microseconds);
  - `RecvAt − BookTS` (book → us; DK only);
  - p50/p95/max over a rolling window.

  `boardctl` prints these, and the page shows them later.

---

## 11. Acceptance tests and fixtures

`out/` is gitignored, so **copy trimmed fixtures into `app/testdata/`**. Trim DK frames by dropping heavy fields you don't parse (`eventScorecard`, `liveGameState`, participant `metadata`, selection `participants`, `fractional`/`percentage`/`multiplier` odds, and so on) but keep every frame. Keep `at` and `conn`.

These must all pass, run with `-race` where the toolchain supports it:

1. **DK full soak replay, connection 1** (all of conn 1 against `bootstrap-0001`), with a fake clock set to each frame's `at` and `expire` called with the clock: **0 resyncs**. In-play updates appear; no nil line on spread/total updates.
2. **DK full soak replay, connection 2** (all **6,849** update frames against `bootstrap-0002`, spanning the 1 p.m. kickoff): **0 resyncs**. No `Removed` for any selection that's later replaced or re-added. Every game that kicks off ends with `InPlay=true` on its updates. No nil lines on spread/total. The only `Removed` slots are the 13 next-week events (78 selections) plus anything really removed. Print the count and justify it in your report.
3. DK unit tests (fake clock): add-then-remove and remove-then-add each give exactly one update with the new line and no `Removed`; a remove with no add gives `Removed` after 2 s; a market or event removal followed by a re-add within 15 s gives no `Removed`; same-ID re-add revives; a moneyline cross-market replacement keeps identity and game; `change.markets` suspension emits suspended for every selection in that market (including live markets added by `add.markets`); a selection-level remove doesn't shorten an event tombstone; a stale tombstone can't wipe a slot owned by its replacement.
4. **FD replay:** all data frames in `out/soak-1h/fd/ws.jsonl` against the matching discovery snapshot from `out/soak-1h/fd/rest/` (each FD connection starts with a discovery). Unknown markets must equal markets missing from that snapshot (the subscription is built from it, so there should be 0 if you pair them correctly). Line comes from handicap; moneyline line is nil.
5. Snapshot parity stays at 25/150/150/124 pregame and 12/72/72/58 live (`internal/books/parity_test.go`).
6. **Best-price table test**, covering every rule in section 10, including `−3 −110` vs `−3.5 +100`, both total directions, suspended, stale-book, probability-book and deterministic ties.
7. **Board:** a line move updates the same slot and records `Prev`; a poll older than a stream update doesn't overwrite it; a stale book recomputes best and emits deltas; a slow subscriber is dropped without blocking; `Seq` is strictly increasing.
8. **Supervisor with a fake in-memory book:** reconnect after a `Stream` error, re-snapshot on every reconnect, `ErrResync` → immediate re-snapshot, heartbeat timeout cancels the stream, the poll fallback runs while the stream is down, a panic in one book doesn't stop another, and health transitions go live → degraded → live.
9. **Extensibility grep test** (section 7).
10. `go vet ./...` is clean.

---

## 12. Order of work

1. `git init`; commit the current state as "DeepSeek draft".
2. Rewrite `internal/book` (section 7) and extend `model`. Adapt the DK/FD snapshot code to put its seeds in `Snapshot.Private` and set `AsOf` and `Subscription`. Keep parity green.
3. DK stream state machine (section 8) + tests 1–3. **Don't move on until conn 2 replays with 0 resyncs.**
4. DK transport (dial/subscribe/read/ping/ticker), ported from `go/internal/dk`.
5. FD stream state, transport, SMP poller and bundle provider (section 9) + test 4.
6. Board (section 10) + tests 6–7.
7. Supervisor + test 8.
8. `cmd/boardctl`:
   - `-seconds N` runs the real registry live and prints each delta (`seq`, game, market, side, per-book line/odds, best book, `AppliedAt−RecvAt` in µs, `RecvAt−BookTS` in ms), health transitions, and a summary per book (games, slots, deltas, reconnects, resyncs, latency p50/p95);
   - `-kill-after D` cancels one book's stream once, to show degraded → live recovery while the other book keeps updating;
   - `-book draftkings|fanduel` runs just one.
9. **Live run on this machine** (it has the Ontario IP): run `boardctl -seconds 120`, then a run with `-kill-after 40s`. Paste both summaries into your report. Run during a live NFL game if one is on (check the schedule); otherwise say it was pregame only.
10. Phase 5, the extensibility proof: add `internal/books/polymarket` (port `go/internal/pm` + `normalizer/internal/adapters/pm.go`) with `PriceKind = probability`, mains only. The diff should touch only its own package, its fixtures and **one** `Register` line. Its quotes show in slots but are never best.

---

## 13. After this: fanout, UI, deploy, README

These come later, but design for them now:

- **SSE hub:** `GET /stream` sends `event: snapshot` (full board state + seq), then `event: delta` per board delta, **immediately with no batching**, and a `ping` comment every 15 s. Support `Last-Event-ID` resume from a recent ring buffer, and a fresh snapshot if the client has fallen too far behind. Payloads are small JSON per delta. Also `GET /api/board`, `GET /healthz`, `GET /metrics`.
- **Clock sync:** include `serverTime` in the snapshot and in pings, so the browser can compute `now − RecvAt` accurately and show per-book freshness.
- **UI:** a static page embedded with `go:embed`. Toggle (Best Price / DraftKings / FanDuel); per-book health pills (live / degraded / stale + age); cells with line + odds and the book name underneath; flash green or red on change and show the previous price struck through; stale books greyed and excluded from best; live game badge. A mockup was agreed: rows of games, columns Spread / Total / Moneyline, two rows per game (away/home).
- **Deploy:** Dockerfile + `fly.toml`, region **yyz**. **First** deploy a tiny probe there and check DK and FD return 200 from Fly's IPs. If they're blocked, the fallback is collectors on an Ontario machine streaming `model.Update`s to the cloud hub over one WebSocket.
- **README:** architecture diagram; per-book data path and why (push beats poll: the race numbers in section 6); the DK line-move and kickoff behavior (section 5; reviewers will like this); best-price rule; latency per book, pregame and live, with method (DK `websocketPublishTimestamp` → `RecvAt` → `AppliedAt` → browser paint; FD from `RecvAt` only); geo/bot findings (the Akamai UA 403, the SMP Go-UA 500, the FD app key and bundle rotation); what changes for 10 books (adapter-per-book, golden replay tests, AI-generated adapters checked against captured traffic).

---

## 14. Reporting rules

- Report **only what's in the code**. For every phase, include the exact replay numbers: frames, resyncs, updates by status.
- If something can't be verified (live bundle fetch, live game behavior), say so plainly. Don't infer success.
- If a measured number changes from this document, explain why with the frames that prove it.
- Keep `go/`, `normalizer/` and `out/` untouched. Commit per phase with clear messages.
