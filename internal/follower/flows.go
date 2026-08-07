// Solver flow extraction: deposits (mt_mint), withdrawals (mt_burn) and
// internal transfers, recorded for known solvers only.
//
// Settlement receipts get their flows in writeSettlement (burn/mint events
// from the receipt logs + `transfer` intents from the signed messages —
// mt_transfer *events* there are token_diff legs, never flows). All other
// successful intents.near receipts (deposits, direct withdrawals, direct
// transfers) are handled uniformly from their events and receipt args.
package follower

import (
	"context"
	"encoding/base64"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"intents-explorer/internal/intents"
	"intents-explorer/internal/neardata"
)

// minFlowUSD drops dust rows (bridge-helper change, gas refuels). Rows whose
// USD value cannot be estimated are kept.
const minFlowUSD = 1.0

// originTTLBlocks bounds the BRIDGED_FROM cache: the bridge-token receipt
// and the deposit mint receipt share a tx and land within a block or two.
const originTTLBlocks = 60

type flowRow struct {
	ReceiptID            string
	Seq                  int
	TxHash               string
	Height               int64
	TsNs                 int64
	Account              string
	Direction            string // deposit|withdrawal|transfer_in|transfer_out
	AssetID              string
	AmountRaw            *big.Int
	Counterparty         string
	CounterpartyWithdrew bool
	ExternalAddress      string
	OriginChain          string
	OriginTx             string
	Memo                 string
}

type originEntry struct {
	intents.DepositOrigin
	Height int64
}

// harvestOrigins caches BRIDGED_FROM provenance from any receipt in the
// block, keyed by NEAR tx hash. In-memory only: the restart overlap
// re-scans recent blocks, so entries survive restarts in practice.
func (f *Follower) harvestOrigins(reo *neardata.ReceiptExecutionOutcome, height int64) {
	if o := intents.ParseDepositOrigin(reo.ExecutionOutcome.Outcome.Logs); o != nil {
		f.origins[reo.TxHash] = &originEntry{DepositOrigin: *o, Height: height}
	}
}

func (f *Follower) pruneOrigins(height int64) {
	for tx, o := range f.origins {
		if height-o.Height > originTTLBlocks {
			delete(f.origins, tx)
		}
	}
}

// isDust reports whether the flow is below the USD floor. Unknown or
// unpriced assets are kept (surfaced, not guessed).
func (f *Follower) isDust(assetID string, amount *big.Int) bool {
	a := f.Registry.Get(assetID)
	if a == nil {
		return false
	}
	price := a.PriceUSD
	if price == nil {
		if !a.IsStable() {
			return false
		}
		one := 1.0
		price = &one
	}
	v, _ := a.FormatAmount(decimal.NewFromBigInt(amount, 0)).Float64()
	return v*(*price) < minFlowUSD
}

// receiptCalls decodes the FunctionCall actions of a receipt.
func receiptCalls(reo *neardata.ReceiptExecutionOutcome) []neardata.FunctionCall {
	var calls []neardata.FunctionCall
	for _, a := range reo.Actions() {
		if fc, ok := neardata.AsFunctionCall(a); ok {
			calls = append(calls, fc)
		}
	}
	return calls
}

// genericFlows extracts flows from a successful non-settlement intents.near
// receipt: deposit mints (with NEAR sender + bridged origin), direct-call
// withdrawal burns (with destination from the call args), and direct
// mt_transfer moves. Refund mints/burns (memo "refund") land here too and
// net out flows recorded earlier.
func (f *Follower) genericFlows(reo *neardata.ReceiptExecutionOutcome, height, tsNs int64) []flowRow {
	events := intents.ParseNEP245Events(reo.ExecutionOutcome.Outcome.Logs)
	if len(events) == 0 {
		return nil
	}

	var sender string
	var withdrawals []*intents.WithdrawalIntent
	for _, fc := range receiptCalls(reo) {
		args, err := base64.StdEncoding.DecodeString(fc.Args)
		if err != nil {
			continue
		}
		switch fc.MethodName {
		case "ft_on_transfer", "mt_on_transfer":
			if s := intents.OnTransferSender(args); s != "" {
				sender = s
			}
		default:
			if w := intents.WithdrawalFromArgs(fc.MethodName, args); w != nil {
				withdrawals = append(withdrawals, w)
			}
		}
	}

	// Each event owns two seq slots (2i primary, 2i+1 transfer mirror) so
	// row identity stays deterministic across retries regardless of the
	// solver set or dust filtering at the time of the attempt.
	var rows []flowRow
	for i, ev := range events {
		row := flowRow{
			ReceiptID: reo.ExecutionOutcome.ID, Seq: 2 * i, TxHash: reo.TxHash,
			Height: height, TsNs: tsNs, AssetID: ev.AssetID, AmountRaw: ev.Amount, Memo: ev.Memo,
		}
		switch ev.Kind {
		case "mint":
			if !f.Solvers.IsSolver(ev.Owner) {
				continue
			}
			row.Account, row.Direction, row.Counterparty = ev.Owner, "deposit", sender
			if o := f.origins[reo.TxHash]; o != nil {
				row.OriginChain, row.OriginTx = o.Network, o.TxHash
			}
		case "burn":
			if !f.Solvers.IsSolver(ev.Owner) {
				continue
			}
			row.Account, row.Direction = ev.Owner, "withdrawal"
			for _, w := range withdrawals {
				if w.Covers(ev.AssetID) {
					row.Counterparty, row.ExternalAddress = w.Receiver, w.ExternalAddress()
					break
				}
			}
		case "transfer":
			// Direct mt_transfer outside a settlement (settlement transfers
			// come from intents in writeSettlement, never from events).
			if f.Solvers.IsSolver(ev.From) {
				out := row
				out.Account, out.Direction, out.Counterparty = ev.From, "transfer_out", ev.To
				if !f.isDust(out.AssetID, out.AmountRaw) {
					rows = append(rows, out)
				}
			}
			if f.Solvers.IsSolver(ev.To) {
				in := row
				in.Seq = 2*i + 1
				in.Account, in.Direction, in.Counterparty = ev.To, "transfer_in", ev.From
				if !f.isDust(in.AssetID, in.AmountRaw) {
					rows = append(rows, in)
				}
			}
			continue
		}
		if row.Direction == "" || f.isDust(row.AssetID, row.AmountRaw) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// settlementFlows extracts flows from a successful settlement: burn (and
// rare mint) events for solver owners, enriched with destinations from the
// matching withdrawal intents, plus `transfer` intents where a known solver
// is on either side.
func (f *Follower) settlementFlows(p *pendingSettlement, msgs []intents.SignedMsg, logs []string) []flowRow {
	events := intents.ParseNEP245Events(logs)

	withdrawalsBy := map[string][]*intents.WithdrawalIntent{}
	for i := range msgs {
		for j := range msgs[i].Withdrawals {
			w := &msgs[i].Withdrawals[j]
			withdrawalsBy[msgs[i].Signer] = append(withdrawalsBy[msgs[i].Signer], w)
		}
	}
	match := func(account, assetID string) *intents.WithdrawalIntent {
		var first *intents.WithdrawalIntent
		for _, w := range withdrawalsBy[account] {
			if first == nil {
				first = w
			}
			if w.Covers(assetID) {
				return w
			}
		}
		return first
	}

	base := flowRow{ReceiptID: p.ReceiptID, TxHash: p.TxHash, Height: p.Height, TsNs: p.TsNs}
	var rows []flowRow

	// Burn/mint events: the ledger's own record of funds leaving/entering.
	// mt_transfer events here are token_diff legs — skipped by design.
	// Seq slots: one per event, then two per transfer-intent token (out/in),
	// enumerated identically on every attempt so retries hit ON CONFLICT.
	for i, ev := range events {
		if ev.Kind == "transfer" || !f.Solvers.IsSolver(ev.Owner) {
			continue
		}
		row := base
		row.Seq, row.Account, row.AssetID, row.AmountRaw, row.Memo = i, ev.Owner, ev.AssetID, ev.Amount, ev.Memo
		if ev.Kind == "burn" {
			row.Direction = "withdrawal"
			if w := match(ev.Owner, ev.AssetID); w != nil && w.Covers(ev.AssetID) {
				row.Counterparty, row.ExternalAddress = w.Receiver, w.ExternalAddress()
			}
		} else {
			row.Direction = "deposit"
		}
		if !f.isDust(row.AssetID, row.AmountRaw) {
			rows = append(rows, row)
		}
	}

	// Transfer intents: internal moves. When the counterparty also signed a
	// withdrawal in this settlement, the transfer is a bridge-out via a
	// helper account — resolve the external destination from that intent.
	slot := len(events)
	for i := range msgs {
		signer := msgs[i].Signer
		for _, t := range msgs[i].Transfers {
			for _, tok := range t.Tokens {
				outSeq, inSeq := slot, slot+1
				slot += 2
				if tok.Amount.Sign() <= 0 || f.isDust(tok.Asset, tok.Amount) {
					continue
				}
				emit := func(seq int, account, direction, counterparty string) {
					row := base
					row.Seq, row.Account, row.Direction = seq, account, direction
					row.AssetID, row.AmountRaw = tok.Asset, tok.Amount
					row.Counterparty, row.Memo = counterparty, t.Memo
					if len(withdrawalsBy[counterparty]) > 0 {
						row.CounterpartyWithdrew = true
						if w := match(counterparty, tok.Asset); w != nil {
							row.ExternalAddress = w.ExternalAddress()
						}
					}
					rows = append(rows, row)
				}
				if f.Solvers.IsSolver(signer) {
					emit(outSeq, signer, "transfer_out", t.Receiver)
				}
				if f.Solvers.IsSolver(t.Receiver) {
					emit(inSeq, t.Receiver, "transfer_in", signer)
				}
			}
		}
	}
	return rows
}

func writeFlows(ctx context.Context, tx pgx.Tx, f *Follower, rows []flowRow) error {
	for _, r := range rows {
		var amount *string
		if a := f.Registry.Get(r.AssetID); a != nil {
			v := a.FormatAmount(decimal.NewFromBigInt(r.AmountRaw, 0)).String()
			amount = &v
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO solver_flows (receipt_id, seq, tx_hash, block_height, block_ts, account_id,
			                          direction, asset_id, amount_raw, amount, counterparty,
			                          counterparty_withdrew, external_address, origin_chain, origin_tx, memo)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			ON CONFLICT (receipt_id, seq) DO NOTHING`,
			r.ReceiptID, r.Seq, r.TxHash, r.Height, time.Unix(0, r.TsNs).UTC(), r.Account,
			r.Direction, r.AssetID, r.AmountRaw.String(), amount, nullable(r.Counterparty),
			r.CounterpartyWithdrew, nullable(r.ExternalAddress), nullable(r.OriginChain),
			nullable(r.OriginTx), nullable(r.Memo)); err != nil {
			return err
		}
		f.Metrics.FlowsTotal.Add(1)
	}
	return nil
}
