#!/usr/bin/env python3
"""Stage 1 — ingest solver swap legs from `intents.near` settlements in a time
window, writing a per-swap CSV and a per-pair summary CSV.

Finds the block range covering [--start, --end] (binary search on block time),
walks it backward in parallel, parses every `execute_intents` settlement into
solver swap legs (one per token_diff, solver side; see lib/settle.py), keeps the
price-bearing ("interesting") legs, and writes:

    swaps CSV    timestamp, from_asset_id, from_name, to_asset_id, to_name,
                 amount_in, amount_out
                 (from = what the solver receives; to = what it gives)
    summary CSV  from_asset_id, from_name, to_asset_id, to_name,
                 quotes, approx_volume_usd
    legs JSONL   richer per-leg rows (solver, class, raw integer amounts) for
                 the downstream pricing stage

Timestamps are `YYYY-MM-DDThh:mm:ss`, interpreted as UTC.

    python ingest.py --start 2026-06-12T13:49:00 --end 2026-06-12T13:56:00
    python ingest.py --start 2026-06-12T10:00:00 --end 2026-06-12T11:00:00 \
                     --out data/swaps.csv --summary data/summary.csv
    python ingest.py --start ... --end ... --legs all     # include bridges/stables

RPC endpoint comes from .env (NEAR_RPC_URL).
"""
from __future__ import annotations

import argparse
import csv
import json
import sys
import time
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from decimal import Decimal
from typing import Optional

from lib import config
from lib.assets import Registry
from lib.rpc import NearRpc, NotFound, RpcError
from lib.settle import parse_settlement
from lib.solvers import SolverSet

LEGS_JSONL = config.DATA / "legs.jsonl"


def parse_ts(s: str) -> float:
    """Parse `YYYY-MM-DDThh:mm:ss` (UTC) to epoch seconds."""
    for fmt in ("%Y-%m-%dT%H:%M:%S", "%Y-%m-%d %H:%M:%S", "%Y-%m-%dT%H:%M", "%Y-%m-%d"):
        try:
            return datetime.strptime(s, fmt).replace(tzinfo=timezone.utc).timestamp()
        except ValueError:
            continue
    raise SystemExit(f"Bad timestamp {s!r}; use 2026-06-12T15:04:52")


def iso(ts: float) -> str:
    return datetime.fromtimestamp(ts, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S")


def block_ts(header: dict) -> float:
    return int(header.get("timestamp_nanosec") or header["timestamp"]) / 1e9


def has_settlement(actions: list) -> bool:
    for a in actions:
        if isinstance(a, dict):
            fc = a.get("FunctionCall")
            if fc and fc.get("method_name") == config.SETTLEMENT_METHOD:
                return True
    return False


def approx_usd(reg: Registry, from_id: str, amount_in: Decimal,
               to_id: str, amount_out: Decimal) -> Optional[Decimal]:
    """Rough USD notional from the 1Click snapshot price. Stable side is taken
    at its face amount; otherwise amount x token price (averaged if both known)."""
    fa, ta = reg.get(from_id), reg.get(to_id)
    if fa and fa.is_stable:
        return amount_in
    if ta and ta.is_stable:
        return amount_out
    cands = []
    if fa and fa.price_usd:
        cands.append(amount_in * Decimal(str(fa.price_usd)))
    if ta and ta.price_usd:
        cands.append(amount_out * Decimal(str(ta.price_usd)))
    if cands:
        return sum(cands) / len(cands)
    return None


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--start", required=True, help="window start, e.g. 2026-06-12T13:49:00 (UTC)")
    ap.add_argument("--end", required=True, help="window end (UTC); use a future time for 'up to now'")
    ap.add_argument("--out", default=str(config.DATA / "swaps.csv"))
    ap.add_argument("--summary", default=str(config.DATA / "swaps_summary.csv"))
    ap.add_argument("--legs", choices=["interesting", "all"], default="interesting")
    ap.add_argument("--workers", type=int, default=14)
    ap.add_argument("--batch", type=int, default=80, help="blocks fetched per round")
    ap.add_argument("--no-verify-status", action="store_true")
    args = ap.parse_args()

    start_ts, end_ts = parse_ts(args.start), parse_ts(args.end)
    if end_ts <= start_ts:
        raise SystemExit("--end must be after --start")

    config.ensure_dirs()
    reg = Registry.load()
    rpc = NearRpc(min_interval_s=0.0)
    pool = ThreadPoolExecutor(max_workers=args.workers)
    solvers = SolverSet()

    def get_block(h: int) -> Optional[dict]:
        try:
            return rpc.block(block_id=h)
        except RpcError:
            return None

    def block_at_or_below(h: int) -> Optional[dict]:
        for hh in range(h, h - 60, -1):
            b = get_block(hh)
            if b:
                return b
        return None

    def get_chunk(ch_hash: str) -> Optional[dict]:
        try:
            return rpc.chunk(ch_hash)
        except RpcError:
            return None

    def get_status(tx_hash: str):
        try:
            res = rpc.tx(tx_hash, config.INTENTS_CONTRACT)
            return ("Failure" not in (res.get("status") or {}), res["transaction"]["actions"])
        except RpcError:
            return (None, None)

    # ---- Locate the upper block height (largest with ts <= end_ts) -----------
    head = rpc.block(finality="final")
    head_h, head_ts = head["header"]["height"], block_ts(head["header"])
    if start_ts > head_ts:
        raise SystemExit(f"--start {args.start} is after chain head ({iso(head_ts)}).")

    if end_ts >= head_ts:
        upper_h = head_h
    else:
        est = head_h - int((head_ts - end_ts) / 0.8)
        lo = max(1, est - 3000)
        while True:
            b = block_at_or_below(lo)
            if b and block_ts(b["header"]) <= end_ts:
                break
            lo = max(1, lo - 3000)
            if lo == 1:
                break
        hi = head_h
        while lo < hi:
            mid = (lo + hi + 1) // 2
            b = block_at_or_below(mid)
            if b is None:
                hi = mid - 1
                continue
            if block_ts(b["header"]) <= end_ts:
                lo = mid
            else:
                hi = b["header"]["height"] - 1
        upper_h = block_at_or_below(lo)["header"]["height"]

    print(f"Window {args.start} .. {args.end} UTC")
    print(f"Chain head height {head_h} ({iso(head_ts)}); starting walk at height {upper_h}")
    print(f"legs={args.legs} workers={args.workers} batch={args.batch}")

    # ---- Walk the window backward -------------------------------------------
    rows: list[dict] = []                                   # detailed swap rows
    summary: dict[tuple, dict] = defaultdict(lambda: {"quotes": 0, "usd": Decimal(0)})
    cls_counts: Counter[str] = Counter()
    seen_tx: set[str] = set()
    n_settlements = n_failed = n_legs_total = 0
    legs_out = LEGS_JSONL.open("w")

    t0 = time.time()
    height = upper_h
    done = False
    while not done and height > 0:
        heights = list(range(height, max(height - args.batch, 0), -1))
        blocks = [b for b in pool.map(get_block, heights) if b]
        # Keep only blocks within the window; detect when we've passed the start.
        in_window = []
        for blk in blocks:
            bts = block_ts(blk["header"])
            if bts < start_ts:
                done = True
            elif bts <= end_ts:
                in_window.append((blk, bts))
        # Gather chunks for in-window blocks only.
        chunk_meta = []
        for blk, bts in in_window:
            bh = blk["header"]["height"]
            for ch in blk["chunks"]:
                chunk_meta.append((ch["chunk_hash"], bh, bts))
        chunks = pool.map(get_chunk, [c[0] for c in chunk_meta]) if chunk_meta else []

        settlements = []  # (tx_hash, signer, actions, block_h, block_ts)
        for (ch_hash, bh, bts), chunk in zip(chunk_meta, chunks):
            if not chunk:
                continue
            for tx in chunk.get("transactions", []):
                if tx.get("receiver_id") != config.INTENTS_CONTRACT:
                    continue
                actions = tx.get("actions", [])
                if has_settlement(actions) and tx["hash"] not in seen_tx:
                    settlements.append((tx["hash"], tx.get("signer_id"), actions, bh, bts))

        if not args.no_verify_status and settlements:
            statuses = list(pool.map(lambda s: get_status(s[0]), settlements))
        else:
            statuses = [(True, s[2]) for s in settlements]

        for (tx_hash, signer, chunk_actions, bh, bts), (ok, actions) in zip(settlements, statuses):
            if actions is None:
                actions = chunk_actions
            seen_tx.add(tx_hash)
            if ok is False:
                n_failed += 1
                continue
            s = parse_settlement(tx_hash, actions, relayer=signer,
                                 succeeded=bool(ok), solvers=solvers)
            n_settlements += 1
            for leg in s.legs:
                leg.leg_class = reg.classify_pair(leg.recv_asset, leg.give_asset)
                cls_counts[leg.leg_class] += 1
                n_legs_total += 1
                ra, ga = reg.get(leg.recv_asset), reg.get(leg.give_asset)
                recv_amt = ra.format_amount(leg.recv_amount) if ra else Decimal(leg.recv_amount)
                give_amt = ga.format_amount(leg.give_amount) if ga else Decimal(leg.give_amount)
                # from = what the solver receives, to = what it gives
                # (matches the quote-request capture: `from` is the solver's inflow).
                from_id, to_id = leg.recv_asset, leg.give_asset
                from_amt, to_amt = recv_amt, give_amt
                # rich JSONL (always, all classes)
                legs_out.write(json.dumps({
                    "ts": bts, "ts_iso": iso(bts), "tx": tx_hash, "block": bh,
                    "solver": leg.signer, "leg_class": leg.leg_class, "multi_asset": leg.multi_asset,
                    "from_asset": from_id, "from_name": reg.label(from_id),
                    "to_asset": to_id, "to_name": reg.label(to_id),
                    "amount_in_raw": str(leg.recv_amount), "amount_in": str(from_amt),
                    "amount_out_raw": str(leg.give_amount), "amount_out": str(to_amt),
                }) + "\n")
                if args.legs == "interesting" and leg.leg_class != "interesting":
                    continue
                row = {
                    "timestamp": iso(bts),
                    "from_asset_id": from_id, "from_name": reg.label(from_id),
                    "to_asset_id": to_id, "to_name": reg.label(to_id),
                    "amount_in": str(from_amt), "amount_out": str(to_amt),
                }
                rows.append(row)
                key = (from_id, reg.label(from_id), to_id, reg.label(to_id))
                agg = summary[key]
                agg["quotes"] += 1
                usd = approx_usd(reg, from_id, from_amt, to_id, to_amt)
                if usd is not None:
                    agg["usd"] += usd

        height = heights[-1] - 1
        if in_window:
            print(f"  at {iso(in_window[-1][1])} | settlements {n_settlements} "
                  f"| swap rows {len(rows)} | {time.time()-t0:.0f}s", flush=True)

    legs_out.close()
    pool.shutdown(wait=False)

    # ---- Write CSVs ----------------------------------------------------------
    rows.sort(key=lambda r: r["timestamp"])
    with open(args.out, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["timestamp", "from_asset_id", "from_name",
                                          "to_asset_id", "to_name", "amount_in", "amount_out"])
        w.writeheader()
        w.writerows(rows)

    summ_rows = []
    for (fid, fname, tid, tname), agg in summary.items():
        summ_rows.append({
            "from_asset_id": fid, "from_name": fname,
            "to_asset_id": tid, "to_name": tname,
            "quotes": agg["quotes"],
            "approx_volume_usd": f"{agg['usd']:.2f}",
        })
    summ_rows.sort(key=lambda r: float(r["approx_volume_usd"]), reverse=True)
    with open(args.summary, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["from_asset_id", "from_name", "to_asset_id",
                                          "to_name", "quotes", "approx_volume_usd"])
        w.writeheader()
        w.writerows(summ_rows)

    # ---- Report --------------------------------------------------------------
    total_usd = sum(agg["usd"] for agg in summary.values())
    span_min = (end_ts - start_ts) / 60.0
    print(f"\n{'='*72}")
    print(f"Done in {time.time()-t0:.0f}s. Window covered: {span_min:.1f} min "
          f"({span_min/60:.2f} h) of chain time.")
    print(f"Settlements: {n_settlements} (failed/skipped {n_failed}) | "
          f"solver legs {n_legs_total} {dict(cls_counts)}")
    print(f"Swap rows written ({args.legs}): {len(rows)} -> {args.out}")
    print(f"Pair summary: {len(summ_rows)} pairs, ~${total_usd:,.0f} approx volume -> {args.summary}")
    print(f"Rich per-leg JSONL: {n_legs_total} rows -> {LEGS_JSONL}")
    print(f"\nTop pairs by approx USD volume:")
    for r in summ_rows[:12]:
        print(f"   ${float(r['approx_volume_usd']):>14,.0f}  {r['quotes']:>4} quotes  "
              f"{r['from_name']} -> {r['to_name']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
