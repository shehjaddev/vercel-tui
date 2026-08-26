package config

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
)

// VERCEL_CLI_CLIENT_ID is the public OAuth client id the Vercel CLI uses;
// Vercel's refresh endpoint accepts it for token exchanges on our behalf.
const VERCEL_CLI_CLIENT_ID = "cl_HYyOPBNtFMfHhaUn9L4QPfTZz6TP47bp"

const tokenEndpoint = "https://api.vercel.com/login/oauth/token"

type cliAuth struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
	UserID       string `json:"userId"`
}

// LoadCLIAuth returns the access and refresh tokens from the official CLI's
// credentials file, preferring the first one found.
func LoadCLIAuth() (token, refresh string, ok bool) {
	for _, p := range cliAuthPaths() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var a cliAuth
		if json.Unmarshal(b, &a) != nil {
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
// the CLI credentials file, preserving the other fields so the CLI and vtui
// stay in sync.
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
	resp, err := http.PostForm(tokenEndpoint, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("token refresh failed: " + out.Error)
	}
	if err := SaveCLIAuth(out.AccessToken, out.RefreshToken); err != nil {
		// the new token is still usable this session even if persisting fails
		return out.AccessToken, nil
	}
	return out.AccessToken, nil
}
