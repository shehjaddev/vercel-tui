package api

import (
	"context"
	"fmt"
	"net/url"
)

// CancelDeployment aborts an in-progress build.
func (c *Client) CancelDeployment(ctx context.Context, id, teamID string) (*Deployment, error) {
	var d Deployment
	// Vercel returns 415 without an explicit empty JSON body.
	if err := c.request(ctx, "POST", "/v12/deployments/"+id+"/cancel", scoped(url.Values{}, teamID), map[string]any{}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *Client) DeleteDeployment(ctx context.Context, id, teamID string) error {
	return c.request(ctx, "DELETE", "/v13/deployments/"+id, scoped(url.Values{}, teamID), nil, nil)
}

// GitSource identifies a git-connected repo for redeployments.
type GitSource struct {
	Type string `json:"type"`
	Org  string `json:"org"`
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
}

// Redeploy rebuilds the same git commit as an existing deployment.
// Git-connected projects require the full gitSource; pass nil for
// deployments without git metadata. Pass target="production" to keep
// a production redeploy in production (API defaults to preview).
func (c *Client) Redeploy(ctx context.Context, name, deploymentID, teamID string, git *GitSource, target string) (*Deployment, error) {
	body := map[string]any{"name": name, "deploymentId": deploymentID}
	if git != nil {
		body["gitSource"] = git
	}
	if target != "" {
		body["target"] = target
	}
	var d Deployment
	if err := c.request(ctx, "POST", "/v13/deployments", scoped(url.Values{}, teamID), body, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Promote performs an instant rollback by making an existing ready
// production deployment the current production deployment again.
func (c *Client) Promote(ctx context.Context, projectID, deploymentID, teamID string) error {
	return c.request(ctx, "POST", fmt.Sprintf("/v10/projects/%s/promote/%s", projectID, deploymentID), scoped(url.Values{}, teamID), map[string]any{}, nil)
}

// ProjectByName resolves a project id from its name; needed because the
// deployments list only carries the name.
func (c *Client) ProjectByName(ctx context.Context, name, teamID string) (*Project, error) {
	q := url.Values{"name": {name}, "limit": {"1"}}
	var out struct {
		Projects []Project `json:"projects"`
	}
	if err := c.get(ctx, "/v9/projects", scoped(q, teamID), &out); err != nil {
		return nil, err
	}
	if len(out.Projects) == 0 {
		return nil, fmt.Errorf("project %q not found", name)
	}
	return &out.Projects[0], nil
}
