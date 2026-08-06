-- USDT0 (Tether's omnichain USDT) now folds into the USDT family via
-- assets.BaseSymbol. Bring already-ingested legs in line with the new
-- grouping:
--
-- 1. USDT<->USDT0 legs are the same underlying asset — reclassify the
--    stable_stable rows as same_asset so they show up as bridge flows.
UPDATE legs SET leg_class = 'same_asset'
WHERE leg_class = 'stable_stable'
  AND (SELECT upper(symbol) FROM assets WHERE asset_id = from_asset) IN ('USDT', 'USDT0')
  AND (SELECT upper(symbol) FROM assets WHERE asset_id = to_asset)   IN ('USDT', 'USDT0');

-- 2. Reset enrichment on every leg touching USDT0 so the enricher reprices
--    them and their pair/base/quote labels collapse onto USDT.
UPDATE legs SET pair = NULL, side = NULL, base_symbol = NULL, quote_symbol = NULL,
                native_rate = NULL, hl_rate = NULL, binance_rate = NULL,
                edge_bps_hl = NULL, edge_bps_binance = NULL, notional_usd = NULL,
                price_status = 'pending'
WHERE from_asset IN (SELECT asset_id FROM assets WHERE upper(symbol) = 'USDT0')
   OR to_asset   IN (SELECT asset_id FROM assets WHERE upper(symbol) = 'USDT0');
