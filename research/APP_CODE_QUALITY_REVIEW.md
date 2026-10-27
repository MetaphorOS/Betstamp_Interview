# App code quality review

**Round-two update:** reviewed watchdog change `4933776` at HEAD `43b65b1`. The original synchronous-REST watchdog problem in finding 7 is resolved. See [the follow-up review](D:/Projects/BitStamp_Interview/APP_CODE_QUALITY_REVIEW_ROUND_2.md) for remaining scheduling/reconnect concerns and fresh test results. Other findings below remain the first-round assessment; their application source was unchanged by this fix.

Reviewed October 26, 2026. Scope: `app/`, including the Go services, book adapters, board, SSE delivery, browser code, existing tests, and deployment configuration. No application code or tests were changed.

The architecture has useful boundaries: book-specific parsing is isolated, the board has a single owner goroutine, and fixture/replay tests cover several difficult feed transitions. The main risks are consistency across concurrent REST and stream updates, stale-price handling, and cleanup during long-running sessions.

Findings below are based on code inspection. The scenarios describe regression tests worth adding; they were not implemented or executed as new tests during this review. “High confidence” means the behavior follows directly from the code, not that it has been observed in production. Conditional risks identify the circumstances needed to trigger them.

## Validation

- Existing `go test -mod=readonly ./...`: passed using Go 1.27.3, with some package results cached.
- `go vet -mod=readonly ./...`: passed, exit code 0.
- The first sandboxed test run failed on loopback HTTP connections used by the FanDuel and fanout tests. A rerun with local-server access passed. Those initial errors were environmental, not evidence of application defects.
- No live upstream integration run, browser interaction test, race-detector run, or deployment was performed. Existing tests passing does not exercise all scenarios below.

## Findings

### 1. Older REST results can resurrect a quote removed by the stream

**Priority: high. Confidence: high.**

References: [board.go:465](D:/Projects/BitStamp_Interview/app/internal/board/board.go:465), [board.go:532](D:/Projects/BitStamp_Interview/app/internal/board/board.go:532).

The stale-REST guard only runs when an entry already exists (`had`). Removing an entry deletes both its price and the timestamp needed to reject older data. A poll can start, a stream removal can be applied, and then the earlier poll can return its formerly open quote. Because the entry is now absent, the board accepts that old quote and may select it as best again.

Suggested improvement: retain a per-book, per-slot removal watermark and compare REST request times against it, even when the entry is absent. Bound the retention period to the lifetime of outstanding requests.

Regression scenario: start a poll at T1, remove an existing quote at T2, and apply the poll result at T3 with `AsOf=T1`. The quote must remain removed.

### 2. Decimal-only changes and freshness tie-breaks do not recompute best price

**Priority: high. Confidence: high.**

References: [board.go:485](D:/Projects/BitStamp_Interview/app/internal/board/board.go:485), [board.go:527](D:/Projects/BitStamp_Interview/app/internal/board/board.go:527), [best.go:52](D:/Projects/BitStamp_Interview/app/internal/board/best.go:52), [best.go:73](D:/Projects/BitStamp_Interview/app/internal/board/best.go:73).

`visibleChange` ignores `Decimal` and `UpdatedAt`, although the best-price comparator uses both. When American odds, line, status, and in-play state stay the same, the board silently replaces the decimal value and timestamp, then returns before recomputing best. Confirmation deltas carry only timestamps, so an existing browser also keeps its previous decimal price.

This can leave the wrong winner selected after a precise decimal change, and it violates the documented “most recently updated book” tie-break after an unchanged-price confirmation. A new snapshot can show a different decimal price from an already connected browser.

Suggested improvement: distinguish freshness-only updates from precise-price updates, publish decimal changes, and recompute best whenever an input to the comparator changes.

Regression scenarios: two entries have the same American odds but their precise decimal ranking changes; separately, the losing book confirms an otherwise exact tie more recently.

### 3. Polymarket leaves the previous executable price in place when the ask disappears

**Priority: high. Confidence: high.**

Reference: [transport.go:217](D:/Projects/BitStamp_Interview/app/internal/books/polymarket/transport.go:217).

`seed.update` returns no update when `ask <= 0`, `ask > 1`, `bid <= 0`, or the book is crossed. For a full `book` message with no asks, `bestOf` produces zero, so the previously open quote remains on the board. Heartbeats can keep the book live, and that retained quote remains eligible for best price until another path corrects it. Requiring a positive bid also discards an otherwise usable buy-side ask when there are no bids.

Suggested improvement: distinguish missing liquidity from malformed data. An absent executable ask should suspend or remove the previous quote; ask-side execution should not require a bid unless that is an explicit product rule.

Regression scenario: emit an open quote, then a full book with an empty asks array. The old price must immediately stop being eligible.

### 4. Browser game removal retains old prices and cached display state

**Priority: medium. Confidence: high.**

References: [app.js:191](D:/Projects/BitStamp_Interview/app/cmd/server/web/app.js:191), [app.js:208](D:/Projects/BitStamp_Interview/app/cmd/server/web/app.js:208), [board.go:246](D:/Projects/BitStamp_Interview/app/internal/board/board.go:246).

The browser deletes a removed game from `state.games`, but does not delete its slots or `shown` cache entries. The server's timed prune deletes all slots and sends only a game-removal delta. Consequently, the browser retains entries that no longer exist on the server. If one new open price revives the game, the other old slots can reappear alongside it. Ordinary slot removal also leaves empty slot objects behind, while `hasPrices` checks only whether a key exists.

Suggested improvement: delete all slots and display-cache entries belonging to a removed game; delete a slot when its last entry disappears.

Regression scenario: prune a game with six populated slots, revive it with one quote, and verify that only that quote returns. Verify map sizes remain bounded over repeated add/remove cycles.

### 5. “Slow client dropped” does not interrupt an HTTP write already blocked on the client

**Priority: medium. Confidence: high; impact depends on client/network behavior.**

References: [hub.go:283](D:/Projects/BitStamp_Interview/app/internal/fanout/hub.go:283), [main.go:186](D:/Projects/BitStamp_Interview/app/cmd/server/main.go:186), [hub_test.go:193](D:/Projects/BitStamp_Interview/app/internal/fanout/hub_test.go:193).

The fanout queue is bounded, but `w.Write` and `Flush` have no write deadline. Closing a client's channel cannot interrupt a handler already blocked writing to a peer that has stopped reading. Its goroutine and connection can outlive the hub's client registration. The current slow-client test fills an internal channel; it does not simulate a socket that stops consuming bytes.

Suggested improvement: use a refreshed per-write deadline suitable for a long-lived SSE response and handle flush errors. Avoid a single short timeout covering the whole SSE session.

Regression scenario: connect a real client, stop reading until socket buffers fill, and verify the handler exits within a bounded interval while other clients continue receiving updates.

### 6. Health freshness can advance without receiving fresh data

**Priority: medium. Confidence: high.**

References: [supervisor.go:212](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:212), [supervisor.go:386](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:386), [draftkings.go:135](D:/Projects/BitStamp_Interview/app/internal/books/draftkings/draftkings.go:135).

Reconnecting from a cached seed calls `markData` even though no new data was fetched. Separately, DraftKings returns `(nil, nil)` when its REST budget suppresses a poll; the supervisor counts that as a successful poll and refreshes `lastData`. Repeated reconnects or skipped polls can therefore postpone stale/down health despite a lack of fresh quotes. Since best-price eligibility depends on book health, the effect goes beyond misleading counters.

Suggested improvement: represent “poll skipped” separately from a successful response, and preserve the original data age when reusing a seed. Record transport attempts separately from confirmed freshness.

Regression scenario: seed once, repeatedly fail connections and suppress polls, and verify the original data ages into stale rather than being refreshed by reuse.

### 7. REST reconciliation blocks the supervisor's watchdog and control loop — resolved in round two

**Current status:** commit `4933776` moves REST work off the control loop and cancels it with the stream. The new watchdog regression test passes, including in the full race-enabled suite and twenty repeated supervisor-suite runs. The explanation below describes the previous implementation, not the current code. Remaining concerns are documented in the round-two review linked above.

**Priority: medium. Confidence: high.**

References: [supervisor.go:432](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:432), [supervisor.go:493](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:493), [supervisor.go:580](D:/Projects/BitStamp_Interview/app/internal/supervisor/supervisor.go:580).

Reconcile and resnapshot execute synchronously inside the same select loop that processes heartbeat timeouts, stream completion, and kill requests. A slow multi-request snapshot can occupy this loop across several HTTP timeouts. Meanwhile, `evaluate` classifies the book as live whenever `s.connected` is true; it does not independently check heartbeat age. A dead stream can therefore remain live and eligible while a REST operation is in flight.

Suggested improvement: run bounded reconciliation work separately and deliver its result to the control loop; derive live health from both connection state and heartbeat age. Preserve serialization of snapshot application and stream restarts.

Regression scenario: block a fake reconciliation request while the stream stops heartbeating. Timeout handling and health degradation should still occur on schedule.

### 8. Polymarket discovery silently stops at the first 100 events

**Priority: medium. Confidence: high about the implementation; conditional on series size.**

References: [polymarket.go:32](D:/Projects/BitStamp_Interview/app/internal/books/polymarket/polymarket.go:32), [polymarket.go:122](D:/Projects/BitStamp_Interview/app/internal/books/polymarket/polymarket.go:122).

Discovery requests `limit=100` once per series and never paginates or checks whether it reached the cap. If a series contains more matching events, coverage becomes incomplete without an error. Because a snapshot is treated as authoritative, previously displayed entries that fall outside the returned page can be removed as though the source stopped offering them.

Suggested improvement: fetch the complete intended event window using the upstream pagination contract, or explicitly bound discovery by date and report truncation. Treat an incomplete fetch as incomplete rather than authoritative deletion evidence.

Regression scenario: a mock source returns more than one page, and an event appears only on the second page. It should remain discoverable and subscribed. Current upstream event counts were not checked in this review.

### 9. Server-side game history has no retention bound

**Priority: medium for a long-lived service. Confidence: high.**

References: [board.go:246](D:/Projects/BitStamp_Interview/app/internal/board/board.go:246), [board.go:532](D:/Projects/BitStamp_Interview/app/internal/board/board.go:532), [match.go:22](D:/Projects/BitStamp_Interview/app/internal/match/match.go:22).

Pruning removes slots but retains `core.games`; removing the final entry also leaves its game record. The matcher retains all historical games in `byTeams` and `byID`. Every board tick sorts all retained game IDs, and matching scans the historical games for a team pair. Memory and background work therefore grow with the lifetime of the process, rather than the number of active games.

Suggested improvement: define coordinated retention for board and matcher records, preserving a limited revival window and any references still needed by active subscriptions.

Regression scenario: simulate many days of completed games and assert that retained records and tick work settle near a configured bound.

### 10. Board request methods can wait forever if the board stops after accepting a request

**Priority: medium. Confidence: high; requires a shutdown race.**

References: [board.go:717](D:/Projects/BitStamp_Interview/app/internal/board/board.go:717), [board.go:739](D:/Projects/BitStamp_Interview/app/internal/board/board.go:739).

`do` returns after enqueueing a closure. If `Run` stops before executing that closure, `Snapshot` and `SubscribeWithState` wait only for their reply or the caller's context, not `b.done`. A caller using `context.Background()` can wait indefinitely. A subscription whose caller cancels before receiving the reply can also be created without its cancellation function ever reaching the caller; it survives until the board drops it or stops.

Suggested improvement: observe board termination while awaiting replies and arrange cleanup when subscription creation races caller cancellation.

Regression scenarios: stop the board after request acceptance but before execution; separately cancel a subscription request during creation. Neither should leave a blocked caller or an abandoned subscriber.

## Additional conditional and maintenance concerns

- **Debug controls are unauthenticated when enabled.** [main.go:156](D:/Projects/BitStamp_Interview/app/cmd/server/main.go:156) exposes kill/pause operations on the main listener. They default to disabled, so this is a deployment-dependent concern. If enabled on a public instance, any caller can disrupt ingestion. Bind them to an internal listener or require authorization before public use.
- **SNTP response validation is incomplete.** [clocksync.go:44](D:/Projects/BitStamp_Interview/app/internal/clocksync/clocksync.go:44) checks length and nonzero stratum but does not reject unsynchronized replies, validate response mode, correlate an echoed request timestamp, or reject impossible timestamp/RTT combinations. Invalid samples could be marked valid and distort displayed latency. Add protocol-validation tests before relying on these numbers operationally.
- **The prediction-market line rule has an undocumented fallback.** [board.go:338](D:/Projects/BitStamp_Interview/app/internal/board/board.go:338) permits a spread/total prediction-market quote when there are no eligible sportsbook lines. The preceding comment and best-price policy say it must match a sportsbook line. Clarify whether standalone prediction-market selection is intended, and test that decision explicitly.
- **CLI diagnostics do not handle all delta kinds.** In [boardctl/main.go](D:/Projects/BitStamp_Interview/app/cmd/boardctl/main.go), `printer.delta` handles health and game deltas, then treats everything else as a slot delta. Confirmation deltas have no game/identity and create bookkeeping under the key `|`, inflating per-book delta counts and printing misleading empty-slot output. Game removal also does not delete the printer's game cache. Handle confirmation and removal semantics explicitly.
- **Coverage is concentrated below the UI and transport boundaries.** Existing parsing, replay, board, and supervisor tests are valuable. There are no browser tests in the reviewed tree, no clocksync tests, and no command-package tests. Prioritize regression coverage for findings 1–7 and actual blocked-network behavior over broad cosmetic test additions.

## Suggested order of work

Address removal ordering, precise-price propagation, and unavailable-liquidity handling first: these can display or select an incorrect price. Next fix browser cleanup and health freshness, then bound transport waits and retained history. Resolve the conditional policy and deployment concerns according to how the application will be used.
