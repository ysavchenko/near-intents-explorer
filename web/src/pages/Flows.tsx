import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { apiGet, rangeParams, type FlowRow, type FlowsResp } from "../api";
import { DataTable, ErrorBox, fmtTime, fmtUsd, SectionCard, shortHash, Spinner, useRange } from "../components";

// Deposit/withdrawal/transfer history of solver inventory on intents.near.
// Transfers with `counterparty_withdrew` are bridge-outs via a helper account
// that withdrew in the same settlement (the HOT-bridge pattern).

// External-chain explorers, keyed by the registry's blockchain slug.
const EXPLORERS: Record<string, { addr: (a: string) => string; tx: (h: string) => string }> = {
  eth: { addr: (a) => `https://etherscan.io/address/${a}`, tx: (h) => `https://etherscan.io/tx/${h}` },
  bsc: { addr: (a) => `https://bscscan.com/address/${a}`, tx: (h) => `https://bscscan.com/tx/${h}` },
  arb: { addr: (a) => `https://arbiscan.io/address/${a}`, tx: (h) => `https://arbiscan.io/tx/${h}` },
  base: { addr: (a) => `https://basescan.org/address/${a}`, tx: (h) => `https://basescan.org/tx/${h}` },
  op: { addr: (a) => `https://optimistic.etherscan.io/address/${a}`, tx: (h) => `https://optimistic.etherscan.io/tx/${h}` },
  pol: { addr: (a) => `https://polygonscan.com/address/${a}`, tx: (h) => `https://polygonscan.com/tx/${h}` },
  gnosis: { addr: (a) => `https://gnosisscan.io/address/${a}`, tx: (h) => `https://gnosisscan.io/tx/${h}` },
  avax: { addr: (a) => `https://snowtrace.io/address/${a}`, tx: (h) => `https://snowtrace.io/tx/${h}` },
  bera: { addr: (a) => `https://berascan.com/address/${a}`, tx: (h) => `https://berascan.com/tx/${h}` },
  sol: { addr: (a) => `https://solscan.io/account/${a}`, tx: (h) => `https://solscan.io/tx/${h}` },
  tron: { addr: (a) => `https://tronscan.org/#/address/${a}`, tx: (h) => `https://tronscan.org/#/transaction/${h}` },
  btc: { addr: (a) => `https://mempool.space/address/${a}`, tx: (h) => `https://mempool.space/tx/${h}` },
  ltc: { addr: (a) => `https://blockchair.com/litecoin/address/${a}`, tx: (h) => `https://blockchair.com/litecoin/transaction/${h}` },
  doge: { addr: (a) => `https://blockchair.com/dogecoin/address/${a}`, tx: (h) => `https://blockchair.com/dogecoin/transaction/${h}` },
  zec: { addr: (a) => `https://blockchair.com/zcash/address/${a}`, tx: (h) => `https://blockchair.com/zcash/transaction/${h}` },
  xrp: { addr: (a) => `https://xrpscan.com/account/${a}`, tx: (h) => `https://xrpscan.com/tx/${h}` },
  ton: { addr: (a) => `https://tonviewer.com/${a}`, tx: (h) => `https://tonviewer.com/transaction/${h}` },
  near: { addr: (a) => `https://nearblocks.io/address/${a}`, tx: (h) => `https://nearblocks.io/txns/${h}` },
};

// Full-width address, linked to an explorer when one is known. NEAR accounts
// always resolve to nearblocks; external ones use the asset's chain.
function Addr({ value, chain, near }: { value: string; chain?: string; near?: boolean }) {
  const url = near ? EXPLORERS.near.addr(value) : chain && EXPLORERS[chain] ? EXPLORERS[chain].addr(value) : null;
  const text = <span className="mono break-all">{value}</span>;
  if (!url) return text;
  return (
    <a href={url} target="_blank" rel="noreferrer" style={{ color: "var(--series-1)" }}>
      {text}
    </a>
  );
}

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

// The "other end" of a flow, one line per hop: the NEAR-side counterparty
// and/or the external-chain address / origin tx, fully displayed and linked
// to the matching explorer.
function Counterparty({ row }: { row: FlowRow }) {
  const muted = { color: "var(--text-muted)" };
  const lines: React.ReactNode[] = [];
  if (row.direction === "deposit") {
    if (row.origin_chain) {
      lines.push(
        <div key="o">
          <span style={muted}>from {row.origin_chain} </span>
          {row.origin_tx &&
            (EXPLORERS[row.origin_chain] ? (
              <a
                href={EXPLORERS[row.origin_chain].tx(row.origin_tx)}
                target="_blank"
                rel="noreferrer"
                style={{ color: "var(--series-1)" }}
              >
                <span className="mono break-all">{row.origin_tx}</span>
              </a>
            ) : (
              <span className="mono break-all">{row.origin_tx}</span>
            ))}
        </div>,
      );
    } else if (row.counterparty) {
      lines.push(
        <div key="c">
          <span style={muted}>from </span>
          <Addr value={row.counterparty} near />
        </div>,
      );
    }
  } else {
    const dir = row.direction === "transfer_in" ? "from" : "to";
    if (row.counterparty) {
      lines.push(
        <div key="c">
          <span style={muted}>{dir} </span>
          <Addr value={row.counterparty} near />
        </div>,
      );
    }
    if (row.external_address) {
      lines.push(
        <div key="e">
          <span style={muted}>→ {row.chain} </span>
          <Addr value={row.external_address} chain={row.chain} />
        </div>,
      );
    }
  }
  if (lines.length === 0) return <span style={muted}>–</span>;
  return (
    <div
      className="space-y-0.5 text-xs"
      style={{ minWidth: "18rem", maxWidth: "24rem", whiteSpace: "normal" }}
    >
      {lines}
    </div>
  );
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
              // deposit/withdraw memos restate the direction badge; WITHDRAW_TO
              // restates the destination column — show only informative memos,
              // clamped (some transfer memos are huge base64 blobs).
              value: (r: FlowRow) => {
                const memo = r.memo ?? "";
                if (memo === "deposit" || memo === "withdraw" || memo.startsWith("WITHDRAW_TO:")) return null;
                return (
                  <span className="block text-xs" style={{ color: "var(--text-muted)", maxWidth: "10rem" }} title={memo}>
                    {memo.length > 16 ? memo.slice(0, 16) + "…" : memo}
                  </span>
                );
              },
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

// Standalone per-solver flow history (linked from the balances card).
export function SolverFlowsPage() {
  const { id = "" } = useParams();
  return (
    <>
      <div className="flex items-baseline gap-3">
        <h1 className="mono text-lg font-bold break-all">{id}</h1>
        <span className="shrink-0 text-xs" style={{ color: "var(--text-muted)" }}>
          <Link to={`/solvers/${encodeURIComponent(id)}`} style={{ color: "var(--series-1)" }}>
            solver detail
          </Link>
          {" · "}
          <Link to="/solvers" style={{ color: "var(--series-1)" }}>
            all solvers
          </Link>
        </span>
      </div>
      <FlowsSection solver={id} />
    </>
  );
}
