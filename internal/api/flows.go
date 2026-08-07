package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// flowOut is one solver deposit/withdrawal/transfer row.
type flowOut struct {
	Ts                   time.Time `json:"ts"`
	Tx                   string    `json:"tx"`
	ReceiptID            string    `json:"receipt_id"`
	Seq                  int       `json:"seq"`
	Solver               string    `json:"solver"`
	Direction            string    `json:"direction"`
	AssetID              string    `json:"asset_id"`
	Label                string    `json:"label"`
	Chain                string    `json:"chain"` // asset's chain (explorer links for external addresses)
	Amount               *string   `json:"amount"`
	ValueUSD             *float64  `json:"value_usd"`
	Counterparty         *string   `json:"counterparty"`
	CounterpartyWithdrew bool      `json:"counterparty_withdrew"`
	ExternalAddress      *string   `json:"external_address"`
	OriginChain          *string   `json:"origin_chain"`
	OriginTx             *string   `json:"origin_tx"`
	Memo                 *string   `json:"memo"`
}

type flowTotal struct {
	N        int64   `json:"n"`
	ValueUSD float64 `json:"value_usd"`
	Complete bool    `json:"complete"` // false when some rows were unpriceable
}

// priceOf returns the registry snapshot price (stables default to $1).
func (s *Server) priceOf(assetID string) *float64 {
	a := s.Registry.Get(assetID)
	if a == nil {
		return nil
	}
	if a.PriceUSD != nil {
		return a.PriceUSD
	}
	if a.IsStable() {
		one := 1.0
		return &one
	}
	return nil
}

// handleFlows lists solver deposits/withdrawals/transfers (newest first) with
// per-direction totals over the whole window. USD values are current-price
// approximations from the registry snapshot, matching the balances endpoint.
func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > 5000 {
			httpErr(w, 400, fmt.Errorf("bad limit"))
			return
		}
	}
	f := &legFilters{}
	f.addf("block_ts >= ?", win.From)
	f.addf("block_ts < ?", win.To)
	if v := r.URL.Query().Get("solver"); v != "" {
		f.addf("account_id = ?", v)
	}
	if v := r.URL.Query().Get("direction"); v != "" {
		f.addf("direction = ?", v)
	}
	if v := r.URL.Query().Get("asset"); v != "" {
		f.addf("asset_id = ?", v)
	}

	q := fmt.Sprintf(`
		SELECT block_ts, tx_hash, receipt_id, seq, account_id, direction, asset_id,
		       amount::text, counterparty, counterparty_withdrew,
		       external_address, origin_chain, origin_tx, memo
		FROM solver_flows WHERE %s
		ORDER BY block_ts DESC, id DESC LIMIT %d`, f.where(), limit)
	rows, err := s.Pool.Query(r.Context(), q, f.args...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	defer rows.Close()

	out := []flowOut{}
	for rows.Next() {
		var o flowOut
		if err := rows.Scan(&o.Ts, &o.Tx, &o.ReceiptID, &o.Seq, &o.Solver, &o.Direction, &o.AssetID,
			&o.Amount, &o.Counterparty, &o.CounterpartyWithdrew,
			&o.ExternalAddress, &o.OriginChain, &o.OriginTx, &o.Memo); err != nil {
			httpErr(w, 500, err)
			return
		}
		o.Label = s.Registry.Label(o.AssetID)
		if a := s.Registry.Get(o.AssetID); a != nil {
			o.Chain = a.Blockchain
		}
		if o.Amount != nil {
			if p := s.priceOf(o.AssetID); p != nil {
				if amt, err := strconv.ParseFloat(*o.Amount, 64); err == nil {
					v := amt * *p
					o.ValueUSD = &v
				}
			}
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		httpErr(w, 500, err)
		return
	}

	// Window totals: aggregate per (direction, asset) so USD conversion is
	// exact over the full window, not just the returned page.
	tq := fmt.Sprintf(`
		SELECT direction, asset_id, count(*), count(amount), sum(amount)::text
		FROM solver_flows WHERE %s GROUP BY direction, asset_id`, f.where())
	trows, err := s.Pool.Query(r.Context(), tq, f.args...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	defer trows.Close()
	totals := map[string]*flowTotal{}
	for trows.Next() {
		var direction, assetID string
		var n, nAmt int64
		var sum *string
		if err := trows.Scan(&direction, &assetID, &n, &nAmt, &sum); err != nil {
			httpErr(w, 500, err)
			return
		}
		t := totals[direction]
		if t == nil {
			t = &flowTotal{Complete: true}
			totals[direction] = t
		}
		t.N += n
		p := s.priceOf(assetID)
		if sum == nil || p == nil || nAmt < n {
			t.Complete = false
			if sum == nil || p == nil {
				continue
			}
		}
		amt, err := strconv.ParseFloat(*sum, 64)
		if err != nil {
			t.Complete = false
			continue
		}
		t.ValueUSD += amt * *p
	}
	if err := trows.Err(); err != nil {
		httpErr(w, 500, err)
		return
	}

	writeJSON(w, map[string]any{
		"from": win.From, "to": win.To,
		"rows": out, "totals": totals,
	})
}
