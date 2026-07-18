package intents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"intents-explorer/internal/assets"
	"intents-explorer/internal/neardata"

	"github.com/shopspring/decimal"
)

// Golden test: the Go parser must reproduce the Python oracle's legs exactly
// (solver, assets, raw amounts, class, order) for every handoff fixture.
//
// expected_legs.json may list the same tx at two consecutive heights (an
// artifact of the old RPC walk re-reading a repeated chunk header); the legs
// are identical, so we compare against the group at the fixture's own height.

type fixtureTx struct {
	TxHash      string          `json:"tx_hash"`
	BlockHeight int64           `json:"block_height"`
	Status      json.RawMessage `json:"status"`
	Transaction struct {
		Actions []json.RawMessage `json:"actions"`
	} `json:"transaction"`
}

type expectedLeg struct {
	Block        int64  `json:"block"`
	Solver       string `json:"solver"`
	LegClass     string `json:"leg_class"`
	MultiAsset   bool   `json:"multi_asset"`
	FromAsset    string `json:"from_asset"`
	FromName     string `json:"from_name"`
	ToAsset      string `json:"to_asset"`
	ToName       string `json:"to_name"`
	AmountInRaw  string `json:"amount_in_raw"`
	AmountIn     string `json:"amount_in"`
	AmountOutRaw string `json:"amount_out_raw"`
	AmountOut    string `json:"amount_out"`
}

func loadTestRegistry(t *testing.T) *assets.Registry {
	t.Helper()
	raw, err := os.ReadFile("../assets/testdata/tokens.json")
	if err != nil {
		t.Fatalf("read tokens snapshot: %v", err)
	}
	reg := assets.NewRegistry()
	if err := reg.LoadJSON(raw); err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

func TestFixturesMatchPythonOracle(t *testing.T) {
	reg := loadTestRegistry(t)

	rawExp, err := os.ReadFile("testdata/expected_legs.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string][]expectedLeg
	if err := json.Unmarshal(rawExp, &expected); err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob("testdata/tx_*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}

	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var fx fixtureTx
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			if !neardata.StatusSucceeded(fx.Status) {
				t.Fatalf("fixture %s is a failed settlement; all fixtures should be successes", fx.TxHash)
			}

			// Fresh SolverSet per tx: fixtures classify via the seed set alone.
			solvers := NewSolverSet()
			msgs, err := SignedFromActions(fx.Transaction.Actions)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			signers := map[string]bool{}
			for i := range msgs {
				if msgs[i].HasTokenDiff() {
					signers[msgs[i].Signer] = true
				}
			}
			solvers.Observe(signers)
			roles := ClassifySigners(msgs, solvers)
			legs := ExtractSolverLegs(msgs, roles)

			// Expected legs at the fixture's height (dedup across the RPC-walk artifact).
			var exp []expectedLeg
			for _, e := range expected[fx.TxHash] {
				if e.Block == fx.BlockHeight {
					exp = append(exp, e)
				}
			}
			if len(exp) == 0 {
				t.Fatalf("no expected legs for %s at height %d", fx.TxHash, fx.BlockHeight)
			}

			if len(legs) != len(exp) {
				t.Fatalf("leg count: got %d want %d", len(legs), len(exp))
			}
			for i, leg := range legs {
				e := exp[i]
				if leg.Signer != e.Solver {
					t.Errorf("leg %d solver: got %s want %s", i, leg.Signer, e.Solver)
				}
				if leg.RecvAsset != e.FromAsset || leg.GiveAsset != e.ToAsset {
					t.Errorf("leg %d assets: got %s -> %s want %s -> %s",
						i, leg.RecvAsset, leg.GiveAsset, e.FromAsset, e.ToAsset)
				}
				if leg.RecvAmount.String() != e.AmountInRaw || leg.GiveAmount.String() != e.AmountOutRaw {
					t.Errorf("leg %d raw amounts: got %s/%s want %s/%s",
						i, leg.RecvAmount, leg.GiveAmount, e.AmountInRaw, e.AmountOutRaw)
				}
				if leg.MultiAsset != e.MultiAsset {
					t.Errorf("leg %d multi_asset: got %v want %v", i, leg.MultiAsset, e.MultiAsset)
				}
				if got := reg.ClassifyPair(leg.RecvAsset, leg.GiveAsset); got != e.LegClass {
					t.Errorf("leg %d class: got %s want %s", i, got, e.LegClass)
				}
				if got := reg.Label(leg.RecvAsset); got != e.FromName {
					t.Errorf("leg %d from_name: got %s want %s", i, got, e.FromName)
				}
				if got := reg.Label(leg.GiveAsset); got != e.ToName {
					t.Errorf("leg %d to_name: got %s want %s", i, got, e.ToName)
				}
				// Decimal-adjusted amounts must match numerically.
				if a := reg.Get(leg.RecvAsset); a != nil {
					got := a.FormatAmount(decimal.RequireFromString(leg.RecvAmount.String()))
					if !got.Equal(decimal.RequireFromString(e.AmountIn)) {
						t.Errorf("leg %d amount_in: got %s want %s", i, got, e.AmountIn)
					}
				}
				if a := reg.Get(leg.GiveAsset); a != nil {
					got := a.FormatAmount(decimal.RequireFromString(leg.GiveAmount.String()))
					if !got.Equal(decimal.RequireFromString(e.AmountOut)) {
						t.Errorf("leg %d amount_out: got %s want %s", i, got, e.AmountOut)
					}
				}
			}
		})
	}
}

// The MANIFEST notes fixtures only cover nep413 + erc191; make sure each
// fixture's payload standards actually parse (no silently-skipped messages).
func TestFixtureMessageCounts(t *testing.T) {
	counts := map[string]int{ // tx -> n_signed_messages from the MANIFEST
		"CqEGVbMajEHonbzb9ai2yvDeKCUuXzF2tY6DVLccBXby": 5,
		"AJjgNtgv3P16WUHjmNdk8JRWhqwsgmD2SriC1QEA7GEC": 4,
		"7j9CRcpi23A2Yc3efSATWvvwhRaqYkVfA96njwbrHwWk": 3,
		"4u23tHV6mDjGetDxyHCt6PT9RGuR4HsSXe3DZZNrz7Yb": 4,
		"G5SwitQnPHEJCJ7WKKj1S2Ln6WHeE7r6mTshY1prD5Dq": 6,
	}
	files, _ := filepath.Glob("testdata/tx_*.json")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var fx fixtureTx
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatal(err)
		}
		msgs, err := SignedFromActions(fx.Transaction.Actions)
		if err != nil {
			t.Fatal(err)
		}
		want, ok := counts[fx.TxHash]
		if !ok {
			t.Fatalf("fixture %s missing from MANIFEST counts (update %s)", fx.TxHash, strings.TrimPrefix(f, "testdata/"))
		}
		if len(msgs) != want {
			t.Errorf("%s: parsed %d signed messages, MANIFEST says %d", fx.TxHash, len(msgs), want)
		}
	}
}
