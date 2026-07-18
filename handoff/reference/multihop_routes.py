#!/usr/bin/env python3
"""Analysis — group a solver's legs within one settlement into multi-hop routes and
synthesize the direct "replacement" swap.

When a solver fills an intent by routing through an intermediate in the SAME tx
(e.g. ETH -> USDC -> SOL, two legs), the net token movement is a single direct swap
(ETH -> SOL) with the intermediate (USDC) passing through. This groups legs by
(tx, solver), nets each asset, and for clean 1-source / 1-sink chains emits the
synthetic replacement leg so its route-level edge can be priced.

The indexer already tags every leg with `tx`, so no indexer change is needed — this
reads `data/legs.jsonl` and groups in post.

    python multihop_routes.py                    # summary + CSV + synthetic legs jsonl
    python multihop_routes.py --solver crux-solver.near
    # then price the direct routes with the normal stage-2 machinery:
    python enrich.py --file data/multihop_synth_legs.jsonl --min-notional 0 \
                     --out data/multihop_enriched.csv --agg-prefix data/multihop_agg

An asset is a pass-through intermediate when it's both received and given in the group
and nets to < `--intermediate-tol` of its throughput; the leftover net-in asset is the
source, net-out the sink. Groups with several sources/sinks (unrelated swaps batched,
or fan-out routes) are counted as `complex` and not synthesized.
"""
from __future__ import annotations

import argparse
import csv
import json
import sys
from collections import Counter, defaultdict
from decimal import Decimal

from lib import config
from lib.assets import Registry

LEGS_JSONL = config.DATA / "legs.jsonl"


def reconstruct_path(legs: list[dict], source_id: str, sink_id: str):
    """Follow from_asset -> to_asset edges from source; (path, is_linear)."""
    out = defaultdict(list)
    for i, l in enumerate(legs):
        out[l["from_asset"]].append((l["to_asset"], i))
    path, used, cur = [source_id], set(), source_id
    for _ in range(len(legs) + 1):
        nxt = [(t, i) for t, i in out.get(cur, []) if i not in used]
        if not nxt:
            break
        t, i = nxt[0]
        used.add(i)
        path.append(t)
        cur = t
        if cur == sink_id:
            break
    return path, (path[-1] == sink_id and len(used) == len(legs))


def net_group(legs: list[dict], tol: Decimal):
    """Return (sources, sinks, intermediates, recv, give, recv_raw, give_raw)."""
    recv, give = defaultdict(Decimal), defaultdict(Decimal)
    recv_raw, give_raw = defaultdict(int), defaultdict(int)
    for l in legs:
        recv[l["from_asset"]] += Decimal(l["amount_in"])
        give[l["to_asset"]] += Decimal(l["amount_out"])
        recv_raw[l["from_asset"]] += int(l["amount_in_raw"])
        give_raw[l["to_asset"]] += int(l["amount_out_raw"])
    src, snk, inter = [], [], []
    for a in set(recv) | set(give):
        rv, gv = recv.get(a, Decimal(0)), give.get(a, Decimal(0))
        net, thru = rv - gv, max(rv, gv)
        if rv > 0 and gv > 0 and thru > 0 and abs(net) / thru < tol:
            inter.append(a)
        elif net > 0:
            src.append(a)
        elif net < 0:
            snk.append(a)
    return src, snk, inter, recv, give, recv_raw, give_raw


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--file", default=str(LEGS_JSONL))
    ap.add_argument("--solver", default="", help="restrict to one solver id")
    ap.add_argument("--intermediate-tol", type=float, default=0.10,
                    help="max |net|/throughput for an asset to count as pass-through (default 0.10)")
    ap.add_argument("--min-notional", type=float, default=0.0,
                    help="drop synthesized routes below this USD notional (default 0 = keep dust)")
    ap.add_argument("--out", default=str(config.DATA / "multihop_routes.csv"))
    ap.add_argument("--synth-legs", default=str(config.DATA / "multihop_synth_legs.jsonl"))
    args = ap.parse_args()

    reg = Registry.load()
    tol = Decimal(str(args.intermediate_tol))

    groups: dict[tuple, list] = defaultdict(list)
    for line in open(args.file):
        r = json.loads(line)
        if args.solver and r["solver"] != args.solver:
            continue
        groups[(r["tx"], r["solver"])].append(r)

    def label(aid):
        return reg.label(aid)

    def base(aid):
        a = reg.get(aid)
        return a.base_symbol if a else "?"

    def notional(src_id, snk_id, in_amt, out_amt):
        sa, ka = reg.get(src_id), reg.get(snk_id)
        if ka and ka.is_stable:
            return float(out_amt)
        if sa and sa.is_stable:
            return float(in_amt)
        if sa and sa.price_usd:
            return float(in_amt) * sa.price_usd
        if ka and ka.price_usd:
            return float(out_amt) * ka.price_usd
        return None

    sizes = Counter(len(v) for v in groups.values())
    n_multi = sum(1 for v in groups.values() if len(v) >= 2)
    clean = complex_ = kept = 0
    inter_ctr, pair_ctr, pair_vol = Counter(), Counter(), defaultdict(float)
    stable_routed = 0
    routes, synth = [], []

    for (tx, solver), legs in groups.items():
        if len(legs) < 2:
            continue
        src, snk, inter, recv, give, recv_raw, give_raw = net_group(legs, tol)
        if not (len(src) == 1 and len(snk) == 1 and inter):
            complex_ += 1
            continue
        clean += 1
        s_id, k_id = src[0], snk[0]
        in_amt = recv[s_id] - give.get(s_id, Decimal(0))
        out_amt = give[k_id] - recv.get(k_id, Decimal(0))
        in_raw = recv_raw[s_id] - give_raw.get(s_id, 0)
        out_raw = give_raw[k_id] - recv_raw.get(k_id, 0)
        nv = notional(s_id, k_id, in_amt, out_amt)
        if nv is not None and nv < args.min_notional:
            continue
        kept += 1
        path_ids, linear = reconstruct_path(legs, s_id, k_id)
        path = " -> ".join(label(a) for a in path_ids) if linear else \
            f"{label(s_id)} ->..({len(inter)} mid).. {label(k_id)}"
        is_stable_mid = all(reg.get(a).is_stable for a in inter if reg.get(a))
        stable_routed += is_stable_mid
        leg_class = reg.classify_pair(s_id, k_id)
        pair = f"{base(s_id)}/{base(k_id)}"
        inter_ctr.update(label(a) for a in inter)
        pair_ctr[pair] += 1
        if nv:
            pair_vol[pair] += nv
        rate = float(out_amt / in_amt) if in_amt else None

        routes.append({
            "tx": tx, "solver": solver, "hops": len(legs), "path": path,
            "linear": int(linear), "synth_pair": pair, "synth_class": leg_class,
            "stable_intermediate": int(is_stable_mid),
            "source": label(s_id), "source_chain": (reg.get(s_id).blockchain if reg.get(s_id) else "?"),
            "source_asset_id": s_id,
            "sink": label(k_id), "sink_chain": (reg.get(k_id).blockchain if reg.get(k_id) else "?"),
            "sink_asset_id": k_id,
            "intermediates": " | ".join(label(a) for a in inter),
            "amount_in": f"{in_amt.normalize():f}", "amount_out": f"{out_amt.normalize():f}",
            "native_rate": f"{rate:.10g}" if rate else "",
            "notional_usd": f"{nv:.2f}" if nv else "",
        })
        synth.append({
            "ts": legs[0]["ts"], "ts_iso": legs[0]["ts_iso"], "tx": tx,
            "block": legs[0]["block"], "solver": solver, "leg_class": leg_class,
            "multi_asset": False, "synthetic": True, "hops": len(legs), "path": path,
            "from_asset": s_id, "from_name": label(s_id),
            "to_asset": k_id, "to_name": label(k_id),
            "amount_in_raw": str(in_raw), "amount_in": f"{in_amt.normalize():f}",
            "amount_out_raw": str(out_raw), "amount_out": f"{out_amt.normalize():f}",
        })

    routes.sort(key=lambda r: float(r["notional_usd"] or 0), reverse=True)
    cols = ["tx", "solver", "hops", "path", "linear", "synth_pair", "synth_class",
            "stable_intermediate", "source", "source_chain", "source_asset_id",
            "sink", "sink_chain", "sink_asset_id", "intermediates",
            "amount_in", "amount_out", "native_rate", "notional_usd"]
    with open(args.out, "w", newline="") as fh:
        w = csv.DictWriter(fh, fieldnames=cols)
        w.writeheader()
        w.writerows(routes)
    with open(args.synth_legs, "w") as fh:
        for s in synth:
            fh.write(json.dumps(s) + "\n")

    # ---- Report -------------------------------------------------------------
    floor = f" >= ${args.min_notional:g}" if args.min_notional else ""
    total_vol = sum(pair_vol.values())
    print(f"(tx, solver) groups: {len(groups)} | size dist: {dict(sorted(sizes.items()))}")
    print(f"multi-leg groups: {n_multi}")
    print(f"clean 1-source/1-sink routes: {clean} ({kept} kept{floor})  ->  {args.out}")
    print(f"  synthetic legs written: {len(synth)}  ->  {args.synth_legs}")
    print(f"  total synthetic notional: ${total_vol:,.0f}")
    print(f"  routed through a stablecoin intermediate: {stable_routed} "
          f"({100*stable_routed/kept:.0f}% of kept)" if kept else "")
    print(f"complex/batched groups (multi source/sink, not synthesized): {complex_}")
    print(f"\ntop pass-through intermediates:")
    for a, n in inter_ctr.most_common(10):
        print(f"  {n:>5}  {a}")
    print(f"\ntop synthetic pairs by VOLUME (count | ~volume):")
    for p, v in sorted(pair_vol.items(), key=lambda kv: -kv[1])[:15]:
        print(f"  {pair_ctr[p]:>5}  {p:16} ~${v:>13,.0f}")
    print(f"\ntop synthetic pairs by COUNT (count | ~volume):")
    for p, n in pair_ctr.most_common(15):
        print(f"  {n:>5}  {p:16} ~${pair_vol.get(p, 0):>13,.0f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
