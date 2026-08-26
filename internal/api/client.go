package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBase = "https://api.vercel.com"

// ErrThrottled is returned when the API keeps answering 429 after retries.
var ErrThrottled = errors.New("throttled by vercel api")

type Client struct {
	http    *http.Client
	token   string
	baseURL string
}

func New(token string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		token:   token,
		baseURL: defaultBase,
	}
}

// withQuery appends "?query" only when there is one.
func withQuery(path string, q url.Values) string {
	if enc := q.Encode(); enc != "" {
		return path + "?" + enc
	}
	return path
}

func (c *Client) get(path string, query url.Values, out any) error {
	body, err := c.do(http.MethodGet, c.baseURL+withQuery(path, query), nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// request performs a write call with an optional JSON body.
func (c *Client) request(method, path string, query url.Values, body any, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	bodyBytes, err := c.do(method, c.baseURL+withQuery(path, query), payload)
	if err != nil {
		return err
	}
	if out != nil && len(bodyBytes) > 0 {
		return json.Unmarshal(bodyBytes, out)
	}
	return nil
}

// do performs a request with 429 backoff and returns the raw response
// body. Non-2xx responses become errors.
func (c *Client) do(method, fullURL string, payload []byte) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<uint(attempt-1)) * time.Second)
		}
		req, err := http.NewRequest(method, fullURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, apiError(method, fullURL, resp.StatusCode, body)
		}
		return body, nil
	}
	return nil, ErrThrottled
}

func apiError(method, url string, status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	return fmt.Errorf("%s %s: %d %s", method, urlPath(url), status, msg)
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
