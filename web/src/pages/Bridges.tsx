import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiGet, rangeParams, type SameAsset } from "../api";
import { Bps, DataTable, ErrorBox, fmtNum, fmtUsd, SectionCard, shortHash, Spinner, StatTile, useRange } from "../components";

// Bridges = same-asset legs (e.g. USDT@eth -> USDT@tron) where the solver kept
// an in/out spread. in_out_diff_bps > 0 means the solver received more than it
// gave.
export default function Bridges() {
  const { hours, minNotional } = useRange();
  const [symbols, setSymbols] = useState("USDT,USDC");
  const [hub, setHub] = useState("");
  const [route, setRoute] = useState("");

  const q = useQuery({
    queryKey: ["same-asset", hours, minNotional, symbols, hub, route],
    queryFn: () =>
      apiGet<SameAsset>("/api/routes/same-asset", {
        ...rangeParams(hours, minNotional),
        symbols,
        hub,
        route,
      }),
  });

  if (q.error) return <ErrorBox error={q.error} />;
  const d = q.data;

  const controls = (
    <div className="flex items-center gap-2">
      <label className="text-xs" style={{ color: "var(--text-muted)" }}>
        symbols
      </label>
      <input
        defaultValue={symbols}
        onBlur={(e) => setSymbols(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && setSymbols((e.target as HTMLInputElement).value)}
        className="w-32 rounded border px-1.5 py-0.5 text-xs"
        style={{ borderColor: "var(--border)", background: "var(--surface-1)" }}
      />
    </div>
  );

  return (
    <>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
        <StatTile label="Bridge legs with spread" value={d ? fmtNum(d.n_legs) : "…"} />
        <StatTile label="Bridge volume" value={d ? fmtUsd(d.volume_usd) : "…"} />
        <StatTile
          label="Symbols"
          value={<span className="text-base">{symbols}</span>}
          sub="same-underlying legs only"
        />
      </div>

      <SectionCard title="Chain routes (received chain → given chain)" right={controls}>
        {d ? (
          <DataTable
            cols={[
              {
                key: "route",
                label: "route",
                align: "left",
                value: (r) => (
                  <button
                    onClick={() => setRoute(`${r.recv_chain}:${r.give_chain}`)}
                    style={{ color: "var(--series-1)" }}
                  >
                    {r.recv_chain} → {r.give_chain}
                  </button>
                ),
                sortVal: (r) => `${r.recv_chain}:${r.give_chain}`,
              },
              { key: "n", label: "legs", value: (r) => fmtNum(r.n), sortVal: (r) => r.n },
              { key: "vol", label: "volume", value: (r) => fmtUsd(r.volume_usd), sortVal: (r) => r.volume_usd },
              { key: "pct", label: "% vol", value: (r) => `${r.pct_vol.toFixed(1)}%`, sortVal: (r) => r.pct_vol },
              { key: "vw", label: "vw bps", value: (r) => <Bps v={r.vw_bps} />, sortVal: (r) => r.vw_bps ?? -1e9 },
              {
                key: "mean",
                label: "mean bps",
                value: (r) => <Bps v={r.mean_bps} />,
                sortVal: (r) => r.mean_bps ?? -1e9,
              },
              {
                key: "hub",
                label: "",
                value: (r) => (
                  <span className="space-x-2">
                    <button onClick={() => setHub(r.recv_chain)} style={{ color: "var(--text-muted)" }}>
                      hub:{r.recv_chain}
                    </button>
                  </span>
                ),
              },
            ]}
            rows={d.routes}
            rowKey={(r) => `${r.recv_chain}:${r.give_chain}`}
            defaultSort="vol"
          />
        ) : (
          <Spinner />
        )}
      </SectionCard>

      {d?.hub && (
        <SectionCard
          title={`Hub ${d.hub.chain}: both directions`}
          right={
            <button className="text-xs" style={{ color: "var(--text-muted)" }} onClick={() => setHub("")}>
              close
            </button>
          }
        >
          <DataTable
            cols={[
              { key: "cp", label: "counterparty", align: "left", value: (r) => r.counterparty, sortVal: (r) => r.counterparty },
              { key: "in_n", label: `into ${d.hub!.chain} n`, value: (r) => fmtNum(r.into.n), sortVal: (r) => r.into.n },
              { key: "in_vol", label: "into vol", value: (r) => fmtUsd(r.into.volume_usd), sortVal: (r) => r.into.volume_usd },
              { key: "in_vw", label: "into vw bps", value: (r) => <Bps v={r.into.vw_bps} /> },
              { key: "out_n", label: `out of ${d.hub!.chain} n`, value: (r) => fmtNum(r.out.n), sortVal: (r) => r.out.n },
              { key: "out_vol", label: "out vol", value: (r) => fmtUsd(r.out.volume_usd), sortVal: (r) => r.out.volume_usd },
              { key: "out_vw", label: "out vw bps", value: (r) => <Bps v={r.out.vw_bps} /> },
            ]}
            rows={d.hub.rows}
            rowKey={(r) => r.counterparty}
            defaultSort="in_vol"
          />
        </SectionCard>
      )}

      {d?.buckets && (
        <SectionCard
          title={`Size buckets — ${d.buckets.route.replace(":", " → ")}`}
          right={
            <button className="text-xs" style={{ color: "var(--text-muted)" }} onClick={() => setRoute("")}>
              close
            </button>
          }
        >
          <DataTable
            cols={[
              { key: "bucket", label: "bucket", align: "left", value: (r) => r.bucket },
              { key: "n", label: "legs", value: (r) => fmtNum(r.n), sortVal: (r) => r.n },
              { key: "vol", label: "volume", value: (r) => fmtUsd(r.volume_usd), sortVal: (r) => r.volume_usd },
              { key: "pct", label: "% vol", value: (r) => `${r.pct_vol.toFixed(1)}%` },
              { key: "vw", label: "vw bps", value: (r) => <Bps v={r.vw_bps} /> },
              { key: "avg", label: "avg size", value: (r) => fmtUsd(r.avg_size_usd) },
            ]}
            rows={d.buckets.rows}
            rowKey={(r) => r.bucket}
          />
        </SectionCard>
      )}

      <SectionCard title="By solver">
        {d ? (
          <DataTable
            cols={[
              {
                key: "solver",
                label: "solver",
                align: "left",
                value: (r) => <span className="mono">{shortHash(r.solver, 40)}</span>,
                sortVal: (r) => r.solver,
              },
              { key: "n", label: "legs", value: (r) => fmtNum(r.n), sortVal: (r) => r.n },
              { key: "vol", label: "volume", value: (r) => fmtUsd(r.volume_usd), sortVal: (r) => r.volume_usd },
              { key: "vw", label: "vw bps", value: (r) => <Bps v={r.vw_bps} />, sortVal: (r) => r.vw_bps ?? -1e9 },
              { key: "mean", label: "mean bps", value: (r) => <Bps v={r.mean_bps} /> },
            ]}
            rows={d.by_solver}
            rowKey={(r) => r.solver}
            defaultSort="vol"
          />
        ) : (
          <Spinner />
        )}
      </SectionCard>
    </>
  );
}
