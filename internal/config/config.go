package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// ResolveToken finds a Vercel token: explicit flag, environment,
// vtui's own storage, then credentials saved by the official CLI.
func ResolveToken(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("VERCEL_TOKEN"); env != "" {
		return env
	}
	if path, err := vtuiTokenPath(); err == nil {
		if b, err := os.ReadFile(path); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				return t
			}
		}
	}
	for _, p := range cliAuthPaths() {
		if a, ok := readCLIAuth(p); ok && a.Token != "" {
			return a.Token
		}
	}
	return ""
}

// StoreToken saves a validated token for future runs.
func StoreToken(token string) error {
	path, err := vtuiTokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token), 0o600)
}

// vtuiTokenPath is where vtui keeps the token it was given.
func vtuiTokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vtui", "token"), nil
}

// cliAuthPaths are the places the official CLI keeps its credentials, the file
// it writes today first.
func cliAuthPaths() []string {
	var paths []string
	for _, dir := range dataHomes() {
		paths = append(paths, filepath.Join(dir, "com.vercel.cli", "auth.json"))
	}
	return paths
}

// dataHomes lists the directories the CLI may have written its credentials to,
// most likely first. The CLI resolves them with the xdg-portable package, whose
// data directory is XDG_DATA_HOME when that is set, %APPDATA%\xdg.data on
// Windows, the application support directory on macOS and ~/.local/share
// elsewhere. The remaining entries are locations older builds have used.
func dataHomes() []string {
	var dirs []string
	add := func(dir string) {
		if dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}

	add(os.Getenv("XDG_DATA_HOME"))

	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			add(filepath.Join(appData, "xdg.data"))
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			add(filepath.Join(local, "xdg.data"))
		}
	case "darwin":
		if dir, err := os.UserConfigDir(); err == nil { // ~/Library/Application Support
			add(dir)
		}
	default:
		if home, err := os.UserHomeDir(); err == nil {
			add(filepath.Join(home, ".local", "share"))
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".local", "share"))
		add(filepath.Join(home, ".config"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		add(dir)
	}
	return dirs
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
