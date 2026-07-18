"""1-minute reference prices from Binance and Hyperliquid.

Both venues are read for the union of minutes a pair needs and reduced to a
per-minute mid = (high + low) / 2 (the same proxy the old pipeline used).
Returned as {minute_open_ms -> mid}. Stablecoins are pinned to 1.0 by the
caller, never fetched.

  Binance      GET data-api.binance.vision/api/v3/klines  (1m, 1000/page)
  Hyperliquid  POST api.hyperliquid.xyz/info candleSnapshot (1m perps)

A symbol a venue does not list raises InvalidSymbol (caller flags it
`no_reference`); transient HTTP errors are retried before giving up, so one bad
symbol never aborts a batch run.
"""
from __future__ import annotations

import time

import requests

BINANCE_URL = "https://data-api.binance.vision/api/v3/klines"
HL_URL = "https://api.hyperliquid.xyz/info"
MIN_MS = 60_000
BINANCE_MAX = 1000
RETRIES = 3


class InvalidSymbol(Exception):
    """The venue does not list this symbol — caller flags it `no_reference`."""


def _get(url, **kw):
    last = None
    for attempt in range(1, RETRIES + 1):
        try:
            r = requests.get(url, timeout=30, **kw)
        except requests.RequestException as e:
            last = e
            time.sleep(0.5 * attempt)
            continue
        if r.status_code != 429 and r.status_code < 500:
            return r
        last = f"HTTP {r.status_code}"
        time.sleep(0.5 * attempt)
    raise RuntimeError(f"GET {url} failed: {last}")


def _post(url, json_body):
    last = None
    for attempt in range(1, RETRIES + 1):
        try:
            r = requests.post(url, json=json_body, timeout=30)
        except requests.RequestException as e:
            last = e
            time.sleep(0.5 * attempt)
            continue
        if r.status_code < 500:
            return r
        last = f"HTTP {r.status_code}"
        time.sleep(0.5 * attempt)
    return None  # persistent 5xx (HL returns 500 for unknown coins) -> no_reference


def binance_mids(symbol: str, start_ms: int, end_ms: int) -> dict[int, float]:
    out: dict[int, float] = {}
    cur = start_ms
    while cur <= end_ms:
        r = _get(BINANCE_URL, params={
            "symbol": symbol, "interval": "1m",
            "startTime": cur, "endTime": end_ms, "limit": BINANCE_MAX,
        })
        if r.status_code != 200:
            # Any 400 on a klines call is a symbol problem (unlisted / illegal
            # characters / etc.) — flag no_reference rather than abort the batch.
            if r.status_code == 400:
                raise InvalidSymbol(symbol)
            raise RuntimeError(f"binance {symbol}: HTTP {r.status_code} {r.text[:200]}")
        data = r.json()
        if not data:
            break
        for k in data:
            out[int(k[0])] = (float(k[2]) + float(k[3])) / 2.0
        if len(data) < BINANCE_MAX:
            break
        cur = int(data[-1][0]) + MIN_MS
    if not out:
        raise InvalidSymbol(symbol)
    return out


def hyperliquid_mids(coin: str, start_ms: int, end_ms: int) -> dict[int, float]:
    out: dict[int, float] = {}
    cur = start_ms
    while cur <= end_ms:
        r = _post(HL_URL, {"type": "candleSnapshot",
                           "req": {"coin": coin, "interval": "1m",
                                   "startTime": cur, "endTime": end_ms}})
        if r is None:
            raise InvalidSymbol(coin)             # persistent 5xx = unknown coin
        if r.status_code != 200:
            raise RuntimeError(f"hyperliquid {coin}: HTTP {r.status_code} {r.text[:200]}")
        data = r.json()
        if not isinstance(data, list) or not data:
            break
        for c in data:
            out[int(c["t"])] = (float(c["h"]) + float(c["l"])) / 2.0
        last = int(data[-1]["t"])
        if last < cur or len(data) < 5000:
            break
        cur = last + MIN_MS
    if not out:
        raise InvalidSymbol(coin)
    return out
