import { createContext, useContext, useMemo, useState } from "react";
import type { ReactNode } from "react";

// ---------------------------------------------------------------------------
// Global filter context (shared time-range + min-notional controls in the header)
// ---------------------------------------------------------------------------

export const RangeContext = createContext<{
  hours: number;
  setHours: (h: number) => void;
  minNotional: number;
  setMinNotional: (v: number) => void;
}>({
  hours: 24,
  setHours: () => {},
  minNotional: 0,
  setMinNotional: () => {},
});

export function useRange() {
  return useContext(RangeContext);
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

export function fmtUsd(v: number | null | undefined): string {
  if (v == null) return "–";
  if (Math.abs(v) >= 1e9) return `$${(v / 1e9).toFixed(2)}B`;
  if (Math.abs(v) >= 1e6) return `$${(v / 1e6).toFixed(2)}M`;
  if (Math.abs(v) >= 1e4) return `$${(v / 1e3).toFixed(1)}K`;
  return `$${v.toLocaleString(undefined, { maximumFractionDigits: 0 })}`;
}

export function fmtNum(v: number | null | undefined): string {
  if (v == null) return "–";
  return v.toLocaleString();
}

export function fmtBpsValue(v: number | null | undefined): string {
  if (v == null) return "–";
  return `${v >= 0 ? "+" : ""}${v.toFixed(2)}`;
}

export function Bps({ v }: { v: number | null | undefined }) {
  if (v == null) return <span style={{ color: "var(--text-muted)" }}>–</span>;
  const color = v >= 0 ? "var(--delta-good)" : "var(--delta-bad)";
  return <span style={{ color }}>{fmtBpsValue(v)}</span>;
}

// Signed USD amount (fees kept vs the venue mid), colored like Bps. Small
// amounts keep cents — a table full of $0 would hide most fee rows.
export function UsdDelta({ v }: { v: number | null | undefined }) {
  if (v == null) return <span style={{ color: "var(--text-muted)" }}>–</span>;
  const a = Math.abs(v);
  const mag = a < 100 ? `$${a.toFixed(2)}` : fmtUsd(a);
  const color = v >= 0 ? "var(--delta-good)" : "var(--delta-bad)";
  return <span style={{ color }}>{(v >= 0 ? "+" : "−") + mag}</span>;
}

export function fmtTime(iso: string): string {
  const d = new Date(iso);
  return d.toISOString().replace("T", " ").slice(0, 19) + "Z";
}

export function shortHash(h: string, n = 8): string {
  return h.length > n ? h.slice(0, n) + "…" : h;
}

// ---------------------------------------------------------------------------
// Stat tile (hero numbers) — a number with a label, not a chart.
// ---------------------------------------------------------------------------

export function StatTile({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="card px-4 py-3">
      <div className="text-xs" style={{ color: "var(--text-muted)" }}>
        {label}
      </div>
      <div className="text-2xl font-semibold mt-0.5">{value}</div>
      {sub != null && (
        <div className="text-xs mt-0.5" style={{ color: "var(--text-secondary)" }}>
          {sub}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Sortable table
// ---------------------------------------------------------------------------

export type Col<T> = {
  key: string;
  label: string;
  value: (row: T) => ReactNode;
  sortVal?: (row: T) => number | string;
  align?: "left" | "right";
};

export function DataTable<T>({
  cols,
  rows,
  defaultSort,
  rowKey,
  emptyText = "No data in this window",
}: {
  cols: Col<T>[];
  rows: T[];
  defaultSort?: string;
  rowKey: (row: T, i: number) => string;
  emptyText?: string;
}) {
  const [sortKey, setSortKey] = useState(defaultSort ?? "");
  const [desc, setDesc] = useState(true);

  const sorted = useMemo(() => {
    const col = cols.find((c) => c.key === sortKey);
    if (!col?.sortVal) return rows;
    const sv = col.sortVal;
    return [...rows].sort((a, b) => {
      const va = sv(a);
      const vb = sv(b);
      const cmp = typeof va === "number" && typeof vb === "number" ? va - vb : String(va).localeCompare(String(vb));
      return desc ? -cmp : cmp;
    });
  }, [rows, sortKey, desc, cols]);

  if (rows.length === 0) {
    return (
      <div className="px-4 py-8 text-center text-sm" style={{ color: "var(--text-muted)" }}>
        {emptyText}
      </div>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="data-table">
        <thead>
          <tr>
            {cols.map((c) => (
              <th
                key={c.key}
                style={c.align === "left" ? { textAlign: "left" } : undefined}
                onClick={() => {
                  if (!c.sortVal) return;
                  if (sortKey === c.key) setDesc(!desc);
                  else {
                    setSortKey(c.key);
                    setDesc(true);
                  }
                }}
              >
                {c.label}
                {sortKey === c.key ? (desc ? " ↓" : " ↑") : ""}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {sorted.map((r, i) => (
            <tr key={rowKey(r, i)}>
              {cols.map((c) => (
                <td key={c.key} style={c.align === "left" ? { textAlign: "left" } : undefined}>
                  {c.value(r)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function SectionCard({ title, children, right }: { title: string; children: ReactNode; right?: ReactNode }) {
  return (
    <div className="card">
      <div className="flex items-center justify-between px-4 pt-3 pb-1">
        <h2 className="text-sm font-semibold">{title}</h2>
        {right}
      </div>
      {children}
    </div>
  );
}

export function Spinner() {
  return (
    <div className="px-4 py-10 text-center text-sm" style={{ color: "var(--text-muted)" }}>
      Loading…
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  return (
    <div className="card px-4 py-3 text-sm" style={{ color: "var(--status-critical)" }}>
      {String(error)}
    </div>
  );
}

export function ClassBadge({ cls }: { cls: string }) {
  const label: Record<string, string> = {
    interesting: "swap",
    stable_stable: "stable↔stable",
    same_asset: "bridge",
    unknown: "unknown",
  };
  return (
    <span
      className="inline-block rounded px-1.5 py-0.5 text-[11px]"
      style={{ background: "color-mix(in oklab, var(--text-primary) 7%, transparent)", color: "var(--text-secondary)" }}
    >
      {label[cls] ?? cls}
    </span>
  );
}
