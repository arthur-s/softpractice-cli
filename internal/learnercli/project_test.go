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
			"current_assignment": map[string]any{"id": "pa-diagnostic-01", "title": "Diagnostic", "version": 1, "state": "available", "local_checks": map[string]any{"schema_version": 1, "checks": []any{}}},
			"lessons":            []any{map[string]any{"id": "pa-diagnostic-01", "title": "Diagnostic", "version": 1, "learner_surface": "diagnostic", "state": "available"}},
		}}, "upcoming_practicums": []any{}})
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
	if practicum.ID != "equipment-rental-python" || practicum.Workspace == nil || practicum.Workspace.ID != workspaceID || practicum.CurrentAssignment == nil || practicum.CurrentAssignment.ID != "pa-diagnostic-01" || len(practicum.CurrentAssignment.LocalChecks) == 0 {
		t.Fatalf("started practicum = %+v", practicum)
	}
}

func TestResolveProjectRestoreSourceChoosesAcceptedPredecessor(t *testing.T) {
	workspaceID := uuid.NewString()
	baseRevisionID := uuid.NewString()
	acceptedSubmissionID := uuid.NewString()
	now := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/v1/practicums":
			_ = json.NewEncoder(writer).Encode(map[string]any{"practicums": []any{map[string]any{
				"id": "equipment-rental-python", "title": "Equipment rental", "can_skip_entry": true,
				"introduction":       map[string]any{"eyebrow": "Practice"},
				"first_assignment":   map[string]any{"id": "pa-foundation-01", "title": "Pricing", "version": 1, "estimated_minutes": 25, "learner_surface": "material"},
				"workspace":          map[string]any{"id": workspaceID, "project_id": "equipment-rental-python", "state": "active", "base_revision_id": baseRevisionID, "support_mode": nil, "created_at": now, "updated_at": now},
				"current_assignment": map[string]any{"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available"},
				"lessons": []any{
					map[string]any{"id": "pa-foundation-01", "version": 1, "state": "accepted", "result_submission_id": acceptedSubmissionID},
					map[string]any{"id": "pa-foundation-02", "version": 1, "state": "current", "result_submission_id": nil},
				},
			}}, "upcoming_practicums": []any{}})
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"workspace":         map[string]any{"id": workspaceID, "project_id": "equipment-rental-python", "state": "active", "base_revision_id": baseRevisionID, "support_mode": nil, "created_at": now, "updated_at": now},
				"assignment":        map[string]any{"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available"},
				"latest_submission": nil,
			})
		default:
			http.NotFound(writer, request)
		}
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
		APIURL: server.URL, AccessToken: "test-access", AccessExpiresAt: now.Add(time.Hour),
		RefreshToken: "test-refresh", RefreshIdleExpiresAt: now.Add(time.Hour),
		RefreshAbsoluteExpiresAt: now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	source, err := client.ResolveProjectRestoreSource(context.Background(), "equipment-rental-python")
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != "revision" || source.SubmissionID != acceptedSubmissionID ||
		source.AssignmentID != "pa-foundation-01" || !source.NeedsCourseUpdate ||
		source.ExpectedBaseRevisionID != baseRevisionID {
		t.Fatalf("restore source = %+v", source)
	}
}
