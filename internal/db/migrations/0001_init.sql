-- one row per execute_intents transaction
CREATE TABLE settlements (
  tx_hash      text PRIMARY KEY,
  block_height bigint NOT NULL,
  block_ts     timestamptz NOT NULL,
  relayer      text,
  succeeded    boolean NOT NULL,
  n_msgs       int NOT NULL DEFAULT 0
);
CREATE INDEX settlements_block_ts_idx ON settlements (block_ts);

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
  pair           text,                   -- directed FROM_BASE/TO_BASE (received/given)
  side           text,                   -- buy_base|sell_base (solver's side)
  base_symbol    text,
  quote_symbol   text,
  native_rate    double precision,
  hl_rate        double precision,
  binance_rate   double precision,
  edge_bps_hl    double precision,
  edge_bps_binance double precision,
  notional_usd   double precision,
  price_status   text NOT NULL DEFAULT 'pending',  -- pending|ok|no_reference
  UNIQUE (tx_hash, seq)
);
CREATE INDEX legs_block_ts_idx ON legs (block_ts);
CREATE INDEX legs_solver_ts_idx ON legs (solver, block_ts);
CREATE INDEX legs_assets_ts_idx ON legs (from_asset, to_asset, block_ts);
CREATE INDEX legs_pair_ts_idx ON legs (pair, block_ts);
CREATE INDEX legs_class_ts_idx ON legs (leg_class, block_ts);
CREATE INDEX legs_pending_idx ON legs (block_ts) WHERE price_status = 'pending';

-- asset registry (refreshed periodically from the 1Click tokens API)
CREATE TABLE assets (
  asset_id   text PRIMARY KEY,           -- nep141:... / nep245:...
  symbol     text, chain text, contract text,
  decimals   int, is_stable boolean NOT NULL DEFAULT false,
  price_usd_snapshot double precision,   -- rough mark for approx notional
  first_seen timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
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
  mid       double precision,            -- null = fetched, venue has no data
  PRIMARY KEY (venue, symbol, minute_ms)
);

CREATE TABLE indexer_state (key text PRIMARY KEY, value jsonb NOT NULL);

-- Curated seed solvers (mined from a prior backfill, current as of 2026-07).
INSERT INTO solvers (account_id, is_seed) VALUES
  ('crux-solver.near', true),
  ('solver-priv-liq.near', true),
  ('solver-priv-liq-2.near', true),
  ('solver-multichain-asset.near', true),
  ('solver-multichain-asset-escrow.near', true),
  ('solver-multichain-asset-3.near', true),
  ('solver-ref.near', true),
  ('l2solver.near', true),
  ('nocturnallabs.near', true),
  ('kipseli.near', true),
  ('sodax-solver.near', true),
  ('defuse-relay.near', true),
  ('pablow-stables.near', true),
  ('0x2e30c32738a28833e3d85189c04895c25ee54cb5', true),
  ('6247d9c671ca6a80cf2c75e26edb8cd75ebf00fcd3c56c39107d6ca20c9cfb29', true),
  ('433d1f6e5452c9f6087af460d1c6b799587132caabeb2477a0ad37e3e0b88d0c', true),
  ('4fdbc608fd712338c7680c224f249360c855987cc25ec4501a9a47ef4c789264', true);
