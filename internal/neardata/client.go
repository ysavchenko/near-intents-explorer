package neardata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client fetches blocks from a neardata.xyz-compatible server.
//
// Verified server semantics (2026-07):
//   - serves finalized blocks only;
//   - requesting the next unproduced height long-polls until finalized;
//   - a skipped height returns JSON null;
//   - free tier is 180 req/min/IP (tail-following needs ~100).
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		// Generous timeout: the next-height request long-polls until the block
		// is finalized (normally <1s, but allow for hiccups).
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// ErrSkipped marks a height NEAR skipped (server returned null).
var ErrSkipped = fmt.Errorf("height skipped")

// Block fetches one finalized block by height. Returns ErrSkipped for skipped
// heights. Retries transient errors (429/5xx/network) with backoff until the
// context is cancelled.
func (c *Client) Block(ctx context.Context, height int64) (*Block, error) {
	url := fmt.Sprintf("%s/v0/block/%d", c.baseURL, height)
	return c.fetch(ctx, url)
}

// Head fetches the current chain head (final block). The server answers with a
// 302 to the concrete block URL; the default client follows it.
func (c *Client) Head(ctx context.Context) (*Block, error) {
	return c.fetch(ctx, c.baseURL+"/v0/last_block/final")
}

func (c *Client) fetch(ctx context.Context, url string) (*Block, error) {
	if c.apiKey != "" {
		url += "?apiKey=" + c.apiKey
	}
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err := sleep(ctx, backoff); err != nil {
				return nil, err
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && readErr == nil:
			if isJSONNull(body) {
				return nil, ErrSkipped
			}
			var b Block
			if err := json.Unmarshal(body, &b); err != nil {
				return nil, fmt.Errorf("decode block: %w", err)
			}
			return &b, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			if err := sleep(ctx, max(backoff, 5*time.Second)); err != nil {
				return nil, err
			}
		case resp.StatusCode >= 500 || readErr != nil:
			if err := sleep(ctx, backoff); err != nil {
				return nil, err
			}
		default:
			// 4xx other than 429: transient server quirks happen; retry a few
			// times, then surface (the follower logs and keeps retrying).
			if attempt >= 5 {
				return nil, fmt.Errorf("GET %s: HTTP %d: %.200s", url, resp.StatusCode, body)
			}
			if err := sleep(ctx, backoff); err != nil {
				return nil, err
			}
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func isJSONNull(body []byte) bool {
	for _, c := range body {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		break
	}
	trimmed := string(body)
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\n' || trimmed[0] == '\t' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	return trimmed == "null" || trimmed == ""
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
