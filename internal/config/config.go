package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ResolveToken finds a Vercel token: explicit flag, environment,
// vtui's own storage, then credentials saved by the official CLI.
func ResolveToken(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("VERCEL_TOKEN"); env != "" {
		return env
	}
	if b, err := os.ReadFile(vtuiTokenPath()); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t
		}
	}
	for _, p := range cliAuthPaths() {
		if t := tokenFromCLIAuth(p); t != "" {
			return t
		}
	}
	return ""
}

// StoreToken saves a validated token for future runs.
func StoreToken(token string) error {
	path := vtuiTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token), 0o600)
}

func vtuiTokenPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "vtui", "token")
}

func cliAuthPaths() []string {
	var paths []string
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "com.vercel.cli", "auth.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".local", "share", "com.vercel.cli", "auth.json"),
			filepath.Join(home, ".config", "com.vercel.cli", "auth.json"),
		)
	}
	return paths
}

func tokenFromCLIAuth(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var a struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &a) != nil {
		return ""
	}
	return a.Token
}

// ProjectLink mirrors .vercel/project.json, the artifact the official
// CLI writes; both tools stay interoperable through it.
type ProjectLink struct {
	ProjectID string `json:"projectId"`
	OrgID     string `json:"orgId"`
}

// WriteProjectLink writes .vercel/project.json so the official CLI and vtui
// both pick up the same project/team scoping. The file belongs to the CLI
// too, so any field we don't own is left as it is. An empty orgID means the
// personal account: the key is dropped rather than written empty, which the
// CLI would read as a malformed org.
func WriteProjectLink(dir, projectID, orgID string) error {
	dir = filepath.Join(dir, ".vercel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "project.json")
	fields := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		var existing map[string]any
		if json.Unmarshal(b, &existing) == nil {
			fields = existing
		}
	}
	fields["projectId"] = projectID
	if orgID == "" {
		delete(fields, "orgId")
	} else {
		fields["orgId"] = orgID
	}
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func LoadProjectLink(dir string) (*ProjectLink, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".vercel", "project.json"))
	if err != nil {
		return nil, err
	}
	var link ProjectLink
	if err := json.Unmarshal(b, &link); err != nil {
		return nil, err
	}
	return &link, nil
}
