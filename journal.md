# Project journal

## 2026-10-25 — DraftKings/FanDuel Go port, soak, normalization, and NBA markets

### Goal

Port the existing JavaScript DraftKings and FanDuel REST/WebSocket clients to Go,
verify parity on live Ontario feeds, run a long soak, and build a separate
measurement/normalization layer that can compare the books across sports,
mainlines, alternate lines and player props.

The work split into three separate pieces:

1. `go/` — faithful transport/parsing port plus soak harness.
2. `normalizer/` — independent module for canonical keys and measurement.
3. `captures/nba/` and the NBA API investigation — event-level alternate/prop
   discovery, including FanDuel's MGA and event-page routes.

---

## 1. Go port

Created `go/` as module `sportsbookfeed`, with the only external dependency being
`github.com/gorilla/websocket v1.5.3`.

Commands:

```text
dk-snapshot
dk-ws       -mode probe|partials
fd-discover
fd-rest
fd-ws       -live
verify
soak
```

What was verified:

- DK snapshot parity: 26 events / 78 markets / 156 selections with identical
  entity IDs and partial keys.
- FD normalized rows: 156/156 identical to `extract-fd-nfl.cjs`.
- FD GraphQL body (2,671 B) and SMP body (3,477 B) byte-identical.
- FD AppSync received live data frames.
- SMP POST returned HTTP 500 with Go's default User-Agent; setting a Chrome UA
  and `Accept: */*` returned the byte-identical body.

Operational fixes required on this machine:

- Workspace-local `GOPATH`, `GOMODCACHE`, `GOCACHE`, and `TMP/TEMP` because the
  sandbox cannot write the default user-profile caches.
- Windows ACL repair for workspace writes; pre-existing `captures/` remained
  sandbox-unwritable, so analysis outputs went to `out/` and `work/`.
- DK WebSocket closes at 1800.05 s (`1006`); the harness reconnects in ~1.6 s.
- FD reconnects/resubscribes were local discovery changes, not socket failures.

### 1-hour soak

`soak -duration 1h` ran 2026-10-25 16:20–17:20Z, spanning NFL kickoff.

- 99.94% DK uptime, 99.93% FD uptime.
- 0 REST errors; 120 reconciliations per book.
- 57.2 MB in `out/soak-1h/`.
- DK closed its socket at exactly 30 minutes; reconnect succeeded.
- FD resubscribed twice when its discovery market set changed.

### 3-hour soak

Ran 2026-10-25T17:39:23Z to 20:39:23Z, duration 10,800 s, output `out/soak-3h/`.
Final summary:

- DK: 7 connections, 6 reconnects, 43,933 frames, 99.917% uptime, 0 REST
  errors.
- FD: 5 connections, 4 reconnects, 11,058 frames, 506 data frames, 99.951%
  uptime, 0 REST errors.
- Reconcile: DK 360 checks / 22,230 compared / 274 price diffs; FD 360 checks /
  11,286 compared / 157 price diffs / 30 line diffs.
- The soak’s FD subscription covered the discovered mainline markets only, so it
  did not prove alternate/prop WS coverage by itself.


---

## 2. Normalizer

Created `normalizer/` as a separate Go module (`sportsbook-normalizer`) with no
dependency on the feed clients. It replays raw payloads and measures them.

Canonical quote key:

```text
sport | event-scope | family | period | subject | stat | comparator | line | outcome
```

Identity key is the same but **without line**. Line is deliberately part of the
quote key, not the identity key, so "same market, different line" is measurable
rather than silently collapsed.

Event scope:

- sorted canonical team IDs + UTC date;
- team aliases / mascot fallback;
- event matching within ±15 minutes.

Source identity is separate and preserved as strings:

```text
book | eventId | marketId | selectionId
```

The normalizer measures:

- event and board overlap, separately from normalization quality;
- quote identity coverage (line ignored);
- exact-line coverage;
- line mismatches;
- price dispersion on exact-line matches;
- taxonomy coverage and high-confidence coverage;
- dangling records and data-quality issues.

### NFL mainline results

Pregame (25 games, exact same timestamp):

- 25/25 events matched, 100% identity.
- 124/150 exact-line quotes (82.7%).
- 26 line mismatches.
- Same price on 28/124 exact-line matches (22.6%).
- Median absolute American difference: 6.

Live (12 games, exact same timestamp):

- 12/12 events, 100% identity.
- 58/72 exact-line (80.6%).
- 14 line mismatches.
- Same price on 12/58 (20.7%).
- Median absolute American difference: 10.

### NBA mainline results

Boards only overlapped on 7 of 18 FanDuel events (DK had 12 events including five
Christmas games; FD had 18 including ten suffix-named games and one late game).

Conditional on the seven shared games:

- identity 42/42 (100%);
- exact line 34/42 (81.0%);
- 8 line differences;
- same price 6/34 (17.6%);
- median fair-probability gap 1.08%;
- max fair gap 1.94%.

The unadjusted 34/72 figure is a slate artifact, not a normalization failure.

### Price-difference decomposition

On matched exact lines:

- NFL pregame median fair-probability gap 0.87%, 98.4% within 2%;
- NFL live median 1.03%, 82.8% within 2%;
- NBA shared median 1.08%, 100% within 2%;
- pregame overrounds were identical (4.71% DK / 4.71% FD);
- live DK overround was wider (6.71% vs 5.91%);
- best-price outcomes were symmetric in pregame NFL (58/58) and NBA (18/18),
  so neither book was systematically better.

Conclusion: same-line price differences are mostly vig/rounding. The bigger
structural issue is line differences (17–19%), followed by board/slate and
period/inventory differences.

One NFL favorite flip (DK Jets/Raiders) conflicts with DK's own moneyline,
indicating stale/inconsistent DK data. The NBA Pistons/76ers flip is internally
consistent on both books and is a real disagreement.

### Taxonomy

All sampled market types now get a family. High-confidence coverage:

- FD NFL 81.7%;
- FD NBA league board 32.6%;
- FD NHL 54.9%;
- DK NBA 100%.

Futures/awards are low-confidence and not cross-book comparable. Unknown handling
still exists and is unit-tested.

### NFL alts

- FD: 18 full-game player prop markets (alt passing, alt receiving, anytime TD),
  46 futures, 14 specials.
- DK sampled alternate subcategories: 1st-half passing 10 markets/270
  selections; 1st-half receiving 49/562; 2nd-half/4th-quarter TD 9/190;
  tackles/assists 37 each.
- Player overlap: passing 0 common (10 DK vs 6 FD); receiving 0 common (49 vs 9);
  anytime TD 24 common players but different periods (DK partial-game vs FD full
  game).
- Exact cross-book NFL alt identity in sampled sets: 0.

This is an inventory/period problem, not a parser failure.

---

## 3. NBA mainlines and alternate markets

### DraftKings

Base: `https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1`

| Purpose | Path |
|---|---|
| Main lines | `/leagues/42648` |
| Alternate spreads | `/leagues/42648/categories/487/subcategories/13202` |
| Alternate totals | `/leagues/42648/categories/487/subcategories/13201` |

Observed:

- Main: 12 events / 36 markets / 72 selections.
- Alt spreads: 7 events / 7 markets / 560 selections.
- Alt totals: 7 events / 7 markets / 616 selections.
- Alt market types: `Spread Alternate` (`2_628`) and `Total Alternate` (`3_628`).
- Alternate markets also have `main:true`; classify by subcategory/type, not that
  flag.
- Lakers–Jazz `34888793`: alt spread `2_86923924` has 80 selections, Lakers
  −27.5 to +14.5, main −6.5 absent. Alt total `3_86923924` has 88 selections,
  215.5–259.5, main 237.5 absent.
- Alternate endpoints contain only the seven current games; the five Christmas
  games are main-only in this capture.
- Preserve `selection.points`, `outcomeType`, `participants`, `displayOdds`, and
  `trueOdds`. DK display decimal can be rounded while `trueOdds` is precise;
  277/560 alt-spread selections differed.

### FanDuel league board and event tabs

Base: `https://sbapi.on.sportsbook.fanduel.ca/api`

1. `GET /content-managed-page?page=CUSTOM&customPageId=nba&includePrices=true&_ak=...`
2. Filter `MONEY_LINE`, `MATCH_HANDICAP_(2-WAY)`, `TOTAL_POINTS_(OVER/UNDER)`.
3. Per event: `GET /event-page?_ak=...&eventId=...&tab=popular`.
4. Read actual `attachments.markets` entries of type `ALTERNATE_HANDICAPS` and
   `ALTERNATE_TOTAL_POINTS`.

Observed:

- Filtered main: 17 events / 51 markets / 102 selections from a raw board with
  19 events / 255 markets (including futures).
- Default event page: 8 markets, no alternate ladders; layout advertises
  alternates with `attachmentsFullyLoaded:false`.
- Popular tab: 80 markets, including 118 alternate-spread and 157
  alternate-total runners; all 275 ACTIVE and priced.
- Alt runner `handicap` is 0; the line is only in `runnerName`
  (`Los Angeles Lakers +24.5`, `Over 201.5`).
- FD alt ladders include the main line (−6.5 spread, 237.5 total), unlike DK.
- FD alt total is 77 Over + 80 Under; missing sides are Over 197.5–200.5 and
  Under 277.5.
- Selection IDs recur across markets; the stable key is marketId + selectionId.

### FanDuel price refresh

`POST https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0`

Headers:

```text
Content-Type: application/json
X-Application: FhMFpcPWXMeyZxOx
```

Body includes all five sampled market IDs. Response: 5 markets OPEN, runner counts
2, 2, 118, 2, 157; all priced and identical to the Popular-tab odds.

Price responses contain selection IDs and odds but omit runner names. Keep the
discovery mapping.

### FanDuel full event market feed (MGA)

The event-page Popular tab is not the full inventory. The MGA facet-search
endpoint returns every market for an event:

```text
POST https://scan.on.sportsbook.fanduel.ca/api/sports/navigation/facet/v1.0/search
Content-Type: application/json
X-Application: <FanDuel app key>

{
  "filter": {
    "eventIds": [36229340],
    "contentGroup": {"language": "en", "regionCode": "NAMERICA"},
    "maxResults": 0,
    "selectBy": "RANK"
  },
  "facets": [{"type": "EVENT"}, {"type": "MARKET"}],
  "currencyCode": "USD"
}
```

`regionCode` must be `NAMERICA`; `ON`, `CA`, `CANADA`, etc. return
`INVALID_REGION`.

For Pistons @ 76ers (`36229340`):

- 268 markets / 1,521 runners.
- Full-game props: 10 points, 10 rebounds, 7 assists, 7 threes.
- 69 milestone props, 7 player-quarter props, 2 alternate ladders, 156 other
  game/quarter/half markets.
- Prices fetched separately from SMP.

### NBA full-game prop comparison

DK and FD had the same 34 full-game prop markets and the same 10 players.

- 68 common player/stat/outcomes.
- 58 exact-line (85.3%).
- 10 line differences across 5 player/stat pairs:
  LeBron rebounds 7.5 vs 6.5; LeBron assists 6.5 vs 7.5; VJ Edgecombe rebounds
  5.5 vs 6.5; Paul Reed points 14.5 vs 15.5; Adem Bona points 5.5 vs 6.5.
- Same American price: 3/58.
- Median fair-probability gap 1.38%; max 5.79% (Cade Cunningham assists, FD +106
  vs DK −119).
- By stat medians: points 0.49%, threes 1.25%, assists 2.20%, rebounds 2.26%.
- 38/58 within 2% fair probability.

This corrected the earlier conclusion that NBA props were unavailable. They were
missing only because the league page omits them.

### Comparison of methods

- FanDuel Popular tab: complete for alternate spreads/totals (118/157 matches
  MGA), but only 80 markets versus 363 in MGA for the same event.
- FanDuel MGA: full inventory including player props/period markets.
- DK: main + separate alternate subcategory snapshots; alt ladders omit the main
  line.
- For alternate-only collection, event-page Popular is sufficient.
- For full props/period inventory, MGA is required.

---

### FanDuel event-level adapter (done)

Implemented `fd-event` and the FanDuel event-level normalizer support:

- `go/internal/fd/event.go`:
  - event-page Popular fetch;
  - MGA MARKET facet fetch;
  - board merge;
  - SMP price application.
- `go/cmd/oddsfeed/fd_event.go`:
  - writes `popular.json`, `mga.json`, `prices.json`, `board.json`.
- Normalizer:
  - `ALTERNATE_HANDICAP(S)` and `ALTERNATE_TOTAL(_POINTS)` classification;
  - signed alt spread line parsed from `runnerName` when `handicap:0`;
  - alt total line parsed from `runnerName`;
  - MGA `PLAYER_*_TOTAL_*` props (points/rebounds/assists/threes);
  - `TO_RECORD_*`/`TO_SCORE_*` as non-comparable milestones.

Real Lakers–Jazz run:

```text
event=36229335 sources=popular+mga events=1 markets=205 pricedMarkets=205
```

Normalized: 205 markets, 694 quotes, 89.3% classified, 22 unknown, 0 dangling.
High-confidence is 4.4% because most of the event is props/milestones; the
mainline and alternate spread/total subset is high-confidence.

MGA gotchas fixed:

- numeric `eventIds`, not strings;
- `regionCode: NAMERICA`;
- `X-Application` header required, otherwise generic HTTP 500;
- MGA returns metadata without prices; SMP fills `marketId+selectionId`.

This closes the FanDuel event-level discovery gap. It does not yet merge main +
alternate ladders or compare all games; that is the next item.

### Unified DraftKings discovery + main/alt merge (done)

Added:

- `go/internal/dk/merge.go` — `MergeData` dedupes events/markets/selections by
  ID, preserving the first (main) description.
- `go/cmd/oddsfeed/dk_discover.go` — `dk-discover` fetches and merges:
  - NBA main `/leagues/42648`;
  - NBA alt spread `13202`, alt total `13201`;
  - NBA props auto-selected from categories Player Points/Rebounds/Assists/
    Threes/Defense;
  - NFL main `/leagues/88808`;
  - NFL alt spread `13195`, alt total `13196`;
  - NFL props auto-selected from Passing/Receiving/Rushing/Defensive/TD
    Scorers;
  - explicit `-props` override and `-no-props` opt-out.

Auto-selection reads the main snapshot's `categories`/`subcategories` catalog,
then chooses O/U subcategories in the sport allowlist plus the full-game
`TD Scorer` market. This replaced the hardcoded prop IDs.

Outputs: one raw file per source, plus `board.json` and `meta.json`.

Measured:

- NBA: 12 events / 297 markets / 1,534 selections; 7 auto-selected prop
  subcategories; normalizer 1,534 comparable quotes, 100% classified, 16.8%
  high-confidence (props use 0.85 confidence).
- NFL: 12 events / 395 markets / 1,586 selections; 26 auto-selected prop
  subcategories; normalizer 1,586 comparable quotes, 100% classified, 11.6%
  high-confidence.
- Prop stats: NBA points/rebounds/assists/threes/steals/blocks; NFL
  passing/rushing/receiving/TD/tackles/sacks/longest-play.
- Alternate spread/total markets are marked `Alt=true`.

The unified board is now accepted directly by `normalizer/adapters.DK` and by
`normctl -dk-nba/-dk-partial`; no Node concatenation is needed.

Current live PM-vs-unified-DK exact-line overlap: 38 quotes NBA / 68 NFL. The
earlier ad hoc combined captures produced 90/74; that difference is live line
movement and capture timing, not a merge regression.


---

## 4. Polymarket integration

Added Polymarket to the ring on 2026-10-25:

- `go/internal/pm/` — Gamma REST, CLOB REST book, and CLOB market WebSocket.
- `go/cmd/oddsfeed/pm.go` — `pm-discover` and `pm-ws`.
- `normalizer/internal/adapters/pm.go` — Gamma binary markets to canonical
  quotes.
- `normalizer/cmd/pmctl/` — standalone PM-vs-DK coverage/overlap report.

### Endpoints

- Gamma sports: `https://gamma-api.polymarket.com/sports`
  - NBA series `10345`, NFL series `12185`.
  - Events: `/events?series_id=...&closed=false&limit=...`.
  - Event: `/events/{id}`.
- CLOB book: `https://clob.polymarket.com/book?token_id=...`.
- CLOB market WS: `wss://ws-subscriptions-clob.polymarket.com/ws/market`.
  - Subscribe `{assets_ids, type:"market", custom_feature_enabled:true}`.
  - `PING` every 10 s; server returns `PONG`.
  - Frames: `book`, `price_change`, `best_bid_ask`, `last_trade_price`,
    `new_market`, `market_resolved`.

Observed:

- NBA: 100 open events / 4,760 markets (before event de-duplication).
- NFL: 62 open events / 14,480 markets.
- Lakers–Jazz: 472 markets, 926 normalized quotes.
- 20-second market WS test: 27,085 frames across 932 asset IDs.

### Normalization mapping

- `moneyline`: outcomes are teams; binary prices.
- `spreads`: each Gamma market is one line; named team gets `line`, opponent gets
  `-line`.
- `totals`: each market is one line; Over/Under.
- `team_totals`: line parsed from question; team subject.
- Player props: `points`, `rebounds`, `assists`, `threes`, `passing_yards`,
  `receiving_yards`, `receptions`, `anytime_touchdowns`, etc.
- Periods: `first_half_*` → h1, `second_half_*` → h2, `qN_*` → qN.
- Gamma splits game, props and exact-margin markets into separate events with
  the same teams/start; PM adapter merges them under one event key.
- Main line is the most liquid spread/total line; other full-game lines are
  marked `Alt`.
- Binary second-side price is the complement of the first side; the first version
  incorrectly copied the same price to both sides and was fixed.

### One-game overlap with DK

| Game | PM markets | PM quotes | PM main/alt | Identity vs DK | Exact line vs DK |
|---|---:|---:|---:|---:|---:|
| Lakers–Jazz | 472 | 926 | 6 / 920 | 6/6 | 90 |
| Packers–Lions (+ props) | 544 | 892 | 8 / 884 | 6/6 | 74 |

The exact-line matches are the overlapping ladder rungs against combined DK
main + alternate snapshots. The six identities are moneyline, spread and total,
both sides.

### Three-way line interaction

For the NBA Lakers–Jazz fixture, all three books agreed on the main numbers
(−6.5 spread, 237.5 total), but their alternate ladder widths differed:

| Ladder | DK | FD | PM | Three-way common |
|---|---:|---:|---:|---:|
| Lakers spread rungs | 40 | 59 | 33 | **24** |
| Total rungs | 44 | 77 | 25 | **18** |

For NFL Packers–Lions the ladder overlap was:

| Ladder | DK | FD | PM | Three-way common |
|---|---:|---:|---:|---:|
| Packers spread rungs | 25 | 45 | 34 | **22** |
| Total rungs | 33 | 56 | 21 | **14** |

Those are **line-set intersections**, not synchronized price comparisons. The
earlier three-way table used a DK snapshot from 16:55Z (pregame) against later
FD/PM captures; that was not a like-for-like comparison. Current DK status for
both games is **STARTED**.

Current synchronized mainlines:

- NFL (22:21Z): DK Packers +2.5 / 52.5; FD Packers +2.5 / 52.5; PM Lions −3.5 /
  52.5. ML: DK +163, FD +176, PM .375 for Packers.
- NBA (22:22Z): DK Lakers −7.5 / 260.5; FD Lakers −6.5 / 261.5; PM Lakers −6.5 /
  237.5. PM's total is still on the old 237.5 line while the sportsbooks have
  moved to 260.5/261.5; that is a stale/misaligned PM total, not a three-way
  price disagreement.

The correct comparison requires a synchronized snapshot, the same period/state,
and de-vigged fair probabilities.

### Known PM issues

- Prices are probabilities, not sportsbook American odds. Compare de-vigged fair
  probabilities; raw American comparison is invalid.
- Gamma `outcomePrices` are JSON strings; volume/liquidity can be strings.
- Gamma `gameId` and `sport` can be numbers/objects.
- Finished games remain in `closed=false` results; current-slate filtering is
  required.
- `exact_margin` and some specials are normalized as unknown (843 NBA / 811 NFL
  markets across the snapshots).
- Player names are name slugs; a player registry is still needed for cross-book
  prop matching.
- The main/alt heuristic uses liquidity/volume; it needs validation against
  DK/FD main-line labels.

### All-game NBA comparison (done)

Added `fd-events`, which discovers all current FanDuel game events and merges
their Popular + MGA + SMP data into one board.

All-game capture (2026-10-25T22:49Z, 3.6 s skew):

- 7 shared NBA events.
- DK auto: 12 events / 293 markets / 1,040 comparable quotes (the auto-prop
  board had 297/1,534 in the standalone run; the live capture dropped four
  markets).
- FD all: 7 events / 2,073 markets / 3,067 comparable quotes.
- DK/FD player props: 486 DK quote sides, 724 FD, **384 exact-line matched**;
  15 same price, 369 different.
- PM vs DK: 900 exact-line matches, 674 fair comparisons; median 2.63%, props
  332 sides at 8.40%.
- PM vs FD: 1,200 exact-line matches, 792 fair comparisons including **370
  player-prop** sides; median 2.70%, max 32.18%; props median 6.35%.
- PM/DK props did align on 332 sides; the remaining gap is six missing PM
  players and PM ladder/line differences.

The aggregate identity rate is low because FD exposes much more alt/prop
inventory than DK. The family/exact-line numbers are the meaningful ones.

Why DK is not perfectly aligned:

- All 64 DK players appear in FD; FD has 92 props players.
- FD lacks some DK defensive props: 46 DK steals, 22 blocks and 10
  steals+blocks quote sides; only part of that set exists on the FD event
  boards, and FD has no steals+blocks O/U. This explains 38 of the 46 remaining
  DK/FD unmatched identities.
- The other 8 unmatched DK/FD identities are assists/threes for players FD does
  not list for that stat.
- All 56 DK/FD same-identity line mismatches are one point.
- PM is missing six DK players: Ron Holland II, Anthony Black, Jase Richardson,
  Ja’Kobe Walter, Daniss Jenkins, Isaiah Joe.
- Of 432 DK/PM identity matches, only 332 have the DK line in PM’s ladder; the
  other 100 are one or two points away.

DK/FD exact-line prop coverage: 384/486 = 79.0%. DK/PM: 332/486 = 68.3%.

### De-vig + synchronized snapshot (done)

Added `normalizer/internal/price`:

- converts each quote to implied probability;
- de-vigs only two-outcome groups at the same canonical line;
- compares PM mid probability to sportsbook fair probability on exact-line
  matches;
- reports median/mean/max absolute fair-probability gap and counts within
  1/2/5 percentage points;
- skips three-way/push groups until those settlement states are modeled.

Added `capture-tri.ps1`:

- runs DK unified discovery, FD event discovery and PM single-event discovery
  concurrently;
- records start/end and file times;
- invokes `pmctl` with `-dk`, `-fd` and `-max-skew`.

Synchronized NBA run (Lakers–Jazz, 2026-10-25T22:47:21Z, later auto-props run):

- Capture skew: **0.99 s**; synchronized.
- PM vs DK: 20 common exact-line outcomes; median fair gap **2.07%**, max
  3.96%.
- PM vs FD: 66 common outcomes; median **4.19%**, max 24.75%.
- By family: PM-vs-DK moneyline 3.29%, spread 2.54%, total 1.71%; PM-vs-FD
  moneyline 0.65%, spread 0.91%, total 3.49%, player props 7.52%.

The first synchronized run (main/alt-only DK board) gave PM-vs-DK 2.26% and
PM-vs-FD 6.35%; the difference is the newer capture and the auto-prop board.

The large total gaps are not a de-vig failure: PM's total was still on 237.5
while the live sportsbooks had moved to ~260.5/261.5. De-vig removes vig, not
stale lines. The timestamp guard and same-line matching are working; the next
filtering step is to reject internally stale PM markets.

Without de-vig the same run gives ~13% median gaps; with de-vig it drops to
~2.3% for DK and ~6.3% for FD, confirming that most of the raw gap was margin.

### Tests

`go test ./...` and `normalizer/.../go test ./...` pass, including PM binary
price, complement, American conversion, spread sign, total, player prop and
de-vig tests.

---

## 5. Verification of Astra's NBA report

Re-fetched all three DK endpoints and FD `event-page?tab=popular`; all 200 with
identical counts. Confirmed:

- DK counts, types, subcategories, `main:true` alternates, ranges, main-line
  omission.
- FD filtered main 17/51/102; raw board 19/255.
- FD default event 8 markets, 17 `attachmentsFullyLoaded:false` cards.
- FD Popular 118/157 alternate runners, all active/priced.
- SMP five-market refresh; odds identical.
- FD `handicap:0` and line-in-name.
- Selection ID reuse; use marketId + selectionId.
- DK display vs true odds differences.

Corrections/nuances recorded in `NBA-MARKETS-REPORT.md`:

1. "Future fixtures" applies to DK main (Christmas games), not DK alternates;
   alternate endpoints returned only the seven current games.
2. FD alternate ladders include the main line; DK alternates omit it. Dedupe FD,
   add DK main separately.
3. FD alt total 157 = 77 Over + 80 Under; missing Over 197.5–200.5 and Under
   277.5.
4. Popular is complete for alternates but not full inventory (80 vs 363 markets).
5. The report's counts and the "275 active/priced" claim are correct.

WebSocket claims were appropriately conservative: Astra did not verify NBA
alternate socket updates or latency. That remains open.

---

## 6. Current state

### Files

- `go/` — Go port and soak harness.
- `go/internal/pm/` — Polymarket Gamma REST and CLOB market WebSocket.
- `normalizer/` — canonical model, adapters, report.
- `normalizer/internal/adapters/pm.go` — Polymarket binary-market adapter.
- `normalizer/cmd/pmctl/` — Polymarket-vs-DK coverage tool.
- `normalizer/REPORT.md` — measured results and difference decomposition.
- `normalizer/README.md` — usage and limitations.
- `normalizer/DESIGN.md` — framework design.
- `NBA-MARKETS-REPORT.md` — main/alternate endpoint report plus verification.
- `captures/nba/` — raw DK/FD NBA main/alternate captures and metadata.
- `captures/` — original JS/Node captures; partially sandbox-unwritable.
- `out/soak-1h/`, `out/soak-3h/` — soak outputs.
- `out/norm/` — normalized fixtures, reports and JSON dumps.
- `out/norm/fd-mga-event-market.json` — 268-market FD event feed.
- `out/norm/fd-event-prices.json` — SMP prices for those markets.
- `out/pm-nba-events.json`, `out/pm-nfl-events.json` — Gamma snapshots.
- `out/pm-nba-lakers-jazz.json`, `out/pm-nfl-packers-lions-full.json` — test events.
- `out/pm/` — PM-vs-DK reports and quote dumps.
- `out/ws-proof/` — WS coverage proof logs and summary.

### Verified endpoint summary

| Book | Data | Endpoint |
|---|---|---|
| DK | NBA main | `/leagues/42648` |
| DK | NBA alt spread | `/leagues/42648/categories/487/subcategories/13202` |
| DK | NBA alt total | `/leagues/42648/categories/487/subcategories/13201` |
| DK | NFL main | `/leagues/88808` |
| DK | NFL alt spread | `/leagues/88808/categories/492/subcategories/13195` |
| DK | NFL alt total | `/leagues/88808/categories/492/subcategories/13196` |
| FD | NBA/NFL main | `/content-managed-page?page=CUSTOM&customPageId=nba|nfl` |
| FD | Event alternates | `/event-page?eventId=...&tab=popular` |
| FD | Full event markets | MGA `scan.{state}.../facet/v1.0/search` with `MARKET` facet |
| FD | Prices | SMP `getMarketPrices` |

### Known limitations

- No production always-on collector; soak harness is the current long-running
  process.
- NBA next week is not posted yet; league/competition queries return current
  slate and futures only.
- NFL alt periods differ between books (DK 1st half/quarter, FD full game), so
  exact NFL alt comparison is still 0.
- NBA milestone/quarter/ladder markets are available on FD but the DK event-level
  counterparts have not been fetched.
- Player identity is name-slug based; sampled NBA full-game props matched 10/10
  names, but a registry is required for scale.
- WebSocket coverage is verified for mainlines, alternate spreads/totals and
  player props on both books:
  - DK: mainlines (3-hour soak), NBA props, NBA/NFL alternate spreads/totals;
  - FD: mainlines (3-hour soak), NBA main/alternate/props, live NFL
    main/alternate/props.
  Remaining work is multiplexing all DK class subscriptions on one connection and
  a FanDuel reconnect/keepalive soak.
- FD selection IDs are not globally unique; always pair marketId + selectionId.
- DK alternate ladders omit the main line; FD ladders include it.

### Transport conclusion: hybrid, not WebSocket-only

WebSockets are an update transport, not a discovery mechanism.

- REST is mandatory for:
  - event/schedule discovery;
  - market, selection and runner IDs;
  - alternate ladder names/lines (especially FD `runnerName` with `handicap:0`);
  - player-prop menus and period markets;
  - reconnect/reconciliation, deletions and suspensions;
  - new alt rungs/props appearing during the day.
- WebSockets are appropriate for:
  - live price updates after REST bootstrap;
  - mainlines where subscriptions are already verified.
- Verified:
  - DK player-prop subscription for subcategory 12488 produced 7 updates in
    30 seconds, including a market add and Jaren Jackson Jr. Over/Under
    selection changes at 17.5 with new odds.
  - DK alternate spread and total subscriptions produced selection-level changes
    for both NFL (687/418) and NBA (82/84).
  - FanDuel one-socket subscriptions produced data frames for mainlines, NBA
    alternate spread/total, NBA points props, live NFL alternate spread/total
    and a live NFL passing prop.
- Remaining work:
  - multiplex all DK class subscriptions on one connection (the server already
    supports multiple subscription IDs);
  - soak FanDuel reconnect/keepalive across its 300-second connection timeout;
  - if a class does not update, poll REST (FD SMP, DK snapshots) as fallback.
- Reconciliation must remain: a missed WS frame, a deleted rung, or a DK 30-minute
  close cannot be recovered from the socket alone.

The correct architecture is therefore: **REST discovery/bootstrap +
WebSocket price updates + REST reconciliation/fallback**.

### WS coverage tests (20:34–20:53Z)

All tests used REST-discovered market IDs/subcategories and observed actual
selection-level data frames.

DK:

- NBA player points (`12488`): 30 s, seven updates, including a market add and
  Jaren Jackson Jr. Over/Under changes at 17.5. **Prop price/line updates
  proven.**
- NFL alternate spread (`13195`): 45 s, 687 selection changes, 20 adds, 22
  removes. **Alternate spread updates proven.**
- NFL alternate total (`13196`): 30 s, 418 selection changes, 2 adds, 2 removes.
  **Alternate total updates proven.**
- NBA alternate spread (`13202`): controlled 120 s test; 82 selection changes;
  REST moved 60 selections during the same window. **NBA alternate spread
  proven.**
- NBA alternate total (`13201`): 90 s, 84 selection changes. **NBA alternate
  total proven.**
- Mainlines: the 3-hour soak produced 43,912 DK updates and continuous
  reconciliation. **Mainline updates proven.**

FanDuel:

- NBA 103-ID subscription (mainlines + all current NBA alternate spreads/totals
  + points props) on one socket: 20 data frames across 15 markets, including:
  - `ALTERNATE_HANDICAPS` (118 runners);
  - `ALTERNATE_TOTAL_POINTS` (157/158 runners, two frames);
  - multiple `PLAYER_*_TOTAL_POINTS` props with runner odds changes;
  - a mainline moneyline frame.
  **NBA mainline, alternate and prop coverage proven on one socket.**
- Live NFL six-ID subscription (mainline + alternate spread + alternate total +
  passing prop): 6 data frames across 5 markets, including the 90-runner
  alternate spread, 112-runner alternate total and the passing prop.
  **NFL mainline/alternate/prop coverage proven on one socket.**

What remains:

- The DK tests proved each class on its own subscription; the next wiring step is
  to send all class subscriptions on one DK connection. The server already
  demonstrates multiple subscription IDs on one connection.
- FD reconnect/keepalive across the 300-second connection timeout still needs a
  soak-style run.
- WS frames are price/line updates, not discovery. REST remains required.




---

## 7. Next steps

1. **DONE (2026-10-25):** Wire FanDuel **event-page Popular + MGA + SMP** into
   the Go client as `fd-event`, and add normalizer support for event-level
   mainlines, alternate spreads/totals and MGA player props. Measured Lakers–Jazz:
   205 markets / 694 quotes, 89.3% classified, 22 unknown.
2. **DONE (2026-10-25):** DK prop subcategories are now auto-selected from the
   category catalog (NBA 7, NFL 26), and DK prop market types map to canonical
   stats (points/rebounds/assists/threes/defense; passing/rushing/receiving/
   TD/tackles). `-props` remains an override.
3. **DONE (2026-10-25):** `dk-discover`/`MergeData` deduplicates main + alternate
   ladders by source ID; exact signed lines are preserved in the normalizer.
4. **DONE (2026-10-25):** expanded to all 7 shared NBA games. DK/FD props:
   384 exact-line quote sides; PM/DK: 332 prop sides; PM/FD: 370 prop sides.
   See the all-game section above.
5. Fetch DK event-level milestones/quarter props and compare to FD.
6. Multiplex all DK class subscriptions on one connection and soak FanDuel
   reconnect/keepalive across its 300-second connection timeout.
7. **DONE (2026-10-25):** de-vig/fair-probability comparator plus synchronized
   `capture-tri.ps1` capture with `-max-skew` guard. Synchronized NBA run at
   0.25 s skew produced median fair gaps 2.26% (PM-DK) and 6.35% (PM-FD), versus
   ~13% raw.
8. Filter Gamma snapshots to the current slate and validate PM main/alt labels
   against DK/FD.
9. Add player registry and period/settlement rule packs.
10. Run a fresh soak once the event-level collectors are integrated.

