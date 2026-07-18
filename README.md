# intents-explorer

Continuously running indexer + Postgres + web UI for real-time **NEAR Intents**
swap statistics. One Go binary runs three loops:

1. **Follower** — tails finalized NEAR blocks from
   [neardata.xyz](https://mainnet.neardata.xyz) (one GET per block, all shards +
   execution outcomes included), parses every `execute_intents` settlement on
   `intents.near` into solver swap legs, and writes them to Postgres. Legs land
   in the DB ~2 s after finality.
2. **Enricher** — every 30 s prices new legs against venue 1-minute mids
   (Hyperliquid perps by default, optionally Binance), computes the signed
   solver edge in bps, and stores USD notionals. Minute mids are cached in the
   `price_marks` table.
3. **HTTP API + SPA** — JSON API and an embedded React UI (Overview, Pairs,
   Solvers, Bridges, Multi-hop, Status) behind a single basic-auth password.

The parsing and enrichment logic is a line-for-line port of the verified Python
pipeline in [`handoff/reference/`](handoff/reference/) (the **oracle**). Golden
fixtures from real transactions guard the port: `go test ./internal/intents/`
must reproduce the oracle's legs exactly. Architecture and decisions:
[`handoff/LIVE_INDEXER_SEED.md`](handoff/LIVE_INDEXER_SEED.md).

## Semantics (inherited from the oracle)

- **One `token_diff` = one leg**, from the solver's side — never netted per
  account. Dominant positive entry = received, dominant negative = given;
  `multi_asset` flags diffs with >2 assets.
- **Solver identification**: curated seed set → frequency promotion (≥5
  distinct settlements, persisted in the `solvers` table) → withdrawal-intent
  fallback → `unknown` (surfaced, never guessed).
- **Leg classes**: `interesting` (price-bearing) | `stable_stable` |
  `same_asset` (wrap/bridge) | `unknown`. Par legs (stable↔stable, same-asset)
  are benchmarked against a fair rate of 1.0.
- **Signed edge**: `edge_bps > 0` always means the solver transacted better
  than the venue mid. Rate convention is quote-per-base with stables (then
  majors) as the quote.
- Only **successful** settlements produce legs; failures are counted in
  `settlements` with `succeeded=false`. Amounts are stored as exact numerics —
  never floats.

## Run locally

Prereqs: Go 1.26+, Node 22+, Docker (for Postgres).

```bash
make db-up        # local Postgres 16 on :5433 (docker)
make build        # npm build + go build -tags ui -> bin/intents-explorer
make run          # follower + enricher + UI on http://localhost:8080
```

No basic auth locally (auth enables itself when `BASIC_AUTH_PASS` is set).
The indexer starts at the current chain head — history accumulates from first
launch; there is no backfill by design.

Frontend dev with hot reload: `cd web && npm run dev` (proxies `/api` to
`:8080`).

Tests: `make test` (includes the golden-fixture oracle tests; no network or DB
needed).

## Configuration (env vars only — never argv)

| Var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | — (required) | Postgres connection string |
| `PORT` | `8080` | HTTP port |
| `BASIC_AUTH_USER` / `BASIC_AUTH_PASS` | `admin` / empty | Auth for API+UI; empty password disables |
| `NEARDATA_URL` | `https://mainnet.neardata.xyz` | Block server |
| `NEARDATA_API_KEY` | empty | Optional key (free tier: 180 req/min) |
| `PRICE_VENUES` | `hl` | `hl`, `binance`, or `hl,binance` (first = primary) |
| `ENRICH_EVERY` | `30s` | Enricher cadence |
| `TOKENS_URL` | 1Click tokens API | Asset registry source |
| `TOKENS_REFRESH` | `1h` | Registry refresh cadence |
| `START_OVERLAP_BLOCKS` | `30` | Re-scan overlap on restart (writes are idempotent) |

## Deploy (Render)

`render.yaml` defines the whole stack: one Docker web service + Postgres.
Create a Blueprint from the repo, then set `BASIC_AUTH_PASS` (and optionally
`NEARDATA_API_KEY`) in the dashboard. Health check: `/healthz` (unauthed);
`/api/status` reports chain head vs indexed head — alert if `lag_blocks`
exceeds ~120 for 5 minutes.

## API

All endpoints take `?from=&to=` (RFC3339 or epoch seconds; default last 24 h)
and sit behind basic auth. Aggregate endpoints also take
`par=diff|all|off` (default `diff`: par legs only when the in/out amounts
differ — the oracle's convention) and `min_notional`.

```
GET /api/status                    indexer lag, pricing backlog, counters
GET /api/summary                   headline tiles + hourly volume series
GET /api/pairs                     per-pair aggregates (agg_by_pair)
GET /api/solvers                   per-solver aggregates (agg_by_solver)
GET /api/solvers/{id}              one solver: per-pair breakdown (agg_by_solver_pair)
GET /api/legs?pair=&solver=&class= leg detail (paginated: limit/offset)
GET /api/daily?group=pair|token|solver&bucket=day|hour   time series
GET /api/routes/same-asset?symbols=&hub=&route=          bridge flows
GET /api/routes/multihop?tol=&min_notional=              synthesized direct swaps
```

## Validation

Parallel-run against the Python oracle over a shared window and diff `legs`
rows on `tx_hash + seq` (identical counts, assets, raw amounts, solver
attribution, classes). Cross-check daily volumes against
[dune.com/near/near-intents](https://dune.com/near/near-intents) — an
independent pipeline on an independent data source.
