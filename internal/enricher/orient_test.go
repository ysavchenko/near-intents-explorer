package enricher

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"intents-explorer/internal/assets"
)

func testRegistry(t *testing.T) *assets.Registry {
	t.Helper()
	raw, err := os.ReadFile("../assets/testdata/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	reg := assets.NewRegistry()
	if err := reg.LoadJSON(raw); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestOrient(t *testing.T) {
	cases := []struct{ s1, s2, base, quote string }{
		{"LTC", "USDC", "LTC", "USDC"},   // stable is quote
		{"USDC", "BTC", "BTC", "USDC"},   // order-independent
		{"ETH", "SOL", "SOL", "ETH"},     // both majors: lower rank (ETH) is quote
		{"XLM", "TRX", "XLM", "TRX"},     // major beats unranked
		{"AAA", "BBB", "AAA", "BBB"},     // tie: alphabetical, first is base
		{"USDT", "USDC", "USDC", "USDT"}, // stable tie: alphabetical, first is base
	}
	for _, c := range cases {
		base, quote := Orient(c.s1, c.s2)
		if base != c.base || quote != c.quote {
			t.Errorf("Orient(%s,%s): got (%s,%s) want (%s,%s)", c.s1, c.s2, base, quote, c.base, c.quote)
		}
	}
}

func TestVenueSymbol(t *testing.T) {
	for in, want := range map[string]string{
		"$WIF": "WIF", "ZEC": "ZEC", "usdt": "USDT", "BTC(OMNI)": "BTCOMNI", "$$": "",
	} {
		if got := VenueSymbol(in); got != want {
			t.Errorf("VenueSymbol(%q) = %q want %q", in, got, want)
		}
	}
}

func TestMinuteMs(t *testing.T) {
	ts := time.Date(2026, 7, 9, 23, 15, 22, 500e6, time.UTC)
	want := time.Date(2026, 7, 9, 23, 15, 0, 0, time.UTC).UnixMilli()
	if got := MinuteMs(ts); got != want {
		t.Errorf("MinuteMs: got %d want %d", got, want)
	}
}

func dec(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func f64(v float64) *float64 { return &v }

// Real rows from handoff/samples/enriched_legs.sample.csv — the Go edge math
// must reproduce the oracle's numbers.
func TestEdgeMatchesSampleRows(t *testing.T) {
	// LTC/USDC buy_base: native 43.84610961, hl 43.928 -> +18.68 bps
	native := 43.539143 / 0.992999
	e := Edge(native, f64(43.928), "buy_base")
	if e == nil || math.Abs(*e-18.68) > 0.01 {
		t.Errorf("buy_base edge: got %v want 18.68", e)
	}
	// USDC/BTC sell_base: native 63214.66279, hl 63181 -> +5.33 bps
	native = 43.539099 / 0.00068875
	e = Edge(native, f64(63181), "sell_base")
	if e == nil || math.Abs(*e-5.33) > 0.01 {
		t.Errorf("sell_base edge: got %v want 5.33", e)
	}
	// Par USDT/USDC sell_base vs 1.0: native 1.000610088 -> +6.10 bps
	e = Edge(1.000610088, f64(1.0), "sell_base")
	if e == nil || math.Abs(*e-6.10) > 0.01 {
		t.Errorf("par edge: got %v want 6.10", e)
	}
	// Guards: zero native or nil/zero rate -> nil
	if Edge(0, f64(1), "sell_base") != nil || Edge(1, nil, "sell_base") != nil || Edge(1, f64(0), "buy_base") != nil {
		t.Error("degenerate inputs must yield nil edge")
	}
}

const (
	usdtEth  = "nep141:eth-0xdac17f958d2ee523a2206206994597c13d831ec7.omft.near"
	usdcEth  = "nep141:eth-0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48.omft.near"
	usdtBsc  = "nep245:v2_1.omni.hot.tg:56_2CMMyVTGZkeyNZTSvS5sarzfir6g"
	usdt0Arb = "nep141:arb-0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9.omft.near"
	daiEth   = "nep141:eth-0x6b175474e89094c44da98b954eedeac495271d0f.omft.near"
	ltc      = "nep141:ltc.omft.near"
	xlm      = "nep245:v2_1.omni.hot.tg:1100_111bzQBB5v7AhLyPMDwS8uJgQV24KaAPXtwyVWu2KXbbfQU6NXRCz"
)

func TestOrientLegInteresting(t *testing.T) {
	reg := testRegistry(t)
	// Solver received XLM, gave USDT (fixture leg): base=XLM, quote=USDT,
	// solver bought base.
	o, term := orientLeg(reg, legInput{
		LegClass: "interesting", FromAsset: xlm, ToAsset: usdtEth,
		AmountIn: dec("13.8599861"), AmountOut: dec("2.634851"),
	})
	if term {
		t.Fatal("unexpected terminal")
	}
	if o.Pair != "XLM/USDT" || o.Side != "buy_base" || o.Base != "XLM" || o.Quote != "USDT" {
		t.Errorf("orientation: %+v", o)
	}
	if !o.BaseAmt.Equal(decimal.RequireFromString("13.8599861")) ||
		!o.QuoteAmt.Equal(decimal.RequireFromString("2.634851")) {
		t.Errorf("amounts: base=%s quote=%s", o.BaseAmt, o.QuoteAmt)
	}
	if o.BaseAssetID != xlm {
		t.Errorf("base asset id: %s", o.BaseAssetID)
	}
	if got := o.NeededSymbols(); len(got) != 1 || got[0] != "XLM" {
		t.Errorf("needed symbols: %v", got)
	}

	// Solver received USDT, gave LTC: solver sold base (LTC).
	o, term = orientLeg(reg, legInput{
		LegClass: "interesting", FromAsset: usdtEth, ToAsset: ltc,
		AmountIn: dec("4.982915"), AmountOut: dec("0.11224754"),
	})
	if term || o.Side != "sell_base" || o.Base != "LTC" || o.Pair != "USDT/LTC" {
		t.Errorf("sell_base orientation: %+v", o)
	}
	if o.BaseAssetID != ltc || !o.BaseAmt.Equal(decimal.RequireFromString("0.11224754")) {
		t.Errorf("sell_base base side: %+v", o)
	}
}

func TestOrientLegPar(t *testing.T) {
	reg := testRegistry(t)
	// same_asset USDT@bsc -> USDT@eth: par, base = given side, quote = received.
	o, term := orientLeg(reg, legInput{
		LegClass: "same_asset", FromAsset: usdtBsc, ToAsset: usdtEth,
		AmountIn: dec("4.983418363808652768"), AmountOut: dec("4.98292"),
	})
	if term {
		t.Fatal("unexpected terminal")
	}
	if !o.Par || o.Side != "sell_base" || o.Pair != "USDT/USDT" {
		t.Errorf("par orientation: %+v", o)
	}
	if !o.BaseAmt.Equal(decimal.RequireFromString("4.98292")) {
		t.Errorf("par base amount must be the GIVEN side: %s", o.BaseAmt)
	}
	if len(o.NeededSymbols()) != 0 {
		t.Errorf("stable par leg needs no venue symbols: %v", o.NeededSymbols())
	}
	// native = recv/give > 1 -> positive edge (solver kept the spread)
	native, _ := o.QuoteAmt.Div(o.BaseAmt).Float64()
	e := Edge(native, f64(1.0), o.Side)
	if e == nil || *e <= 0 {
		t.Errorf("par edge should be positive: %v", e)
	}
}

func TestCrossPairDetectionAndRate(t *testing.T) {
	reg := testRegistry(t)
	// USDC received, USDT given: base=USDT (given), quote=USDC. The fair rate
	// is USDC-per-USDT = 1/p where p = USDT per USDC (Binance USDCUSDT mid).
	o, term := orientLeg(reg, legInput{
		LegClass: "stable_stable", FromAsset: usdcEth, ToAsset: usdtEth,
		AmountIn: dec("21.013484"), AmountOut: dec("21.020725805751890000"),
	})
	if term || !o.Par || !o.CrossPair {
		t.Fatalf("USDC<->USDT must be a cross par leg: %+v", o)
	}
	p := 1.0006 // USDC premium of ~6 bps
	if got := o.CrossRate(p); math.Abs(got-1/1.0006) > 1e-12 {
		t.Errorf("USDT-base cross rate: got %v want %v", got, 1/1.0006)
	}
	// With the real cross the solver's edge turns positive: it gave 1.00034
	// USDT per USDC received while fair is 1.0006.
	native, _ := o.QuoteAmt.Div(o.BaseAmt).Float64()
	rate := o.CrossRate(p)
	e := Edge(native, &rate, o.Side)
	if e == nil || *e <= 0 || *e > 10 {
		t.Errorf("cross-corrected edge should be small positive: %v", e)
	}
	// Old 1.0 benchmark called the same leg negative — the mispricing we fix.
	one := 1.0
	if old := Edge(native, &one, o.Side); old == nil || *old >= 0 {
		t.Errorf("sanity: 1.0 benchmark should be negative here: %v", old)
	}

	// Reverse direction: USDT received, USDC given -> base=USDC, rate = p.
	o2, term := orientLeg(reg, legInput{
		LegClass: "stable_stable", FromAsset: usdtEth, ToAsset: usdcEth,
		AmountIn: dec("100.01"), AmountOut: dec("100"),
	})
	if term || !o2.CrossPair {
		t.Fatalf("reverse cross: %+v", o2)
	}
	if got := o2.CrossRate(p); got != p {
		t.Errorf("USDC-base cross rate: got %v want %v", got, p)
	}
}

func TestStableFamily(t *testing.T) {
	if stableFamily("USDT0") != "USDT" || stableFamily("USDT") != "USDT" ||
		stableFamily("USDC") != "USDC" || stableFamily("DAI") != "DAI" {
		t.Error("stableFamily mapping wrong")
	}
	reg := testRegistry(t)
	// USDT<->DAI stays a plain 1.0 par leg (no liquid managed cross wired up).
	o, term := orientLeg(reg, legInput{
		LegClass: "stable_stable", FromAsset: usdtEth, ToAsset: daiEth,
		AmountIn: dec("1"), AmountOut: dec("1"),
	})
	if term || o.CrossPair {
		t.Errorf("USDT<->DAI must not be a cross pair: %+v", o)
	}
	// USDT0 (omnichain USDT) <-> USDC uses the cross via the family mapping.
	o, term = orientLeg(reg, legInput{
		LegClass: "stable_stable", FromAsset: usdt0Arb, ToAsset: usdcEth,
		AmountIn: dec("1"), AmountOut: dec("1"),
	})
	if term || !o.CrossPair {
		t.Errorf("USDT0<->USDC must be a cross pair: %+v", o)
	}
	// USDT0 <-> USDT is the same family: plain 1.0 par.
	o, term = orientLeg(reg, legInput{
		LegClass: "stable_stable", FromAsset: usdt0Arb, ToAsset: usdtEth,
		AmountIn: dec("1"), AmountOut: dec("1"),
	})
	if term || o.CrossPair {
		t.Errorf("USDT0<->USDT must not be a cross pair: %+v", o)
	}
}

func TestOrientLegTerminal(t *testing.T) {
	reg := testRegistry(t)
	if _, term := orientLeg(reg, legInput{LegClass: "unknown", FromAsset: "x", ToAsset: "y"}); !term {
		t.Error("unknown class must be terminal")
	}
	if _, term := orientLeg(reg, legInput{
		LegClass: "interesting", FromAsset: "nep141:not-a-token.near", ToAsset: usdtEth,
		AmountIn: dec("1"), AmountOut: dec("1")}); !term {
		t.Error("unknown asset must be terminal")
	}
	if _, term := orientLeg(reg, legInput{
		LegClass: "interesting", FromAsset: usdtEth, ToAsset: usdcEth,
		AmountIn: nil, AmountOut: dec("1")}); !term {
		t.Error("missing amounts must be terminal")
	}
}
