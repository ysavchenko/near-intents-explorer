import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { apiGet, rangeParams, type AggRow, type Summary } from "../api";
import { Bps, DataTable, ErrorBox, fmtNum, fmtUsd, SectionCard, Spinner, StatTile, useRange } from "../components";
import { VolumeBars } from "../charts";

export default function Overview() {
  const { hours, minNotional } = useRange();
  const params = rangeParams(hours, minNotional);

  const summary = useQuery({
    queryKey: ["summary", hours, minNotional],
    queryFn: () => apiGet<Summary>("/api/summary", params),
    refetchInterval: 30_000,
  });
  const pairs = useQuery({
    queryKey: ["pairs", hours, minNotional],
    queryFn: () => apiGet<{ rows: AggRow[] }>("/api/pairs", params),
  });
  const solvers = useQuery({
    queryKey: ["solvers", hours, minNotional],
    queryFn: () => apiGet<{ rows: AggRow[] }>("/api/solvers", params),
  });

  if (summary.error) return <ErrorBox error={summary.error} />;
  if (!summary.data) return <Spinner />;
  const s = summary.data;

  const aggCols = (keyCol: "pair" | "solver") => [
    {
      key: keyCol,
      label: keyCol,
      align: "left" as const,
      value: (r: AggRow) =>
        keyCol === "pair" ? (
          <Link to={`/pair?p=${encodeURIComponent(r.pair!)}`} style={{ color: "var(--series-1)" }}>
            {r.pair}
          </Link>
        ) : (
          <Link to={`/solvers/${encodeURIComponent(r.solver!)}`} className="mono" style={{ color: "var(--series-1)" }}>
            {r.solver!.length > 30 ? r.solver!.slice(0, 30) + "…" : r.solver}
          </Link>
        ),
      sortVal: (r: AggRow) => (keyCol === "pair" ? r.pair! : r.solver!),
    },
    { key: "n", label: "legs", value: (r: AggRow) => fmtNum(r.n_legs), sortVal: (r: AggRow) => r.n_legs },
    { key: "vol", label: "volume", value: (r: AggRow) => fmtUsd(r.volume_usd), sortVal: (r: AggRow) => r.volume_usd },
    {
      key: "vw",
      label: "vw edge (HL)",
      value: (r: AggRow) => <Bps v={r.hl_vw_edge_bps} />,
      sortVal: (r: AggRow) => r.hl_vw_edge_bps ?? -1e9,
    },
  ];

  return (
    <>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatTile label="Volume" value={fmtUsd(s.volume_usd)} sub={`${fmtNum(s.legs)} legs`} />
        <StatTile
          label="Settlements"
          value={fmtNum(s.settlements)}
          sub={s.settlements_failed > 0 ? `${fmtNum(s.settlements_failed)} failed` : "all succeeded"}
        />
        <StatTile label="Active solvers" value={fmtNum(s.active_solvers)} />
        <StatTile
          label="Swap volume (price-bearing)"
          value={fmtUsd(s.volume_by_class["interesting"] ?? 0)}
          sub={`bridges ${fmtUsd(s.volume_by_class["same_asset"] ?? 0)} · stables ${fmtUsd(
            s.volume_by_class["stable_stable"] ?? 0,
          )}`}
        />
      </div>

      <SectionCard title="Volume by hour (USD)">
        <div className="px-2 pb-2">
          <VolumeBars data={s.hourly} bucket="hour" />
        </div>
      </SectionCard>

      <div className="grid gap-4 lg:grid-cols-2">
        <SectionCard title="Top pairs">
          {pairs.data ? (
            <DataTable
              cols={aggCols("pair")}
              rows={pairs.data.rows.slice(0, 12)}
              rowKey={(r) => r.pair!}
              defaultSort="vol"
            />
          ) : (
            <Spinner />
          )}
        </SectionCard>
        <SectionCard title="Top solvers">
          {solvers.data ? (
            <DataTable
              cols={aggCols("solver")}
              rows={solvers.data.rows.slice(0, 12)}
              rowKey={(r) => r.solver!}
              defaultSort="vol"
            />
          ) : (
            <Spinner />
          )}
        </SectionCard>
      </div>
    </>
  );
}
