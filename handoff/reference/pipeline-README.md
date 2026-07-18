# NEAR Intents solver-edge pipeline

Two stages, run in order: **index** solver swap legs from `intents.near` settlements
on-chain, then **enrich** them with Hyperliquid (default) / Binance reference rates and
compute per-solver / per-pair edge.

## Prerequisites

- Python venv is at `.venv` (deps in `requirements.txt`).
- A NEAR RPC endpoint in `.env` as `NEAR_RPC_URL` (a paid/archival node is strongly
  recommended for a 24h window; falls back to public FastNEAR if unset). The `.env`
  is read at runtime — never pass the URL on the command line.

## Run the last 24 hours

From the project root:

```bash
cd "/Users/ysavchenko/Projects/Near Intents/block-explorer-stats"

# Stage 1 — index (parse on-chain settlements into solver legs)
.venv/bin/python ingest.py \
  --start "$(date -u -v-24H +%Y-%m-%dT%H:%M:%S)" \
  --end   "$(date -u -v+10M +%Y-%m-%dT%H:%M:%S)" \
  --legs all

# Stage 2 — enrich (reference rates + per-solver/per-pair edge)
.venv/bin/python enrich.py --min-notional 0
```

- `--start` = exactly 24h ago (UTC); `--end` = ~now (10 min ahead clamps cleanly to
  the chain head). Both timestamps are UTC.
- Stage 1 is the slow one: ~130k blocks for 24h, on the order of tens of minutes.
  Speed it up on a paid RPC with `--workers 28 --batch 120`.
- Stage 2 prints per-symbol progress and disk-caches every venue fetch, so it's fast
  and resumable (see below).

## Outputs (in `data/`)

| Stage | Files |
|-------|-------|
| index  | `legs.jsonl` (all solver legs, all classes), `swaps.csv`, `swaps_summary.csv` |
| enrich | `enriched_legs.csv` (per-leg edge), `agg_by_pair.csv`, `agg_by_solver.csv`, `agg_by_solver_pair.csv` |

`ingest.py` **overwrites** `legs.jsonl`/`swaps*.csv` each run. To keep a previous
dataset, run `make archive` (or `python archive_data.py`) first — it moves all current
top-level outputs into `data/archive_<last-data-date>/` (named for the newest timestamp
in the data; `_1`, `_2` appended on collision), leaving `state/`/`raw/`/`out/` in place.

## Useful flags

**`ingest.py`**
- `--legs interesting|all` — `all` keeps bridge/stable legs in `swaps.csv` too
  (default here). `legs.jsonl` always contains every class regardless.
- `--workers N` / `--batch N` — parallelism (default 14 / 80).

**`enrich.py`**
- `--min-notional N` — drop legs below `$N` (use `0` for full coverage).
- `--par-legs off|diff|all` — score same-asset (wrap/bridge) and stable↔stable legs
  against par = 1.0. `diff` (default) keeps those whose in/out amounts differ (a real
  spread); `all` keeps every one; `off` = price-bearing legs only.
- `--reference hl|binance|both` — reference venue for the edge. **`hl` (default)** =
  Hyperliquid, where solvers hedge; only HL is fetched, so `edge_bps_binance` /
  `binance_*` columns are blank. `both` also fetches Binance for comparison but keeps
  Hyperliquid primary (headline edge + notional). `binance` = the old Binance-only
  behaviour. Note: HL perps don't list every token (e.g. PEPE, SHIB, TON, OKB), so
  those get `no_reference` under `hl`/`both` — use `both` if you need Binance to fill
  that gap in a separate column.
- `--fresh-prices` — ignore the on-disk price cache (`data/state/price_cache/`) and
  refetch all mids. Normally leave off: an interrupted run **resumes from cache** — just
  re-run the same command.
- `--pair BTC` or `--pair ETH/SOL` — restrict to one pair.

## Notes

- Enrich reads `data/legs.jsonl` by default; re-run it freely without re-indexing.
- The price cache is keyed by symbol + window, so re-running the same window resumes
  instantly; a different window (new index run) refetches correctly.

## Analysis recipes

`same_asset_edges.py` profiles the same-asset legs where a solver kept an in/out
spread — cross-chain stablecoin bridging (USDT@eth -> USDT@tron etc.) that the edge
stats otherwise treat as par. It reads `data/legs.jsonl`, decodes each side's chain
from its asset id, and reports the kept spread as
`in_out_diff_bps = (amount_in / amount_out - 1) * 1e4` (> 0 = solver received more
than it gave). All views run off one pass; add flags to print extra breakdowns.

```bash
# Per-solver USDT/USDT + USDC/USDC summary; writes data/stable_same_asset_legs.csv
.venv/bin/python same_asset_edges.py

# One solver only (e.g. the Tron USDT specialist)
.venv/bin/python same_asset_edges.py --solver 6247d9c6...  --out data/solver_usdt.csv

# Chain-route breakdown (recv_chain -> give_chain), volumes + edge
.venv/bin/python same_asset_edges.py --routes

# Directional table for a hub chain: into vs out of Tron, per counterparty chain
.venv/bin/python same_asset_edges.py --hub tron

# Trade-size buckets for specific routes
.venv/bin/python same_asset_edges.py --buckets eth:tron tron:eth sol:tron
```

Exported CSV columns: `solver, pair, timestamp, tx, received, recv_chain,
recv_asset_id, recv_contract, given, give_chain, give_asset_id, give_contract,
amount_in, amount_out, in_out_diff_bps, notional_usd` (sorted by notional). `received`
/ `given` are the solver's own flow: a `X -> tron` leg is the maker **sending out**
Tron-side USDT. Other flags: `--symbols ETH` for a different same-asset symbol.

### Multi-hop / synthetic routes

`multihop_routes.py` groups a solver's legs within one settlement and, when it routed
through an intermediate (e.g. ETH -> USDC -> SOL, two legs), synthesizes the direct
"replacement" swap (ETH -> SOL). It nets each asset per `(tx, solver)`: a pass-through
intermediate nets ~0, leaving a single source (net-in) and sink (net-out). No indexer
change is needed — legs are already tagged with `tx`.

```bash
# Summary + data/multihop_routes.csv + data/multihop_synth_legs.jsonl
.venv/bin/python multihop_routes.py --min-notional 1     # drop sub-$1 dust routes

# Price the direct routes with the normal stage-2 machinery (synthetic legs are
# emitted in legs.jsonl schema, so enrich consumes them directly):
.venv/bin/python enrich.py --file data/multihop_synth_legs.jsonl --min-notional 1 \
    --out data/multihop_enriched.csv --agg-prefix data/multihop_agg
```

`multihop_routes.csv` columns include `path` (ETH@eth -> USDT@eth -> SOL@sol),
`synth_pair`, `source`/`sink` (+chain, +asset id), `intermediates`,
`stable_intermediate`, `amount_in`/`amount_out`, `native_rate`, `notional_usd`. Groups
with multiple sources/sinks (unrelated swaps batched, or fan-outs) are counted as
`complex` and not synthesized. `--intermediate-tol` (default 0.10) sets how close to
par a pass-through must net.
