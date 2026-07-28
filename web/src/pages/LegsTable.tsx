import { useQuery } from "@tanstack/react-query";
import { apiGet, rangeParams, type Leg } from "../api";
import { Bps, ClassBadge, DataTable, fmtTime, fmtUsd, SectionCard, shortHash, Spinner, useRange } from "../components";
import { EdgeHistogram } from "../charts";

// Shared "recent legs" section for pair / solver detail pages, with an edge
// distribution histogram computed from the fetched legs.
export default function LegsSection({ filter, title }: { filter: Record<string, string>; title: string }) {
  const { hours, minNotional } = useRange();
  const q = useQuery({
    queryKey: ["legs", hours, minNotional, filter],
    queryFn: () =>
      apiGet<{ rows: Leg[] }>("/api/legs", { ...rangeParams(hours, minNotional), limit: "500", ...filter }),
  });

  const legs = q.data?.rows ?? [];
  const edges = legs.map((l) => l.edge_bps_hl).filter((e): e is number => e != null);

  const amount = (v: string | null) => {
    if (v == null) return "–";
    const n = Number(v);
    return n >= 1000 ? n.toLocaleString(undefined, { maximumFractionDigits: 2 }) : n.toPrecision(6);
  };

  return (
    <>
      {edges.length > 5 && (
        <SectionCard title="Edge distribution (bps vs Hyperliquid, last 500 legs)">
          <div className="px-2 pb-2">
            <EdgeHistogram edges={edges} />
          </div>
        </SectionCard>
      )}
      <SectionCard title={title}>
        {q.data ? (
          <DataTable
            cols={[
              {
                key: "ts",
                label: "time",
                align: "left",
                value: (l: Leg) => <span className="mono">{fmtTime(l.ts)}</span>,
                sortVal: (l: Leg) => l.ts,
              },
              {
                key: "tx",
                label: "tx",
                align: "left",
                value: (l: Leg) => (
                  <a
                    className="mono"
                    style={{ color: "var(--series-1)" }}
                    href={`https://nearblocks.io/txns/${l.tx}`}
                    target="_blank"
                    rel="noreferrer"
                  >
                    {shortHash(l.tx)}
                  </a>
                ),
              },
              { key: "class", label: "class", align: "left", value: (l: Leg) => <ClassBadge cls={l.leg_class} /> },
              {
                key: "recv",
                label: "received",
                value: (l: Leg) => `${amount(l.amount_in)} ${l.from_name}`,
              },
              {
                key: "give",
                label: "gave",
                value: (l: Leg) => `${amount(l.amount_out)} ${l.to_name}`,
              },
              {
                key: "rate",
                label: "rate",
                value: (l: Leg) => (l.native_rate != null ? l.native_rate.toPrecision(6) : "–"),
                sortVal: (l: Leg) => l.native_rate ?? 0,
              },
              {
                key: "edge",
                label: "edge bps (HL)",
                value: (l: Leg) => <Bps v={l.edge_bps_hl} />,
                sortVal: (l: Leg) => l.edge_bps_hl ?? -1e9,
              },
              {
                key: "notional",
                label: "notional",
                value: (l: Leg) => fmtUsd(l.notional_usd),
                sortVal: (l: Leg) => l.notional_usd ?? 0,
              },
              {
                key: "solver",
                label: "solver",
                align: "left",
                value: (l: Leg) => <span className="mono">{shortHash(l.solver, 22)}</span>,
                sortVal: (l: Leg) => l.solver,
              },
            ]}
            rows={legs}
            rowKey={(l) => `${l.tx}:${l.seq}`}
            defaultSort="ts"
          />
        ) : (
          <Spinner />
        )}
      </SectionCard>
    </>
  );
}
