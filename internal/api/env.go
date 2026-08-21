package api

import (
	"net/url"
)

type EnvVar struct {
	ID        string   `json:"id"`
	Key       string   `json:"key"`
	Target    []string `json:"target"`
	Type      string   `json:"type"` // "encrypted" or "sensitive"
	CreatedAt msTime   `json:"createdAt"`
	UpdatedAt msTime   `json:"updatedAt"`
}

func (e EnvVar) Sensitive() bool { return e.Type == "sensitive" }

func (c *Client) EnvVars(projectID, teamID string) ([]EnvVar, error) {
	var out struct {
		Envs []EnvVar `json:"envs"`
	}
	if err := c.get("/v10/projects/"+projectID+"/env", scoped(url.Values{}, teamID), &out); err != nil {
		return nil, err
	}
	return out.Envs, nil
}

// CreateEnv stores a new environment variable; sensitive values are
// write-only through the API and never come back readable.
func (c *Client) CreateEnv(projectID, teamID, key, value string, targets []string) error {
	body := map[string]any{
		"key":    key,
		"value":  value,
		"type":   "encrypted",
		"target": targets,
	}
	return c.request("POST", "/v10/projects/"+projectID+"/env", scoped(url.Values{}, teamID), body, nil)
}

func (c *Client) UpdateEnvValue(projectID, teamID, envID, value string, targets []string) error {
	q := url.Values{"upsert": {"true"}}
	body := map[string]any{"value": value}
	if len(targets) > 0 {
		body["target"] = targets
	}
	return c.request("PATCH", "/v10/projects/"+projectID+"/env/"+envID, scoped(q, teamID), body, nil)
}

func (c *Client) DeleteEnv(projectID, teamID, envID string) error {
	return c.request("DELETE", "/v10/projects/"+projectID+"/env/"+envID, scoped(url.Values{}, teamID), nil, nil)
}

type Domain struct {
	Name      string `json:"name"`
	Verified  bool   `json:"verified"`
	CreatedAt msTime `json:"createdAt"`
}

func (c *Client) ProjectDomains(projectID, teamID string) ([]Domain, error) {
	var out struct {
		Domains []Domain `json:"domains"`
	}
	if err := c.get("/v9/projects/"+projectID+"/domains", scoped(url.Values{}, teamID), &out); err != nil {
		return nil, err
	}
	return out.Domains, nil
}

func (c *Client) TeamDomains(teamID string) ([]Domain, error) {
	var out struct {
		Domains []Domain `json:"domains"`
	}
	if err := c.get("/v5/domains", scoped(url.Values{"limit": {"100"}}, teamID), &out); err != nil {
		return nil, err
	}
	return out.Domains, nil
}
