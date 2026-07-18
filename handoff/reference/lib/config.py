"""Shared configuration: paths and runtime secrets.

Secrets (the NEAR RPC URL, which may embed an auth token) are read from a
gitignored `.env` at runtime via python-dotenv — never logged or passed on a
command line.
"""
from __future__ import annotations

import os
from pathlib import Path

from dotenv import load_dotenv

ROOT = Path(__file__).resolve().parent.parent
load_dotenv(ROOT / ".env")

# --- NEAR RPC --------------------------------------------------------------
# A paid/archival node is recommended for scale; the public FastNEAR endpoint
# is the default so the tooling runs out of the box for small samples.
PUBLIC_RPC = "https://rpc.mainnet.fastnear.com"


def near_rpc_urls() -> list[str]:
    """Ordered list of RPC endpoints to try (primary first, then fallback)."""
    urls: list[str] = []
    primary = (os.getenv("NEAR_RPC_URL") or "").strip()
    fallback = (os.getenv("NEAR_RPC_URL_FALLBACK") or "").strip()
    for u in (primary, fallback, PUBLIC_RPC):
        if u and u not in urls:
            urls.append(u)
    return urls


# --- Contract under study --------------------------------------------------
INTENTS_CONTRACT = "intents.near"
SETTLEMENT_METHOD = "execute_intents"

# --- Data layout -----------------------------------------------------------
DATA = ROOT / "data"
RAW_SETTLEMENTS = DATA / "raw" / "settlements"
STATE = DATA / "state"
OUT = DATA / "out"

TOKENS_CACHE = STATE / "tokens.json"
PRICE_CACHE = STATE / "price_cache"     # per-symbol venue mids, for resumable enrich


def ensure_dirs() -> None:
    for d in (RAW_SETTLEMENTS, STATE, OUT, PRICE_CACHE):
        d.mkdir(parents=True, exist_ok=True)
