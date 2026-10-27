# BetMGM odds feed: implementation handoff

Prepared for Opus 5.5 on 2026-10-27. Scope: Canadian BetMGM frontend, NFL/NBA main lines, and NFL player props.

## 1. Read this first

The browser feed is usable for reading prices, and its REST routes and SignalR transport have been identified. **A browser-independent collector has not been demonstrated.** A direct REST probe received a block page and a direct WebSocket probe returned HTTP 403. Successful browser access does not establish that a Go service on Fly.io will work.

The investigation was read-only. No BetMGM adapter, dummy project, saved HAR, raw capture files, or downloaded bundles were created. Examples below are selected observations from tool output, sometimes reduced to relevant fields. They are not complete replay fixtures. Prices and event IDs are inspection-time examples, not current-price promises.

Implement an isolated research collector first. Prove access, complete snapshots, and actual NFL/NBA deltas before integrating into the production board. Do not deploy or modify the production app merely because this handoff exists.

### Evidence status

| Item | Status |
|---|---|
| Logged-out browser reads NFL/NBA prices | Verified |
| NBA JSON snapshot includes main lines | Verified |
| NFL event JSON includes player props | Verified |
| SignalR connection URL and JSON handshake | Observed |
| Subscribe/Unsubscribe and successful invocation acknowledgements | Observed |
| Actual live market updates | Observed on homepage tennis fixtures |
| Actual NFL/NBA market-price delta | Not captured during this inspection |
| Actual NFL prop-price delta | Not captured during this inspection |
| V2 update payload schema | Still requires capture |
| Standalone REST access | Probe blocked |
| Standalone WebSocket access | Probe returned HTTP 403 |
| Reconnect, replay, sequence semantics, rate limits | Not established |

## 2. Entry points and geography

Observed frontend origin:

```text
https://www.betmgm.ca
```

The older `https://sports.on.betmgm.ca/en/sports` URL redirected to this origin. The browser requested Ontario configuration:

```text
lang=en-us
country=CA
userCountry=CA
subdivision=CA-Ontario
```

Public pages:

- Homepage: https://www.betmgm.ca/en/sports
- NFL: https://www.betmgm.ca/en/sports/football-11/betting/usa-9/nfl-35
- NBA: https://www.betmgm.ca/en/sports/basketball-7/betting/usa-9/nba-6004
- Example NFL event: https://www.betmgm.ca/en/sports/events/carolina-panthers-green-bay-packers-6:48482

| League | Sport ID | Competition ID |
|---|---:|---:|
| NFL | 11 | 35 |
| NBA | 7 | 6004 |

Sport ID alone is insufficient: basketball includes other competitions. One observed basketball response reported `totalCount: 154`; only four fixtures in the returned page matched NBA. Pagination and league filtering are necessary.

Requests contained `x-bwin-accessid`, a frontend-supplied access identifier visible in logged-out requests. Treat it as environment configuration, discover its current value from frontend traffic, and avoid assuming permanence. The value observed in this session was:

```text
ZjNmOGM4OTAtZmVhOS00ZTZiLWE5MDQtNGVjNWM5MDkxNjQ1
```

This identifier alone did not make the standalone probe work. No account cookies or account credentials were used in that probe.

## 3. REST routes actually observed

All routes below are relative to `https://www.betmgm.ca` unless specified otherwise. Parameter sets describe observed browser requests, not a formally documented contract. Use a URL builder and preserve compound IDs as strings.

### 3.1 Fixture discovery and main markets

```http
GET /cds-api/bettingoffer/fixtures
```

Observed basketball query, excluding the shared access/geography parameters:

```text
fixtureTypes=Standard
state=Latest
offerMapping=Filtered
offerCategories=Gridable
fixtureCategories=Gridable,NonGridable,Other
sportIds=7
isPriceBoost=false
statisticsModes=Rank
skip=0
take=50
sortBy=Tags
```

The homepage also requested this route with `sportIds=11`. Do not equate the football response with NFL only.

Observed response fields (TypeScript shape; counter types should be checked against a fresh capture):

```typescript
type FixtureEnvelope = {
  fixtures: Fixture[];
  totalCount: number;
  totalSports: number;
  totalRegions: number;
  totalCompetitions: number;
};
```

The inspected basketball response had `totalCount: 154`. All five field names were observed.

Implementation requirements:

1. Page using `skip` and `take`; verify termination and deduplicate fixture IDs.
2. Filter by the fixture competition, not team-name heuristics.
3. Investigate a server-side competition filter. A standalone request using `competitionIds=35` was blocked, so that request did **not** validate the parameter's behavior.
4. Do not silently retire games from an incomplete or failed page traversal.
5. Do not assume `Gridable` includes player props or all alternate lines.

### 3.2 Initial event detail

```http
GET /cds-api/bettingoffer/fixture-view
```

Observed query for Panthers–Packers:

```text
offerMapping=Filtered
scoreboardMode=Full
fixtureIds=6:48482
state=Latest
includePrecreatedBetBuilder=true
supportVirtual=true
isBettingInsightsEnabled=true
useRegionalisedConfiguration=true
includeRelatedFixtures=false
statisticsModes=Rank,Pitchers
firstMarketGroupOnly=true
```

Observed top-level fields:

```text
fixture
splitFixtures
preCreatedOffer
groupingVersion
bettingInsights
availableMarketGroupIds
```

**The initial response intentionally loads only the first market group. It is not a complete market snapshot.** The frontend then fetched additional offers.

### 3.3 Additional event markets, including props

```http
GET /cds-api/bettingoffer/fixture-offers
```

Observed query:

```text
fixtureIds=6:48482
excludeMarketGroupIds=3b194af8-494c-4453-bedc-d041f0b1cccb
offerMapping=Filtered
```

The excluded group is an observed event-specific value, not a constant to copy across fixtures. Determine the initially loaded group from the response/frontend behavior.

Response shape:

```json
{
  "fixtureOffers": [
    {
      "fixtureId": "6:48482",
      "optionMarkets": []
    }
  ]
}
```

Combining `fixture.optionMarkets` with `fixtureOffers[].optionMarkets`, then deduplicating by market ID within this fixture, yielded **551 unique markets**. **368 had a `fixtureParticipantId`**. This is an observed count of player-associated markets, not a guarantee of all available props or a count of players.

Do not assume omitting `firstMarketGroupOnly` or changing it to `false` returns everything until tested. Do not assume `splitFixtures` or related fixtures can always be ignored; their coverage remains to be assessed.

### 3.4 Other observed requests

```text
/cds-api/bettingoffer/live/highlights
/cds-api/bettingoffer/counts
/cds-api/statistics/player-stats
```

`live/highlights` used `state=Live`, `scoreboardMode=Slim`, `offerCategories=Gridable`, and `maxFixturesPerSport=5`. It is a limited discovery view, not a full feed.

The NFL page requested `player-stats` with `eventId=6:48482`, `mainMarket=false`, and `marketFilter=4218571`. Player statistics are separate from odds; they are unnecessary for the first odds collector.

## 4. Fixture and market data model

### 4.1 Fixture fields

The inspected NFL `fixture` contained:

```text
optionMarkets, games, participants, id, name, sourceId, source,
fixtureType, context, addons, stage, liveAlert, scoreboard,
startDate, cutOffDate, sport, competition, region, viewType,
isOpenForBetting, isVirtual, taggedLocations, totalMarketsCount,
conferences, marketGroups, priceBoostCount, linkedTv1EventIds,
contexts, playerStats, isPlannedInPlay
```

Important rules:

- Preserve fixture IDs as strings. V1 examples look like `20030815`; V2 examples look like `6:48482`.
- Names are often localized objects, such as `{"value":"Brooklyn Nets at Boston Celtics"}`.
- Capture and inspect participant metadata before inferring home/away or player-to-team relationships. Array order and `sourceName: "1"`/`"2"` are not sufficient proof of home/away.
- Keep market and selection IDs scoped to fixture/source. Their global uniqueness was not established.
- Preserve source formats separately even when the REST frontend maps them into `optionMarkets`.

### 4.2 Common REST market shape

Reduced observed V2 moneyline example:

```json
{
  "id": 4204561,
  "name": {"value": "Moneyline"},
  "status": "Visible",
  "options": [
    {
      "id": 6794522,
      "status": "Visible",
      "name": {"value": "Carolina Panthers"},
      "sourceName": {"value": "2"},
      "price": {
        "id": 23933820,
        "numerator": 31,
        "denominator": 20,
        "odds": 2.55,
        "americanOdds": 155
      },
      "parameters": {"optionTypes": ["Max"], "fixtureParticipant": 771053}
    }
  ],
  "parameters": [
    {"key": "MarketType", "type": "String", "value": "2way"},
    {"key": "Happening", "type": "String", "value": "Point"},
    {"key": "Period", "type": "String", "value": "FullTime"}
  ],
  "isMain": true
}
```

Other observed fields include `source`, `attr`, `spread`, `balanced`, `templateCategory`, `grouping`, `fixtureParticipantId`, `isBetBuilder`, `comboPrevention`, and `minCombo`.

**Do not use `spread` as the handicap.** The NBA spread market had `spread: 0` while its selections carried `attr: "+12.5"` and `attr: "-12.5"`. The moneyline example had `spread: 1`. The intended meaning of this field was not established.

**Do not filter all main-line markets with `isMain == true`.** Observed full-game NBA spread and total markets had `isMain: false`; the moneyline had `isMain: true`.

### 4.3 V1-origin REST markets

The NBA snapshot used the common `optionMarkets` structure, with `source: "V1"` and parameter arrays containing template/category IDs:

| Observed NBA market | TemplateId | CategoryId | Line location |
|---|---:|---:|---|
| Moneyline | 3450 | 43 | No line |
| Spread | 8098 | 44 | `options[].attr` |
| Totals | 11577 | 48 | Market `attr`; options have `totalsPrefix` |
| 1st half spread | 7068 | 728 | `options[].attr` |
| 1st half totals | 15038 | 730 | Market `attr` |

These are observed NBA IDs, not universal NFL or V2 mappings. Build explicit, sport/source-specific mappings and reject unknown combinations.

Example NBA prices observed:

```text
Brooklyn Nets at Boston Celtics
Moneyline: Nets +450 / Celtics -625
Spread: Nets +12.5 -110 / Celtics -12.5 -110
Total: Over 222.5 -110 / Under 222.5 -110
```

V1-origin `price.id` was `-1` in inspected NBA options. Use the selection/option ID, not `price.id`, as selection identity.

### 4.4 V2 player props

Reduced observed single-sided touchdown milestone:

```json
{
  "id": 4218631,
  "name": {"value": "Christian Watson to score 3+ TDs"},
  "status": "Visible",
  "options": [
    {
      "id": 6825439,
      "status": "Visible",
      "name": {"value": "Yes"},
      "price": {
        "id": 24096964,
        "numerator": 45,
        "denominator": 1,
        "odds": 46,
        "americanOdds": 4500
      },
      "parameters": {"optionTypes": ["Over"]}
    }
  ],
  "parameters": [
    {"key": "DecimalValue", "type": "Decimal", "value": "2.5000"},
    {"key": "FixtureParticipant", "type": "Long", "value": "771489"},
    {"key": "Happening", "type": "String", "value": "Touchdown"},
    {"key": "MarketSubType", "type": "String", "value": "PlayerTD"},
    {"key": "MarketType", "type": "String", "value": "Over/Under"},
    {"key": "Period", "type": "String", "value": "FullTime"}
  ],
  "attr": "2.5",
  "fixtureParticipantId": 771489,
  "source": "V2",
  "isBetBuilder": true
}
```

This example is a **single-sided milestone** despite `MarketType: Over/Under`. The display label is 3+ TDs, while the encoded threshold is 2.5. Preserve the original fields and encode comparator semantics explicitly.

Other milestones used `MarketSubType: YesNo`, `DecimalValue: 50`, and a label such as “50+ receiving yards”. Do not universally add/subtract 0.5. Validate normalization by market subtype, stat, and option semantics.

Observed conventional two-sided props:

| Market | Line | Over | Under |
|---|---:|---:|---:|
| Bryce Young passing yards | 252.5 | -115 | -115 |
| Jordan Love passing yards | 263.5 | -115 | -115 |
| Christian Watson receptions | 4.5 | -125 | -105 |
| MarShawn Lloyd receiving yards | 5.5 | -120 | -110 |

For these, parameters included `DecimalValue`, `FixtureParticipant`, `Happening`, `MarketType: Over/Under`, and `Period: FullTime`.

Observed player-associated `Happening` values:

```text
ReceivingYards, Touchdown, RushingReceivingYards, RushingYards,
PassingYards, TouchdownPass, RushingAttempt, PassCompleted,
PassAttempt, PassingRushingYards, Reception, InterceptionThrown, Point
```

Suggested normalized stat names are `receiving_yards`, `touchdowns`, `rushing_receiving_yards`, `rushing_yards`, `passing_yards`, `passing_touchdowns`, `rushing_attempts`, `completions`, `passing_attempts`, `passing_rushing_yards`, `receptions`, and `interceptions_thrown`. Investigate `Point` before classifying it as a particular player stat.

Market `parameters` is an array of typed key/value objects; option `parameters` is an object in the observed V2 examples. Do not deserialize both with the same type.

`isBetBuilder: true` also appeared on normal single-market odds. It is not sufficient reason to discard a market as a parlay. Separately exclude `preCreatedOffer` and `betbuilder|updates|...` combination products from the initial collector.

## 5. WebSocket transport

### 5.1 Connection

Observed URL:

```text
wss://cds-push.betmgm.ca/ws-1-0?lang=en-us&country=CA&x-bwin-accessId=<frontend-access-id>&appUpdates=false
```

The socket was observed in a logged-out browser. A standalone Windows `ClientWebSocket` connection using the same URL and `Origin: https://www.betmgm.ca` returned **403 instead of 101**.

A Node probe first encountered `unable to verify the first certificate`. The subsequent Windows probe used normal OS certificate trust and reached the HTTP 403. Do not disable certificate verification to solve either problem.

No negotiate request was captured as a prerequisite to this socket. This is not proof that negotiation can never be required; reproduce the current frontend startup before deciding on a transport library.

### 5.2 Framing and handshake

Protocol: ASP.NET Core SignalR JSON hub protocol.

Observed client handshake:

```json
{"protocol":"json","version":1}
```

Append byte `0x1e` (record separator) to each message. Observed WebSocket text frames sometimes contained several JSON messages separated by `0x1e`. Do not call `JSON.parse()` on an entire multi-message WebSocket frame.

Observed heartbeat:

```json
{"type":6}
```

A standard SignalR successful handshake response is `{}` followed by the delimiter, but the initial server handshake response was not retained in the inspection output. Validate it in the implementation. Do not claim a heartbeat interval from this inspection; measure it or use the client's current configuration.

### 5.3 Subscribe and unsubscribe

Reduced observed event subscription, excluding separate bet-builder topics:

```json
{
  "target": "Subscribe",
  "arguments": [{"topics": ["v2|en-us|6:48482_101_any|all"]}],
  "invocationId": "1",
  "type": 1
}
```

Observed successful invocation response:

```json
{"type":3,"invocationId":"1","result":null}
```

Observed unsubscribe pattern:

```json
{
  "target": "Unsubscribe",
  "arguments": [{"topics": ["v2|en-us|6:48482_101_any|grd"]}],
  "type": 1
}
```

The UI unsubscribed from league-grid topics and subscribed to the event's `all` topic on opening the detail page. Keep a desired-topic set and correlate acknowledgements with invocation IDs. An acknowledgement establishes accepted invocation, not complete price coverage.

### 5.4 Topic forms observed

| Topic example | Observed context / interpretation |
|---|---|
| `v1\|en-us\|20030815\|grd` | V1 fixture grid |
| `v2\|en-us\|6:48482_101_any\|grd` | V2 NFL league grid |
| `v2\|en-us\|6:48482_101_any\|all` | V2 NFL event detail |
| `v1\|en-us\|20030816\|fxm-1572074895` | Specific market subscription on homepage |
| `v2\|en-us\|6:48266_101_any\|fxm-1572074895` | Related V2 market subscription on homepage |
| `v2\|en-us\|6:48482_101_any\|sbf` | Event scoreboard-related subscription; exact coverage not established |
| `v2\|en-us\|6:48482_101_any\|psts` | Event player-stat-related subscription; exact coverage not established |
| `v1\|212` | Received a pre-match-to-live transition message |
| `betbuilder\|updates\|<opaque-value>` | Pre-created combination offers |

The backslashes in this Markdown table escape pipes only; they are not wire characters.

**Do not blindly construct `_101_any` for every event.** That suffix was observed, but its semantics and source were not decoded. Read `source`, `context`, `contexts`, and the frontend subscription logic to derive correct topics, including split/linked fixtures. Do not assume `grd` streams every prop; the UI used `all` for event detail.

### 5.5 Incoming V1 update schema

Reduced actual homepage tennis update:

```json
{
  "type": 1,
  "target": "Receive",
  "arguments": [
    {
      "producer": "v1",
      "messageType": "GameUpdate",
      "payload": {
        "game": {
          "id": 1571026286,
          "name": {"value": "Match winner"},
          "results": [
            {
              "id": 2330956957,
              "odds": 1.30,
              "name": {"value": "A. Zverev"},
              "visibility": "Visible",
              "numerator": 3,
              "denominator": 10,
              "americanOdds": -325,
              "version": 11100607117
            }
          ],
          "templateId": 41271,
          "categoryId": 33,
          "visibility": "Visible",
          "isMain": true
        },
        "fixtureId": "20043239",
        "latencyMs": 543,
        "sequence": 64604
      },
      "timestamp": "2026-10-27T19:19:33Z",
      "topic": "v1|en-us|20043239|grd",
      "latencyMs": 483
    },
    4
  ]
}
```

This example proves transport and V1 update structure, not NFL/NBA template mappings. The full message had two results; this excerpt shows one.

Important differences from REST:

- V1 stream uses `game.results[]`, not `optionMarket.options[]`.
- Odds are directly on each result rather than nested under `price`.
- Stream status fields are `visibility`; REST common-model fields are `status`.
- Result `version` and payload `sequence` are present in these messages.
- The second `arguments` element is numeric. Its semantics are unknown; do not declare it a resume cursor or global version.
- The meaning and scope of both `latencyMs` fields are unknown. Preserve them as telemetry, not as proven end-to-end latency.
- Do not assume result omission means deletion or that every `GameUpdate` is a complete replacement until checked against the frontend reducer.

Observed additional message types:

```text
ScoreboardSlim
MainToLiveUpdate
```

Observed transition payload:

```json
{
  "switchedFixtures": [
    {"preMatchId": "20050677", "inPlayId": "20050711"}
  ]
}
```

This implies pre-match and in-play IDs can differ. Correct integration must preserve canonical game identity and retire superseded quotes when a transition occurs.

An actual `GameUpdate` had market `visibility: Suspended` while its results remained `Visible`. Market suspension must override selection visibility.

### 5.6 What remains unknown about streaming

- Actual V2 price-update messages and reducers.
- Whether a single update replaces a market or patches it.
- Removal message types and fixture/selection retirement behavior.
- Sequence/version scope, ordering guarantees, and reset behavior.
- Whether replay/resume exists and how it is requested.
- Reliable topic derivation for all sources and contexts.
- Whether NFL/NBA props are streamed with the expected coverage on `all`.
- Subscription limits, heartbeat timeout, rate limits, and reconnect policy.

Capture these before writing a supposedly complete streaming parser. Raw unknown messages should remain observable and trigger a conservative resnapshot when they affect price correctness.

## 6. Suggested collector design

This section is an implementation proposal, not observed server behavior.

### Phase A: reproducible access and snapshots

1. Create an isolated research project after confirming the intended implementation scope.
2. Reproduce the logged-out browser flow with ordinary browser/network instrumentation. Capture only odds-related responses and frames; redact cookies, account headers, and unrelated analytics.
3. Record current access configuration and determine whether direct requests are supported in the intended runtime. Do not present a browser-only success as a server deployment success.
4. Retrieve paginated NFL/NBA fixtures, filter competition IDs, and load full NFL event offers.
5. Store raw snapshots and parse main lines plus selected prop families.
6. Emit JSON/console output first; a UI is unnecessary to prove ingestion.

### Phase B: updates and correctness

1. Record representative V1 and V2 frames, including actual NFL/NBA changes.
2. Connect and subscribe while buffering incoming updates; acquire a complete REST snapshot; reconcile buffered updates using established version semantics.
3. Do not assume a naïve snapshot-then-subscribe sequence is gap-free.
4. Track fixture, market, selection and source versions separately until their scopes are understood.
5. Honor suspension/removal at every relevant level. Keep tombstones so delayed REST cannot revive removed prices.
6. On disconnect or an unresolvable gap, mark data stale, reconnect with backoff and jitter, resubscribe, and resnapshot. Do not assume replay.
7. Reconcile periodically, with rate-limit-aware scheduling. The frontend API's safe poll interval was not established.
8. Prove that updates refer to the same fixture/market/selection as the REST seed.

### Proposed standalone normalized record

```typescript
type NormalizedQuote = {
  book: 'betmgm';
  league: 'NFL' | 'NBA';
  source: 'V1' | 'V2';
  fixtureId: string;
  marketId: string;
  selectionId: string;
  playerId?: string;
  playerName?: string;
  family: 'moneyline' | 'spread' | 'total' | 'player_prop';
  period: string;
  stat?: string;
  comparator: 'none' | 'ou' | 'atleast' | 'yesno' | 'exact';
  outcome: string;
  line?: string;                 // exact decimal text in the research collector
  american: number;
  decimal: number;
  status: 'open' | 'suspended' | 'removed';
  receivedAt: string;
  publishedAt?: string;
  upstreamVersion?: string;
  upstreamSequence?: string;
};
```

Keep raw parameter metadata alongside normalized records. Distinguish full-game from period markets, conventional over/under from milestones, and normal odds from pre-created parlays. Missing/unrecognized statuses must not silently become `open`.

Use exact-line identities for prop ladders. Player identity must eventually use a stable cross-book registry; name slugging alone is provisional. Do not compare different players, periods, stats, or thresholds as the same quote.

Preserve supplied American and decimal odds. Do not recompute one from the other and expect exact equality, since the displayed formats may be rounded differently.

## 7. Integration with this repository

Relevant existing files/packages:

```text
app/internal/book/book.go
app/internal/model/model.go
app/internal/books/
app/internal/supervisor/
app/internal/board/board.go
app/internal/board/best.go
```

The existing contract is:

```go
Snapshot(ctx context.Context) (*book.Snapshot, error)
Stream(ctx context.Context, seed *book.Snapshot, em book.Emitter) error
// Optional:
Poll(ctx context.Context, seed *book.Snapshot) ([]model.Update, error)
```

When access and parsing are proven, a BetMGM implementation belongs in `app/internal/books/betmgm` and should follow the existing supervisor contract. Reconnection/backoff belong in the supervisor, not a second independent loop inside the book. `Snapshot.Private` can hold raw source indexes and topic derivation inputs; each stream must deep-copy mutable state.

Use `Snapshot.Subscription` to represent the desired subscription set so changes cause the expected stream restart. Report actual subscription readiness through `Emitter.Connected()`, and socket liveness through `Heartbeat()`; heartbeat traffic alone is not fresh odds data.

The model already has `FamilyPlayer`, `Subject`, `Stat`, `Comparator`, `Line`, and a provisional `PlayerSubject(name)` helper. However:

- The board's supported-family logic currently focuses on moneyline/spread/total.
- `IdentityKey` and `BoardSlotKey` exclude the line. Multiple alternate prop thresholds could overwrite each other if pushed directly into those slots.
- `QuoteKey` includes the line, but this alone does not change board slot behavior.
- Best-price comparison and the frontend must explicitly support props before enabling them in the main board.

Therefore, keep prop output isolated initially. Integration requires deliberate line/ladder storage and comparison rules, not just registering a fourth book.

## 8. Meaningful verification before claiming completion

- REST pagination finds all expected NFL/NBA events without treating partial fetches as removals.
- Initial event response plus additional offers reproduces the market list without duplicates.
- V1 REST `options` and V1 stream `results` map to the same source identities.
- Actual V2 frames update the correct normalized quotes.
- Captured NFL/NBA price changes reconcile with subsequent REST reads and the page.
- NFL prop change coverage is demonstrated, not inferred from successful subscription.
- A fixture ID containing `:` and a V2 topic context suffix survive parsing intact.
- Multi-message SignalR frames and buffered partial protocol data parse correctly.
- Market-level suspension overrides visible selections.
- Removed prices cannot be revived by an older snapshot/poll.
- Pregame-to-live transitions do not duplicate games or leave stale quotes eligible.
- Milestone and conventional over/under semantics remain distinct.
- Multiple alternate thresholds coexist without overwriting one another.
- Reconnect restores subscriptions and current state; unknown messages are visible.
- Access is verified from the eventual deployment host before claiming deployability.

Build regression fixtures from newly captured, sanitized traffic. The reduced examples in this document are not adequate as the sole test corpus.

## 9. Separate documented partner API

BetMGM also publishes a partner Sports API. This is **not** the `/cds-api` frontend API observed above.

Sources read during the investigation:

- https://sportsapi.nj.betmgm.com/articles/intro.html
- https://sportsapi.nj.betmgm.com/restapi/swagger.html
- https://sportsapi.il.betmgm.com/offer/swagger/index.html

Its documentation describes sports/competition discovery and fixture retrieval with markets and prices, including filters for competition, fixtures, in-play state, main markets, and changes since a timestamp. The introduction states that use requires registration and approval. It may be a supported alternative if access is available; do not assume approval, Ontario coverage, or interchangeability with frontend identifiers.

## 10. Suggested instruction to the implementing agent

> Start with an isolated BetMGM research collector using this handoff. Validate current access and capture representative REST and WebSocket traffic. Prove NFL/NBA main-line extraction and NFL player-prop extraction. Decode V1 and V2 updates from actual traffic, including suspension, removal, reconnect, and pregame/live transitions. Keep unverified behavior explicit. Do not modify or deploy the production app until the collector is demonstrated and integration is requested. Preserve exact prop thresholds and comparator semantics. Report browser-dependent access separately from standalone-server access.
