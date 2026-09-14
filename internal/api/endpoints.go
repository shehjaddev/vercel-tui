package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// maxDeploymentPages bounds list fetching: 5 pages of 100 keeps the
// overview complete for large teams without hammering the rate limit.
const maxDeploymentPages = 5

func (c *Client) Deployments(ctx context.Context, projectID, teamID, target string, limit int) ([]Deployment, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var all []Deployment
	until := ""
	for page := 0; page < maxDeploymentPages; page++ {
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if projectID != "" {
			q.Set("projectId", projectID)
		}
		if target != "" {
			q.Set("target", target)
		}
		if until != "" {
			q.Set("until", until)
		}
		var out struct {
			Deployments []Deployment `json:"deployments"`
			Pagination  struct {
				Next msTime `json:"next"`
			} `json:"pagination"`
		}
		if err := c.get(ctx, "/v6/deployments", scoped(q, teamID), &out); err != nil {
			return nil, err
		}
		if len(out.Deployments) == 0 {
			break
		}
		all = append(all, out.Deployments...)
		next := strconv.FormatInt(int64(out.Pagination.Next), 10)
		if out.Pagination.Next == 0 || next == until {
			break
		}
		until = next
	}
	return all, nil
}

func (c *Client) Deployment(ctx context.Context, id, teamID string) (*Deployment, error) {
	var d Deployment
	if err := c.get(ctx, "/v13/deployments/"+id, scoped(url.Values{}, teamID), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Events returns build log events for a deployment, oldest first.
func (c *Client) Events(ctx context.Context, id, teamID string) ([]Event, error) {
	q := url.Values{"limit": {"1000"}, "builds": {"1"}, "direction": {"forward"}}
	body, err := c.do(ctx, http.MethodGet, c.baseURL+withQuery("/v2/deployments/"+id+"/events", scoped(q, teamID)), nil)
	if err != nil {
		return nil, err
	}
	return parseEvents(body)
}

// parseEvents handles both the JSON array and the NDJSON stream shapes
// the events endpoint has been seen to return.
func parseEvents(body []byte) ([]Event, error) {
	var arr []Event
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, nil
	}
	var events []Event
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

func (c *Client) Teams(ctx context.Context) ([]Team, error) {
	var out struct {
		Teams []Team `json:"teams"`
	}
	if err := c.get(ctx, "/v2/teams", url.Values{"limit": {"100"}}, &out); err != nil {
		return nil, err
	}
	return out.Teams, nil
}

func (c *Client) User(ctx context.Context) (*User, error) {
	var out struct {
		User User `json:"user"`
	}
	if err := c.get(ctx, "/v2/user", url.Values{}, &out); err != nil {
		return nil, err
	}
	return &out.User, nil
}
