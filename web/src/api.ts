// Thin typed client for the Go API. Basic auth is handled by the browser.

export type AggRow = {
  pair?: string;
  solver?: string;
  from_asset?: string;
  to_asset?: string;
  from_label?: string;
  to_label?: string;
  n_legs: number;
  volume_usd: number;
  binance_n: number;
  binance_vw_edge_bps: number | null;
  binance_mean_edge_bps: number | null;
  hl_n: number;
  hl_vw_edge_bps: number | null;
  hl_mean_edge_bps: number | null;
};

export type Leg = {
  tx: string;
  seq: number;
  ts: string;
  block: number;
  solver: string;
  leg_class: string;
  multi_asset: boolean;
  from_asset: string;
  from_name: string;
  to_asset: string;
  to_name: string;
  amount_in: string | null;
  amount_out: string | null;
  pair: string | null;
  side: string | null;
  native_rate: number | null;
  hl_rate: number | null;
  binance_rate: number | null;
  edge_bps_hl: number | null;
  edge_bps_binance: number | null;
  notional_usd: number | null;
  price_status: string;
};

export type Summary = {
  from: string;
  to: string;
  volume_usd: number;
  legs: number;
  pending_legs: number;
  settlements: number;
  settlements_failed: number;
  active_solvers: number;
  volume_by_class: Record<string, number>;
  legs_by_class: Record<string, number>;
  hourly: { ts: string; volume_usd: number; n_legs: number }[];
};

export type Status = {
  chain_head: number;
  chain_head_ts: string | null;
  indexed_head: number;
  indexed_head_ts: string | null;
  lag_blocks: number;
  pricing_backlog: number;
  started_at: string;
  uptime_s: number;
  assets: number;
  venues: string[];
  solvers_learned: number;
  counters: Record<string, number>;
};

export type VwStats = {
  n: number;
  volume_usd: number;
  vw_bps: number | null;
  mean_bps: number | null;
};

export type SameAsset = {
  n_legs: number;
  volume_usd: number;
  routes: ({ recv_chain: string; give_chain: string; pct_vol: number } & VwStats)[];
  by_solver: ({ solver: string } & VwStats)[];
  hub?: {
    chain: string;
    rows: { counterparty: string; into: VwStats; out: VwStats }[];
  };
  buckets?: {
    route: string;
    rows: ({ bucket: string; pct_vol: number; avg_size_usd: number } & VwStats)[];
  };
};

export type MultihopRoute = {
  tx: string;
  solver: string;
  ts: string;
  hops: number;
  path: string;
  linear: boolean;
  synth_pair: string;
  synth_class: string;
  stable_intermediate: boolean;
  source: string;
  source_chain: string;
  sink: string;
  sink_chain: string;
  intermediates: string[];
  amount_in: string;
  amount_out: string;
  native_rate: number | null;
  notional_usd: number | null;
};

export type Multihop = {
  multi_leg_groups: number;
  clean: number;
  complex: number;
  kept: number;
  top_intermediates: { label: string; n: number }[];
  routes: MultihopRoute[];
};

export type DailyPoint = {
  ts: string;
  key: string;
  n_legs: number;
  volume_usd: number;
  hl_vw_edge_bps: number | null;
};

export async function apiGet<T>(path: string, params?: Record<string, string>): Promise<T> {
  const url = new URL(path, window.location.origin);
  for (const [k, v] of Object.entries(params ?? {})) {
    if (v !== "") url.searchParams.set(k, v);
  }
  const resp = await fetch(url);
  if (!resp.ok) {
    throw new Error(`${path}: HTTP ${resp.status} ${await resp.text()}`);
  }
  return resp.json();
}

// Shared time-range presets. `hours = 0` means "since launch" (server clamps).
export const RANGES = [
  { label: "6h", hours: 6 },
  { label: "24h", hours: 24 },
  { label: "3d", hours: 72 },
  { label: "7d", hours: 168 },
  { label: "30d", hours: 720 },
] as const;

// Global minimum-notional presets (USD). 0 = everything.
export const AMOUNTS = [
  { label: "All", min: 0 },
  { label: "$100+", min: 100 },
  { label: "$1K+", min: 1000 },
  { label: "$10K+", min: 10000 },
] as const;

export function rangeParams(hours: number, minNotional = 0): Record<string, string> {
  const to = Math.ceil(Date.now() / 1000);
  const p: Record<string, string> = { from: String(to - hours * 3600), to: String(to) };
  if (minNotional > 0) p.min_notional = String(minNotional);
  return p;
}
