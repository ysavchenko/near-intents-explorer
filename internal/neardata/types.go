// Package neardata fetches finalized NEAR blocks from FASTNEAR's neardata.xyz
// block server (the NEAR Lake "streamer message" JSON shape, with tx_hash
// conveniently added to receipt execution outcomes).
package neardata

import (
	"encoding/json"
	"strconv"
)

// Block is one streamer message: the block header plus every shard's chunk
// transactions and receipt execution outcomes.
type Block struct {
	Block struct {
		Header struct {
			Height           int64   `json:"height"`
			Hash             string  `json:"hash"`
			TimestampNanosec FlexInt `json:"timestamp_nanosec"`
			Timestamp        FlexInt `json:"timestamp"`
		} `json:"header"`
	} `json:"block"`
	Shards []Shard `json:"shards"`
}

// TimestampNs returns the block time in nanoseconds since epoch.
func (b *Block) TimestampNs() int64 {
	if b.Block.Header.TimestampNanosec != 0 {
		return int64(b.Block.Header.TimestampNanosec)
	}
	return int64(b.Block.Header.Timestamp)
}

func (b *Block) Height() int64 { return b.Block.Header.Height }

type Shard struct {
	ShardID json.Number `json:"shard_id"`
	// Chunk is null when this shard produced no new chunk in this block —
	// unlike raw RPC, Lake never repeats a stale chunk, so no height_included
	// filtering is needed.
	Chunk                    *Chunk                    `json:"chunk"`
	ReceiptExecutionOutcomes []ReceiptExecutionOutcome `json:"receipt_execution_outcomes"`
}

type Chunk struct {
	Transactions []IndexerTx `json:"transactions"`
}

type IndexerTx struct {
	Transaction Transaction `json:"transaction"`
	Outcome     TxOutcome   `json:"outcome"`
}

type Transaction struct {
	SignerID   string            `json:"signer_id"`
	ReceiverID string            `json:"receiver_id"`
	Hash       string            `json:"hash"`
	Actions    []json.RawMessage `json:"actions"`
}

type TxOutcome struct {
	ExecutionOutcome ExecutionOutcome `json:"execution_outcome"`
}

type ExecutionOutcome struct {
	ID      string `json:"id"`
	Outcome struct {
		ExecutorID string          `json:"executor_id"`
		ReceiptIDs []string        `json:"receipt_ids"`
		Logs       []string        `json:"logs"`
		Status     json.RawMessage `json:"status"`
	} `json:"outcome"`
}

type ReceiptExecutionOutcome struct {
	ExecutionOutcome ExecutionOutcome `json:"execution_outcome"`
	Receipt          struct {
		ReceiptID     string `json:"receipt_id"`
		PredecessorID string `json:"predecessor_id"`
		ReceiverID    string `json:"receiver_id"`
		Receipt       struct {
			Action *struct {
				SignerID string            `json:"signer_id"`
				Actions  []json.RawMessage `json:"actions"`
			} `json:"Action"`
		} `json:"receipt"`
	} `json:"receipt"`
	TxHash string `json:"tx_hash"` // neardata addition
}

// Actions returns the receipt's actions (empty for data receipts).
func (r *ReceiptExecutionOutcome) Actions() []json.RawMessage {
	if r.Receipt.Receipt.Action == nil {
		return nil
	}
	return r.Receipt.Receipt.Action.Actions
}

// StatusSucceeded reports whether an execution outcome status is a success
// (SuccessValue / SuccessReceiptId — anything without a Failure key).
func StatusSucceeded(status json.RawMessage) bool {
	if len(status) == 0 {
		return true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(status, &m); err != nil {
		return true // non-object status (e.g. a bare string) carries no failure
	}
	_, failed := m["Failure"]
	return !failed
}

// FunctionCall is the one action shape we care about.
type FunctionCall struct {
	MethodName string `json:"method_name"`
	Args       string `json:"args"` // base64 JSON
}

// AsFunctionCall decodes an action if it is a FunctionCall, else returns false.
func AsFunctionCall(action json.RawMessage) (FunctionCall, bool) {
	var wrapper struct {
		FunctionCall *FunctionCall `json:"FunctionCall"`
	}
	if err := json.Unmarshal(action, &wrapper); err != nil || wrapper.FunctionCall == nil {
		return FunctionCall{}, false
	}
	return *wrapper.FunctionCall, true
}

// FlexInt tolerates both JSON numbers and numeric strings (the header carries
// timestamp as a number and timestamp_nanosec as a string).
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	s := string(b)
	if s[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*f = FlexInt(n)
	return nil
}
