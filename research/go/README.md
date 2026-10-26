# Go port: DraftKings / FanDuel REST + WebSocket

Working Go implementation of the JavaScript feed clients in the repository root.
It was built and verified against the live Ontario endpoints on **2026-10-25**
(approximately 11:17–11:25 America/Toronto).

This is a faithful port of the transport and parsing layer, not the production
collector described in `README.md`. Reconnect/resync, subscription narrowing and
browser fanout are still open, exactly as they were for the Node probes.

## What is ported

| JavaScript | Go | Command |
|---|---|---|
| `dk-http.cjs` | `internal/dk/rest.go` | `dk-snapshot` |
| `probe-ws.cjs dk` | `internal/dk/ws.go`, `subscribe.go` | `dk-ws -mode probe` |
| `compare-dk.cjs` subscriptions + merge cache | `internal/dk/cache.go`, `subscribe.go` | `dk-ws -mode partials` |
| `probe-fd-rest.cjs` | `internal/fd/rest.go` | `fd-rest` |
| `extract-fd-nfl.cjs` | `internal/fd/types.go` (`Board.Rows`) | `fd-discover` |
| `probe-ws.cjs fd` | `internal/fd/ws.go`, `bundle.go` | `fd-ws` |
| `compare-fd.cjs` discovery/live board | `internal/fd/rest.go` (`InPlay`) | `fd-ws -live` |
| Gamma sports API | `internal/pm/rest.go` | `pm-discover` |
| Polymarket CLOB market WS | `internal/pm/ws.go` | `pm-ws` |

Not ported: the race analyzers (`compare-dk.cjs`, `compare-fd.cjs` polling
loops, `analyze-*.cjs`, `summarize.cjs`) and the diagnostic probes
(`dk-ablation.cjs`, `dk-client-check.cjs`, `dk-replay.cjs`, `capture.cjs`).

## Layout

```text
go/
  go.mod
  cmd/oddsfeed/
    main.go        command dispatch, shared flags/helpers
    dk.go          dk-snapshot, dk-ws
    fd.go          fd-discover, fd-rest, fd-ws
    verify.go      all REST + WS paths in one acceptance run
    logger.go      JSONL capture + console policy
  internal/clock/  performance.now / Date.toISOString equivalents
  internal/jsonx/  JSON.stringify-compatible marshal (no HTML escaping, json.Number)
  internal/dk/     DraftKings types, REST, cache merge, WebSocket, subscriptions
  internal/fd/     FanDuel types, bundle extraction, REST, SMP, graphql-ws
  internal/pm/     Polymarket Gamma REST and CLOB market WebSocket
```

## Build and test

Run from `go/`. The cache paths below are required **on this machine** because
the sandbox cannot write Go's default user-profile cache; on a normal checkout
plain `go build` is enough.

```powershell
cd go
$env:GOPATH     = 'D:\Projects\BitStamp_Interview\.gopath'
$env:GOMODCACHE = 'D:\Projects\BitStamp_Interview\.gopath\pkg\mod'
$env:GOCACHE    = 'D:\Projects\BitStamp_Interview\.gocache'
$env:TMP        = 'D:\Projects\BitStamp_Interview\go\.gotmp'   # avoids a Windows test-binary cleanup race
$env:TEMP       = $env:TMP
New-Item -ItemType Directory -Force $env:TMP | Out-Null
go build -o oddsfeed.exe ./cmd/oddsfeed
go test ./...          # 18 unit tests, no network
```

Only dependency: `github.com/gorilla/websocket v1.5.3`.

Run the binary from the repository root so the default `captures/fd-main.js`
bundle and `out/` directory resolve:

```powershell
.\go\oddsfeed.exe verify -seconds 15
```

## Commands

```text
oddsfeed dk-snapshot  [-url URL] [-out FILE] [-timeout DUR]
oddsfeed dk-discover  [-sport nba|nfl] [-out DIR] [-no-props]
                      [-props sub1,sub2] [-timeout DUR]
oddsfeed dk-ws        [-mode probe|partials] [-seconds N] [-log FILE]
                      [-snapshot-url URL] [-bootstrap-out FILE]
oddsfeed fd-discover  [-ak KEY] [-out FILE] [-normalized FILE]
oddsfeed fd-rest      [-nfl FILE] [-ids id,id] [-mode both|graphql|smp] [-limit N]
oddsfeed fd-ws        [-nfl FILE | -live | -ids id,id] [-seconds N] [-log FILE]
oddsfeed fd-event     -event ID [-mode popular|mga|both] [-tab popular]
                      [-prices] [-out DIR]
oddsfeed fd-events    [-sport nba|nfl] [-max-events N] [-prices] [-out DIR]
oddsfeed pm-discover  [-sport nba|nfl] [-series ID] [-limit N] [-out FILE]
oddsfeed pm-ws        [-event ID | -sport nba|nfl | -ids id,id]
                      [-types main,alt,props,all] [-seconds N] [-log FILE]
oddsfeed verify       [-seconds N] [-skip-ws]
oddsfeed soak         [-duration 1h] [-out DIR] [-dk-rest-every 30s]
                      [-fd-poll-every 30s] [-fd-discovery-every 5m]
                      [-dk-scope partials|probe] [-fd-markets N]
                      [-ping-every 30s] [-stale-after 10m]
                      [-reconnect-every 0] [-verbose]
```

Defaults mirror the Node probes:

- `CAPTURE_MS` (milliseconds) sets the default WebSocket window; the CLI flag
  `-seconds` overrides it.
- `DK_USER_AGENT` overrides the measured Chrome 156 UA.
- `FD_MAIN_JS` overrides the FanDuel bundle path.
- WebSocket JSONL logs default to `out/dk-ws.jsonl` and `out/fd-ws.jsonl`.
- `dk-ws -mode partials` fetches a fresh snapshot, builds the subscriptions
  from `subscriptionPartials`, **seeds the merge cache from that snapshot**, then
  applies add/change/remove frames and reports `unknownChanges`.
- `fd-ws` reads the AppSync client id and both GraphQL documents from the
  current bundle, then sends `connection_init` → `start` exactly like
  `probe-ws.cjs fd`.

## Unified DraftKings discovery

`dk-discover` merges DK mainlines, alternate spreads/totals and optional prop
subcategories into one deduplicated board:

```powershell
.\go\oddsfeed.exe dk-discover -sport nba -out out/dk-nba-discover
.\go\oddsfeed.exe dk-discover -sport nfl -out out/dk-nfl-discover
```

NBA sources:

- `/leagues/42648` (main)
- `/leagues/42648/categories/487/subcategories/13202` (alt spread)
- `/leagues/42648/categories/487/subcategories/13201` (alt total)
- automatically selected props under Player Points/Rebounds/Assists/Threes/Defense

NFL sources:

- `/leagues/88808` (main)
- `/leagues/88808/categories/492/subcategories/13195` (alt spread)
- `/leagues/88808/categories/492/subcategories/13196` (alt total)
- automatically selected Passing/Receiving/Rushing/Defensive/TD props

`-props` overrides automatic selection; `-no-props` skips props.

The main snapshot is fetched first because it carries the
`categories`/`subcategories` catalog. `AutoPropSubcategories` selects O/U
subcategories in the sport’s allowlist plus the full-game TD Scorer market.

It writes one raw file per source plus `board.json` and `meta.json`. Events,
markets and selections are deduped by source ID; the main snapshot is loaded
first so its market descriptions win.

Observed on 2026-10-25:

- NBA: 12 events / 297 markets / 1,534 selections, with 7 auto-selected prop
  subcategories.
- NFL: 12 events / 395 markets / 1,586 selections, with 26 auto-selected prop
  subcategories.
- Normalizer: 100% classified; mainline/alternate markets are high-confidence,
  while player props carry the lower 0.85 prop confidence. Alternate markets get
  `Alt=true`.
- NBA prop stats: points/rebounds/assists/threes/steals/blocks/steals+blocks.
- NFL prop stats: passing/rushing/receiving/TD/tackles/assists/sacks and
  longest-play markets.

## FanDuel event-level

The league page is mainline-only. `fd-event` fetches the event-level markets
and prices:

```powershell
.\go\oddsfeed.exe fd-event -event 36229335 -mode both -prices -out out/fd-event-lakers
```

It writes:

- `popular.json` — event-page `tab=popular` response.
- `mga.json` — full MGA `MARKET` facet response.
- `prices.json` — SMP prices for all discovered market IDs.
- `board.json` — merged board-shaped payload for the normalizer.

`popular` carries the priced alternate ladders; `mga` carries the fuller
player-prop/period inventory. The merge keeps the first descriptive market and
fills odds by `marketId+selectionId`. On the Lakers–Jazz event this produced
205 markets and 694 normalized quotes.

MGA request notes:

- `eventIds` must be numeric, not strings.
- `regionCode` is `NAMERICA`.
- `X-Application` and `Content-Type` are required; without the app header the
  endpoint returns a generic 500.

## All current FanDuel NBA games

`fd-events` discovers all current game events from the FanDuel NBA board and
runs the same Popular + MGA + SMP pipeline for each:

```powershell
.\go\oddsfeed.exe fd-events -sport nba -out out/fd-nba-all
```

It filters futures, suffix-named games and non-game events, then writes one
`board.json` with all event markets. On 2026-10-25 it returned **7 events /
2,073 markets / 2,069 priced markets**.

## Polymarket

Polymarket is a prediction market: prices are probabilities in `0..1`, not
American sportsbook odds. The client treats each outcome token as a separate
quote and preserves the decimal probability plus the derived American price.

```powershell
# Open NBA/NFL Gamma events, including every spread/total line and player prop.
.\go\oddsfeed.exe pm-discover -sport nba -limit 100 -out out/pm-nba-events.json
.\go\oddsfeed.exe pm-discover -sport nfl -limit 100 -out out/pm-nfl-events.json
.\go\oddsfeed.exe pm-discover -event 1215089 -out out/pm-one.json

# Subscribe to mainlines, alternates and props for one game.
.\go\oddsfeed.exe pm-ws -event 1215089 -types main,alt,props -seconds 30
```

Series IDs are NBA `10345` and NFL `12185`. The Gamma event's `endDate` is the
game start; market `line` is the named team's signed spread or the total line;
`clobTokenIds` supplies the CLOB asset IDs. The market channel is
`wss://ws-subscriptions-clob.polymarket.com/ws/market`; it sends `book`,
`price_change`, `best_bid_ask`, `last_trade_price`, `new_market` and
`market_resolved` frames.

Observed on 2026-10-25:

- 100 open NBA events / 4,760 markets before event de-duplication.
- 62 open NFL events / 14,480 markets.
- Lakers–Jazz: 472 markets, 926 normalized quotes; 20 seconds yielded 27,085
  WebSocket frames.
- The CLOB WS supports one subscription with hundreds of asset IDs; it sends
  `PONG` to a 10-second `PING`.

Polymarket prices must be de-vigged or compared as fair probabilities; raw
American odds are not directly comparable to DK/FD sportsbook prices.

## Long-running soak test

`soak` runs both books concurrently for a fixed duration and writes every
WebSocket frame, every REST body, every reconciliation record and lifecycle
event to a timestamped directory:

```powershell
.\go\oddsfeed.exe soak -duration 1h
.\go\oddsfeed.exe soak -duration 3h            # same harness, three-hour run
```

Defaults: DraftKings snapshot + reconciliation every 30 s, FanDuel SMP poll
every 30 s, FanDuel discovery every 5 min, WebSocket ping every 30 s, and a
10-minute no-traffic watchdog. `-duration`, the four intervals, the market
limit and the ping/watchdog values are all flags, so a 3-hour run needs no code
change.

What it does:

- **Keepalive:** each connection sends a WebSocket ping every 30 s and records
  pongs; if no frame or pong arrives for `-stale-after` (default 10 min) the
  socket is closed as stale.
- **Reconnect:** capped exponential backoff with jitter (1 s → 30 s), fresh
  bootstrap snapshot/discovery, resubscribe, and a full cache/state resync.
  `-reconnect-every 40s` deliberately forces a reconnect, which is how the
  recovery path was tested.
- **DraftKings reconciliation:** each snapshot compares all main-market
  selections (moneyline/spread/total) against the merged WebSocket cache on
  American price, points/line, status and presence. Missing records are filled
  from the snapshot but existing WebSocket values are never overwritten.
- **FanDuel reconciliation:** each SMP poll (batched at 70 ids) compares market
  status, in-play, runner set, runner status, handicap and American odds with
  the latest pushed state. Discovery refreshes detect added/removed main
  markets and trigger a resubscribe when the set changes.
- **Restart safety:** every REST body is saved before parsing; the final cache
  and FanDuel state are written at the end; `events.jsonl` records every
  connection, reconnect, resubscribe, REST error and reconciliation drift.

Output tree (`-out` defaults to `out/soak-<UTC timestamp>`):

```text
run.json                 config and start time
events.jsonl             lifecycle, progress, REST errors, resyncs, drift
summary.json             final counters, uptime, reconcile totals, ok
console.log              stdout/stderr of the run (when redirected)
dk/ws.jsonl              every DraftKings frame, sent and received
dk/reconcile.jsonl       one record per snapshot reconciliation
dk/rest/*.json(.meta)    every bootstrap and periodic snapshot body
dk/cache-final.json      final merged cache
fd/ws.jsonl              every AppSync frame
fd/reconcile.jsonl       one record per SMP reconciliation
fd/discovery.jsonl       one record per discovery refresh
fd/rest/*.json(.meta)    every discovery, SMP batch and resync body
fd/state-final.json      final pushed-state view
```

A 90-second smoke run and a 2-minute forced-reconnect run both completed with
`ok:true`; the forced run made three connections per book with two reconnects
and no REST errors. The full one-hour run (2026-10-25, including the NFL 1 p.m.
kickoff) finished `ok:true` with **99.94% DraftKings / 99.93% FanDuel uptime,
zero REST errors and zero unknown changes**. DraftKings closed the socket at
exactly 30 minutes and the harness reconnected in 1.6 s; FanDuel resubscribed
twice after discovery market-set changes. Full numbers, timeline and the
three-hour assessment: [`SOAK-1H-RESULTS.md`](SOAK-1H-RESULTS.md). Raw output is
in `out/soak-1h/` (57 MB).

## Live verification results

| Check | JavaScript | Go |
|---|---|---|
| DK snapshot | 26 events / 78 markets / 156 selections | 26 / 78 / 156 |
| DK entity IDs | — | same event, market and selection ID sets; 3×26 market types |
| DK selection prices | — | 150/156 identical; 6 moved between the two fetches |
| DK `subscriptionPartials` | 3 keys | same 3 keys |
| DK WS (20 s, probe) | subscribed + updates | `subscribed: 1`, `update: 3095` |
| DK WS (15 s, partials) | — | 3 subscriptions, 11 updates, 0 unknown after seeding |
| FD discovery | 26 / 78 / 156 | 26 / 78 / 156 |
| FD normalized rows | `extract-fd-nfl.cjs` | **156/156 rows byte-identical** |
| FD GraphQL snapshot | 2,671 bytes | **byte-identical body** (same SHA-256) |
| FD SMP poll | 3,477 bytes | **byte-identical body** (same SHA-256) |
| FD WS (pregame NFL) | `connection_ack`, `ka`, `start_ack` | same, no data frames (quiet markets) |
| FD WS (live, 45 s) | — | 9 markets, `connection_ack` + `start_ack`, 20 data frames across 6 markets |
| `verify` | — | `{"...","ok":true}` with 3/3 markets matching between GraphQL and SMP |

The byte-identical FanDuel bodies were fetched for market ids
`801.192827512`, `801.192827513`, `801.192827671`; the raw response bodies from
both implementations hash to the same SHA-256.

### One behavioural finding

The FanDuel SMP endpoint returns **HTTP 500 with an empty body** when the
request carries Go's default `Go-http-client/1.1` User-Agent. The Node probe
happened to send a browser-like/Node UA and worked. The Go client now sends the
measured Chrome UA (plus `Accept: */*`) for the SMP POST, which returns 200 and
the same body as Node. This is a transport detail, not a credential change.

## Deliberate differences from the JS

- **Output goes to `out/`, not `captures/`.** `captures/` is read-only in this
  session (a Windows permission issue on the pre-existing folder) and is also
  gitignored raw data. Everything the Go tools write is under `out/`.
- **Deterministic ordering.** Discovery markets and subscription partials are
  sorted, so repeated runs produce stable output; the JS relied on object order.
- **Errors are returned.** A GraphQL `errors` response fails the call instead of
  yielding `undefined`; malformed frames are skipped and counted.
- **No MessagePack.** JSON only, matching the transports actually tested.
- **No reconnect loop.** Like the probes, these are finite observations. A
  production collector still needs capped exponential backoff with jitter,
  subscribe-and-buffer during bootstrap, a fresh snapshot on reconnect, and
  stale-book marking.

## Unit tests

`go test ./...` covers, without network access:

- snapshot UA, schema validation, HTTP error truncation and metadata;
- partial add/change/remove merging, nested `displayOdds` merge and unknown IDs;
- both DraftKings subscription builders;
- FanDuel bundle regex extraction, AppSync URL base64, `start` payload shape;
- discovery normalization (moneyline/spread/total, null line, suspended runner,
  missing prices), missing-event error;
- GraphQL and SMP requests, headers, decoding and error responses;
- graphql-ws frame parsing.
