"""Discover recent `execute_intents` settlement transactions on-chain.

Without an indexer, we enumerate settlements by walking blocks backward from the
chain head: for each block, read every chunk and keep the transactions whose
receiver is `intents.near` with an `execute_intents` action. The chunk already
carries the transaction's actions, so a discovered settlement can be parsed with
no extra round-trip (its execution status still needs a `tx` fetch).

`intents.near` settles on the order of a few execute_intents per block, so a
handful of blocks yields plenty of samples. For large backfills a NEAR indexer
(NEARBlocks / a custom indexer) is the scalable replacement for this walk.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Iterator, Optional

from . import config
from .rpc import NearRpc, RpcError


@dataclass
class DiscoveredTx:
    tx_hash: str
    signer_id: str          # the relayer that submitted the settlement
    receiver_id: str
    block_height: int
    timestamp_s: float      # block time, seconds
    actions: list           # raw actions (FunctionCall args base64) from the chunk


def _block_time_s(header: dict) -> float:
    ns = header.get("timestamp_nanosec") or header.get("timestamp")
    return int(ns) / 1e9


def iter_settlements(
    rpc: NearRpc,
    *,
    max_blocks: int = 200,
    start_height: Optional[int] = None,
) -> Iterator[DiscoveredTx]:
    """Yield settlement txs walking backward from `start_height` (or head).

    Stops after scanning `max_blocks` blocks. Missing heights (NEAR skips some)
    and transient chunk errors are skipped rather than fatal.
    """
    if start_height is None:
        head = rpc.block(finality="final")
        height = head["header"]["height"]
    else:
        height = start_height

    scanned = 0
    while scanned < max_blocks and height > 0:
        try:
            blk = rpc.block(block_id=height)
        except RpcError:
            height -= 1
            continue
        scanned += 1
        ts = _block_time_s(blk["header"])
        for ch in blk["chunks"]:
            try:
                chunk = rpc.chunk(ch["chunk_hash"])
            except RpcError:
                continue
            for tx in chunk.get("transactions", []):
                if tx.get("receiver_id") != config.INTENTS_CONTRACT:
                    continue
                actions = tx.get("actions", [])
                if not _has_settlement(actions):
                    continue
                yield DiscoveredTx(
                    tx_hash=tx["hash"],
                    signer_id=tx.get("signer_id", "?"),
                    receiver_id=tx["receiver_id"],
                    block_height=blk["header"]["height"],
                    timestamp_s=ts,
                    actions=actions,
                )
        height -= 1


def _has_settlement(actions: list) -> bool:
    for a in actions:
        if isinstance(a, dict):
            fc = a.get("FunctionCall")
            if fc and fc.get("method_name") == config.SETTLEMENT_METHOD:
                return True
    return False
