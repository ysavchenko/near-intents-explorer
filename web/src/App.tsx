import { useState } from "react";
import { NavLink, Route, Routes } from "react-router-dom";
import { RANGES } from "./api";
import { RangeContext } from "./components";
import Overview from "./pages/Overview";
import Pairs from "./pages/Pairs";
import PairDetail from "./pages/PairDetail";
import Solvers from "./pages/Solvers";
import SolverDetail from "./pages/SolverDetail";
import Bridges from "./pages/Bridges";
import Multihop from "./pages/Multihop";
import StatusPage from "./pages/Status";

const NAV = [
  { to: "/", label: "Overview" },
  { to: "/pairs", label: "Pairs" },
  { to: "/solvers", label: "Solvers" },
  { to: "/bridges", label: "Bridges" },
  { to: "/multihop", label: "Multi-hop" },
  { to: "/status", label: "Status" },
];

export default function App() {
  const [hours, setHours] = useState(24);
  return (
    <RangeContext.Provider value={{ hours, setHours }}>
      <div className="min-h-screen">
        <header
          className="sticky top-0 z-10 border-b"
          style={{ background: "var(--surface-1)", borderColor: "var(--border)" }}
        >
          <div className="mx-auto flex max-w-6xl items-center gap-1 px-4 py-2">
            <span className="mr-3 text-sm font-bold whitespace-nowrap">NEAR Intents Explorer</span>
            <nav className="flex flex-1 items-center gap-1 overflow-x-auto">
              {NAV.map((n) => (
                <NavLink
                  key={n.to}
                  to={n.to}
                  end={n.to === "/"}
                  className="rounded px-2.5 py-1 text-sm whitespace-nowrap"
                  style={({ isActive }) => ({
                    background: isActive ? "color-mix(in oklab, var(--series-1) 14%, transparent)" : undefined,
                    color: isActive ? "var(--text-primary)" : "var(--text-secondary)",
                    fontWeight: isActive ? 600 : 400,
                  })}
                >
                  {n.label}
                </NavLink>
              ))}
            </nav>
            <div className="flex items-center gap-0.5">
              {RANGES.map((r) => (
                <button
                  key={r.hours}
                  onClick={() => setHours(r.hours)}
                  className="rounded px-2 py-1 text-xs"
                  style={{
                    background:
                      hours === r.hours ? "color-mix(in oklab, var(--series-1) 14%, transparent)" : undefined,
                    color: hours === r.hours ? "var(--text-primary)" : "var(--text-muted)",
                    fontWeight: hours === r.hours ? 600 : 400,
                  }}
                >
                  {r.label}
                </button>
              ))}
            </div>
          </div>
        </header>
        <main className="mx-auto max-w-6xl space-y-4 px-4 py-4">
          <Routes>
            <Route path="/" element={<Overview />} />
            <Route path="/pairs" element={<Pairs />} />
            <Route path="/pair" element={<PairDetail />} />
            <Route path="/solvers" element={<Solvers />} />
            <Route path="/solvers/:id" element={<SolverDetail />} />
            <Route path="/bridges" element={<Bridges />} />
            <Route path="/multihop" element={<Multihop />} />
            <Route path="/status" element={<StatusPage />} />
          </Routes>
        </main>
      </div>
    </RangeContext.Provider>
  );
}
