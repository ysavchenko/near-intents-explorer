package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ---------------------------------------------------------------------------
// Same-asset (wrap/bridge) flows — port of reference/same_asset_edges.py
// ---------------------------------------------------------------------------

// sizeBuckets mirrors the script's amount-size buckets.
var sizeBuckets = []struct {
	Lo, Hi float64
	Label  string
}{
	{0, 100, "< $100"},
	{100, 1_000, "$100 - $1K"},
	{1_000, 10_000, "$1K - $10K"},
	{10_000, 100_000, "$10K - $100K"},
	{100_000, math.Inf(1), ">= $100K"},
}

func sizeBucket(v float64) string {
	for _, b := range sizeBuckets {
		if v >= b.Lo && v < b.Hi {
			return b.Label
		}
	}
	return "?"
}

type saLeg struct {
	Solver    string
	Ts        time.Time
	Tx        string
	RecvChain string
	GiveChain string
	Bps       float64
	Notional  float64
}

type vwStats struct {
	N       int64    `json:"n"`
	Volume  float64  `json:"volume_usd"`
	VWBps   *float64 `json:"vw_bps"`
	MeanBps *float64 `json:"mean_bps"`
}

func vw(legs []saLeg) vwStats {
	s := vwStats{N: int64(len(legs))}
	var wsum, bsum float64
	for _, l := range legs {
		s.Volume += l.Notional
		wsum += l.Bps * l.Notional
		bsum += l.Bps
	}
	if s.Volume > 0 {
		v := wsum / s.Volume
		s.VWBps = &v
	}
	if s.N > 0 {
		m := bsum / float64(s.N)
		s.MeanBps = &m
	}
	return s
}

// handleSameAsset reports same-underlying legs where the solver kept an in/out
// spread: chain routes, optional hub table and size buckets.
// in_out_diff_bps = (amount_in/amount_out - 1)*1e4; > 0 = solver received more
// than it gave.
func (s *Server) handleSameAsset(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	symbols := map[string]bool{}
	symParam := r.URL.Query().Get("symbols")
	if symParam == "" {
		symParam = "USDT,USDC"
	}
	for _, sym := range strings.Split(symParam, ",") {
		if sym = strings.ToUpper(strings.TrimSpace(sym)); sym != "" {
			symbols[sym] = true
		}
	}
	f := &legFilters{}
	f.addf("l.block_ts >= ?", win.From)
	f.addf("l.block_ts < ?", win.To)
	f.addf("l.leg_class = 'same_asset'")
	f.addf("l.amount_in IS NOT NULL AND l.amount_out IS NOT NULL AND l.amount_in <> l.amount_out")
	if v := r.URL.Query().Get("solver"); v != "" {
		f.addf("l.solver = ?", v)
	}

	q := fmt.Sprintf(`
		SELECT l.solver, l.block_ts, l.tx_hash, l.from_asset, l.to_asset,
		       l.amount_in::text, l.amount_out::text, l.notional_usd
		FROM legs l WHERE %s ORDER BY l.block_ts DESC LIMIT 200000`, f.where())
	rows, err := s.Pool.Query(r.Context(), q, f.args...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	defer rows.Close()

	var legs []saLeg
	for rows.Next() {
		var solver, tx, fromAsset, toAsset, amountIn, amountOut string
		var ts time.Time
		var notional *float64
		if err := rows.Scan(&solver, &ts, &tx, &fromAsset, &toAsset, &amountIn, &amountOut, &notional); err != nil {
			httpErr(w, 500, err)
			return
		}
		fa, ta := s.Registry.Get(fromAsset), s.Registry.Get(toAsset)
		if fa == nil || ta == nil || !symbols[fa.BaseSymbol()] || fa.BaseSymbol() != ta.BaseSymbol() {
			continue
		}
		recv := decimal.RequireFromString(amountIn)
		give := decimal.RequireFromString(amountOut)
		if give.IsZero() {
			continue
		}
		bps, _ := recv.Div(give).Sub(decimal.NewFromInt(1)).Mul(decimal.NewFromInt(10000)).Float64()
		n := 0.0
		if notional != nil {
			n = *notional
		} else if fa.IsStable() {
			n, _ = recv.Float64()
		} else if fa.PriceUSD != nil {
			rf, _ := recv.Float64()
			n = rf * *fa.PriceUSD
		}
		legs = append(legs, saLeg{
			Solver: solver, Ts: ts, Tx: tx,
			RecvChain: fa.Blockchain, GiveChain: ta.Blockchain,
			Bps: bps, Notional: n,
		})
	}
	if err := rows.Err(); err != nil {
		httpErr(w, 500, err)
		return
	}

	// Chain routes (recv_chain -> give_chain).
	byRoute := map[[2]string][]saLeg{}
	bySolver := map[string][]saLeg{}
	var totalVol float64
	for _, l := range legs {
		byRoute[[2]string{l.RecvChain, l.GiveChain}] = append(byRoute[[2]string{l.RecvChain, l.GiveChain}], l)
		bySolver[l.Solver] = append(bySolver[l.Solver], l)
		totalVol += l.Notional
	}
	type routeOut struct {
		RecvChain string  `json:"recv_chain"`
		GiveChain string  `json:"give_chain"`
		PctVol    float64 `json:"pct_vol"`
		vwStats
	}
	routes := []routeOut{}
	for k, ls := range byRoute {
		st := vw(ls)
		pct := 0.0
		if totalVol > 0 {
			pct = 100 * st.Volume / totalVol
		}
		routes = append(routes, routeOut{RecvChain: k[0], GiveChain: k[1], PctVol: pct, vwStats: st})
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Volume > routes[j].Volume })

	type solverOut struct {
		Solver string `json:"solver"`
		vwStats
	}
	solversOut := []solverOut{}
	for sv, ls := range bySolver {
		solversOut = append(solversOut, solverOut{Solver: sv, vwStats: vw(ls)})
	}
	sort.Slice(solversOut, func(i, j int) bool { return solversOut[i].Volume > solversOut[j].Volume })

	resp := map[string]any{
		"from": win.From, "to": win.To, "symbols": symParam,
		"n_legs": len(legs), "volume_usd": totalVol,
		"routes": routes, "by_solver": solversOut,
	}

	// Optional directional hub table (e.g. ?hub=tron).
	if hub := r.URL.Query().Get("hub"); hub != "" {
		into := map[string][]saLeg{} // X -> hub (solver sends out hub-side funds)
		out := map[string][]saLeg{}  // hub -> X
		for _, l := range legs {
			if l.GiveChain == hub && l.RecvChain != hub {
				into[l.RecvChain] = append(into[l.RecvChain], l)
			} else if l.RecvChain == hub && l.GiveChain != hub {
				out[l.GiveChain] = append(out[l.GiveChain], l)
			}
		}
		chains := map[string]bool{}
		for c := range into {
			chains[c] = true
		}
		for c := range out {
			chains[c] = true
		}
		type hubRow struct {
			Counterparty string  `json:"counterparty"`
			Into         vwStats `json:"into"`
			Out          vwStats `json:"out"`
		}
		hubRows := []hubRow{}
		for c := range chains {
			hubRows = append(hubRows, hubRow{Counterparty: c, Into: vw(into[c]), Out: vw(out[c])})
		}
		sort.Slice(hubRows, func(i, j int) bool {
			return hubRows[i].Into.Volume+hubRows[i].Out.Volume > hubRows[j].Into.Volume+hubRows[j].Out.Volume
		})
		resp["hub"] = map[string]any{"chain": hub, "rows": hubRows}
	}

	// Optional size buckets for one route (?route=eth:tron).
	if route := r.URL.Query().Get("route"); route != "" {
		parts := strings.SplitN(route, ":", 2)
		if len(parts) != 2 {
			httpErr(w, 400, fmt.Errorf("route must be RECV:GIVE, e.g. eth:tron"))
			return
		}
		var ls []saLeg
		for _, l := range legs {
			if l.RecvChain == parts[0] && l.GiveChain == parts[1] {
				ls = append(ls, l)
			}
		}
		byBucket := map[string][]saLeg{}
		var routeVol float64
		for _, l := range ls {
			byBucket[sizeBucket(l.Notional)] = append(byBucket[sizeBucket(l.Notional)], l)
			routeVol += l.Notional
		}
		type bucketRow struct {
			Bucket  string  `json:"bucket"`
			PctVol  float64 `json:"pct_vol"`
			AvgSize float64 `json:"avg_size_usd"`
			vwStats
		}
		bucketRows := []bucketRow{}
		for _, b := range sizeBuckets {
			g := byBucket[b.Label]
			st := vw(g)
			row := bucketRow{Bucket: b.Label, vwStats: st}
			if routeVol > 0 {
				row.PctVol = 100 * st.Volume / routeVol
			}
			if st.N > 0 {
				row.AvgSize = st.Volume / float64(st.N)
			}
			bucketRows = append(bucketRows, row)
		}
		resp["buckets"] = map[string]any{"route": route, "rows": bucketRows}
	}

	writeJSON(w, resp)
}

// ---------------------------------------------------------------------------
// Multi-hop routes — port of reference/multihop_routes.py
// ---------------------------------------------------------------------------

func (s *Server) handleMultihop(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	tol := decimal.NewFromFloat(0.10)
	if v := r.URL.Query().Get("tol"); v != "" {
		d, err := decimal.NewFromString(v)
		if err != nil || d.IsNegative() {
			httpErr(w, 400, fmt.Errorf("bad tol"))
			return
		}
		tol = d
	}
	minNotional := 0.0
	if v := r.URL.Query().Get("min_notional"); v != "" {
		if minNotional, err = strconv.ParseFloat(v, 64); err != nil {
			httpErr(w, 400, fmt.Errorf("bad min_notional"))
			return
		}
	}
	solverFilter := r.URL.Query().Get("solver")

	f := &legFilters{}
	f.addf("l.block_ts >= ?", win.From)
	f.addf("l.block_ts < ?", win.To)
	if solverFilter != "" {
		f.addf("l.solver = ?", solverFilter)
	}
	q := fmt.Sprintf(`
		WITH multi AS (
		  SELECT tx_hash, solver FROM legs l WHERE %s
		  GROUP BY tx_hash, solver HAVING count(*) >= 2
		)
		SELECT l.tx_hash, l.solver, l.block_ts, l.from_asset, l.to_asset,
		       l.amount_in::text, l.amount_out::text
		FROM legs l JOIN multi m ON m.tx_hash = l.tx_hash AND m.solver = l.solver
		WHERE %s
		ORDER BY l.tx_hash, l.solver, l.seq`, f.where(), f.where())
	q = renumberSecondWhereFrom(q, f)

	rows, err := s.Pool.Query(r.Context(), q, append(f.args, f.args...)...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	defer rows.Close()

	type groupKey struct{ Tx, Solver string }
	type rawLeg struct {
		mhLeg
		Ts         time.Time
		BadAmounts bool
	}
	groups := map[groupKey][]rawLeg{}
	var order []groupKey
	for rows.Next() {
		var k groupKey
		var ts time.Time
		var fromAsset, toAsset string
		var amountIn, amountOut *string
		if err := rows.Scan(&k.Tx, &k.Solver, &ts, &fromAsset, &toAsset, &amountIn, &amountOut); err != nil {
			httpErr(w, 500, err)
			return
		}
		l := rawLeg{Ts: ts}
		l.FromAsset, l.ToAsset = fromAsset, toAsset
		if amountIn == nil || amountOut == nil {
			l.BadAmounts = true
		} else {
			l.AmountIn = decimal.RequireFromString(*amountIn)
			l.AmountOut = decimal.RequireFromString(*amountOut)
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], l)
	}
	if err := rows.Err(); err != nil {
		httpErr(w, 500, err)
		return
	}

	base := func(aid string) string {
		if a := s.Registry.Get(aid); a != nil {
			return a.BaseSymbol()
		}
		return "?"
	}
	chain := func(aid string) string {
		if a := s.Registry.Get(aid); a != nil {
			return a.Blockchain
		}
		return "?"
	}
	notional := func(srcID, snkID string, inAmt, outAmt decimal.Decimal) *float64 {
		sa, ka := s.Registry.Get(srcID), s.Registry.Get(snkID)
		if ka != nil && ka.IsStable() {
			v, _ := outAmt.Float64()
			return &v
		}
		if sa != nil && sa.IsStable() {
			v, _ := inAmt.Float64()
			return &v
		}
		if sa != nil && sa.PriceUSD != nil {
			f, _ := inAmt.Float64()
			v := f * *sa.PriceUSD
			return &v
		}
		if ka != nil && ka.PriceUSD != nil {
			f, _ := outAmt.Float64()
			v := f * *ka.PriceUSD
			return &v
		}
		return nil
	}

	type routeOut struct {
		Tx                 string    `json:"tx"`
		Solver             string    `json:"solver"`
		Ts                 time.Time `json:"ts"`
		Hops               int       `json:"hops"`
		Path               string    `json:"path"`
		Linear             bool      `json:"linear"`
		SynthPair          string    `json:"synth_pair"`
		SynthClass         string    `json:"synth_class"`
		StableIntermediate bool      `json:"stable_intermediate"`
		Source             string    `json:"source"`
		SourceChain        string    `json:"source_chain"`
		SourceAssetID      string    `json:"source_asset_id"`
		Sink               string    `json:"sink"`
		SinkChain          string    `json:"sink_chain"`
		SinkAssetID        string    `json:"sink_asset_id"`
		Intermediates      []string  `json:"intermediates"`
		AmountIn           string    `json:"amount_in"`
		AmountOut          string    `json:"amount_out"`
		NativeRate         *float64  `json:"native_rate"`
		NotionalUSD        *float64  `json:"notional_usd"`
	}

	var routes []routeOut
	interCount := map[string]int{}
	nMulti, clean, complexN, kept := 0, 0, 0, 0
	for _, k := range order {
		ls := groups[k]
		nMulti++
		bad := false
		legs := make([]mhLeg, 0, len(ls))
		for _, l := range ls {
			if l.BadAmounts {
				bad = true
				break
			}
			legs = append(legs, l.mhLeg)
		}
		if bad {
			complexN++
			continue
		}
		n := netGroup(legs, tol)
		if len(n.Sources) != 1 || len(n.Sinks) != 1 || len(n.Intermediates) == 0 {
			complexN++
			continue
		}
		clean++
		srcID, snkID := n.Sources[0], n.Sinks[0]
		inAmt := n.Recv[srcID].Sub(n.Give[srcID])
		outAmt := n.Give[snkID].Sub(n.Recv[snkID])
		nv := notional(srcID, snkID, inAmt, outAmt)
		if nv != nil && *nv < minNotional {
			continue
		}
		kept++
		pathIDs, linear := reconstructPath(legs, srcID, snkID)
		var path string
		if linear {
			labels := make([]string, len(pathIDs))
			for i, a := range pathIDs {
				labels[i] = s.Registry.Label(a)
			}
			path = strings.Join(labels, " -> ")
		} else {
			path = fmt.Sprintf("%s ->..(%d mid).. %s",
				s.Registry.Label(srcID), len(n.Intermediates), s.Registry.Label(snkID))
		}
		stableMid := true
		for _, a := range n.Intermediates {
			if asset := s.Registry.Get(a); asset != nil && !asset.IsStable() {
				stableMid = false
			}
		}
		var rate *float64
		if inAmt.IsPositive() {
			v, _ := outAmt.Div(inAmt).Float64()
			if v != 0 {
				rate = &v
			}
		}
		inter := make([]string, len(n.Intermediates))
		for i, a := range n.Intermediates {
			inter[i] = s.Registry.Label(a)
			interCount[inter[i]]++
		}
		routes = append(routes, routeOut{
			Tx: k.Tx, Solver: k.Solver, Ts: ls[0].Ts, Hops: len(legs),
			Path: path, Linear: linear,
			SynthPair:          base(srcID) + "/" + base(snkID),
			SynthClass:         s.Registry.ClassifyPair(srcID, snkID),
			StableIntermediate: stableMid,
			Source:             s.Registry.Label(srcID), SourceChain: chain(srcID), SourceAssetID: srcID,
			Sink: s.Registry.Label(snkID), SinkChain: chain(snkID), SinkAssetID: snkID,
			Intermediates: inter,
			AmountIn:      inAmt.String(),
			AmountOut:     outAmt.String(),
			NativeRate:    rate,
			NotionalUSD:   nv,
		})
	}
	sort.Slice(routes, func(i, j int) bool {
		vi, vj := 0.0, 0.0
		if routes[i].NotionalUSD != nil {
			vi = *routes[i].NotionalUSD
		}
		if routes[j].NotionalUSD != nil {
			vj = *routes[j].NotionalUSD
		}
		return vi > vj
	})
	if routes == nil {
		routes = []routeOut{}
	}

	type interOut struct {
		Label string `json:"label"`
		N     int    `json:"n"`
	}
	inters := []interOut{}
	for l, n := range interCount {
		inters = append(inters, interOut{Label: l, N: n})
	}
	sort.Slice(inters, func(i, j int) bool { return inters[i].N > inters[j].N })
	if len(inters) > 15 {
		inters = inters[:15]
	}

	writeJSON(w, map[string]any{
		"from": win.From, "to": win.To,
		"multi_leg_groups": nMulti, "clean": clean, "complex": complexN, "kept": kept,
		"top_intermediates": inters,
		"routes":            routes,
	})
}

// renumberSecondWhereFrom shifts placeholders after the second WHERE (the CTE
// and outer query share the same filter; args are passed twice).
func renumberSecondWhereFrom(q string, f *legFilters) string {
	half := len(f.args)
	idx := strings.LastIndex(q, "WHERE")
	head, tail := q[:idx], q[idx:]
	tail = placeholderRe.ReplaceAllStringFunc(tail, func(m string) string {
		n, _ := strconv.Atoi(m[1:])
		if n >= 1 && n <= half {
			return fmt.Sprintf("$%d", n+half)
		}
		return m
	})
	return head + tail
}
