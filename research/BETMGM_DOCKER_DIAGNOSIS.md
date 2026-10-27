# BetMGM Docker diagnosis

Investigated 2026-10-27. This supplements the initial implementation handoff and examines the subsequently added Go adapter.

## Conclusion

**Linux containers can retrieve this feed. A blanket Linux/Docker incompatibility is disproven.** The running app repeatedly received HTTP 403 from the NFL fixture route, but a diagnostic binary in the same container network successfully retrieved 15 NFL fixtures and completed the SignalR handshake.

The exact upstream rejection rule is not visible. Client/TLS characteristics and transient upstream classification are plausible factors. A 403, a Cloudflare response header, or a bot-management cookie alone does not establish the precise WAF rule, rule out rate-related policies, or prove the operating system caused the rejection.

## Update: applied to the running container, then to the project

After this investigation, the existing `betstamp-odds-local` container was
recreated with `GODEBUG=tlssecpmlkem=0` in its environment (the old container
was stopped and renamed `betstamp-odds-before-mgm-tls` first, as a rollback).
With that container in place, both halves of the feed worked: REST listings and
per-game reads, and the SignalR stream. That matches the full-adapter result
below and is the first run of the deployed container shape, not a probe.

The setting is no longer something typed at a prompt. It is now part of the
repository:

| Where | What |
|---|---|
| `app/Dockerfile` | `ENV GODEBUG=tlssecpmlkem=0` in the runtime stage, with the reason above it |
| `app/fly.toml` | the same key in `[env]`, so the deployed machine carries it explicitly |
| `app/scripts/recreate-local.ps1` | build, stop + rename the old container, run, then verify the environment reached the process and `/healthz` answers |
| `app/internal/archtest/deploy_test.go` | fails the test run if the image or the Fly config loses the setting |
| `README.md` §1, §4.5, §6.1, §11, §13 | what it is, how to run it, and the evidence's limits |

The caveats below stand unchanged: this is a tested workaround with a
correlational basis, and it applies to the whole process. The Fly deployment
was updated later the same day; the next section reports what it showed.

## Update: what happened on Fly (2026-10-27, ~21:31Z)

The deployment was then updated and the question above was answered: **the
setting is necessary here but not sufficient there.** The running Fly process
logs `"msg":"tls compatibility","godebug":"tlssecpmlkem=0"`, which is now
emitted at startup precisely so this cannot be inferred wrongly from
configuration. With that confirmed, BetMGM answered **403** to every listing
attempt at 21:31, 21:33 and 21:35Z on its 2-minute cycle, while the other three
books stayed live from the same machine and the same app-scoped egress IP
(209.71.100.151).

Joined with the home-IP result, the reading is a two-factor rule: at home the
ClientHello shape decided it, and from Fly the refusal survives the compatible
handshake, so there the address decides it. The same-minute comparison removes
the time-dependent confounder that limited every earlier sample: at 21:31–21:41Z
one client got 200 from a residential address and 403 from Fly.

A probe deployment (`fly.probe.toml`, `cmd/probe`, same region) separated the
address from the network. From a shared Fly address (185.156.47.129) BetMGM was
refused again — and so were DraftKings and FanDuel's homepage, which is the
known reason the app has a dedicated egress IP at all; Polymarket and FanDuel's
SMP path worked. So: two Fly addresses, two different binaries, one residential
address, and the refusal tracks the network rather than the client. Cloudflare's
score weighs address reputation alongside the fingerprint, and the handshake
setting cannot compensate once the address is bad enough.

Operationally that makes the answer on Fly the provider route (partner API, or
an address they allow-list), not further client tuning. A probe from the same
region with `cmd/probe` reproduces the address reading at any time; the deployed
app shows it whenever the book reports `down` with `betmgm: HTTP 403`. The
refusal is scoped to the `cds-api` REST path — the push endpoint still accepts
its upgrade — so a connected socket would keep streaming if one could be seeded
by another route.

## Controlled comparisons

One source program was compiled for Windows and Linux with Go 1.27.3. Each invocation made one NFL listing request and one WebSocket handshake, with the adapter's exact query fields and headers, no cookies, and normal certificate verification. The Linux probe used the running app's Docker network namespace and its distroless base image. Both platforms used the same public IPv4 address and reached the same REST server IP and Toronto edge location.

| UTC time | Client | Module compatibility / setting | REST | WebSocket |
|---|---|---|---|---|
| 21:13:37 | Windows | Research module `go 1.22` | 200, 15 fixtures | 101, SignalR `{}` |
| 21:13:38 | Linux | Research module `go 1.22` | 200, 15 fixtures | 101, SignalR `{}` |
| 21:14:18 | Windows | App-compatible `go 1.27.3` | 200, 15 fixtures | 101, SignalR `{}` |
| 21:14:20 | Linux | App-compatible `go 1.27.3` | 403, HTML | 101, SignalR `{}` |
| 21:14:35 | Linux | `go 1.27.3`, `GODEBUG=tlssecpmlkem=0` | 200, 15 fixtures | 101, SignalR `{}` |
| 21:14:36 | Linux | `go 1.27.3`, `GODEBUG=tlsmlkem=0` | 200, 15 fixtures | 101, SignalR `{}` |
| 21:14:55 | Linux | `go 1.27.3`, unchanged control | 200, 15 fixtures | 101, SignalR `{}` |
| 21:14:56 | Linux | `go 1.27.3`, `GODEBUG=tlssecpmlkem=0` | 200, 15 fixtures | 101, SignalR `{}` |

These are small sequential samples, not a randomized experiment or reliability measurement. The unchanged Linux control recovered, so the compatibility setting cannot be credited as a deterministic fix.

Both platforms negotiated HTTP/2 and TLS 1.3. Windows negotiated cipher suite 4866; Linux negotiated 4865. Those differences are observations, not proof of the blocking rule. Successful TLS followed by HTTP 403 rules out failed certificate validation as the immediate cause of those responses.

The running container's health showed zero BetMGM snapshots, frames, and updates, with repeated `betmgm: nfl fixtures: betmgm: HTTP 403`. It could not enter streaming because its initial REST seed failed. The successful independent WebSocket handshakes show that debugging SignalR framing is not the first repair step for that failure.

## Why the older research program behaved differently

The research module declares `go 1.22`, while the application declares `go 1.27.3`. Both inspected binaries were actually built with **Go 1.27.3**. The difference is not necessarily an older compiler.

Go chooses compatibility defaults partly from the module's `go` directive. Build metadata for the research binary included `tlsmlkem=0` and `tlssecpmlkem=0`, among other compatibility settings; the app binary did not.

The documented setting `tlssecpmlkem=0` removes the newer SecP256r1/ML-KEM and SecP384r1/ML-KEM hybrid groups from default TLS offerings. It retains the X25519/ML-KEM hybrid when otherwise enabled. This is narrower than `tlsmlkem=0`, which disables all default ML-KEM hybrids. Neither diagnostic disabled certificate verification, TLS 1.3, or hostname validation.

Sources:

- [Go compatibility defaults](https://go.dev/doc/godebug)
- [Go TLS configuration](https://pkg.go.dev/crypto/tls)
- [Cloudflare TLS fingerprints](https://developers.cloudflare.com/bots/additional-configurations/ja3-ja4-fingerprint/)

## Repair options and constraints

### Full-adapter verification

After the small probes, the existing application BetMGM adapter was compiled under the application's module, with no adapter changes, and exercised in the running app's Docker network namespace:

| Full adapter | Result |
|---|---|
| `GODEBUG=tlssecpmlkem=0` | Snapshot succeeded: 18 games, 626 quotes; skipped `unmapped_fixture:1`, `unmapped_market:1358`; stream reported ready, 27 heartbeats, 0 price updates during a 10-second listen |
| Unchanged defaults, subsequent control | Failed immediately: `betmgm: nfl fixtures: betmgm: HTTP 403` |

The successful run used default leagues and the adapter's per-fixture NFL prop reads. It did not report cached/unavailable prop fallbacks. The quote count includes all parsed quote families; a per-family count was not emitted. The skipped mapping counts indicate incomplete market coverage, not a network failure. Subscription readiness and heartbeat receipt are verified; actual price updates and long-term resnapshot reliability are not.

This makes the setting a **tested local workaround**, still not a deterministic root-cause proof. An earlier minimal unchanged control succeeded, so time-dependent rejection remains a confounder.

To try the existing image independently of the running service, use a different host port:

```powershell
docker run --rm --name betstamp-mgm-check -p 127.0.0.1:8081:8080 -e GODEBUG=tlssecpmlkem=0 betstamp-odds:local
```

Inspect `http://localhost:8081/healthz` and the logs across several resnapshots. This runs a second collector and therefore increases provider traffic; stop it after the validation window. Do not simultaneously add `tlsmlkem=0`, since the narrower setting already passed and preserves the X25519/ML-KEM hybrid. If `GODEBUG` already contains settings in another deployment, merge this key rather than overwriting those settings.

For the existing Docker deployment, the corresponding change is adding `GODEBUG=tlssecpmlkem=0` to its container environment and recreating the container after validation. A plain restart cannot change the container's configured environment. That recreation was not performed during this investigation; it was done separately the same day (see the Update above) and is now baked into the image and `fly.toml`.

1. **Keep Linux.** There is no evidence that moving to Windows containers or replacing distroless is required. This app is a static Go binary using Go's TLS implementation; installing curl/OpenSSL or Chromium does not change its existing HTTP client.
2. **Treat TLS compatibility as a candidate, not a solved incident.** A staging comparison can use `GODEBUG=tlssecpmlkem=0`. It affects the entire process, including other books. A narrower implementation would configure only BetMGM's HTTP transport with supported curves equivalent to that compatibility set, preserving certificate verification and X25519/ML-KEM. Adding such a permanent change on these few samples is what this note originally advised against; it was adopted the same day regardless (see the Update), so the open question is whether it should stay once the front end's behaviour is better understood.
3. **Validate the complete path.** A successful listing is insufficient: test every NFL prop fixture read, NBA listing, SignalR subscription acknowledgement, actual updates, and repeated resnapshots. Test from the intended deployment host as well as local Docker.
4. **Improve diagnostics.** Capture status, content type, `CF-Ray`, `Retry-After`, negotiated protocol, and a bounded non-sensitive error category. Never log cookie values or credentials. The current HTTP error exposes only status and retry-after, which is too little to attribute a refusal.
5. **Preserve data freshness during partial failures.** Cached prop reads must retain their original age and must not be stamped as fresh simply because a new snapshot succeeded elsewhere. Audit the existing 20-minute view reuse before relying on it as recovery.
6. **Use provider-supported access for dependable deployment.** If frontend refusals persist, request approved API access or provider-side investigation using timestamps and ray IDs. Browser or Windows success is not a production availability guarantee.

The README's earlier statement that BetMGM refuses Linux, and its definitive attribution of every refusal to Bot Management rather than other policies, overstated the available evidence. §13 has since been rewritten to name the ClientHello as the difference that was observed and to keep the correlation caveat; its older-program comparison now distinguishes compiler version from module compatibility defaults.

## Artifacts and operational impact

- Added reusable diagnostic source: `research/betmgm/cmd/platformprobe/main.go`.
- Diagnostic executables and alternate module files live under ignored `.gotmp` directories.
- The running app was not restarted, reconfigured, rebuilt, or redeployed by this investigation.
- No production adapter source was changed.
