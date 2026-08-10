package learnercli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStartedPracticumAcceptsCurrentPublicCatalogFields(t *testing.T) {
	workspaceID := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/practicums" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"practicums": []any{map[string]any{
			"id": "equipment-rental-python", "title": "Equipment rental", "can_skip_entry": true,
			"introduction":       map[string]any{"eyebrow": "Practice", "sections": []any{map[string]any{"title": "Welcome"}}},
			"first_assignment":   map[string]any{"id": "pa-diagnostic-01", "title": "Diagnostic", "version": 1, "estimated_minutes": 35, "learner_surface": "diagnostic"},
			"workspace":          map[string]any{"id": workspaceID, "project_id": "equipment-rental-python", "state": "active", "base_revision_id": nil, "support_mode": nil, "created_at": time.Now(), "updated_at": time.Now()},
			"current_assignment": map[string]any{"id": "pa-diagnostic-01", "title": "Diagnostic", "version": 1, "state": "available"},
			"lessons":            []any{map[string]any{"id": "pa-diagnostic-01", "title": "Diagnostic", "version": 1, "learner_surface": "diagnostic", "state": "available"}},
		}}})
	}))
	defer server.Close()

	store := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: newMemorySecretStore()}
	client, err := NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SaveCredentials(Credentials{
		APIURL: server.URL, AccessToken: "test-access", AccessExpiresAt: time.Now().Add(time.Hour),
		RefreshToken: "test-refresh", RefreshIdleExpiresAt: time.Now().Add(time.Hour), RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	practicum, err := client.StartedPracticum(context.Background(), "equipment-rental-python")
	if err != nil {
		t.Fatal(err)
	}
	if practicum.ID != "equipment-rental-python" || practicum.Workspace == nil || practicum.Workspace.ID != workspaceID || practicum.CurrentAssignment == nil || practicum.CurrentAssignment.ID != "pa-diagnostic-01" {
		t.Fatalf("started practicum = %+v", practicum)
	}
}
