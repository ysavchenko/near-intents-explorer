// Chart building blocks per the dataviz method: thin marks, rounded data-ends,
// recessive hairline grid, hover tooltips, one axis per chart (never dual).
import {
  Bar,
  BarChart,
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { fmtUsd, fmtBpsValue } from "./components";

const axisTick = { fill: "var(--text-muted)", fontSize: 11 } as const;

function chartTooltipStyle() {
  return {
    contentStyle: {
      background: "var(--surface-1)",
      border: "1px solid var(--border)",
      borderRadius: 6,
      fontSize: 12,
      color: "var(--text-primary)",
    },
    labelStyle: { color: "var(--text-secondary)" },
    cursor: { fill: "color-mix(in oklab, var(--text-primary) 6%, transparent)" },
  } as const;
}

export type TimePoint = { ts: string; [k: string]: number | string | null };

// Fill missing buckets with zero-volume points so quiet periods render as
// zero bars instead of being skipped (skipping compresses the time axis and
// exaggerates activity). Bucket phase is inferred from the data, so day
// buckets stay aligned with the server's date_trunc timezone.
export function fillBuckets(rows: TimePoint[], from: string, to: string, bucket: "hour" | "day"): TimePoint[] {
  const step = bucket === "hour" ? 3_600_000 : 86_400_000;
  const fromMs = new Date(from).getTime();
  const toMs = new Date(to).getTime();
  if (!Number.isFinite(fromMs) || !Number.isFinite(toMs)) return rows;
  const phase = rows.length ? new Date(rows[0].ts).getTime() % step : 0;
  const align = (t: number) => t - ((((t - phase) % step) + step) % step);
  const have = new Map(rows.map((r) => [new Date(r.ts).getTime(), r]));
  const out: TimePoint[] = [];
  for (let t = align(fromMs); t <= toMs && out.length < 5000; t += step) {
    out.push(have.get(t) ?? { ts: new Date(t).toISOString(), volume_usd: 0, n_legs: 0, hl_vw_edge_bps: null });
  }
  return out;
}

function fmtTick(iso: string, bucket: "hour" | "day"): string {
  const d = new Date(iso);
  if (bucket === "hour") return d.toISOString().slice(11, 16) + "Z";
  return d.toISOString().slice(5, 10);
}

// Single-series volume bars over time. One series → no legend; the title
// names it (dataviz rule).
export function VolumeBars({
  data,
  bucket,
  height = 220,
}: {
  data: TimePoint[];
  bucket: "hour" | "day";
  height?: number;
}) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={data} margin={{ top: 8, right: 12, left: 4, bottom: 0 }}>
        <CartesianGrid stroke="var(--grid)" strokeWidth={1} vertical={false} />
        <XAxis
          dataKey="ts"
          tickFormatter={(v) => fmtTick(v, bucket)}
          tick={axisTick}
          axisLine={{ stroke: "var(--baseline)" }}
          tickLine={false}
          minTickGap={40}
        />
        <YAxis
          tickFormatter={(v) => fmtUsd(v)}
          tick={axisTick}
          axisLine={false}
          tickLine={false}
          width={58}
        />
        <Tooltip
          {...chartTooltipStyle()}
          formatter={(v) => [fmtUsd(Number(v)), "Volume"]}
          labelFormatter={(v) => new Date(String(v)).toISOString().replace("T", " ").slice(0, 16) + "Z"}
        />
        <Bar dataKey="volume_usd" fill="var(--series-1)" radius={[4, 4, 0, 0]} maxBarSize={28} isAnimationActive={false} />
      </BarChart>
    </ResponsiveContainer>
  );
}

// Volume-weighted edge line over time (its own panel — never a second axis on
// the volume chart).
export function EdgeLine({
  data,
  bucket,
  height = 180,
}: {
  data: TimePoint[];
  bucket: "hour" | "day";
  height?: number;
}) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <LineChart data={data} margin={{ top: 8, right: 12, left: 4, bottom: 0 }}>
        <CartesianGrid stroke="var(--grid)" strokeWidth={1} vertical={false} />
        <XAxis
          dataKey="ts"
          tickFormatter={(v) => fmtTick(v, bucket)}
          tick={axisTick}
          axisLine={{ stroke: "var(--baseline)" }}
          tickLine={false}
          minTickGap={40}
        />
        <YAxis
          tickFormatter={(v) => fmtBpsValue(Number(v))}
          tick={axisTick}
          axisLine={false}
          tickLine={false}
          width={58}
        />
        <Tooltip
          {...chartTooltipStyle()}
          formatter={(v) => [`${fmtBpsValue(Number(v))} bps`, "VW edge"]}
          labelFormatter={(v) => new Date(String(v)).toISOString().replace("T", " ").slice(0, 16) + "Z"}
        />
        <Line
          dataKey="hl_vw_edge_bps"
          stroke="var(--series-5)"
          strokeWidth={2}
          dot={false}
          activeDot={{ r: 4 }}
          connectNulls
          isAnimationActive={false}
        />
      </LineChart>
    </ResponsiveContainer>
  );
}

// Edge distribution histogram, bucketed client-side from leg edges. Edges are
// rounded to the nearest whole bps first: sub-bps bin widths would render
// several bars that all carry the same label.
export function EdgeHistogram({ edges, height = 180 }: { edges: number[]; height?: number }) {
  if (edges.length === 0) return null;
  const sorted = edges.map((e) => Math.round(e)).sort((a, b) => a - b);
  const q = (p: number) => sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * p))];
  // Drop only true outliers (0.5% per tail) — a skewed-but-real tail (e.g.
  // mostly +1 bps with a cluster at +10) must stay visible. Tighten further
  // only when the remaining span would flatten every real bar into one bin.
  let lo = q(0.005);
  let hi = q(0.995);
  if (hi - lo > 200) {
    lo = q(0.02);
    hi = q(0.98);
  }
  const maxBins = Math.min(40, Math.max(10, Math.floor(Math.sqrt(sorted.length))));
  const binW = Math.max(1, Math.ceil((hi - lo + 1) / maxBins)); // whole bps per bin
  const nBins = Math.max(1, Math.ceil((hi - lo + 1) / binW));
  type Bin = { x: number | string; label: string; n: number };
  const bins: Bin[] = Array.from({ length: nBins }, (_, i) => {
    const start = lo + i * binW;
    return {
      x: start + (binW - 1) / 2,
      label: binW === 1 ? `${start}` : `${start} … ${start + binW - 1}`,
      n: 0,
    };
  });
  // Clipped mass gets explicit overflow bars instead of silently inflating
  // the edge bins under a wrong label.
  let below = 0;
  let above = 0;
  for (const e of sorted) {
    if (e < lo) below++;
    else if (e > hi) above++;
    else bins[Math.min(nBins - 1, Math.floor((e - lo) / binW))].n++;
  }
  if (below > 0) bins.unshift({ x: `≤${lo - 1}`, label: `≤ ${lo - 1}`, n: below });
  if (above > 0) bins.push({ x: `≥${hi + 1}`, label: `≥ ${hi + 1}`, n: above });
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={bins} margin={{ top: 8, right: 12, left: 4, bottom: 0 }}>
        <CartesianGrid stroke="var(--grid)" strokeWidth={1} vertical={false} />
        <XAxis
          dataKey="x"
          tickFormatter={(v) => (Number.isFinite(Number(v)) ? Number(v).toFixed(0) : String(v))}
          tick={axisTick}
          axisLine={{ stroke: "var(--baseline)" }}
          tickLine={false}
          minTickGap={30}
        />
        <YAxis tick={axisTick} axisLine={false} tickLine={false} width={40} />
        <Tooltip
          {...chartTooltipStyle()}
          formatter={(v) => [String(v), "legs"]}
          labelFormatter={(_, payload) => `${payload?.[0]?.payload?.label ?? ""} bps`}
        />
        <Bar dataKey="n" fill="var(--series-1)" radius={[4, 4, 0, 0]} maxBarSize={24} isAnimationActive={false} />
      </BarChart>
    </ResponsiveContainer>
  );
}
