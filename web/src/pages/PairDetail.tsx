import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { apiGet, rangeParams, type DailyPoint } from "../api";
import { ErrorBox, SectionCard, Spinner, useRange } from "../components";
import { EdgeLine, VolumeBars } from "../charts";
import LegsSection from "./LegsTable";

export default function PairDetail() {
  const [params] = useSearchParams();
  const pair = params.get("p") ?? "";
  const { hours } = useRange();
  const bucket = hours <= 72 ? "hour" : "day";

  const daily = useQuery({
    queryKey: ["daily-pair", pair, hours],
    queryFn: () =>
      apiGet<{ rows: DailyPoint[] }>("/api/daily", {
        ...rangeParams(hours),
        group: "pair",
        bucket,
        par: "all",
      }),
    enabled: pair !== "",
  });

  if (!pair) return <ErrorBox error="no pair given" />;
  if (daily.error) return <ErrorBox error={daily.error} />;

  const series = (daily.data?.rows ?? []).filter((r) => r.key === pair);

  return (
    <>
      <div className="flex items-baseline gap-3">
        <h1 className="text-xl font-bold">{pair}</h1>
        <span className="text-xs" style={{ color: "var(--text-muted)" }}>
          received/given by the solver ·{" "}
          <Link to="/pairs" style={{ color: "var(--series-1)" }}>
            all pairs
          </Link>
        </span>
      </div>
      {daily.data ? (
        <div className="grid gap-4 lg:grid-cols-2">
          <SectionCard title={`Volume by ${bucket} (USD)`}>
            <div className="px-2 pb-2">
              <VolumeBars data={series} bucket={bucket} height={200} />
            </div>
          </SectionCard>
          <SectionCard title={`VW edge by ${bucket} (bps vs Hyperliquid)`}>
            <div className="px-2 pb-2">
              <EdgeLine data={series} bucket={bucket} height={200} />
            </div>
          </SectionCard>
        </div>
      ) : (
        <Spinner />
      )}
      <LegsSection filter={{ pair }} title={`Legs — ${pair}`} />
    </>
  );
}
