import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { apiGet, rangeParams, type AggRow, type DailyPoint } from "../api";
import { Bps, DataTable, ErrorBox, fmtNum, fmtUsd, SectionCard, shortHash, Spinner, UsdDelta, useRange } from "../components";
import { EdgeLine, fillBuckets, VolumeBars } from "../charts";
import LegsSection from "./LegsTable";

// Pair detail, optionally scoped to one solver (?solver=) and/or one concrete
// asset direction (?from=&to=, e.g. USDT@eth -> USDT@tron within USDT/USDT).
export default function PairDetail() {
  const [params] = useSearchParams();
  const pair = params.get("p") ?? "";
  const solver = params.get("solver") ?? "";
  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";
  const { hours, minNotional } = useRange();
  const bucket = hours <= 72 ? "hour" : "day";

  const scope: Record<string, string> = { pair };
  if (solver) scope.solver = solver;
  if (from) scope.from_asset = from;
  if (to) scope.to_asset = to;

  const daily = useQuery({
    queryKey: ["daily-pair", scope, hours, minNotional],
    queryFn: () =>
      apiGet<{ from: string; to: string; rows: DailyPoint[] }>("/api/daily", {
        ...rangeParams(hours, minNotional),
        group: "none",
        bucket,
        par: "all",
        ...scope,
      }),
    enabled: pair !== "",
  });
  const dirs = useQuery({
    queryKey: ["pair-directions", pair, solver, hours, minNotional],
    queryFn: () =>
      apiGet<{ rows: AggRow[] }>("/api/pairs/directions", {
        ...rangeParams(hours, minNotional),
        pair,
        par: "all",
        ...(solver ? { solver } : {}),
      }),
    enabled: pair !== "",
  });

  if (!pair) return <ErrorBox error="no pair given" />;
  if (daily.error) return <ErrorBox error={daily.error} />;

  const series = daily.data ? fillBuckets(daily.data.rows, daily.data.from, daily.data.to, bucket) : [];
  const dirUrl = (r: AggRow) =>
    `/pair?p=${encodeURIComponent(pair)}` +
    (solver ? `&solver=${encodeURIComponent(solver)}` : "") +
    `&from=${encodeURIComponent(r.from_asset!)}&to=${encodeURIComponent(r.to_asset!)}`;
  const activeDir = (dirs.data?.rows ?? []).find((r) => r.from_asset === from && r.to_asset === to);

  const dirCols = [
    {
      key: "dir",
      label: "direction (recv → give)",
      align: "left" as const,
      value: (r: AggRow) => {
        const active = r.from_asset === from && r.to_asset === to;
        return (
          <Link to={dirUrl(r)} style={{ color: "var(--series-1)", fontWeight: active ? 700 : 400 }}>
            {r.from_label} → {r.to_label}
          </Link>
        );
      },
      sortVal: (r: AggRow) => `${r.from_label} → ${r.to_label}`,
    },
    { key: "n", label: "legs", value: (r: AggRow) => fmtNum(r.n_legs), sortVal: (r: AggRow) => r.n_legs },
    { key: "vol", label: "volume", value: (r: AggRow) => fmtUsd(r.volume_usd), sortVal: (r: AggRow) => r.volume_usd },
    {
      key: "fees",
      label: "fees (HL)",
      value: (r: AggRow) => <UsdDelta v={r.hl_fees_usd} />,
      sortVal: (r: AggRow) => r.hl_fees_usd ?? -1e9,
    },
    { key: "hn", label: "HL n", value: (r: AggRow) => fmtNum(r.hl_n), sortVal: (r: AggRow) => r.hl_n },
    {
      key: "hvw",
      label: "HL vw bps",
      value: (r: AggRow) => <Bps v={r.hl_vw_edge_bps} />,
      sortVal: (r: AggRow) => r.hl_vw_edge_bps ?? -1e9,
    },
    {
      key: "hmean",
      label: "HL mean bps",
      value: (r: AggRow) => <Bps v={r.hl_mean_edge_bps} />,
      sortVal: (r: AggRow) => r.hl_mean_edge_bps ?? -1e9,
    },
    {
      key: "bvw",
      label: "Binance vw bps",
      value: (r: AggRow) => <Bps v={r.binance_vw_edge_bps} />,
      sortVal: (r: AggRow) => r.binance_vw_edge_bps ?? -1e9,
    },
  ];

  const scopeLabel = activeDir ? `${activeDir.from_label} → ${activeDir.to_label}` : from ? "direction" : "";

  return (
    <>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h1 className="text-xl font-bold">{pair}</h1>
        {scopeLabel && (
          <span className="text-sm font-semibold">
            {scopeLabel}{" "}
            <Link
              to={`/pair?p=${encodeURIComponent(pair)}${solver ? `&solver=${encodeURIComponent(solver)}` : ""}`}
              className="text-xs font-normal"
              style={{ color: "var(--text-muted)" }}
            >
              ✕ all directions
            </Link>
          </span>
        )}
        {solver && (
          <span className="mono text-sm" title={solver}>
            {shortHash(solver, 30)}{" "}
            <Link to={`/solvers/${encodeURIComponent(solver)}`} className="text-xs" style={{ color: "var(--series-1)" }}>
              solver page
            </Link>{" "}
            <Link
              to={`/pair?p=${encodeURIComponent(pair)}${from ? `&from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}` : ""}`}
              className="text-xs"
              style={{ color: "var(--text-muted)" }}
            >
              ✕ all solvers
            </Link>
          </span>
        )}
        <span className="text-xs" style={{ color: "var(--text-muted)" }}>
          received/given by the solver ·{" "}
          <Link to="/pairs" style={{ color: "var(--series-1)" }}>
            all pairs
          </Link>
        </span>
      </div>
      {daily.data ? (
        <div className="grid gap-4 lg:grid-cols-2">
          <SectionCard title={`Volume by ${bucket} (USD)`}>
            <div className="px-2 pb-2">
              <VolumeBars data={series} bucket={bucket} height={200} />
            </div>
          </SectionCard>
          <SectionCard title={`VW edge by ${bucket} (bps vs Hyperliquid)`}>
            <div className="px-2 pb-2">
              <EdgeLine data={series} bucket={bucket} height={200} />
            </div>
          </SectionCard>
        </div>
      ) : (
        <Spinner />
      )}
      {dirs.data && dirs.data.rows.length > 1 && (
        <SectionCard title="Directions (by concrete asset)">
          <DataTable
            cols={dirCols}
            rows={dirs.data.rows}
            rowKey={(r) => `${r.from_asset}:${r.to_asset}`}
            defaultSort="vol"
          />
        </SectionCard>
      )}
      <LegsSection filter={scope} title={`Legs — ${scopeLabel || pair}`} />
    </>
  );
}
