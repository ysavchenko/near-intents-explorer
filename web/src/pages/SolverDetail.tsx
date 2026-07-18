import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { apiGet, rangeParams, type AggRow, type DailyPoint } from "../api";
import { DataTable, ErrorBox, SectionCard, Spinner, useRange } from "../components";
import { VolumeBars } from "../charts";
import { aggTableCols } from "./Pairs";
import LegsSection from "./LegsTable";

type SolverResp = {
  solver: string;
  is_seed: boolean;
  n_settlements: number;
  first_seen: string | null;
  pairs: AggRow[];
};

export default function SolverDetail() {
  const { id = "" } = useParams();
  const { hours } = useRange();
  const bucket = hours <= 72 ? "hour" : "day";

  const q = useQuery({
    queryKey: ["solver", id, hours],
    queryFn: () => apiGet<SolverResp>(`/api/solvers/${encodeURIComponent(id)}`, { ...rangeParams(hours), par: "diff" }),
  });
  const daily = useQuery({
    queryKey: ["daily-solver", id, hours],
    queryFn: () =>
      apiGet<{ rows: DailyPoint[] }>("/api/daily", {
        ...rangeParams(hours),
        group: "solver",
        bucket,
        par: "all",
      }),
  });

  if (q.error) return <ErrorBox error={q.error} />;
  if (!q.data) return <Spinner />;
  const s = q.data;
  const series = (daily.data?.rows ?? []).filter((r) => r.key === id);

  return (
    <>
      <div className="flex items-baseline gap-3">
        <h1 className="mono text-lg font-bold break-all">{s.solver}</h1>
        <span className="shrink-0 text-xs" style={{ color: "var(--text-muted)" }}>
          {s.is_seed ? "seed solver" : "learned by frequency"} · {s.n_settlements.toLocaleString()} settlements
          {" · "}
          <Link to="/solvers" style={{ color: "var(--series-1)" }}>
            all solvers
          </Link>
        </span>
      </div>
      <SectionCard title={`Activity by ${bucket} (USD)`}>
        <div className="px-2 pb-2">
          {daily.data ? <VolumeBars data={series} bucket={bucket} height={180} /> : <Spinner />}
        </div>
      </SectionCard>
      <SectionCard title="Pairs (real spreads)">
        <DataTable cols={aggTableCols(true)} rows={s.pairs} rowKey={(r) => r.pair!} defaultSort="vol" />
      </SectionCard>
      <LegsSection filter={{ solver: id }} title="Legs" />
    </>
  );
}
