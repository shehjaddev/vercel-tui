package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// .vercel/project.json is shared with the official CLI, so linking must not
// throw away the fields it keeps there, and personal scope must not write an
// empty orgId that the CLI reads as a malformed org.
func TestWriteProjectLinkPreservesForeignFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vercel", "project.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"projectId":"prj_old","orgId":"team_old","projectName":"web"}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteProjectLink(dir, "prj_new", "team_new"); err != nil {
		t.Fatal(err)
	}
	link, err := LoadProjectLink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if link.ProjectID != "prj_new" || link.OrgID != "team_new" {
		t.Fatalf("link = %+v, want the new project and team", link)
	}
	if fields := readFields(t, path); fields["projectName"] != "web" {
		t.Fatalf("field owned by the CLI dropped: %v", fields)
	}

	// personal scope: the key goes away instead of turning empty
	if err := WriteProjectLink(dir, "prj_new", ""); err != nil {
		t.Fatal(err)
	}
	fields := readFields(t, path)
	if _, ok := fields["orgId"]; ok {
		t.Fatalf("empty orgId written: %v", fields)
	}
	if fields["projectId"] != "prj_new" {
		t.Fatalf("projectId lost: %v", fields)
	}

	// a file we can't parse is replaced rather than left blocking the link
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjectLink(dir, "prj_last", "team_last"); err != nil {
		t.Fatal(err)
	}
	link, err = LoadProjectLink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if link.ProjectID != "prj_last" || link.OrgID != "team_last" {
		t.Fatalf("link = %+v after a corrupt file", link)
	}
}

func readFields(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("project.json is not readable: %v (%s)", err, b)
	}
	return fields
}
