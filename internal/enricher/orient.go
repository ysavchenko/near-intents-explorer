// Package enricher prices legs against venue reference rates and computes the
// signed solver edge (port of reference/enrich.py + reference/lib/prices.py).
package enricher

import (
	"regexp"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"intents-explorer/internal/assets"
)

func init() {
	// Python's Decimal context carries 28 significant digits; match it for the
	// native-rate division before the float64 conversion.
	decimal.DivisionPrecision = 28
}

// Quote preference between the two sides of a pair (lower = more likely the
// quote/numeraire). Stables always win; then majors; everything else ranks
// last and ties break alphabetically. Orientation barely moves edge_bps but
// keeps native_rate readable.
var majors = []string{"BTC", "ETH", "BNB", "SOL", "XRP", "TON", "NEAR", "TRX", "POL", "LTC", "AVAX", "ZEC"}

func qrank(sym string) int {
	if assets.Stables[sym] {
		return -1
	}
	for i, m := range majors {
		if sym == m {
			return i
		}
	}
	return 10_000
}

// Orient returns (base, quote): quote is the more numeraire-like side.
func Orient(s1, s2 string) (string, string) {
	r1, r2 := qrank(s1), qrank(s2)
	if r1 == r2 {
		pair := []string{s1, s2}
		sort.Strings(pair) // deterministic tie-break
		return pair[0], pair[1]
	}
	if r2 < r1 {
		return s1, s2
	}
	return s2, s1
}

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]`)

// VenueSymbol is the ticker as the exchanges spell it — strip memecoin
// punctuation like the leading '$' ($WIF -> WIF) so the lookup resolves.
func VenueSymbol(sym string) string {
	return nonAlnum.ReplaceAllString(toUpper(sym), "")
}

func toUpper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

// MinuteMs floors a timestamp to its minute, in epoch milliseconds.
func MinuteMs(ts time.Time) int64 {
	return ts.Unix() / 60 * 60 * 1000
}

// Oriented is a leg prepared for pricing.
type Oriented struct {
	Par         bool // same-asset / stable<->stable leg benchmarked against a par fair rate
	CrossPair   bool // par leg between the USDT and USDC families: fair rate is the live cross, not 1.0
	FromBase    string
	ToBase      string
	Base, Quote string
	BaseAssetID string // asset id of the base side (notional fallback price)
	Pair        string // DIRECTED: from_base/to_base = received/given by the solver
	Side        string // buy_base | sell_base
	BaseAmt     decimal.Decimal
	QuoteAmt    decimal.Decimal
}

// CrossRate converts the Binance USDCUSDT mid (USDT per USDC) into this leg's
// quote-per-base fair rate. Only meaningful when CrossPair is set. Wrapped
// variants (USDT0, Tether's omnichain USDT) are already collapsed onto their
// base symbol by assets.BaseSymbol, so direct comparison suffices.
func (o *Oriented) CrossRate(usdtPerUsdc float64) float64 {
	if o.Quote == "USDT" { // base is USDC
		return usdtPerUsdc
	}
	return 1 / usdtPerUsdc // base is USDT, quote is USDC
}

// legInput is the slice of a legs row that orientation needs.
type legInput struct {
	LegClass  string
	FromAsset string
	ToAsset   string
	AmountIn  *decimal.Decimal // decimal-adjusted; nil if the asset was unknown at parse time
	AmountOut *decimal.Decimal
}

// orientLeg mirrors enrich.py's leg-building. terminal=true means the leg can
// never be priced (unknown asset, degenerate amounts) — mark no_reference.
func orientLeg(reg *assets.Registry, l legInput) (o *Oriented, terminal bool) {
	fa, ta := reg.Get(l.FromAsset), reg.Get(l.ToAsset)
	var fb, tb string
	if fa != nil {
		fb = fa.BaseSymbol()
	}
	if ta != nil {
		tb = ta.BaseSymbol()
	}
	switch l.LegClass {
	case "interesting":
		if fb == "" || tb == "" || fb == tb || l.AmountIn == nil || l.AmountOut == nil {
			return nil, true
		}
		base, quote := Orient(fb, tb)
		o := &Oriented{
			FromBase: fb, ToBase: tb, Base: base, Quote: quote,
			Pair: fb + "/" + tb,
		}
		if fb == base {
			o.Side = "buy_base"
			o.BaseAssetID = l.FromAsset
			o.BaseAmt, o.QuoteAmt = *l.AmountIn, *l.AmountOut
		} else {
			o.Side = "sell_base"
			o.BaseAssetID = l.ToAsset
			o.BaseAmt, o.QuoteAmt = *l.AmountOut, *l.AmountIn
		}
		return o, false
	case "same_asset", "stable_stable":
		// Par trade: same token (wrap/bridge) or stable<->stable. base = given
		// side, quote = received side, side = sell_base, so edge = native/fair
		// - 1: > 0 when the solver kept a spread over the fair rate. The fair
		// rate is 1.0 — except for USDT<->USDC, which persistently trades off
		// par (USDC premium averaged +9 bps over the 30 days to 2026-07-18,
		// ranging +4.5..+26.5), so those legs are benchmarked against the live
		// Binance USDCUSDT minute mid instead of a hardcoded constant.
		if fb == "" || tb == "" || l.AmountIn == nil || l.AmountOut == nil ||
			l.AmountIn.IsZero() || l.AmountOut.IsZero() {
			return nil, true
		}
		cross := (fb == "USDT" && tb == "USDC") || (fb == "USDC" && tb == "USDT")
		return &Oriented{
			Par: true, CrossPair: cross,
			FromBase: fb, ToBase: tb, Base: tb, Quote: fb,
			Pair: fb + "/" + tb, BaseAssetID: l.ToAsset,
			BaseAmt: *l.AmountOut, QuoteAmt: *l.AmountIn,
			Side: "sell_base",
		}, false
	default: // unknown / unclassified — surfaced, never priced
		return nil, true
	}
}

// NeededSymbols returns the non-stable symbols whose venue mids this leg needs
// (par legs only need the base for USD notional; their rate is pinned to 1.0).
func (o *Oriented) NeededSymbols() []string {
	var out []string
	if o.Par {
		if !assets.Stables[o.Base] {
			out = append(out, o.Base)
		}
		return out
	}
	for _, s := range []string{o.Base, o.Quote} {
		if !assets.Stables[s] {
			out = append(out, s)
		}
	}
	return out
}

// Edge computes the signed solver edge in bps: > 0 always means the solver
// transacted better than the venue mid (gross, ignoring fees/spread).
func Edge(native float64, rate *float64, side string) *float64 {
	if native == 0 || rate == nil || *rate == 0 {
		return nil
	}
	var e float64
	if side == "sell_base" {
		e = (native / *rate - 1) * 1e4
	} else {
		e = (*rate/native - 1) * 1e4
	}
	return &e
}
