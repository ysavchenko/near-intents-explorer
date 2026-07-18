package intents

import "sync"

// SolverSet identifies solver accounts: the curated seed set plus a frequency
// tally that promotes accounts appearing as token_diff signers in at least
// MinSettlements distinct settlements (port of reference/lib/solvers.py).
//
// The live indexer persists the tally in the `solvers` table so it survives
// restarts and keeps learning; this in-memory view is loaded at startup and
// kept in sync by the follower.
type SolverSet struct {
	mu             sync.RWMutex
	MinSettlements int64
	seeds          map[string]bool
	counts         map[string]int64
}

// SeedSolvers is the curated set of known professional makers (mined from a
// prior backfill; current as of 2026-07). Mirrors solvers.py::SEED_SOLVERS and
// the rows the initial migration inserts.
var SeedSolvers = []string{
	"crux-solver.near",
	"solver-priv-liq.near",
	"solver-priv-liq-2.near",
	"solver-multichain-asset.near",
	"solver-multichain-asset-escrow.near",
	"solver-multichain-asset-3.near",
	"solver-ref.near",
	"l2solver.near",
	"nocturnallabs.near",
	"kipseli.near",
	"sodax-solver.near",
	"defuse-relay.near",
	"pablow-stables.near",
	// EVM / implicit accounts that recur as solvers:
	"0x2e30c32738a28833e3d85189c04895c25ee54cb5",
	"6247d9c671ca6a80cf2c75e26edb8cd75ebf00fcd3c56c39107d6ca20c9cfb29",
	"433d1f6e5452c9f6087af460d1c6b799587132caabeb2477a0ad37e3e0b88d0c",
	"4fdbc608fd712338c7680c224f249360c855987cc25ec4501a9a47ef4c789264",
}

func NewSolverSet() *SolverSet {
	s := &SolverSet{
		MinSettlements: 5,
		seeds:          make(map[string]bool, len(SeedSolvers)),
		counts:         map[string]int64{},
	}
	for _, a := range SeedSolvers {
		s.seeds[a] = true
	}
	return s
}

// Load replaces the frequency tally (from the solvers table at startup).
// Accounts flagged is_seed in the DB are honored as seeds too, so future seed
// additions need only a DB row, not a redeploy.
func (s *SolverSet) Load(counts map[string]int64, extraSeeds []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = make(map[string]int64, len(counts))
	for a, c := range counts {
		s.counts[a] = c
	}
	for _, a := range extraSeeds {
		s.seeds[a] = true
	}
}

// Observe records the distinct token_diff signers seen in one settlement.
// Mirrors Python's call order: observe runs BEFORE classify, so a settlement
// counts toward its own signers' promotion.
func (s *SolverSet) Observe(signers map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for a := range signers {
		s.counts[a]++
	}
}

func (s *SolverSet) IsSeed(account string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seeds[account]
}

func (s *SolverSet) IsSolver(account string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seeds[account] || s.counts[account] >= s.MinSettlements
}

func (s *SolverSet) Count(account string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.counts[account]
}

// Learned returns accounts promoted purely by frequency (excludes seeds).
func (s *SolverSet) Learned() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int64{}
	for a, c := range s.counts {
		if !s.seeds[a] && c >= s.MinSettlements {
			out[a] = c
		}
	}
	return out
}
