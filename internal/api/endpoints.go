package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

func (c *Client) Deployments(projectID, teamID, target string, limit int) ([]Deployment, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if projectID != "" {
		q.Set("project", projectID)
	}
	if target != "" {
		q.Set("target", target)
	}
	var out struct {
		Deployments []Deployment `json:"deployments"`
	}
	if err := c.get("/v6/deployments", scoped(q, teamID), &out); err != nil {
		return nil, err
	}
	return out.Deployments, nil
}

func (c *Client) Deployment(id, teamID string) (*Deployment, error) {
	var d Deployment
	if err := c.get("/v13/deployments/"+id, scoped(url.Values{}, teamID), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Events returns build log events for a deployment, oldest first.
func (c *Client) Events(id, teamID string) ([]Event, error) {
	q := url.Values{"limit": {"1000"}, "builds": {"1"}, "direction": {"forward"}}
	path := "/v2/deployments/" + id + "/events?" + scoped(q, teamID).Encode()
	req := c.baseURL + path
	body, status, err := c.getRaw(req)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("GET %s: %d", path, status)
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

func (c *Client) Projects(teamID string, limit int) ([]Project, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	var out struct {
		Projects []Project `json:"projects"`
	}
	if err := c.get("/v9/projects", scoped(q, teamID), &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

func (c *Client) Teams() ([]Team, error) {
	var out struct {
		Teams []Team `json:"teams"`
	}
	if err := c.get("/v2/teams", url.Values{"limit": {"100"}}, &out); err != nil {
		return nil, err
	}
	return out.Teams, nil
}

func (c *Client) User() (*User, error) {
	var out struct {
		User User `json:"user"`
	}
	if err := c.get("/v2/user", url.Values{}, &out); err != nil {
		return nil, err
	}
	return &out.User, nil
}
