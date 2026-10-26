# WebSocket versus polling: measured results

**October 25 update:** DraftKings is now measured successfully. See [DK-POLLING-FIX.md](DK-POLLING-FIX.md): 120 successful snapshots, six matched live NFL selection-price transitions; WebSocket first on five, polling first on one. The earlier DraftKings blocker section below is historical.

Tested October 24, 2026, approximately 21:09-21:12 America/Toronto.

## FanDuel

A two-minute simultaneous run watched 18 live college-football main markets using one AppSync WebSocket and one batched SMP getMarketPrices request every five seconds. Both were measured in the same Node process with monotonic receipt timestamps, eliminating cross-machine clock skew from the comparison.

| Metric | Result |
|---|---:|
| Successful polls | 24 |
| WebSocket data frames | 73 |
| Matched selection-price transitions | 76 |
| WebSocket first | 76 |
| Poll first | 0 |
| Median WebSocket lead | 1,803.5 ms |
| 95th percentile WebSocket lead | 4,079.6 ms |
| Minimum / maximum lead | 110.3 / 4,364.8 ms |

A positive lead means the polling response completed after the WebSocket message carrying the same price change.

Method: ignore each transport's initial state for a selection, deduplicate unchanged prices, then match subsequent transitions by market ID, selection ID, handicap and American odds within 15 seconds. Count selection transitions, not independent games: two sides of one market may move together. Market suspension changes alone are not price transitions. The run contained 92 WebSocket and 100 polling transitions; 16 and 24 respectively were unmatched. Unmatched transitions are not proof of data loss: different baselines, intermediate prices, end-of-run boundaries, availability and repeated price states complicate matching. Exact recurrence can make pairing ambiguous; raw records are retained for audit.

Reanalysis of the prior 90-second capture found 73 matched transitions: WebSocket first 65 times, polling first eight times, median WebSocket lead 2,295 ms. The largest polling lead was 442 ms. That prior run used wall-clock timestamps and is weaker timing evidence than the new monotonic run, but cautions against claiming the socket always leads.

Conclusion: **WebSocket was faster in the tested five-second polling setup.** Most of this measured advantage is consistent with avoiding the polling interval. This does not establish that the WebSocket's upstream source always publishes before the HTTP source, that one-second polling would show the same advantage, or that live NFL has the same distribution. A short college-football test is not an NFL-specific or source-engine-to-screen latency benchmark.

Evidence:

- `captures/comparison-1792890604271/raw.jsonl`: simultaneous raw captures.
- `captures/comparison-1792890604271/race.json`: all matched timing pairs and summary.
- `captures/comparison-1792890146407/race.json`: earlier run reanalysis.
- `compare-fd.cjs`: capture tool; `CAPTURE_MS=120000` selects two minutes.
- `analyze-race.cjs`: transition matching and timing calculation.

## DraftKings

**Not measured successfully.** The socket was verified earlier, but the HTTP side still returned Akamai 403 on this run with ordinary public frontend headers:

- `/api/sportscontent/dkcaon/v1/leagues/88808`
- `/api/sportscontent/dkcaon/v1/leagues/88808/categories/492/subcategories/4518`
- `/api/sportscontent/dkcaon/v1/leagues/87637`

Error bodies are in `captures/dk-discovery-0.json` through `dk-discovery-2.json`. These are failed snapshot responses, not odds data. `discover-dk.cjs` reproduces the checks.

The architectural expectation is that push avoids waiting for a polling cycle. That is not an empirical claim that DraftKings' socket always beats its snapshot API. A current accessible snapshot route/session is needed before a fair race is possible. No latency is inferred from a 403 response or from the earlier clock-skewed DraftKings publish timestamps.
