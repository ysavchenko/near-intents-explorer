package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"intents-explorer/internal/assets"
	"intents-explorer/internal/intents"
)

// balanceChunk is how many token ids go into one mt_batch_balance_of call.
const balanceChunk = 100

type balanceRow struct {
	AssetID  string   `json:"asset_id"`
	Label    string   `json:"label"`
	Balance  string   `json:"balance"` // decimal-adjusted
	PriceUSD *float64 `json:"price_usd"`
	ValueUSD *float64 `json:"value_usd"`
}

// handleSolverBalances queries the solver's NEP-245 balances held inside the
// intents.near contract (its working inventory) for every registry asset.
func (s *Server) handleSolverBalances(w http.ResponseWriter, r *http.Request) {
	if s.RPC == nil {
		httpErr(w, 503, fmt.Errorf("no NEAR RPC configured"))
		return
	}
	solver := chi.URLParam(r, "id")
	all := s.Registry.All()
	ids := make([]string, 0, len(all))
	byID := make(map[string]*assets.Asset, len(all))
	for _, a := range all {
		ids = append(ids, a.AssetID)
		byID[a.AssetID] = a
	}
	sort.Strings(ids) // deterministic chunking

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	raw := make([]string, len(ids)) // raw balance per ids index
	for start := 0; start < len(ids); start += balanceChunk {
		start := start
		end := min(start+balanceChunk, len(ids))
		g.Go(func() error {
			out, err := s.RPC.CallFunction(ctx, intents.Contract, "mt_batch_balance_of",
				map[string]any{"account_id": solver, "token_ids": ids[start:end]})
			if err != nil {
				return err
			}
			var bals []string
			if err := json.Unmarshal(out, &bals); err != nil {
				return fmt.Errorf("mt_batch_balance_of result: %w", err)
			}
			if len(bals) != end-start {
				return fmt.Errorf("mt_batch_balance_of: got %d balances for %d tokens", len(bals), end-start)
			}
			copy(raw[start:], bals)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		httpErr(w, 502, err)
		return
	}

	rows := []balanceRow{}
	total := 0.0
	totalKnown := true
	for i, id := range ids {
		if raw[i] == "" || raw[i] == "0" {
			continue
		}
		n, err := decimal.NewFromString(raw[i])
		if err != nil || n.IsZero() {
			continue
		}
		a := byID[id]
		bal := a.FormatAmount(n)
		row := balanceRow{AssetID: id, Label: s.Registry.Label(id), Balance: bal.String()}
		price := a.PriceUSD
		if price == nil && a.IsStable() {
			one := 1.0
			price = &one
		}
		if price != nil {
			row.PriceUSD = price
			f, _ := bal.Float64()
			v := f * *price
			row.ValueUSD = &v
			total += v
		} else {
			totalKnown = false
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		vi, vj := -1.0, -1.0
		if rows[i].ValueUSD != nil {
			vi = *rows[i].ValueUSD
		}
		if rows[j].ValueUSD != nil {
			vj = *rows[j].ValueUSD
		}
		return vi > vj
	})

	writeJSON(w, map[string]any{
		"solver": solver, "contract": intents.Contract,
		"rows": rows, "total_usd": total, "total_complete": totalKnown,
		"fetched_at": time.Now().UTC(),
	})
}
