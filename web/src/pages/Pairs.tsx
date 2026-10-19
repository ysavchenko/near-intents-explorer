import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { apiGet, rangeParams, type AggRow } from "../api";
import { Bps, type Col, DataTable, ErrorBox, fmtNum, fmtUsd, SectionCard, Spinner, UsdDelta, useRange } from "../components";

export function ParFilter({ par, setPar }: { par: string; setPar: (p: string) => void }) {
  const opts = [
    { v: "diff", label: "swaps + real spreads" },
    { v: "all", label: "everything" },
    { v: "off", label: "swaps only" },
  ];
  return (
    <select
      value={par}
      onChange={(e) => setPar(e.target.value)}
      className="rounded border px-1.5 py-0.5 text-xs"
      style={{ borderColor: "var(--border)", background: "var(--surface-1)", color: "var(--text-secondary)" }}
    >
      {opts.map((o) => (
        <option key={o.v} value={o.v}>
          {o.label}
        </option>
      ))}
    </select>
  );
}

// pairUrl builds a pair-detail link, optionally scoped to one solver.
export const pairUrl = (pair: string, solver?: string) =>
  `/pair?p=${encodeURIComponent(pair)}` + (solver ? `&solver=${encodeURIComponent(solver)}` : "");

// Edge columns shared by the pair / solver / direction tables. Headline numbers
// are vs Binance spot in the leg's own quote currency; the Hyperliquid perp mid
// (USDT-indexed, a few bps under spot) is shown as a secondary view.
export const edgeCols = (): Col<AggRow>[] => [
  {
    key: "fees",
    label: "fees",
    value: (r) => <UsdDelta v={r.binance_fees_usd} />,
    sortVal: (r) => r.binance_fees_usd ?? -1e9,
  },
  { key: "priced", label: "priced", value: (r) => fmtNum(r.binance_n), sortVal: (r) => r.binance_n },
  {
    key: "vw",
    label: "vw (spot)",
    value: (r) => <Bps v={r.binance_vw_edge_bps} />,
    sortVal: (r) => r.binance_vw_edge_bps ?? -1e9,
  },
  {
    key: "mean",
    label: "mean (spot)",
    value: (r) => <Bps v={r.binance_mean_edge_bps} />,
    sortVal: (r) => r.binance_mean_edge_bps ?? -1e9,
  },
  {
    key: "perp",
    label: "vw (HL perp)",
    value: (r) => <Bps v={r.hl_vw_edge_bps} />,
    sortVal: (r) => r.hl_vw_edge_bps ?? -1e9,
  },
];

export const aggTableCols = (linkPair: boolean, solver?: string) => [
  {
    key: "pair",
    label: "pair (recv/give)",
    align: "left" as const,
    value: (r: AggRow) =>
      linkPair ? (
        <Link to={pairUrl(r.pair!, solver)} style={{ color: "var(--series-1)" }}>
          {r.pair}
        </Link>
      ) : (
        r.pair
      ),
    sortVal: (r: AggRow) => r.pair ?? "",
  },
  { key: "n", label: "legs", value: (r: AggRow) => fmtNum(r.n_legs), sortVal: (r: AggRow) => r.n_legs },
  { key: "vol", label: "volume", value: (r: AggRow) => fmtUsd(r.volume_usd), sortVal: (r: AggRow) => r.volume_usd },
  ...edgeCols(),
];

export default function Pairs() {
  const { hours, minNotional } = useRange();
  const [par, setPar] = useState("diff");
  const q = useQuery({
    queryKey: ["pairs", hours, minNotional, par],
    queryFn: () => apiGet<{ rows: AggRow[] }>("/api/pairs", { ...rangeParams(hours, minNotional), par }),
  });
  if (q.error) return <ErrorBox error={q.error} />;
  return (
    <SectionCard title="Pairs" right={<ParFilter par={par} setPar={setPar} />}>
      {q.data ? (
        <DataTable cols={aggTableCols(true)} rows={q.data.rows} rowKey={(r) => r.pair!} defaultSort="vol" />
      ) : (
        <Spinner />
      )}
    </SectionCard>
  );
}
