-- Solver deposit/withdrawal/transfer history: NEP-245 mint/burn events on
-- intents.near plus `transfer` intents, recorded for known solvers only
-- (no backfill; rows start at deploy).
CREATE TABLE solver_flows (
  id            bigserial PRIMARY KEY,
  receipt_id    text NOT NULL,
  seq           int NOT NULL,            -- deterministic row index within the receipt
  tx_hash       text NOT NULL,
  block_height  bigint NOT NULL,
  block_ts      timestamptz NOT NULL,
  account_id    text NOT NULL,
  direction     text NOT NULL,           -- deposit|withdrawal|transfer_in|transfer_out
  asset_id      text NOT NULL,           -- NEP-245 token id == registry asset id
  amount_raw    numeric NOT NULL,
  amount        numeric,                 -- decimal-adjusted (null if unknown asset)
  counterparty  text,                    -- NEAR-side other end (transfer peer, withdraw receiver, deposit sender)
  counterparty_withdrew boolean NOT NULL DEFAULT false, -- transfer peer signed a withdrawal in the same settlement (bridge-out pattern)
  external_address text,                 -- external-chain destination (WITHDRAW_TO / bridge msg)
  origin_chain  text,                    -- deposit source network (BRIDGED_FROM memo)
  origin_tx     text,                    -- deposit source tx hash (BRIDGED_FROM memo)
  memo          text,
  UNIQUE (receipt_id, seq)
);
CREATE INDEX solver_flows_account_ts_idx ON solver_flows (account_id, block_ts);
CREATE INDEX solver_flows_ts_idx ON solver_flows (block_ts);
