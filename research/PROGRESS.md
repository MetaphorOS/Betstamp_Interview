# Progress report: handoff steps 1–10

Everything below was measured by the tests in this module or by `boardctl`
live runs from the Ontario dev PC on 2026-10-25/26. Re-run with:

```bash
. ../research/goenv.sh && go vet ./... && go test -race ./... -count=1 -v
```

## Replay acceptance tests (section 11)

| # | Test | Result |
|---|------|--------|
| 1 | DK soak conn 1 (all 427 frames vs `bootstrap-0001`) | **0 resyncs**; 244 updates: 201 open, 37 suspended, 6 removed; 32 line moves; in-play updates present; no nil spread/total lines |
| 2 | DK soak conn 2 (all 6,852 frames = 6,849 updates + 3 acks, vs `bootstrap-0002`, spans the 1 p.m. kickoff) | **0 resyncs**; 3,791 updates: 3,610 open, 103 suspended, 78 removed; 618 line moves; 4 same-ID revivals; all 7 kicked-off games end `InPlay=true`; no slot is `Removed` and later back |
| 3 | DK unit tests (fake clock) | add→remove, remove→add, remove-only after 2 s, market flap, kickoff re-add with new market IDs, selection remove not shortening an event tombstone, ML cross-market replacement, live-market suspension, partial change keeps line, stale tombstone vs replacement, seed not mutated: all pass |
| 4 | FD soak replay (3 connections, each vs the discovery board it started from) | 1,621 data frames, **0 unknown markets/runners**; ML lines nil, spread/total from handicap; 1 `start_ack` per connection |
| 5 | Snapshot parity | pregame 25/150/150/124, live 12/72/72/58 (unchanged) |
| 6 | Best-price table | −3 −110 beats −3.5 +100; +3.5 beats +3; over lower / under higher; same line → better price; decimal tiebreak; suspended, stale book and probability book never best; deterministic ties |
| 7 | Board | line move updates the same slot and records `Prev`; older poll never overwrites a newer stream entry; stale book recomputes best (and back); snapshot removes only older missing entries; slow subscriber is dropped while a fast one keeps every delta; `Seq` strictly increasing |
| 8 | Supervisor (fake book) | reconnect + re-snapshot per reconnect; `ErrResync` → immediate re-snapshot; heartbeat timeout cancels a silent stream; poll fallback while down; a panicking book does not stop another; live → degraded → live; snapshot failure → down → live; 10/10 runs under `-race` |
| 9 | Extensibility grep | no `draftkings`/`fanduel` outside `internal/books/**` and `cmd/**` (17 files checked; it caught two old tests, now fixed) |
| 10 | `go vet ./...` | clean |

### Why conn 1 has 6 `Removed` and conn 2 has 78

- **Conn 1:** all 6 are PIT @ NO (event 34118009, kicked off 13:32Z, in the
  4th quarter). The Steelers moneyline was pulled at 16:23:47 with no
  replacement (it was −2400). The markets were pulled at 16:32:28 and the
  event at 16:34:41. It is absent from `bootstrap-0002`.
- **Conn 2:** exactly 78 slots across the 13 next-week events (start
  2026-10-30 to 2026-11-03), pulled 17:01:13–17:01:43 and never re-added.

### Measurement corrections to the handoff

- 32 of the 654 line-move replacements had the remove **2.3–3.7 s before**
  the add (more than the 2 s selection grace). All were kickoff
  teardowns covered by the 15 s event tombstone, so none produced a
  `Removed`. Pairing each add with its nearest remove matters: the same
  selection ID comes back when a line moves back.
- FD soak conn 3 subscribed to only 36 markets, because the 17:00 discovery
  board had dropped the 7 games that had kicked off. This is why the app
  merges `/api/in-play` into the snapshot. (An earlier version of this note
  said live runs confirmed it with an in-play PHI @ KC game. That "game" was
  FanDuel esports; see the correction below. The merge itself is right.)

## Correction (2026-10-26): FanDuel esports counted as NFL

FanDuel files esports under the same sport type as the real NFL. Example
from its live board: "Philadelphia Eagles (OFFICER) @ Detroit Lions
(MEMENTO)" in competition "eFootball H2H GG League 4x4mins" (12787949), a
video-game match with 4-minute quarters. The NFL filter only checked the sport
type (6423), and the team matcher found "Eagles" and "Lions", so these showed
up as live NFL games at odd hours (PHI @ KC at 00:03Z, SF @ KC at 01:35Z, WAS
@ BUF at 03:49Z, PHI @ DET at 05:01Z).

Fixed: a FanDuel event must be in the league's real competition, by ID (NFL
12282733, NBA 10547864) or by the attached competition name ("NFL", "NBA").
The NBA filter already did this by ID; its 30 refused markets turn out to be
"eBasketball H2H GG League" esports. Test: `TestEsportsWithNFLTeamNamesRefused`.

So the FanDuel rows in the live-run tables below overstate it: the 18
"in-play slots" were 3 games, of which only KC @ SEA (6 slots) was real.
DraftKings and Polymarket were not affected (DK reads the NFL league
directly; Polymarket's NFL series has no esports).

## Live findings

- **FD bundle live fetch works** from this PC: `GET on.sportsbook.fanduel.ca/`
  → `static/js/main.ea04e06bc3f1ff8b.js` → client ID `NfxZUK…`. No bot wall
  was hit. The `research/captures/fd-main.js` fallback is still wired in.
- **FD discovery boards are CDN-cached** (`max-age=30, swr=60`). The snapshot
  now re-reads prices from SMP (no-cache). If SMP fails, `AsOf` is dated
  back 90 s so cached prices never overwrite fresher data.
- **This PC's clock is ~485 ms ahead of NTP** (w32tm and in-process SNTP
  agree). Raw `RecvAt − BookTS` therefore reads ~530 ms for DK.
  `boardctl` measures the offset (`internal/clocksync`) and prints corrected
  numbers.
- **Windows monotonic time is coarse.** `AppliedAt − RecvAt` reads 0 µs p50
  and ~1 ms p95 here. Real µs processing numbers have to come from the Linux
  host.

## Live runs (`boardctl`, Ontario home IP, Sunday 2026-10-25 evening ET)

Book→us is `RecvAt − BookTS`, corrected by an SNTP offset that is measured
once at startup. The offset read −394 to −483 ms across runs (Windows was
slewing the clock), so corrected book→us is only good to about ±40 ms. FD
frames carry no publish time. Processing is under Windows timer resolution
(see above).

**11 min spanning Sunday Night Football kickoff (00:13:50–00:24:50Z, KC @ SEA):**

| book | health | games | slots | in-play slots | deltas | reconnects | resyncs | book→us p50 / p95 |
|---|---|---|---|---|---|---|---|---|
| DraftKings | live throughout | 14 | 84 | 6 | 172 | 0 | 0 | −29 / 22 ms (n=98), i.e. inside the clock error |
| FanDuel | live throughout | 18 | 108 | 18 | 458 | 0 | 0 | n/a |
| Polymarket | live throughout | 32 | 188 | 0 | 864 | 0 | 0 | −7 / 0 ms (n=4096) |

DK's live kickoff teardown/re-add went through the state machine with no
resync. Polymarket shows 0 in-play slots here because its in-play flag only
comes from Gamma snapshots. In the next run it showed 6.

**120 s during the live game (00:25:09Z):**

| book | games | slots | in-play | deltas | reconnects | resyncs | book→us p50 / p95 |
|---|---|---|---|---|---|---|---|
| DraftKings | 14 | 84 | 6 | 138 | 0 | 0 | 25.0 / 50.9 ms (n=80) |
| FanDuel | 18 | 108 | 18 | 208 | 0 | 0 | n/a |
| Polymarket | 32 | 188 | 6 | 470 | 0 | 0 | 41.4 / 71.7 ms (n=4096) |

**Kill tests (`-kill-after 40s`):**
- DraftKings (pregame, 00:11Z): live → degraded 00:11:49.370 → live 00:11:50.282 (0.9 s). FD and PM kept updating.
- FanDuel (live game, 00:28Z): live → degraded 00:28:03.099 → live 00:28:04.213 (1.1 s), reconnecting
  from a 41 s-old snapshot because the discovery limit forbade a new one.
- Shutdown on timeout/Ctrl+C: 0–1 ms.

## Not done / not verified (as of 2026-10-26 05:15Z)

- Deployed at https://betstamp-odds.fly.dev (yyz) behind a static egress IP
  (209.71.100.151): Fly host IPs in 185.156.47.x were 403'd by DK and FD's
  homepage. Not yet watched for hours, or through a live game, from Fly.
- Live latency (book→screen, pregame vs live) not yet recorded with the
  current method; README not written; no GitHub repo.
- The FD `ErrAuth` → bundle-refresh path and app-key rotation are tested
  with fakes only; no real rejection has happened live.
