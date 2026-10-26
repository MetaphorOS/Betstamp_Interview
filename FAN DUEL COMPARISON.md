# FanDuel WebSocket versus SMP polling sanity check

Run October 24, 2026 (Toronto), approximately 21:02-21:04. Raw UTC/epoch timestamps are saved in the capture directory.

One AppSync WebSocket subscription and SMP `getMarketPrices` polling every five seconds observed the same 18 live college-football moneyline/spread/total markets for 90 seconds. This checks live sportsbook data, not NFL-specific coverage or latency.

## Results

- 18 successful batched HTTP polls.
- 59 WebSocket data messages across 15 markets.
- 57/59 pushed states matched a polled state exactly on market ID, in-play flag, market status, sorted selection IDs, handicap, runner status and American odds.
- 56/59 matched the first subsequent poll that returned that market.
- 33/59 matched the closest poll by response receipt time. The lower count reflects sampling at different times; it is not a feed error rate.
- Decimal odds were not included in exact equality: the socket commonly rounds true decimal odds to three places while SMP returns more precision. Example: -114 = 1.877 on the socket versus 1.87719298245614 over SMP.

## Two unmatched states

1. `801.192302465` (Georgia State @ Arkansas State total): polling observed 59.5 at +100/-132; socket then delivered +104/-138, then +102/-136; the next poll observed +102/-136. The intermediate price existed only between polls in this capture. This directly demonstrates the information lost at a five-second polling interval.
2. `801.192302663` (Boise State @ Washington State moneyline): socket delivered SUSPENDED with two runners. Earlier SMP polling returned OPEN with only one runner; later polls omitted that market. The endpoint representations differ around suspension/availability. Do not blindly overwrite a pushed suspension with an older polling response or interpret an omitted market as a confirmed OPEN market.

Example matched spread: `801.192302601`, San Diego State @ Colorado State, +10.5/-10.5 at -114/-114, OPEN, identical selection IDs and runner statuses. The matching poll completed 1,235 ms after the push. This is observation timing, not a source-to-consumer latency estimate.

Conclusion: both transports carry the same sportsbook markets and prices. They are not byte-identical payloads or interchangeable lifecycle representations. Prefer push for changes, polling for bootstrap/recovery, and explicitly reconcile omissions, suspensions, decimal precision and updates arriving during snapshots. This short test cannot prove universal equality or which transport always leads.

Reproduce: Node 24+, `node --use-system-ca compare-fd.cjs`. Requires the previously captured public FanDuel main bundle under `captures/fd-main.js`; discovers fresh live market IDs itself. Finite 90-second run, no login.

Evidence: `captures/comparison-1792890146407/raw.jsonl`, `discovery.json`, `comparison.json`. The comparison JSON contains every pushed normalized state and its closest/matching polling observations.
