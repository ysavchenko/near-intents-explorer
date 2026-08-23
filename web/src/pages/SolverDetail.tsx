import { useState } from "react";
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

const fmtShare = (value: number | null, total: number) => {
  if (value == null || !(total > 0)) return "–";
  const pct = (value / total) * 100;
  if (pct < 0.01) return "<0.01%";
  return `${pct.toLocaleString(undefined, { maximumFractionDigits: pct >= 10 ? 1 : 2 })}%`;
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
        <span className="text-xs" style={{ color: "var(--text-secondary)" }}>
          {q.data && (
            <>
              total <span className="font-semibold">{fmtUsd(q.data.total_usd)}</span>
              {!q.data.total_complete && " (some unpriced)"}
              {" · "}
            </>
          )}
          <Link to={`/solvers/${encodeURIComponent(solver)}/flows`} style={{ color: "var(--series-1)" }}>
            deposits & withdrawals →
          </Link>
        </span>
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
            {
              key: "share",
              label: "% of book",
              value: (r: BalanceRow) => fmtShare(r.value_usd, q.data.total_usd),
              sortVal: (r: BalanceRow) => r.value_usd ?? 0,
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

type ActivityMetric = "volume" | "fees";

// Segmented volume/fees switch for the activity chart. Fees are HL-mid fees
// (same series as the solvers table), so a bucket can go negative.
function MetricToggle({ metric, setMetric }: { metric: ActivityMetric; setMetric: (m: ActivityMetric) => void }) {
  return (
    <div className="flex overflow-hidden rounded border text-xs" style={{ borderColor: "var(--border)" }}>
      {(["volume", "fees"] as const).map((m) => (
        <button
          key={m}
          onClick={() => setMetric(m)}
          className="cursor-pointer px-2 py-0.5"
          style={
            m === metric
              ? { background: "color-mix(in oklab, var(--series-1) 14%, transparent)", color: "var(--text-primary)" }
              : { color: "var(--text-muted)" }
          }
        >
          {m}
        </button>
      ))}
    </div>
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
  const [metric, setMetric] = useState<ActivityMetric>("volume");

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
      <SectionCard
        title={`Activity by ${bucket} — ${metric} (USD)`}
        right={<MetricToggle metric={metric} setMetric={setMetric} />}
      >
        <div className="px-2 pb-2">
          {daily.data ? <VolumeBars data={series} bucket={bucket} height={180} metric={metric} /> : <Spinner />}
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
