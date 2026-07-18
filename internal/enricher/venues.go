package enricher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// 1-minute reference prices from Binance and Hyperliquid, reduced to a
// per-minute mid = (high + low) / 2 keyed by minute-open epoch ms (the same
// proxy the reference pipeline used). Stablecoins are pinned to 1.0 by the
// caller, never fetched.

const (
	binanceURL = "https://data-api.binance.vision/api/v3/klines"
	hlURL      = "https://api.hyperliquid.xyz/info"
	minMs      = 60_000
	binanceMax = 1000
	retries    = 3
)

// ErrInvalidSymbol means the venue does not list this symbol — the caller
// flags it no_reference.
var ErrInvalidSymbol = errors.New("venue does not list symbol")

// MidsFetcher fetches per-minute mids for [startMs, endMs]. venue-specific
// symbol spelling (e.g. ZECUSDT vs ZEC) is the caller's concern.
type MidsFetcher func(ctx context.Context, symbol string, startMs, endMs int64) (map[int64]float64, error)

var venueHTTP = &http.Client{Timeout: 30 * time.Second}

// BinanceMids pages through 1m klines. Any 400 is a symbol problem (unlisted /
// illegal characters) → ErrInvalidSymbol; transient errors are retried.
func BinanceMids(ctx context.Context, symbol string, startMs, endMs int64) (map[int64]float64, error) {
	out := map[int64]float64{}
	cur := startMs
	for cur <= endMs {
		url := fmt.Sprintf("%s?symbol=%s&interval=1m&startTime=%d&endTime=%d&limit=%d",
			binanceURL, symbol, cur, endMs, binanceMax)
		resp, err := getRetry(ctx, url)
		if err != nil {
			return nil, err
		}
		if resp.status == http.StatusBadRequest {
			return nil, ErrInvalidSymbol
		}
		if resp.status != http.StatusOK {
			return nil, fmt.Errorf("binance %s: HTTP %d %.200s", symbol, resp.status, resp.body)
		}
		// kline row: [openTime, open, high, low, close, ...] — numbers as strings.
		var data [][]json.RawMessage
		if err := json.Unmarshal(resp.body, &data); err != nil {
			return nil, fmt.Errorf("binance %s: %w", symbol, err)
		}
		if len(data) == 0 {
			break
		}
		var lastOpen int64
		for _, k := range data {
			if len(k) < 4 {
				continue
			}
			var openTime int64
			var high, low stringFloat
			if err := json.Unmarshal(k[0], &openTime); err != nil {
				continue
			}
			if err := json.Unmarshal(k[2], &high); err != nil {
				continue
			}
			if err := json.Unmarshal(k[3], &low); err != nil {
				continue
			}
			out[openTime] = (float64(high) + float64(low)) / 2.0
			lastOpen = openTime
		}
		if len(data) < binanceMax {
			break
		}
		cur = lastOpen + minMs
	}
	if len(out) == 0 {
		return nil, ErrInvalidSymbol
	}
	return out, nil
}

// HyperliquidMids pages 1m perp candles. Persistent 5xx = unknown coin (HL
// returns 500 for them) → ErrInvalidSymbol.
func HyperliquidMids(ctx context.Context, coin string, startMs, endMs int64) (map[int64]float64, error) {
	out := map[int64]float64{}
	cur := startMs
	for cur <= endMs {
		body, _ := json.Marshal(map[string]any{
			"type": "candleSnapshot",
			"req": map[string]any{
				"coin": coin, "interval": "1m",
				"startTime": cur, "endTime": endMs,
			},
		})
		resp, err := postRetry(ctx, hlURL, body)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, ErrInvalidSymbol // persistent 5xx
		}
		if resp.status != http.StatusOK {
			return nil, fmt.Errorf("hyperliquid %s: HTTP %d %.200s", coin, resp.status, resp.body)
		}
		data, err := hlParseCandles(resp.body)
		if err != nil || len(data) == 0 {
			break
		}
		for _, c := range data {
			out[c.T] = (float64(c.H) + float64(c.L)) / 2.0
		}
		last := data[len(data)-1].T
		if last < cur || len(data) < 5000 {
			break
		}
		cur = last + minMs
	}
	if len(out) == 0 {
		return nil, ErrInvalidSymbol
	}
	return out, nil
}

// hlCandle binds both "t" (open ms) and "T" (close ms). Both fields must be
// declared: with only "t" tagged, Go's case-insensitive JSON matching lets the
// payload's "T" key clobber the open time — every mid then lands on a
// close-time key and no minute lookup ever hits.
type hlCandle struct {
	T      int64       `json:"t"`
	CloseT int64       `json:"T"`
	H      stringFloat `json:"h"`
	L      stringFloat `json:"l"`
}

func hlParseCandles(body []byte) ([]hlCandle, error) {
	var data []hlCandle
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return data, nil
}

type httpResp struct {
	status int
	body   []byte
}

func getRetry(ctx context.Context, url string) (*httpResp, error) {
	var last error
	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		r, err := venueHTTP.Do(req)
		if err != nil {
			last = err
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body.Close()
		if r.StatusCode != http.StatusTooManyRequests && r.StatusCode < 500 {
			return &httpResp{status: r.StatusCode, body: body}, nil
		}
		last = fmt.Errorf("HTTP %d", r.StatusCode)
		if err := sleepCtx(ctx, backoff(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("GET %s failed: %w", url, last)
}

// postRetry retries network errors and 5xx; a persistent 5xx returns (nil,
// nil) so the caller can flag no_reference (mirrors prices.py::_post).
func postRetry(ctx context.Context, url string, body []byte) (*httpResp, error) {
	var last error
	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		r, err := venueHTTP.Do(req)
		if err != nil {
			last = err
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body.Close()
		if r.StatusCode < 500 {
			return &httpResp{status: r.StatusCode, body: respBody}, nil
		}
		last = fmt.Errorf("HTTP %d", r.StatusCode)
		if err := sleepCtx(ctx, backoff(attempt)); err != nil {
			return nil, err
		}
	}
	_ = last
	return nil, nil
}

func backoff(attempt int) time.Duration {
	return time.Duration(attempt) * 500 * time.Millisecond
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// stringFloat decodes JSON numbers that may arrive as strings ("123.4").
type stringFloat float64

func (f *stringFloat) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		b = []byte(s)
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = stringFloat(v)
	return nil
}
