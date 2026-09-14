package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// VERCEL_CLI_CLIENT_ID is the public OAuth client id the Vercel CLI uses;
// Vercel's refresh endpoint accepts it for token exchanges on our behalf.
const VERCEL_CLI_CLIENT_ID = "cl_HYyOPBNtFMfHhaUn9L4QPfTZz6TP47bp"

const tokenEndpoint = "https://api.vercel.com/login/oauth/token"

// cliAuth is the part of the CLI's credentials file we read. Other fields are
// left in place by SaveCLIAuth, which round-trips the file as a map.
type cliAuth struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
}

// readCLIAuth parses the CLI's credentials file. ok is false when the file is
// missing or isn't JSON, which is how both callers walk the candidate paths.
func readCLIAuth(path string) (cliAuth, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cliAuth{}, false
	}
	var a cliAuth
	if json.Unmarshal(b, &a) != nil {
		return cliAuth{}, false
	}
	return a, true
}

// LoadCLIAuth returns the access and refresh tokens from the official CLI's
// credentials file, preferring the first one that has a token.
func LoadCLIAuth() (token, refresh string, ok bool) {
	for _, p := range cliAuthPaths() {
		a, ok := readCLIAuth(p)
		if !ok {
			continue
		}
		if a.RefreshToken == "" && a.Token == "" {
			continue
		}
		return a.Token, a.RefreshToken, true
	}
	return "", "", false
}

// SaveCLIAuth writes a fresh access (and refresh, if returned) token back to
// the first credentials file that parses, preserving the other fields so the
// CLI and vtui stay in sync.
func SaveCLIAuth(token, refresh string) error {
	for _, p := range cliAuthPaths() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var a map[string]any
		if json.Unmarshal(b, &a) != nil {
			continue
		}
		if token != "" {
			a["token"] = token
		}
		if refresh != "" {
			a["refreshToken"] = refresh
		}
		out, err := json.MarshalIndent(a, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(p, out, 0o600)
	}
	return os.ErrNotExist
}

// RefreshVercelToken exchanges the CLI's refresh token for a new access token
// and writes it back to the same credentials file. It returns the new token.
func RefreshVercelToken() (string, error) {
	_, refresh, ok := LoadCLIAuth()
	if !ok || refresh == "" {
		return "", errors.New("no refresh token available; set VERCEL_TOKEN or run `vercel login`")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {VERCEL_CLI_CLIENT_ID},
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).PostForm(tokenEndpoint, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("token refresh failed (status %d): %v", resp.StatusCode, err)
	}
	if out.AccessToken == "" {
		msg := out.Error
		if msg == "" {
			msg = string(body)
		}
		return "", fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, msg)
	}
	if err := SaveCLIAuth(out.AccessToken, out.RefreshToken); err != nil {
		// the new token is still usable this session even if persisting fails
		return out.AccessToken, nil
	}
	return out.AccessToken, nil
}
