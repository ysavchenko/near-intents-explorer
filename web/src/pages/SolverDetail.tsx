import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { apiGet, rangeParams, type AggRow, type BalanceRow, type DailyPoint, type SolverBalances } from "../api";
import { DataTable, ErrorBox, fmtUsd, SectionCard, Spinner, useRange } from "../components";
import { fillBuckets, VolumeBars } from "../charts";
import { aggTableCols } from "./Pairs";
import LegsSection from "./LegsTable";

const fmtBalance = (v: string) => {
  const n = Number(v);
  return n >= 1000 ? n.toLocaleString(undefined, { maximumFractionDigits: 2 }) : n.toPrecision(6);
};

const fmtPrice = (p: number | null) => {
  if (p == null) return "–";
  return p >= 1 ? `$${p.toLocaleString(undefined, { maximumFractionDigits: 2 })}` : `$${p.toPrecision(4)}`;
};

// Balances the solver holds inside intents.near (its working inventory),
// refreshed while the page is open.
function BalancesSection({ solver }: { solver: string }) {
  const q = useQuery({
    queryKey: ["solver-balances", solver],
    queryFn: () => apiGet<SolverBalances>(`/api/solvers/${encodeURIComponent(solver)}/balances`),
    refetchInterval: 30_000,
  });
  return (
    <SectionCard
      title="Balances on intents.near"
      right={
        q.data && (
          <span className="text-xs" style={{ color: "var(--text-secondary)" }}>
            total <span className="font-semibold">{fmtUsd(q.data.total_usd)}</span>
            {!q.data.total_complete && " (some unpriced)"}
          </span>
        )
      }
    >
      {q.error ? (
        <div className="px-4 pb-3 text-sm" style={{ color: "var(--status-critical)" }}>
          {String(q.error)}
        </div>
      ) : q.data ? (
        <DataTable
          cols={[
            { key: "token", label: "token", align: "left", value: (r: BalanceRow) => r.label, sortVal: (r: BalanceRow) => r.label },
            {
              key: "balance",
              label: "balance",
              value: (r: BalanceRow) => <span className="mono">{fmtBalance(r.balance)}</span>,
              sortVal: (r: BalanceRow) => Number(r.balance),
            },
            { key: "price", label: "price", value: (r: BalanceRow) => fmtPrice(r.price_usd), sortVal: (r: BalanceRow) => r.price_usd ?? 0 },
            {
              key: "value",
              label: "value",
              value: (r: BalanceRow) => fmtUsd(r.value_usd),
              sortVal: (r: BalanceRow) => r.value_usd ?? 0,
            },
          ]}
          rows={q.data.rows}
          rowKey={(r) => r.asset_id}
          defaultSort="value"
          emptyText="No balances on intents.near"
        />
      ) : (
        <Spinner />
      )}
    </SectionCard>
  );
}

type SolverResp = {
  solver: string;
  n_settlements: number;
  first_seen: string | null;
  pairs: AggRow[];
};

export default function SolverDetail() {
  const { id = "" } = useParams();
  const { hours, minNotional } = useRange();
  const bucket = hours <= 72 ? "hour" : "day";

  const q = useQuery({
    queryKey: ["solver", id, hours, minNotional],
    queryFn: () =>
      apiGet<SolverResp>(`/api/solvers/${encodeURIComponent(id)}`, {
        ...rangeParams(hours, minNotional),
        par: "diff",
      }),
  });
  const daily = useQuery({
    queryKey: ["daily-solver", id, hours, minNotional],
    queryFn: () =>
      apiGet<{ from: string; to: string; rows: DailyPoint[] }>("/api/daily", {
        ...rangeParams(hours, minNotional),
        group: "none",
        solver: id,
        bucket,
        par: "all",
      }),
  });

  if (q.error) return <ErrorBox error={q.error} />;
  if (!q.data) return <Spinner />;
  const s = q.data;
  const series = daily.data ? fillBuckets(daily.data.rows, daily.data.from, daily.data.to, bucket) : [];

  return (
    <>
      <div className="flex items-baseline gap-3">
        <h1 className="mono text-lg font-bold break-all">{s.solver}</h1>
        <span className="shrink-0 text-xs" style={{ color: "var(--text-muted)" }}>
          {s.n_settlements.toLocaleString()} settlements
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
      <BalancesSection solver={id} />
      <SectionCard title="Pairs (real spreads)">
        <DataTable cols={aggTableCols(true, id)} rows={s.pairs} rowKey={(r) => r.pair!} defaultSort="vol" />
      </SectionCard>
      <LegsSection filter={{ solver: id }} title="Legs" />
    </>
  );
}
