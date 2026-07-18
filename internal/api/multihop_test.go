package api

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestNetGroupCleanTwoHop(t *testing.T) {
	// Solver received ETH, gave USDC; then received USDC, gave SOL. The USDC
	// nets to ~0 -> pass-through intermediate, ETH source, SOL sink.
	legs := []mhLeg{
		{FromAsset: "eth", ToAsset: "usdc", AmountIn: d("1.0"), AmountOut: d("3000")},
		{FromAsset: "usdc", ToAsset: "sol", AmountIn: d("2999"), AmountOut: d("20")},
	}
	n := netGroup(legs, d("0.10"))
	if len(n.Sources) != 1 || n.Sources[0] != "eth" {
		t.Fatalf("sources: %v", n.Sources)
	}
	if len(n.Sinks) != 1 || n.Sinks[0] != "sol" {
		t.Fatalf("sinks: %v", n.Sinks)
	}
	if len(n.Intermediates) != 1 || n.Intermediates[0] != "usdc" {
		t.Fatalf("intermediates: %v", n.Intermediates)
	}
	path, linear := reconstructPath(legs, "eth", "sol")
	if !linear || len(path) != 3 || path[0] != "eth" || path[1] != "usdc" || path[2] != "sol" {
		t.Fatalf("path: %v linear=%v", path, linear)
	}
}

func TestNetGroupIntermediateTolerance(t *testing.T) {
	// USDC leaks 20% of throughput -> above tol 0.10, NOT an intermediate:
	// it becomes a second source, so the group is complex.
	legs := []mhLeg{
		{FromAsset: "eth", ToAsset: "usdc", AmountIn: d("1.0"), AmountOut: d("3000")},
		{FromAsset: "usdc", ToAsset: "sol", AmountIn: d("2400"), AmountOut: d("20")},
	}
	n := netGroup(legs, d("0.10"))
	if len(n.Intermediates) != 0 {
		t.Fatalf("20%% leak must not be an intermediate: %v", n.Intermediates)
	}
	if len(n.Sinks) != 2 {
		t.Fatalf("expected complex (2 sinks: usdc leak + sol): %+v", n)
	}
}

func TestNetGroupBatchedUnrelatedSwaps(t *testing.T) {
	// Two unrelated swaps in one settlement: 2 sources + 2 sinks, no
	// intermediates -> complex, not synthesized.
	legs := []mhLeg{
		{FromAsset: "eth", ToAsset: "usdc", AmountIn: d("1"), AmountOut: d("3000")},
		{FromAsset: "btc", ToAsset: "usdt", AmountIn: d("1"), AmountOut: d("60000")},
	}
	n := netGroup(legs, d("0.10"))
	if len(n.Sources) != 2 || len(n.Sinks) != 2 || len(n.Intermediates) != 0 {
		t.Fatalf("batched swaps must be complex: %+v", n)
	}
}

func TestReconstructPathNonLinear(t *testing.T) {
	// Fan-out: eth -> usdc twice from the same source; path can't use every
	// leg in one chain -> not linear.
	legs := []mhLeg{
		{FromAsset: "eth", ToAsset: "usdc", AmountIn: d("1"), AmountOut: d("1500")},
		{FromAsset: "eth", ToAsset: "usdc", AmountIn: d("1"), AmountOut: d("1500")},
		{FromAsset: "usdc", ToAsset: "sol", AmountIn: d("2999"), AmountOut: d("20")},
	}
	_, linear := reconstructPath(legs, "eth", "sol")
	if linear {
		t.Fatal("fan-out path must not be linear")
	}
}

func TestNetGroupThreeHop(t *testing.T) {
	legs := []mhLeg{
		{FromAsset: "xrp", ToAsset: "usdt", AmountIn: d("100"), AmountOut: d("220")},
		{FromAsset: "usdt", ToAsset: "usdc", AmountIn: d("219.9"), AmountOut: d("219.8")},
		{FromAsset: "usdc", ToAsset: "near", AmountIn: d("219.7"), AmountOut: d("100")},
	}
	n := netGroup(legs, d("0.10"))
	if len(n.Sources) != 1 || len(n.Sinks) != 1 || len(n.Intermediates) != 2 {
		t.Fatalf("three-hop: %+v", n)
	}
	path, linear := reconstructPath(legs, "xrp", "near")
	if !linear || len(path) != 4 {
		t.Fatalf("three-hop path: %v %v", path, linear)
	}
}
