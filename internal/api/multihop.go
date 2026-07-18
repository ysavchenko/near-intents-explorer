package api

import (
	"github.com/shopspring/decimal"
)

// Multi-hop route synthesis (port of reference/multihop_routes.py).
//
// When a solver fills an intent by routing through an intermediate in the SAME
// tx (e.g. ETH -> USDC -> SOL, two legs), the net token movement is a single
// direct swap with the intermediate passing through. Legs are grouped by
// (tx, solver), each asset netted, and clean 1-source/1-sink chains emit the
// synthetic replacement swap. Groups with several sources/sinks (unrelated
// swaps batched, or fan-out routes) are `complex` and not synthesized.

type mhLeg struct {
	FromAsset string
	ToAsset   string
	AmountIn  decimal.Decimal
	AmountOut decimal.Decimal
}

type mhNet struct {
	Sources       []string
	Sinks         []string
	Intermediates []string
	Recv          map[string]decimal.Decimal
	Give          map[string]decimal.Decimal
}

// netGroup nets one (tx, solver) group per asset. An asset is a pass-through
// intermediate when it is both received and given and its |net|/throughput is
// under tol; the leftover net-in asset is the source, net-out the sink.
func netGroup(legs []mhLeg, tol decimal.Decimal) mhNet {
	recv := map[string]decimal.Decimal{}
	give := map[string]decimal.Decimal{}
	var order []string // deterministic iteration: first-seen order
	seen := map[string]bool{}
	note := func(a string) {
		if !seen[a] {
			seen[a] = true
			order = append(order, a)
		}
	}
	for _, l := range legs {
		recv[l.FromAsset] = recv[l.FromAsset].Add(l.AmountIn)
		give[l.ToAsset] = give[l.ToAsset].Add(l.AmountOut)
		note(l.FromAsset)
		note(l.ToAsset)
	}
	n := mhNet{Recv: recv, Give: give}
	for _, a := range order {
		rv, gv := recv[a], give[a]
		net := rv.Sub(gv)
		thru := decimal.Max(rv, gv)
		switch {
		case rv.IsPositive() && gv.IsPositive() && thru.IsPositive() &&
			net.Abs().Div(thru).LessThan(tol):
			n.Intermediates = append(n.Intermediates, a)
		case net.IsPositive():
			n.Sources = append(n.Sources, a)
		case net.IsNegative():
			n.Sinks = append(n.Sinks, a)
		}
	}
	return n
}

// reconstructPath follows from_asset -> to_asset edges from the source.
// Returns the asset path and whether it is a clean linear chain using every
// leg and ending at the sink.
func reconstructPath(legs []mhLeg, sourceID, sinkID string) ([]string, bool) {
	out := map[string][][2]any{} // from -> [(to, legIdx)]
	for i, l := range legs {
		out[l.FromAsset] = append(out[l.FromAsset], [2]any{l.ToAsset, i})
	}
	path := []string{sourceID}
	used := map[int]bool{}
	cur := sourceID
	for range len(legs) + 1 {
		var next string
		nextIdx := -1
		for _, edge := range out[cur] {
			if !used[edge[1].(int)] {
				next, nextIdx = edge[0].(string), edge[1].(int)
				break
			}
		}
		if nextIdx == -1 {
			break
		}
		used[nextIdx] = true
		path = append(path, next)
		cur = next
		if cur == sinkID {
			break
		}
	}
	return path, path[len(path)-1] == sinkID && len(used) == len(legs)
}
