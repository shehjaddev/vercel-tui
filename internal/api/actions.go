package api

import (
	"fmt"
	"net/url"
)

// CancelDeployment aborts an in-progress build.
func (c *Client) CancelDeployment(id, teamID string) (*Deployment, error) {
	var d Deployment
	if err := c.request("POST", "/v12/deployments/"+id+"/cancel", scoped(url.Values{}, teamID), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *Client) DeleteDeployment(id, teamID string) error {
	return c.request("DELETE", "/v13/deployments/"+id, scoped(url.Values{}, teamID), nil, nil)
}

// Redeploy rebuilds the same git commit as an existing deployment.
func (c *Client) Redeploy(name, deploymentID, teamID string) (*Deployment, error) {
	body := map[string]any{"name": name, "deploymentId": deploymentID}
	var d Deployment
	if err := c.request("POST", "/v13/deployments", scoped(url.Values{}, teamID), body, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Promote performs an instant rollback by making an existing ready
// production deployment the current production deployment again.
func (c *Client) Promote(projectID, deploymentID, teamID string) error {
	return c.request("POST", fmt.Sprintf("/v10/projects/%s/promote/%s", projectID, deploymentID), scoped(url.Values{}, teamID), map[string]any{}, nil)
}

// ProjectByName resolves a project id from its name; needed because the
// deployments list only carries the name.
func (c *Client) ProjectByName(name, teamID string) (*Project, error) {
	q := url.Values{"name": {name}, "limit": {"1"}}
	var out struct {
		Projects []Project `json:"projects"`
	}
	if err := c.get("/v9/projects", scoped(q, teamID), &out); err != nil {
		return nil, err
	}
	if len(out.Projects) == 0 {
		return nil, fmt.Errorf("project %q not found", name)
	}
	return &out.Projects[0], nil
}
