package intents

import (
	"testing"
)

// Fixtures below are verbatim from mainnet receipts (blocks ~210321572+).

const mintLog = `EVENT_JSON:{"standard":"nep245","version":"1.0.0","event":"mt_mint","data":[{"owner_id":"27695d3e216982e4b32db97ca046ae31b1feb354710e65f0f420859bdbcd8e2c","token_ids":["nep141:tron-d28a265909efecdcee7c5028585214ea0b96f015.omft.near"],"amounts":["349998500000"],"memo":"deposit"}]}`

const burnMultiLog = `EVENT_JSON:{"standard":"nep245","version":"1.0.0","event":"mt_burn","data":[{"owner_id":"3f3766998b9475fd82523d825c7dc8f16bd2a934a4b61ce454f2b877c33ac4eb","token_ids":["nep245:v2_1.omni.hot.tg:56_2CMMyVTGZkeyNZTSvS5sarzfir6g","nep245:v2_1.omni.hot.tg:56_11111111111111111111"],"amounts":["50000000000000000000000","13000000000000"],"memo":null}]}`

const transferLog = `EVENT_JSON:{"standard":"nep245","version":"1.0.0","event":"mt_transfer","data":[{"old_owner_id":"solver-priv-liq.near","new_owner_id":"7066024d3f20f94de601c003163367873cca78507eeca4df66d9be645f197f05","token_ids":["nep245:v2_1.omni.hot.tg:56_11111111111111111111"],"amounts":["13000014"]}]}`

func TestParseNEP245Events(t *testing.T) {
	rows := ParseNEP245Events([]string{
		"plain log line",
		mintLog,
		burnMultiLog,
		transferLog,
		`EVENT_JSON:{"standard":"nep141","event":"ft_transfer","data":[{}]}`, // wrong standard
		"EVENT_JSON:not-json",
	})
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != "mint" || rows[0].Owner != "27695d3e216982e4b32db97ca046ae31b1feb354710e65f0f420859bdbcd8e2c" ||
		rows[0].AssetID != "nep141:tron-d28a265909efecdcee7c5028585214ea0b96f015.omft.near" ||
		rows[0].Amount.String() != "349998500000" || rows[0].Memo != "deposit" {
		t.Errorf("mint row mismatch: %+v", rows[0])
	}
	if rows[1].Kind != "burn" || rows[1].AssetID != "nep245:v2_1.omni.hot.tg:56_2CMMyVTGZkeyNZTSvS5sarzfir6g" ||
		rows[1].Amount.String() != "50000000000000000000000" {
		t.Errorf("burn row 0 mismatch: %+v", rows[1])
	}
	if rows[2].Kind != "burn" || rows[2].AssetID != "nep245:v2_1.omni.hot.tg:56_11111111111111111111" {
		t.Errorf("burn row 1 mismatch: %+v", rows[2])
	}
	if rows[3].Kind != "transfer" || rows[3].From != "solver-priv-liq.near" ||
		rows[3].To != "7066024d3f20f94de601c003163367873cca78507eeca4df66d9be645f197f05" {
		t.Errorf("transfer row mismatch: %+v", rows[3])
	}
}

func TestMessageToSignedTransferAndWithdrawals(t *testing.T) {
	// The HOT-bridge pattern from tx 3FcVPj4vdMYbSGMfnq8wEzQnA7YawH2GnvEw7gwdnzNn:
	// solver signs a plain transfer to a helper, the helper signs mt_withdraw.
	solverMsg := `{"signer_id":"27695d3e216982e4b32db97ca046ae31b1feb354710e65f0f420859bdbcd8e2c","intents":[{"intent":"transfer","receiver_id":"3f3766998b9475fd82523d825c7dc8f16bd2a934a4b61ce454f2b877c33ac4eb","tokens":{"nep245:v2_1.omni.hot.tg:56_2CMMyVTGZkeyNZTSvS5sarzfir6g":"50000007694007694007695"}}]}`
	m := messageToSigned([]byte(solverMsg))
	if len(m.Transfers) != 1 {
		t.Fatalf("want 1 transfer, got %+v", m.Transfers)
	}
	tr := m.Transfers[0]
	if tr.Receiver != "3f3766998b9475fd82523d825c7dc8f16bd2a934a4b61ce454f2b877c33ac4eb" ||
		len(tr.Tokens) != 1 || tr.Tokens[0].Amount.String() != "50000007694007694007695" {
		t.Errorf("transfer mismatch: %+v", tr)
	}
	if m.TakerMarked() {
		t.Error("plain transfer must not taker-mark the signer")
	}

	helperMsg := `{"signer_id":"3f3766998b9475fd82523d825c7dc8f16bd2a934a4b61ce454f2b877c33ac4eb","intents":[{"intent":"token_diff","diff":{"nep245:v2_1.omni.hot.tg:56_2CMMyVTGZkeyNZTSvS5sarzfir6g":"-7694007694007695","nep245:v2_1.omni.hot.tg:56_11111111111111111111":"13000000000000"}},{"intent":"mt_withdraw","receiver_id":"bridge-refuel.hot.tg","amounts":["50000000000000000000000","13000000000000"],"token_ids":["56_2CMMyVTGZkeyNZTSvS5sarzfir6g","56_11111111111111111111"],"token":"v2_1.omni.hot.tg","msg":"{\"receiver_id\":\"3kWFo94a1K1pQpSsDLAhMiY3viu1\",\"amount_native\":\"13000000000000\",\"block_number\":114559184}"}]}`
	h := messageToSigned([]byte(helperMsg))
	if !h.TakerMarked() || !h.HasWithdrawal() {
		t.Error("mt_withdraw must taker-mark the signer")
	}
	if len(h.Withdrawals) != 1 {
		t.Fatalf("want 1 withdrawal, got %+v", h.Withdrawals)
	}
	wd := h.Withdrawals[0]
	if !wd.Covers("nep245:v2_1.omni.hot.tg:56_2CMMyVTGZkeyNZTSvS5sarzfir6g") ||
		!wd.Covers("nep245:v2_1.omni.hot.tg:56_11111111111111111111") {
		t.Errorf("mt_withdraw asset ids mismatch: %+v", wd.AssetIDs)
	}
	// HOT bridge destination: base58 msg receiver decodes to the BSC address.
	if got := wd.ExternalAddress(); got != "0xc565b3db8527fe7f7f89b630886ec348ae1eb994" {
		t.Errorf("HOT external address = %q", got)
	}
}

func TestWithdrawalDestinations(t *testing.T) {
	// PoA bridge: WITHDRAW_TO memo wins.
	poa := messageToSigned([]byte(`{"signer_id":"s.near","intents":[{"intent":"ft_withdraw","token":"eth.omft.near","receiver_id":"eth.omft.near","amount":"1230638285260197582","memo":"WITHDRAW_TO:0x45D5Aa294dfbA3f36eb85e66b8B04D8360d81a7a"}]}`))
	if len(poa.Withdrawals) != 1 {
		t.Fatalf("want 1 withdrawal, got %+v", poa.Withdrawals)
	}
	w := poa.Withdrawals[0]
	if !w.Covers("nep141:eth.omft.near") {
		t.Errorf("asset ids: %+v", w.AssetIDs)
	}
	if got := w.ExternalAddress(); got != "0x45D5Aa294dfbA3f36eb85e66b8B04D8360d81a7a" {
		t.Errorf("WITHDRAW_TO address = %q", got)
	}

	// NEAR-internal withdrawal: receiver only, no external address.
	ref := messageToSigned([]byte(`{"signer_id":"solver-ref.near","intents":[{"intent":"ft_withdraw","token":"zec.omft.near","receiver_id":"v2.ref-finance.near","amount":"872656","memo":"rebalance","msg":"{\"force\":0}"}]}`))
	if got := ref.Withdrawals[0].ExternalAddress(); got != "" {
		t.Errorf("NEAR-internal withdrawal external address = %q, want empty", got)
	}
	if ref.Withdrawals[0].Receiver != "v2.ref-finance.near" {
		t.Errorf("receiver = %q", ref.Withdrawals[0].Receiver)
	}

	// native_withdraw burns the wNEAR balance.
	nat := messageToSigned([]byte(`{"signer_id":"s.near","intents":[{"intent":"native_withdraw","receiver_id":"someone.near","amount":"1000000000000000000000000"}]}`))
	if !nat.Withdrawals[0].Covers("nep141:wrap.near") {
		t.Errorf("native_withdraw assets: %+v", nat.Withdrawals[0].AssetIDs)
	}
}

func TestDecodeBridgeAddr(t *testing.T) {
	// 20-byte payload -> EVM hex.
	if got := decodeBridgeAddr("3kWFo94a1K1pQpSsDLAhMiY3viu1"); got != "0xc565b3db8527fe7f7f89b630886ec348ae1eb994" {
		t.Errorf("EVM decode = %q", got)
	}
	// 32-byte payload (Solana pubkey) stays base58.
	sol := "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"
	if got := decodeBridgeAddr(sol); got != sol {
		t.Errorf("Solana decode = %q, want unchanged", got)
	}
	// Non-base58 input stays as-is.
	if got := decodeBridgeAddr("not/base58!"); got != "not/base58!" {
		t.Errorf("junk decode = %q", got)
	}
}

func TestBase58Decode(t *testing.T) {
	b, err := base58Decode("3kWFo94a1K1pQpSsDLAhMiY3viu1")
	if err != nil || len(b) != 20 {
		t.Fatalf("decode: len=%d err=%v", len(b), err)
	}
	// Leading '1's are zero bytes.
	b, err = base58Decode("11z")
	if err != nil || len(b) != 3 || b[0] != 0 || b[1] != 0 {
		t.Errorf("leading-ones decode = %v (%v)", b, err)
	}
	if _, err := base58Decode("0OIl"); err == nil {
		t.Error("want error for invalid alphabet")
	}
}

func TestParseDepositOrigin(t *testing.T) {
	log := `EVENT_JSON:{"standard":"nep141","version":"1.0.0","event":"ft_transfer","data":[{"old_owner_id":"omft.near","new_owner_id":"intents.near","amount":"349998500000","memo":"BRIDGED_FROM:{\"networkType\":\"tron\",\"chainId\":\"mainnet\",\"txHash\":\"d13692c18c81512029a41c388424795add2f11f6fcb824413d4df55e2240f291\"}"}]}`
	o := ParseDepositOrigin([]string{"noise", log})
	if o == nil || o.Network != "tron" ||
		o.TxHash != "d13692c18c81512029a41c388424795add2f11f6fcb824413d4df55e2240f291" {
		t.Fatalf("origin = %+v", o)
	}
	if ParseDepositOrigin([]string{"nothing here"}) != nil {
		t.Error("want nil for logs without BRIDGED_FROM")
	}
}

func TestWithdrawalFromArgs(t *testing.T) {
	w := WithdrawalFromArgs("ft_withdraw", []byte(`{"token":"eth.omft.near","receiver_id":"eth.omft.near","amount":"5","memo":"WITHDRAW_TO:0xAbC"}`))
	if w == nil || w.ExternalAddress() != "0xAbC" || !w.Covers("nep141:eth.omft.near") {
		t.Errorf("direct ft_withdraw: %+v", w)
	}
	if WithdrawalFromArgs("mt_transfer", []byte(`{}`)) != nil {
		t.Error("non-withdrawal method must return nil")
	}
}

func TestOnTransferSender(t *testing.T) {
	if got := OnTransferSender([]byte(`{"sender_id":"omft.near","amount":"1","msg":"{}"}`)); got != "omft.near" {
		t.Errorf("sender = %q", got)
	}
}
