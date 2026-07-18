"""Asset registry — resolve NEAR Intents asset ids to symbol/decimals/chain.

Backed by the official 1Click token list, cached to disk. The on-chain
`token_diff` amounts are integers in base units; this registry turns an asset id
into a human-readable symbol and the decimals needed to format the amount.

Asset id forms seen on-chain:
  nep141:eth-0xa0b8...omft.near    omni-bridged ERC20 (chain + contract in id)
  nep141:sol.omft.near             bridged native
  nep141:wrap.near                 wrapped NEAR
  nep245:v2_1.omni.hot.tg:56_2CMM  HOT omni multi-token (numeric chain id embedded)
"""
from __future__ import annotations

import json
from dataclasses import dataclass
from decimal import Decimal
from pathlib import Path
from typing import Optional

import requests

from . import config

TOKENS_URL = "https://1click.chaindefuser.com/v0/tokens"

# Symbols treated as USD-pegged stablecoins (priced at 1.0). EUR pegs and
# yield-bearing vault tokens are intentionally excluded — they carry price risk.
STABLES = {
    "USDT", "USDC", "DAI", "FDUSD", "TUSD", "USDE", "USDP", "PYUSD", "USDD",
    "GUSD", "LUSD", "USD1", "GHO", "CRVUSD", "SUSDE", "USDF", "USDB", "FRAX",
    "USDS", "USDG", "BUSD", "USDT0", "SUSDC", "USDCX", "NRUSDT", "USDON",
}

# Symbol -> reference base symbol when they differ (wrapped/renamed tokens).
BINANCE_ALIAS = {
    "BTC(OMNI)": "BTC", "WNEAR": "NEAR", "WETH": "ETH", "WBTC": "BTC",
    "CBBTC": "BTC", "WSOL": "SOL", "WBNB": "BNB", "WMATIC": "POL",
    "MATIC": "POL", "WAVAX": "AVAX", "WPOL": "POL",
}


@dataclass(frozen=True)
class Asset:
    asset_id: str
    symbol: str
    decimals: int
    blockchain: str
    contract: Optional[str]
    price_usd: Optional[float]

    @property
    def norm_symbol(self) -> str:
        return self.symbol.upper()

    @property
    def is_stable(self) -> bool:
        return self.norm_symbol in STABLES

    @property
    def base_symbol(self) -> str:
        """Underlying asset identity, unwrapping wrapped/native variants."""
        return BINANCE_ALIAS.get(self.norm_symbol, self.norm_symbol)

    def format_amount(self, raw: int) -> Decimal:
        """Base-unit integer -> human decimal using the token's decimals."""
        return Decimal(raw) / (Decimal(10) ** self.decimals)


class Registry:
    def __init__(self, assets: dict[str, Asset]):
        self._assets = assets

    @classmethod
    def load(cls, refresh: bool = False) -> "Registry":
        config.ensure_dirs()
        cache: Path = config.TOKENS_CACHE
        raw = None
        if not refresh and cache.exists():
            raw = json.loads(cache.read_text())
        if raw is None:
            raw = requests.get(TOKENS_URL, timeout=30).json()
            cache.write_text(json.dumps(raw))
        assets: dict[str, Asset] = {}
        for t in raw:
            a = Asset(
                asset_id=t["assetId"],
                symbol=t.get("symbol", "?"),
                decimals=int(t.get("decimals", 0)),
                blockchain=t.get("blockchain", "?"),
                contract=t.get("contractAddress"),
                price_usd=t.get("price"),
            )
            assets[a.asset_id] = a
        return cls(assets)

    def get(self, asset_id: str) -> Optional[Asset]:
        return self._assets.get(asset_id)

    def label(self, asset_id: str) -> str:
        """Short human label: `SYMBOL@chain` (falls back to the raw id tail)."""
        a = self._assets.get(asset_id)
        if a:
            return f"{a.norm_symbol}@{a.blockchain}"
        # Unknown: surface a readable tail of the id rather than the whole thing.
        return asset_id

    def classify_pair(self, asset_a: str, asset_b: str) -> str:
        """Classify a swap leg's two assets:
          same_asset    — same underlying (wrapped/native/cross-chain variants)
          stable_stable — both USD-pegged stablecoins (no price risk)
          interesting   — a price-bearing swap we want for edge stats
          unknown       — at least one asset not in the registry (surfaced)
        """
        a, b = self._assets.get(asset_a), self._assets.get(asset_b)
        if a is None or b is None:
            return "unknown"
        if a.base_symbol == b.base_symbol:
            return "same_asset"
        if a.is_stable and b.is_stable:
            return "stable_stable"
        return "interesting"

    def __len__(self) -> int:
        return len(self._assets)
