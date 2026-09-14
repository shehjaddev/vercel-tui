package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// The UI batches its fetches, so several requests can be rejected with 403 at
// the same moment. Only one of them may exchange the refresh token: the
// official CLI's token is single use and a second exchange logs it out.
func TestConcurrentForbiddenShareOneRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "token expired"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"username": "shehjad"}})
	}))
	defer srv.Close()

	var exchanges int32
	c := New("stale")
	c.baseURL = srv.URL
	c.SetRefresh(func() (string, error) {
		atomic.AddInt32(&exchanges, 1)
		return "fresh", nil
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := c.User(context.Background())
			if err != nil {
				t.Errorf("User: %v", err)
				return
			}
			if u.Username != "shehjad" {
				t.Errorf("username = %q, want shehjad", u.Username)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&exchanges); got != 1 {
		t.Fatalf("token exchanges = %d, want 1", got)
	}
}

// A refresh that fails must surface the API error, not retry forever.
func TestRefreshFailureKeepsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "nope"}})
	}))
	defer srv.Close()

	c := New("stale")
	c.baseURL = srv.URL
	var calls int32
	c.SetRefresh(func() (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", context.DeadlineExceeded
	})

	_, err := c.User(context.Background())
	if err == nil || err.Error() != "GET /v2/user: 403 nope" {
		t.Fatalf("err = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("refresh attempts = %d, want 1", got)
	}
}
