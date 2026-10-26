# DraftKings / FanDuel odds-feed investigation

**Update October 25 (later):** The DraftKings and FanDuel REST + WebSocket clients now also exist in Go under [`go/`](go/README.md). Live verification passed: DraftKings snapshot 26/78/156 with identical entity IDs to the JS fetch; FanDuel discovery normalization 156/156 rows identical; FanDuel GraphQL and SMP bodies byte-identical; DraftKings WS frames and FanDuel AppSync live data frames received. Build and run with `.\go\oddsfeed.exe verify`. A one-hour soak (`.\go\oddsfeed.exe soak -duration 1h`, output in `out/soak-1h/`) then held 99.94% / 99.93% WebSocket uptime with zero REST errors and 120 reconciliations per book; DraftKings closes its socket every 30 minutes, which the harness reconnects through in 1.6 s. See [go/SOAK-1H-RESULTS.md](go/SOAK-1H-RESULTS.md). The original Node kit below is unchanged.

**Update October 25:** DraftKings polling is now working. Controlled checks isolated a User-Agent-sensitive Akamai rejection: the current browser User-Agent succeeds without cookies; the default Node agent and the previously tried Chrome 142 agent fail. `dk-http.cjs` contains the tested client and `compare-dk.cjs` runs the simultaneous benchmark. See [DK-POLLING-FIX.md](DK-POLLING-FIX.md). Earlier blocker descriptions below document the October 24 investigation and are superseded by this update.

Investigated October 24, 2026 (America/Toronto); capture timestamps are UTC, October 25. This is a reproducible research kit, not the deployed comparison app requested in the attached take-home document.

## Result

**Use snapshots for discovery/bootstrap and a persistent WebSocket per book for price changes. Both books have working sportsbook push connections.** FanDuel does not need to be limited to polling its cached page API.

| Book | Verified in this session | Remaining work |
|---|---|---|
| DraftKings Ontario | Anonymous JSON-RPC WebSocket accepted NFL subscription; 616 update frames captured in the first approximately 45-second observation | Complete bootstrap snapshot: legacy REST route returned 403. Current NFL page uses a CMS widget. Reconnect/resync and narrow main-market subscriptions still need implementation |
| FanDuel Ontario | NFL discovery: 27 events, 81 main markets, 162 priced selections. GraphQL and SMP price snapshots returned matching market IDs. WebSocket subscriptions acknowledged; 14 live college-football price messages captured | Actual NFL price movement and live NFL latency not observed during this short Saturday session; production lifecycle and performance tests still needed |

The broad DraftKings subscription includes props and futures. The 616 messages are NOT 616 changes to the NFL main-line board, and are NOT evidence of a complete initial snapshot. A merged partial cache contained only one event. Do not display that cache as a full board.

## DraftKings protocol

Primary evidence: the [public homepage](https://sportsbook.draftkings.com/), [NFL page](https://sportsbook.draftkings.com/leagues/football/nfl), [data-layer SDK 4.0.0](https://cdn.draftkings.com/rj/@draftkings/dk-data-layer/4.0.0/dkDataLayer.js), and [current CMS widget](https://cdn.draftkings.com/rj/@draftkings/cms/3.1.7/assets/index.js). Saved locally under `captures/`.

The homepage selected `dkcaon` / `CA-ON-SB` for this connection.

```text
wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en-US
```

```json
{
  "jsonrpc": "2.0",
  "method": "subscribe",
  "id": "nfl-events",
  "params": {
    "entity": "events",
    "queryParams": {
      "query": "$filter=leagueId eq '88808'",
      "initialData": false,
      "includeMarkets": "$filter=tags/all(t: t ne 'SportcastBetBuilder')",
      "projection": "sportsbook",
      "locale": "en-US"
    },
    "forwardedHeaders": {},
    "clientMetadata": {
      "feature": "odds-research",
      "X-Client-Name": "web",
      "X-Client-Version": "unknown"
    },
    "jwt": "",
    "siteName": "dkcaon"
  }
}
```

The SDK uses `initialData:false`. The first experiment used `true`; the server acknowledged it but did not supply a complete snapshot. The delivered probe now follows the SDK setting. Empty JWT was accepted for the public feed.

Responses include `event:"subscribed"`, then `event:"update"`. Changes live under `message.data.data.{add,change,remove}.{events,markets,selections}`. Merge changes by ID; partial changes must not replace an entire record. Apply removals, preserve status/suspension, and resolve selection `marketId` to market `eventId` using the bootstrap cache. Unknown IDs require resync, not guessed joins.

The SDK also supports `format=msgpack` with array-shaped entity mappings. JSON was tested; MessagePack was inspected in code but not benchmarked. Start with JSON for correctness and compare CPU, allocation, bytes and p95 before changing codecs.

The older snapshot route present in the client is:

```text
https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808
```

It returned Akamai 403 with curl and Node here. The NFL HTML was accessible with curl but its `stadiumLeagueData` was empty; it loads CMS 3.1.7. That widget uses the configured `controlDataBff` and `pageDataBff` resolvers. A current widget request capture is the next step for reliable bootstrap; do not assume the older route is the only route or claim this is solved.

## FanDuel protocol

Primary evidence: [Ontario homepage](https://on.sportsbook.fanduel.ca/), [main bundle](https://on.sportsbook.fanduel.ca/static/js/main.ea04e06bc3f1ff8b.js), [dependency bundle](https://on.sportsbook.fanduel.ca/static/js/dependencies.ee5fbd711c9dda4a.js), and successful requests saved locally. Versioned asset URLs will change.

### Discover events and main markets

```text
GET https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nfl&includePrices=true&_ak=<public frontend application key>
```

Read `attachments.events` and `attachments.markets`. Filter exact `marketType` values:

| Source type | Normalized type |
|---|---|
| `MONEY_LINE` | moneyline |
| `MATCH_HANDICAP_(2-WAY)` | spread |
| `TOTAL_POINTS_(OVER/UNDER)` | total |

Join `market.eventId` to the event. Each runner has `selectionId`, `handicap`, `result.type`, `runnerStatus`, and `winRunnerOdds`. Keep IDs as strings. Retain raw American odds and true decimal odds; display decimal odds can be rounded.

Observed cache header: `public, max-age=30, stale-while-revalidate=60`. Frequent polling of this endpoint does not guarantee fresh prices, and stale-while-revalidate means 30 seconds is not a strict freshness ceiling. Use it for periodic structural discovery, then refresh prices through the price service.

### Price snapshot and push

```text
POST https://pir.on.sportsbook.fanduel.ca/graphql
wss://pir.on.sportsbook.fanduel.ca/graphql/realtime
```

The frontend publishes a client authorization value; this is not a user account token. `probe-ws.cjs` reads it and the exact query from the downloaded main bundle rather than depending on a private credential.

WebSocket setup:

1. Connect with subprotocol `graphql-ws` and query arguments `header=base64(JSON.stringify({Authorization:clientValue,host:"pir.on.sportsbook.fanduel.ca"}))` and `payload=base64("{}")`.
2. Send `{"type":"connection_init"}`; await `connection_ack`. Server advertised a 300000 ms connection timeout and sends `ka` keepalives.
3. Send `{"id":"prices","type":"start","payload":{"data":"<JSON string containing query and variables>","extensions":{"authorization":<same authorization object>}}}`.
4. Query is `subscription OnUpdateMarketPrice($ids:[String!]!){onUpdateMarketPrice(ids:$ids){...}}`; variables contain discovered sportsbook market IDs. Await `start_ack`; process `type:"data"`.

Fields include `marketId`, `marketStatus`, `inplay`, and `runnerDetails` with selection ID, handicap, runner status and odds. These use the same `801.*` market IDs returned by the sportsbook discovery API. Actual in-play college-football updates were received, not merely a handshake. The 81 NFL market subscription was acknowledged but had no data frames in its 45-second observation. Quiet pregame markets are not proof of a broken feed.

The bundle has another query variant including `updatedAt`; that variant and its timestamp semantics have not been validated. A GraphQL `GetMarketPrices` query returned current sportsbook prices for three NFL markets.

### Fallback price polling

```text
POST https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0
X-Application: <public frontend application key>
Content-Type: application/json

{"marketIds":["<discovered ID>"]}
```

Verified HTTP 200, real runner odds, and `Cache-Control:no-cache`. The frontend's SMP polling configuration uses 5000 ms and batch size 70. Use that as the initial fallback cadence, respect throttling / Retry-After, and tune only from measured results. Do not assume `no-cache` means the underlying odds engine has zero delay.

For an interval T with uniformly timed changes, polling contributes approximately T/2 average and 0.95T p95 detection delay, plus request time and any upstream delay. At 5 seconds that is approximately 2.5 seconds average / 4.75 seconds p95. These are a model, not observed latency measurements. Push avoids that polling wait.

## Measurements and limitations

- FanDuel NFL discovery: 636.6 ms for one cold request, 1,321,718 decoded bytes; response was Brotli encoded on the wire.
- FanDuel GraphQL snapshot: 450.4 ms for three markets, 2,671 decoded bytes, one request.
- FanDuel SMP snapshot: 108.6 ms for the same three markets, 3,477 decoded bytes, one request.
- These are single-request elapsed times, not latency distributions, and do not establish SMP as faster under comparable warm-connection conditions.
- DraftKings: 583,327 application payload bytes across subscribed/update messages in the first probe. Publish-to-local-receive subtraction had median **-75 ms** (range -81 to -26 ms): clocks are inconsistent. No valid one-way latency claim can be made from this.
- FanDuel live validation: 14 data messages for four college-football markets. No live NFL validation was performed.
- No backend-to-browser rendering benchmark has been performed. There is no defensible end-to-end latency number yet.

To measure properly: synchronize host clocks and record uncertainty; timestamp socket receipt, cache application, server send, browser receive and paint; track source timestamps only after validating their semantics and clock offset. Use monotonic time for local stages. Report p50/p95/p99 by book and pregame/live. Source-publish-to-receive does not necessarily include time inside the odds engine before publication.

## Recommended collector design

One collector per book/region -> normalized in-memory cache -> immediate SSE or WebSocket deltas to every browser. No upstream connection per viewer. Keep structural refresh off the price hot path. Subscribe only to the requested main markets once bootstrap is reliable; the broad DK research subscription is too noisy for production.

Subscribe and buffer during bootstrap, then reconcile a fresh snapshot and buffered updates using validated ordering/version semantics; an old snapshot must never overwrite newer prices. On disconnect, mark that book stale, reconnect with capped exponential backoff and jitter, resubscribe and resnapshot. Keep the other book running. A working heartbeat proves connection health, not the age of each price.

Handle suspended, removed and closed markets immediately. Never select stale/suspended odds as the best available price. Match events using canonical teams, home/away and start time; preserve each book's IDs. Compare prices only at identical market/period/side/line. Different lines must remain visible as different offers unless an explicit valuation policy is implemented.

## Run the research kit

Use Node 24+; `--use-system-ca` was necessary here because system Node 20 could not validate the network certificate chain. TLS verification stays enabled. The bundled Node path on this machine is `C:/Users/USER/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node.exe`.

```powershell
# Run in this project, using Node 24 on PATH.
New-Item -ItemType Directory -Force captures
node --use-system-ca fetch.cjs 'https://on.sportsbook.fanduel.ca/static/js/main.ea04e06bc3f1ff8b.js' captures/fd-main.js
node --use-system-ca fetch.cjs 'https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nfl&includePrices=true&_ak=FhMFpcPWXMeyZxOx' captures/fd-nfl.json
node extract-fd-nfl.cjs
node --use-system-ca probe-ws.cjs fd --nfl
node --use-system-ca probe-ws.cjs dk
node --use-system-ca probe-fd-rest.cjs
node summarize.cjs
```

`probe-ws.cjs` runs for 45 seconds by default (`CAPTURE_MS` overrides it), saves full frames and limits console output. Logs append across runs; timestamps distinguish the observations. `summarize.cjs` aggregates all runs and intentionally labels the DraftKings merged state a partial cache. Its generic FD partial-cache counters are unused; FD data messages are counted separately.

`capture.cjs` optionally records browser HTTP bodies and WebSocket frames using Playwright and installed Chrome. Install optional dependencies with `npm install`. It creates a separate directory for each run. Browser captures hit access-denied / bot-check pages here; native HTTP and WebSocket clients had different results. No account login, CAPTCHA solving, proxy rotation or location spoofing was used.

Raw captures and frontend assets are excluded from Git. These are finite diagnostic probes: production reconnection, schema validation, token/config rotation, cache reconciliation, browser fanout and monitoring are not implemented. Public frontend values can change; rediscover them from current assets on failure, and distinguish authorization errors from transient network failures.
