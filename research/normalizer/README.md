# Cross-book normalization framework (separate workstream)

> Historical research prototype. The submitted application is in [app/](../../app/); see the [submission README](../../README.md) for current behavior. Status and limitations below describe this experiment.

Status: **working reference prototype**, measured on real DraftKings and
FanDuel captures. It is intentionally a separate module from the feed clients in
`../go/`; it consumes raw payloads and does not depend on the transports.

## What it does

- Parses DraftKings snapshots/partial caches and FanDuel boards.
- Resolves events and participants to canonical IDs.
- Classifies main, prop, futures and special markets into a common taxonomy.
- Emits **atomic quotes** with a cross-book key:
  `sport | event-scope | family | period | subject | stat | comparator | line | outcome`.
- Measures event matching, quote-identity coverage, exact-line coverage, line
  mismatches, price dispersion, taxonomy coverage and unusable records.
- Handles NFL main markets across both books, and classifies NFL/NBA/NHL props,
  futures and specials. Unknown types are surfaced, never silently compared.

## Run it

Run the following from `research/`; command paths and capture/output paths
below are relative to that directory unless stated otherwise.

```powershell
cd normalizer
$env:GOCACHE = 'D:\Projects\BitStamp_Interview\.gocache'
$env:TMP     = 'D:\Projects\BitStamp_Interview\research\go\.gotmp'
$env:TEMP    = $env:TMP
go build -o normctl.exe ./cmd/normctl

cd ..
.\normalizer\normctl.exe -dump
```

Outputs:

```text
out/norm/report.json          machine-readable measurements
out/norm/REPORT.md            generated report
out/norm/pregame-dk-quotes.json
out/norm/pregame-fd-quotes.json
out/norm/live-dk-quotes.json
out/norm/live-fd-quotes.json
out/norm/tax-fanduel-nfl.json
out/norm/tax-fanduel-nba.json
out/norm/tax-fanduel-nhl.json
out/norm/tax-draftkings-nfl.json
```

The default fixtures are the exact-timestamp pair from the one-hour soak:
`snapshot-0071`/`discovery-0008` at 16:55:28Z (25 games, 75 main markets) and
`snapshot-0111`/`discovery-0012` at 17:15:28Z (live subset). NBA/NHL boards were
captured from FanDuel's `content-managed-page` with `customPageId=nba|nhl`.

## Measured result (summary)

| Check | Pregame NFL | Live NFL | NBA (shared events) |
|---|---:|---:|---:|
| Events | 25/25 | 12/12 | 7/18 shared |
| Median start-time skew | 60 s | 60 s | 60 s |
| Event + outcome identity coverage | **100%** | **100%** | **100%** |
| Exact-line quotes | 124/150 (82.7%) | 58/72 (80.6%) | 34/42 (81.0%) |
| Line mismatches (real half-points) | 26 | 14 | 8 |
| Same price on matched line | 28/124 (22.6%) | 12/58 (20.7%) | 6/34 (17.6%) |
| Median absolute American diff | 6 | 10 | 5 |

The NBA “7/18 shared” is a slate difference, not a matching failure: DK had five
Christmas games in its board while FanDuel had ten suffix-named games and a late
game. Conditional on the seven common games, NBA normalizes as well as NFL.
Full 12/18 unadjusted event overlap is 30.4% union / 38.9% of the larger board;
the unadjusted exact-line figure (34/72) is misleading because it includes
unmatched slates.

**What drives the differences:** line mismatches are 17–19% of mainline quotes.
On the same line, the median fair-probability gap is only 0.87%/1.03%/1.08%
(NFL pregame/live/NBA), and 98%/83%/100% are within 2%. Pregame NFL and NBA
overrounds are identical (4.71%), and best-price outcomes are symmetric, so the
books are not systematically better. Large American differences are mostly
scale (`+100` vs `−106` ≈ 1.5%), not entirely different values. Live DK has a
wider margin (6.71% vs 5.91%), and a minority of lines are genuine disagreements
or stale/inconsistent quotes. Details in [REPORT.md](REPORT.md).

Taxonomy coverage:

| Book / sport | Markets | Quotes | Comparable | Classified | High confidence | Unknown | Dangling |
|---|---:|---:|---:|---:|---:|---:|---:|
| FanDuel NFL | 153 | 717 | 150 | 100% | 81.7% | 0 | 0 |
| FanDuel NBA | 258 | 168 | 108 | 100% | 32.6% | 0 | 0 |
| FanDuel NHL | 184 | 693 | 48 | 100% | 54.9% | 0 | 0 |
| DraftKings NBA | 36 | 72 | 72 | 100% | 100% | 0 | 0 |
| DraftKings partial cache | 3 | 2 | 0 | 100% | 66.7% | 0 | **590** |
| Polymarket NBA | 4,760 | 7,568 | 7,568 | 82.3% | 38.1% | 843 | 0 |
| Polymarket NFL | 14,480 | 25,109 | 25,109 | 94.4% | 75.4% | 811 | 0 |

Polymarket rows are raw Gamma snapshots across all open events, including
already-finished games; they are not a clean current-slate count. Use `pmctl`
for a single-game view.

Full detail and interpretation: [REPORT.md](REPORT.md). Framework design:
[DESIGN.md](DESIGN.md).

## Alt markets: NFL far, NBA props now accessible

Mainlines normalize; alts do not, on these feeds. The taxonomy classifies them,
but the two books expose different periods and menus:

- FanDuel NFL league board: 18 full-game player-prop markets (alt passing,
  alt receiving, anytime TD), 46 futures and 14 specials.
- DraftKings NFL alt subcategories found: passing/receiving milestones for the
  **1st half**, anytime TD for the **2nd half/4th quarter**, tackles/assists
  O/U. DK’s full-game alt passing/receiving/TD set was not exposed by the
  sampled subcategories.
- Player overlap: passing 10 DK vs 6 FD, **0 common**; receiving 49 vs 9,
  **0 common**; anytime TD 96 vs 85, **24 common players but different period**
  (DK partial-game vs FD full game). So even where the player appears in both,
  the quote keys are not comparable.
- DK assists (37 markets) and tackles props have no counterpart on the FD NFL
  board.
- NBA props were initially missed because FanDuel’s league page omits them. The
  event-level MGA feed fixes that:
  ```text
  POST https://scan.on.sportsbook.fanduel.ca/api/sports/navigation/facet/v1.0/search
  facets: EVENT + MARKET, filter: eventIds
  → attachments.sportsBookMarkets
  POST https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices
  → prices
  ```
  For Pistons @ 76ers this returned **268 markets / 1,521 runners**, including
  10 points, 10 rebounds, 7 assists and 7 threes full-game props, plus 69
  milestone, 7 quarter and 2 alternate-ladder markets. DK has the same 34
  full-game prop markets and the same 10 players. Matching them gives **68
  common player/stat/outcomes, 58 exact-line (85.3%)**, 10 line differences and
  a median fair-probability gap of **1.38%** (max 5.79%). This is close, but
  noisier than mainlines.

Net: NFL alt identity in the sampled sets is 0 (period/inventory problem). NBA
full-game props are available and close; NBA milestone/quarter/ladder markets
still need a DK event-level counterpart before they can be measured.

## Unified DraftKings main + alt

`dk-discover` writes a deduplicated board containing mainlines, alternate
spreads/totals and automatically selected prop subcategories:

```powershell
.\go\oddsfeed.exe dk-discover -sport nba -out out/dk-nba-discover
.\go\oddsfeed.exe dk-discover -sport nfl -out out/dk-nfl-discover
.\normalizer\normctl.exe -dk-nba out/dk-nba-discover/board.json \
  -dk-partial out/dk-nfl-discover/board.json
```

The normalizer sees one DK board with:

- NBA: 12 events / 297 markets / 1,534 comparable quotes; 7 auto-selected prop
  subcategories.
- NFL: 12 events / 395 markets / 1,586 comparable quotes; 26 auto-selected prop
  subcategories.
- 100% classified; mainline/alternate markets are high-confidence, player props
  use 0.85 confidence. Alternate spread/total markets carry `Alt=true`.

Props are selected from DraftKings’ category catalog:

- NBA: Player Points/Rebounds/Assists/Threes/Defense.
- NFL: Passing/Receiving/Rushing/Defensive/TD Scorers.

`-props` overrides auto-selection; `-no-props` disables it.

This removes the ad hoc “combine DK files with Node” step. The main snapshot is
loaded first so its market metadata wins; duplicate market/selection IDs are
dropped by `MergeData`.

## FanDuel event-level

`fd-event` writes a board-shaped `board.json` from FanDuel's event page + MGA
inventory + SMP prices. The normalizer parses it with the same `FD` adapter:

```powershell
.\go\oddsfeed.exe fd-event -event 36229335 -mode both -prices -out out/fd-event-lakers
.\normalizer\normctl.exe -fd-event out/fd-event-lakers/board.json -fd-event-sport nba
```

The adapter now handles:

- `ALTERNATE_HANDICAP` / `ALTERNATE_HANDICAPS` (line parsed from `runnerName`
  because `handicap` is 0);
- `ALTERNATE_TOTAL` / `ALTERNATE_TOTAL_POINTS`;
- period alt types such as `3RD_QUARTER_ALTERNATE_HANDICAP`;
- MGA player types such as `PLAYER_F_TOTAL_POINTS`, `PLAYER_F_TOTAL_REBOUNDS`,
  `PLAYER_F_TOTAL_ASSISTS`, `PLAYER_F_TOTAL_MADE_3_POINT_FIELD_GOALS`,
  `PLAYER_F_TOTAL_STEALS`, `PLAYER_F_TOTAL_BLOCKS`;
- `TO_RECORD_*` / `TO_SCORE_*` as non-comparable player milestones.

Measured Lakers–Jazz event board: 205 markets, 694 quotes, 89.3% classified,
22 unknown, 0 dangling. High-confidence share is only 4.4% because most of the
event is props/milestones; the mainline and alternate spread/total markets are
high-confidence.

## All-game NBA comparison

`fd-events` expands the FD event board to every current NBA game:

```powershell
.\go\oddsfeed.exe dk-discover -sport nba -out out/all-nba/dk
.\go\oddsfeed.exe fd-events -sport nba -out out/all-nba/fd
.\go\oddsfeed.exe pm-discover -sport nba -out out/all-nba/pm.json
.\normalizer\normctl.exe -dk-nba out/all-nba/dk/board.json \
  -fd-nba out/all-nba/fd/board.json
.\normalizer\pmctl.exe -pm out/all-nba/pm.json -sport nba \
  -dk out/all-nba/dk/board.json -fd out/all-nba/fd/board.json -max-skew 2m
```

Observed 2026-10-25:

- 7 shared NBA events.
- DK: 297 markets / 1,534 comparable quotes.
- FD: 2,073 markets / 9,103 quotes (3,067 comparable).
- DK/FD player props: **384 exact-line quote sides** matched; 15 same price,
  369 different. FD adds 724 prop quote sides, but its defensive prop inventory
  is narrower.
- PM/FD: 1,200 exact-line matches, 792 fair-probability comparisons including
  **370 player-prop** quote sides.
- PM/DK: 900 exact-line matches, 674 fair comparisons including **332
  player-prop** sides; props median fair gap 8.40%.

Why DK is not perfectly aligned:

- FD lacks some of DK’s defensive props (steals/blocks/steals+blocks), accounting
  for 38 unmatched DK identities.
- FD has no same player/stat for 8 more DK assists/threes identities.
- 56 DK/FD prop identities have a one-point line difference.
- PM is missing six DK players, and only 332 of the 432 DK/PM identity matches
  have the DK line in PM’s ladder.

DK/FD exact-line prop coverage is 384/486 = **79.0%**; DK/PM is 332/486 =
**68.3%**. All 64 DK players appear in FD; PM has 58 of 64.

The all-game de-vig result is less clean than the one-game sample because many
FD alt/prop markets have no DK counterpart. Use the family-level numbers rather
than the aggregate identity percentage, which is dominated by unmatched FD
inventory.

## Polymarket

Polymarket is a prediction market: each Gamma market is binary and prices are
probabilities in `0..1`. The normalizer maps each outcome token to a canonical
quote; spreads carry the named team's signed line and the opposite side gets the
negated line. `pmctl` is the standalone comparison tool:

```powershell
cd normalizer
go build -o pmctl.exe ./cmd/pmctl
cd ..

# Gamma events are fetched by the Go feed client:
.\go\oddsfeed.exe pm-discover -sport nba -limit 100 -out out/pm-nba-events.json
.\go\oddsfeed.exe pm-discover -sport nfl -limit 100 -out out/pm-nfl-events.json

# Compare one PM game with a DK board (combined main + alt snapshot).
.\normalizer\pmctl.exe -pm out/pm-nba-lakers-jazz.json -sport nba -dk out/dk-nba-combined.json
```

One-game results on 2026-10-25:

| Game | PM markets | PM quotes | PM main / alt | Identity vs DK | Exact line vs DK |
|---|---:|---:|---:|---:|---:|
| Lakers–Jazz | 472 | 926 | 6 / 920 | 6/6 | 90 |
| Packers–Lions (+ props event) | 544 | 892 | 8 / 884 | 6/6 | 74 |

The 6 identities are moneyline, spread and total, both sides. Exact-line matches
cover the overlapping ladder rungs. PM has 283 NBA player-prop quotes and 211
NFL player-prop quotes in those samples, with names/lines normalized, but PM
probabilities must be de-vigged into fair probabilities before comparing them
with sportsbook American prices.

`pmctl` writes `out/pm/report.json` and optional quote dumps. The PM event
identity merges Gamma's separate game, props and exact-margin events that share
the same teams/start.

## De-vig and synchronized capture

Raw sportsbook prices include vig; PM prices are probabilities. `pmctl` now
de-vigs each two-outcome market at the same canonical line before comparing:

```powershell
# One command captures DK, FD and PM concurrently.
powershell -NoProfile -ExecutionPolicy Bypass -File .\capture-tri.ps1 `
  -Sport nba -PMEvent 1215089 -FDEvent 36229335 -Out out/sync-nba

# Or compare existing files with a capture-time guard.
.\normalizer\pmctl.exe -pm out/sync-nba/pm.json -sport nba `
  -dk out/sync-nba/dk/board.json -fd out/sync-nba/fd/board.json `
  -max-skew 2m -out out/sync-nba/report
```

`capture-tri.ps1` runs:

- `dk-discover` for the unified DK main+alt board;
- `fd-event` for the FD event board + SMP prices;
- `pm-discover -event` for the PM event.

`pmctl` reports the capture skew and refuses to call the result synchronized if
it exceeds `-max-skew`.

Synchronized NBA run (Lakers–Jazz, 22:40:56Z, 0.25s skew):

| Comparison | Common exact lines | Median fair gap | Max |
|---|---:|---:|---:|
| PM vs DK | 24 | **2.26%** | 6.58% |
| PM vs FD | 64 | **6.35%** | 18.21% |

By family, PM-vs-DK moneyline 1.42%, spread 1.07%, total 6.41%; PM-vs-FD
moneyline 0.65%, spread 1.43%, total 8.05%, props 6.35%.

The large total gap is a stale PM line (still 237.5 while the live books were
260.5/261.5), not a de-vig failure. The old raw comparison was ~13%; de-vig
removes most of that margin. De-vig does not fix stale lines or different game
states.

## Tests

```powershell
cd normalizer
go test ./...
```

Tests cover key semantics (line is part of the quote key, not the identity key),
period/name parsers, team aliases, event time tolerance, a synthetic DK/FD pair
with identical quote keys, and alt quotes being normalized but non-comparable.

## Current limitations

- Cross-book price comparison is limited to exact same-line quotes; line-grid
  alignment/consensus is not implemented.
- Player subjects are name slugs. A player alias/roster registry is required for
  cross-book prop matching and for player names that change between books.
- Futures are classified but not comparable yet (season/competition scope and
  settlement rules need a registry).
- Period markets are parsed from names but the captured league boards mostly
  expose full-game markets; period-specific fixtures are still needed.
- NBA/NHL team identity uses a mascot fallback, not a full registry; NBA and NHL
  boards were validated, but a registry is still the production answer.
- **NBA next week is not posted yet.** Both league boards contained only the
  current slate (plus DK Christmas games); FanDuel’s competition search returned
  only “NBA” and “NBA Futures”. The event-level endpoints work once games exist,
  but there is no date-window route to force next week.
- FanDuel NBA props are **not** on the league page; they come from the MGA
  event-market feed plus SMP prices. That feed is captured for Pistons @ 76ers
  and compared in [REPORT.md](REPORT.md). The event-level payload is now handled
  by the existing Go FD adapter, as described above. Broader player aliases and settlement validation remain
  limitations.
- Specials are intentionally not comparable.
