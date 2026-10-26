# NBA WebSocket versus polling: measured results

Tested October 25, 2026, approximately **4:34–4:44 p.m. Toronto time**, in two five-minute pregame windows. The collectors have stopped. This was a targeted comparison, not the canceled three-hour soak test.

**Actual alternate-price changes were verified over WebSocket for both books. DraftKings' alternate updates beat one-second polling in this sample. FanDuel was mixed: sometimes the socket won, sometimes polling won. DraftKings main lines remain inconclusive because neither transport showed a price change.**

## Results

Positive lead means WebSocket arrived before polling. Negative lead means polling arrived first. Counts are matched selection-price transitions, not independent games or statistical trials.

| Book / market | Matched price changes | Socket first | Poll first | Observed socket lead |
|---|---:|---:|---:|---|
| DraftKings main lines | 0 | — | — | Inconclusive |
| DraftKings alternate spread | 65 | 65 | 0 | +265 to +523 ms |
| DraftKings alternate total | 85 | 85 | 0 | +208 to +355 ms |
| FanDuel moneyline | 4 | 3 | 1 | −267 to +435 ms |
| FanDuel alternate spread | 118 | 82 | 36 | −393 to +653 ms |
| FanDuel alternate total | 174 | 101 | 73 | −293 to +753 ms |

FanDuel main spreads and main totals were subscribed and polled, but produced no matched price movements. The moneyline result must not be generalized into verified price updates for all three main-line types.

### DraftKings

The server acknowledged separate main, alternate-spread, and alternate-total subscriptions, using the exact event filters and market filters supplied in its REST snapshots. For LA Clippers at OKC Thunder, we observed actual selection-price deltas for alternate market IDs `2_86923930` and `3_86923930`. All 150 changed prices were subsequently observed in polling.

Selection-weighted median socket leads were **480 ms for alternate spreads** and **333 ms for alternate totals**. However, these changes came from one market of each type in short repricing bursts. DraftKings emitted individual selection updates; 150 messages do not establish 150 independent latency experiments.

The main subscription received event updates, but no price changes occurred in either transport. This confirms an active subscription, not main-line price-delivery completeness or a speed advantage. A separate 15-second request with `initialData:true` also yielded acknowledgments/event updates, not an initial priced snapshot; it is excluded from timing analysis.

### FanDuel

All observed matched movements were for **Lakers at Jazz**. Actual `onUpdateMarketPrice` messages contained moneyline and alternate runners, and their changed American odds matched the REST price responses.

The first window favored the socket:

- Jazz moneyline +184 → +188: socket first by **18 ms**.
- 27 alternate-spread price changes: socket first by **122 ms**.
- 77 alternate-total price changes: socket first by **79 ms**.

The next window immediately demonstrated the reverse:

- Lakers moneyline −220 → −225: polling first by **267 ms**.
- 35 alternate-spread price changes: polling first by **119 ms**.
- 33 alternate-total price changes: polling first by **23 ms**.

Later batches favored either transport. Across both windows, the 296 matched FanDuel selection changes were carried by just **11 price-changing socket messages**: three moneyline, four alternate-spread, and four alternate-total messages. A whole ladder can be repriced together, so counting each runner as an independent speed win would exaggerate the evidence.

## What the experiment actually measures

- One process and one monotonic clock timestamped completed HTTP response bodies and incoming WebSocket callbacks. No comparison of clocks on separate sportsbook servers was required.
- Each DraftKings endpoint was polled once per second, independently. FanDuel's 35 selected market IDs were refreshed together once per second through its fixed-odds `getMarketPrices` endpoint.
- DraftKings coverage was 36 main markets plus seven alternate-spread and seven alternate-total markets. FanDuel coverage was seven actual NBA games, with three main markets and two alternate markets each.
- We matched sportsbook, market ID, selection ID, line, and the exact old → new American price. Only unique matches within 15 seconds were paired. Initial observations were not counted as changes.
- We compared American prices to avoid falsely treating differing decimal rounding as a price change. New selections, line changes without a same-line price transition, and status-only changes are not included in the timing table.
- All 1,798 DraftKings HTTP responses and 614 FanDuel HTTP responses across the two windows returned 200; FanDuel's count includes 16 discovery requests. There were no recorded poll errors or unexpected socket error events. The scheduled shutdown closed the sockets.

These are **arrival-time differences under one-second polling**, not intrinsic network latency or a guarantee that one transport always publishes first. Poll timing, response caching, server publication paths, and local processing can affect the result. Tens-of-milliseconds differences deserve particular caution because both feeds share the collector's event loop and synchronous raw-log writes. The test did not isolate the cause of FanDuel's ordering reversals.

## Practical conclusion

For DraftKings alternates, the socket provided earlier observations in the measured repricing bursts. For FanDuel, choosing the socket alone would have missed the earliest observation on several batches. Keeping REST for discovery/reconciliation and considering both update sources is justified, but a production merger needs stale-update and ordering rules; blindly overwriting the latest value with the next arrival can regress the price.

The remaining validation is **actual DraftKings main-line movement and FanDuel main spread/total movement**, preferably during live NBA play. This run does not support claiming that WebSocket is faster for both books across all NBA markets.

## Reproducibility and raw evidence

- Collector: `nba-race.cjs` (default five minutes; `CAPTURE_MS` can override duration).
- Analyzer: `analyze-nba-race.cjs`; run with the capture directory as its argument.
- Window 1: `captures/nba-race-1792960471538/`
- Window 2: `captures/nba-race-1792960778002/`
- Each directory contains `metadata.json`, timestamped raw HTTP/WebSocket records in `raw.jsonl`, and matched transitions in `analysis.json`.
- Synthetic analyzer checks separately verified socket-first and poll-first matching and DraftKings partial-selection merging. Synthetic data is under `captures/nba-analyzer-check/` and is excluded from the empirical results.

An initial aborted discovery run selected simulated basketball from FanDuel's page. It was excluded entirely. The collector now explicitly requires NBA competition ID `10547864`. This also corrected the earlier market report: its FanDuel main-board count is **7 NBA games / 21 main markets / 42 runners**, rather than the previous count that included simulated basketball.
