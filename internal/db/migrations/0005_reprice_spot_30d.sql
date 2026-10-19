-- Re-price the last 31 days under the spot convention (commit fea0a55: Binance
-- spot headline, USDC family valued at the USDCUSDT cross on every venue).
-- Rows priced before that deploy have no Binance edge, so the UI's 30-day
-- window showed no fees on them; one day of margin keeps the window edge
-- covered while the backfill runs. Older rows deliberately keep the old numbers.
--
-- Only the venue rate/edge columns are cleared: pair, side, native_rate and
-- notional do not change, and keeping them means volume and pair grouping stay
-- visible while the enricher works through the backlog (newest first).
-- Hyperliquid marks are already cached in price_marks, so only Binance klines
-- and the USDCUSDT cross get fetched. Idempotent; the overlapping Render
-- instance during deploy already runs the same pricing code, so no race.
UPDATE legs SET hl_rate = NULL, binance_rate = NULL,
                edge_bps_hl = NULL, edge_bps_binance = NULL,
                price_status = 'pending'
WHERE block_ts >= now() - interval '31 days'
  AND price_status <> 'pending';
