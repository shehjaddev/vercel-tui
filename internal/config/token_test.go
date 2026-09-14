package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// everywhere points every directory the CLI or vtui might use at a temp dir,
// so the tests never read or write the real user's tokens. XDG_DATA_HOME and
// LOCALAPPDATA are included: they are where the CLI looks first.
func everywhere(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "APPDATA", "LOCALAPPDATA", "HOME", "USERPROFILE",
	} {
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

// The CLI resolves its data directory with xdg-portable: XDG_DATA_HOME when it
// is set, %APPDATA%\xdg.data on Windows, the application support directory on
// macOS and ~/.local/share elsewhere. vtui has to look in the same place.
func TestResolveTokenFindsCLICredentialsWhereItWritesThem(t *testing.T) {
	t.Run("XDG_DATA_HOME", func(t *testing.T) {
		dir := everywhere(t)
		writeFile(t, filepath.Join(dir, "com.vercel.cli", "auth.json"), `{"token":"from-data-home"}`)
		if got := ResolveToken(""); got != "from-data-home" {
			t.Fatalf("token = %q, want the one under XDG_DATA_HOME", got)
		}
	})

	t.Run("windows appdata", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("the CLI only nests credentials under xdg.data on Windows")
		}
		dir := everywhere(t)
		t.Setenv("XDG_DATA_HOME", "")
		writeFile(t, filepath.Join(dir, "xdg.data", "com.vercel.cli", "auth.json"), `{"token":"from-xdg-data"}`)
		if got := ResolveToken(""); got != "from-xdg-data" {
			t.Fatalf("token = %q, want the one under %%APPDATA%%\\xdg.data", got)
		}
	})
}
