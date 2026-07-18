#!/usr/bin/env python3
"""Stage 2 — enrich all NEAR Intents swap legs with Binance + Hyperliquid
reference rates, compute the signed solver edge in bps vs each venue, and
aggregate per pair / solver / solver×pair.

For every interesting leg we orient the pair as quote-per-base (stable, else the
more numeraire-like asset, is the quote), then:

    native_rate = quote_amount / base_amount
    venue_rate  = venue_usd(base) / venue_usd(quote)        (stable usd = 1.0)

    edge_bps = (native_rate/venue_rate - 1)*1e4   if the solver SOLD base
             = (venue_rate/native_rate - 1)*1e4   if the solver BOUGHT base

so edge_bps is a single signed number: > 0 means the solver transacted better
than the venue mid (gross edge, ignoring fees/spread). Volume-weighted by the
trade's USD notional. Legs whose asset isn't listed on a venue are `no_reference`
for that venue (blank edge), counted but excluded from that venue's averages.

    python enrich.py                       # all pairs in data/legs.jsonl
    python enrich.py --file data/swaps.csv
    python enrich.py --pair NEAR           # restrict to one pair

Outputs:
    data/enriched_legs.csv         per leg (native/binance/hl rate, edge_bps each)
    data/agg_by_pair.csv
    data/agg_by_solver.csv
    data/agg_by_solver_pair.csv
"""
from __future__ import annotations

import argparse
import csv
import json
import re
import sys
import time
from collections import defaultdict
from datetime import datetime, timezone
from decimal import Decimal
from pathlib import Path
from typing import Optional

from lib import config
from lib.assets import BINANCE_ALIAS, STABLES, Registry
from lib.prices import InvalidSymbol, binance_mids, hyperliquid_mids

LEGS_JSONL = config.DATA / "legs.jsonl"

# Quote preference between the two sides of a pair (lower = more likely the
# quote/numeraire). Stables always win; then majors; everything else ranks last
# and ties break alphabetically. Orientation barely moves edge_bps but keeps
# native_rate readable.
_MAJORS = ["BTC", "ETH", "BNB", "SOL", "XRP", "TON", "NEAR", "TRX", "POL", "LTC", "AVAX", "ZEC"]
QUOTE_RANK = {**{s: -1 for s in STABLES}, **{m: i for i, m in enumerate(_MAJORS)}}


def qrank(sym: str) -> int:
    return QUOTE_RANK.get(sym, 10_000)


def orient(s1: str, s2: str) -> tuple[str, str]:
    """Return (base, quote): quote is the more numeraire-like side."""
    r1, r2 = qrank(s1), qrank(s2)
    if r1 == r2:
        base, quote = sorted((s1, s2))     # deterministic tie-break
        return base, quote
    return (s1, s2) if r2 < r1 else (s2, s1)


def canon(sym: str) -> str:
    s = sym.upper()
    return BINANCE_ALIAS.get(s, s)


def venue_symbol(sym: str) -> str:
    """Ticker as the exchanges spell it — strip memecoin punctuation like the
    leading '$' ($WIF -> WIF) so the lookup actually resolves."""
    return re.sub(r"[^A-Z0-9]", "", sym.upper())


def minute_ms(ts: float) -> int:
    return (int(ts) // 60) * 60 * 1000


def to_epoch(s: str) -> float:
    return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()


def load_rows(path: str):
    """Normalize jsonl/csv to: ts, from_asset, to_asset (received/given by the
    solver), amount_in, amount_out, solver, tx, leg_class."""
    rows = []
    if path.endswith(".jsonl"):
        for line in open(path):
            r = json.loads(line)
            rows.append({
                "ts": float(r["ts"]), "from_asset": r["from_asset"], "to_asset": r["to_asset"],
                "amount_in": Decimal(r["amount_in"]), "amount_out": Decimal(r["amount_out"]),
                "solver": r.get("solver", ""), "tx": r.get("tx", ""),
                "leg_class": r.get("leg_class", "interesting"),
            })
    else:
        for r in csv.DictReader(open(path)):
            rows.append({
                "ts": to_epoch(r["timestamp"]), "from_asset": r["from_asset_id"], "to_asset": r["to_asset_id"],
                "amount_in": Decimal(r["amount_in"]), "amount_out": Decimal(r["amount_out"]),
                "solver": "", "tx": "", "leg_class": "interesting",
            })
    return rows


def fnum(x: Optional[float], prec: str = ".4f") -> str:
    return "" if x is None else format(x, prec)


def venue_stats(legs: list[dict], edge_key: str) -> dict:
    rel = [l for l in legs if l.get(edge_key) is not None]
    if not rel:
        return {"n": 0, "vol": 0.0, "vw": None, "mean": None}
    vol = sum(l["notional"] for l in rel if l["notional"])
    vw = (sum(l[edge_key] * l["notional"] for l in rel if l["notional"]) / vol) if vol else None
    mean = sum(l[edge_key] for l in rel) / len(rel)
    return {"n": len(rel), "vol": vol, "vw": vw, "mean": mean}


def aggregate(legs: list[dict], key_fields, keyfn):
    groups: dict = defaultdict(list)
    for l in legs:
        groups[keyfn(l)].append(l)
    out = []
    for key, ls in groups.items():
        b, h = venue_stats(ls, "edge_bin"), venue_stats(ls, "edge_hl")
        row = dict(zip(key_fields, key if isinstance(key, tuple) else (key,)))
        row.update({
            "n_legs": len(ls),
            "volume_usd": f"{sum(l['notional'] for l in ls if l['notional']):.2f}",
            "binance_n": b["n"], "binance_vw_edge_bps": fnum(b["vw"], ".2f"),
            "binance_mean_edge_bps": fnum(b["mean"], ".2f"),
            "hl_n": h["n"], "hl_vw_edge_bps": fnum(h["vw"], ".2f"),
            "hl_mean_edge_bps": fnum(h["mean"], ".2f"),
        })
        out.append(row)
    out.sort(key=lambda r: float(r["volume_usd"]), reverse=True)
    return out


def write_csv(path, fieldnames, rows):
    with open(path, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=fieldnames)
        w.writeheader()
        w.writerows(rows)


# --- Resumable on-disk price cache ----------------------------------------
# Each (venue, symbol, window) result is written to its own JSON file the moment
# it lands, so an interrupted enrich resumes without refetching — including the
# slow no_reference lookups (unlisted coins cost ~3s each in HL retry backoff).

def _cache_path(venue: str, symbol: str, start_ms: int, end_ms: int) -> Path:
    safe = re.sub(r"[^A-Za-z0-9]", "_", symbol) or "_"
    return config.PRICE_CACHE / f"{venue}_{safe}_{start_ms}_{end_ms}.json"


def _read_price_cache(path: Path):
    """Return (hit, mids); mids == {} marks a cached no_reference."""
    if not path.exists():
        return False, None
    try:
        obj = json.loads(path.read_text())
    except (json.JSONDecodeError, OSError):
        return False, None
    if obj.get("missing"):
        return True, {}
    return True, {int(k): v for k, v in obj.get("mids", {}).items()}


def _write_price_cache(path: Path, mids: dict) -> None:
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps({"missing": True} if not mids else {"mids": mids}))
    tmp.replace(path)            # atomic: a half-written file never poisons a resume


def fetch_mids(venue: str, key: str, start_ms: int, end_ms: int,
               fetch_fn, use_cache: bool) -> dict:
    """Disk-cached venue fetch; returns {} for no_reference (also cached)."""
    path = _cache_path(venue, key, start_ms, end_ms)
    if use_cache:
        hit, mids = _read_price_cache(path)
        if hit:
            return mids
    try:
        mids = fetch_fn()
    except InvalidSymbol:
        mids = {}
    _write_price_cache(path, mids)
    return mids


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--file", default=str(LEGS_JSONL))
    ap.add_argument("--pair", default="", help="restrict to one pair, e.g. NEAR or ETH/SOL")
    ap.add_argument("--min-notional", type=float, default=1.0,
                    help="drop legs below this USD notional (default 1.0)")
    ap.add_argument("--fresh-prices", action="store_true",
                    help="ignore the on-disk price cache and refetch all venue mids")
    ap.add_argument("--par-legs", choices=["off", "diff", "all"], default="diff",
                    help="also score same_asset / stable_stable legs against par=1.0: "
                         "'diff' (default) keeps those whose in/out amounts differ "
                         "(a real spread), 'all' keeps every one, 'off' = interesting only")
    ap.add_argument("--reference", choices=["hl", "binance", "both"], default="hl",
                    help="reference venue for the edge (default hl = Hyperliquid, where "
                         "solvers hedge). 'both' fetches Binance too for comparison but "
                         "keeps Hyperliquid primary; 'binance' = Binance only.")
    ap.add_argument("--out", default=str(config.DATA / "enriched_legs.csv"))
    ap.add_argument("--agg-prefix", default=str(config.DATA / "agg"))
    args = ap.parse_args()

    reg = Registry.load()
    rows = load_rows(args.file)

    def base_of(aid):
        a = reg.get(aid)
        return a.base_symbol if a else None

    pair_filter = None
    if args.pair:
        ps = [canon(x) for x in args.pair.split("/")] if "/" in args.pair else [canon(args.pair), None]
        pair_filter = {ps[0], ps[1]} if ps[1] else {ps[0]}

    # Build oriented legs.
    legs = []
    for r in rows:
        cls = r["leg_class"]
        fb, tb = base_of(r["from_asset"]), base_of(r["to_asset"])
        if cls == "interesting":
            if not fb or not tb or fb == tb:
                continue
            if pair_filter and not pair_filter <= {fb, tb}:
                continue
            base, quote = orient(fb, tb)
            amt_by_base = {fb: r["amount_in"], tb: r["amount_out"]}
            base_asset = r["from_asset"] if fb == base else r["to_asset"]
            legs.append({
                **r, "from_base": fb, "to_base": tb, "base": base, "quote": quote,
                # `pair` is DIRECTED: from_base/to_base = received/given by the solver,
                # so a swap and its reverse are separate pairs (buy vs sell). Rates use
                # the canonical base/quote (volatile-in-stable) for readability.
                "pair": f"{fb}/{tb}", "base_asset": base_asset,
                "base_amount": amt_by_base[base], "quote_amount": amt_by_base[quote],
                "side": "buy_base" if fb == base else "sell_base", "par": False,
            })
        elif cls in ("same_asset", "stable_stable") and args.par_legs != "off":
            # Same-underlying (wrap/bridge) and stable<->stable legs have a par
            # fair-rate of 1.0; the solver's spread IS the in/out amount gap. Keep
            # them and benchmark against 1.0 (no venue mid needed). 'diff' keeps
            # only those with an actual gap (recv != give); 'all' keeps every one.
            recv, give = r["amount_in"], r["amount_out"]
            if not recv or not give or not fb or not tb:
                continue
            if args.par_legs == "diff" and recv == give:
                continue
            if pair_filter and not pair_filter <= {fb, tb}:
                continue
            # base = given side, quote = received side, side = sell_base, so
            # edge = (native/1 - 1) = recv/give - 1: > 0 when the solver received
            # more than it gave (kept the spread).
            legs.append({
                **r, "from_base": fb, "to_base": tb, "base": tb, "quote": fb,
                "pair": f"{fb}/{tb}", "base_asset": r["to_asset"],
                "base_amount": give, "quote_amount": recv,
                "side": "sell_base", "par": True,
            })
    if not legs:
        raise SystemExit(f"No legs to enrich in {args.file}"
                         + (f" for pair {args.pair}" if args.pair else ""))

    ts_lo, ts_hi = min(l["ts"] for l in legs), max(l["ts"] for l in legs)
    start_ms, end_ms = minute_ms(ts_lo) - 60_000, minute_ms(ts_hi) + 60_000
    n_par = sum(1 for l in legs if l["par"])
    print(f"{len(legs)} legs ({len(legs)-n_par} interesting + {n_par} par "
          f"[{args.par_legs}]) | "
          f"{datetime.fromtimestamp(ts_lo, timezone.utc):%Y-%m-%dT%H:%M} .. "
          f"{datetime.fromtimestamp(ts_hi, timezone.utc):%Y-%m-%dT%H:%M} UTC")

    # Fetch each non-stable symbol's USD mids once, from the reference venue(s).
    # Each result is disk-cached per symbol+window (data/state/price_cache) so an
    # interrupted run resumes instantly, and every symbol logs as it lands.
    need_bin = args.reference in ("binance", "both")
    need_hl = args.reference in ("hl", "both")
    primary = "hl" if need_hl else "binance"
    primary_name = "Hyperliquid" if primary == "hl" else "Binance"
    symbols = sorted({s for l in legs for s in (l["base"], l["quote"]) if s not in STABLES})
    use_cache = not args.fresh_prices
    bin_ref: dict[str, dict[int, float]] = {}
    hl_ref: dict[str, dict[int, float]] = {}
    t_fetch = time.time()
    venues = " + ".join(n for n, on in (("Binance", need_bin), ("Hyperliquid", need_hl)) if on)
    print(f"Reference: {primary_name} (primary). Fetching {len(symbols)} symbols from "
          f"{venues} (cache {'on' if use_cache else 'OFF'}: {config.PRICE_CACHE})", flush=True)
    for i, s in enumerate(symbols, 1):
        vs = venue_symbol(s)
        bin_ref[s], hl_ref[s] = {}, {}
        if vs and need_bin:
            bin_ref[s] = fetch_mids("binance", f"{vs}USDT", start_ms, end_ms,
                                    lambda vs=vs: binance_mids(f"{vs}USDT", start_ms, end_ms), use_cache)
        if vs and need_hl:
            hl_ref[s] = fetch_mids("hl", vs, start_ms, end_ms,
                                   lambda vs=vs: hyperliquid_mids(vs, start_ms, end_ms), use_cache)
        bn = (f"{len(bin_ref[s])}m" if bin_ref[s] else "no_ref") if need_bin else "off"
        hl = (f"{len(hl_ref[s])}m" if hl_ref[s] else "no_ref") if need_hl else "off"
        print(f"  [{i:>3}/{len(symbols)}] {s:<12} binance={bn:<7} hyperliquid={hl:<7} "
              f"| {time.time()-t_fetch:.0f}s", flush=True)
    prim_ref = hl_ref if primary == "hl" else bin_ref
    miss = [s for s in symbols if not prim_ref[s]]
    print(f"Fetched {len(symbols)} symbols in {time.time()-t_fetch:.0f}s. "
          f"no_reference on {primary_name}: {miss or 'none'}", flush=True)

    def usd(table, sym, m):
        return 1.0 if sym in STABLES else table.get(sym, {}).get(m)

    def venue_rate(table, base, quote, m):
        ub, uq = usd(table, base, m), usd(table, quote, m)
        return (ub / uq) if (ub and uq) else None

    def edge(native, rate, side):
        if not native or not rate:
            return None
        return (native / rate - 1) * 1e4 if side == "sell_base" else (rate / native - 1) * 1e4

    # Per-leg enrichment (annotate each leg).
    for l in legs:
        m = minute_ms(l["ts"])
        base_amt, quote_amt = l["base_amount"], l["quote_amount"]
        l["native"] = float(quote_amt / base_amt) if base_amt else None
        if l["par"]:
            # Par trade: same dollar (stable<->stable) or same token (wrap/bridge),
            # so the fair rate is 1.0 on any venue in use.
            l["b_rate"] = 1.0 if need_bin else None
            l["h_rate"] = 1.0 if need_hl else None
        else:
            l["b_rate"] = venue_rate(bin_ref, l["base"], l["quote"], m) if need_bin else None
            l["h_rate"] = venue_rate(hl_ref, l["base"], l["quote"], m) if need_hl else None
        l["edge_bin"] = edge(l["native"], l["b_rate"], l["side"])
        l["edge_hl"] = edge(l["native"], l["h_rate"], l["side"])
        # USD notional for weighting (priced off the primary venue, other as fallback).
        if l["quote"] in STABLES:
            notional = float(quote_amt)
        else:
            if primary == "hl":
                px = usd(hl_ref, l["base"], m) or usd(bin_ref, l["base"], m)
            else:
                px = usd(bin_ref, l["base"], m) or usd(hl_ref, l["base"], m)
            if px is None:
                a = reg.get(l["base_asset"])
                px = a.price_usd if a else None
            notional = float(base_amt) * px if px else None
        l["notional"] = notional

    # Drop dust below the USD floor (legs we can't price are kept).
    before = len(legs)
    legs = [l for l in legs if l["notional"] is None or l["notional"] >= args.min_notional]
    dropped = before - len(legs)
    print(f"Min-notional ${args.min_notional:g}: dropped {dropped} dust legs, kept {len(legs)}")

    out_rows = [{
        "timestamp": datetime.fromtimestamp(l["ts"], timezone.utc).strftime("%Y-%m-%dT%H:%M:%S"),
        "tx": l["tx"], "solver": l["solver"], "leg_class": l["leg_class"],
        "pair": l["pair"], "side": l["side"],
        "base_symbol": l["base"], "quote_symbol": l["quote"],
        "base_amount": f"{l['base_amount'].normalize():f}", "quote_amount": f"{l['quote_amount'].normalize():f}",
        "native_rate": fnum(l["native"], ".10g"),
        "binance_rate": fnum(l["b_rate"], ".10g"), "hyperliquid_rate": fnum(l["h_rate"], ".10g"),
        "edge_bps_binance": fnum(l["edge_bin"], ".2f"),
        "edge_bps_hyperliquid": fnum(l["edge_hl"], ".2f"),
        "notional_usd": fnum(l["notional"], ".2f"),
    } for l in legs]
    out_rows.sort(key=lambda r: r["timestamp"])
    write_csv(args.out, ["timestamp", "tx", "solver", "leg_class", "pair", "side", "base_symbol",
                         "quote_symbol", "base_amount", "quote_amount", "native_rate",
                         "binance_rate", "hyperliquid_rate", "edge_bps_binance",
                         "edge_bps_hyperliquid", "notional_usd"], out_rows)

    # Aggregations.
    agg_cols_tail = ["n_legs", "volume_usd", "binance_n", "binance_vw_edge_bps",
                     "binance_mean_edge_bps", "hl_n", "hl_vw_edge_bps", "hl_mean_edge_bps"]
    by_pair = aggregate(legs, ["pair"], lambda l: l["pair"])
    by_solver = aggregate(legs, ["solver"], lambda l: l["solver"])
    by_sp = aggregate(legs, ["solver", "pair"], lambda l: (l["solver"], l["pair"]))
    write_csv(f"{args.agg_prefix}_by_pair.csv", ["pair"] + agg_cols_tail, by_pair)
    write_csv(f"{args.agg_prefix}_by_solver.csv", ["solver"] + agg_cols_tail, by_solver)
    write_csv(f"{args.agg_prefix}_by_solver_pair.csv", ["solver", "pair"] + agg_cols_tail, by_sp)

    # Console summary.
    total_vol = sum(l["notional"] for l in legs if l["notional"])
    par_vol = sum(l["notional"] for l in legs if l["par"] and l["notional"])
    print(f"\nWrote {len(out_rows)} legs -> {args.out}")
    print(f"Total approx volume: ${total_vol:,.0f} "
          f"(par legs: ${par_vol:,.0f} across {sum(1 for l in legs if l['par'])} legs)")
    pcol = "hl_vw_edge_bps" if primary == "hl" else "binance_vw_edge_bps"
    plabel = f"{primary_name} bps(vw)"
    print(f"\n{'pair':16} {'legs':>5} {'volume$':>13} {plabel:>22}")
    for r in by_pair[:15]:
        print(f"{r['pair']:16} {r['n_legs']:>5} {float(r['volume_usd']):>13,.0f} "
              f"{r[pcol] or 'n/a':>22}")
    print(f"\n{'solver':34} {'legs':>5} {'volume$':>13} {plabel:>22}")
    for r in by_solver[:12]:
        print(f"{r['solver'][:34]:34} {r['n_legs']:>5} {float(r['volume_usd']):>13,.0f} "
              f"{r[pcol] or 'n/a':>22}")
    print(f"\nAggregates -> {args.agg_prefix}_by_pair.csv / _by_solver.csv / _by_solver_pair.csv")
    return 0


if __name__ == "__main__":
    sys.exit(main())
