"""Throttled, retrying NEAR JSON-RPC client.

Wraps the handful of RPC methods we need:
  * `block`  — fetch a block by height/hash or latest final, with its chunk list
  * `chunk`  — fetch a chunk's transactions (used to discover settlements)
  * `tx`     — fetch one transaction's actions + execution outcome

Endpoints are tried in order (paid node first, public fallback) so a transient
error or a missing archival range on one node falls through to the next.
"""
from __future__ import annotations

import time
from typing import Any, Optional

import requests

from . import config


class RpcError(RuntimeError):
    pass


class NotFound(RpcError):
    """The node definitively does not have this block/chunk/tx (e.g. a skipped
    height, or data outside an archival range). Not retryable."""
    pass


class NearRpc:
    def __init__(
        self,
        urls: Optional[list[str]] = None,
        *,
        min_interval_s: float = 0.0,
        max_retries: int = 5,
        timeout: float = 30.0,
    ) -> None:
        self.urls = urls or config.near_rpc_urls()
        self.min_interval_s = min_interval_s
        self.max_retries = max_retries
        self.timeout = timeout
        self._session = requests.Session()
        self._last_call = 0.0

    # -- transport ----------------------------------------------------------
    def _wait_turn(self) -> None:
        if self.min_interval_s <= 0:
            return
        elapsed = time.monotonic() - self._last_call
        if elapsed < self.min_interval_s:
            time.sleep(self.min_interval_s - elapsed)

    def call(self, method: str, params: Any) -> Any:
        """Call an RPC method, returning `result`. Retries transient errors and
        rotates through endpoints; raises RpcError on a definitive RPC error."""
        body = {"jsonrpc": "2.0", "id": "1", "method": method, "params": params}
        last_err: Any = None
        for attempt in range(1, self.max_retries + 1):
            url = self.urls[(attempt - 1) % len(self.urls)]
            self._wait_turn()
            self._last_call = time.monotonic()
            try:
                resp = self._session.post(url, json=body, timeout=self.timeout)
            except requests.RequestException as exc:
                last_err = f"network: {exc}"
                self._backoff(attempt)
                continue
            if resp.status_code == 429:
                last_err = "HTTP 429"
                self._backoff(attempt, base=2.0)
                continue
            if resp.status_code >= 500:
                last_err = f"HTTP {resp.status_code}"
                self._backoff(attempt)
                continue
            if resp.status_code != 200:
                raise RpcError(f"{method} -> HTTP {resp.status_code}: {resp.text[:300]}")
            data = resp.json()
            if "error" in data and data["error"] is not None:
                err = data["error"]
                last_err = err
                cause = (err.get("cause") or {}).get("name") if isinstance(err, dict) else None
                # Definitively-missing data (skipped height, non-archival range):
                # don't waste retries — the caller skips it.
                if cause in ("UNKNOWN_TRANSACTION", "UNKNOWN_BLOCK", "UNKNOWN_CHUNK"):
                    raise NotFound(f"{method}: {cause}")
                self._backoff(attempt)
                continue
            return data["result"]
        raise RpcError(f"{method} failed after {self.max_retries} attempts: {last_err}")

    def _backoff(self, attempt: int, *, base: float = 1.0) -> None:
        time.sleep(min(30.0, base * (2 ** (attempt - 1))))

    # -- typed helpers ------------------------------------------------------
    def block(self, block_id: Optional[int] = None, *, finality: str = "final") -> dict:
        params = {"block_id": block_id} if block_id is not None else {"finality": finality}
        return self.call("block", params)

    def chunk(self, chunk_hash: str) -> dict:
        return self.call("chunk", {"chunk_id": chunk_hash})

    def tx(self, tx_hash: str, sender_id: str) -> dict:
        """Full transaction status (transaction + receipts + outcome).

        `sender_id` only needs to be an account in the tx's shard; the contract
        receiver (`intents.near`) works for our settlement txs.
        """
        return self.call(
            "tx",
            {"tx_hash": tx_hash, "sender_account_id": sender_id, "wait_until": "FINAL"},
        )
