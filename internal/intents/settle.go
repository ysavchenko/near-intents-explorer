// Package intents parses `execute_intents` settlements into individual solver
// swap legs. Parsing mechanics (payload decoding, diff ordering, dominant-pair
// selection) mirror the Python reference (handoff/reference/lib/settle.py) and
// are guarded by golden fixtures. Signer role classification deliberately
// diverges from the reference — see ClassifySigners.
//
// The unit of analysis is one token_diff intent = one swap leg, from the
// solver's side. We do NOT net by account: a solver that routes WNEAR->USDT
// then USDT->USDC signs two token_diffs, and those are two distinct swaps.
package intents

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"intents-explorer/internal/neardata"
)

const (
	Contract         = "intents.near"
	SettlementMethod = "execute_intents"
)

var withdrawalKinds = map[string]bool{
	"ft_withdraw": true, "native_withdraw": true, "mt_withdraw": true,
}

// DiffEntry is one asset's signed amount inside a token_diff. Order matters:
// entries keep the JSON object order so dominant-pair tie-breaks match the
// Python oracle (dict order + stable sort).
type DiffEntry struct {
	Asset  string
	Amount *big.Int
}

// SignedMsg is one signer's signed message within a settlement.
type SignedMsg struct {
	Signer      string
	Kinds       []string
	TokenDiffs  [][]DiffEntry // one per token_diff intent (zero entries dropped)
	HasReferral bool          // any intent carried a `referral` (aggregator-routed user flow)
}

func (m *SignedMsg) HasWithdrawal() bool {
	for _, k := range m.Kinds {
		if withdrawalKinds[k] {
			return true
		}
	}
	return false
}

func (m *SignedMsg) HasTokenDiff() bool { return len(m.TokenDiffs) > 0 }

// TakerMarked reports whether the message carries a structural taker signal:
// the signer withdraws funds out of intents, or its intents carry a referral
// tag (set by aggregators on user-originated flow, never by solvers).
func (m *SignedMsg) TakerMarked() bool { return m.HasWithdrawal() || m.HasReferral }

// SwapLeg is one token_diff from the solver's perspective: the dominant
// positive entry is what the solver receives, the dominant negative what it
// gives.
type SwapLeg struct {
	Signer     string
	RecvAsset  string
	RecvAmount *big.Int
	GiveAsset  string
	GiveAmount *big.Int
	MultiAsset bool // diff had >2 assets (dominant pair kept)
}

// --------------------------------------------------------------------------
// Decode raw actions -> signed messages (token_diffs split out individually)
// --------------------------------------------------------------------------

// messageToSigned mirrors settle.py::_message_to_signed.
func messageToSigned(raw []byte) SignedMsg {
	var msg struct {
		SignerID string            `json:"signer_id"`
		Intents  []json.RawMessage `json:"intents"`
	}
	// Tolerate junk: an unparseable message yields an empty SignedMsg exactly
	// like Python's .get() defaults would.
	_ = json.Unmarshal(raw, &msg)
	out := SignedMsg{Signer: msg.SignerID}
	if out.Signer == "" {
		out.Signer = "?"
	}
	for _, it := range msg.Intents {
		var intent struct {
			Intent   string          `json:"intent"`
			Diff     json.RawMessage `json:"diff"`
			Referral string          `json:"referral"`
		}
		if err := json.Unmarshal(it, &intent); err != nil {
			continue
		}
		kind := intent.Intent
		if kind == "" {
			kind = "?"
		}
		out.Kinds = append(out.Kinds, kind)
		if intent.Referral != "" {
			out.HasReferral = true
		}
		if intent.Intent == "token_diff" {
			d, err := parseDiff(intent.Diff)
			if err != nil {
				continue
			}
			if len(d) > 0 {
				out.TokenDiffs = append(out.TokenDiffs, d)
			}
		}
	}
	return out
}

// parseDiff decodes a token_diff object preserving key order, dropping zero
// amounts. Amounts arrive as JSON strings (u128) but numbers are tolerated.
func parseDiff(raw []byte) ([]DiffEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("token_diff: not an object")
	}
	var entries []DiffEntry
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		asset := keyTok.(string)
		var val any
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		var s string
		switch v := val.(type) {
		case string:
			s = v
		case float64:
			s = big.NewFloat(v).Text('f', 0)
		default:
			return nil, fmt.Errorf("token_diff: bad amount for %s", asset)
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil, fmt.Errorf("token_diff: bad amount %q for %s", s, asset)
		}
		if n.Sign() != 0 {
			entries = append(entries, DiffEntry{Asset: asset, Amount: n})
		}
	}
	return entries, nil
}

// ParseSigned decodes an `execute_intents` args object into signed messages,
// tolerating NEP-413 (`payload.message` is a JSON string) and EIP-712/ERC-191
// (`payload` is the message JSON string) encodings. Unknown payload shapes are
// skipped, never fatal (mirrors settle.py::parse_signed).
func ParseSigned(argsObj []byte) ([]SignedMsg, error) {
	var args struct {
		Signed []json.RawMessage `json:"signed"`
	}
	if err := json.Unmarshal(argsObj, &args); err != nil {
		return nil, fmt.Errorf("execute_intents args: %w", err)
	}
	var out []SignedMsg
	for _, s := range args.Signed {
		// An entry may itself be a JSON-encoded string.
		if len(s) > 0 && s[0] == '"' {
			var inner string
			if err := json.Unmarshal(s, &inner); err != nil {
				continue
			}
			s = []byte(inner)
		}
		var envelope struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(s, &envelope); err != nil || len(envelope.Payload) == 0 {
			continue
		}
		var msgRaw []byte
		if envelope.Payload[0] == '"' { // EIP-712 / ERC-191: payload is the message JSON string
			var inner string
			if err := json.Unmarshal(envelope.Payload, &inner); err != nil {
				continue
			}
			msgRaw = []byte(inner)
		} else if envelope.Payload[0] == '{' { // NEP-413: payload.message is a JSON string
			var payload struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.Message == "" {
				continue
			}
			msgRaw = []byte(payload.Message)
		} else {
			continue
		}
		out = append(out, messageToSigned(msgRaw))
	}
	return out, nil
}

// SignedFromActions extracts signed messages from every `execute_intents`
// FunctionCall in a transaction's actions (args are base64 JSON).
func SignedFromActions(actions []json.RawMessage) ([]SignedMsg, error) {
	var out []SignedMsg
	for _, a := range actions {
		fc, ok := neardata.AsFunctionCall(a)
		if !ok || fc.MethodName != SettlementMethod {
			continue
		}
		argsObj, err := base64.StdEncoding.DecodeString(fc.Args)
		if err != nil {
			return nil, fmt.Errorf("args base64: %w", err)
		}
		msgs, err := ParseSigned(argsObj)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	return out, nil
}

// HasSettlement reports whether any action is an execute_intents call.
func HasSettlement(actions []json.RawMessage) bool {
	for _, a := range actions {
		if fc, ok := neardata.AsFunctionCall(a); ok && fc.MethodName == SettlementMethod {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------------------
// Signer roles + leg extraction
// --------------------------------------------------------------------------

// ClassifySigners maps each token_diff signer to a role: solver | user |
// unknown.
//
// Signals, combined rather than tiered:
//   - taker markers (TakerMarked: a withdrawal, or a referral-tagged intent)
//     pin a signer as the user regardless of identity;
//   - known solvers — the curated seed set plus frequency-promoted accounts —
//     are solvers when unmarked; everyone else in such a settlement is a user.
//
// Without any known solver, the taker markers alone decide (marked = user,
// rest = solver); with no signal at all, roles are unknown (surfaced, not
// guessed).
//
// This deliberately diverges from the Python reference, which gave the seed
// set primacy and demoted every co-signer to taker: one settlement can carry
// several independent solver legs (e.g. a large fill by a learned solver plus
// seed-solver dust covering a bridge fee), and all of them must be kept.
func ClassifySigners(msgs []SignedMsg, solvers *SolverSet) map[string]string {
	marked := map[string]bool{} // token_diff signer -> taker-marked
	for i := range msgs {
		if msgs[i].HasTokenDiff() {
			marked[msgs[i].Signer] = marked[msgs[i].Signer] || msgs[i].TakerMarked()
		}
	}
	roles := make(map[string]string, len(marked))

	isKnown := func(s string) bool { return solvers.IsSeed(s) || solvers.IsSolver(s) }
	anyKnown := false
	for s, m := range marked {
		if !m && isKnown(s) {
			anyKnown = true
			break
		}
	}
	if anyKnown {
		for s, m := range marked {
			if !m && isKnown(s) {
				roles[s] = "solver"
			} else {
				roles[s] = "user"
			}
		}
		return roles
	}

	anyMarked := false
	for _, m := range marked {
		if m {
			anyMarked = true
			break
		}
	}
	if anyMarked {
		for s, m := range marked {
			if m {
				roles[s] = "user"
			} else {
				roles[s] = "solver"
			}
		}
		return roles
	}

	for s := range marked {
		roles[s] = "unknown"
	}
	return roles
}

// legFromDiff builds a leg from one token_diff: dominant received vs dominant
// given. Returns nil for one-sided accounting entries (not a swap).
func legFromDiff(signer string, diff []DiffEntry) *SwapLeg {
	var pos, neg []DiffEntry
	for _, e := range diff {
		if e.Amount.Sign() > 0 {
			pos = append(pos, e)
		} else {
			neg = append(neg, DiffEntry{Asset: e.Asset, Amount: new(big.Int).Neg(e.Amount)})
		}
	}
	// Stable sort descending by amount: on ties the original JSON order wins,
	// matching Python's stable sorted() over dict order.
	sort.SliceStable(pos, func(i, j int) bool { return pos[i].Amount.Cmp(pos[j].Amount) > 0 })
	sort.SliceStable(neg, func(i, j int) bool { return neg[i].Amount.Cmp(neg[j].Amount) > 0 })
	if len(pos) == 0 || len(neg) == 0 {
		return nil
	}
	return &SwapLeg{
		Signer:     signer,
		RecvAsset:  pos[0].Asset,
		RecvAmount: pos[0].Amount,
		GiveAsset:  neg[0].Asset,
		GiveAmount: neg[0].Amount,
		MultiAsset: len(pos) > 1 || len(neg) > 1,
	}
}

// ExtractSolverLegs returns the legs of every solver-role signer, in signed
// message order (the leg's position is the stable `seq` join key).
func ExtractSolverLegs(msgs []SignedMsg, roles map[string]string) []SwapLeg {
	var legs []SwapLeg
	for i := range msgs {
		if roles[msgs[i].Signer] != "solver" {
			continue
		}
		for _, d := range msgs[i].TokenDiffs {
			if leg := legFromDiff(msgs[i].Signer, d); leg != nil {
				legs = append(legs, *leg)
			}
		}
	}
	return legs
}
