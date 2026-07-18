package enricher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"intents-explorer/internal/assets"
	"intents-explorer/internal/metrics"
)

const (
	batchSize = 2000
	// Venue 1m candles trail real time; don't try to price a leg until its
	// minute has closed and the venues have had time to publish it.
	minLegAge = 90 * time.Second
	// A needed minute with no candle after this long is a real gap (thin
	// symbol) — persist a null mark so we stop refetching it.
	nullMarkAfter = 3 * time.Minute
	// Remember venue-unlisted symbols in memory this long before probing again.
	noRefTTL = 6 * time.Hour
)

type Enricher struct {
	Pool     *pgxpool.Pool
	Registry *assets.Registry
	Metrics  *metrics.Metrics
	Venues   []string // subset of {"hl", "binance"}; first is primary
	Every    time.Duration
	Log      *slog.Logger

	// Fetchers keyed by venue name; tests override these.
	Fetchers map[string]MidsFetcher

	noRef map[string]time.Time // "venue|symbol" -> when flagged unlisted
	now   func() time.Time
}

func (e *Enricher) init() {
	if e.Log == nil {
		e.Log = slog.Default()
	}
	if e.Fetchers == nil {
		e.Fetchers = map[string]MidsFetcher{"hl": HyperliquidMids, "binance": BinanceMids}
	}
	if e.noRef == nil {
		e.noRef = map[string]time.Time{}
	}
	if e.now == nil {
		e.now = time.Now
	}
}

// Run prices pending legs on a fixed cadence until the context is cancelled.
// A full batch means there is backlog — loop immediately instead of waiting.
func (e *Enricher) Run(ctx context.Context) error {
	e.init()
	ticker := time.NewTicker(e.Every)
	defer ticker.Stop()
	for {
		n, err := e.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			e.Metrics.EnricherErrors.Add(1)
			e.Log.Error("enricher tick failed", "err", err)
		}
		if n >= batchSize {
			continue // draining backlog
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type legRow struct {
	ID      int64
	BlockTs time.Time
	in      legInput
}

// markKey addresses one venue-symbol's minute mids.
type markKey struct {
	venue  string
	symbol string // venue spelling (ZECUSDT / ZEC)
}

// Tick prices one batch of pending legs; returns how many rows it examined.
func (e *Enricher) Tick(ctx context.Context) (int, error) {
	e.init()
	cutoff := e.now().UTC().Add(-minLegAge)
	rows, err := e.Pool.Query(ctx, `
		SELECT id, block_ts, leg_class, from_asset, to_asset, amount_in::text, amount_out::text
		FROM legs
		WHERE price_status = 'pending' AND block_ts < $1
		ORDER BY block_ts
		LIMIT $2`, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	legs, err := scanLegs(rows)
	if err != nil {
		return 0, err
	}
	if len(legs) == 0 {
		return 0, nil
	}

	type work struct {
		row legRow
		o   *Oriented
		m   int64 // minute for venue lookups
	}
	var terminal []int64 // leg ids that can never be priced
	var pending []work
	needed := map[markKey]map[int64]bool{} // (venue,symbol) -> minutes
	for _, l := range legs {
		o, term := orientLeg(e.Registry, l.in)
		if term {
			terminal = append(terminal, l.ID)
			continue
		}
		m := MinuteMs(l.BlockTs)
		w := work{row: l, o: o, m: m}
		for _, venue := range e.Venues {
			for _, sym := range o.NeededSymbols() {
				k := markKey{venue: venue, symbol: e.venueSpelling(venue, sym)}
				if k.symbol == "" {
					continue
				}
				if needed[k] == nil {
					needed[k] = map[int64]bool{}
				}
				needed[k][m] = true
			}
		}
		pending = append(pending, w)
	}

	marks, err := e.resolveMarks(ctx, needed)
	if err != nil {
		return len(legs), err
	}

	// usd returns (price, known): stables are pinned at 1.0; known=false means
	// the mark is not yet available (leg stays pending).
	usd := func(venue, sym string, m int64) (*float64, bool) {
		if assets.Stables[sym] {
			one := 1.0
			return &one, true
		}
		vs := e.venueSpelling(venue, sym)
		if vs == "" {
			return nil, true // unspellable symbol: terminally unpriceable on this venue
		}
		mid, known := marks[markKey{venue, vs}][m]
		if !known {
			return nil, false
		}
		return mid, true // mid may be nil (venue has no data) — that is known
	}

	batch := &pgx.Batch{}
	for _, id := range terminal {
		batch.Queue(`UPDATE legs SET price_status='no_reference' WHERE id=$1 AND price_status='pending'`, id)
		e.Metrics.NoReferenceLegs.Add(1)
	}

	enriched := 0
	for _, w := range pending {
		// Readiness: every needed venue mark must be known (value or null).
		ready := true
		for _, venue := range e.Venues {
			for _, sym := range w.o.NeededSymbols() {
				if _, known := usd(venue, sym, w.m); !known {
					ready = false
				}
			}
		}
		if !ready {
			continue // next tick
		}

		var native float64
		if !w.o.BaseAmt.IsZero() {
			native, _ = w.o.QuoteAmt.Div(w.o.BaseAmt).Float64()
		}
		rates := map[string]*float64{}
		for _, venue := range e.Venues {
			if w.o.Par {
				one := 1.0
				rates[venue] = &one
				continue
			}
			ub, _ := usd(venue, w.o.Base, w.m)
			uq, _ := usd(venue, w.o.Quote, w.m)
			if ub != nil && uq != nil && *ub != 0 && *uq != 0 {
				r := *ub / *uq
				rates[venue] = &r
			}
		}
		edgeHl := Edge(native, rates["hl"], w.o.Side)
		edgeBin := Edge(native, rates["binance"], w.o.Side)

		var notional *float64
		if assets.Stables[w.o.Quote] {
			v, _ := w.o.QuoteAmt.Float64()
			notional = &v
		} else {
			var px *float64
			for _, venue := range e.Venues {
				if p, _ := usd(venue, w.o.Base, w.m); p != nil {
					px = p
					break
				}
			}
			if px == nil {
				if a := e.Registry.Get(w.o.BaseAssetID); a != nil && a.PriceUSD != nil {
					px = a.PriceUSD
				}
			}
			if px != nil {
				baseF, _ := w.o.BaseAmt.Float64()
				v := baseF * *px
				notional = &v
			}
		}

		status := "no_reference"
		for _, r := range rates {
			if r != nil {
				status = "ok"
				break
			}
		}
		var nativePtr *float64
		if native != 0 {
			nativePtr = &native
		}
		batch.Queue(`
			UPDATE legs SET pair=$2, side=$3, base_symbol=$4, quote_symbol=$5, native_rate=$6,
			                hl_rate=$7, binance_rate=$8, edge_bps_hl=$9, edge_bps_binance=$10,
			                notional_usd=$11, price_status=$12
			WHERE id=$1`,
			w.row.ID, w.o.Pair, w.o.Side, w.o.Base, w.o.Quote, nativePtr,
			rates["hl"], rates["binance"], edgeHl, edgeBin, notional, status)
		if status == "ok" {
			e.Metrics.EnrichedLegs.Add(1)
		} else {
			e.Metrics.NoReferenceLegs.Add(1)
		}
		enriched++
	}

	if batch.Len() > 0 {
		if err := e.Pool.SendBatch(ctx, batch).Close(); err != nil {
			return len(legs), fmt.Errorf("apply enrichment: %w", err)
		}
	}
	e.Log.Info("enricher tick", "examined", len(legs), "priced", enriched,
		"terminal", len(terminal), "still_pending", len(legs)-len(terminal)-enriched)
	return len(legs), nil
}

// resolveMarks loads needed minute mids from price_marks, fetching gaps from
// the venues and persisting what it learns. The returned map's inner value is
// nil for "venue has no data for this minute" (a known, terminal answer);
// a missing inner key means "not yet known" (leg stays pending).
func (e *Enricher) resolveMarks(ctx context.Context, needed map[markKey]map[int64]bool) (map[markKey]map[int64]*float64, error) {
	out := map[markKey]map[int64]*float64{}
	now := e.now().UTC()
	for k, minutes := range needed {
		if len(minutes) == 0 {
			continue
		}
		out[k] = map[int64]*float64{}
		var minM, maxM int64
		for m := range minutes {
			if minM == 0 || m < minM {
				minM = m
			}
			if m > maxM {
				maxM = m
			}
		}

		rows, err := e.Pool.Query(ctx, `
			SELECT minute_ms, mid FROM price_marks
			WHERE venue=$1 AND symbol=$2 AND minute_ms BETWEEN $3 AND $4`,
			k.venue, k.symbol, minM, maxM)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var m int64
			var mid *float64
			if err := rows.Scan(&m, &mid); err != nil {
				rows.Close()
				return nil, err
			}
			out[k][m] = mid
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}

		var missing []int64
		for m := range minutes {
			if _, ok := out[k][m]; !ok {
				missing = append(missing, m)
			}
		}
		if len(missing) == 0 {
			continue
		}

		// Known-unlisted symbol: all missing minutes are terminally null.
		if flagged, ok := e.noRef[k.venue+"|"+k.symbol]; ok && now.Sub(flagged) < noRefTTL {
			for _, m := range missing {
				out[k][m] = nil
			}
			continue
		}

		fetchMin, fetchMax := missing[0], missing[0]
		for _, m := range missing {
			if m < fetchMin {
				fetchMin = m
			}
			if m > fetchMax {
				fetchMax = m
			}
		}
		fetcher := e.Fetchers[k.venue]
		if fetcher == nil {
			return nil, fmt.Errorf("no fetcher for venue %q", k.venue)
		}
		e.Metrics.VenueFetches.Add(1)
		mids, err := fetcher(ctx, k.symbol, fetchMin, fetchMax+minMs-1)
		if errors.Is(err, ErrInvalidSymbol) {
			e.noRef[k.venue+"|"+k.symbol] = now
			// Persist null marks so restarts don't re-probe old minutes.
			for _, m := range missing {
				out[k][m] = nil
				if err := e.upsertMark(ctx, k, m, nil); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err != nil {
			e.Metrics.EnricherErrors.Add(1)
			e.Log.Warn("venue fetch failed", "venue", k.venue, "symbol", k.symbol, "err", err)
			continue // marks stay unknown; affected legs stay pending
		}
		for _, m := range missing {
			if mid, ok := mids[m]; ok {
				v := mid
				out[k][m] = &v
				if err := e.upsertMark(ctx, k, m, &v); err != nil {
					return nil, err
				}
			} else if now.Sub(time.UnixMilli(m+minMs)) > nullMarkAfter {
				// The minute closed long ago and the venue has nothing for it:
				// a real gap. Persist the null so we stop refetching.
				out[k][m] = nil
				if err := e.upsertMark(ctx, k, m, nil); err != nil {
					return nil, err
				}
			}
			// else: candle not published yet — leave unknown, retry next tick.
		}
	}
	return out, nil
}

func (e *Enricher) upsertMark(ctx context.Context, k markKey, minuteMs int64, mid *float64) error {
	_, err := e.Pool.Exec(ctx, `
		INSERT INTO price_marks (venue, symbol, minute_ms, mid) VALUES ($1,$2,$3,$4)
		ON CONFLICT (venue, symbol, minute_ms) DO UPDATE SET mid = EXCLUDED.mid
		WHERE price_marks.mid IS NULL`,
		k.venue, k.symbol, minuteMs, mid)
	return err
}

// venueSpelling maps a base symbol to the venue's ticker ("" = unspellable).
func (e *Enricher) venueSpelling(venue, sym string) string {
	vs := VenueSymbol(sym)
	if vs == "" {
		return ""
	}
	if venue == "binance" {
		return vs + "USDT"
	}
	return vs
}

func scanLegs(rows pgx.Rows) ([]legRow, error) {
	defer rows.Close()
	var out []legRow
	for rows.Next() {
		var l legRow
		var amountIn, amountOut *string
		if err := rows.Scan(&l.ID, &l.BlockTs, &l.in.LegClass, &l.in.FromAsset, &l.in.ToAsset,
			&amountIn, &amountOut); err != nil {
			return nil, err
		}
		if amountIn != nil {
			if d, err := decimal.NewFromString(*amountIn); err == nil {
				l.in.AmountIn = &d
			}
		}
		if amountOut != nil {
			if d, err := decimal.NewFromString(*amountOut); err == nil {
				l.in.AmountOut = &d
			}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
