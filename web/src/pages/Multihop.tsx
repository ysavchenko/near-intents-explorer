import { useQuery } from "@tanstack/react-query";
import { apiGet, rangeParams, type Multihop } from "../api";
import { ClassBadge, DataTable, ErrorBox, fmtNum, fmtTime, fmtUsd, SectionCard, shortHash, Spinner, StatTile, useRange } from "../components";

// Multi-hop routes: a solver's legs within one settlement netted per asset;
// clean 1-source/1-sink chains synthesize the direct replacement swap.
export default function MultihopPage() {
  const { hours, minNotional } = useRange();
  const q = useQuery({
    queryKey: ["multihop", hours, minNotional],
    queryFn: () => apiGet<Multihop>("/api/routes/multihop", rangeParams(hours, minNotional)),
  });
  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data;

  return (
    <>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatTile label="Multi-leg groups" value={d ? fmtNum(d.multi_leg_groups) : "…"} />
        <StatTile label="Clean routes" value={d ? fmtNum(d.clean) : "…"} sub="1 source → 1 sink" />
        <StatTile label="Complex / batched" value={d ? fmtNum(d.complex) : "…"} sub="not synthesized" />
        <StatTile
          label="Top intermediate"
          value={
            d?.top_intermediates[0] ? (
              <span className="text-base">{d.top_intermediates[0].label}</span>
            ) : (
              "–"
            )
          }
          sub={d?.top_intermediates[0] ? `${d.top_intermediates[0].n} routes` : undefined}
        />
      </div>

      <SectionCard title="Synthesized direct swaps (net of intermediates)">
        {d ? (
          <DataTable
            cols={[
              {
                key: "ts",
                label: "time",
                align: "left",
                value: (r) => <span className="mono">{fmtTime(r.ts)}</span>,
                sortVal: (r) => r.ts,
              },
              {
                key: "tx",
                label: "tx",
                align: "left",
                value: (r) => (
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
              {
                key: "path",
                label: "path",
                align: "left",
                value: (r) => <span title={r.path}>{r.path.length > 60 ? r.path.slice(0, 60) + "…" : r.path}</span>,
              },
              { key: "pair", label: "synth pair", align: "left", value: (r) => r.synth_pair, sortVal: (r) => r.synth_pair },
              { key: "class", label: "class", align: "left", value: (r) => <ClassBadge cls={r.synth_class} /> },
              { key: "hops", label: "hops", value: (r) => r.hops, sortVal: (r) => r.hops },
              {
                key: "rate",
                label: "rate",
                value: (r) => (r.native_rate != null ? r.native_rate.toPrecision(6) : "–"),
              },
              {
                key: "notional",
                label: "notional",
                value: (r) => fmtUsd(r.notional_usd),
                sortVal: (r) => r.notional_usd ?? 0,
              },
              {
                key: "solver",
                label: "solver",
                align: "left",
                value: (r) => <span className="mono">{shortHash(r.solver, 22)}</span>,
                sortVal: (r) => r.solver,
              },
            ]}
            rows={d.routes}
            rowKey={(r) => `${r.tx}:${r.solver}`}
            defaultSort="notional"
          />
        ) : (
          <Spinner />
        )}
      </SectionCard>

      {d && d.top_intermediates.length > 0 && (
        <SectionCard title="Top pass-through intermediates">
          <DataTable
            cols={[
              { key: "label", label: "asset", align: "left", value: (r) => r.label },
              { key: "n", label: "routes", value: (r) => fmtNum(r.n), sortVal: (r) => r.n },
            ]}
            rows={d.top_intermediates}
            rowKey={(r) => r.label}
            defaultSort="n"
          />
        </SectionCard>
      )}
    </>
  );
}
