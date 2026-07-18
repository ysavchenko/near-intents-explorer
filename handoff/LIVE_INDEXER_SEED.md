# NEAR Intents live swap-stats platform — project seed

Seed document for a new project: a continuously running indexer + database + web UI
for real-time NEAR Intents swap statistics. It replaces the batch Python pipeline in
`block-explorer-stats` (kept as the reference implementation / parsing oracle).

## Decisions already made

| Decision | Choice |
|---|---|
| Scope | Live tail-following only — no historical backfill; history accumulates from launch day |
| Audience | Personal/internal tool (single basic-auth password, no multi-user auth) |
| Backend | Go |
| Database | Render Postgres (same platform as the service) |
| Frontend | React + TypeScript SPA (Vite, Tailwind, TanStack Query, Recharts), embedded into the Go binary |
| Hosting | Render — one Web Service runs indexer + enricher + API in a single Go binary |

## 1. Why the current pipeline is slow, and the fix

The Python pipeline walks the window via JSON-RPC: one `block` call per height, one
`chunk` call per shard (NEAR runs multiple shards, so ~6–9 calls/block), plus one `tx`
status call per settlement. NEAR has produced blocks every ~600 ms since May 2025
(measured ~592 ms in July 2026), so a 24 h window is ~145k blocks ⇒ on the order of a
million HTTP round-trips — hence 4–6 hours.

The fix is to stop using JSON-RPC for bulk reads. **FASTNEAR's neardata block server
(`https://mainnet.neardata.xyz/v0/block/{height}`) returns the entire block — all
shards, all chunk transactions, all receipt execution outcomes — in one GET**, in the
same "streamer message" JSON shape as NEAR Lake (with `tx_hash` conveniently added).
That collapses ~10 RPC calls per block into 1 GET and removes the per-tx status calls
entirely (execution outcomes are included).

Verified server semantics (from neardata.xyz, 2026-07):

- Serves **finalized blocks only** — no reorg handling needed downstream.
- Requesting the **next unproduced height long-polls** until the block is finalized,
  then returns it — the follower is just `GET cursor+1` in a loop, no poll timer.
- A **skipped height returns `null`** (vs. blocking for a future height) — advance
  the cursor past it; no ambiguity between "skipped" and "not yet produced".
- Free tier: **180 req/min/IP**; an `?apiKey` upgrade path exists. At one block per
  ~600 ms NEAR produces ~100 blocks/min, so tail-following fits the free tier with
  headroom (catch-up after downtime is briefly rate-limited but self-heals).
- `GET /v0/last_block/final` returns the chain head — use it in `/api/status` to
  compute indexer lag without touching RPC.

### Alternatives considered (the "subgraphs for NEAR" question)

Surveyed 2026-07; verdicts for indexing one contract (`intents.near`) with full
function-call args:

- **SQD (Subsquid)** — eliminated: no NEAR dataset exists (EVM/Solana/Substrate/
  Bitcoin/Tron/Hyperliquid only), and after the Rezolve AI acquisition (Oct 2025)
  NEAR support looks unlikely to appear.
- **The Graph subgraphs** — technically possible but weak: NEAR support is still
  *beta*, served only by the single centralized "upgrade indexer" (no decentralized
  redundancy). Receipt handlers do expose FunctionCall args/logs, but mappings are
  AssemblyScript and the nested signed-payload parsing (NEP-413/EIP-712 JSON inside
  base64 args) would be painful there.
- **Substreams (StreamingFast/Pinax)** — the one viable managed alternative: NEAR
  mainnet is first-class (endpoints from both StreamingFast and Pinax), the block
  model carries full args + execution outcomes, there's an official Postgres sink and
  Go gRPC consumption, and Pinax's free tier ($25/mo usage credit) covers a from-now
  stream. But there is no off-the-shelf NEAR filter module — it needs a custom Rust
  map module — and all the real parsing logic still has to live in our Go code.
  Adding a Rust build + a third-party streaming dependency to save ~200 lines of
  follower loop isn't worth it.
- **NEAR Lake S3** — no longer an option: **deprecated 2026-03-24, no new blocks
  indexed**. neardata.xyz is its official drop-in replacement.
- **Running our own indexer node** — nearcore's indexer framework is Rust-only and
  embeds a full neard node: 32 GB RAM / 4 TB NVMe class hardware, ~$500+/mo in the
  cloud. Absurd overkill for one contract's transactions.
- **Hosted explorer APIs** — NearBlocks serves full decoded `execute_intents` args
  (free tier: 6 calls/min — unusable for streaming; no execution logs at all);
  Pikespeak is alive but pricing-opaque; Pagoda Enhanced API and Flipside are dead.
  None fit continuous tailing.
- **Dune** — has full NEAR history with parsed function-call args *and* logs, plus an
  official NEAR Intents dashboard (`dune.com/near/near-intents`). Wrong shape for a
  live pipeline, but **valuable as a free external cross-check** of our volume/swap
  counts (see §9).

**Verdict: self-run Go follower on neardata.xyz, with Substreams as the documented
plan-B if neardata ever degrades** (and raw JSON-RPC via the paid node as the
emergency fallback — same data, just ~10× the requests).

## 2. Service shape: continuously running worker, not a cron

**Recommendation: one always-on Go process** (Render Web Service), with three loops:

1. **Follower** — polls neardata for `cursor+1`, parses, writes to Postgres, advances
   the cursor (a DB row). Sub-minute freshness, no overlap/locking concerns.
2. **Enricher** — every minute, prices legs that have no reference rate yet
   (venue candles trail real time by ~1–2 min).
3. **HTTP API + static UI** — serves the SPA and JSON endpoints.

Why not a 5-minute Render cron job: it *would* work — 5 min of chain is only a few
hundred blocks, well under a minute of processing via neardata — but you gain nothing.
A cron run pays cold-start each time, needs a DB lock against overlapping runs, caps
freshness at the interval, and Render bills cron by runtime anyway. The always-on
worker is simpler and the natural fit for "real-time".

### Follower details

- **Cursor**: `indexer_state(key, value)` row `last_final_height`. On restart, resume
  from `last_final_height − small overlap (≈30 blocks)`; all writes are idempotent
  (unique keys on tx hash / leg identity), so reprocessing is safe.
- **Skipped heights**: NEAR skips some heights; a 404/missing block at `h` while
  `h+1` exists is normal — advance past it.
- **Finality**: index **final** blocks only — which is all `/v0/block/{height}`
  serves anyway. NEAR finalizes after 2 consecutive blocks (~1.2 s); a final block
  cannot revert without ≥⅓ of stake committing a slashable offence. No reorg
  handling needed, and end-to-end freshness is still ~2 s from block production.
- **Success status**: the `execute_intents` function-call receipt executes 1–2 blocks
  after the tx is included. Maintain a small in-memory pending map
  `tx_hash → expected receipt_id` (from the tx outcome's `receipt_ids`); resolve when
  the receipt's execution outcome appears in a later block's
  `receipt_execution_outcomes`. Persist unresolved entries with the cursor overlap so
  restarts can't lose them. Only successful settlements produce legs (failed ones are
  counted in `settlements` with `succeeded=false`).

## 3. Parsing (port of the proven Python logic)

Port from the Python sources in `reference/` (this handoff folder) — the logic is
settled and verified; keep semantics identical so results diff cleanly against the
Python oracle. Golden test fixtures with expected outputs are in `fixtures/`.

- `lib/settle.py` — decode `execute_intents` base64 args → signed messages, tolerating
  **NEP-413** (`payload.message` is a JSON string) and **EIP-712** (`payload` is the
  message JSON string); split at the **token_diff level** (one leg per token_diff —
  never net per account); dominant positive entry = received, dominant negative =
  given; `multi_asset` flag when a diff has >2 assets.
- `lib/solvers.py` — solver identification: seed set of known makers first; if none
  present, frequency promotion (≥5 distinct settlements); withdrawal-intent fallback;
  else `unknown`. **The frequency state moves into the DB** (`solvers` table) so it
  survives restarts and keeps learning.
- Leg classification: `interesting | stable_stable | same_asset | unknown` from the
  asset registry.
- Amounts are u128 raw integers — store as Postgres `numeric`, handle in Go with
  `big.Int` / `shopspring/decimal`. Never float for amounts.

## 4. Enrichment (reference rates + solver edge)

Same conventions as `reference/enrich.py` + `reference/lib/prices.py`:

- Venues: **Hyperliquid 1m perp candles** (primary — where solvers hedge) and
  optionally Binance 1m klines. Per-minute mid = `(high+low)/2`. Stables pinned to 1.0.
- Rate convention: quote-per-base. `native_rate = quote_amount / base_amount`;
  `venue_rate = venue_usd(base)/venue_usd(quote)` (USD triangulation for
  crypto/crypto pairs).
- **Signed edge**: `edge_bps = (native/venue − 1)·1e4` if solver sold base, negated if
  it bought base ⇒ positive always means the solver beat the venue mid.
- Minute mids land in a `price_marks` table (venue, symbol, minute) — the DB replaces
  the on-disk price cache; enrichment is fully resumable and re-runnable in SQL.
- Legs whose asset has no venue listing get `price_status='no_reference'` (kept,
  excluded from edge averages). New/unknown symbols simply stay unpriced, never fatal.

## 5. Postgres schema (first cut)

```sql
-- one row per execute_intents transaction
CREATE TABLE settlements (
  tx_hash      text PRIMARY KEY,
  block_height bigint NOT NULL,
  block_ts     timestamptz NOT NULL,
  relayer      text,
  succeeded    boolean NOT NULL,
  n_msgs       int NOT NULL DEFAULT 0
);

-- one row per solver token_diff (the unit of analysis)
CREATE TABLE legs (
  id             bigserial PRIMARY KEY,
  tx_hash        text NOT NULL REFERENCES settlements(tx_hash),
  seq            int  NOT NULL,          -- position within the settlement
  block_ts       timestamptz NOT NULL,
  block_height   bigint NOT NULL,
  solver         text NOT NULL,
  leg_class      text NOT NULL,          -- interesting|stable_stable|same_asset|unknown
  multi_asset    boolean NOT NULL DEFAULT false,
  from_asset     text NOT NULL,          -- what the solver receives
  to_asset       text NOT NULL,          -- what the solver gives
  amount_in_raw  numeric NOT NULL,
  amount_out_raw numeric NOT NULL,
  amount_in      numeric,                -- decimal-adjusted (null if unknown asset)
  amount_out     numeric,
  -- enrichment (null until priced)
  pair           text,                   -- oriented BASE/QUOTE
  side           text,                   -- buy_base|sell_base (solver's side)
  native_rate    numeric,
  hl_rate        numeric,
  binance_rate   numeric,
  edge_bps_hl    numeric,
  edge_bps_binance numeric,
  notional_usd   numeric,
  price_status   text NOT NULL DEFAULT 'pending',  -- pending|ok|no_reference
  UNIQUE (tx_hash, seq)
);
CREATE INDEX ON legs (block_ts);
CREATE INDEX ON legs (solver, block_ts);
CREATE INDEX ON legs (from_asset, to_asset, block_ts);
CREATE INDEX ON legs (pair, block_ts);
CREATE INDEX ON legs (price_status) WHERE price_status = 'pending';

-- asset registry (refreshed periodically from the tokens API)
CREATE TABLE assets (
  asset_id   text PRIMARY KEY,           -- nep141:... / nep245:...
  symbol     text, chain text, contract text,
  decimals   int, is_stable boolean NOT NULL DEFAULT false,
  price_usd_snapshot numeric,            -- rough mark for approx notional
  first_seen timestamptz NOT NULL DEFAULT now()
);

-- persistent solver identification state
CREATE TABLE solvers (
  account_id     text PRIMARY KEY,
  is_seed        boolean NOT NULL DEFAULT false,
  n_settlements  bigint NOT NULL DEFAULT 0,
  first_seen     timestamptz NOT NULL DEFAULT now()
);

-- venue minute mids (replaces the on-disk price cache)
CREATE TABLE price_marks (
  venue     text NOT NULL,               -- hl|binance
  symbol    text NOT NULL,
  minute_ms bigint NOT NULL,
  mid       numeric,                     -- null = fetched, venue has no data
  PRIMARY KEY (venue, symbol, minute_ms)
);

CREATE TABLE indexer_state (key text PRIMARY KEY, value jsonb NOT NULL);
```

Sizing (from the current dataset): ~43k legs/day, ~20k settlements/day, ~100 assets.
One year ≈ 15M legs ≈ single-digit GB with indexes — plain Postgres with the indexes
above needs no rollup tables to start. Add materialized daily rollups only if the
daily-grouped queries get slow (they won't for a long time).

**Raw payloads**: don't store full raw args in Postgres by default (they're bulky).
Optional debug switch writes raw settlement JSON to disk/object storage for the last
N days. The chain remains the source of truth; anything can be re-derived.

## 6. API (Go)

Single binary, `net/http` + chi. All endpoints take `?from=&to=` time bounds
(default: last 24 h) and return JSON shaped for the UI. Basic-auth middleware on
everything.

```
GET /api/status                 indexer head vs chain head, pricing backlog, uptime
GET /api/summary                headline: volume, settlements, legs, active solvers
GET /api/pairs                  per-pair agg: n_legs, volume, vw/mean edge (agg_by_pair)
GET /api/pairs/{pair}/legs      leg detail for one pair (paginated)
GET /api/solvers                per-solver agg (agg_by_solver)
GET /api/solvers/{id}           one solver: pairs breakdown (agg_by_solver_pair) + legs
GET /api/daily?group=pair|token|solver   daily time series for charts
GET /api/routes/same-asset      bridge flows: chain→chain routes, hub view, size buckets
GET /api/routes/multihop        synthesized direct swaps from multi-leg routes
```

The analysis scripts map onto SQL views:

- `same_asset_edges.py` → SQL over `legs WHERE leg_class='same_asset'` with chain
  decoded from `assets.chain`; `in_out_diff_bps = (amount_in/amount_out − 1)·1e4`.
- `multihop_routes.py` → group legs by `(tx_hash, solver)`, net per asset, synthesize
  source→sink when intermediates net to ~0 (tolerance 0.10). Do this in Go at query
  time (or a small view); groups with multiple sources/sinks are `complex`, skipped.

## 7. Frontend

Vite + React + TS + Tailwind + TanStack Query + Recharts, built into `dist/` and
embedded in the Go binary via `embed.FS` — one deployable, no separate static host.

Pages (mirroring today's script outputs):

1. **Overview** — 24 h headline tiles, volume-by-hour chart, top pairs, top solvers.
2. **Pairs** — sortable agg table → pair detail with legs, edge distribution, daily series.
3. **Solvers** — agg table → solver detail: per-pair edge, volume share, activity timeline.
4. **Bridges** (same-asset) — chain→chain matrix, hub view (e.g. Tron), size buckets.
5. **Multi-hop** — synthesized routes table.
6. **Status** — indexer lag, pricing backlog, error counters.

## 8. Deployment (Render)

- **Web Service** (Go binary): starter instance is enough. Env: `DATABASE_URL`,
  `BASIC_AUTH_USER/PASS`, `NEARDATA_URL`, optional venue API overrides. Secrets via
  Render env vars only — never on command lines.
- **Render Postgres**: basic plan; enable daily backups.
- `render.yaml` blueprint in-repo so the whole stack is reproducible.
- Health: `/api/status` doubles as the health check; alert if `chain_head −
  indexed_head` exceeds ~120 blocks for 5 min.

## 9. Validation plan

Run the Go follower for a day in parallel with the Python pipeline over the same
window, then diff `legs` rows against `legs.jsonl` (join on `tx_hash + seq`):
identical leg count, assets, raw amounts, solver attribution, classes. The Python
project is the oracle; any divergence is a Go port bug until proven otherwise.

As a secondary sanity check, compare daily settlement counts and volume against the
official Dune dashboard (`dune.com/near/near-intents`) — independent pipeline,
independent data source; large discrepancies mean a coverage bug on one side.

## 10. Milestones

1. **Skeleton**: repo, Go module, Postgres schema + migrations (goose/atlas), config.
2. **Follower**: neardata polling, settlement/leg parsing, cursor, idempotent writes.
   *Exit: legs appear in DB within ~1 min of on-chain settlement.*
3. **Validation**: parallel-run diff vs Python oracle (§9).
4. **Enricher**: price_marks loop + leg pricing + edge. *Exit: enriched_legs.csv
   numbers reproduced for a shared window.*
5. **API + UI**: status/summary/pairs/solvers first, then bridges + multi-hop.
6. **Deploy**: render.yaml, basic auth, alerting.

## Open questions

- Asset registry source & refresh cadence (today: 1Click tokens snapshot).
- Retention for the optional raw-payload debug dump.
