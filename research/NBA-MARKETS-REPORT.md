# NBA main lines and alternate markets

Update: subsequent WebSocket/polling measurements are documented in [NBA-WEBSOCKET-COMPARISON.md](NBA-WEBSOCKET-COMPARISON.md). The transport limitations below describe the original discovery investigation.

Verified October 25, 2026 against the Ontario feeds. **Both DraftKings and FanDuel return priced NBA moneylines, main spreads/totals, and alternate spreads/totals.** All discovery requests described below returned HTTP 200. These are captured snapshots, not a claim of continuous availability. The canceled soak test was never started.

## Observed coverage

| Feed | Events | Markets | Priced selections/runners |
|---|---:|---:|---:|
| DraftKings main lines | 12 | 36 | 72 |
| DraftKings alternate spreads | 7 | 7 | 560 |
| DraftKings alternate totals | 7 | 7 | 616 |
| FanDuel main lines, filtered to NBA competition 10547864 | 7 | 21 | 42 |
| FanDuel alternate spreads, Lakers at Jazz sample | 1 | 1 | 118 |
| FanDuel alternate totals, Lakers at Jazz sample | 1 | 1 | 157 |

DK’s 12 main events include five Christmas fixtures; the DK alternate endpoints
returned only the seven current games. The FanDuel main board includes today’s
games and season futures; its alternate sample is today’s Lakers–Jazz game.
None of these are next-week games. FanDuel alternate coverage was verified for
one event, so its counts cannot be compared with DraftKings’ seven-event totals.
Player props and period markets are outside this report’s main-line/alternate
scope.

## DraftKings: three league-wide requests

Base: `https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1`

| Purpose | GET path |
|---|---|
| NBA main lines | `/leagues/42648` |
| Alternate spreads | `/leagues/42648/categories/487/subcategories/13202` |
| Alternate totals | `/leagues/42648/categories/487/subcategories/13201` |

NBA league ID is `42648`; Game Lines category is `487`; main Game subcategory is `4511`. Responses have separate `events`, `markets`, and `selections` arrays. Join selections to markets by `marketId`, and markets to events by `eventId`.

Main market types are Moneyline, Spread and Total. Alternates are `Spread Alternate` (`2_628`) and `Total Alternate` (`3_628`). Each alternate market contains a ladder of selections: **market ID alone is not a line identifier**. Read selection `points`, `outcomeType`, `participants`, `displayOdds`, and `trueOdds`.

For Lakers at Jazz (`34888793`), the alternate spread market `2_86923924` had 80 selections, and alternate total market `3_86923924` had 88. Lakers spread points ranged from -27.5 to +14.5; total points ranged from 215.5 to 259.5. These are discrete offered lines, not continuous ranges. In this capture the alternate ladders omitted the main -6.5 spread and 237.5 total, so fetch both main and alternate endpoints to build the full offering.

Requests succeeded using Node 24, system CA certificates, and the current Chrome user-agent from `dk-http.cjs`. Earlier controlled checks showed a user-agent-sensitive 403 difference; they did not establish TLS fingerprinting as the cause. This is an observed working request configuration, not a permanence guarantee.

## FanDuel: board discovery, then event tabs

Base: `https://sbapi.on.sportsbook.fanduel.ca/api`

1. GET `/content-managed-page?page=CUSTOM&customPageId=nba&includePrices=true&_ak=FhMFpcPWXMeyZxOx`.
2. From `attachments.markets`, retain `MONEY_LINE`, `MATCH_HANDICAP_(2-WAY)`, and `TOTAL_POINTS_(OVER/UNDER)` for main lines. The board also includes futures and simulated basketball. Require event competitionId 10547864; eventTypeId 7522 alone does not distinguish actual NBA from simulations.
3. For each relevant event, GET `/event-page?_ak=FhMFpcPWXMeyZxOx&eventId=EVENT_ID&tab=popular`.
4. Read actual `attachments.markets` entries of type `ALTERNATE_HANDICAPS` and `ALTERNATE_TOTAL_POINTS`.

The default event response advertised alternate types in layout cards but did not include the actual ladders. **The Popular tab returned the priced runners.** A card declaration with `attachmentsFullyLoaded:false` is not evidence that market data has been loaded.

Verified event: Lakers at Jazz, FanDuel ID `36229335` (same fixture as DraftKings `34888793`). Alternate market IDs were `801.193911720` for spread and `801.193911698` for total. All 275 alternate runners in this sample were ACTIVE and had odds.

### Price refresh also works for alternates

POST `https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0`

Headers: `Content-Type: application/json`, `X-Application: FhMFpcPWXMeyZxOx` (public frontend application value).

Body: `{"marketIds":["801.193911720","801.193911698"]}`; main market IDs can be included in the same request.

The verification request included three main markets plus both alternate markets. It returned HTTP 200, all five markets OPEN, and runner counts **2, 2, 118, 2, 157**, all priced. Therefore discovery does not need to be repeated for every price refresh. Keep the discovery mapping: price responses contain selection IDs and odds but omit the descriptive runner names needed to interpret alternate lines.

## Normalization issues that matter

- **FanDuel alternate line extraction:** all sampled alternate runners had `handicap: 0`. The actual line is in `runnerName`, e.g. `Los Angeles Lakers +24.5` or `Over 201.5`. Main lines use numeric `handicap`. Parse alternate names with validation and retain the raw name; do not normalize every alternate to zero.
- **DraftKings main/alternate classification:** alternate markets also have `main:true`. Use subcategory and market type to classify them, not that flag alone.
- **Precision:** DraftKings display decimal odds can be rounded (`1.90`) while `trueOdds` is `1.90909091`. FanDuel also distinguishes true odds and display odds. Preserve the precise value and raw payload. DraftKings American strings can contain a Unicode minus sign.
- **Stable source identity:** preserve book, event ID, market ID and selection ID as strings. FanDuel selection IDs can recur across different main markets; selection ID alone is insufficient.
- **Cross-book matching:** map team aliases such as `LA Lakers` and `Los Angeles Lakers`, then match fixture start time and home/away. Compare full-game moneyline/spread/total by outcome and exact signed line. Main/alternate is source metadata, not a different payoff when line and settlement rules match. Verify overtime/settlement rules before treating offers as equivalent.
- **Incomplete ladders:** do not assume every threshold has both sides. FanDuel's sample alternate total had an odd 157 runners. Preserve precisely what is offered and its status.

## Collection recommendation and limits

Bootstrap DraftKings with the three league snapshots. Bootstrap FanDuel with its NBA board and a Popular-tab request for each target fixture. Preserve discovery metadata, then apply price refreshes by source identity. Periodically rediscover markets and reconcile deletions, suspended selections, and changed main lines.

DraftKings snapshots include `subscriptionPartials` with subcategory-specific event/market filters suitable for its existing WebSocket adapter. FanDuel alternate market IDs are candidates for the existing market-price subscription. **This NBA investigation verified HTTP snapshots and FanDuel price refresh; it did not verify NBA alternate WebSocket updates or measure their latency.** Previous socket results from other sports do not prove NBA alternate coverage or speed. A follow-up transport check should require an actual alternate price change, not merely a subscription acknowledgment.

## Reproducible evidence

Raw responses and request/status/timestamp metadata are in `captures/nba/`:

- `dk-main.json`, `dk-alt-spread.json`, `dk-alt-total.json`
- `fd-main.json`, `fd-event.json`, `fd-popular.json`
- `fd-prices.json` and the corresponding `.meta.json` files

`nba-research.cjs` fetches the two NBA boards and exports a capture helper for further endpoints. `nba-price-check.cjs` refreshes the five sampled FanDuel markets from the saved Popular-tab response. Raw captures are locally retained and gitignored. No three-hour/background collection was launched.

## Independent verification (2026-10-25)

Re-fetched all three DK endpoints and FanDuel’s
`event-page?eventId=36229335&tab=popular` at approximately 20:30Z. All returned
HTTP 200 with the same counts as the saved captures.

Confirmed against the raw JSON:

- DK main: 12 events / 36 markets / 72 selections; types Moneyline, Spread,
  Total; subcategory 4511.
- DK alternate spread: 7 events / 7 markets / 560 selections; `Spread
  Alternate` (`2_628`), subcategory 13202, **`main:true`**.
- DK alternate total: 7 events / 7 markets / 616 selections; `Total Alternate`
  (`3_628`), subcategory 13201, **`main:true`**.
- Lakers–Jazz (`34888793`): alt spread market `2_86923924` has 80 selections;
  Lakers range −27.5 to +14.5; main −6.5 is absent. Alt total `3_86923924` has
  88 selections; range 215.5–259.5; main 237.5 is absent.
- FanDuel main filter: 17 events / 51 markets / 102 selections. The unfiltered
  board is 19 events / 255 markets, including futures.
- FanDuel default event response: 8 markets, no alternate ladder; 17
  `attachmentsFullyLoaded:false` cards, including the alternate cards. Popular
  tab: 80 markets, with **118** `ALTERNATE_HANDICAPS` and **157**
  `ALTERNATE_TOTAL_POINTS` runners, all ACTIVE and priced.
- FanDuel SMP refresh: five markets, all OPEN, runner counts 2, 2, 118, 2, 157;
  odds match the Popular tab exactly.
- All 275 alternate runners have `handicap: 0`; the line is in `runnerName`.
- Selection IDs do recur across markets. For example, Popular selection
  `237484` appears in 1st-half handicap, main handicap, moneyline and 1st-quarter
  winner. The stable key is **market ID + selection ID**, not selection ID alone.
- DK precision matters: 277 of 560 alternate-spread selections have a rounded
  display decimal different from `trueOdds` (e.g. `1.05` vs `1.05208334`).

Corrections/nuances to the main report:

1. “Future fixtures” applies to DK’s **main** board (five Christmas games). The
   DK alternate endpoints returned only the seven current games, and the
   FanDuel alternate sample is a current game.
2. FanDuel’s alternate ladders **include the main line** (−6.5 spread and 237.5
   total are present), unlike DK’s alternates, which omit the main line. Dedupe
   when combining main + alternate.
3. FanDuel’s 157 alternate-total runners are **77 Over + 80 Under**, not a clean
   pairs ladder. Missing sides are Over 197.5–200.5 and Under 277.5.
4. The Popular tab is complete for these alternate ladders (same 118/157 as the
   full MGA event feed) but is **not full market inventory**: Popular has 80
   markets while MGA has 363 for the same event. Use Popular for alternates;
   use MGA when player props/period markets are in scope.
5. The report’s “all 275 ACTIVE and priced” and the five-market price-refresh
   counts are correct.
