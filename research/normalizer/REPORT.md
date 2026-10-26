# Normalization measurement report

Generated from the reference implementation on 2026-10-25. Inputs are saved
captures from the one-hour soak plus saved NBA boards; replay needs no live calls.

## Inputs

| Fixture | Files | Contents |
|---|---|---|
| NFL pregame, exact same timestamp | `out/soak-1h/dk/rest/snapshot-0071-20261025T165528Z.json`, `out/soak-1h/fd/rest/discovery-0008-20261025T165528Z.json` | 25 games, 75 main markets per book |
| NFL live, exact same timestamp | `out/soak-1h/dk/rest/snapshot-0111-20261025T171528Z.json`, `out/soak-1h/fd/rest/discovery-0012-20261025T171528Z.json` | 12 games, 36 main markets per book |
| NBA mainlines | `out/norm/dk-nba.json`, `out/norm/fd-nba-now.json` | DK 12 events, FD 18 events; 7 shared |
| FanDuel NFL alt/futures | `out/soak-1h/fd/rest/discovery-0008-20261025T165528Z.json` | 153 markets: main, props, futures, specials |
| DraftKings NFL alt subcategories | `out/norm/dk-sub-18493.json`, `-18495`, `-18506`, `-18508`, `-18541`, `-18537..42` | 1st-half passing/receiving, partial-game TD, tackles/assists |
| FanDuel NBA | `out/norm/fd-nba-now.json` | 258 markets, 18 main games, futures/awards |
| FanDuel NHL | `out/norm/fd-nhl.json` | 184 markets, 10 events |
| Polymarket NBA | `out/pm-nba-events.json` | 100 Gamma events, 4,760 markets |
| Polymarket NFL | `out/pm-nfl-events.json` | 62 Gamma events, 14,480 markets |
| Polymarket one-game samples | `out/pm-nba-lakers-jazz.json`, `out/pm-nfl-packers-lions-full.json` | Lakers–Jazz, Packers–Lions + props |
| Combined DK alt boards | `out/dk-nba-combined.json`, `out/dk-nfl-combined.json` | DK main + alternate spreads/totals |
| DraftKings partial cache | `captures/dk-partial-cache.json` | broad partial cache; test of dangling data |

## Headline results

| Check | NFL pregame | NFL live | NBA shared events |
|---|---:|---:|---:|
| Events | DK 25 / FD 25 | DK 12 / FD 12 | DK 12 / FD 18, **7 shared** |
| Event overlap | 100% | 100% | 30.4% union / 38.9% max |
| Median start-time skew | 60 s | 60 s | 60 s |
| Comparable quotes | DK 150 / FD 150 | DK 72 / FD 72 | DK 42 / FD 42 |
| Quote identity coverage (line ignored) | **100%** | **100%** | **100%** |
| Exact-line quote matches | **124/150 (82.7%)** | **58/72 (80.6%)** | **34/42 (81.0%)** |
| Line mismatches (same identity, different line) | 26 | 14 | 8 |
| Same price on exact-line matches | 28/124 (22.6%) | 12/58 (20.7%) | 6/34 (17.6%) |
| Median absolute American price difference | 6 | 10 | 5 |

The important result is the split between identity and line. On real data:

- Every shared event and every main-market outcome found an equivalent in the
  other book (100% identity coverage).
- 82.7% of pregame NFL quotes, 80.6% of live NFL quotes and 81.0% of shared-event
  NBA quotes were on the **same line** and are directly price-comparable.
- The remaining 17–19% were the same market and outcome on a different line,
  typically a half point:

```text
nfl|...|giants@texans|spread|full|event||none||nfl-giants  dk=10.5 fd=9.5
nfl|...|bengals@titans|total|full|event|points|ou||under    dk=46.5 fd=45.5
nfl|...|bills@ravens|total|full|event|points|ou||under      dk=52.5 fd=53.5
```

This is not a normalization bug. It is the reason a comparison product needs a
line-grid policy: 10.5 and 9.5 are different offers. The framework keeps them
distinct and only compares prices on identical lines.

### Why NBA shows 7/18 events

The NBA board sets are genuinely different, not a matcher failure:

- DraftKings' 12 events included the 7 games played that evening plus **5
  Christmas Day games**.
- FanDuel's 18 events included the same 7 games, **10 suffix-named games**
  (`Los Angeles Lakers (BREAKER)`, `Boston Celtics (HOLLOW)`, etc.) and one late
  game not in the DK board.
- Conditional on the 7 shared games, all 42 quotes per book have an identity
  match (100%) and 34/42 are on the same line (81.0%). That is the normalization
  quality; the 30% aggregate is a slate/board difference.

One of the 8 NBA line mismatches is a real disagreement rather than a half-point
shift: DraftKings had Detroit −1.5 while FanDuel had Detroit +1.5, and their
moneylines flipped favorite too. The framework correctly refuses to call those
the same price.

## What causes the differences

Decomposition of the matched mainline quotes:

| | NFL pregame | NFL live | NBA shared |
|---|---:|---:|---:|
| Exact-line share | 82.7% | 80.6% | 81.0% |
| Line mismatch shape | 6 half, 18 one-point, 1 flip (2 sides) | 14 one-point | 4 one-point, 2 two-point, 1 flip (2 sides) |
| Median fair-probability gap on matched line | 0.87% | 1.03% | 1.08% |
| Within 2% fair probability | 122/124 (98.4%) | 48/58 (82.8%) | 34/34 (100%) |
| Maximum fair-probability gap | 2.47% | 2.96% | 1.94% |
| Median overround DK / FD | 4.71% / 4.71% | 6.71% / 5.91% | 4.71% / 4.71% |
| Outcomes where each book has best price | 58 DK / 58 FD / 34 same | 28 DK / 32 FD / 12 same | 18 DK / 18 FD / 6 same |

Conclusions:

1. **Same-line price differences are mostly margin, not “different values.”**
   The median implied-probability gap is about 1%, and 98%/83%/100% are within
   2%. Pregame NFL and NBA have identical median overrounds (4.71%), and the
   best-price outcomes are symmetric (58/58, 18/18). The books disagree on juice
   and rounding, not on a completely different probability.
2. **American odds exaggerate the visual gap.** `+100` vs `−106` is a 206-point
   American difference but only ~1.5% implied probability; the same applies to
   `−550` vs `−620`. Comparing raw American numbers overstates disagreement.
3. **Live NFL has a wider DK margin.** DK’s live overround was 6.71% vs FD’s
   5.91%, and FD had the best price on more outcomes (32 vs 28). That is a
   genuine live-margin difference, but still only ~1% median fair probability.
4. **A minority are real probability disagreements.** Examples:
   Commanders/Eagles moneyline (2.47%), Bengals/Ravens live moneyline (2.96%),
   Thunder/Clippers moneyline (1.94%). These are still under 3%, not a different
   market.
5. **Line is the larger structural cause.** 17–19% of mainline quotes are on a
   different line; those cannot be compared at all as the same offer. NFL
   pregame had 6 half-point, 18 full-point and one favorite flip (two sides);
   NBA had 4 one-point, 2 two-point and one favorite flip (two sides).
   - The NFL Jets/Raiders flip is **internally inconsistent inside DK**: DK’s
     moneyline has the Jets as the favorite (`-112`), while DK’s spread has the
     Raiders at `-1.5`. FD is consistent (Jets favorite, Jets `-1.5`). This is
     likely a stale/inconsistent DK line, not a genuine two-book disagreement.
   - The NBA Pistons/76ers flip is internally consistent on both books (DK
     favorite Detroit, FD favorite Philadelphia). That is a real market
     disagreement, not a sign bug.
   A production normalizer should add an internal-consistency gate (moneyline
   favorite vs spread sign) and quarantine the inconsistent quote instead of
   presenting it as a competing price.
6. **The initial NFL alt sample has no comparable overlap.** Those sampled
   feeds expose different periods/players. This does not apply to the later NBA
   event-level comparisons below, which include matched props and alternate lines.

Priority order for the product: fix line/slate/period alignment first, then model
margin/vig, then treat the small number of genuine probability gaps as real
market disagreement.

## NBA next-week and event-level availability

The NBA mainline numbers above are **pre-game for the seven shared games on the
current slate**, not a next-week schedule:

- DK’s NBA league board had 12 events: 7 games on 2026-10-25/26 plus 5 Christmas
  games on 2026-12-25/26.
- FanDuel’s league board had 18 events: the same 7 games, 10 suffix-named games
  on 2026-10-25 and one late game on 2026-10-26.
- Neither board contained an Oct 26–Nov 1 slate. FanDuel’s competition search
  returned only “NBA” and “NBA Futures”; next week’s games are not posted yet.

The earlier report said FanDuel’s NBA props were unavailable. That was wrong: the
league page omits them, but the **event-level MGA market feed** exposes them.

### The missing FanDuel endpoint

FanDuel’s event page uses the MGA facet-search service:

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

The response’s `attachments.sportsBookMarkets` contains every market for the
event, with market IDs, types, runner/selection IDs, handicaps and names. Prices
are then fetched from SMP:

```text
POST https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0
{"marketIds": ["801.193900371", "..."]}
```

For Pistons @ 76ers (`eventId 36229340`), this returned **268 markets / 1,521
runners**:

| Market class | Count |
|---|---:|
| Full-game player points O/U | 10 |
| Full-game player rebounds O/U | 10 |
| Full-game player assists O/U | 7 |
| Full-game player threes O/U | 7 |
| Player quarter props | 7 |
| Milestone props (`TO_RECORD_*`, `TO_SCORE_*`) | 69 |
| Alternate total/handicap ladders | 2 markets (many runners) |
| Other game/quarter/half markets | 156 |

### NBA full-game prop comparison

DK exposes the same full-game prop families for the same game:
10 points, 10 rebounds, 7 assists and 7 threes. The player sets match exactly:
**10/10 common players**, with 68 common player/stat/outcome quotes.

| Metric | Result |
|---|---:|
| Common player/stat/outcomes | 68 |
| Same player set | 10/10 |
| Exact line | 58/68 (85.3%) |
| Line differences | 10 outcomes across 5 player/stat pairs |
| Same American price | 3/58 (5.2%) |
| Median fair-probability gap | **1.38%** |
| Maximum fair gap | 5.79% |
| Within 2% fair probability | 38/58 (65.5%) |

By stat, the median fair-probability gaps were: points 0.49%, threes 1.25%,
assists 2.20%, rebounds 2.26%. The largest disagreement was Cade Cunningham
assists at 9.5 (FD +106 vs DK −119, 5.79% fair probability).

Line differences were:

- LeBron James rebounds: FD 7.5, DK 6.5
- LeBron James assists: FD 6.5, DK 7.5
- VJ Edgecombe rebounds: FD 5.5, DK 6.5
- Paul Reed points: FD 14.5, DK 15.5
- Adem Bona points: FD 5.5, DK 6.5

So NBA full-game props are **not** a missing-data case. They are available,
match at the player level, and are 85% line-comparable; the main issue is price
disagreement, which is larger than in mainlines.


## Taxonomy coverage

| Book / sport | Markets | Quotes | Comparable | Classified | High confidence | Unknown | Dangling |
|---|---:|---:|---:|---:|---:|---:|---:|
| FanDuel NFL | 153 | 717 | 150 | 100% | 81.7% | 0 | 0 |
| FanDuel NBA | 258 | 168 | 108 | 100% | 32.6% | 0 | 0 |
| FanDuel NHL | 184 | 693 | 48 | 100% | 54.9% | 0 | 0 |
| DraftKings NBA | 36 | 72 | 72 | 100% | 100% | 0 | 0 |
| DraftKings NFL partial | 3 | 2 | 0 | 100% | 66.7% | 0 | **590** |

The FanDuel NBA taxonomy row is the **league board only** (54 main + 202
futures/awards), not the 268-market event-level feed measured below.

Family distribution (markets):

| Book / sport | main | props | futures | special | unknown |
|---|---:|---:|---:|---:|---:|
| FD NFL | 75 | 18 | 46 | 14 | 0 |
| FD NBA | 54 | 0 | 202 | 2 | 0 |
| FD NHL | 24 | 34 player + 63 team | 61 | 2 | 0 |
| DK NBA | 36 | 0 | 0 | 0 | 0 |

All sampled market types now get a family. High-confidence coverage is the honest
strictness metric: the remaining futures/awards/specials are classified with
low-confidence heuristic rules and are **not** cross-book comparable. The
unknown path remains in the code and is covered by a unit test for future books.

## Alt markets: NFL far, NBA props close

The taxonomy can represent alts. The result depends on the sport and the feed
level:

| Alt family | FanDuel | DraftKings | Overlap |
|---|---|---|---|
| NFL alt passing yards | 6 full-game markets, 42 quotes | 10 **1st-half** markets, 270 selections | 0 common players, 0 identical keys |
| NFL alt receiving yards | 9 full-game markets | 49 **1st-half** markets, 562 selections | 0 common players, 0 identical keys |
| NFL anytime TD | 3 full-game markets, 85 players | 4th-quarter + 2nd-half, 96 players | 24 common players, but **different period** → 0 comparable keys |
| NFL assists / tackles | 0 on the NFL board | 37 assists + 37 tackles + milestones | 0 |
| NBA full-game player props | 10 points, 10 rebounds, 7 assists, 7 threes | Same 34 markets / 10 players | **68 common outcomes; 58 exact-line; median 1.38%** |
| NBA milestones / quarter / alt ladders | 69 milestone + 7 quarter + 2 ladder markets | Not yet fetched at event level | FD inventory exists; DK counterpart open |

The alt normalization itself works:

- `Michael Penix Jr. - Alt Passing Yds` → `player_prop | passing_yards | atleast | 150..325 | yes`.
- `Anytime TD Scorer - 2nd Half` → `player_prop | anytime_touchdown | yesno | yes`, period `h2`.
- Season wins → `futures | season | team | wins | ou | 3.5 | over`.
- NHL: 497 player-prop quotes and 148 team-prop quotes normalized.
- NBA event props: player + stat + line + over/under, with 68 matched outcomes.
- Specials are classified and excluded from comparison.

The NFL gap is **inventory and period**, not parsing: exact cross-book alt
identity in the sampled NFL sets is 0. Even the 24 common anytime-TD players
cannot be compared because DK's markets are 2nd-half/4th-quarter and FD's are
full-game. The NBA full-game props are different: the player sets match exactly
and 85% are line-comparable, so the remaining issue is price disagreement
(median ~1.4%, tail to 5.8%) and the richer milestone/quarter markets still need
a DK event-level comparison.

## Unified DraftKings main + alt

`dk-discover` now merges DK mainlines with alternate spread/total subcategories
and automatically selected prop subcategories into one deduplicated board.
Results:

| Board | Events | Markets | Comparable quotes | Classified | High confidence | Alt |
|---|---:|---:|---:|---:|---:|---|
| DK NBA | 12 | 297 | 1,534 | 100% | 16.8% | spread/total alts marked |
| DK NFL | 12 | 395 | 1,586 | 100% | 11.6% | spread/total alts marked |

High-confidence is lower than before because the auto-discovered player props
use 0.85 confidence; mainlines and alternates are still high-confidence. The
props are selected from the DK category catalog (NBA Player
Points/Rebounds/Assists/Threes/Defense; NFL Passing/Receiving/Rushing/Defensive/
TD Scorers), not a hardcoded ID list.

This replaces the ad hoc combined-file scripts. The main snapshot is loaded
first; duplicate market/selection IDs are dropped. Alternate markets are
`Spread Alternate` / `Total Alternate` with `Alt=true`.

The current live PM-vs-unified-DK exact-line overlap is **38 quotes NBA / 68
quotes NFL**. Earlier ad hoc combined captures produced 90/74; the difference is
live line movement and a different capture time, not a merge regression.

## FanDuel event-level

FanDuel's league page is mainline-only. The new `fd-event` command merges:

- event-page `tab=popular` (priced alternate ladders and a subset of props);
- MGA `MARKET` facet (full event inventory);
- SMP prices by market/selection id.

It emits a board-shaped payload that the existing FD adapter parses.

Lakers–Jazz event board (2026-10-25):

| Metric | Result |
|---|---:|
| Markets | 205 |
| Quotes | 694 |
| Classified | 89.3% |
| High confidence | 4.4% |
| Unknown | 22 |
| Dangling | 0 |

The low high-confidence share is expected: most of the event is player props,
milestones and period markets. The mainline and alternate spread/total markets
are high-confidence. The adapter handles FanDuel's `handicap: 0` alternate
runners by parsing the signed line from `runnerName`, and supports
`ALTERNATE_HANDICAP(S)`, `ALTERNATE_TOTAL(_POINTS)`, quarter/half variants, and
MGA `PLAYER_*_TOTAL_*` player props.

This closes the FanDuel event-level discovery gap. The later all-game NBA
section below reports comparisons using the expanded
main+alt data. A robust player alias registry and settlement validation remain
follow-up work.

## Polymarket

Polymarket is now normalized through `normalizer/internal/adapters/pm.go`; the
Gamma client is `go/internal/pm`. It is a binary prediction market: each Gamma
market has two outcome tokens priced 0..1, and each spread/total line is its own
market.

Observed Gamma snapshots:

| Book | Events | Markets | Quotes | Comparable | Classified | High confidence | Unknown |
|---|---:|---:|---:|---:|---:|---:|---:|
| Polymarket NBA | 53 merged | 4,760 | 7,568 | 7,568 | 82.3% | 38.1% | 843 |
| Polymarket NFL | 43 merged | 14,480 | 25,109 | 25,109 | 94.4% | 75.4% | 811 |

The event count is lower than the raw Gamma event count because Gamma splits
game lines, props and exact-margin markets into separate events that share the
same teams and start; the adapter merges them under one event key.

One-game normalized overlap against a combined DK main + alternate board:

| Game | PM markets | PM quotes | PM main / alt | Identity vs DK | Exact line vs DK |
|---|---:|---:|---:|---:|---:|
| Lakers–Jazz | 472 | 926 | 6 / 920 | 6/6 | 90 |
| Packers–Lions (+ props) | 544 | 892 | 8 / 884 | 6/6 | 74 |

The six identities are moneyline, spread and total, both sides. The exact-line
matches are the overlapping ladder rungs. PM also normalized 283 NBA player-prop
quotes and 211 NFL player-prop quotes in those samples.

Three-way ladder interaction on the same fixtures:

| Fixture / ladder | DK | FD | PM | Three-way common |
|---|---:|---:|---:|---:|
| NBA Lakers spread rungs | 40 | 59 | 33 | **24** |
| NBA total rungs | 44 | 77 | 25 | **18** |
| NFL Packers spread rungs | 25 | 45 | 34 | **22** |
| NFL total rungs | 33 | 56 | 21 | **14** |

The earlier three-way table used a DK snapshot from 16:55Z (pregame) against
later FD/PM captures; that was not like-for-like. Current DK status for both
games is **STARTED**. Current synchronized mainlines:

- NFL (22:21Z): DK Packers +2.5 / 52.5; FD Packers +2.5 / 52.5; PM Lions −3.5 /
  52.5. ML: DK +163, FD +176, PM .375 for Packers.
- NBA (22:22Z): DK Lakers −7.5 / 260.5; FD Lakers −6.5 / 261.5; PM Lakers −6.5 /
  237.5. PM's total is still on the old 237.5 line while the sportsbooks have
  moved to 260.5/261.5; that is a stale/misaligned PM total, not a three-way
  price disagreement.

A valid three-way comparison requires synchronized timestamps and the same
period/state.

Important caveat: Polymarket prices are **probabilities**, not sportsbook
American odds. Main/alt identity and lines are comparable, but price comparison
requires de-vigging/fair-probability treatment and settlement-rule validation.

## Heuristic futures (non-comparable)

All captured award/futures types now get `family=futures` with low confidence:

- NFL: coach/rookie-of-the-year, draft pick, Super Bowl winner.
- NBA: season matchups, finals winner/loser, play-in, coach/rookie/sixth-man
  awards.
- NHL: Presidents' Trophy.

They are stored and grouped but never price-compared until competition/season
and settlement semantics are defined. The unknown path remains tested for future
books.

## Data-quality findings

- **DraftKings partial cache:** 590 of 592 selections had no `marketId` in the
  captured broad cache. They cannot be joined to a market safely. The adapter
  reports them as dangling and only normalizes the 2 that had context. This is a
  strong argument for resync on unknown IDs and for storing raw payloads.
- **Unicode:** DraftKings sends U+2212 minus in `displayOdds.american`
  (`−166`); it must be normalized before parsing.
- **Mojibake:** FanDuel's `Presidentsâ€™ Trophy` shows source text corruption;
  display names need a text-normalization layer independent of the canonical key.
- **Market type is not enough:** NHL totals are typed
  `TOTAL_POINTS_(OVER/UNDER)` but settle on goals; NBA totals are points; MLB is
  runs. The sport registry supplies the stat/unit.
- **Names carry semantics:** FanDuel subject/line/comparator are often only in
  `marketName`/`runnerName`; exact rules are required.

## All-game NBA prop comparison

The one-game comparison is now expanded to all seven shared NBA games.

| Board | Events | Markets | Comparable quotes |
|---|---:|---:|---:|
| DK auto | 12 | 297 | 1,534 |
| FD all events | 7 | 2,073 | 3,112 |
| PM all events | 53 merged | 4,760 | 7,549 |

DK/FD player-prop family:

| Metric | Result |
|---|---:|
| DK prop quote sides | 486 |
| FD prop quote sides | 724 |
| Exact-line prop quote sides matched | **384** |
| Same price | 15 |
| Different price | 369 |

Three-way exact-line/fair comparisons (all shared events):

| Comparison | Exact-line matches | Fair comparisons | Median fair gap | Max |
|---|---:|---:|---:|---:|
| PM vs DK | 900 | 674 | 2.63% | 29.62% |
| PM vs FD | 1,200 | 792 | 2.70% | 32.18% |

PM/FD fair comparisons include **370 player-prop quote sides** with a 6.35%
median gap. PM/DK includes **332 player props** with an 8.40% median gap.

### Why DK is not perfectly aligned

After adding FD steals/blocks support:

| Cause | DK/FD | DK/PM |
|---|---:|---:|
| DK prop quote sides | 486 | 486 |
| Identity match ignoring line | 440 | 432 |
| Same line | 384 | 332 |
| Identity missing | 46 | 54 |
| Same identity, different line | 56 | 100 |

Causes:

1. **FD inventory is still narrower for defense.** DK has 46 steals sides,
   22 blocks and 10 steals+blocks; FD’s event boards expose only a subset,
   and no steals+blocks O/U market. This accounts for 38 of the 46 remaining
   unmatched DK identities.
2. **Assists/threes inventory.** The other 8 unmatched DK identities are
   assists/threes for players FD does not list for that stat.
3. **FD line differences are small.** Every one of the 56 same-identity line
   mismatches is one point. DK/FD exact-line coverage is 384/486 = **79.0%**.
4. **PM has six DK players missing.** Ron Holland II, Anthony Black, Jase
   Richardson, Ja’Kobe Walter, Daniss Jenkins and Isaiah Joe are not in the PM
   prop set. That accounts for the 54 unmatched DK identities.
5. **PM line ladders differ.** Of the 432 DK/PM identity matches, only 332
   have the DK line somewhere in PM’s ladder. The other 100 are one or two
   points away. PM exact-line prop coverage is 332/486 = **68.3%**.
6. **Player names are not the main DK/FD problem.** All 64 DK players appear
   in FD; FD has 92 players. PM has 58 of the 64 DK players.

The aggregate percentage is low because FD exposes much more alt/prop
inventory than DK; use family/exact-line counts rather than the full FD board.

## De-vig and synchronized comparison

Added `normalizer/internal/price` and `capture-tri.ps1`.

- Each two-outcome group is converted to implied probabilities and normalized
  to a 100% fair probability.
- PM uses its probability/mid; DK/FD use American/decimal odds.
- Only exact-line matches are compared.
- Three-way/push groups are skipped until those states are modeled.
- `pmctl` records capture times and enforces `-max-skew`.

Synchronized NBA run (Lakers–Jazz, 22:40:56Z, **0.25 s skew**):

| Comparison | Common exact lines | Median fair gap | Max |
|---|---:|---:|---:|
| PM vs DK | 24 | **2.26%** | 6.58% |
| PM vs FD | 64 | **6.35%** | 18.21% |

By family:

| Comparison | Moneyline | Spread | Total | Props |
|---|---:|---:|---:|---:|
| PM vs DK | 1.42% | 1.07% | 6.41% | — |
| PM vs FD | 0.65% | 1.43% | 8.05% | 6.35% |

The raw comparison was ~13% median. De-vig removes most of that, confirming
that the raw gap was mostly margin. The remaining total gap is a stale PM total
(237.5 while live sportsbooks were 260.5/261.5), not a de-vig failure.

## Interpretation

1. **The atomic-quote model works on real data.** 100% event and identity
   coverage on every shared event: NFL pregame, NFL live and NBA. No hard-coded
   event IDs or book-specific market IDs are used in the join.
2. **Line differences dominate the remaining mainline gap.** The 17–19%
   exact-line miss is a market reality, not a parsing failure. A comparison app
   must decide whether to compare only exact lines or use a line-adjustment model.
3. **NBA's low aggregate is a board/slate issue.** DK had Christmas games; FD had
   suffix-named games. Conditional on the 7 shared games, identity is 100% and
   same-line is 81.0%, essentially identical to NFL.
4. **Alternate-market coverage depends on the sample.** The initial NFL
   alt sample has zero comparable overlap because periods and players differ.
   Later NBA event-level work matched 384 DK/FD player-prop quote sides at
   exact lines, alongside alternate-line comparisons. Player aliases and
   period/settlement validation still need broader coverage.
5. **Unknown and dangling metrics are the operating system for 10–100 books.**
   All sampled types are now classified, but low-confidence futures stay
   non-comparable. Dangling DK selections remain a concrete resync signal.
6. **Price equality is not a quality metric.** Cross-book prices differ
   legitimately; median absolute American differences were 5–10 points. Quality
   is identity/line/period/settlement correctness plus freshness.

## Recommended next steps

1. Player and team registries with alias/override queues to harden the existing
   prop comparisons and expand coverage beyond the sampled names.
2. A line-grid/comparability policy: exact line now; half-point/hook alignment as
   an explicit optional model.
3. Period and settlement rule packs before adding more books.
4. Futures competition/season registry.
5. Then onboard the next 8 books using the same adapter + rule-pack contract and
   track coverage/unknown/dangling per book.

Reproduce from `research/` with:

```powershell
cd normalizer
go build -o normctl.exe ./cmd/normctl
cd ..
.\normalizer\normctl.exe -dump
```

Machine-readable output: `out/norm/report.json`; generated tables:
`out/norm/REPORT.md`.
