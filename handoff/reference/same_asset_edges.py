#!/usr/bin/env python3
"""Analysis — same-asset (wrap/bridge) legs where the solver kept an in/out spread.

Reads `data/legs.jsonl`, keeps legs whose two sides are the SAME underlying symbol
(e.g. USDT@eth -> USDT@tron) with a non-zero amount gap, decodes each side's chain
from its NEAR Intents asset id, and reports the kept spread in bps
(`in_out_diff_bps = (amount_in / amount_out - 1) * 1e4`; > 0 = solver received more
than it gave). Defaults to the stablecoins USDT + USDC.

    python same_asset_edges.py                       # all solvers, USDT+USDC
    python same_asset_edges.py --solver 6247d9c6...  # one solver
    python same_asset_edges.py --symbols ETH         # a different same-asset symbol
    python same_asset_edges.py --routes              # + chain-route breakdown
    python same_asset_edges.py --hub tron            # + into/out directional table
    python same_asset_edges.py --buckets eth:tron tron:eth   # + amount-size buckets
"""
from __future__ import annotations

import argparse
import csv
import json
import sys
from collections import defaultdict
from decimal import Decimal

from lib import config
from lib.assets import Registry

LEGS_JSONL = config.DATA / "legs.jsonl"

BUCKETS = [(0, 100, "< $100"), (100, 1_000, "$100 - $1K"), (1_000, 10_000, "$1K - $10K"),
           (10_000, 100_000, "$10K - $100K"), (100_000, float("inf"), ">= $100K")]


def bucket(v: float) -> str:
    for lo, hi, lab in BUCKETS:
        if lo <= v < hi:
            return lab
    return "?"


def vw(legs: list[dict]) -> tuple[int, float, float, float]:
    """(n, volume, volume-weighted bps, mean bps)."""
    v = sum(l["notional"] for l in legs)
    n = len(legs)
    wbps = sum(l["bps"] * l["notional"] for l in legs) / v if v else 0.0
    mbps = sum(l["bps"] for l in legs) / n if n else 0.0
    return n, v, wbps, mbps


def load_legs(reg: Registry, symbols: set[str], solver: str | None) -> list[dict]:
    legs = []
    for line in open(LEGS_JSONL):
        r = json.loads(line)
        if solver and r["solver"] != solver:
            continue
        fa, ta = reg.get(r["from_asset"]), reg.get(r["to_asset"])
        fb = fa.base_symbol if fa else None
        tb = ta.base_symbol if ta else None
        if fb not in symbols or fb != tb:        # same underlying symbol only
            continue
        recv, give = Decimal(r["amount_in"]), Decimal(r["amount_out"])
        if recv == give or not give:             # keep only real spreads
            continue
        px = 1.0 if fa.is_stable else (fa.price_usd or None)
        legs.append({
            "solver": r["solver"], "pair": f"{fb}/{tb}",
            "timestamp": r["ts_iso"], "tx": r["tx"],
            "received": reg.label(r["from_asset"]), "recv_chain": fa.blockchain,
            "recv_asset_id": r["from_asset"], "recv_contract": fa.contract or "",
            "given": reg.label(r["to_asset"]), "give_chain": ta.blockchain,
            "give_asset_id": r["to_asset"], "give_contract": ta.contract or "",
            "amount_in": r["amount_in"], "amount_out": r["amount_out"],
            "bps": float((recv / give - 1) * 10_000),
            "notional": float(recv) * px if px else 0.0,
        })
    return legs


def write_csv(path: str, legs: list[dict]) -> None:
    cols = ["solver", "pair", "timestamp", "tx", "received", "recv_chain", "recv_asset_id",
            "recv_contract", "given", "give_chain", "give_asset_id", "give_contract",
            "amount_in", "amount_out", "in_out_diff_bps", "notional_usd"]
    with open(path, "w", newline="") as fh:
        w = csv.DictWriter(fh, fieldnames=cols)
        w.writeheader()
        for l in sorted(legs, key=lambda x: x["notional"], reverse=True):
            w.writerow({**{k: l[k] for k in cols if k in l},
                        "in_out_diff_bps": f"{l['bps']:.4f}", "notional_usd": f"{l['notional']:.2f}"})


def print_by_solver(legs: list[dict]) -> None:
    for pair in sorted({l["pair"] for l in legs}):
        ls = [l for l in legs if l["pair"] == pair]
        print(f"\n=== {pair}: per solver ({len(ls)} legs, ${sum(l['notional'] for l in ls):,.0f}) ===")
        print(f"  {'solver':52}{'n':>5}{'volume$':>14}{'vw_bps':>8}{'mean_bps':>10}")
        g = defaultdict(list)
        for l in ls:
            g[l["solver"]].append(l)
        for s, sl in sorted(g.items(), key=lambda kv: -sum(x["notional"] for x in kv[1])):
            n, v, w, m = vw(sl)
            print(f"  {s[:52]:52}{n:>5}{v:>14,.0f}{w:>8.2f}{m:>10.2f}")


def print_routes(legs: list[dict], top: int = 15) -> None:
    print(f"\n=== chain routes (recv_chain -> give_chain) ===")
    g = defaultdict(list)
    for l in legs:
        g[f"{l['recv_chain']} -> {l['give_chain']}"].append(l)
    tot = sum(l["notional"] for l in legs) or 1
    print(f"  {'route':20}{'n':>5}{'volume$':>14}{'%vol':>7}{'vw_bps':>8}")
    for route, ls in sorted(g.items(), key=lambda kv: -sum(x["notional"] for x in kv[1]))[:top]:
        n, v, w, _ = vw(ls)
        print(f"  {route:20}{n:>5}{v:>14,.0f}{100*v/tot:>6.1f}%{w:>8.2f}")


def print_hub(legs: list[dict], hub: str) -> None:
    hub_legs = [l for l in legs if hub in (l["recv_chain"], l["give_chain"])]
    into, out = defaultdict(list), defaultdict(list)         # keyed by counterparty chain
    for l in hub_legs:
        if l["give_chain"] == hub and l["recv_chain"] != hub:
            into[l["recv_chain"]].append(l)                  # X -> hub (maker sends out hub)
        elif l["recv_chain"] == hub and l["give_chain"] != hub:
            out[l["give_chain"]].append(l)                   # hub -> X (maker sends out X)
    chains = sorted(set(into) | set(out),
                    key=lambda c: -(sum(x["notional"] for x in into[c] + out[c])))
    print(f"\n=== {hub} routes, both directions ({len(hub_legs)} legs) ===")
    print(f"{'counterparty':12} | {'INTO '+hub+' (X->'+hub+')':>32} | {'OUT of '+hub+' ('+hub+'->X)':>32}")
    print(f"{'':12} | {'n':>5}{'volume$':>15}{'vw_bps':>8} | {'n':>5}{'volume$':>15}{'vw_bps':>8}")
    print("-" * 90)
    for c in chains:
        ni, vi, wi, _ = vw(into[c]) if into[c] else (0, 0, 0, 0)
        no, vo, wo, _ = vw(out[c]) if out[c] else (0, 0, 0, 0)
        si = f"{ni:>5}{vi:>15,.0f}{wi:>8.2f}" if ni else f"{'-':>5}{'-':>15}{'-':>8}"
        so = f"{no:>5}{vo:>15,.0f}{wo:>8.2f}" if no else f"{'-':>5}{'-':>15}{'-':>8}"
        print(f"{c:12} | {si} | {so}")
    ni, vi, wi, _ = vw(sum(into.values(), []))
    no, vo, wo, _ = vw(sum(out.values(), []))
    print("-" * 90)
    print(f"{'TOTAL':12} | {ni:>5}{vi:>15,.0f}{wi:>8.2f} | {no:>5}{vo:>15,.0f}{wo:>8.2f}")


def print_buckets(legs: list[dict], route: str) -> None:
    recv, give = route.split(":")
    ls = [l for l in legs if l["recv_chain"] == recv and l["give_chain"] == give]
    print(f"\n=== {recv} -> {give}: amount buckets ({len(ls)} legs, ${sum(l['notional'] for l in ls):,.0f}) ===")
    print(f"  {'bucket':14}{'n':>5}{'volume$':>14}{'%vol':>7}{'vw_bps':>8}{'avg_size$':>12}")
    by = defaultdict(list)
    for l in ls:
        by[bucket(l["notional"])].append(l)
    tot = sum(l["notional"] for l in ls) or 1
    for _, _, lab in BUCKETS:
        g = by.get(lab, [])
        if not g:
            print(f"  {lab:14}{'-':>5}{'-':>14}{'-':>7}{'-':>8}{'-':>12}")
            continue
        n, v, w, _ = vw(g)
        print(f"  {lab:14}{n:>5}{v:>14,.0f}{100*v/tot:>6.1f}%{w:>8.2f}{v/n:>12,.0f}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--symbols", default="USDT,USDC", help="comma list of same-asset symbols")
    ap.add_argument("--solver", default="", help="restrict to one solver id")
    ap.add_argument("--out", default=str(config.DATA / "stable_same_asset_legs.csv"))
    ap.add_argument("--routes", action="store_true", help="print chain-route breakdown")
    ap.add_argument("--hub", default="", help="print into/out directional table for a chain, e.g. tron")
    ap.add_argument("--buckets", nargs="*", default=[],
                    help="amount-size buckets for routes RECV:GIVE, e.g. eth:tron tron:eth")
    args = ap.parse_args()

    reg = Registry.load()
    symbols = {s.strip().upper() for s in args.symbols.split(",") if s.strip()}
    legs = load_legs(reg, symbols, args.solver or None)
    if not legs:
        raise SystemExit(f"No same-asset legs for symbols={sorted(symbols)}"
                         + (f" solver={args.solver}" if args.solver else ""))

    write_csv(args.out, legs)
    print(f"Exported {len(legs)} legs -> {args.out}")
    print_by_solver(legs)
    if args.routes:
        print_routes(legs)
    if args.hub:
        print_hub(legs, args.hub)
    for route in args.buckets:
        print_buckets(legs, route)
    return 0


if __name__ == "__main__":
    sys.exit(main())
