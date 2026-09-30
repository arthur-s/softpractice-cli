package learnercli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentCredentialsDoNotReuseRefreshTokens(t *testing.T) {
	for _, separateClients := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate_clients=%t", separateClients), func(t *testing.T) {
			var mu sync.Mutex
			current := "initial"
			rotations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/auth/tokens/refresh" {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				var input map[string]string
				_ = json.NewDecoder(r.Body).Decode(&input)
				mu.Lock()
				defer mu.Unlock()
				if input["refresh_token"] != current {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":{"code":"invalid_credentials","message":"refresh reuse"}}`))
					return
				}
				rotations++
				current = fmt.Sprintf("refresh-%d", rotations)
				writeRefreshTokens(w, current, fmt.Sprintf("access-%d", rotations))
			}))
			defer server.Close()
			store := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore()}
			first, _ := NewClient(server.URL, store)
			if err := first.SaveCredentials(testRefreshCredentials(server.URL, "initial")); err != nil {
				t.Fatal(err)
			}
			clients := []*Client{first, first}
			if separateClients {
				clients[1], _ = NewClient(server.URL, store)
			}
			start := make(chan struct{})
			results := make(chan error, len(clients))
			for _, client := range clients {
				go func() {
					<-start
					var out map[string]any
					results <- client.AuthorizedJSON(context.Background(), "GET", "/v1/me", nil, &out)
				}()
			}
			close(start)
			for range clients {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if separateClients {
				want = 2
			}
			mu.Lock()
			defer mu.Unlock()
			if rotations != want {
				t.Fatalf("rotations = %d, want %d", rotations, want)
			}
		})
	}
}

func TestConcurrentUnauthorizedResponsesUseTheNewAccessToken(t *testing.T) {
	var rejected atomic.Int32
	var rotations atomic.Int32
	bothRejected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/tokens/refresh" {
			rotations.Add(1)
			writeRefreshTokens(w, "rotated", "new-access")
			return
		}
		if r.Header.Get("Authorization") == "Bearer old-access" {
			if rejected.Add(1) == 2 {
				close(bothRejected)
			}
			select {
			case <-bothRejected:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_credentials","message":"expired"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore()}
	client, _ := NewClient(server.URL, store)
	credentials := testRefreshCredentials(server.URL, "initial")
	credentials.AccessToken, credentials.AccessExpiresAt = "old-access", time.Now().Add(time.Hour)
	if err := client.SaveCredentials(credentials); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for range 2 {
		go func() { var out map[string]any; results <- client.AuthorizedJSON(ctx, "GET", "/v1/me", nil, &out) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if rotations.Load() != 1 {
		t.Fatalf("rotations = %d, want 1", rotations.Load())
	}
}

func TestRunningClientObservesTerminalLoginAndLogout(t *testing.T) {
	var refreshed atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/tokens/refresh" {
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			refreshed.Store(input["refresh_token"])
			if input["refresh_token"] != "terminal-login" {
				w.WriteHeader(401)
				return
			}
			writeRefreshTokens(w, "rotated", "new-access")
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore()}
	running, _ := NewClient(server.URL, store)
	terminal, _ := NewClient(server.URL, store)
	old := testRefreshCredentials(server.URL, "old-session")
	old.AccessToken, old.AccessExpiresAt = "old-access", time.Now().Add(time.Hour)
	if err := running.SaveCredentials(old); err != nil {
		t.Fatal(err)
	}
	if err := terminal.SaveCredentials(testRefreshCredentials(server.URL, "terminal-login")); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := running.AuthorizedJSON(context.Background(), "GET", "/v1/me", nil, &out); err != nil {
		t.Fatal(err)
	}
	if refreshed.Load() != "terminal-login" {
		t.Fatalf("refreshed %q", refreshed.Load())
	}
	if err := terminal.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := running.AuthorizedJSON(context.Background(), "GET", "/v1/me", nil, &out); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("after logout: %v", err)
	}
}

func TestCredentialLockAcrossProcesses(t *testing.T) {
	if path := os.Getenv("SOFTPRACTICE_TEST_LOCK_PATH"); path != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		unlock, err := (CredentialStore{Path: path}).lock(ctx)
		if os.Getenv("SOFTPRACTICE_TEST_LOCK_HELD") == "1" {
			if err == nil {
				unlock()
				t.Fatal("acquired lock held by parent")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			unlock()
		}
		return
	}
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json")}
	unlock, err := store.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, held := range []string{"1", "0"} {
		if held == "0" {
			unlock()
		}
		command := exec.Command(os.Args[0], "-test.run=^TestCredentialLockAcrossProcesses$")
		command.Env = append(os.Environ(), "SOFTPRACTICE_TEST_LOCK_PATH="+store.Path, "SOFTPRACTICE_TEST_LOCK_HELD="+held)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("child: %v\n%s", err, output)
		}
	}
}

func testRefreshCredentials(api, refresh string) Credentials {
	return Credentials{APIURL: api, RefreshToken: refresh, RefreshIdleExpiresAt: time.Now().Add(time.Hour), RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour)}
}

func writeRefreshTokens(w http.ResponseWriter, refresh, access string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "expires_in_seconds": 900, "refresh_token": refresh,
		"refresh_idle_expires_at": time.Now().Add(time.Hour), "refresh_absolute_expires_at": time.Now().Add(2 * time.Hour)})
}
