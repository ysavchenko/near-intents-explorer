package enricher

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reprice resets legs priced under the pre-spot convention (before commit
// fea0a55: USDC pinned at 1.0, Hyperliquid as the headline) so the enricher
// re-prices them, in short chunks newest first, resuming from indexer_state
// after a restart. It replaces migration 0005: a single UPDATE over a month
// of legs ran longer than Render's port-bind window and was rolled back with
// the killed instance. Migrations run before the port binds; bulk rewrites
// must not. A chunk is a few thousand rows, so it cannot stall startup, hold
// the follower's rows, or deadlock the live enricher.
//
// Only the venue rate/edge columns are cleared: pair, side, native_rate and
// notional do not change, so volume and grouping stay visible meanwhile.
// Rows that already carry a Binance rate were priced after the switch and
// are skipped, which makes the job idempotent.
type Reprice struct {
	Pool  *pgxpool.Pool
	Log   *slog.Logger
	Key   string        // indexer_state key holding progress
	Since time.Time     // oldest block_ts to reset (inclusive)
	Chunk time.Duration // block_ts span per transaction
	Pause time.Duration // breather between chunks
}

type repriceState struct {
	Cursor time.Time `json:"cursor"` // upper bound of the next chunk
	Since  time.Time `json:"since"`
	Rows   int64     `json:"rows"`
	Done   bool      `json:"done"`
}

// Run performs the backfill to completion (or until ctx is cancelled); it is
// a no-op once the persisted state says done.
func (r *Reprice) Run(ctx context.Context) error {
	st, err := r.load(ctx)
	if err != nil {
		return err
	}
	if st.Done {
		return nil
	}
	if st.Cursor.IsZero() {
		st = repriceState{Cursor: time.Now().UTC(), Since: r.Since.UTC()}
	}
	r.Log.Info("reprice backfill starting", "since", st.Since, "cursor", st.Cursor, "rows_so_far", st.Rows)
	for {
		hi := st.Cursor
		lo, last := nextChunk(hi, st.Since, r.Chunk)
		tag, err := r.Pool.Exec(ctx, `
			UPDATE legs SET hl_rate = NULL, binance_rate = NULL,
			                edge_bps_hl = NULL, edge_bps_binance = NULL,
			                price_status = 'pending'
			WHERE block_ts >= $1 AND block_ts < $2
			  AND price_status <> 'pending' AND binance_rate IS NULL`, lo, hi)
		if err != nil {
			return err
		}
		st.Rows += tag.RowsAffected()
		st.Cursor = lo
		st.Done = last
		if err := r.save(ctx, st); err != nil {
			return err
		}
		r.Log.Info("reprice chunk", "from", lo, "to", hi, "reset", tag.RowsAffected(), "total", st.Rows)
		if last {
			r.Log.Info("reprice backfill done", "rows", st.Rows)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.Pause):
		}
	}
}

// nextChunk returns the lower bound of the chunk ending at cursor, clamped to
// since, and whether that chunk is the last one.
func nextChunk(cursor, since time.Time, chunk time.Duration) (time.Time, bool) {
	lo := cursor.Add(-chunk)
	if !lo.After(since) {
		return since, true
	}
	return lo, false
}

func (r *Reprice) load(ctx context.Context) (repriceState, error) {
	var raw []byte
	err := r.Pool.QueryRow(ctx, `SELECT value FROM indexer_state WHERE key=$1`, r.Key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return repriceState{}, nil
	}
	if err != nil {
		return repriceState{}, err
	}
	var st repriceState
	return st, json.Unmarshal(raw, &st)
}

func (r *Reprice) save(ctx context.Context, st repriceState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = r.Pool.Exec(ctx, `
		INSERT INTO indexer_state (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, r.Key, raw)
	return err
}
