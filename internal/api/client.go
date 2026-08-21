package api

import (
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
	body, status, err := c.getRaw(c.baseURL+path+"?"+query.Encode())
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("GET %s: %d %s", path, status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
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
