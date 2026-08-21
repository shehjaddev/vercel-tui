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

func (c *Client) get(path string, query url.Values, out any) error {
	body, status, err := c.getRaw(c.baseURL + path + "?" + query.Encode())
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("GET %s: %d %s", path, status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// request performs a write call (POST/PATCH/DELETE) with an optional
// JSON body, sharing the 429 backoff with reads.
func (c *Client) request(method, path string, query url.Values, body any, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	var lastBody []byte
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<uint(attempt-1)) * time.Second)
		}
		req, err := http.NewRequest(method, c.baseURL+path+"?"+query.Encode(), bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			lastBody = bodyBytes
			continue
		}
		if resp.StatusCode >= 400 {
			return apiError(method, path, resp.StatusCode, bodyBytes)
		}
		if out != nil && len(bodyBytes) > 0 {
			return json.Unmarshal(bodyBytes, out)
		}
		return nil
	}
	_ = lastBody
	return ErrThrottled
}

func apiError(method, path string, status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	return fmt.Errorf("%s %s: %d %s", method, path, status, msg)
}

// getRaw performs the GET with 429 backoff and returns the raw body.
func (c *Client) getRaw(url string) ([]byte, int, error) {
	var lastBody []byte
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<uint(attempt-1)) * time.Second)
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, 0, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			lastBody = body
			continue
		}
		return body, resp.StatusCode, nil
	}
	return lastBody, http.StatusTooManyRequests, ErrThrottled
}

func scoped(q url.Values, teamID string) url.Values {
	if teamID != "" {
		q.Set("teamId", teamID)
	}
	return q
}
