# Handoff: NEAR Intents live indexer

Self-contained seed for the new project — a continuous Go indexer + Postgres +
web UI for real-time NEAR Intents swap stats. Copy this whole folder into the
new repo; nothing here references the old project's paths.

**Start with [`LIVE_INDEXER_SEED.md`](LIVE_INDEXER_SEED.md)** — the full
architecture: decisions, block source (neardata.xyz) with verified semantics,
service shape, Postgres schema, API surface, UI pages, deployment, milestones.

## What's in here

### `reference/` — the Python pipeline being replaced (the **oracle**)

The parsing and enrichment logic is settled and verified; the Go port must
reproduce it exactly. Any divergence is a Go bug until proven otherwise.

| File | Why it matters |
|---|---|
| `lib/settle.py` | **The core.** `execute_intents` args → signed messages → solver legs. Payload encodings (NEP-413 dict / EIP-712 string), one-leg-per-token_diff rule (never net per account), dominant-pair selection, withdrawal detection. |
| `lib/solvers.py` | Solver identification: curated seed set + frequency promotion (≥5 settlements) + withdrawal fallback. Seed list is current as of 2026-07. |
| `lib/assets.py` | Asset registry from the 1Click token list (`1click.chaindefuser.com/v0/tokens`): asset-id forms, decimals, chain extraction, stables set, venue symbol aliases, pair classification. |
| `lib/prices.py` | Venue fetchers: Binance 1m klines, Hyperliquid 1m perp candles; per-minute mid = (high+low)/2. |
| `enrich.py` | Edge math: pair orientation (quote-per-base), USD triangulation, **sign-by-side** (positive = solver beat venue mid), aggregation columns. |
| `ingest.py` | The old RPC block-walk (being replaced) — but also the leg emission schema (`legs.jsonl` fields) and approx-USD logic. |
| `same_asset_edges.py` | Bridge-flow analysis (chain routes, hub tables, size buckets) → becomes SQL/API. |
| `multihop_routes.py` | Multi-hop grouping by (tx, solver), per-asset netting, synthetic direct-swap emission → becomes SQL/API. |
| `lib/rpc.py`, `lib/discover.py`, `lib/config.py` | RPC fallback client, block-walk discovery, env conventions (secrets from env, never argv). |
| `pipeline-README.md` | The old project's README — CLI flags, output files, analysis recipes. |

### `fixtures/` — Go parser test fixtures

Real raw `execute_intents` transactions (base64 args intact) + final status,
with `expected_legs.json` as golden output from the Python parser. See
`fixtures/MANIFEST.md` for what each covers and known coverage gaps
(no eip712/webauthn or multi_asset example in the window — handle per notes).

### `samples/` — output shapes

- `legs.sample.jsonl` — 200 rows of the per-leg record (the `legs` table's spirit)
- `enriched_legs.sample.csv` — post-enrichment per-leg columns
- `agg_by_pair.csv`, `agg_by_solver.csv`, `agg_by_solver_pair.csv` — full small
  aggregates from a real 8h window (the API's target payloads)
- `swaps_summary.sample.csv` — pair summary shape

## Ground facts (verified 2026-07-18)

- Volumes: ~43k legs/day, ~20k settlements/day, ~100 distinct assets, ~28 solvers.
- NEAR: ~600 ms blocks (~145k/day), finality after 2 blocks (~1.2 s).
- neardata.xyz: finalized-only, long-polls next height, `null` for skipped
  heights, 180 req/min/IP free.
- External cross-check for volumes: `dune.com/near/near-intents`.
