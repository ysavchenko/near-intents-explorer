import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { apiGet, rangeParams, type AggRow } from "../api";
import { DataTable, ErrorBox, fmtNum, fmtUsd, SectionCard, Spinner, useRange } from "../components";
import { edgeCols, ParFilter } from "./Pairs";

export default function Solvers() {
  const { hours, minNotional } = useRange();
  const [par, setPar] = useState("diff");
  const q = useQuery({
    queryKey: ["solvers", hours, minNotional, par],
    queryFn: () => apiGet<{ rows: AggRow[] }>("/api/solvers", { ...rangeParams(hours, minNotional), par }),
  });
  if (q.error) return <ErrorBox error={q.error} />;
  return (
    <SectionCard title="Solvers" right={<ParFilter par={par} setPar={setPar} />}>
      {q.data ? (
        <DataTable
          cols={[
            {
              key: "solver",
              label: "solver",
              align: "left",
              value: (r: AggRow) => (
                <Link
                  to={`/solvers/${encodeURIComponent(r.solver!)}`}
                  className="mono"
                  style={{ color: "var(--series-1)" }}
                >
                  {r.solver!.length > 44 ? r.solver!.slice(0, 44) + "…" : r.solver}
                </Link>
              ),
              sortVal: (r: AggRow) => r.solver!,
            },
            { key: "n", label: "legs", value: (r) => fmtNum(r.n_legs), sortVal: (r) => r.n_legs },
            { key: "vol", label: "volume", value: (r) => fmtUsd(r.volume_usd), sortVal: (r) => r.volume_usd },
            ...edgeCols(),
          ]}
          rows={q.data.rows}
          rowKey={(r) => r.solver!}
          defaultSort="vol"
        />
      ) : (
        <Spinner />
      )}
    </SectionCard>
  );
}
