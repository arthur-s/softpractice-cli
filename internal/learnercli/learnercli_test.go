package learnercli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
)

func TestCredentialStoreUsesSystemSecretAndPrivateAtomicMetadata(t *testing.T) {
	secrets := newMemorySecretStore()
	store := CredentialStore{
		Path: filepath.Join(t.TempDir(), "nested", "credentials.json"), Secrets: secrets,
	}
	want := Credentials{
		APIURL: "https://api.example", AccessToken: "at_secret",
		AccessExpiresAt: time.Now().Add(time.Minute), RefreshToken: "rt_secret",
		RefreshIdleExpiresAt:     time.Now().Add(time.Hour),
		RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials metadata mode = %o", info.Mode().Perm())
	}
	metadata, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), want.RefreshToken) ||
		strings.Contains(string(metadata), want.AccessToken) {
		t.Fatalf("credentials metadata contains a bearer secret: %s", metadata)
	}
	got, err := store.Load()
	if err != nil || got.RefreshToken != want.RefreshToken || got.APIURL != want.APIURL {
		t.Fatalf("Load() = %+v, %v", got, err)
	}
	if got.AccessToken != "" || !got.AccessExpiresAt.IsZero() {
		t.Fatalf("persisted access credential = %+v", got)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(store.Path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatal("broad credentials metadata permissions were accepted")
		}
	}
}

func TestDefaultCredentialStoreKeepsExplicitTestDirectoryIsolated(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(credentialsDirEnv, directory)
	store, err := DefaultCredentialStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.Path != filepath.Join(directory, "credentials.json") {
		t.Fatalf("credentials path = %q", store.Path)
	}
	if store.KeyringAccountSuffix == "" {
		t.Fatal("explicit credentials directory must isolate the keyring account")
	}

	secrets := newMemorySecretStore()
	first := CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: secrets, KeyringAccountSuffix: "-first",
	}
	second := CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: secrets, KeyringAccountSuffix: "-second",
	}
	credentials := Credentials{
		APIURL: "https://api.example.test", RefreshToken: "first-refresh",
		RefreshIdleExpiresAt: time.Now().Add(time.Hour), RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	}
	if err := first.Save(credentials); err != nil {
		t.Fatal(err)
	}
	credentials.RefreshToken = "second-refresh"
	if err := second.Save(credentials); err != nil {
		t.Fatal(err)
	}
	loaded, err := first.Load()
	if err != nil || loaded.RefreshToken != "first-refresh" {
		t.Fatalf("first isolated credentials = %+v, %v", loaded, err)
	}
}

func TestLogoutDeletesLocalCredentialsWhenRemoteRevokeFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(writer, `{"error":{"code":"service_unavailable","message":"unavailable"}}`)
	}))
	defer server.Close()
	store := CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore(),
	}
	client, err := NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SaveCredentials(Credentials{
		APIURL: server.URL, RefreshToken: "refresh-token",
		RefreshIdleExpiresAt:     time.Now().Add(time.Hour),
		RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Logout(context.Background()); err == nil {
		t.Fatal("Logout() accepted a failed remote revoke")
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("local credentials remained readable after failed remote revoke")
	}
}

func TestLoadProjectLinkRequiresStrictCanonicalFile(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".softpractice")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceID := uuid.NewString()
	path := filepath.Join(directory, "project.json")
	if err := os.WriteFile(path, []byte(`{
  "schema_version": 1,
  "workspace_id": "`+workspaceID+`",
  "project_id": "equipment-rental-python"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link, err := LoadProjectLink(root)
	if err != nil || link.WorkspaceID != workspaceID {
		t.Fatalf("LoadProjectLink() = %+v, %v", link, err)
	}
	if err := os.WriteFile(path, append([]byte(`{
  "schema_version": 1,
  "workspace_id": "`+workspaceID+`",
  "project_id": "equipment-rental-python"
}`), []byte(` {}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectLink(root); err == nil {
		t.Fatal("project link with trailing JSON was accepted")
	}
}

func TestNewClientRequiresHTTPSOutsideLoopback(t *testing.T) {
	store := CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore(),
	}
	for _, address := range []string{
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
		"https://api.example",
	} {
		if _, err := NewClient(address, store); err != nil {
			t.Errorf("NewClient(%q) error = %v", address, err)
		}
	}
	if _, err := NewClient("http://api.example", store); err == nil {
		t.Fatal("NewClient accepted remote plain HTTP")
	}
}

func TestParseHTTPErrorAcceptsServerFailureSupportID(t *testing.T) {
	err := parseHTTPError(http.StatusInternalServerError, []byte(`{
  "error": {
    "code": "internal_error",
    "message": "Internal server error.",
    "support_id": "00000000-0000-4000-8000-000000000000"
  },
  "request_id": "00000000-0000-4000-8000-000000000001"
}`))
	var response *HTTPError
	if !errors.As(err, &response) {
		t.Fatalf("parseHTTPError() = %T, want *HTTPError", err)
	}
	if response.SupportID != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("support ID = %q", response.SupportID)
	}
}

// A learner keeps running the CLI version they installed, so a response that
// gained a field on the server has to stay readable by every published CLI.
func TestAPIResponsesTolerateFieldsThisVersionDoesNotKnow(t *testing.T) {
	var update CourseUpdate
	err := decodeJSON([]byte(`{
  "ref": "pa-foundation-01-to-pa-foundation-02@1.0.0",
  "from_assignment_id": "pa-foundation-01",
  "from_assignment_version": 1,
  "to_assignment_id": "pa-foundation-02",
  "to_assignment_version": 1,
  "base_revision_id": "00000000-0000-4000-8000-000000000000",
  "base_content_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
  "base_commit_sha": "504929d0ac0dd3f2cf2d1b6cbd5ac0cfd1cb2b3f",
  "archive_sha256": "1111111111111111111111111111111111111111111111111111111111111111",
  "archive_size": 1,
  "files": [],
  "operations": []
}`), &update)
	if err != nil {
		t.Fatalf("decode response with a newer field: %v", err)
	}
	if update.ToAssignmentID != "pa-foundation-02" || update.BaseRevisionID != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("decoded course update = %+v", update)
	}
}

// Tolerating unknown fields must not tolerate a second document: a response
// body carries exactly one, and anything after it means the stream is not what
// it claims to be.
func TestAPIResponsesStillRejectTrailingDocuments(t *testing.T) {
	var update CourseUpdate
	err := decodeJSON([]byte(`{"ref": "first"}{"ref": "second"}`), &update)
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("decodeJSON() = %v, want a trailing JSON error", err)
	}
}

// An error envelope goes through the same decoder. A field added to it must
// not collapse every server message into "invalid_response", which is what a
// failed decode falls back to.
func TestErrorEnvelopeSurvivesNewFields(t *testing.T) {
	err := parseHTTPError(http.StatusConflict, []byte(`{
  "error": {"code": "course_update_base_ambiguous", "message": "Choose a revision.", "support_id": ""},
  "request_id": "00000000-0000-4000-8000-000000000001",
  "documentation_url": "https://softpractice.ru/docs/course-update"
}`))
	var response *HTTPError
	if !errors.As(err, &response) {
		t.Fatalf("parseHTTPError() = %T, want *HTTPError", err)
	}
	if response.Code != "course_update_base_ambiguous" || response.Message != "Choose a revision." {
		t.Fatalf("parsed error = %+v", *response)
	}
}

func TestClientRefreshesAndKeepsAccessTokenInMemory(t *testing.T) {
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/auth/tokens/refresh":
			refreshes++
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"token_type": "Bearer", "access_token": "new-access",
				"expires_in_seconds": 900, "refresh_token": "new-refresh",
				"refresh_idle_expires_at":     time.Now().Add(time.Hour),
				"refresh_absolute_expires_at": time.Now().Add(2 * time.Hour),
				"scopes":                      []string{"profile:read"},
			})
		case "/v1/me":
			if request.Header.Get("Authorization") != "Bearer new-access" {
				writer.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(writer).Encode(map[string]any{
					"error":      map[string]string{"code": "invalid_credentials", "message": "bad"},
					"request_id": "00000000-0000-4000-8000-000000000000",
				})
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "user", "email": "ada@example.com", "display_name": "Ada",
				"email_verified": true, "cli_last_session_at": nil, "created_at": time.Now(),
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	store := CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore(),
	}
	if err := store.Save(Credentials{
		APIURL: server.URL, RefreshToken: "old-refresh",
		RefreshIdleExpiresAt:     time.Now().Add(time.Hour),
		RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	type profile struct {
		ID               string     `json:"id"`
		Email            string     `json:"email"`
		DisplayName      string     `json:"display_name"`
		EmailVerified    bool       `json:"email_verified"`
		CreatedAt        time.Time  `json:"created_at"`
		CLILastSessionAt *time.Time `json:"cli_last_session_at"`
	}
	for range 2 {
		var response profile
		if err := client.AuthorizedJSON(
			context.Background(),
			"GET",
			"/v1/me",
			nil,
			&response,
		); err != nil {
			t.Fatal(err)
		}
		if response.CLILastSessionAt != nil {
			t.Fatalf("response = %+v", response)
		}
	}
	if refreshes != 1 {
		t.Fatalf("refresh requests = %d, want 1", refreshes)
	}
	stored, err := store.Load()
	if err != nil || stored.RefreshToken != "new-refresh" || stored.AccessToken != "" {
		t.Fatalf("stored credentials = %+v, %v", stored, err)
	}
}

func TestDownloadCourseUpdateRefreshesAfterUnauthorizedResponse(t *testing.T) {
	contents := []byte("course update")
	checksum := fmt.Sprintf("%x", sha256.Sum256(contents))
	requests := 0
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/auth/tokens/refresh":
			refreshes++
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"token_type": "Bearer", "access_token": "new-access",
				"expires_in_seconds": 900, "refresh_token": "new-refresh",
				"refresh_idle_expires_at":     time.Now().Add(time.Hour),
				"refresh_absolute_expires_at": time.Now().Add(2 * time.Hour),
			})
		case "/v1/course-update":
			requests++
			if request.Header.Get("Authorization") != "Bearer new-access" {
				writer.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(writer).Encode(map[string]any{
					"error":      map[string]string{"code": "invalid_credentials", "message": "expired"},
					"request_id": "00000000-0000-4000-8000-000000000000",
				})
				return
			}
			writer.Header().Set("Content-Length", fmt.Sprint(len(contents)))
			writer.Header().Set("X-Softpractice-Course-Update-SHA256", checksum)
			writer.Header().Set("X-Softpractice-Course-Update-Ref", "update-v1")
			_, _ = writer.Write(contents)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	store := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore()}
	client, err := NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SaveCredentials(Credentials{
		APIURL: server.URL, AccessToken: "old-access", AccessExpiresAt: time.Now().Add(time.Hour),
		RefreshToken: "old-refresh", RefreshIdleExpiresAt: time.Now().Add(time.Hour),
		RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	archive, err := client.DownloadCourseUpdate(context.Background(), "/v1/course-update", &destination)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || refreshes != 1 || archive.SHA256 != checksum || !bytes.Equal(destination.Bytes(), contents) {
		t.Fatalf("requests=%d refreshes=%d archive=%+v contents=%q", requests, refreshes, archive, destination.Bytes())
	}
}

type memorySecretStore struct{ values map[string]string }

func newMemorySecretStore() *memorySecretStore {
	return &memorySecretStore{values: make(map[string]string)}
}

func (s *memorySecretStore) Get(service, account string) (string, error) {
	value, ok := s.values[service+"\x00"+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (s *memorySecretStore) Set(service, account, secret string) error {
	if s.values == nil {
		return errors.New("secret store is not initialized")
	}
	s.values[service+"\x00"+account] = secret
	return nil
}

func (s *memorySecretStore) Delete(service, account string) error {
	delete(s.values, service+"\x00"+account)
	return nil
}
