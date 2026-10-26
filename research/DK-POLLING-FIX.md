# DraftKings polling fixed and compared with WebSocket

Verified October 25, 2026, approximately 10:09-10:13 America/Toronto.

## Cause established by controlled tests

The failures were Akamai edge responses. Changing only the HTTP User-Agent to the current browser's observed value made the public snapshot work in Node 24. No login, cookie, private token, TLS verification disablement or regional proxy was required. This isolates User-Agent-sensitive edge behavior in this environment; we cannot see Akamai's internal rule or guarantee it will remain unchanged.

| Request variation | Status |
|---|---|
| Default Node fetch headers | 403 |
| Ordinary product headers without browser User-Agent | 403 |
| Previously attempted Chrome 142 User-Agent | 403 |
| Current browser's Chrome 156 User-Agent alone | 200 |
| Full current browser headers, cookies omitted | 200 |
| Same headers with spaces encoded as `+` rather than `%20` | 200 |
| Original `/api/sportscontent/dkcaon/v1/leagues/88808` with working headers | 200 |

We also tested Node 22, HTTPS versus HTTP/2, and an anonymous cookie jar before finding the working header. Those changes alone did not resolve the failure. The earlier explanation that Node's TLS fingerprint was the main issue was not established; the controlled User-Agent comparison is stronger evidence.

Today's actual browser uses a CMS `controldata/league/leagueSubcategory/v1/markets` route. Capturing that request supplied the working headers and confirmed the current API response. The older league snapshot URL also works, so changing endpoints is not necessary for this collector.

The minimal implemented request is:

```js
const response = await fetch(
  'https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808',
  {
    headers: {
      Accept: 'application/json',
      'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36'
    },
    signal: AbortSignal.timeout(15000)
  }
);
```

`dk-http.cjs` validates successful JSON responses and required arrays. `DK_USER_AGENT` can override the observed default when the site changes. On this machine use the bundled Node 24 executable with `--use-system-ca`; system Node 20 lacks the needed trust configuration. Do not turn off certificate validation.

## Actual race against the WebSocket

Two-minute run: one-second polling, the original league snapshot URL, and JSON WebSocket subscriptions copied from `subscriptionPartials` in the successful response. The board contained 26 events, 78 markets and 156 selections, with PIT Steelers @ NO Saints live.

| Metric | Result |
|---|---:|
| Successful snapshot requests, including bootstrap | 120 |
| Polling errors | 0 |
| WebSocket update frames | 19 |
| Matched selection-price changes | 6 |
| WebSocket first | 5 |
| Polling first | 1 |
| Median WebSocket lead among matched changes | 2,214.5 ms |
| Largest polling lead | 108.3 ms |
| Unmatched price transitions | 0 |
| Unknown partial-update entities | 0 |

Four selection changes belonged to the same burst and reached polling about 2.215 seconds after push. Another reached push 36.6 ms before polling. One reached polling 108.3 ms before push. **These are six correlated selection changes in one live game, not six independent events or enough data for a general latency claim.** No pregame price transitions occurred.

Both transports were timed in the same process with `performance.now()`. This avoids the cross-clock issue encountered previously. Match identity was market ID + outcome side + line + American odds; matched source selection IDs agreed in all six cases. Full snapshot caches and partial WebSocket entity updates were kept separate. A 15-second matching window handles timing differences; recurrent identical prices can still make this method ambiguous in larger captures.

The snapshot response advertises `Cache-Control: public,max-age=1`. Poll cadence, cache behavior and upstream publication all contribute to the observed difference. A lead longer than one second is possible; the test does not isolate those contributions or establish source-engine latency. The socket is not guaranteed to win every change.

## Run again

Node 24+ on PATH:

```powershell
npm run snapshot:dk
npm run compare:dk
# Use the directory printed by compare:dk:
node analyze-dk-race.cjs captures/dk-race-1792937452944
```

`CAPTURE_MS` controls duration (default 120000); `POLL_MS` controls polling (default 1000). The finite benchmark stops polling on 403/429. It is a diagnostic, not a production service: production needs reconnect/resync, rate-limit handling and session/config health monitoring.

Evidence:

- `captures/dk-ablation-summary.json`: request variation results.
- `captures/dk-browser-request.json`: observed non-cookie, non-authorization request headers.
- `captures/dk-browser-snapshot.json`: browser's successful snapshot.
- `captures/dk-race-1792937452944/raw.jsonl`: every snapshot and WebSocket message with receipt times.
- `captures/dk-race-1792937452944/race.json`: matching results.
- `captures/dk-race-1792937452944/transitions.json`: independently detected transitions per transport.

Raw captures remain excluded from Git. The portable Node 22 diagnostic runtime is also under `captures/` and does not change the installed Node version.
