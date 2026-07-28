package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// Common query parsing
// ---------------------------------------------------------------------------

type window struct {
	From time.Time
	To   time.Time
}

// parseWindow reads ?from=&to= (RFC3339 or epoch seconds). Default: last 24h.
func parseWindow(r *http.Request) (window, error) {
	now := time.Now().UTC()
	w := window{From: now.Add(-24 * time.Hour), To: now}
	parse := func(s string) (time.Time, error) {
		if s == "" {
			return time.Time{}, nil
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(n, 0).UTC(), nil
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("bad time %q (want RFC3339 or epoch seconds)", s)
		}
		return t.UTC(), nil
	}
	if t, err := parse(r.URL.Query().Get("from")); err != nil {
		return w, err
	} else if !t.IsZero() {
		w.From = t
	}
	if t, err := parse(r.URL.Query().Get("to")); err != nil {
		return w, err
	} else if !t.IsZero() {
		w.To = t
	}
	if !w.To.After(w.From) {
		return w, fmt.Errorf("to must be after from")
	}
	return w, nil
}

// legFilters builds the shared WHERE fragment for aggregate queries.
//
// par mirrors enrich.py --par-legs: "diff" (default) keeps par legs (same_asset
// / stable_stable) only when the in/out amounts differ (a real spread), "all"
// keeps every one, "off" keeps interesting legs only.
type legFilters struct {
	conds []string
	args  []any
}

// addf appends a condition, rewriting each ? placeholder to the next $n.
func (f *legFilters) addf(cond string, args ...any) {
	for _, a := range args {
		f.args = append(f.args, a)
		cond = strings.Replace(cond, "?", fmt.Sprintf("$%d", len(f.args)), 1)
	}
	f.conds = append(f.conds, cond)
}

func (f *legFilters) where() string {
	if len(f.conds) == 0 {
		return "TRUE"
	}
	return strings.Join(f.conds, " AND ")
}

// parseMinNotional reads ?min_notional= (USD). 0 = no filter.
func parseMinNotional(r *http.Request) (float64, error) {
	mn := r.URL.Query().Get("min_notional")
	if mn == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(mn, 64)
	if err != nil {
		return 0, fmt.Errorf("bad min_notional")
	}
	return v, nil
}

func baseFilters(r *http.Request, w window, pricedOnly bool) (*legFilters, error) {
	f := &legFilters{}
	f.addf("block_ts >= ?", w.From)
	f.addf("block_ts < ?", w.To)
	if pricedOnly {
		f.addf("pair IS NOT NULL")
	}
	switch par := r.URL.Query().Get("par"); par {
	case "", "diff":
		f.addf("(leg_class = 'interesting' OR amount_in IS DISTINCT FROM amount_out)")
	case "all":
	case "off":
		f.addf("leg_class = 'interesting'")
	default:
		return nil, fmt.Errorf("par must be diff|all|off")
	}
	v, err := parseMinNotional(r)
	if err != nil {
		return nil, err
	}
	if v > 0 {
		f.addf("(notional_usd IS NULL OR notional_usd >= ?)", v)
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// Aggregates (agg_by_pair / agg_by_solver / agg_by_solver_pair equivalents)
// ---------------------------------------------------------------------------

type aggRow struct {
	Key                map[string]string `json:"-"`
	NLegs              int64             `json:"n_legs"`
	VolumeUSD          float64           `json:"volume_usd"`
	BinanceN           int64             `json:"binance_n"`
	BinanceVWEdgeBps   *float64          `json:"binance_vw_edge_bps"`
	BinanceMeanEdgeBps *float64          `json:"binance_mean_edge_bps"`
	HlN                int64             `json:"hl_n"`
	HlVWEdgeBps        *float64          `json:"hl_vw_edge_bps"`
	HlMeanEdgeBps      *float64          `json:"hl_mean_edge_bps"`
}

const aggCols = `
	count(*) AS n_legs,
	COALESCE(sum(notional_usd), 0) AS volume_usd,
	count(edge_bps_binance) AS binance_n,
	sum(edge_bps_binance * notional_usd) FILTER (WHERE edge_bps_binance IS NOT NULL AND notional_usd IS NOT NULL)
	  / NULLIF(sum(notional_usd) FILTER (WHERE edge_bps_binance IS NOT NULL AND notional_usd IS NOT NULL), 0) AS binance_vw,
	avg(edge_bps_binance) AS binance_mean,
	count(edge_bps_hl) AS hl_n,
	sum(edge_bps_hl * notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL)
	  / NULLIF(sum(notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL), 0) AS hl_vw,
	avg(edge_bps_hl) AS hl_mean`

// runAgg executes an aggregate query grouped by keyCols (column names).
func (s *Server) runAgg(r *http.Request, f *legFilters, keyCols ...string) ([]map[string]any, error) {
	keySel := ""
	for _, k := range keyCols {
		keySel += k + ", "
	}
	groupBy := ""
	for i := range keyCols {
		if i > 0 {
			groupBy += ", "
		}
		groupBy += strconv.Itoa(i + 1)
	}
	q := "SELECT " + keySel + aggCols + " FROM legs WHERE " + f.where()
	if groupBy != "" {
		q += " GROUP BY " + groupBy
	}
	q += " ORDER BY volume_usd DESC"

	rows, err := s.Pool.Query(r.Context(), q, f.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		keys := make([]*string, len(keyCols))
		var a aggRow
		dest := make([]any, 0, len(keyCols)+8)
		for i := range keys {
			dest = append(dest, &keys[i])
		}
		dest = append(dest, &a.NLegs, &a.VolumeUSD,
			&a.BinanceN, &a.BinanceVWEdgeBps, &a.BinanceMeanEdgeBps,
			&a.HlN, &a.HlVWEdgeBps, &a.HlMeanEdgeBps)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := map[string]any{
			"n_legs": a.NLegs, "volume_usd": a.VolumeUSD,
			"binance_n": a.BinanceN, "binance_vw_edge_bps": a.BinanceVWEdgeBps,
			"binance_mean_edge_bps": a.BinanceMeanEdgeBps,
			"hl_n":                  a.HlN, "hl_vw_edge_bps": a.HlVWEdgeBps, "hl_mean_edge_bps": a.HlMeanEdgeBps,
		}
		for i, k := range keyCols {
			v := ""
			if keys[i] != nil {
				v = *keys[i]
			}
			row[k] = v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Server) handlePairs(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	f, err := baseFilters(r, win, true)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	rowsOut, err := s.runAgg(r, f, "pair")
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"from": win.From, "to": win.To, "rows": emptyList(rowsOut)})
}

// handlePairDirections breaks one pair down by concrete asset direction
// (e.g. USDT/USDT -> USDT@eth->USDT@tron vs USDT@tron->USDT@eth), optionally
// scoped to a solver.
func (s *Server) handlePairDirections(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	pair := r.URL.Query().Get("pair")
	if pair == "" {
		httpErr(w, 400, fmt.Errorf("pair required"))
		return
	}
	f, err := baseFilters(r, win, true)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	f.addf("pair = ?", pair)
	if v := r.URL.Query().Get("solver"); v != "" {
		f.addf("solver = ?", v)
	}
	rowsOut, err := s.runAgg(r, f, "from_asset", "to_asset")
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	for _, row := range rowsOut {
		from, _ := row["from_asset"].(string)
		to, _ := row["to_asset"].(string)
		row["from_label"] = s.Registry.Label(from)
		row["to_label"] = s.Registry.Label(to)
	}
	writeJSON(w, map[string]any{"from": win.From, "to": win.To, "pair": pair, "rows": emptyList(rowsOut)})
}

func (s *Server) handleSolvers(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	f, err := baseFilters(r, win, true)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	rowsOut, err := s.runAgg(r, f, "solver")
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"from": win.From, "to": win.To, "rows": emptyList(rowsOut)})
}

func (s *Server) handleSolverDetail(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	solver := chi.URLParam(r, "id")
	f, err := baseFilters(r, win, true)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	f.addf("solver = ?", solver)
	pairs, err := s.runAgg(r, f, "pair")
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	var isSeed bool
	var nSettlements int64
	var firstSeen *time.Time
	err = s.Pool.QueryRow(r.Context(),
		`SELECT is_seed, n_settlements, first_seen FROM solvers WHERE account_id=$1`, solver).
		Scan(&isSeed, &nSettlements, &firstSeen)
	if err != nil {
		// Not in the solvers table: still return the aggregation (may be empty).
		isSeed, nSettlements = false, 0
	}
	writeJSON(w, map[string]any{
		"from": win.From, "to": win.To, "solver": solver,
		"is_seed": isSeed, "n_settlements": nSettlements, "first_seen": firstSeen,
		"pairs": emptyList(pairs),
	})
}

// ---------------------------------------------------------------------------
// Legs listing (pair / solver detail tables)
// ---------------------------------------------------------------------------

func (s *Server) handleLegs(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	f := &legFilters{}
	f.addf("l.block_ts >= ?", win.From)
	f.addf("l.block_ts < ?", win.To)
	if v := r.URL.Query().Get("pair"); v != "" {
		f.addf("l.pair = ?", v)
	}
	if v := r.URL.Query().Get("solver"); v != "" {
		f.addf("l.solver = ?", v)
	}
	if v := r.URL.Query().Get("from_asset"); v != "" {
		f.addf("l.from_asset = ?", v)
	}
	if v := r.URL.Query().Get("to_asset"); v != "" {
		f.addf("l.to_asset = ?", v)
	}
	if v := r.URL.Query().Get("class"); v != "" {
		f.addf("l.leg_class = ?", v)
	}
	if v := r.URL.Query().Get("status"); v != "" {
		f.addf("l.price_status = ?", v)
	}
	if mn, err := parseMinNotional(r); err != nil {
		httpErr(w, 400, err)
		return
	} else if mn > 0 {
		f.addf("(l.notional_usd IS NULL OR l.notional_usd >= ?)", mn)
	}
	limit := clampInt(r.URL.Query().Get("limit"), 200, 1, 1000)
	offset := clampInt(r.URL.Query().Get("offset"), 0, 0, 1_000_000)

	q := fmt.Sprintf(`
		SELECT l.tx_hash, l.seq, l.block_ts, l.block_height, l.solver, l.leg_class, l.multi_asset,
		       l.from_asset, l.to_asset, l.amount_in::text, l.amount_out::text,
		       l.pair, l.side, l.base_symbol, l.quote_symbol,
		       l.native_rate, l.hl_rate, l.binance_rate, l.edge_bps_hl, l.edge_bps_binance,
		       l.notional_usd, l.price_status
		FROM legs l
		WHERE %s
		ORDER BY l.block_ts DESC, l.tx_hash, l.seq
		LIMIT %d OFFSET %d`, f.where(), limit, offset)
	rows, err := s.Pool.Query(r.Context(), q, f.args...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	defer rows.Close()

	type legOut struct {
		Tx          string    `json:"tx"`
		Seq         int       `json:"seq"`
		Ts          time.Time `json:"ts"`
		Block       int64     `json:"block"`
		Solver      string    `json:"solver"`
		LegClass    string    `json:"leg_class"`
		MultiAsset  bool      `json:"multi_asset"`
		FromAsset   string    `json:"from_asset"`
		FromName    string    `json:"from_name"`
		ToAsset     string    `json:"to_asset"`
		ToName      string    `json:"to_name"`
		AmountIn    *string   `json:"amount_in"`
		AmountOut   *string   `json:"amount_out"`
		Pair        *string   `json:"pair"`
		Side        *string   `json:"side"`
		BaseSymbol  *string   `json:"base_symbol"`
		QuoteSymbol *string   `json:"quote_symbol"`
		NativeRate  *float64  `json:"native_rate"`
		HlRate      *float64  `json:"hl_rate"`
		BinanceRate *float64  `json:"binance_rate"`
		EdgeHl      *float64  `json:"edge_bps_hl"`
		EdgeBinance *float64  `json:"edge_bps_binance"`
		NotionalUSD *float64  `json:"notional_usd"`
		PriceStatus string    `json:"price_status"`
	}
	out := []legOut{}
	for rows.Next() {
		var l legOut
		if err := rows.Scan(&l.Tx, &l.Seq, &l.Ts, &l.Block, &l.Solver, &l.LegClass, &l.MultiAsset,
			&l.FromAsset, &l.ToAsset, &l.AmountIn, &l.AmountOut,
			&l.Pair, &l.Side, &l.BaseSymbol, &l.QuoteSymbol,
			&l.NativeRate, &l.HlRate, &l.BinanceRate, &l.EdgeHl, &l.EdgeBinance,
			&l.NotionalUSD, &l.PriceStatus); err != nil {
			httpErr(w, 500, err)
			return
		}
		l.FromName = s.Registry.Label(l.FromAsset)
		l.ToName = s.Registry.Label(l.ToAsset)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"from": win.From, "to": win.To, "rows": out})
}

// ---------------------------------------------------------------------------
// Summary + daily series + status
// ---------------------------------------------------------------------------

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	ctx := r.Context()

	// Legs-based stats honor min_notional; settlement counts are tx-level and
	// have no USD notional, so they stay window-only.
	legCond := "block_ts >= $1 AND block_ts < $2"
	legArgs := []any{win.From, win.To}
	if mn, err := parseMinNotional(r); err != nil {
		httpErr(w, 400, err)
		return
	} else if mn > 0 {
		legCond += " AND (notional_usd IS NULL OR notional_usd >= $3)"
		legArgs = append(legArgs, mn)
	}

	var volume float64
	var nLegs, pendingLegs, activeSolvers int64
	err = s.Pool.QueryRow(ctx, `
		SELECT COALESCE(sum(notional_usd),0), count(*),
		       count(*) FILTER (WHERE price_status='pending'),
		       count(DISTINCT solver)
		FROM legs WHERE `+legCond, legArgs...).
		Scan(&volume, &nLegs, &pendingLegs, &activeSolvers)
	if err != nil {
		httpErr(w, 500, err)
		return
	}

	var settlements, failed int64
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE NOT succeeded)
		FROM settlements WHERE block_ts >= $1 AND block_ts < $2`, win.From, win.To).
		Scan(&settlements, &failed); err != nil {
		httpErr(w, 500, err)
		return
	}

	classRows, err := s.Pool.Query(ctx, `
		SELECT leg_class, COALESCE(sum(notional_usd),0), count(*)
		FROM legs WHERE `+legCond+` GROUP BY 1`, legArgs...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	volumeByClass := map[string]float64{}
	legsByClass := map[string]int64{}
	for classRows.Next() {
		var cls string
		var v float64
		var n int64
		if err := classRows.Scan(&cls, &v, &n); err != nil {
			classRows.Close()
			httpErr(w, 500, err)
			return
		}
		volumeByClass[cls] = v
		legsByClass[cls] = n
	}
	classRows.Close()

	hourly, err := s.Pool.Query(ctx, `
		SELECT date_trunc('hour', block_ts) AS h, COALESCE(sum(notional_usd),0), count(*)
		FROM legs WHERE `+legCond+` GROUP BY 1 ORDER BY 1`, legArgs...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	type bucket struct {
		Ts        time.Time `json:"ts"`
		VolumeUSD float64   `json:"volume_usd"`
		NLegs     int64     `json:"n_legs"`
	}
	buckets := []bucket{}
	for hourly.Next() {
		var b bucket
		if err := hourly.Scan(&b.Ts, &b.VolumeUSD, &b.NLegs); err != nil {
			hourly.Close()
			httpErr(w, 500, err)
			return
		}
		buckets = append(buckets, b)
	}
	hourly.Close()

	writeJSON(w, map[string]any{
		"from": win.From, "to": win.To,
		"volume_usd": volume, "legs": nLegs, "pending_legs": pendingLegs,
		"settlements": settlements, "settlements_failed": failed,
		"active_solvers":  activeSolvers,
		"volume_by_class": volumeByClass, "legs_by_class": legsByClass,
		"hourly": buckets,
	})
}

func (s *Server) handleDaily(w http.ResponseWriter, r *http.Request) {
	win, err := parseWindow(r)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "day"
	}
	if bucket != "day" && bucket != "hour" {
		httpErr(w, 400, fmt.Errorf("bucket must be day|hour"))
		return
	}
	group := r.URL.Query().Get("group")

	f, err := baseFilters(r, win, true)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	// Optional scoping (column names are a fixed whitelist, values are params).
	for _, col := range []string{"pair", "solver", "from_asset", "to_asset"} {
		if v := r.URL.Query().Get(col); v != "" {
			f.addf(col+" = ?", v)
		}
	}

	var q string
	switch group {
	case "", "none":
		q = fmt.Sprintf(`SELECT date_trunc('%s', block_ts) AS b, '' AS key, count(*),
			COALESCE(sum(notional_usd),0),
			sum(edge_bps_hl*notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL)
			  / NULLIF(sum(notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL),0)
			FROM legs WHERE %s GROUP BY 1 ORDER BY 1`, bucket, f.where())
	case "pair", "solver":
		q = fmt.Sprintf(`SELECT date_trunc('%s', block_ts) AS b, %s AS key, count(*),
			COALESCE(sum(notional_usd),0),
			sum(edge_bps_hl*notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL)
			  / NULLIF(sum(notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL),0)
			FROM legs WHERE %s GROUP BY 1, 2 ORDER BY 1, 4 DESC`, bucket, group, f.where())
	case "token":
		// A leg counts toward both of its sides' tokens.
		q = fmt.Sprintf(`SELECT b, key, count(*), COALESCE(sum(notional_usd),0),
			sum(edge_bps_hl*notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL)
			  / NULLIF(sum(notional_usd) FILTER (WHERE edge_bps_hl IS NOT NULL AND notional_usd IS NOT NULL),0)
			FROM (
			  SELECT date_trunc('%[1]s', block_ts) AS b, base_symbol AS key, notional_usd, edge_bps_hl
			  FROM legs WHERE %[2]s
			  UNION ALL
			  SELECT date_trunc('%[1]s', block_ts) AS b, quote_symbol AS key, notional_usd, edge_bps_hl
			  FROM legs WHERE %[2]s
			) t WHERE key IS NOT NULL GROUP BY 1, 2 ORDER BY 1, 4 DESC`, bucket, f.where())
		f.args = append(f.args, f.args...) // the WHERE runs twice with the same params
		// Renumber the second copy's placeholders.
		q = renumberSecondWhere(q, f)
	default:
		httpErr(w, 400, fmt.Errorf("group must be none|pair|solver|token"))
		return
	}

	rows, err := s.Pool.Query(r.Context(), q, f.args...)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	defer rows.Close()
	type point struct {
		Ts        time.Time `json:"ts"`
		Key       string    `json:"key"`
		NLegs     int64     `json:"n_legs"`
		VolumeUSD float64   `json:"volume_usd"`
		HlVWEdge  *float64  `json:"hl_vw_edge_bps"`
	}
	out := []point{}
	for rows.Next() {
		var p point
		if err := rows.Scan(&p.Ts, &p.Key, &p.NLegs, &p.VolumeUSD, &p.HlVWEdge); err != nil {
			httpErr(w, 500, err)
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"from": win.From, "to": win.To, "bucket": bucket, "group": group, "rows": out})
}

// renumberSecondWhere shifts the placeholders in the branch after UNION ALL:
// the token query embeds the same WHERE twice, but Postgres numbered params
// cannot repeat, so the args are duplicated and the second occurrence's $n
// becomes $(n+half). Single regex pass, so $1 inside $12 stays intact.
func renumberSecondWhere(q string, f *legFilters) string {
	half := len(f.args) / 2
	idx := strings.LastIndex(q, "UNION ALL")
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

var placeholderRe = regexp.MustCompile(`\$\d+`)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	headHeight, headTsNs := s.chainHead(r)

	var backlog int64
	if err := s.Pool.QueryRow(r.Context(),
		`SELECT count(*) FROM legs WHERE price_status='pending'`).Scan(&backlog); err != nil {
		httpErr(w, 500, err)
		return
	}

	indexed := s.Metrics.LastBlockHeight.Load()
	indexedTsNs := s.Metrics.LastBlockTsNs.Load()
	m := s.Metrics
	writeJSON(w, map[string]any{
		"chain_head":      headHeight,
		"chain_head_ts":   nsToTime(headTsNs),
		"indexed_head":    indexed,
		"indexed_head_ts": nsToTime(indexedTsNs),
		"lag_blocks":      headHeight - indexed,
		"pricing_backlog": backlog,
		"started_at":      m.StartedAt,
		"uptime_s":        int64(time.Since(m.StartedAt).Seconds()),
		"assets":          s.Registry.Len(),
		"venues":          s.Cfg.Venues,
		"solvers_learned": len(s.Solvers.Learned()),
		"counters": map[string]int64{
			"blocks_processed":    m.BlocksProcessed.Load(),
			"skipped_heights":     m.SkippedHeights.Load(),
			"pending_settlements": m.PendingSettlements.Load(),
			"settlements":         m.SettlementsTotal.Load(),
			"settlements_failed":  m.SettlementsFailed.Load(),
			"legs":                m.LegsTotal.Load(),
			"parse_errors":        m.ParseErrors.Load(),
			"follower_errors":     m.FollowerErrors.Load(),
			"enriched_legs":       m.EnrichedLegs.Load(),
			"no_reference_legs":   m.NoReferenceLegs.Load(),
			"enricher_errors":     m.EnricherErrors.Load(),
			"venue_fetches":       m.VenueFetches.Load(),
		},
	})
}

func nsToTime(ns int64) *time.Time {
	if ns == 0 {
		return nil
	}
	t := time.Unix(0, ns).UTC()
	return &t
}

func clampInt(s string, def, lo, hi int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func emptyList(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}
