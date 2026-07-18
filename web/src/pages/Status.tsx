import { useQuery } from "@tanstack/react-query";
import { apiGet, type Status } from "../api";
import { DataTable, ErrorBox, fmtNum, SectionCard, Spinner, StatTile } from "../components";

export default function StatusPage() {
  const q = useQuery({
    queryKey: ["status"],
    queryFn: () => apiGet<Status>("/api/status"),
    refetchInterval: 10_000,
  });
  if (q.error) return <ErrorBox error={q.error} />;
  if (!q.data) return <Spinner />;
  const s = q.data;

  // ~600ms blocks: lag in blocks ≈ lag in seconds × 1.7.
  const lagOk = s.lag_blocks < 120;
  const counters = Object.entries(s.counters).sort(([a], [b]) => a.localeCompare(b));

  return (
    <>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatTile
          label="Indexer lag"
          value={
            <span style={{ color: lagOk ? "var(--status-good)" : "var(--status-critical)" }}>
              {fmtNum(s.lag_blocks)} blocks
            </span>
          }
          sub={`head ${fmtNum(s.chain_head)} · indexed ${fmtNum(s.indexed_head)}`}
        />
        <StatTile
          label="Pricing backlog"
          value={fmtNum(s.pricing_backlog)}
          sub={`venues: ${s.venues.join(", ")}`}
        />
        <StatTile
          label="Uptime"
          value={`${Math.floor(s.uptime_s / 3600)}h ${Math.floor((s.uptime_s % 3600) / 60)}m`}
          sub={`since ${new Date(s.started_at).toISOString().slice(0, 16)}Z`}
        />
        <StatTile
          label="Assets / learned solvers"
          value={`${s.assets} / ${s.solvers_learned}`}
          sub="registry size / frequency-promoted"
        />
      </div>
      <SectionCard title="Counters (since process start)">
        <DataTable
          cols={[
            { key: "k", label: "counter", align: "left", value: (r: [string, number]) => r[0] },
            { key: "v", label: "value", value: (r: [string, number]) => fmtNum(r[1]), sortVal: (r: [string, number]) => r[1] },
          ]}
          rows={counters}
          rowKey={(r) => r[0]}
        />
      </SectionCard>
    </>
  );
}
