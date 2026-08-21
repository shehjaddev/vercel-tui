package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvCRUD(t *testing.T) {
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if r.Body != nil {
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			body = string(buf)
		}
		json.NewEncoder(w).Encode(map[string]any{"envs": []map[string]any{{
			"key": "API_KEY", "target": []string{"production"}, "type": "sensitive",
		}}})
	}))
	defer srv.Close()

	c := New("tok")
	c.baseURL = srv.URL

	envs, err := c.EnvVars("proj_1", "")
	if err != nil || len(envs) != 1 || envs[0].Key != "API_KEY" || !envs[0].Sensitive() {
		t.Fatalf("list: %v %+v", err, envs)
	}

	err = c.CreateEnv("proj_1", "", "K", "v", []string{"production"})
	if err != nil || method != "POST" || path != "/v10/projects/proj_1/env" {
		t.Errorf("create: %s %s %v", method, path, err)
	}
	var b map[string]any
	json.Unmarshal([]byte(body), &b)
	if b["key"] != "K" || b["value"] != "v" || b["type"] != "encrypted" {
		t.Errorf("create body: %s", body)
	}

	err = c.UpdateEnvValue("proj_1", "", "env_1", "newval", nil)
	if err != nil || method != "PATCH" || path != "/v10/projects/proj_1/env/env_1" {
		t.Errorf("update: %s %s %v", method, path, err)
	}
	json.Unmarshal([]byte(body), &b)
	if b["value"] != "newval" {
		t.Errorf("update body: %s", body)
	}

	if err := c.DeleteEnv("proj_1", "", "env_1"); err != nil || method != "DELETE" {
		t.Errorf("delete: %s %v", method, err)
	}
}

func TestDomains(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"domains": []map[string]any{
			{"name": "example.com", "verified": true},
		}})
	}))
	defer srv.Close()

	c := New("tok")
	c.baseURL = srv.URL

	d, err := c.ProjectDomains("proj_1", "")
	if err != nil || len(d) != 1 || d[0].Name != "example.com" || !d[0].Verified {
		t.Fatalf("project domains: %v %+v (path=%s)", err, d, gotPath)
	}
	if gotPath != "/v9/projects/proj_1/domains" {
		t.Errorf("path = %s", gotPath)
	}

	d, err = c.TeamDomains("")
	if err != nil || len(d) != 1 || gotPath != "/v5/domains" {
		t.Fatalf("team domains: %v %+v path=%s", err, d, gotPath)
	}
}
