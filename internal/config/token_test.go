package config

import (
	"os"
	"path/filepath"
	"testing"
)

// everywhere points the config and home directories at a temp dir, so the
// tests never read or write the real user's tokens.
func everywhere(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"XDG_CONFIG_HOME", "APPDATA", "HOME", "USERPROFILE"} {
		t.Setenv(k, dir)
	}
	t.Setenv("VERCEL_TOKEN", "")
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ResolveToken's order is the one the README promises: flag, environment,
// vtui's own store, then whatever the official CLI saved.
func TestResolveTokenOrder(t *testing.T) {
	dir := everywhere(t)
	cliAuth := filepath.Join(dir, "com.vercel.cli", "auth.json")

	if got := ResolveToken("from-flag"); got != "from-flag" {
		t.Fatalf("token = %q, want the flag's", got)
	}
	if got := ResolveToken(""); got != "" {
		t.Fatalf("token = %q, want none", got)
	}

	writeFile(t, cliAuth, `{"token":"from-cli"}`)
	if got := ResolveToken(""); got != "from-cli" {
		t.Fatalf("token = %q, want the CLI's", got)
	}

	if err := StoreToken("from-vtui"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveToken(""); got != "from-vtui" {
		t.Fatalf("token = %q, want vtui's own", got)
	}

	t.Setenv("VERCEL_TOKEN", "from-env")
	if got := ResolveToken(""); got != "from-env" {
		t.Fatalf("token = %q, want the environment's", got)
	}
}

// A credentials file that parses but holds no token must not shadow the one
// the CLI actually logged in with.
func TestResolveTokenSkipsTokenlessCLIFile(t *testing.T) {
	dir := everywhere(t)
	writeFile(t, filepath.Join(dir, "com.vercel.cli", "auth.json"), `{}`)
	writeFile(t, filepath.Join(dir, ".local", "share", "com.vercel.cli", "auth.json"),
		`{"token":"from-second"}`)

	if got := ResolveToken(""); got != "from-second" {
		t.Fatalf("token = %q, want the token from the second location", got)
	}
}
