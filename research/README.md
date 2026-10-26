# Research and validation

The submitted application is in [`../app/`](../app/). See the [submission README](../README.md) for its current architecture, setup, tests and limitations.

This directory preserves the October 24–25, 2026 experiments that informed the implementation. Results describe the captured samples and test conditions; they are not guarantees about current provider behavior. Statements about unfinished work in historical reports refer to those experiments, not the submitted app.

## Evidence and findings

| Report | What it explains |
|---|---|
| [Transport comparison](TRANSPORT-SPEED.md) | REST versus WebSocket observations and timing methodology |
| [DraftKings polling investigation](DK-POLLING-FIX.md) | Controlled checks of snapshot request behavior |
| [FanDuel comparison](FAN%20DUEL%20COMPARISON.md) | Snapshot and streaming price comparisons |
| [NBA market coverage](NBA-MARKETS-REPORT.md) | Market discovery and cross-book coverage |
| [NBA WebSocket comparison](NBA-WEBSOCKET-COMPARISON.md) | Streaming observations for NBA markets |
| [Soak-test results](go/SOAK-1H-RESULTS.md) | Connection uptime, reconciliation and recovery observations |
| [Normalization results](normalizer/REPORT.md) | Quote identity, exact-line overlap and price differences |
| [Normalization design](normalizer/DESIGN.md) | Experimental taxonomy and matching decisions |
| [Original investigation](INVESTIGATION.md) | Historical protocol notes and Node probe instructions |

## Reproducing the experiments

- **Node probes:** the `.cjs` scripts and [package manifest](package.json) contain discovery, snapshot and transport comparisons. Run them from `research/`; see the [original investigation](INVESTIGATION.md) for prerequisites and commands.
- **Go transport prototype:** [`go/`](go/README.md) is a separate experimental module with verification and soak tooling.
- **Normalization prototype:** [`normalizer/`](normalizer/README.md) is a separate replay-based experiment covering a broader market taxonomy.

Both Go modules are historical prototypes. The submitted implementation and its regression tests live in `app/`.

Raw captures, downloaded frontend bundles and experiment outputs are excluded from Git. Some experiments require regenerating those inputs, Ontario endpoint access, and current provider configuration. The app's committed replay fixtures are in [`../app/testdata/`](../app/testdata/).
