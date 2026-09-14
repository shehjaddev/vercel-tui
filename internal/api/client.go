package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const defaultBase = "https://api.vercel.com"

// ErrThrottled is returned when the API keeps answering 429 after retries.
var ErrThrottled = errors.New("throttled by vercel api")

type Client struct {
	http    *http.Client
	baseURL string

	mu      sync.Mutex // guards token and refresh
	token   string
	refresh func() (string, error) // optional; refreshes a stale OAuth token

	refreshMu sync.Mutex // serializes token exchanges
}

func New(token string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		token:   token,
		baseURL: defaultBase,
	}
}

// SetRefresh lets the client self-heal a stale OAuth access token: when the
// API answers 403, it asks for a new one and retries once.
func (c *Client) SetRefresh(fn func() (string, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh = fn
}

// credentials returns the token to send with the next request and the
// refresh hook, if one is set. Every command the UI batches runs in its own
// goroutine, so both move under the lock.
func (c *Client) credentials() (string, func() (string, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, c.refresh
}

func (c *Client) setToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// refreshIfStale exchanges the access token that produced a 403 for a fresh
// one. Several calls can be rejected at the same moment, and the first one to
// get here does the exchange while the rest reuse its result: a second
// exchange would rotate the CLI's refresh token again and could leave both
// tools logged out.
func (c *Client) refreshIfStale(used string, refresh func() (string, error)) (string, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	current, _ := c.credentials()
	if current != used {
		return current, nil // another call already refreshed it
	}
	token, err := refresh()
	if err != nil {
		return "", err
	}
	c.setToken(token)
	return token, nil
}

// withQuery appends "?query" only when there is one.
func withQuery(path string, q url.Values) string {
	if enc := q.Encode(); enc != "" {
		return path + "?" + enc
	}
	return path
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	body, err := c.do(ctx, http.MethodGet, c.baseURL+withQuery(path, query), nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// request performs a write call with an optional JSON body.
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	bodyBytes, err := c.do(ctx, method, c.baseURL+withQuery(path, query), payload)
	if err != nil {
		return err
	}
	if out != nil && len(bodyBytes) > 0 {
		return json.Unmarshal(bodyBytes, out)
	}
	return nil
}

// do performs a request with 429 backoff and returns the raw response
// body. Non-2xx responses become errors. A stale OAuth token is refreshed
// at most once per call, and concurrent calls share a single exchange.
func (c *Client) do(ctx context.Context, method, fullURL string, payload []byte) ([]byte, error) {
	refreshed := false
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
		if err != nil {
			return nil, err
		}
		token, refresh := c.credentials()
		req.Header.Set("Authorization", "Bearer "+token)
		if len(payload) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		limit := int64(32 << 20)
		if method != http.MethodGet {
			limit = 1 << 20
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			continue
		}
		if resp.StatusCode >= 400 {
			if resp.StatusCode == http.StatusForbidden && !refreshed && refresh != nil {
				if _, rerr := c.refreshIfStale(token, refresh); rerr == nil {
					refreshed = true
					attempt-- // retry the same slot without burning 429 backoff
					continue
				}
			}
			return nil, apiError(method, fullURL, resp.StatusCode, body)
		}
		return body, nil
	}
	return nil, ErrThrottled
}

func apiError(method, rawURL string, status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	return fmt.Errorf("%s %s: %d %s", method, urlPath(rawURL), status, msg)
}

// urlPath strips scheme and host so error messages read like "GET /v6/...".
func urlPath(full string) string {
	for _, scheme := range []string{"https://", "http://"} {
		if rest, ok := strings.CutPrefix(full, scheme); ok {
			if i := strings.Index(rest, "/"); i >= 0 {
				return rest[i:]
			}
			return "/"
		}
	}
	return full
}

func scoped(q url.Values, teamID string) url.Values {
	if teamID != "" {
		q.Set("teamId", teamID)
	}
	return q
}
