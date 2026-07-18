package intents

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

// buildArgs wraps signed messages into an execute_intents args object using
// the NEP-413 shape (payload.message = message JSON string).
func buildArgsNEP413(t *testing.T, messages ...map[string]any) []byte {
	t.Helper()
	var signed []map[string]any
	for _, m := range messages {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		signed = append(signed, map[string]any{
			"standard": "nep413",
			"payload":  map[string]any{"message": string(raw)},
		})
	}
	out, err := json.Marshal(map[string]any{"signed": signed})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func tokenDiffMsg(signer string, diff string) map[string]any {
	var d map[string]any
	if err := json.Unmarshal([]byte(diff), &d); err != nil {
		panic(err)
	}
	return map[string]any{
		"signer_id": signer,
		"intents":   []any{map[string]any{"intent": "token_diff", "diff": json.RawMessage(diff)}},
	}
}

func TestMultiAssetDominantPair(t *testing.T) {
	// >2 assets in one diff: dominant positive = received, dominant negative =
	// given, multi_asset flagged. No fixture covers this path (0 of 14,292 legs
	// in the reference window) — the MANIFEST asks for synthetic coverage.
	msgs, err := ParseSigned(buildArgsNEP413(t, tokenDiffMsg("s.near",
		`{"a":"100","b":"250","c":"-30","d":"-500"}`)))
	if err != nil {
		t.Fatal(err)
	}
	legs := ExtractSolverLegs(msgs, map[string]string{"s.near": "solver"})
	if len(legs) != 1 {
		t.Fatalf("want 1 leg, got %d", len(legs))
	}
	l := legs[0]
	if l.RecvAsset != "b" || l.RecvAmount.String() != "250" {
		t.Errorf("recv: got %s/%s want b/250", l.RecvAsset, l.RecvAmount)
	}
	if l.GiveAsset != "d" || l.GiveAmount.String() != "500" {
		t.Errorf("give: got %s/%s want d/500", l.GiveAsset, l.GiveAmount)
	}
	if !l.MultiAsset {
		t.Error("multi_asset should be true")
	}
}

func TestDominantPairTieKeepsJSONOrder(t *testing.T) {
	// Equal amounts: Python's stable sort over dict order keeps the first key.
	msgs, err := ParseSigned(buildArgsNEP413(t, tokenDiffMsg("s.near",
		`{"first":"100","second":"100","out":"-10"}`)))
	if err != nil {
		t.Fatal(err)
	}
	legs := ExtractSolverLegs(msgs, map[string]string{"s.near": "solver"})
	if len(legs) != 1 || legs[0].RecvAsset != "first" {
		t.Fatalf("tie should keep JSON order: got %+v", legs)
	}
}

func TestOneSidedDiffIsNotALeg(t *testing.T) {
	msgs, err := ParseSigned(buildArgsNEP413(t, tokenDiffMsg("s.near", `{"a":"100"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if legs := ExtractSolverLegs(msgs, map[string]string{"s.near": "solver"}); len(legs) != 0 {
		t.Fatalf("one-sided diff must yield no legs, got %d", len(legs))
	}
}

func TestZeroAmountsDropped(t *testing.T) {
	msgs, err := ParseSigned(buildArgsNEP413(t, tokenDiffMsg("s.near",
		`{"a":"0","b":"5","c":"-5","d":"0"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(msgs[0].TokenDiffs) != 1 || len(msgs[0].TokenDiffs[0]) != 2 {
		t.Fatalf("zero entries must be dropped: %+v", msgs)
	}
}

func TestEIP712StringPayload(t *testing.T) {
	// EIP-712 / ERC-191: payload is the message JSON *string*.
	msg := `{"signer_id":"0xabc","intents":[{"intent":"token_diff","diff":{"x":"7","y":"-7"}}]}`
	args, _ := json.Marshal(map[string]any{
		"signed": []any{map[string]any{"standard": "eip712", "payload": msg}},
	})
	msgs, err := ParseSigned(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Signer != "0xabc" || len(msgs[0].TokenDiffs) != 1 {
		t.Fatalf("eip712 payload should parse: %+v", msgs)
	}
}

func TestSignedEntryAsJSONString(t *testing.T) {
	// A `signed` entry may itself be a JSON-encoded string.
	inner := `{"payload":{"message":"{\"signer_id\":\"z.near\",\"intents\":[{\"intent\":\"token_diff\",\"diff\":{\"x\":\"1\",\"y\":\"-1\"}}]}"}}`
	args, _ := json.Marshal(map[string]any{"signed": []any{inner}})
	msgs, err := ParseSigned(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Signer != "z.near" {
		t.Fatalf("stringified signed entry should parse: %+v", msgs)
	}
}

func TestUnknownPayloadShapesSkipped(t *testing.T) {
	// Unknown standards / payload shapes must be skipped, never fatal.
	args, _ := json.Marshal(map[string]any{"signed": []any{
		map[string]any{"standard": "webauthn", "payload": 42}, // non-string/dict payload
		map[string]any{"standard": "weird", "payload": map[string]any{ // dict without message
			"challenge": "xyz"}},
		map[string]any{"no_payload": true},
	}})
	msgs, err := ParseSigned(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("unknown shapes must be skipped: %+v", msgs)
	}
}

func TestClassifySignersSeedBeatsFrequency(t *testing.T) {
	// A known maker present -> every other token_diff signer is a taker, even
	// a frequency-promoted one.
	solvers := NewSolverSet()
	for i := 0; i < 10; i++ {
		solvers.Observe(map[string]bool{"frequent-taker.near": true})
	}
	msgs := []SignedMsg{
		mustMsg("crux-solver.near", `{"a":"1","b":"-1"}`),
		mustMsg("frequent-taker.near", `{"a":"-1","b":"1"}`),
	}
	roles := ClassifySigners(msgs, solvers)
	if roles["crux-solver.near"] != "solver" || roles["frequent-taker.near"] != "user" {
		t.Fatalf("seed must win: %v", roles)
	}
}

func TestClassifySignersFrequencyPromotion(t *testing.T) {
	solvers := NewSolverSet()
	msgs := []SignedMsg{
		mustMsg("recurring.near", `{"a":"1","b":"-1"}`),
		mustMsg("oneoff.near", `{"a":"-1","b":"1"}`),
	}
	// Below the threshold: no signal, roles unknown.
	for i := 0; i < 4; i++ {
		solvers.Observe(map[string]bool{"recurring.near": true})
	}
	if roles := ClassifySigners(msgs, solvers); roles["recurring.near"] != "unknown" {
		t.Fatalf("4 observations must not promote: %v", roles)
	}
	// At the threshold (>=5 distinct settlements): promoted.
	solvers.Observe(map[string]bool{"recurring.near": true})
	roles := ClassifySigners(msgs, solvers)
	if roles["recurring.near"] != "solver" || roles["oneoff.near"] != "user" {
		t.Fatalf("5 observations must promote: %v", roles)
	}
}

func TestClassifySignersWithdrawalFallback(t *testing.T) {
	solvers := NewSolverSet()
	withdrawer := mustMsg("user.near", `{"a":"-5","b":"5"}`)
	withdrawer.Kinds = append(withdrawer.Kinds, "ft_withdraw")
	msgs := []SignedMsg{withdrawer, mustMsg("counterparty.near", `{"a":"5","b":"-5"}`)}
	roles := ClassifySigners(msgs, solvers)
	if roles["user.near"] != "user" || roles["counterparty.near"] != "solver" {
		t.Fatalf("withdrawal fallback: %v", roles)
	}
}

func TestClassifySignersNoSignalIsUnknown(t *testing.T) {
	solvers := NewSolverSet()
	msgs := []SignedMsg{
		mustMsg("a.near", `{"x":"1","y":"-1"}`),
		mustMsg("b.near", `{"x":"-1","y":"1"}`),
	}
	roles := ClassifySigners(msgs, solvers)
	if roles["a.near"] != "unknown" || roles["b.near"] != "unknown" {
		t.Fatalf("no signal must surface unknown, not guess: %v", roles)
	}
}

func TestNumericDiffAmountsTolerated(t *testing.T) {
	// Amounts are normally u128 strings but a plain JSON number must parse too.
	msgs, err := ParseSigned(buildArgsNEP413(t, tokenDiffMsg("s.near", `{"a":100,"b":-100}`)))
	if err != nil {
		t.Fatal(err)
	}
	legs := ExtractSolverLegs(msgs, map[string]string{"s.near": "solver"})
	if len(legs) != 1 || legs[0].RecvAmount.String() != "100" {
		t.Fatalf("numeric amounts should parse: %+v", legs)
	}
}

func mustMsg(signer, diff string) SignedMsg {
	raw := fmt.Sprintf(`{"signer_id":%q,"intents":[{"intent":"token_diff","diff":%s}]}`, signer, diff)
	return messageToSigned([]byte(raw))
}

var _ = base64.StdEncoding // keep import if helpers change
