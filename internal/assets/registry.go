// Package assets resolves NEAR Intents asset ids to symbol/decimals/chain via
// the official 1Click token list (port of reference/lib/assets.py).
//
// Asset id forms seen on-chain:
//
//	nep141:eth-0xa0b8...omft.near    omni-bridged ERC20 (chain + contract in id)
//	nep141:sol.omft.near             bridged native
//	nep141:wrap.near                 wrapped NEAR
//	nep245:v2_1.omni.hot.tg:56_2CMM  HOT omni multi-token (numeric chain id embedded)
package assets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// Stables are symbols treated as USD-pegged stablecoins (priced at 1.0). EUR
// pegs and yield-bearing vault tokens are intentionally excluded — they carry
// price risk.
var Stables = map[string]bool{
	"USDT": true, "USDC": true, "DAI": true, "FDUSD": true, "TUSD": true,
	"USDE": true, "USDP": true, "PYUSD": true, "USDD": true, "GUSD": true,
	"LUSD": true, "USD1": true, "GHO": true, "CRVUSD": true, "SUSDE": true,
	"USDF": true, "USDB": true, "FRAX": true, "USDS": true, "USDG": true,
	"BUSD": true, "USDT0": true, "SUSDC": true, "USDCX": true, "NRUSDT": true,
	"USDON": true,
}

// BinanceAlias maps a symbol to its reference base symbol when they differ
// (wrapped/renamed tokens).
var BinanceAlias = map[string]string{
	"BTC(OMNI)": "BTC", "WNEAR": "NEAR", "WETH": "ETH", "WBTC": "BTC",
	"CBBTC": "BTC", "WSOL": "SOL", "WBNB": "BNB", "WMATIC": "POL",
	"MATIC": "POL", "WAVAX": "AVAX", "WPOL": "POL",
}

type Asset struct {
	AssetID    string
	Symbol     string
	Decimals   int
	Blockchain string
	Contract   string
	PriceUSD   *float64 // 1Click snapshot mark, used only for approx notional fallback
}

func (a *Asset) NormSymbol() string { return strings.ToUpper(a.Symbol) }

func (a *Asset) IsStable() bool { return Stables[a.NormSymbol()] }

// BaseSymbol is the underlying asset identity, unwrapping wrapped/native variants.
func (a *Asset) BaseSymbol() string {
	if b, ok := BinanceAlias[a.NormSymbol()]; ok {
		return b
	}
	return a.NormSymbol()
}

// FormatAmount converts a base-unit integer (as decimal string) to a human
// decimal using the token's decimals.
func (a *Asset) FormatAmount(raw decimal.Decimal) decimal.Decimal {
	return raw.Shift(int32(-a.Decimals))
}

// Registry is a point-in-time snapshot of the token list. Lookups are
// read-only and safe for concurrent use; Refresh swaps the map atomically.
type Registry struct {
	mu     sync.RWMutex
	assets map[string]*Asset
}

func NewRegistry() *Registry { return &Registry{assets: map[string]*Asset{}} }

// tokenJSON is the 1Click /v0/tokens row shape.
type tokenJSON struct {
	AssetID         string   `json:"assetId"`
	Symbol          string   `json:"symbol"`
	Decimals        int      `json:"decimals"`
	Blockchain      string   `json:"blockchain"`
	ContractAddress string   `json:"contractAddress"`
	Price           *float64 `json:"price"`
}

// LoadJSON replaces the registry contents from a raw token-list document.
func (r *Registry) LoadJSON(raw []byte) error {
	var tokens []tokenJSON
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return fmt.Errorf("token list: %w", err)
	}
	assets := make(map[string]*Asset, len(tokens))
	for _, t := range tokens {
		sym := t.Symbol
		if sym == "" {
			sym = "?"
		}
		chain := t.Blockchain
		if chain == "" {
			chain = "?"
		}
		assets[t.AssetID] = &Asset{
			AssetID:    t.AssetID,
			Symbol:     sym,
			Decimals:   t.Decimals,
			Blockchain: chain,
			Contract:   t.ContractAddress,
			PriceUSD:   t.Price,
		}
	}
	r.mu.Lock()
	r.assets = assets
	r.mu.Unlock()
	return nil
}

// Fetch downloads the token list and replaces the registry on success.
func (r *Registry) Fetch(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	return r.LoadJSON(raw)
}

func (r *Registry) Get(assetID string) *Asset {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.assets[assetID]
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.assets)
}

// All returns the current snapshot's assets (for persisting to the DB).
func (r *Registry) All() []*Asset {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Asset, 0, len(r.assets))
	for _, a := range r.assets {
		out = append(out, a)
	}
	return out
}

// Label is a short human label: SYMBOL@chain (falls back to the raw id).
func (r *Registry) Label(assetID string) string {
	if a := r.Get(assetID); a != nil {
		return a.NormSymbol() + "@" + a.Blockchain
	}
	return assetID
}

// ClassifyPair classifies a swap leg's two assets:
//
//	same_asset    — same underlying (wrapped/native/cross-chain variants)
//	stable_stable — both USD-pegged stablecoins (no price risk)
//	interesting   — a price-bearing swap we want for edge stats
//	unknown       — at least one asset not in the registry (surfaced)
func (r *Registry) ClassifyPair(assetA, assetB string) string {
	a, b := r.Get(assetA), r.Get(assetB)
	if a == nil || b == nil {
		return "unknown"
	}
	if a.BaseSymbol() == b.BaseSymbol() {
		return "same_asset"
	}
	if a.IsStable() && b.IsStable() {
		return "stable_stable"
	}
	return "interesting"
}
