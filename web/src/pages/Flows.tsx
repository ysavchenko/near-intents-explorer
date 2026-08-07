import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { apiGet, rangeParams, type FlowRow, type FlowsResp } from "../api";
import { DataTable, ErrorBox, fmtTime, fmtUsd, SectionCard, shortHash, Spinner, useRange } from "../components";

// Deposit/withdrawal/transfer history of solver inventory on intents.near.
// Transfers with `counterparty_withdrew` are bridge-outs via a helper account
// that withdrew in the same settlement (the HOT-bridge pattern).

const DIRECTIONS: Record<FlowRow["direction"], { label: string; color: string }> = {
  deposit: { label: "deposit", color: "var(--status-good)" },
  withdrawal: { label: "withdrawal", color: "var(--status-critical)" },
  transfer_in: { label: "transfer in", color: "var(--series-5)" },
  transfer_out: { label: "transfer out", color: "var(--series-6)" },
};

function DirectionBadge({ row }: { row: FlowRow }) {
  const d = DIRECTIONS[row.direction];
  const bridged = row.direction === "transfer_out" && row.counterparty_withdrew;
  return (
    <span
      className="rounded px-1.5 py-0.5 text-xs font-semibold whitespace-nowrap"
      style={{ color: d.color, background: `color-mix(in oklab, ${d.color} 12%, transparent)` }}
      title={bridged ? "counterparty withdrew in the same settlement (bridge-out)" : undefined}
    >
      {d.label}
      {bridged && " ↗"}
    </span>
  );
}

const fmtAmount = (v: string | null) => {
  if (v == null) return "–";
  const n = Number(v);
  return n >= 1000 ? n.toLocaleString(undefined, { maximumFractionDigits: 2 }) : n.toPrecision(6);
};

// The "other end" of a flow: external address when recoverable, else the
// NEAR-side counterparty; deposits show bridged provenance.
function Counterparty({ row }: { row: FlowRow }) {
  const muted = { color: "var(--text-muted)" };
  const parts: React.ReactNode[] = [];
  if (row.direction === "deposit") {
    if (row.origin_chain) {
      parts.push(
        <span key="o" title={row.origin_tx ?? undefined}>
          <span style={muted}>from </span>
          {row.origin_chain}
          {row.origin_tx && <span className="mono"> {shortHash(row.origin_tx)}</span>}
        </span>,
      );
    } else if (row.counterparty) {
      parts.push(
        <span key="c" className="mono" title={row.counterparty}>
          <span style={muted}>from </span>
          {shortHash(row.counterparty, 18)}
        </span>,
      );
    }
  } else {
    const dir = row.direction === "transfer_in" ? "from" : "to";
    if (row.counterparty) {
      parts.push(
        <span key="c" className="mono" title={row.counterparty}>
          <span style={muted}>{dir} </span>
          {shortHash(row.counterparty, 18)}
        </span>,
      );
    }
    if (row.external_address) {
      parts.push(
        <span key="e" className="mono" title={row.external_address}>
          <span style={muted}> → </span>
          {shortHash(row.external_address, 14)}
        </span>,
      );
    }
  }
  if (parts.length === 0) return <span style={muted}>–</span>;
  return <span className="whitespace-nowrap">{parts}</span>;
}

export function FlowsSection({ solver, title = "Deposits & withdrawals" }: { solver?: string; title?: string }) {
  const { hours } = useRange();
  const [direction, setDirection] = useState("");
  const [solverInput, setSolverInput] = useState("");
  const [solverFilter, setSolverFilter] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setSolverFilter(solverInput.trim()), 400);
    return () => clearTimeout(t);
  }, [solverInput]);

  const effSolver = solver ?? solverFilter;
  const q = useQuery({
    queryKey: ["flows", hours, effSolver, direction],
    queryFn: () =>
      apiGet<FlowsResp>("/api/flows", {
        ...rangeParams(hours),
        limit: "500",
        solver: effSolver,
        direction,
      }),
  });

  const totals = q.data?.totals ?? {};
  const usd = (d: string) => totals[d]?.value_usd ?? 0;
  const inflow = usd("deposit") + usd("transfer_in");
  const outflow = usd("withdrawal") + usd("transfer_out");
  const net = inflow - outflow;
  const complete = Object.values(totals).every((t) => t.complete);

  return (
    <SectionCard
      title={title}
      right={
        q.data && (
          <span className="flex flex-wrap items-center gap-2 text-xs" style={{ color: "var(--text-secondary)" }}>
            <span>
              in <span className="font-semibold" style={{ color: "var(--status-good)" }}>{fmtUsd(inflow)}</span>
              {" · "}out{" "}
              <span className="font-semibold" style={{ color: "var(--status-critical)" }}>{fmtUsd(outflow)}</span>
              {" · "}net{" "}
              <span className="font-semibold" style={{ color: net >= 0 ? "var(--status-good)" : "var(--status-critical)" }}>
                {net >= 0 ? "+" : "−"}
                {fmtUsd(Math.abs(net))}
              </span>
              {!complete && " (some unpriced)"}
            </span>
            {solver == null && (
              <input
                value={solverInput}
                onChange={(e) => setSolverInput(e.target.value)}
                placeholder="filter solver…"
                className="mono rounded border px-1.5 py-0.5 text-xs"
                style={{ borderColor: "var(--border)", background: "var(--surface-1)", width: "16rem" }}
              />
            )}
            <select
              value={direction}
              onChange={(e) => setDirection(e.target.value)}
              className="rounded border px-1 py-0.5 text-xs"
              style={{ borderColor: "var(--border)", background: "var(--surface-1)" }}
            >
              <option value="">all directions</option>
              <option value="deposit">deposits</option>
              <option value="withdrawal">withdrawals</option>
              <option value="transfer_in">transfers in</option>
              <option value="transfer_out">transfers out</option>
            </select>
          </span>
        )
      }
    >
      {q.error ? (
        <ErrorBox error={q.error} />
      ) : q.data ? (
        <DataTable
          cols={[
            {
              key: "ts",
              label: "time",
              align: "left",
              value: (r: FlowRow) => <span className="mono">{fmtTime(r.ts)}</span>,
              sortVal: (r: FlowRow) => r.ts,
            },
            {
              key: "tx",
              label: "tx",
              align: "left",
              value: (r: FlowRow) => (
                <a
                  className="mono"
                  style={{ color: "var(--series-1)" }}
                  href={`https://nearblocks.io/txns/${r.tx}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  {shortHash(r.tx)}
                </a>
              ),
            },
            ...(solver == null
              ? [
                  {
                    key: "solver",
                    label: "solver",
                    align: "left" as const,
                    value: (r: FlowRow) => (
                      <Link
                        className="mono"
                        style={{ color: "var(--series-1)" }}
                        to={`/solvers/${encodeURIComponent(r.solver)}`}
                        title={r.solver}
                      >
                        {shortHash(r.solver, 18)}
                      </Link>
                    ),
                    sortVal: (r: FlowRow) => r.solver,
                  },
                ]
              : []),
            {
              key: "direction",
              label: "direction",
              align: "left",
              value: (r: FlowRow) => <DirectionBadge row={r} />,
              sortVal: (r: FlowRow) => r.direction,
            },
            {
              key: "amount",
              label: "amount",
              value: (r: FlowRow) => (
                <span className="mono">
                  {fmtAmount(r.amount)} {r.label}
                </span>
              ),
              sortVal: (r: FlowRow) => Number(r.amount ?? 0),
            },
            {
              key: "value",
              label: "value",
              value: (r: FlowRow) => fmtUsd(r.value_usd),
              sortVal: (r: FlowRow) => r.value_usd ?? 0,
            },
            {
              key: "counterparty",
              label: "counterparty / destination",
              align: "left",
              value: (r: FlowRow) => <Counterparty row={r} />,
            },
            {
              key: "memo",
              label: "memo",
              align: "left",
              value: (r: FlowRow) => (
                <span className="text-xs" style={{ color: "var(--text-muted)" }}>
                  {r.memo ?? ""}
                </span>
              ),
            },
          ]}
          rows={q.data.rows}
          rowKey={(r) => `${r.receipt_id}:${r.seq}`}
          defaultSort="ts"
          emptyText="No flows in this window"
        />
      ) : (
        <Spinner />
      )}
    </SectionCard>
  );
}

export default function FlowsPage() {
  return <FlowsSection title="Solver deposits & withdrawals" />;
}
