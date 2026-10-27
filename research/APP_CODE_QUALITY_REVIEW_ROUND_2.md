# App code quality review — round two

Reviewed October 26, 2026, at HEAD `43b65b1`, focusing on watchdog implementation commit `4933776` and its interactions with the existing adapters, board, health reporting, and reconnect logic.

**Verdict:** the original synchronous-REST watchdog bug is fixed. The replacement has a meaningful scheduling concern: full refresh requests can be discarded while polling is in progress. Reconnect seed freshness remains an existing problem, and the new backoff deadline creates a tradeoff for fallback polling that needs explicit tests.

No application code or tests were changed. This report and the status note in the first review are the only edits made for this round. The existing independent review was read for context; its reproduction and coverage claims are not treated as checks performed in this round.

## Scope and validation

The implementation diff contains changes only to `app/internal/supervisor/supervisor.go`, its tests, and the README. I reviewed that diff, the complete updated supervisor and its tests, and the adapter code involved in polling, snapshot discovery, and cancellation. This was a focused follow-up, rather than a fresh line-by-line audit of every unchanged file.

| Check performed this round | Result |
|---|---|
| `go test -race -mod=readonly -count=1 ./...` | All packages passed; no race reports |
| `go test -mod=readonly ./internal/supervisor -count=20` | Passed all twenty runs, approximately 54 seconds |
| `go vet -mod=readonly ./...` | Passed |
| New probe tests, live upstream testing, browser tests | Not performed |

The race-enabled run used the installed Go 1.27.3 toolchain and GCC, with local HTTP test-server access. The issues below are code-derived findings and conditional scenarios, not newly executed reproductions.

## What the watchdog change fixes

At [supervisor.go:443](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:443), periodic REST work now runs in a background job instead of directly inside the control loop. While a context-aware poll is waiting on HTTP, the loop can process heartbeat expiry, kill requests, and stream completion. The job uses the stream context and is joined before a replacement stream starts.

The job result channel is buffered, and `jobRunning` is owned by the control-loop goroutine. Inspection did not identify a data race in that bookkeeping, and the race-enabled suite passed. Panic recovery also ensures a background job signals completion.

`TestWatchdogFiresDuringSlowReconcile` is useful regression coverage: its fake poll waits for cancellation while the stream goes silent, and the test verifies cancellation and reconnection. This directly covers the old failure. It does not cover overlapping periodic refreshes, slow successful fallback polls, or delayed cleanup after cancellation.

## R2-1. Periodic full refreshes can be discarded behind polls

**Priority: medium. New behavior introduced by the fix. Confidence: high.**

References: [supervisor.go:455](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:455), [supervisor.go:535](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:535), [polymarket.go:107](D:/Projects/BitStamp_Interview/app/internal/books/polymarket/polymarket.go:107), [fanduel.go:150](D:/Projects/BitStamp_Interview/app/internal/books/fanduel/fanduel.go:150).

`startJob` returns false whenever another job is running. The post-connect reconciliation remembers this and retries, but periodic reconciliation and resnapshot handlers ignore the return value. A due full snapshot is therefore consumed and forgotten when a poll is active. Job completion does not run any pending full refresh.

The configured timers make this more than an unusual timing coincidence: Polymarket schedules both operations every minute; FanDuel's two-minute full refresh coincides with its thirty-second poll schedule; DraftKings' five-minute refresh coincides with its thirty-second schedule. Which operation wins depends on timing, and there is no guarantee of eventual full-refresh service if collisions recur.

This distinction matters because polling does not replace discovery. Polymarket's poll obtains a snapshot internally but returns only price updates; it does not install the new game mapping or restart subscriptions when tokens change. FanDuel polls only the seeded market IDs. Repeatedly missed full refreshes can leave new games or markets undiscovered and old subscriptions active.

**Suggested improvement:** coalesce a pending full-refresh request and run it when the active job completes, with full refresh taking precedence over a redundant price poll. A single scheduler tracking next-due work is another option. Keep the one-job-at-a-time property.

**Regression scenario:** hold a poll open across a resnapshot tick, release it, and verify a full snapshot runs promptly afterward without waiting another full interval. Also test equal poll/refresh intervals over several cycles.

## R2-2. Successful same-subscription snapshots do not advance the reconnect seed

**Priority: medium. Existing issue, still present after the fix. Confidence: high.**

References: [supervisor.go:211](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:211), [supervisor.go:539](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:539), [supervisor.go:556](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:556).

When a refreshed snapshot has the same subscription string, `resnapshot` applies it to the board and returns nil. Neither `s.seed` nor the `snap` captured by the job closures is replaced. At the same time, the refresh advances `lastSnapshotAt`.

If the socket drops shortly after that refresh, the snapshot rate limit can prevent another fetch. `Run` then reconnects using the older seed, even though a newer snapshot was just obtained. Changes to the private mapping or cached runner values that do not change the subscription string are lost for that reconnect. A retained older quote can also interact with the first review's removal-watermark issue and reappear when the old snapshot is reapplied.

**Suggested improvement:** return a structured job result containing the latest successful snapshot and whether a restart is required. Update the reconnect seed on the Run/control-loop goroutine even when no restart is necessary. Explicitly decide how poll seeds are advanced; do not mutate a snapshot already shared with a running adapter.

**Regression scenario:** start from snapshot A, complete snapshot B with the same subscription but different private mapping/prices, then drop the socket within `MinSnapshotGap`. Verify the replacement stream receives B.

## R2-3. Fallback polls inherit potentially tiny reconnect deadlines and consume their polling budget

**Priority: medium, conditional on REST latency and reconnect timing. New tradeoff introduced by the fix. Confidence: high about the control flow.**

References: [supervisor.go:278](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:278), [supervisor.go:347](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:347), [draftkings.go:116](D:/Projects/BitStamp_Interview/app/internal/books/draftkings/draftkings.go:116), [fanduel/transport.go:228](D:/Projects/BitStamp_Interview/app/internal/books/fanduel/transport.go:228).

Bounding a backoff wait is useful, but `wait` now passes its entire remaining deadline directly to `Poll`. Full-jitter backoff can be shorter than a normal REST response, especially during early reconnect attempts. The supervisor records `lastPollAt` before calling the adapter; DraftKings also records `lastRead` before its requests. A poll canceled because reconnect time arrived still consumes those rate-limit windows.

For example, a 100 ms backoff can cancel a healthy 500 ms poll, after which FanDuel's next poll is suppressed for its five-second minimum gap or DraftKings' for its fifteen-second gap. Multi-batch FanDuel polls discard accumulated updates if a later batch is canceled. Deadline cancellations also bypass the supervisor's poll-error counters because `ctx.Err()` is non-nil.

The result is a period with neither a working stream nor a successful fallback refresh, despite a healthy REST path. This is a latency-dependent degradation, not a claim that fallback polling always fails. The existing fallback test returns immediately and does not expose this tradeoff.

**Suggested improvement:** give fallback polling an explicit scheduling policy. Avoid starting a request with an unusably small remaining budget, or manage fallback work independently while preserving adapter rate limits and preventing overlap. Track deliberate cancellation separately from successful polls and upstream failures. Do not simply retry canceled requests without respecting the upstream request budget.

**Regression scenario:** use a poll that succeeds after a realistic delay, repeated stream failures, and short jittered waits. Verify useful fallback updates arrive within a stated freshness bound and that cancellation metrics explain skipped refreshes.

## Remaining lifecycle limits and test gaps

**Cancellation still relies on adapter cooperation.** [supervisor.go:447](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:447) waits for the background job, and the stop helper waits for the stream. Neither wait has an independent completion bound. The current HTTP adapters generally propagate request contexts, which is why the fix works for the tested case. A plugin blocked in non-cancelable work can still prevent reconnect or shutdown. Treat this as an interface contract and hardening limit, not a reproduced failure of the current HTTP calls. Avoid resolving it by abandoning workers and allowing overlapping adapter runs.

**Health changes only after worker cleanup.** `s.setConnected(false)` remains after `runStream` returns, and `evaluate` selects live solely from `s.connected` ([supervisor.go:623](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:623)). If cleanup is slow after a watchdog timeout, health can remain live during that delay. Mark the transport disconnected when failure is detected and test the health transition independently of worker completion.

**Useful missing tests:** kill during an active resnapshot; parent cancellation during a job; subscription-changing snapshot completion racing stream failure; bounded job concurrency; a due full refresh behind a poll; and a slow but successful fallback poll. The current watchdog test demonstrates the principal fix, while its two-second allowed duration for a 300 ms heartbeat timeout is a broad liveness bound rather than a strict timing guarantee.

## Status of the first review

| First-round finding | Status after this change |
|---|---|
| 7: synchronous REST blocks the watchdog | Resolved for context-aware REST work; lifecycle limits noted above |
| 6: freshness advances on cached-seed reuse or skipped polls | Still present; this change does not alter those calls to `markData` |
| 1–5 and 8–10: board ordering, decimal updates, missing asks, browser cleanup, blocked SSE writes, pagination, retention, board shutdown requests | Source unchanged by the watchdog fix; prior findings remain applicable |

Prioritize R2-1 so full discovery cannot be lost behind routine polls, then R2-2 to make reconnects use the newest available state. Test and document the fallback deadline policy in R2-3. The successful race and repeat runs support the watchdog fix, but do not establish correctness of these untested schedules.
