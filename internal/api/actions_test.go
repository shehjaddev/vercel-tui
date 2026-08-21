package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestMethodsAndBody(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.Method == "POST" {
			gotCT = r.Header.Get("Content-Type")
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			gotBody = string(buf)
		}
		json.NewEncoder(w).Encode(map[string]any{"uid": "dpl_new"})
	}))
	defer srv.Close()

	c := New("tok")
	c.baseURL = srv.URL

	d, err := c.Redeploy("web", "dpl_old", "")
	if err != nil || d.UID != "dpl_new" {
		t.Fatalf("Redeploy: %v %+v", err, d)
	}
	if gotMethod != "POST" || gotPath != "/v13/deployments" {
		t.Errorf("method/path: %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" || gotCT != "application/json" {
		t.Errorf("headers: %q %q", gotAuth, gotCT)
	}
	var body map[string]any
	if json.Unmarshal([]byte(gotBody), &body) != nil || body["deploymentId"] != "dpl_old" || body["name"] != "web" {
		t.Errorf("body: %s", gotBody)
	}

	c.baseURL = srv.URL
	if err := c.DeleteDeployment("dpl_x", ""); err != nil || gotMethod != "DELETE" {
		t.Errorf("delete: method=%s err=%v", gotMethod, err)
	}
}

func TestAPIErrorMessageParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "not allowed"}})
	}))
	defer srv.Close()

	c := New("tok")
	c.baseURL = srv.URL
	err := c.DeleteDeployment("dpl_x", "")
	want := `DELETE /v13/deployments/dpl_x: 403 not allowed`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestPromotePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New("tok")
	c.baseURL = srv.URL
	if err := c.Promote("proj_1", "dpl_1", ""); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v10/projects/proj_1/promote/dpl_1" {
		t.Errorf("path = %s", gotPath)
	}
}
