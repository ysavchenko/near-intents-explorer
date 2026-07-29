// Package nearrpc is a minimal NEAR JSON-RPC client for contract view calls
// (the block follower uses neardata.xyz instead; this is only for on-demand
// state queries like solver balances).
package nearrpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	URL  string
	HTTP *http.Client
}

func New(url string) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// CallFunction executes a view call and returns the raw function result bytes
// (usually JSON).
func (c *Client) CallFunction(ctx context.Context, accountID, method string, args any) ([]byte, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": "query",
		"params": map[string]any{
			"request_type": "call_function",
			"finality":     "final",
			"account_id":   accountID,
			"method_name":  method,
			"args_base64":  base64.StdEncoding.EncodeToString(argsJSON),
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %.200s", c.URL, resp.StatusCode, raw)
	}
	// result.result is a JSON array of byte values, not a base64 string, so it
	// cannot unmarshal into []byte directly.
	var rpcResp struct {
		Result struct {
			Result []int  `json:"result"`
			Error  string `json:"error"`
		} `json:"result"`
		Error *struct {
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("rpc response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc: %s %s", rpcResp.Error.Message, rpcResp.Error.Data)
	}
	if rpcResp.Result.Error != "" {
		return nil, fmt.Errorf("%s.%s: %s", accountID, method, rpcResp.Result.Error)
	}
	out := make([]byte, len(rpcResp.Result.Result))
	for i, b := range rpcResp.Result.Result {
		out[i] = byte(b)
	}
	return out, nil
}
