# One-hour soak test results

Run: **2026-10-25 16:20:28Z → 17:20:28Z** (12:20–13:20 America/Toronto)
Harness: `.\go\oddsfeed.exe soak -duration 1h`
Output: `out/soak-1h/`
Verdict: **passed** — `"ok": true`, 99.94% DraftKings uptime, 99.93% FanDuel
uptime, zero REST errors, zero unknown WebSocket changes.

The run deliberately spans the NFL 1 p.m. ET kickoff window, so it includes
pregame, market removal/closure, live trading, and a full market-set
transition.

## Configuration

| Setting | Value |
|---|---|
| Duration | 3600 s |
| DraftKings scope | `partials` (snapshot-derived subscriptions) |
| DK snapshot + reconcile | every 30 s |
| FanDuel SMP poll + reconcile | every 30 s (two batches of ≤70 ids) |
| FanDuel discovery refresh | every 5 min |
| WebSocket ping | every 30 s |
| Watchdog (no frame/pong) | 10 min |
| Forced reconnect | disabled |

## Final summary

| Metric | DraftKings | FanDuel |
|---|---:|---:|
| WebSocket connections | 2 | 3 |
| Reconnects | 1 | 2 |
| Connected seconds | 3597.87 | 3597.43 |
| Total not-connected (incl. startup) | 2.13 s | 2.57 s |
| Reconnect gaps only | 1.58 s | 2.29 s |
| Uptime | **99.941%** | **99.929%** |
| WS frames received | 7,279 | 1,688 |
| Price/update frames | 7,273 | 1,621 data |
| Unknown changes | 0 | 0 |
| Pings sent / pongs seen | 119 / 237 | 118 / 117 |
| Pings that failed | 0 | 0 |
| Watchdog timeouts | 0 | 0 |
| REST requests | 122 | 220 |
| REST errors | **0** | **0** |
| Reconcile checks | 120 | 120 |
| Selections/markets compared | 15,054 | 7,311 |
| Missing in WS cache/state | 27 | 0 |
| Price differences | 25 | 17 |
| Status differences | 0 | 3 |
| Line differences | 0 | 2 |
| Records filled from REST | 301 | 0 |
| Discovery checks / changes | — | 12 / 2 |

## Connection timeline

| Time (UTC) | Event |
|---|---|
| 16:20:28.290 | FanDuel AppSync connected, 78 main markets, `start_ack` |
| 16:20:28.564 | DraftKings connected, 3 snapshot subscriptions |
| 16:35:28.884 | FD discovery removed 3 markets (75 left) → intentional resubscribe |
| 16:35:30.051 | FD reconnected with 75 markets; **1.17 s gap** |
| 16:50:28.617 | **DK server closed the socket at exactly 1800.05 s** with `1006 unexpected EOF` |
| 16:50:30.192 | DK reconnected with a fresh snapshot and 3 subscriptions; **1.58 s gap** |
| 17:00:28.657 | NFL kickoff: FD discovery changed 75 → 36 live main markets → resubscribe |
| 17:00:29.777 | FD reconnected with 36 markets; **1.12 s gap** |
| 17:20:28.008 | Clean shutdown (`1000`, run complete) |

The DraftKings close is the important finding: **one DK socket cannot stay up
for three hours.** The server terminates it after 30 minutes, so a three-hour
run needs the reconnect/resubscribe loop, not just a longer idle timeout. The
harness reconnected in 1.6 s and restored the cache from a fresh snapshot.

FanDuel did not show a server-imposed lifetime in this run (connections lasted
1500 s and 1198 s). Its two reconnects were deliberate, caused by discovery
seeing the market set change; each cost about 1.1 s.

## WebSocket liveness

- DraftKings received 7,279 frames in 3600 s, including `subscribed`
  acknowledgements on both connections. The largest gap between frames was
  **417.9 s (6.96 min)**, during a quiet pregame stretch; 30-second pings and
  pongs kept the watchdog satisfied, and the socket stayed up until the
  server's 30-minute limit.
- FanDuel received 3 `connection_ack`, 3 `start_ack`, 61 `ka` keepalives and
  1,621 data frames from **67 distinct markets** over the run. The largest gap
  was 60.0 s. The AppSync socket was never closed by the server.
- No `watchdog_stale`, `subscription_error`, `poll_error`, `resync_error` or
  `rest_error` event occurred.

## REST reconciliation

All 342 REST response bodies were saved as JSON with a `.meta.json` sidecar:
342/342 parsed, 342/342 reported HTTP 200, and every file size matched the
sidecar `bytes` value.

- **DraftKings:** 120 periodic snapshots plus 2 bootstrap snapshots. 15,054
  main-market selections compared; 27 were missing from the WS cache and were
  filled from REST (301 records including their markets/events). 25 price
  differences occurred in 8 of 120 checks, all small and consistent with the
  snapshot being taken a moment before/after a socket update. No status or line
  differences.
- **FanDuel:** 120 SMP reconciliation rounds (200 batch requests), 12 discovery
  refreshes and 3 bootstrap/resync rounds. 7,311 market comparisons: 7,300
  exact (99.85%). The 11 differences were 17 price moves, 3 suspension/status
  differences and 2 handicap moves around live trading. 183 markets appeared in
  the pushed state but were omitted from an SMP poll during transitions; this
  is the documented suspension/availability representation difference, not
  data loss.
- The initial snapshot/poll in each book is recorded as `baseline: true` and is
  not counted as drift.

Representative transient differences (not errors):

```text
FD price  801.191654336/50210 push=-104 poll=-102
FD status 801.191654393 push=SUSPENDED poll=OPEN
DK price  0ML86921423_3 snapshot=-158 cache=-157
DK missing 0HC86921430N850_3 (PIT Steelers)  → filled from snapshot
```

## Saved output

`out/soak-1h/` is 57.2 MB:

| Path | Contents |
|---|---|
| `summary.json` | final counters and `ok` |
| `events.jsonl` | 165 lifecycle/progress/reconcile events |
| `dk/ws.jsonl` | 7,285 sent/received DK frames |
| `dk/reconcile.jsonl` | 120 records |
| `dk/rest/` | 122 snapshot bodies + 122 meta files (29.2 MB total DK) |
| `dk/cache-final.json` | final merged cache (13 events, 39 markets, 78 selections) |
| `fd/ws.jsonl` | 1,694 AppSync frames |
| `fd/reconcile.jsonl` | 120 records |
| `fd/discovery.jsonl` | 12 records |
| `fd/rest/` | 220 body/meta pairs (27.9 MB total FD) |
| `fd/state-final.json` | final 36-market pushed-state map |
| `console.log` | stdout/stderr of the run |

Raw frames and REST bodies are the complete capture; nothing is sampled or
dropped.

## Is three hours feasible?

Yes, with the reconnect design already in the harness:

- **DraftKings:** expect five or six 30-minute server closes over three hours.
  Each costs about 1.6 s, so approximately **10 s of downtime (~99.9% uptime)**.
  The cache is re-seeded from a fresh snapshot on every reconnect, so live
  prices do not go stale across a close.
- **FanDuel:** the second connection lasted 25 minutes and the third was still
  healthy after 20 minutes when the run ended; no server close was observed.
  Market-set transitions cause deliberate resubscribes of about 1.1 s. Any
  server close is handled by the same backoff/baseline path.
- **REST:** at the observed cadences, a three-hour run writes about 360 DK
  snapshots (~58 MB), 36 FD discovery pages (~47 MB), and roughly 500–700 SMP
  batch bodies (~50–70 MB), plus WebSocket logs. Budget roughly **200–300 MB**
  depending on how much live price traffic occurs.
- **Memory:** the process held about 36 MB working set during the hour; the
  caches are bounded by the number of open events/markets, not by frames.

Run it with:

```powershell
.\go\oddsfeed.exe soak -duration 3h
```

The same 30-second REST cadence, 30-second pings and 10-minute watchdog apply.
No code change is needed. `-reconnect-every 40s` remains available to rehearse
recovery immediately; a two-minute run with it produced three connections and
two reconnects per book with zero errors.
