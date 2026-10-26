# Response to the code reviews

Three reviews of `app/` were done on 2026-10-26:

- a first review (findings **#1–#10**)
- a follow-up on the watchdog fix (**R2-1–R2-3**)
- an independent review (**F1–F16**)

This document covers **every problem the three reviews raised**, de-duplicated.
For each one it gives:

- what it is;
- its impact;
- the situation where it would actually bite;
- what was done, and why.

## How fixes were chosen

A finding was fixed if it met at least one of these:

1. **It can show or pick a price that isn't really on offer.** That's the core
   promise of the board ("best price across DK and FD") and the first thing a
   reviewer checks.
2. **It makes the board's state wrong in a way the user sees.** For example, a
   finished game shown as live, or old prices reappearing.
3. **It corrupts a headline number the submission reports**, such as latency
   or health.
4. **I introduced it.** R2-1 came from my own watchdog fix.

Fixes were kept small and checked against the failure they addressed. Backend
behavior fixes have automated regression tests. Browser removal and replay
fixes were checked manually in a browser, not by a committed automated browser
suite. Display and comment corrections were inspected.

A finding was left open if any of these held:

- It only shows up at a scale this deployment doesn't have: one instance, a
  handful of viewers, tens of games.
- It needs a book to send something unusual that hasn't been seen in hours of
  captured traffic.
- It only happens during shutdown.
- It affects a feature that is off in production.
- Fixing it would trade one risk for another, without a clear win.

Open items are listed in README §14.4 (summarized in §13) so the submission doesn't overclaim.

**Verification note.** Findings marked *(verified)* I confirmed in the code or
with a failing test. The others I'm taking from the review's own description;
I read them but didn't reproduce them.

## Summary

| # | Finding | Severity | Status |
|---|---|---|---|
| R2-1 | Full refreshes dropped behind polls (my regression) | Medium-high | **Fixed** |
| R2-2 / F15 | Reconnect uses an older snapshot than the newest refresh | Medium | **Fixed** |
| #1 / F1 | Slow REST result revives a removed price | High | **Fixed** |
| #2 / F2 | Decimal-only and tie-breaking changes don't recompute best | High | **Fixed** |
| #3 / F3 | Polymarket keeps the old price when the asks disappear | High | **Fixed** |
| #6 / F8 | Health looks fresh without new data | Medium | **Fixed** |
| F4 | A stale book holds a finished game open forever | Medium | **Fixed** |
| F5 | Replayed deltas counted as latency samples | Medium | **Fixed** |
| #4 / F6 | Page keeps a removed game's old prices | Medium | **Fixed** |
| — | Zero price renders as "0" | Low | **Fixed** (with F3) |
| — | CSS comment says 5 minutes | Nit | **Fixed** |
| #7 | REST blocks the supervisor's watchdog | Medium | **Fixed earlier** |
| — | Polymarket can be best with no sportsbook line (undocumented) | Low | **Documented** |
| F12 | Third-book claim overstated; arch test misses `polymarket` | Medium-low | **Documented** |
| F9 / #8 | Polymarket discovery capped at 100 events | Medium | **Open, but close to biting: fix next** |
| R2-3 | Backoff polls cut off by short reconnect deadlines | Medium (conditional) | Open |
| #5 / F7 | SSE writes have no deadline | Medium | Open |
| F11 | FanDuel bundle fetch retried on every snapshot after a failure | Medium | Open |
| #10 / F10 | Board calls can hang after the board stops | Medium | Open |
| F13 | DK `change` frame revives a removed selection | Medium-low | Open |
| F14 | DK spread/total with no line is emitted | Medium-low | Open |
| #9 / F16 | Game history never pruned from memory | Low | Open |
| R2 | Cancellation relies on each book honouring its context | Contract | Open (documented as contract) |
| R2 | Health stays "live" until worker cleanup finishes | Low | Open |
| — | 17 smaller items (debug endpoints, SNTP, boardctl, exit code, …) | Low / nit | Open, one each below |

---

## Fixed

### R2-1. Full refreshes dropped behind polls *(verified; my regression)*

**What.** After I moved REST work off the supervisor's control loop, only one
background job could run at a time, and a timer that fired while a job was
running was simply ignored. That's harmless for a price poll, because another
comes 30 s later. It was harmful for a full refresh, because the timers line up:

- Polymarket polls and refreshes every minute; both timers start together and
  fire together.
- FanDuel's 2-minute refresh lands on every fourth 30-second poll.
- DraftKings' 5-minute refresh lands on every tenth.

A full refresh could lose that race every time.

**Impact.** A full refresh is the **only** way a book discovers new games and
markets, and the only way Polymarket re-picks its main line (polls just
re-price what's already known). With it starved:

- a game that a book listed after we connected wouldn't show up until the next
  socket drop;
- Polymarket's "main line" could stay on a stale line for the whole stream
  session;
- the "full REST pull every 5 minutes" the README describes wouldn't reliably
  happen.

**When it matters.** Every long-lived connection. The longer the socket stays
up (the healthy case), the worse it gets.

**Fix.** A refresh that comes due while a job is running is queued and starts as
soon as that job finishes. Test: hold a poll open across a refresh tick, release
it, and the refresh must run within 200 ms, not at the next tick. In production
after the deploy, Polymarket took 3 snapshots in ~3.5 minutes and FanDuel 2, with
polls running alongside them.

**Why fixed.** I introduced it, it breaks a documented behaviour, and the fix is
about 10 lines.

### R2-2 / F15. Reconnect uses an older snapshot than the newest refresh *(verified)*

**What.** A background refresh with an unchanged subscription applied its data
to the board but didn't update `s.seed`, the snapshot a reconnect starts from.
If the socket dropped inside the snapshot rate limit (2 minutes on FanDuel), the
reconnect used the older one.

**Impact.**
- New markets the refresh found would go unstreamed until the next fresh
  snapshot.
- The board's game mapping would roll back to the old snapshot.
- Combined with F1, a price the refresh removed could come back.

**When it matters.** A socket drop shortly after a refresh. This is common during
live games, when FanDuel adds in-play markets and Polymarket moves its main line.

**Fix.** The refresh hands its snapshot back to the control loop, which updates
both the seed and the snapshot later jobs use. Test: a drop shortly after a
refresh must reconnect with the refreshed snapshot.

**Why fixed.** It's the same code as R2-1, both reviews flagged it, and it
quietly makes recovery worse exactly when recovery matters.

### #1 / F1. A slow REST result revives a removed price *(verified, test)*

**What.** The board's "older REST data can't overwrite newer data" guard only
works while the entry exists. Once the stream removes a price, the entry is
gone, so a poll sent **before** the removal that arrives **after** it is
accepted as new.

**Impact.** A price the book has pulled reappears and can be chosen as **best
price**. This is the worst kind of error for an odds board: it advertises a bet
you can't place.

**When it matters.** Most likely live, when books pull and re-add markets
constantly, polls run every 30 s, and a FanDuel full refresh is several requests
long. The same path also covers reconnecting from an old snapshot.

**Fix.** The board remembers each removal for 10 minutes, longer than any request
can be in flight. Poll and snapshot data no newer than the removal is ignored.
Stream data and newer polls still go through. Test covers a poll and a snapshot.

**Why fixed.** Shows a price that isn't offered.

### #2 / F2. Decimal-only and tie-breaking changes don't recompute best *(verified, test)*

**What.** The board treated an update as "unchanged" when line, American odds,
status and in-play were the same. But best price compares the **precise
decimal**, and breaks exact ties on **recency**. So:

- a decimal move with the same American price (Polymarket, or DK/FD true odds)
  never recomputed best;
- a re-confirmed price never won a tie it should have won;
- browsers already connected never received the new decimal.

**Impact.** The board can name the wrong book as best, against its own documented
rule. Decimal view can show different numbers to a browser that just connected
than to one that's been open a while.

**When it matters.** Two books at the same American price. That's very common at
−110, and constant between Polymarket and a sportsbook.

**Fix.** A decimal change now counts as a change and is sent to the page (without
a flash, since the visible American text is the same). A confirmation recomputes
best and publishes it if it changed. Tests for both cases.

**Why fixed.** Picks the wrong best price.

### #3 / F3. Polymarket keeps the old price when the asks disappear *(verified, test)*

**What.** When Polymarket's order book had no asks, or was crossed, the adapter
emitted nothing. The last price stayed on the board, healthy and eligible for
best. Separately, a book with asks but **no bids** was thrown away, even though
the ask is what you'd pay.

**Impact.**
- A price you can't buy can win best price.
- A valid price was dropped.

**When it matters.** Thin markets, which Polymarket's alternate lines and late-game
markets often are, and the moments a market is closing.

**Fix.** No ask, or a crossed book, now sends a suspended update with no price.
An ask with no bids is priced. A message that says nothing about the ask side is
still treated as no change. The board doesn't show "no price" as a previous price,
and the page shows "—" instead of "0".

**Why fixed.** Shows a price that isn't offered.

### #6 / F8. Health looks fresh without new data *(verified, tests)*

**What.** Two paths marked a book as having fresh data when it didn't:

- reconnecting from a reused snapshot;
- DraftKings returning "nothing" when it skipped a poll for its own rate limit,
  which counted as a successful poll.

**Impact.** Health isn't cosmetic. A **stale** book is greyed out and excluded
from best price; a **degraded** one isn't. A book that keeps failing to connect
could stay degraded indefinitely, its old prices still eligible for best.

**When it matters.** A book that's down for a long stretch: blocked IP, outage,
or a broken socket protocol. That's exactly when the stale badge is supposed to
appear.

**Fix.** A reused snapshot doesn't count as new data. A new `book.ErrSkipped`
lets a book say "I chose not to fetch", which counts as neither data nor a
failure. Tests: with every connect failing, the book reaches **stale**; skipped
polls don't change the poll counters.

**Why fixed.** Stale handling is a brief requirement, and this let old prices
compete for best.

### F4. A stale book holds a finished game open forever *(verified, test)*

**What.** A game counted as "open" if any book had an open price on it, including
a book that had gone stale.

**Impact.** If a book goes quiet while still listing a game, that game stays
**LIVE** after it's over, and is never closed or removed, until that book comes
back. It's the same symptom as the PHI@DET problem seen earlier.

**When it matters.** A book outage that spans the end of a game. During a Sunday
slate that's likely.

**Fix.** Only open prices from books that aren't stale or down count. A brief
outage shows the game as suspended (3-minute grace) rather than closing it at
once. Test: a stale book's open price doesn't stop the close and prune.

**Why fixed.** Wrong game state that the user sees, and the closed-game behaviour
was something you asked for.

### F5. Replayed deltas counted as latency samples *(verified in the browser)*

**What.** When a browser reconnects, the server replays the deltas it missed.
Those frames carry their original times, so the page measured "server → you" as
the length of the outage.

**Impact.** A single 30-second disconnect added samples of about 30 s. The page
keeps the last 300 samples, so the p50/p95 latency on screen could be wrong for
a long time. Latency is a headline deliverable of the brief.

**When it matters.** Any viewer whose connection blips: a laptop waking from
sleep, Wi-Fi switching, a mobile network.

**Fix.** Frames the server sent more than 2 s earlier are applied to the board
but not counted as latency. In a manual browser check, a 30-second-old frame
added no sample and a fresh one added one. This age-based heuristic can also exclude
genuinely delayed deliveries, so it does not provide uncensored tail latency.

**Why fixed.** Corrupts a headline number. The fix is three lines.

### #4 / F6. Page keeps a removed game's old prices *(verified in the browser)*

**What.** When the server removed a game, the page removed the game but kept its
price cells and cached display state.

**Impact.** If the game came back (one book re-lists it), the page showed the
new price **next to all the old ones**. Memory also grew with every game seen.

**When it matters.** Long-open tabs, and games that close and reopen; FanDuel
does that around kickoff and suspensions.

**Fix.** Removing a game deletes all its cells; an emptied cell is deleted;
"does this game have prices" checks for actual entries. Checked manually in
the browser: 6 cells before, 0 after.

**Why fixed.** Old prices shown as current.

### Small fixes along the way

- **Zero price rendered as "0"** (independent review): now "—". This came
  naturally with F3.
- **CSS comment said "5+ minutes"** for the ⚠ icon: corrected to 3.

### #7. REST blocks the supervisor's watchdog *(fixed before round two)*

Reconcile and resnapshot ran inside the supervisor's control loop, so a slow
request delayed detecting a dead socket. Moved to a background job with a test
that fails on the old code. Written up in README §8.1, including R2-1, the bug
the fix itself introduced.

---

## Documented rather than changed

### Polymarket can win best when no sportsbook offers a line *(first review)*

**What.** The README said Polymarket only competes on a line a sportsbook also
offers. The code also lets it compete when **no** sportsbook has an eligible open
price on that side.

**Impact.** Small. The cell shows Polymarket's line instead of "no eligible price".

**When it matters.** Both sportsbooks suspended or stale on a market.

**Decision.** Kept the behaviour. Showing the one available price beats showing
nothing, and a sportsbook line takes over the moment one exists. The README now
states it.

### F12. Third-book claim overstated; arch test misses `polymarket`

**What.** The README said the page needed no changes for a third book. It does
read the book list dynamically, but has since gained a few per-book labels:
which book has no publish time, handshake and ping wording, short names. The
architecture test also bans only `draftkings` and `fanduel` outside their packages.

**Impact.** It's a claim-accuracy problem, not a runtime bug. A fourth book would
need a few page entries.

**Decision.** Narrowed the README claim (§7.3) and named the clean fix (capability
fields on the book descriptor). Not refactored, because it doesn't change
behaviour and the brief asks for the explanation more than the refactor.

---

## Open

### F9 / #8. Polymarket discovery capped at 100 events — **fix next**

**What.** Discovery asks Gamma for `limit=100` once per league and never pages.
Anything past 100 is silently missing, and because a snapshot is authoritative,
games past the cap would be **removed** as if they'd ended.

**Impact.** Polymarket games disappear from the board without any error.

**When it matters.** I checked today: the NFL series returns **42** events, and
the NBA series returns **94 of 100** (98 by that afternoon). This is close to real; a busy NBA stretch
could cross it.

**Why not yet.** It's not a wrong-price bug, and it wasn't in the agreed set. It
is the open item closest to happening in production, and paging is a small
change. **I'd do this one next.**

### R2-3. Backoff polls cut off by short reconnect deadlines

**What.** While waiting to reconnect, the supervisor polls REST to keep prices
fresh, and those polls share the wait's deadline. With full-jitter backoff the
wait can be a few hundred milliseconds, shorter than a normal REST response. The
poll is cancelled but still uses up the rate-limit window, so the next poll
waits 5 s on FanDuel or 15 s on DraftKings.

**Impact.** During repeated stream failures, a period where neither the stream
nor polling delivers prices, even though REST is healthy.

**When it matters.** A socket that fails again and again within the first few
backoff steps, while REST works.

**Why not fixed.** It's a trade-off, not a plain bug. Letting the poll outlive
the wait brings back the bug the watchdog fix removed, where a hung request
delays reconnecting. A good fix (skip polls when less than ~1 s of wait is
left, and track cancellations separately) needs a careful policy, and the
failure is self-limiting: a reconnect or the next poll restores prices within
seconds.

### #5 / F7. SSE writes have no deadline

**What.** The per-viewer queue is bounded, but a write to a client whose socket
has stopped reading can block until TCP gives up, which can take minutes.
"Dropping" the slow client can't interrupt that write.

**Impact.** A goroutine and a connection held per stuck client. Other viewers
are **not** slowed: each has its own queue and the hub never waits on a client.

**When it matters.** Many misbehaving clients, or deliberate abuse, against a
public server.

**Why not fixed.** At this deployment's scale the cost is a few KB per stuck
client, and nobody else is affected. The fix is a per-write deadline, about 10
lines, but it needs a real-socket test to prove it, and that was out of scope.

### F11. FanDuel bundle fetch retried on every snapshot after a failure *(verified)*

**What.** If fetching FanDuel's JS bundle (where its keys come from) fails, the
last good keys are kept, but the fetch time isn't recorded. Every following
snapshot retries the full homepage-and-bundle fetch, each with up to a 25 s
timeout.

**Impact.**
- Slower FanDuel snapshots.
- A warning in the logs every 2 minutes.
- Extra requests to a homepage that may be blocking us.

Prices keep flowing through SMP with the last keys.

**When it matters.** Only while FanDuel's homepage is blocked. That happened once,
on the bad Fly IP, and the static IP fixed it.

**Why not fixed.** It only shows up in an already-degraded mode that we have a
working answer for. Adding a backoff would be simple.

### #10 / F10. Board calls can hang after the board stops

**What.** A request queued just as the board stops is never answered, and a
caller with no deadline waits forever.

**Impact.** A hung goroutine.

**When it matters.** Only during shutdown. Every caller in the app passes a
context that is cancelled at shutdown, so in this program it resolves. The bug
is in the API, not in how it's used today.

**Why not fixed.** Shutdown-only, and not reachable from the current callers.

### F13. DK `change` frame revives a removed selection *(verified)*

**What.** DraftKings' state machine brings a removed selection back on any frame
for its ID; its own comment says only an "add" should. If the market was already
torn down, the revived quote comes out as **open** with its old price.

**Impact.** A pulled DK price reappears as open.

**When it matters.** Only if DK sends a `change` for a selection it already
removed: an out-of-order, duplicated or retried frame. It didn't happen across
7,276 replayed frames, including a full Sunday kickoff.

**Why not fixed.** It needs DK to misbehave in a way never seen. It's a one-line
guard and a good candidate for the next pass.

### F14. DK spread/total with no line is emitted

**What.** A spread or total arriving without `points` is emitted with no line,
and best price treats a missing line as 0.

**Impact.** A line-less quote shown briefly, which could rank wrongly.

**When it matters.** Only if DK sends a partial add without points. Every replayed
frame carried points.

**Why not fixed.** Same reason as F13: unseen upstream behaviour, cheap to guard
later.

### #9 / F16. Game history never pruned from memory

**What.** Pruned games stay in the board's game map and in the matcher forever,
and each tick walks all of them.

**Impact.** Memory and tick work grow with uptime, not with live games.

**When it matters.** Months of uptime. At tens of games a week it amounts to
kilobytes.

**Why not fixed.** Not measurable at this scale. A proper fix needs a retention
rule shared by the board and the matcher.

### Cancellation relies on each book honouring its context *(round two)*

**What.** The supervisor waits for a background job or stream to stop before
reconnecting. A book that ignores cancellation would block that, and shutdown.

**Impact.** A stuck book.

**When it matters.** Only with a book that does blocking work it can't cancel.
All three books use context-aware HTTP and WebSocket calls.

**Why not fixed.** It's the normal Go contract. The alternative, abandoning a
stuck worker, risks two runs of the same book at once, which is worse. Treated as
part of the book contract.

### Health stays "live" until worker cleanup finishes *(round two)*

**What.** "Disconnected" is recorded only after the stream and job goroutines
have exited.

**Impact.** For the length of that cleanup, health says live after the socket
has died.

**When it matters.** If cleanup is slow, which only happens with the uncooperative
book above. With the current books, cleanup takes milliseconds.

**Why not fixed.** No visible effect with the current books.

---

## Smaller items

| Item | Impact / when it matters | Decision |
|---|---|---|
| **Debug endpoints unauthenticated** (kill/pause a book) | Anyone could disrupt ingestion **if** `DEBUG_ENDPOINTS=1` were set on a public server. | Open. It's off on Fly; the demo controls only appear locally. Add a token before ever enabling it publicly. |
| **SNTP replies not fully validated** (mode, leap flag, echoed timestamp) | A spoofed or broken NTP reply could skew the **latency numbers** (never odds). | Open. Replies come from a known server over a 60 s loop with min-RTT selection; worth hardening, low real risk. |
| **`boardctl` misreads confirmation deltas** | Inflated per-book delta counts in a diagnostic CLI. | Open. It's a dev tool, not part of the product. |
| **Server exits 0 if it can't bind its port** *(verified)* | A port clash looks like a clean exit, so the platform might not restart it or raise an alert. | Open. One line; Fly's health check still catches a dead app. |
| **Health updates can publish out of order** | The page could briefly show an older state, corrected within 1 s. | Open. Cosmetic and self-correcting. |
| **Health loop can outlive `Run`** | One idle goroutine ticking. | Open. Only when the board has stopped, i.e. shutdown. |
| **Page has no sequence-gap check** | A lost delta would leave the page stale until reconnect. | Open. SSE over TCP doesn't drop frames within a connection, and resume works by sequence number. |
| **Health cards re-rendered every second** | Resets hover/focus on the cards; cost grows with books. | Open. Cheap at three books. |
| **`hasPrices`/`isLive` scan all slots per game** | Slower full re-renders as the board grows. | Open. Fine at 82 games / 330 slots. |
| **Latency samples kept across a clock change** | Old samples computed with a previous NTP offset stay in p50. | Open. Recording already stops while the clock is invalid; clearing on change is a small follow-up. |
| **FanDuel event with no type defaults to NFL; empty competition ID passes the filter** | An esports event missing those fields could slip through as NFL. | Open. Every real event carries both; defaulting to "unknown" is safer and worth doing with the next FD change. |
| **`/api/board`, `/healthz`, `/metrics` copy the whole board per request** | ~23 KB gzipped per call. | Open. Only matters with heavy scraping. |
| **DK `state.gone` is written but never read** | Dead code, unbounded map. | Open. Cleanup. |
| **Book constructor errors ignored** | None today (they can't fail); a future validation could be dropped. | Open. Cleanup. |
| **No `IdleTimeout`/`MaxHeaderBytes`; shutdown wait unbounded** | Idle non-SSE connections linger; a stuck book could hold SIGTERM until Fly kills it. | Open. Measured shutdown is 327 ms. |
| **`static.go` comment says the UI comes "in a later step"** | Stale comment. | Open. Nit. |
| **Coverage gaps**: `clocksync` and `cmd/` untested, no fuzz tests, no browser/E2E test, slow-client test uses a channel, not a real socket | Bugs in the page and in parsing are found later than they could be. | Partly addressed: backend behavior fixes added automated regression tests; page behavior fixes were checked manually in a browser, and display/comment corrections were inspected. A browser test suite and decoder fuzzing are the next testing investments. |

---

## What I'd do next, in order

1. **F9** Polymarket paging: NBA is at 98/100 by the afternoon of 2026-10-26.
2. **F13 + F14**: two one-line DraftKings guards.
3. **F7** SSE write deadline, with a real slow-socket test.
4. **F11** bundle-fetch backoff, and the **exit-code** one-liner.
5. **R2-3** an explicit fallback-poll policy (skip a poll that can't finish in the time left).
