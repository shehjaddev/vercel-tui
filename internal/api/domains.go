package api

import (
	"context"
	"net/url"
)

// Domain is a domain bound to a project.
type Domain struct {
	Name      string `json:"name"`
	Verified  bool   `json:"verified"`
	CreatedAt msTime `json:"createdAt"`
}

// ProjectDomains lists the domains bound to a project.
func (c *Client) ProjectDomains(ctx context.Context, projectID, teamID string) ([]Domain, error) {
	var out struct {
		Domains []Domain `json:"domains"`
	}
	if err := c.get(ctx, "/v9/projects/"+projectID+"/domains", scoped(url.Values{}, teamID), &out); err != nil {
		return nil, err
	}
	return out.Domains, nil
}
