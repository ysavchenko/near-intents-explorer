-- Redo of 0002. On Render's zero-downtime deploy the outgoing instance keeps
-- running while the new one boots, so the old enricher raced the new one on
-- the legs 0002 had reset and relabeled part of them with the pre-USDT0-alias
-- code (writing USDT0 pairs back as price_status='ok'). The old follower also
-- kept classifying fresh USDT<->USDT0 legs as stable_stable during the
-- overlap. Both fixes are idempotent; this time the overlapping "old"
-- instance already carries the USDT0 alias, so the race is harmless.
UPDATE legs SET leg_class = 'same_asset'
WHERE leg_class = 'stable_stable'
  AND (SELECT upper(symbol) FROM assets WHERE asset_id = from_asset) IN ('USDT', 'USDT0')
  AND (SELECT upper(symbol) FROM assets WHERE asset_id = to_asset)   IN ('USDT', 'USDT0');

UPDATE legs SET pair = NULL, side = NULL, base_symbol = NULL, quote_symbol = NULL,
                native_rate = NULL, hl_rate = NULL, binance_rate = NULL,
                edge_bps_hl = NULL, edge_bps_binance = NULL, notional_usd = NULL,
                price_status = 'pending'
WHERE base_symbol = 'USDT0' OR quote_symbol = 'USDT0';
