# Design: a general cross-book normalization framework

## 1. The core decision: normalize to atomic quotes

A book payload is not a market model; it is a presentation of one. The stable
join unit across 2 or 100 books is **one side of one market at one line**:

```text
Quote = Sport + EventScope + Family + Period + Subject + Stat + Comparator + Line + Outcome
```

Example keys produced by the prototype:

```text
nfl|event|nfl|2026-10-25|nfl-giants@nfl-texans|spread|full|event||none|-10|nfl-texans
nfl|event|nfl|2026-10-25|nfl-49ers@nfl-falcons|player_prop|full|player:brock-purdy|passing_yards|atleast|150|yes
nba|comp|nba|nba-futures|futures|season|team:nba-los-angeles-clippers|wins|ou|27.5|over
nhl|event|nhl|2026-10-26|nhl-minnesota-wild@nhl-vancouver-canucks|player_prop|full|player:kirill-kaprizov|first_goal|yesno||yes
```

Why atomic quotes instead of markets as the join unit:

- Books group outcomes differently (a player list, a two-way market, a ladder).
- A quote can be compared without assuming the whole market is equivalent.
- Missing/extra outcomes do not poison the whole market.
- Alt lines are naturally distinct keys (`line` is part of the key) but share an
  **identity key** with the same line removed.

`IdentityKey` answers "is this the same market and outcome?" (`spread | Texans`
regardless of 10 vs 10.5). `QuoteKey` adds the line and is the only key used for
price comparison.

## 2. Canonical entities

### Event

```text
sport, league, start (UTC), away team, home team, canonical event key
```

`EventKey` is `event|sport|YYYY-MM-DD|teamA@teamB` with team IDs sorted. Sorting
removes home/away and neutral-site convention differences; outcomes use team IDs,
not "home"/"away". Matching then verifies a time tolerance (15 minutes in the
prototype; median observed skew was 60 s, max 240 s).

### Team

A registry entry: canonical ID, display name, abbreviation, aliases, league.
NFL works with mascot-based aliases because mascots are unique:

```text
"TB Buccaneers" / "Tampa Bay Buccaneers" / "Buccaneers" -> nfl-buccaneers
"LA Rams" / "Los Angeles Rams"                          -> nfl-rams
"SF 49ers" / "San Francisco 49ers"                      -> nfl-49ers
```

For 100 books this registry is a database with an alias queue, not code.

### Quote

Carries the canonical facets, price (American and decimal), status, in-play,
source rule/confidence and provenance (book IDs). `Comparable` is a separate
boolean: a quote can be understood and stored without being safe to compare.

## 3. Taxonomy: family, period, subject, stat, comparator, line

| Facet | Examples |
|---|---|
| Family | moneyline, spread, total, team_total, player_prop, team_prop, futures, special, unknown |
| Period | full, h1/h2, q1–q4, p1–p3, inning, set, season |
| Subject | event, `team:<id>`, `player:<id>` |
| Stat | points, goals, runs, passing_yards, receiving_yards, anytime_touchdown, first_goal, wins, seed |
| Comparator | none, over/under, exact, at-least, yes/no |
| Line | signed spread, total/prop line, ladder step, season wins |
| Alt | true for alt lines/ladders; still keyed by line |

Book/sport **rule packs** translate raw market types and names:

- exact type mapping (`MONEY_LINE` → moneyline);
- regex/name extraction (`Brock Purdy 150+ Yards` → player + 150 + at-least);
- heuristics for families (`*_OF_THE_YEAR` → futures), with low confidence;
- unknown fallback (no comparison, counted in metrics).

`Classified` and `high-confidence` are different metrics. A low-confidence
heuristic can group a market for reporting but must not make it comparable.

## 4. Why the raw data forces this

From the captures:

- DraftKings main markets are structured: `points` on the selection, `VenueRole`
  on participants.
- DraftKings props expose `marketIdentifier` with a player parameter, but partial
  caches can contain selections without `marketId` (590/592 in the captured
  partial cache). Those are **dangling**, not guessable.
- FanDuel main markets are typed but FanDuel props encode semantics in names:
  `Michael Penix Jr. - Alt Passing Yds`, runner `Michael Penix Jr. 150+ Yards`.
- FanDuel futures encode subject and line in names/runners:
  `Arizona Cardinals - Regular Season Wins 2026-27`, runner
  `Arizona Cardinals Over 3.5 Wins`.
- `marketType` is not sufficient for sport semantics: NHL `TOTAL_POINTS_(OVER/UNDER)`
  is actually **goals**; NBA is points; MLB is runs. The sport registry supplies
  the unit.
- Periods are usually not structured; parse from names, but phrase-based rules
  are required so `1st Seed` and `First Touchdown` are not mistaken for periods.
- Unicode/minus and mojibake exist (`−166`, `Presidentsâ€™ Trophy`); text
  normalization is part of the adapter, not an afterthought.

## 5. Comparability model

Three levels:

1. **Same-book normalized** — every quote can be stored and displayed in-book.
2. **Cross-book exact** — same identity key **and** same line, period, comparator,
   settlement semantics. Only these can produce a best price.
3. **Cross-book aligned** — different lines mapped by a valuation policy
   (half-point/hook adjustment, alt ladder interpolation). Requires an explicit
   model and a confidence; never silently treats 10 as 10.5.

`Comparable=true` means level 2. The prototype enforces this; level 3 is a later
product decision.

Settlement rules that must eventually participate in the key/comparability:
push/void, overtime, dead heat, player participation, stat corrections, futures
season/competition, and period-specific rules. Until then, props/futures/specials
are normalized but `Comparable=false`.

## 6. Measurement framework

Definitions used by the prototype:

- **Event overlap** — matched events / union of events. Report this separately
  from normalization quality: two books can have excellent matching on a small
  shared slate, or poor overlap because one book includes next week/Christmas
  games and the other includes different competitions.
- **Conditional metrics** — all quote/identity/line metrics recomputed on events
  present in both books. This is the normalization-quality number; the
  unconditional number mixes slate differences with parser quality. The NBA
  capture is the example: 7/18 shared events, but 100% identity and 81% exact
  line on the 7 shared games.
- **Quote identity coverage** — identity keys present in both books / identity-key
  union. Ignores line.
- **Exact-line coverage** — quote keys present in both / union. This is the
  directly price-comparable set. Conditional exact-line coverage is exact keys /
  max(shared-event quotes A, shared-event quotes B).
- **Line mismatch rate** — identity keys present in both where no exact line
  matches. The 26/150 NFL and 8/42 shared-NBA misses are real line differences,
  not identity failures.
- **Alt coverage** — separately measure inventory (markets/periods each book
  exposes), subject overlap (common players/teams), period overlap, then exact
  key overlap. A non-zero player overlap with zero period overlap means the
  books are not comparable yet, not that the normalizer failed.
- **Price dispersion** — on exact-line matches: same price count, median absolute
  American difference, optionally implied-probability dispersion. Do **not**
  treat "prices differ" as a normalization error; different books have different
  prices.
- **Taxonomy coverage** — markets with a family / all markets; separately
  high-confidence coverage and unknown-type list.
- **Data quality** — dangling selections, parse errors, missing event context,
  duplicate IDs, stale quotes, suspended share.
- **Operational** — REST/WS reconciliation, reconnect/resync counts, quote age,
  ingest latency, per-book error budgets.

At scale, avoid pairwise comparisons. Group quotes by key and compute:
`booksQuoting`, `bestPrice`, `worstPrice`, `dispersion`, `staleBooks`. Pairwise
reports are only for diagnostics.

## 7. Architecture for 10–100 books

```text
raw capture (immutable)
   -> book adapter (transport, JSON schema, retries)
   -> normalizer worker (taxonomy rule pack, identity lookup)
   -> canonical event/market/quote tables + raw audit link
   -> quality metrics / unknown queue / alerts
   -> quote store -> best-price & display APIs
```

Storage:

- `raw_payloads` — immutable body + headers + received time (already done by the
  soak harness).
- `events` — canonical event, start, teams, competition; book event mappings.
- `markets` — book market mapping to canonical facets; rule/version/confidence.
- `quotes` — time-series by canonical quote key, book, price, status, timestamps.
- `team_aliases`, `player_aliases`, `competition_registry`, `rule_packs`.

Scaling properties:

- Per-book workers; no shared mutable market state.
- Key-based joins at query time; a quote key maps to a book set, not N² pairs.
- Partition by sport/date/event; quotes are append-only with idempotency keys.
- Version every rule pack and canonical schema; replay raw payloads after a rule
  change and measure the delta before promoting it.
- Unknown/low-confidence queue with examples; a new book is onboarded by adding a
  rule pack and aliases, not by changing the core model.

## 8. Alt markets and different sports

- **Alt lines/ladders** — same family/stat/subject/period, different line; keep
  each line as a quote and group by line identity for display. Never compare
  across lines without a valuation policy.
- **Player props** — need a player registry (name aliases, team, position,
  external IDs). Subject is `player:<id>`; stat is a sport-level enum; over/under
  vs "X+" needs a comparator/line transform.
- **Team props** — subject is a team; period season vs game matters.
- **Futures** — no event; scope is competition/season; subject is team/player;
  settlement and season must be in the key before cross-book comparison.
- **Specials** — cross-book semantics are frequently not equivalent; classify,
  store, but do not compare without a manual rule.
- **Sports** — a sport registry supplies periods, stat units, scoring, overtime
  and settlement rules. The taxonomy core is sport-agnostic; only the registry
  and rule packs are sport-specific.

## 9. Roadmap (one thing at a time)

1. **NFL + NBA main across DK/FD** — done: NFL 100% event/identity, 82.7% exact
   line; NBA 100% identity and 81% exact line on the 7 shared events (the low
   aggregate is a slate/board difference). Exact-line and conditional metrics are
   reported separately.
2. **Alt taxonomy** — done as a reference: NFL props/futures, NHL player/team
   props, NBA futures. Alt *inventory* is asymmetric: DK exposes 1st-half/quarter
   props where FD exposes full-game props, so exact cross-book alt identity is 0
   in the sampled sets.
3. **Player/team registries** — next: cross-book player identity and team aliases
   for NBA/NHL/MLB; manual override queue.
4. **Period/settlement alignment** — next for alts: fetch the same period from
   both books and define push/void, overtime and futures semantics.
5. **Line-grid policy** — half-point/hook alignment, alt ladder grouping, and a
   clear comparability class for "same market, different line".
6. **10 books** — onboard via rule packs, measure coverage and unknown rate per
   book; add mapping tables.
7. **100 books** — partition, key-based joins, quote store, per-book SLOs,
   replay/backfill, canary rule rollout.

## 10. Non-goals for the prototype

- Not a production collector or storage service.
- Not claiming every NBA/NHL future is semantically mapped.
- Not comparing props/futures/specials across books yet.
- Not solving line movement/valuation; exact-line comparison only.
