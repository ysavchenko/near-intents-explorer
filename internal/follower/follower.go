// Package follower tails finalized NEAR blocks from neardata.xyz, parses
// `execute_intents` settlements into solver legs, and writes them to Postgres.
//
// Success status: the execute_intents function-call receipt executes 1–2
// blocks after the tx is included. We keep a pending map tx receipt_id →
// parsed settlement and resolve it when the receipt's execution outcome
// appears in a later block. The map is persisted to indexer_state in the same
// transaction as the cursor, so restarts cannot lose entries. Only successful
// settlements produce legs; failed ones are recorded with succeeded=false.
package follower

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"intents-explorer/internal/assets"
	"intents-explorer/internal/intents"
	"intents-explorer/internal/metrics"
	"intents-explorer/internal/neardata"
)

const (
	cursorKey  = "last_final_height"
	pendingKey = "pending_settlements"

	// A function-call receipt normally executes within 1–2 blocks; if it has
	// not appeared after this many, something is wrong — record the settlement
	// as failed and move on (safety valve, should never fire).
	pendingExpiryBlocks = 600

	// Soft request pacing: the neardata free tier allows 180 req/min. NEAR
	// produces ~100 blocks/min, so tail-following fits; this cap only bites
	// during catch-up after downtime.
	minRequestInterval = 340 * time.Millisecond
)

type Follower struct {
	Pool     *pgxpool.Pool
	Client   *neardata.Client
	Registry *assets.Registry
	Solvers  *intents.SolverSet
	Metrics  *metrics.Metrics
	Overlap  int64
	Log      *slog.Logger

	cursor  int64
	pending map[string]*pendingSettlement // receipt_id -> settlement awaiting outcome
	lastReq time.Time
}

// pendingSettlement is a parsed settlement waiting for its receipt outcome.
// It is JSON-serialized into indexer_state for restart safety.
type pendingSettlement struct {
	TxHash    string          `json:"tx_hash"`
	ReceiptID string          `json:"receipt_id"`
	Height    int64           `json:"height"`
	TsNs      int64           `json:"ts_ns"`
	Relayer   string          `json:"relayer"`
	Msgs      []signedMsgJSON `json:"msgs"`
	SeenAt    int64           `json:"seen_at_height"`
}

type signedMsgJSON struct {
	Signer     string        `json:"signer"`
	Kinds      []string      `json:"kinds"`
	TokenDiffs [][][2]string `json:"token_diffs"` // [ [ [asset, amount], ... ], ... ]
}

func msgsToJSON(msgs []intents.SignedMsg) []signedMsgJSON {
	out := make([]signedMsgJSON, len(msgs))
	for i, m := range msgs {
		j := signedMsgJSON{Signer: m.Signer, Kinds: m.Kinds}
		for _, d := range m.TokenDiffs {
			var entries [][2]string
			for _, e := range d {
				entries = append(entries, [2]string{e.Asset, e.Amount.String()})
			}
			j.TokenDiffs = append(j.TokenDiffs, entries)
		}
		out[i] = j
	}
	return out
}

func msgsFromJSON(js []signedMsgJSON) ([]intents.SignedMsg, error) {
	out := make([]intents.SignedMsg, len(js))
	for i, j := range js {
		m := intents.SignedMsg{Signer: j.Signer, Kinds: j.Kinds}
		for _, d := range j.TokenDiffs {
			var entries []intents.DiffEntry
			for _, e := range d {
				n, ok := new(big.Int).SetString(e[1], 10)
				if !ok {
					return nil, fmt.Errorf("bad amount %q", e[1])
				}
				entries = append(entries, intents.DiffEntry{Asset: e[0], Amount: n})
			}
			m.TokenDiffs = append(m.TokenDiffs, entries)
		}
		out[i] = m
	}
	return out, nil
}

// Run follows the chain until the context is cancelled.
func (f *Follower) Run(ctx context.Context) error {
	if f.Log == nil {
		f.Log = slog.Default()
	}
	if err := f.loadState(ctx); err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	f.Log.Info("follower starting", "cursor", f.cursor, "pending", len(f.pending))

	for ctx.Err() == nil {
		f.pace()
		height := f.cursor + 1
		block, err := f.Client.Block(ctx, height)
		if errors.Is(err, neardata.ErrSkipped) {
			f.Metrics.SkippedHeights.Add(1)
			f.cursor = height
			if err := f.persistCursorOnly(ctx); err != nil {
				f.dbErr(err)
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			f.Metrics.FollowerErrors.Add(1)
			f.Log.Error("fetch block failed", "height", height, "err", err)
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		if err := f.processBlock(ctx, block); err != nil {
			// Do not advance the cursor: inserts are idempotent, so retrying
			// the same block after a DB hiccup is safe.
			f.dbErr(err)
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		f.cursor = height
		f.Metrics.BlocksProcessed.Add(1)
		f.Metrics.LastBlockHeight.Store(block.Height())
		f.Metrics.LastBlockTsNs.Store(block.TimestampNs())
		f.Metrics.PendingSettlements.Store(int64(len(f.pending)))
	}
	return ctx.Err()
}

func (f *Follower) dbErr(err error) {
	f.Metrics.FollowerErrors.Add(1)
	f.Log.Error("follower db write failed", "err", err)
}

func (f *Follower) pace() {
	if wait := minRequestInterval - time.Since(f.lastReq); wait > 0 {
		time.Sleep(wait)
	}
	f.lastReq = time.Now()
}

func (f *Follower) loadState(ctx context.Context) error {
	f.pending = map[string]*pendingSettlement{}

	var cursorRaw, pendingRaw []byte
	err := f.Pool.QueryRow(ctx, `SELECT value FROM indexer_state WHERE key=$1`, cursorKey).Scan(&cursorRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if cursorRaw != nil {
		var v struct {
			Height int64 `json:"height"`
		}
		if err := json.Unmarshal(cursorRaw, &v); err != nil {
			return err
		}
		f.cursor = v.Height - f.Overlap
	} else {
		head, err := f.Client.Head(ctx)
		if err != nil {
			return fmt.Errorf("fetch chain head: %w", err)
		}
		f.cursor = head.Height() - f.Overlap
		f.Log.Info("no cursor found; starting from chain head", "head", head.Height())
	}

	err = f.Pool.QueryRow(ctx, `SELECT value FROM indexer_state WHERE key=$1`, pendingKey).Scan(&pendingRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if pendingRaw != nil {
		var v struct {
			Entries []*pendingSettlement `json:"entries"`
		}
		if err := json.Unmarshal(pendingRaw, &v); err != nil {
			return err
		}
		for _, p := range v.Entries {
			f.pending[p.ReceiptID] = p
		}
	}
	return nil
}

// resolution is a settlement whose receipt outcome landed in this block.
type resolution struct {
	p         *pendingSettlement
	succeeded bool
}

func (f *Follower) processBlock(ctx context.Context, block *neardata.Block) error {
	height := block.Height()
	tsNs := block.TimestampNs()

	// 1. Discover new settlements in this block's chunk transactions.
	discovered := 0
	for _, shard := range block.Shards {
		if shard.Chunk == nil {
			continue
		}
		for _, tx := range shard.Chunk.Transactions {
			if tx.Transaction.ReceiverID != intents.Contract ||
				!intents.HasSettlement(tx.Transaction.Actions) {
				continue
			}
			msgs, err := intents.SignedFromActions(tx.Transaction.Actions)
			if err != nil {
				f.Metrics.ParseErrors.Add(1)
				f.Log.Error("settlement parse failed", "tx", tx.Transaction.Hash, "err", err)
				continue
			}
			receiptIDs := tx.Outcome.ExecutionOutcome.Outcome.ReceiptIDs
			if len(receiptIDs) == 0 {
				f.Metrics.ParseErrors.Add(1)
				f.Log.Error("settlement tx without receipt_ids", "tx", tx.Transaction.Hash)
				continue
			}
			f.pending[receiptIDs[0]] = &pendingSettlement{
				TxHash:    tx.Transaction.Hash,
				ReceiptID: receiptIDs[0],
				Height:    height,
				TsNs:      tsNs,
				Relayer:   tx.Transaction.SignerID,
				Msgs:      msgsToJSON(msgs),
				SeenAt:    height,
			}
			discovered++
		}
	}

	// 2. Resolve pending settlements whose receipt outcome appears in this
	// block (possibly the same block the tx was included in).
	var resolutions []resolution
	for _, shard := range block.Shards {
		for _, reo := range shard.ReceiptExecutionOutcomes {
			p, ok := f.pending[reo.ExecutionOutcome.ID]
			if !ok {
				continue
			}
			resolutions = append(resolutions, resolution{
				p:         p,
				succeeded: neardata.StatusSucceeded(reo.ExecutionOutcome.Outcome.Status),
			})
			delete(f.pending, reo.ExecutionOutcome.ID)
		}
	}

	// 3. Expire stuck entries (safety valve; should never fire).
	for id, p := range f.pending {
		if height-p.SeenAt > pendingExpiryBlocks {
			f.Log.Error("pending settlement expired without receipt outcome; recording as failed",
				"tx", p.TxHash, "receipt", id, "seen_at", p.SeenAt)
			resolutions = append(resolutions, resolution{p: p, succeeded: false})
			delete(f.pending, id)
		}
	}

	if discovered == 0 && len(resolutions) == 0 {
		return f.persistCursorAt(ctx, height)
	}
	return f.commitBlock(ctx, height, resolutions)
}

// commitBlock writes everything from one block atomically: resolved
// settlements + their legs + solver-count bumps + cursor + pending map.
func (f *Follower) commitBlock(ctx context.Context, height int64, resolutions []resolution) error {
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, r := range resolutions {
		if err := f.writeSettlement(ctx, tx, r); err != nil {
			return fmt.Errorf("settlement %s: %w", r.p.TxHash, err)
		}
	}
	if err := f.writeState(ctx, tx, height); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (f *Follower) writeSettlement(ctx context.Context, tx pgx.Tx, r resolution) error {
	p := r.p
	blockTs := time.Unix(0, p.TsNs).UTC()

	var inserted string
	err := tx.QueryRow(ctx, `
		INSERT INTO settlements (tx_hash, block_height, block_ts, relayer, succeeded, n_msgs)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tx_hash) DO NOTHING
		RETURNING tx_hash`,
		p.TxHash, p.Height, blockTs, nullable(p.Relayer), r.succeeded, len(p.Msgs)).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already processed (restart overlap) — skip observe + legs too
	}
	if err != nil {
		return err
	}

	if !r.succeeded {
		f.Metrics.SettlementsTotal.Add(1)
		f.Metrics.SettlementsFailed.Add(1)
		return nil
	}

	msgs, err := msgsFromJSON(p.Msgs)
	if err != nil {
		return err
	}
	// Mirror the oracle's call order: observe signers BEFORE classifying, so a
	// settlement counts toward its own signers' frequency promotion.
	signers := map[string]bool{}
	for i := range msgs {
		if msgs[i].HasTokenDiff() {
			signers[msgs[i].Signer] = true
		}
	}
	f.Solvers.Observe(signers)
	for s := range signers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO solvers (account_id, n_settlements) VALUES ($1, 1)
			ON CONFLICT (account_id) DO UPDATE SET n_settlements = solvers.n_settlements + 1`, s); err != nil {
			return err
		}
	}

	roles := intents.ClassifySigners(msgs, f.Solvers)
	legs := intents.ExtractSolverLegs(msgs, roles)
	for seq, leg := range legs {
		legClass := f.Registry.ClassifyPair(leg.RecvAsset, leg.GiveAsset)
		var amountIn, amountOut *string
		if a := f.Registry.Get(leg.RecvAsset); a != nil {
			v := a.FormatAmount(decimal.NewFromBigInt(leg.RecvAmount, 0)).String()
			amountIn = &v
		}
		if a := f.Registry.Get(leg.GiveAsset); a != nil {
			v := a.FormatAmount(decimal.NewFromBigInt(leg.GiveAmount, 0)).String()
			amountOut = &v
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO legs (tx_hash, seq, block_ts, block_height, solver, leg_class, multi_asset,
			                  from_asset, to_asset, amount_in_raw, amount_out_raw, amount_in, amount_out)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (tx_hash, seq) DO NOTHING`,
			p.TxHash, seq, blockTs, p.Height, leg.Signer, legClass, leg.MultiAsset,
			leg.RecvAsset, leg.GiveAsset, leg.RecvAmount.String(), leg.GiveAmount.String(),
			amountIn, amountOut); err != nil {
			return err
		}
	}
	f.Metrics.SettlementsTotal.Add(1)
	f.Metrics.LegsTotal.Add(int64(len(legs)))
	return nil
}

func (f *Follower) writeState(ctx context.Context, tx pgx.Tx, height int64) error {
	cursorVal, _ := json.Marshal(map[string]int64{"height": height})
	entries := make([]*pendingSettlement, 0, len(f.pending))
	for _, p := range f.pending {
		entries = append(entries, p)
	}
	pendingVal, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO indexer_state (key, value) VALUES ($1, $2), ($3, $4)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		cursorKey, cursorVal, pendingKey, pendingVal); err != nil {
		return err
	}
	return nil
}

func (f *Follower) persistCursorOnly(ctx context.Context) error {
	return f.persistCursorAt(ctx, f.cursor)
}

func (f *Follower) persistCursorAt(ctx context.Context, height int64) error {
	cursorVal, _ := json.Marshal(map[string]int64{"height": height})
	_, err := f.Pool.Exec(ctx, `
		INSERT INTO indexer_state (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, cursorKey, cursorVal)
	return err
}

// LoadSolverState hydrates the in-memory SolverSet from the solvers table.
func LoadSolverState(ctx context.Context, pool *pgxpool.Pool, set *intents.SolverSet) error {
	rows, err := pool.Query(ctx, `SELECT account_id, is_seed, n_settlements FROM solvers`)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := map[string]int64{}
	var seeds []string
	for rows.Next() {
		var account string
		var isSeed bool
		var n int64
		if err := rows.Scan(&account, &isSeed, &n); err != nil {
			return err
		}
		counts[account] = n
		if isSeed {
			seeds = append(seeds, account)
		}
	}
	set.Load(counts, seeds)
	return rows.Err()
}

// SyncAssets upserts the current registry snapshot into the assets table.
func SyncAssets(ctx context.Context, pool *pgxpool.Pool, reg *assets.Registry) error {
	for _, a := range reg.All() {
		if _, err := pool.Exec(ctx, `
			INSERT INTO assets (asset_id, symbol, chain, contract, decimals, is_stable, price_usd_snapshot, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7, now())
			ON CONFLICT (asset_id) DO UPDATE SET
			  symbol=EXCLUDED.symbol, chain=EXCLUDED.chain, contract=EXCLUDED.contract,
			  decimals=EXCLUDED.decimals, is_stable=EXCLUDED.is_stable,
			  price_usd_snapshot=EXCLUDED.price_usd_snapshot, updated_at=now()`,
			a.AssetID, a.Symbol, a.Blockchain, nullable(a.Contract), a.Decimals, a.IsStable(), a.PriceUSD); err != nil {
			return err
		}
	}
	return nil
}

// LoadAssetsFromDB fills the registry from the assets table (fallback when the
// 1Click API is unreachable at startup).
func LoadAssetsFromDB(ctx context.Context, pool *pgxpool.Pool, reg *assets.Registry) (int, error) {
	rows, err := pool.Query(ctx, `SELECT asset_id, symbol, chain, contract, decimals, price_usd_snapshot FROM assets`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type tokenJSON struct {
		AssetID         string   `json:"assetId"`
		Symbol          string   `json:"symbol"`
		Decimals        int      `json:"decimals"`
		Blockchain      string   `json:"blockchain"`
		ContractAddress string   `json:"contractAddress"`
		Price           *float64 `json:"price"`
	}
	var tokens []tokenJSON
	for rows.Next() {
		var t tokenJSON
		var contract *string
		var decimals *int
		if err := rows.Scan(&t.AssetID, &t.Symbol, &t.Blockchain, &contract, &decimals, &t.Price); err != nil {
			return 0, err
		}
		if contract != nil {
			t.ContractAddress = *contract
		}
		if decimals != nil {
			t.Decimals = *decimals
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(tokens) == 0 {
		return 0, nil
	}
	raw, err := json.Marshal(tokens)
	if err != nil {
		return 0, err
	}
	return len(tokens), reg.LoadJSON(raw)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
