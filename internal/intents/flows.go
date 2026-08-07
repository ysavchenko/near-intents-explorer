// NEP-245 event parsing and deposit/withdrawal destination decoding.
//
// intents.near is a NEP-245 multi-token: every deposit mints (`mt_mint`),
// every withdrawal burns (`mt_burn`), and internal moves emit `mt_transfer` —
// all verified against the contract source (github.com/near/intents): every
// balance entry/exit funnels through one deposit()/withdraw() pair that
// always emits these events.
package intents

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

const eventPrefix = "EVENT_JSON:"

// EventRow is one (owner, token, amount) of a NEP-245 event. Mint = deposit
// into intents.near, burn = withdrawal out, transfer = internal move.
type EventRow struct {
	Kind    string // mint | burn | transfer
	Owner   string // mint/burn
	From    string // transfer
	To      string // transfer
	AssetID string // NEP-245 token id == registry asset id
	Amount  *big.Int
	Memo    string
}

// ParseNEP245Events extracts mint/burn/transfer rows from receipt logs.
// Junk logs are skipped, never fatal.
func ParseNEP245Events(logs []string) []EventRow {
	var rows []EventRow
	for _, log := range logs {
		if !strings.HasPrefix(log, eventPrefix) {
			continue
		}
		var ev struct {
			Standard string `json:"standard"`
			Event    string `json:"event"`
			Data     []struct {
				OwnerID    string   `json:"owner_id"`
				OldOwnerID string   `json:"old_owner_id"`
				NewOwnerID string   `json:"new_owner_id"`
				TokenIDs   []string `json:"token_ids"`
				Amounts    []string `json:"amounts"`
				Memo       string   `json:"memo"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(log[len(eventPrefix):]), &ev); err != nil {
			continue
		}
		if ev.Standard != "nep245" {
			continue
		}
		var kind string
		switch ev.Event {
		case "mt_mint":
			kind = "mint"
		case "mt_burn":
			kind = "burn"
		case "mt_transfer":
			kind = "transfer"
		default:
			continue
		}
		for _, d := range ev.Data {
			for i, tid := range d.TokenIDs {
				if i >= len(d.Amounts) {
					break
				}
				n, ok := new(big.Int).SetString(d.Amounts[i], 10)
				if !ok || n.Sign() == 0 {
					continue
				}
				rows = append(rows, EventRow{
					Kind: kind, Owner: d.OwnerID, From: d.OldOwnerID, To: d.NewOwnerID,
					AssetID: tid, Amount: n, Memo: d.Memo,
				})
			}
		}
	}
	return rows
}

// --------------------------------------------------------------------------
// Withdrawal / transfer intents (destination + counterparty enrichment)
// --------------------------------------------------------------------------

// WithdrawalIntent is a signed *_withdraw intent, kept for matching burn
// events to their NEAR-side receiver and external-chain destination.
type WithdrawalIntent struct {
	Kind     string   `json:"kind"`
	Receiver string   `json:"receiver,omitempty"`  // NEAR-side receiver_id
	AssetIDs []string `json:"asset_ids,omitempty"` // NEP-245 ids this intent burns
	Memo     string   `json:"memo,omitempty"`
	Msg      string   `json:"msg,omitempty"`
}

// TransferIntent is a signed `transfer` intent: an internal balance move.
// Solvers use these to hand funds to bridge helper accounts that withdraw
// in the same settlement (the HOT-bridge pattern), so they are flows even
// though the NEP-245 ledger records no burn for the sender.
type TransferIntent struct {
	Receiver string
	Tokens   []DiffEntry // positive amounts, order preserved
	Memo     string
}

// Covers reports whether the intent burns the given NEP-245 asset id.
func (w *WithdrawalIntent) Covers(assetID string) bool {
	for _, id := range w.AssetIDs {
		if id == assetID {
			return true
		}
	}
	return false
}

const withdrawToPrefix = "WITHDRAW_TO:"

// ExternalAddress recovers the external-chain destination when the intent
// encodes one:
//   - PoA bridge (omft assets): memo "WITHDRAW_TO:<address>";
//   - HOT omni-bridge: msg JSON carries receiver_id, base58; 20-byte
//     payloads are EVM addresses (re-encoded 0x...), anything else (e.g.
//     32-byte Solana keys) is kept in its base58 form.
//
// Empty result = NEAR-internal withdrawal (destination is Receiver).
func (w *WithdrawalIntent) ExternalAddress() string {
	if strings.HasPrefix(w.Memo, withdrawToPrefix) {
		return strings.TrimSpace(w.Memo[len(withdrawToPrefix):])
	}
	if w.Msg != "" {
		var m struct {
			ReceiverID string `json:"receiver_id"`
		}
		if err := json.Unmarshal([]byte(w.Msg), &m); err == nil && m.ReceiverID != "" {
			return decodeBridgeAddr(m.ReceiverID)
		}
	}
	return ""
}

// decodeBridgeAddr renders a bridge msg receiver in its native form: base58
// payloads of 20 bytes are EVM addresses, everything else stays as given.
func decodeBridgeAddr(s string) string {
	if b, err := base58Decode(s); err == nil && len(b) == 20 {
		return "0x" + fmt.Sprintf("%x", b)
	}
	return s
}

var b58Index = func() [256]int8 {
	var idx [256]int8
	for i := range idx {
		idx[i] = -1
	}
	for i, c := range "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz" {
		idx[c] = int8(i)
	}
	return idx
}()

func base58Decode(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	n := new(big.Int)
	for _, c := range []byte(s) {
		d := b58Index[c]
		if d < 0 {
			return nil, fmt.Errorf("bad base58 char %q", c)
		}
		n.Mul(n, big.NewInt(58)).Add(n, big.NewInt(int64(d)))
	}
	b := n.Bytes()
	for i := 0; i < len(s) && s[i] == '1'; i++ {
		b = append([]byte{0}, b...)
	}
	return b, nil
}

// withdrawalFromIntent builds a WithdrawalIntent from a decoded intent
// object; nil for non-withdrawal kinds. NFT withdrawals are skipped (not
// fungible flows).
func withdrawalFromIntent(kind string, raw intentBody) *WithdrawalIntent {
	w := &WithdrawalIntent{Kind: kind, Receiver: raw.ReceiverID, Memo: raw.Memo, Msg: raw.Msg}
	switch kind {
	case "ft_withdraw":
		if raw.Token == "" {
			return nil
		}
		w.AssetIDs = []string{"nep141:" + raw.Token}
	case "native_withdraw":
		// native NEAR leaves via the contract's wNEAR balance
		w.AssetIDs = []string{"nep141:wrap.near"}
	case "mt_withdraw":
		if raw.Token == "" {
			return nil
		}
		for _, tid := range raw.TokenIDs {
			w.AssetIDs = append(w.AssetIDs, "nep245:"+raw.Token+":"+tid)
		}
	default:
		return nil
	}
	return w
}

// intentBody is the superset of intent fields flows care about. The same
// shape covers direct contract calls (ft_withdraw etc. take identical
// fields as top-level args).
type intentBody struct {
	Intent     string          `json:"intent"`
	Diff       json.RawMessage `json:"diff"`
	Referral   string          `json:"referral"`
	Token      string          `json:"token"`
	TokenIDs   []string        `json:"token_ids"`
	ReceiverID string          `json:"receiver_id"`
	Tokens     json.RawMessage `json:"tokens"` // transfer: {asset: amount}
	Memo       string          `json:"memo"`
	Msg        string          `json:"msg"`
}

// WithdrawalFromArgs parses a direct contract-call withdrawal (args share
// the intent field shape); nil if the method is not a withdrawal.
func WithdrawalFromArgs(method string, args []byte) *WithdrawalIntent {
	if !withdrawalKinds[method] {
		return nil
	}
	var body intentBody
	if err := json.Unmarshal(args, &body); err != nil {
		return nil
	}
	return withdrawalFromIntent(method, body)
}

// OnTransferSender extracts sender_id from ft_on_transfer / mt_on_transfer
// args (the NEAR-side source of a deposit; for bridged deposits this is the
// bridge account and the true origin comes from BRIDGED_FROM memos).
func OnTransferSender(args []byte) string {
	var v struct {
		SenderID string `json:"sender_id"`
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return ""
	}
	return v.SenderID
}

// DepositOrigin is the source-chain provenance of a bridged deposit, parsed
// from the PoA bridge's "BRIDGED_FROM:{...}" transfer memo.
type DepositOrigin struct {
	Network string `json:"networkType"`
	ChainID string `json:"chainId"`
	TxHash  string `json:"txHash"`
}

const bridgedFromPrefix = "BRIDGED_FROM:"

// ParseDepositOrigin scans receipt logs (any NEP-141 event memo) for a
// BRIDGED_FROM marker. Returns nil when absent.
func ParseDepositOrigin(logs []string) *DepositOrigin {
	for _, log := range logs {
		if !strings.Contains(log, bridgedFromPrefix) || !strings.HasPrefix(log, eventPrefix) {
			continue
		}
		var ev struct {
			Data []struct {
				Memo string `json:"memo"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(log[len(eventPrefix):]), &ev); err != nil {
			continue
		}
		for _, d := range ev.Data {
			if !strings.HasPrefix(d.Memo, bridgedFromPrefix) {
				continue
			}
			var o DepositOrigin
			if err := json.Unmarshal([]byte(d.Memo[len(bridgedFromPrefix):]), &o); err != nil {
				continue
			}
			if o.Network != "" || o.TxHash != "" {
				return &o
			}
		}
	}
	return nil
}
