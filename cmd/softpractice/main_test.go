package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/starterbundle"
	"github.com/arthur-s/softpractice-cli/internal/submission"
)

func TestRestoreRevisionProjectCreatesCurrentLinkedGitProject(t *testing.T) {
	workspaceID := uuid.NewString()
	submissionID := uuid.NewString()
	revisionID := uuid.NewString()
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	file, err := zipWriter.Create("app.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("print('restored')\n")); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		switch request.URL.Path {
		case "/v1/submissions/" + submissionID + "/revision/archive":
			writer.Header().Set("Content-Type", "application/zip")
			writer.Header().Set("Content-Length", fmt.Sprint(archive.Len()))
			_, _ = writer.Write(archive.Bytes())
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, map[string]any{
				"workspace": map[string]any{
					"id": workspaceID, "project_id": "equipment-rental-python",
					"state": "active", "base_revision_id": nil, "support_mode": nil,
					"created_at": time.Now(), "updated_at": time.Now(),
				},
				"assignment": map[string]any{
					"id": "pa-foundation-02", "version": 1,
					"title": "Rental period", "state": "available",
					"local_checks": testLocalChecks(),
				},
				"latest_submission": map[string]any{
					"id": submissionID, "revision_id": revisionID,
					"job_state": "completed", "submitted_at": time.Now(),
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	target := filepath.Join(t.TempDir(), "restored-project")
	source := learnercli.ProjectRestoreSource{
		Kind: "revision", WorkspaceID: workspaceID, ProjectID: "equipment-rental-python",
		AssignmentID: "pa-foundation-02", AssignmentVersion: 1,
		SubmissionID: submissionID,
	}
	if err := restoreRevisionProject(context.Background(), client, source, target); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(target, "app.py"))
	if err != nil || string(content) != "print('restored')\n" {
		t.Fatalf("restored content = %q, %v", content, err)
	}
	link, err := learnercli.LoadProjectLink(target)
	if err != nil || link.WorkspaceID != workspaceID || link.AssignmentID != "pa-foundation-02" {
		t.Fatalf("restored link = %+v, %v", link, err)
	}
	if output, err := exec.Command("git", "-C", target, "status", "--porcelain").CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("restored git status = %q, %v", output, err)
	}
}

func TestRunRequiresKnownCommand(t *testing.T) {
	var output, errorsOutput bytes.Buffer
	err := run(
		context.Background(),
		[]string{"unknown"},
		strings.NewReader(""),
		&output,
		&errorsOutput,
	)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestLoginCommandPersistsOnlyRefreshCredential(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/v1/auth/device/authorizations":
			writeTestJSON(writer, map[string]any{
				"device_code": "device-code", "user_code": "ABCD-EFGH",
				"verification_uri":          "https://app.example/device",
				"verification_uri_complete": "https://app.example/device?user_code=ABCD-EFGH",
				"expires_in_seconds":        600, "interval_seconds": 0,
			})
		case "/v1/auth/device/tokens":
			writeTestJSON(writer, map[string]any{
				"token_type": "Bearer", "access_token": "access-token",
				"expires_in_seconds": 900, "refresh_token": "refresh-token",
				"refresh_idle_expires_at":     now.Add(time.Hour),
				"refresh_absolute_expires_at": now.Add(2 * time.Hour),
				"scopes": []string{
					"profile:read", "workspace:read", "submission:write",
				},
			})
		case "/v1/auth/tokens/revoke":
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	store := learnercli.CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"),
		Secrets: &mainMemorySecretStore{
			values: make(map[string]string),
		},
	}
	client, err := learnercli.NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	var output, errorOutput bytes.Buffer
	if err := login(
		context.Background(),
		client,
		[]string{"--no-browser"},
		&output,
		&errorOutput,
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RefreshToken != "refresh-token" || loaded.AccessToken != "" {
		t.Fatalf("stored credentials = %+v", loaded)
	}
	if err := client.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("credential store remained readable after logout")
	}
}

func TestLinkedStatusSubmitAndOpenCommandFlow(t *testing.T) {
	workspaceID := uuid.NewString()
	baseRevisionID := uuid.NewString()
	submissionID := uuid.NewString()
	revisionID := uuid.NewString()
	evaluationJobID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	var idempotencyKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v1/auth/tokens/refresh" &&
			request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		switch {
		case request.URL.Path == "/v1/auth/tokens/refresh":
			writeTestJSON(writer, map[string]any{
				"token_type": "Bearer", "access_token": "access-token",
				"expires_in_seconds": 900, "refresh_token": "rotated-refresh-token",
				"refresh_idle_expires_at":     now.Add(24 * time.Hour),
				"refresh_absolute_expires_at": now.Add(48 * time.Hour),
				"scopes": []string{
					"profile:read", "workspace:read", "submission:write",
				},
			})
		case request.URL.Path == "/v1/me":
			writeTestJSON(writer, map[string]any{
				"id": uuid.NewString(), "email": "ada@example.com", "display_name": "Ada",
				"email_verified": true, "cli_last_session_at": nil, "created_at": now,
			})
		case request.URL.Path == "/v1/workspaces/"+workspaceID+"/current-assignment":
			writeTestJSON(writer, map[string]any{
				"workspace": map[string]any{
					"id": workspaceID, "project_id": "equipment-rental-python",
					"state": "active", "base_revision_id": baseRevisionID, "support_mode": nil,
					"created_at": now, "updated_at": now,
				},
				"assignment": map[string]any{
					"id": "pa-foundation-05", "version": 1,
					"title": "Module boundaries", "state": "submitted",
					"local_checks": testLocalChecks(),
				},
				"latest_submission": map[string]any{
					"id": submissionID, "revision_id": revisionID,
					"job_state": "queued", "submitted_at": now,
				},
			})
		case request.URL.Path == "/v1/submissions/"+submissionID+"/evaluation":
			writeTestJSON(writer, map[string]any{
				"submission_id": submissionID, "evaluation_job_id": evaluationJobID,
				"job_state": "completed", "attempt": 1, "max_attempts": 3,
				"updated_at": now,
				"evaluation": map[string]any{
					"id": uuid.NewString(), "schema_version": 1,
					"exercise_id": "pa-foundation-05", "evaluator_version": "test",
					"profile_id": "local-test", "profile_version": "1",
					"profile_sha256": strings.Repeat("a", 64),
					"status":         "revise", "completed_at": now,
					"deterministic": map[string]any{"status": "passed", "checks": []any{
						map[string]any{"id": "public-regression", "kind": "public_tests", "status": "pass", "summary": "Public tests passed."},
					}},
					"review": map[string]any{
						"projection_version": 1, "source_schema_id": "pa-foundation-05-review-v1",
						"status": "completed", "mode": "llm", "authoritative": true,
						"verdict": "revise", "summary": "safe feedback",
						"feedback": []any{map[string]any{"id": "boundary", "title": "Check a boundary"}},
					},
					"recommendation": nil, "evidence": map[string]any{}, "technical_error": nil,
				},
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/workspaces/"+workspaceID+
				"/assignments/pa-foundation-05/submissions":
			if request.Header.Get("Content-Type") != "application/gzip" ||
				request.Header.Get("X-Softpractice-Base-Revision-ID") != baseRevisionID ||
				request.Header.Get("X-Softpractice-Assignment-Version") != "1" {
				writeTestAPIError(writer, http.StatusBadRequest, "invalid_request")
				return
			}
			if _, err := io.Copy(io.Discard, request.Body); err != nil {
				t.Error(err)
			}
			idempotencyKeys = append(idempotencyKeys, request.Header.Get("Idempotency-Key"))
			replayed := len(idempotencyKeys) > 1
			status := http.StatusCreated
			if replayed {
				status = http.StatusOK
			}
			writer.WriteHeader(status)
			writeTestJSON(writer, map[string]any{
				"submission_id": submissionID, "revision_id": revisionID,
				"evaluation_job_id": evaluationJobID, "job_state": "queued",
				"replayed": replayed, "submitted_at": now,
				"submission_url": "/v1/submissions/" + submissionID,
				"evaluation_url": "/v1/submissions/" + submissionID + "/evaluation",
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	repositoryRoot := createLinkedGitRepository(t, workspaceID)
	secrets := &mainMemorySecretStore{values: make(map[string]string)}
	store := learnercli.CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: secrets,
	}
	if err := store.Save(learnercli.Credentials{
		APIURL: server.URL, RefreshToken: "initial-refresh-token",
		RefreshIdleExpiresAt:     now.Add(time.Hour),
		RefreshAbsoluteExpiresAt: now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	client, err := learnercli.NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}

	var statusOutput, statusErrors bytes.Buffer
	if err := status(
		context.Background(),
		client,
		repositoryRoot,
		nil,
		&statusOutput,
		&statusErrors,
	); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Project: equipment-rental-python",
		"Assignment: pa-foundation-05 v1",
		"Working tree: clean",
		"Latest pa-foundation-05 submission: " + submissionID,
	} {
		if !strings.Contains(statusOutput.String(), expected) {
			t.Fatalf("status output %q does not contain %q", statusOutput.String(), expected)
		}
	}
	var statusJSON bytes.Buffer
	if err := status(
		context.Background(), client, repositoryRoot, []string{"--format", "json"},
		&statusJSON, &statusErrors,
	); err != nil {
		t.Fatal(err)
	}
	var statusPayload machineStatus
	if err := decodeTestJSON(statusJSON.Bytes(), &statusPayload); err != nil {
		t.Fatal(err)
	}
	if statusPayload.ContractVersion != 1 || statusPayload.Kind != "softpractice.status" ||
		statusPayload.Local.Head == "" || !statusPayload.Local.Clean ||
		statusPayload.LatestSubmission == nil || statusPayload.LatestSubmission.ID != submissionID {
		t.Fatalf("machine status = %+v", statusPayload)
	}

	var submissionJSON bytes.Buffer
	if err := submissionCommand(
		context.Background(), client, repositoryRoot,
		[]string{"show", "--id", submissionID, "--format", "json"},
		&submissionJSON, &statusErrors,
	); err != nil {
		t.Fatal(err)
	}
	var submissionPayload machineSubmission
	if err := decodeTestJSON(submissionJSON.Bytes(), &submissionPayload); err != nil {
		t.Fatal(err)
	}
	if !submissionPayload.Terminal || submissionPayload.Evaluation == nil ||
		submissionPayload.Evaluation.Status != "revise" ||
		submissionPayload.Evaluation.DeterministicStatus != "passed" ||
		submissionPayload.Evaluation.Review == nil ||
		submissionPayload.Evaluation.Review.Verdict == nil ||
		*submissionPayload.Evaluation.Review.Verdict != "revise" {
		t.Fatalf("machine submission = %+v", submissionPayload)
	}

	var submissionJSONV2 bytes.Buffer
	if err := submissionCommand(
		context.Background(), client, repositoryRoot,
		[]string{"show", "--id", submissionID, "--format", "json-v2"},
		&submissionJSONV2, &statusErrors,
	); err != nil {
		t.Fatal(err)
	}
	var submissionPayloadV2 machineSubmissionV2
	if err := decodeTestJSON(submissionJSONV2.Bytes(), &submissionPayloadV2); err != nil {
		t.Fatal(err)
	}
	var evaluationV2 struct {
		Deterministic struct {
			Checks []struct {
				ID string `json:"id"`
			} `json:"checks"`
		} `json:"deterministic"`
		Review struct {
			Summary  string `json:"summary"`
			Feedback []any  `json:"feedback"`
		} `json:"review"`
	}
	if submissionPayloadV2.ContractVersion != 2 || !submissionPayloadV2.Terminal ||
		json.Unmarshal(submissionPayloadV2.Evaluation, &evaluationV2) != nil ||
		len(evaluationV2.Deterministic.Checks) != 1 ||
		evaluationV2.Deterministic.Checks[0].ID != "public-regression" ||
		evaluationV2.Review.Summary != "safe feedback" || len(evaluationV2.Review.Feedback) != 1 {
		t.Fatalf("machine submission v2 = %+v, evaluation = %+v", submissionPayloadV2, evaluationV2)
	}

	for range 2 {
		var submitOutput, submitErrors bytes.Buffer
		if err := submit(
			context.Background(),
			client,
			repositoryRoot,
			[]string{"--yes"},
			strings.NewReader(""),
			&submitOutput,
			&submitErrors,
		); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(submitOutput.String(), "Submission: "+submissionID) {
			t.Fatalf("submit output = %q", submitOutput.String())
		}
	}
	if len(idempotencyKeys) != 2 ||
		idempotencyKeys[0] == "" ||
		idempotencyKeys[0] != idempotencyKeys[1] {
		t.Fatalf("idempotency keys = %v", idempotencyKeys)
	}

	var openOutput, openErrors bytes.Buffer
	if err := openCurrent(
		context.Background(),
		client,
		repositoryRoot,
		[]string{"--no-browser", "--web", "https://app.example"},
		&openOutput,
		&openErrors,
	); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(openOutput.String()) != "https://app.example/submissions/"+submissionID+"/result" {
		t.Fatalf("open output = %q", openOutput.String())
	}
}

func TestSubmissionDownloadCommandSavesOwnedRevisionArchive(t *testing.T) {
	submissionID := uuid.NewString()
	contents := []byte("PK\\x03\\x04immutable revision")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		if request.URL.Path != "/v1/submissions/"+submissionID+"/revision/archive" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/zip")
		writer.Header().Set("Content-Length", fmt.Sprint(len(contents)))
		_, _ = writer.Write(contents)
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	target := filepath.Join(t.TempDir(), "accepted-project.zip")
	var output, errorOutput bytes.Buffer
	if err := downloadSubmissionRevision(
		context.Background(), client,
		[]string{"--id", submissionID, "--output", target},
		&output, &errorOutput,
	); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("downloaded archive = %q, %v", got, err)
	}
	if !strings.Contains(output.String(), "Project saved") {
		t.Fatalf("output = %q", output.String())
	}
	if err := downloadSubmissionRevision(
		context.Background(), client,
		[]string{"--id", submissionID, "--output", target},
		io.Discard, &errorOutput,
	); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite error = %v", err)
	}
}

func TestSubmissionShowFallsBackToAcceptedPredecessor(t *testing.T) {
	workspaceID := uuid.NewString()
	baseRevisionID := uuid.NewString()
	acceptedSubmissionID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	workspace := map[string]any{
		"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
		"base_revision_id": baseRevisionID, "support_mode": nil, "created_at": now, "updated_at": now,
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		switch request.URL.Path {
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, map[string]any{
				"workspace": workspace,
				"assignment": map[string]any{
					"id": "pa-foundation-02", "version": 1, "title": "Rental period",
					"state": "available", "local_checks": testLocalChecks(),
				},
				"latest_submission": nil,
			})
		case "/v1/practicums":
			writeTestJSON(writer, map[string]any{"practicums": []any{map[string]any{
				"id": "equipment-rental-python", "title": "Equipment rental", "can_skip_entry": true,
				"introduction":       map[string]any{},
				"first_assignment":   map[string]any{"id": "pa-foundation-01", "title": "Pricing", "version": 2, "estimated_minutes": 25, "learner_surface": "material"},
				"workspace":          workspace,
				"current_assignment": map[string]any{"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available"},
				"lessons": []any{
					map[string]any{"id": "pa-foundation-01", "version": 2, "state": "accepted", "result_submission_id": acceptedSubmissionID},
					map[string]any{"id": "pa-foundation-02", "version": 1, "state": "current", "result_submission_id": nil},
				},
			}}, "upcoming_practicums": []any{}})
		case "/v1/submissions/" + acceptedSubmissionID + "/evaluation":
			writeTestJSON(writer, map[string]any{
				"submission_id": acceptedSubmissionID, "evaluation_job_id": uuid.NewString(),
				"job_state": "completed", "attempt": 1, "max_attempts": 3, "updated_at": now,
				"evaluation": map[string]any{
					"id": uuid.NewString(), "schema_version": 1,
					"exercise_id": "pa-foundation-01", "evaluator_version": "test",
					"profile_id": "local-test", "profile_version": "1",
					"profile_sha256": strings.Repeat("a", 64),
					"status":         "accepted", "completed_at": now,
					"deterministic": map[string]any{"status": "passed", "checks": []any{}},
					"review": map[string]any{
						"projection_version": 1, "source_schema_id": "pa-foundation-01-review-v1",
						"status": "completed", "mode": "llm", "authoritative": true,
						"verdict": "accept", "summary": "safe feedback", "feedback": []any{},
					},
					"recommendation": nil, "evidence": map[string]any{}, "technical_error": nil,
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	repositoryRoot := createLinkedGitRepository(t, workspaceID)

	var output, errorOutput bytes.Buffer
	if err := submissionCommand(
		context.Background(), client, repositoryRoot,
		[]string{"show", "--format", "json"}, &output, &errorOutput,
	); err != nil {
		t.Fatal(err)
	}
	var payload machineSubmission
	if err := decodeTestJSON(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SubmissionID != acceptedSubmissionID || payload.Evaluation == nil ||
		payload.Evaluation.Status != "accepted" {
		t.Fatalf("machine submission = %+v", payload)
	}
	if !strings.Contains(errorOutput.String(), "pa-foundation-02 has no submission yet") ||
		!strings.Contains(errorOutput.String(), "pa-foundation-01") {
		t.Fatalf("fallback notice = %q", errorOutput.String())
	}
}

func TestConfirmationRequiresExplicitInputWhenNonInteractive(t *testing.T) {
	var output bytes.Buffer
	if _, err := confirmSubmission(context.Background(), strings.NewReader(""), &output); err == nil {
		t.Fatal("empty non-interactive input was accepted")
	}
	confirmed, err := confirmSubmission(context.Background(), strings.NewReader("\n"), &output)
	if err != nil || !confirmed {
		t.Fatalf("default confirmation = %v, %v", confirmed, err)
	}
	confirmed, err = confirmSubmission(context.Background(), strings.NewReader("n\n"), &output)
	if err != nil || confirmed {
		t.Fatalf("negative confirmation = %v, %v", confirmed, err)
	}
}

func TestStarterCommandDownloadsLinksAndInitializesGit(t *testing.T) {
	workspaceID := uuid.NewString()
	starterRoot := t.TempDir()
	starterContents := []byte("# Starter\n")
	if err := os.WriteFile(filepath.Join(starterRoot, "README.md"), starterContents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(starterContents)
	var archive bytes.Buffer
	metadata, err := starterbundle.Build(starterRoot, []starterbundle.File{{
		Path: "README.md", SHA256: hex.EncodeToString(digest[:]),
	}}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		switch request.URL.Path {
		case "/v1/practicums":
			writeTestJSON(writer, map[string]any{"practicums": []any{map[string]any{
				"id": "equipment-rental-python", "title": "Equipment rental",
				"first_assignment":   map[string]any{"id": "pa-diagnostic-01", "title": "Diagnostic", "version": 1, "estimated_minutes": 35},
				"workspace":          map[string]any{"id": workspaceID, "project_id": "equipment-rental-python", "state": "active", "base_revision_id": nil, "support_mode": nil, "created_at": time.Now(), "updated_at": time.Now()},
				"current_assignment": map[string]any{"id": "pa-diagnostic-01", "title": "Diagnostic", "version": 1, "state": "available", "local_checks": testLocalChecks()},
			}}})
		case "/v1/workspaces/" + workspaceID + "/current-assignment/starter":
			if request.URL.Query().Get("format") != "tar.gz" {
				t.Fatalf("starter format = %q", request.URL.Query().Get("format"))
			}
			writer.Header().Set("Content-Type", "application/gzip")
			writer.Header().Set("Content-Length", fmt.Sprint(metadata.Size))
			writer.Header().Set("X-Softpractice-Starter-SHA256", metadata.SHA256)
			writer.Header().Set("X-Softpractice-Starter-Ref", "pa-diagnostic-01-starter@0.1.0")
			_, _ = writer.Write(archive.Bytes())
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	store := learnercli.CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: &mainMemorySecretStore{values: map[string]string{}}}
	client, err := learnercli.NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SaveCredentials(learnercli.Credentials{APIURL: server.URL, AccessToken: "access-token", AccessExpiresAt: time.Now().Add(time.Hour), RefreshToken: "refresh-token", RefreshIdleExpiresAt: time.Now().Add(time.Hour), RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "diagnostic-project")
	var output, errorOutput bytes.Buffer
	if err := downloadStarter(context.Background(), client, []string{"--practicum", "equipment-rental-python", "--directory", target}, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	link, err := learnercli.LoadProjectLink(target)
	if err != nil || link.WorkspaceID != workspaceID || link.SchemaVersion != 2 ||
		link.AssignmentID != "pa-diagnostic-01" || link.AssignmentVersion != 1 {
		t.Fatalf("project link = %+v, %v", link, err)
	}
	repository, err := inspectGitRepository(context.Background(), target, false)
	if err != nil || !repository.Clean || repository.CommitSHA == "" {
		t.Fatalf("starter repository = %+v, %v", repository, err)
	}
	if remote, err := gitOutput(context.Background(), target, "remote"); err != nil || len(remote) != 0 {
		t.Fatalf("starter remote = %q, %v", remote, err)
	}
	if !strings.Contains(output.String(), "Starter project created") {
		t.Fatalf("output = %q", output.String())
	}
}

// The entry lesson of every practicum starts the repository the learner keeps,
// so it is named after the project alone; only a later independent starter
// carries its lesson suffix.
func TestStarterCommandUsesProjectDefaultDirectoryForTheEntryLesson(t *testing.T) {
	for _, test := range []struct{ practicum, first, current, want string }{
		{"equipment-rental-python", "pa-foundation-01", "pa-foundation-01", "equipment-rental-python"},
		{"equipment-rental-go", "ga-foundation-01", "ga-foundation-01", "equipment-rental-go"},
		{"equipment-rental-go", "ga-foundation-01", "ga-foundation-10", "equipment-rental-go-ga-foundation-10"},
	} {
		t.Run(test.want, func(t *testing.T) {
			assertStarterDefaultDirectory(t, test.practicum, test.first, test.current, test.want)
		})
	}
}

func assertStarterDefaultDirectory(t *testing.T, practicumID, firstAssignmentID, currentAssignmentID, want string) {
	workspaceID := uuid.NewString()
	starterRoot := t.TempDir()
	starterContents := []byte("# Starter\n")
	if err := os.WriteFile(filepath.Join(starterRoot, "README.md"), starterContents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(starterContents)
	var archive bytes.Buffer
	metadata, err := starterbundle.Build(starterRoot, []starterbundle.File{{
		Path: "README.md", SHA256: hex.EncodeToString(digest[:]),
	}}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/practicums":
			writeTestJSON(writer, map[string]any{"practicums": []any{map[string]any{
				"id": practicumID, "title": "Equipment rental",
				"first_assignment":   map[string]any{"id": firstAssignmentID, "title": "First", "version": 1, "estimated_minutes": 35},
				"workspace":          map[string]any{"id": workspaceID, "project_id": practicumID, "state": "active", "base_revision_id": nil, "support_mode": nil, "created_at": time.Now(), "updated_at": time.Now()},
				"current_assignment": map[string]any{"id": currentAssignmentID, "title": "Current", "version": 1, "state": "available", "local_checks": testLocalChecks()},
			}}})
		case "/v1/workspaces/" + workspaceID + "/current-assignment/starter":
			writer.Header().Set("Content-Type", "application/gzip")
			writer.Header().Set("Content-Length", fmt.Sprint(metadata.Size))
			writer.Header().Set("X-Softpractice-Starter-SHA256", metadata.SHA256)
			_, _ = writer.Write(archive.Bytes())
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	store := learnercli.CredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: &mainMemorySecretStore{values: map[string]string{}}}
	client, err := learnercli.NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SaveCredentials(learnercli.Credentials{APIURL: server.URL, AccessToken: "access-token", AccessExpiresAt: time.Now().Add(time.Hour), RefreshToken: "refresh-token", RefreshIdleExpiresAt: time.Now().Add(time.Hour), RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	t.Chdir(workdir)
	var output, errorOutput bytes.Buffer
	if err := downloadStarter(context.Background(), client, []string{"--practicum", practicumID}, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(workdir, want)
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("starter destination %s is missing: %v", want, err)
	}
	if !strings.Contains(output.String(), "Starter project created") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestUpdateCommandAppliesVerifiedCourseUpdateInPlace(t *testing.T) {
	workspaceID := uuid.NewString()
	updateRoot := t.TempDir()
	updateContents := []byte("lesson two\n")
	if err := os.WriteFile(filepath.Join(updateRoot, "lesson_two.py"), updateContents, 0o600); err != nil {
		t.Fatal(err)
	}
	updatedReadme := []byte("# Lesson two\n")
	if err := os.WriteFile(filepath.Join(updateRoot, "README.md"), updatedReadme, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(updateContents)
	readmeDigest := sha256.Sum256(updatedReadme)
	var archive bytes.Buffer
	metadata, err := starterbundle.Build(updateRoot, []starterbundle.File{
		{Path: "README.md", SHA256: hex.EncodeToString(readmeDigest[:])},
		{Path: "lesson_two.py", SHA256: hex.EncodeToString(digest[:])},
	}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Lesson one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", root, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("stage test README: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", root, "commit", "--quiet", "-m", "add README").CombinedOutput(); err != nil {
		t.Fatalf("commit test README: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", root, "update-index", "--assume-unchanged", ".softpractice/project.json").CombinedOutput(); err != nil {
		t.Fatalf("ignore local project link in test repository: %v: %s", err, output)
	}
	repository, err := inspectGitRepository(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(repository.ArchivePath)
	normalized, err := submission.NormalizeTarGz(repository.ArchivePath, t.TempDir(), submission.DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(normalized.Path)
	baseRevisionID := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		switch request.URL.Path {
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, map[string]any{
				"workspace": map[string]any{
					"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
					"base_revision_id": nil, "support_mode": nil, "created_at": time.Now(), "updated_at": time.Now(),
				},
				"assignment": map[string]any{
					"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available",
					"local_checks": testLocalChecks(),
				},
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment/course-update":
			if request.Method != http.MethodPost {
				t.Fatalf("course update method = %s", request.Method)
			}
			writeTestJSON(writer, map[string]any{
				"ref":                "pa-foundation-01-to-pa-foundation-02@1.0.0",
				"from_assignment_id": "pa-foundation-01", "from_assignment_version": 1,
				"to_assignment_id": "pa-foundation-02", "to_assignment_version": 1,
				"base_revision_id": baseRevisionID, "base_content_sha256": normalized.ContentSHA256,
				"archive_sha256": metadata.SHA256, "archive_size": metadata.Size,
				"files": []any{
					map[string]any{"path": "README.md", "sha256": hex.EncodeToString(readmeDigest[:])},
					map[string]any{"path": "lesson_two.py", "sha256": hex.EncodeToString(digest[:])},
				},
				"operations": []any{
					map[string]any{"kind": "replace", "path": "README.md"},
					map[string]any{"kind": "add", "path": "lesson_two.py"},
				},
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment/course-update/archive":
			writer.Header().Set("Content-Type", "application/gzip")
			writer.Header().Set("Content-Length", fmt.Sprint(metadata.Size))
			writer.Header().Set("X-Softpractice-Course-Update-SHA256", metadata.SHA256)
			writer.Header().Set("X-Softpractice-Course-Update-Ref", "pa-foundation-01-to-pa-foundation-02@1.0.0")
			writer.Header().Set("X-Softpractice-Course-Update-Base-Revision-ID", baseRevisionID)
			writer.Header().Set("X-Softpractice-Course-Update-Base-Content-SHA256", normalized.ContentSHA256)
			_, _ = writer.Write(archive.Bytes())
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	t.Chdir(root)
	var output, errorOutput bytes.Buffer
	if err := updateProject(context.Background(), client, nil, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	link, err := learnercli.LoadProjectLink(root)
	if err != nil || link.AssignmentID != "pa-foundation-02" || link.AssignmentVersion != 1 {
		t.Fatalf("updated project link = %+v, %v", link, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "lesson_two.py")); err != nil || string(content) != string(updateContents) {
		t.Fatalf("course update file = %q, %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(content) != string(updatedReadme) {
		t.Fatalf("course update README = %q, %v", content, err)
	}
	repository, err = inspectGitRepository(context.Background(), root, false)
	if err != nil || !repository.Clean {
		t.Fatalf("updated project is not clean: %+v, %v", repository, err)
	}
	if !strings.Contains(output.String(), "updated in place") {
		t.Fatalf("update output = %q", output.String())
	}
}

func TestUpdateCommandDoesNotOverlayAContinuedProject(t *testing.T) {
	workspaceID := uuid.NewString()
	baseRevisionID := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/workspaces/"+workspaceID+"/current-assignment/course-update" {
			writeTestAPIError(writer, http.StatusConflict, "course_update_unavailable")
			return
		}
		if request.URL.Path != "/v1/workspaces/"+workspaceID+"/current-assignment" {
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
		writeTestJSON(writer, map[string]any{
			"workspace": map[string]any{
				"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
				"base_revision_id": baseRevisionID, "support_mode": nil, "created_at": time.Now(), "updated_at": time.Now(),
			},
			"assignment": map[string]any{
				"id": "pa-foundation-06", "title": "Notification boundary", "version": 1, "state": "available",
				"local_checks": testLocalChecks(),
			},
		})
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-05", 2)
	t.Chdir(root)
	var output, errorOutput bytes.Buffer
	err := updateProject(context.Background(), client, nil, &output, &errorOutput)
	if err == nil || !strings.Contains(err.Error(), "course_update_unavailable") {
		t.Fatalf("update error = %v", err)
	}
}

func TestUpdateCommandLeavesProjectUntouchedWhenPolicyDeniesCurrentLesson(t *testing.T) {
	workspaceID := uuid.NewString()
	archiveRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, map[string]any{
				"workspace": map[string]any{
					"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
					"base_revision_id": nil, "support_mode": nil, "created_at": time.Now(), "updated_at": time.Now(),
				},
				"assignment": map[string]any{
					"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available",
					"local_checks": testLocalChecks(),
				},
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment/course-update":
			writeTestAPIError(writer, http.StatusForbidden, "subscription_required")
		case "/v1/workspaces/" + workspaceID + "/current-assignment/course-update/archive":
			archiveRequested = true
			http.Error(writer, "must not download", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	t.Chdir(root)

	var output, errorOutput bytes.Buffer
	err := updateProject(context.Background(), client, nil, &output, &errorOutput)
	if err == nil || !strings.Contains(err.Error(), "subscription_required") {
		t.Fatalf("update error = %v", err)
	}
	if archiveRequested {
		t.Fatal("CLI downloaded an update after policy denied it")
	}
	link, err := learnercli.LoadProjectLink(root)
	if err != nil || link.AssignmentID != "pa-foundation-01" || link.AssignmentVersion != 1 {
		t.Fatalf("policy denial changed project link: %+v, %v", link, err)
	}
	repository, err := inspectGitRepository(context.Background(), root, false)
	if err != nil || !repository.Clean {
		t.Fatalf("policy denial changed project tree: %+v, %v", repository, err)
	}
}

func TestUpdateMigratesLegacyProjectWithoutLocalChecks(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-02", 1)
	checksPath := filepath.Join(root, filepath.FromSlash(".softpractice/checks.json"))
	if err := os.Remove(checksPath); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "-u"}, {"commit", "--quiet", "-m", "legacy project"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/workspaces/"+workspaceID+"/current-assignment" {
			http.NotFound(writer, request)
			return
		}
		writeTestJSON(writer, map[string]any{
			"workspace":         map[string]any{"id": workspaceID, "project_id": "equipment-rental-python", "state": "active", "base_revision_id": nil, "created_at": time.Now(), "updated_at": time.Now()},
			"assignment":        map[string]any{"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available", "local_checks": testLocalChecks()},
			"latest_submission": nil,
		})
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	var output bytes.Buffer
	if err := updateLinkedProject(context.Background(), client, root, "", &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checksPath); err != nil {
		t.Fatalf("migrated checks: %v", err)
	}
	repository, err := inspectGitRepository(context.Background(), root, false)
	if err != nil || !repository.Clean {
		t.Fatalf("migrated repository = %+v, %v", repository, err)
	}
	if !strings.Contains(output.String(), "Public checks configuration was added") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSubmitDoesNotUploadWhenLocalCheckFails(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-02", 1)
	writeChecksAndCommit(t, root, `{"schema_version":1,"checks":[{"name":"public tests","executable":"git","args":["not-a-softpractice-command"],"timeout_seconds":30}]}`)
	client, uploads, closeServer := submitTestClient(t, workspaceID)
	defer closeServer()
	var output bytes.Buffer
	err := submit(context.Background(), client, root, []string{"--yes", "--checks"}, strings.NewReader(""), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "public tests") {
		t.Fatalf("submit error = %v", err)
	}
	if *uploads != 0 {
		t.Fatalf("uploads = %d", *uploads)
	}
}

func TestSubmitRejectsHeadChangedByLocalCheck(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-02", 1)
	if err := os.WriteFile(filepath.Join(root, "second.py"), []byte("pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", root, "add", "second.py").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", root, "commit", "--quiet", "-m", "second").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	writeChecksAndCommit(t, root, fmt.Sprintf(`{"schema_version":1,"checks":[{"name":"move HEAD","executable":"git","args":["-C",%q,"reset","--soft","HEAD~"],"timeout_seconds":30}]}`, root))
	client, uploads, closeServer := submitTestClient(t, workspaceID)
	defer closeServer()
	var output bytes.Buffer
	err := submit(context.Background(), client, root, []string{"--yes", "--checks"}, strings.NewReader(""), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "HEAD or the working tree changed") {
		t.Fatalf("submit error = %v", err)
	}
	if *uploads != 0 {
		t.Fatalf("uploads = %d", *uploads)
	}
}

func writeChecksAndCommit(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, ".softpractice", "checks.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", root, "add", "--force", "--", ".softpractice/checks.json").CombinedOutput(); err != nil {
		t.Fatalf("stage checks: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", root, "commit", "--quiet", "-m", "checks").CombinedOutput(); err != nil {
		t.Fatalf("commit checks: %v: %s", err, output)
	}
}

func submitTestClient(t *testing.T, workspaceID string) (*learnercli.Client, *int, func()) {
	t.Helper()
	uploads := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/workspaces/"+workspaceID+"/current-assignment" {
			writeTestJSON(writer, map[string]any{
				"workspace":         map[string]any{"id": workspaceID, "project_id": "equipment-rental-python", "state": "active", "base_revision_id": nil, "created_at": time.Now(), "updated_at": time.Now()},
				"assignment":        map[string]any{"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available", "local_checks": testLocalChecks()},
				"latest_submission": nil,
			})
			return
		}
		*uploads++
		writer.WriteHeader(http.StatusCreated)
	}))
	return savedTestClient(t, server.URL), uploads, server.Close
}

func TestCourseUpdateStopsWhenWorktreeChangesDuringDownload(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	updateRoot := t.TempDir()
	contents := []byte("lesson two\n")
	if err := os.WriteFile(filepath.Join(updateRoot, "lesson_two.py"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	var archive bytes.Buffer
	metadata, err := starterbundle.Build(updateRoot, []starterbundle.File{{
		Path: "lesson_two.py", SHA256: hex.EncodeToString(digest[:]),
	}}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	baseRevisionID := uuid.NewString()
	baseContentSHA256 := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		if request.URL.Path != "/v1/workspaces/"+workspaceID+"/current-assignment/course-update/archive" {
			http.NotFound(writer, request)
			return
		}
		if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("student note\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/gzip")
		writer.Header().Set("Content-Length", fmt.Sprint(metadata.Size))
		writer.Header().Set("X-Softpractice-Course-Update-SHA256", metadata.SHA256)
		writer.Header().Set("X-Softpractice-Course-Update-Ref", "test-update@1.0.0")
		writer.Header().Set("X-Softpractice-Course-Update-Base-Revision-ID", baseRevisionID)
		writer.Header().Set("X-Softpractice-Course-Update-Base-Content-SHA256", baseContentSHA256)
		_, _ = writer.Write(archive.Bytes())
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	link, err := learnercli.LoadProjectLink(root)
	if err != nil {
		t.Fatal(err)
	}
	update := learnercli.CourseUpdate{
		Ref: "test-update@1.0.0", FromAssignmentID: "pa-foundation-01", FromAssignmentVersion: 1,
		ToAssignmentID: "pa-foundation-02", ToAssignmentVersion: 1,
		BaseRevisionID: baseRevisionID, BaseContentSHA256: baseContentSHA256,
		ArchiveSHA256: metadata.SHA256, ArchiveSize: metadata.Size,
		Files: []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{{Path: "lesson_two.py", SHA256: hex.EncodeToString(digest[:])}},
		Operations: []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{{Kind: "add", Path: "lesson_two.py"}},
	}
	err = downloadAndApplyCourseUpdate(context.Background(), client, root, link, update, []byte(`{"schema_version":1,"checks":[{"name":"test","executable":"git","args":["--version"],"timeout_seconds":30}]}`),
		"/v1/workspaces/"+link.WorkspaceID+"/current-assignment/course-update/archive?format=tar.gz")
	if err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("update error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lesson_two.py")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("course update file was applied despite concurrent edit: %v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, "notes.txt")); err != nil || string(contents) != "student note\n" {
		t.Fatalf("student file = %q, %v", contents, err)
	}
}

func TestSubmissionIdempotencyIncludesBaseRevision(t *testing.T) {
	link := learnercli.ProjectLink{
		SchemaVersion: 1, WorkspaceID: uuid.NewString(), ProjectID: "equipment-rental-python",
	}
	var workspace learnercli.WorkspaceStatus
	workspace.Assignment.ID = "pa-foundation-05"
	workspace.Assignment.Version = 2
	firstBase := uuid.NewString()
	workspace.Workspace.BaseRevisionID = &firstBase
	_, _, first := submissionRequest(link, workspace, strings.Repeat("a", 40))
	_, _, replay := submissionRequest(link, workspace, strings.Repeat("a", 40))
	secondBase := uuid.NewString()
	workspace.Workspace.BaseRevisionID = &secondBase
	_, _, rebased := submissionRequest(link, workspace, strings.Repeat("a", 40))
	if first != replay || first == rebased {
		t.Fatalf("idempotency identities: first=%s replay=%s rebased=%s", first, replay, rebased)
	}
}

func TestGitTreeRejectsLinksAndOversizedFiles(t *testing.T) {
	for _, test := range []struct {
		name   string
		record string
	}{
		{"symlink", "120000 blob " + strings.Repeat("a", 40) + " 5\tlink"},
		{"submodule", "160000 commit " + strings.Repeat("a", 40) + " -\tvendor/module"},
		{"oversized", "100644 blob " + strings.Repeat("a", 40) + " 5242881\tlarge.bin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseGitTreeRecord(test.record); err == nil {
				t.Fatalf("parseGitTreeRecord(%q) accepted", test.record)
			}
		})
	}
}

func createLinkedGitRepository(t *testing.T, workspaceID string) string {
	t.Helper()
	return createGitRepository(t, `{
  "schema_version": 1,
  "workspace_id": "`+workspaceID+`",
  "project_id": "equipment-rental-python"
}
`)
}

func createPinnedLinkedGitRepository(t *testing.T, workspaceID, assignmentID string, assignmentVersion int) string {
	t.Helper()
	return createGitRepository(t, fmt.Sprintf(`{
  "schema_version": 2,
  "workspace_id": %q,
  "project_id": "equipment-rental-python",
  "assignment_id": %q,
  "assignment_version": %d
}
`, workspaceID, assignmentID, assignmentVersion))
}

func createGitRepository(t *testing.T, project string) string {
	t.Helper()
	root := t.TempDir()
	projectDirectory := filepath.Join(root, ".softpractice")
	if err := os.Mkdir(projectDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, "project.json"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := `{
  "schema_version": 1,
  "checks": [{"name":"test check","executable":"git","args":["--version"],"timeout_seconds":30}]
}
`
	if err := os.WriteFile(filepath.Join(projectDirectory, "checks.json"), []byte(checks), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("print('hello')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "--quiet", "-m", "initial"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	return root
}

func savedTestClient(t *testing.T, apiURL string) *learnercli.Client {
	t.Helper()
	store := learnercli.CredentialStore{
		Path:    filepath.Join(t.TempDir(), "credentials.json"),
		Secrets: &mainMemorySecretStore{values: map[string]string{}},
	}
	client, err := learnercli.NewClient(apiURL, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SaveCredentials(learnercli.Credentials{
		APIURL: apiURL, AccessToken: "access-token", AccessExpiresAt: time.Now().Add(time.Hour),
		RefreshToken: "refresh-token", RefreshIdleExpiresAt: time.Now().Add(time.Hour), RefreshAbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return client
}

func writeTestJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		panic(err)
	}
}

func testLocalChecks() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"checks": []any{map[string]any{
			"name": "test check", "executable": "git", "args": []string{"--version"}, "timeout_seconds": 30,
		}},
	}
}

func decodeTestJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeTestAPIError(writer http.ResponseWriter, status int, code string) {
	writer.WriteHeader(status)
	writeTestJSON(writer, map[string]any{
		"error":      map[string]string{"code": code, "message": code},
		"request_id": uuid.NewString(),
	})
}

type mainMemorySecretStore struct{ values map[string]string }

func (s *mainMemorySecretStore) Get(service, account string) (string, error) {
	value, ok := s.values[service+"\x00"+account]
	if !ok {
		return "", errors.New("secret not found")
	}
	return value, nil
}

func (s *mainMemorySecretStore) Set(service, account, secret string) error {
	s.values[service+"\x00"+account] = secret
	return nil
}

func (s *mainMemorySecretStore) Delete(service, account string) error {
	delete(s.values, service+"\x00"+account)
	return nil
}

func TestUpdateInvalidChecksLeavesProjectUnchanged(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	updateRoot := t.TempDir()
	contents := []byte("lesson two\n")
	if err := os.WriteFile(filepath.Join(updateRoot, "lesson_two.py"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	var archive bytes.Buffer
	metadata, err := starterbundle.Build(updateRoot, []starterbundle.File{{
		Path: "lesson_two.py", SHA256: hex.EncodeToString(digest[:]),
	}}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	baseRevisionID := uuid.NewString()
	baseContentSHA256 := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		if request.URL.Path != "/v1/workspaces/"+workspaceID+"/current-assignment/course-update/archive" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/gzip")
		writer.Header().Set("Content-Length", fmt.Sprint(metadata.Size))
		writer.Header().Set("X-Softpractice-Course-Update-SHA256", metadata.SHA256)
		writer.Header().Set("X-Softpractice-Course-Update-Ref", "test-update@1.0.0")
		writer.Header().Set("X-Softpractice-Course-Update-Base-Revision-ID", baseRevisionID)
		writer.Header().Set("X-Softpractice-Course-Update-Base-Content-SHA256", baseContentSHA256)
		_, _ = writer.Write(archive.Bytes())
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	link, err := learnercli.LoadProjectLink(root)
	if err != nil {
		t.Fatal(err)
	}
	update := learnercli.CourseUpdate{
		Ref: "test-update@1.0.0", FromAssignmentID: "pa-foundation-01", FromAssignmentVersion: 1,
		ToAssignmentID: "pa-foundation-02", ToAssignmentVersion: 1,
		BaseRevisionID: baseRevisionID, BaseContentSHA256: baseContentSHA256,
		ArchiveSHA256: metadata.SHA256, ArchiveSize: metadata.Size,
		Files: []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{{Path: "lesson_two.py", SHA256: hex.EncodeToString(digest[:])}},
		Operations: []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{{Kind: "add", Path: "lesson_two.py"}},
	}
	err = downloadAndApplyCourseUpdate(context.Background(), client, root, link, update, []byte(`{"schema_version":2,"checks":[]}`),
		"/v1/workspaces/"+link.WorkspaceID+"/current-assignment/course-update/archive?format=tar.gz")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported schema: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lesson_two.py")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lesson files changed: %v", err)
	}
	changed, err := learnercli.LoadProjectLink(root)
	if err != nil || changed.AssignmentID != link.AssignmentID {
		t.Fatalf("link changed: %+v %v", changed, err)
	}
	repo, err := inspectGitRepository(context.Background(), root, false)
	if err != nil || !repo.Clean {
		t.Fatalf("tree changed: %+v %v", repo, err)
	}
}

func TestSubmitChecksOnlyArchivedSources(t *testing.T) {
	for _, language := range []string{"python", "go"} {
		t.Run(language, func(t *testing.T) {
			executable := "go"
			if language == "python" {
				executable = "python3"
			}
			if _, err := exec.LookPath(executable); err != nil {
				t.Skip(err)
			}
			id := uuid.NewString()
			root := createPinnedLinkedGitRepository(t, id, "pa-foundation-02", 1)
			files := map[string]string{".gitignore": "helper.go\nhelper.py\n__pycache__/\n.pytest_cache/\n"}
			args := []string{"test", "./..."}
			ignored := "helper.go"
			if language == "go" {
				files["go.mod"] = "module example.com/publiccheck\n\ngo 1.22\n"
				files["helper.go"] = "package example\nfunc answer() int {return 42}\n"
				files["public_test.go"] = "package example\nimport \"testing\"\nfunc TestAnswer(t *testing.T) {if answer()!=42 {t.Fatal(\"answer\")}}\n"
			} else {
				args = []string{"-m", "unittest", "discover"}
				ignored = "helper.py"
				files["helper.py"] = "VALUE = 42\n"
				files["test_public.py"] = "import unittest\nfrom helper import VALUE\nclass PublicTest(unittest.TestCase):\n    def test_value(self):\n        self.assertEqual(VALUE, 42)\n"
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := exec.Command("git", "-C", root, "add", ".").CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			body, _ := json.Marshal(map[string]any{"schema_version": 1, "checks": []any{map[string]any{"name": "public tests", "executable": executable, "args": args, "timeout_seconds": 60}}})
			writeChecksAndCommit(t, root, string(body))
			cmd := exec.Command(executable, args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("worktree tests: %v %s", err, out)
			}
			client, uploads, closeServer := submitTestClient(t, id)
			defer closeServer()
			var output bytes.Buffer
			err := submit(context.Background(), client, root, []string{"--yes", "--checks"}, strings.NewReader(""), &output, &output)
			if err == nil || *uploads != 0 || !strings.Contains(err.Error(), "public tests") {
				t.Fatalf("missing archived source: uploads=%d err=%v output=%s", *uploads, err, &output)
			}
			if out, err := exec.Command("git", "-C", root, "add", "--force", ignored).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			if out, err := exec.Command("git", "-C", root, "commit", "-qm", "include helper").CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			_ = submit(context.Background(), client, root, []string{"--yes", "--checks"}, strings.NewReader(""), &output, &output)
			if *uploads != 1 {
				t.Fatalf("complete snapshot was not uploaded: %s", &output)
			}
		})
	}
}

func TestMetadataWriteFailureRollsBackCourseUpdate(t *testing.T) {
	root := createPinnedLinkedGitRepository(t, uuid.NewString(), "pa-foundation-01", 1)
	stage := t.TempDir()
	original := make(map[string]string)
	for _, path := range []string{".softpractice/checks.json", ".softpractice/project.json"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		original[path] = string(data)
	}
	if err := os.MkdirAll(filepath.Join(stage, ".softpractice"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{"next.py", ".softpractice/checks.json", ".softpractice/project.json"}
	var update learnercli.CourseUpdate
	for i, path := range paths {
		if err := os.WriteFile(filepath.Join(stage, path), []byte("updated"), 0600); err != nil {
			t.Fatal(err)
		}
		kind := "replace"
		if i == 0 {
			kind = "add"
		}
		update.Files = append(update.Files, struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{Path: path})
		update.Operations = append(update.Operations, struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{Kind: kind, Path: path})
	}
	failure := errors.New("injected metadata write failure")
	err := applyCourseUpdateWithWriter(context.Background(), root, stage, update, func(root *os.Root, path string, data []byte, mode os.FileMode) error {
		if err := root.WriteFile(filepath.FromSlash(path), data, mode); err != nil {
			return err
		}
		if path == ".softpractice/project.json" {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	for path, prior := range original {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(data) != prior {
			t.Fatalf("metadata %s not restored: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "next.py")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("added lesson file not rolled back: %v", err)
	}
	repo, err := inspectGitRepository(context.Background(), root, false)
	if err != nil || !repo.Clean {
		t.Fatalf("dirty rollback: %+v %v", repo, err)
	}
}

func TestCheckSnapshotRejectsChangedSource(t *testing.T) {
	root := createPinnedLinkedGitRepository(t, uuid.NewString(), "pa-foundation-02", 1)
	repo, err := inspectGitRepository(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(repo.ArchivePath)
	snapshot, err := prepareCheckSnapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(snapshot.root)
	if err := os.WriteFile(filepath.Join(snapshot.root, "main.py"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.verify(); err == nil {
		t.Fatal("changed snapshot passed verification")
	}
}

func TestSubmitOptionalChecks(t *testing.T) {
	for _, test := range []struct {
		name        string
		configured  bool
		args        []string
		wantUploads int
	}{
		{"default off", false, []string{"--yes"}, 1},
		{"enabled in project", true, []string{"--yes"}, 0},
		{"explicit opt in", false, []string{"--yes", "--checks"}, 0},
		{"explicit opt out", true, []string{"--yes", "--checks=false"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := uuid.NewString()
			root := createPinnedLinkedGitRepository(t, id, "pa-foundation-02", 1)
			writeChecksAndCommit(t, root, `{"schema_version":1,"checks":[{"name":"missing tool","executable":"softpractice-no-such-executable","timeout_seconds":1}]}`)
			if test.configured {
				if _, err := gitOutput(context.Background(), root, "config", "--local", autoChecksKey, "true"); err != nil {
					t.Fatal(err)
				}
			}
			client, uploads, closeServer := submitTestClient(t, id)
			defer closeServer()
			var output bytes.Buffer
			err := submit(context.Background(), client, root, test.args, strings.NewReader(""), &output, &output)
			if *uploads != test.wantUploads {
				t.Fatalf("uploads=%d error=%v output=%s", *uploads, err, &output)
			}
			if test.wantUploads == 1 && !strings.Contains(output.String(), "Local checks were not run") {
				t.Fatalf("missing explanation: %s", &output)
			}
		})
	}
}
func TestAutoChecksSettingIsLocalAndNotSubmitted(t *testing.T) {
	root := createPinnedLinkedGitRepository(t, uuid.NewString(), "pa-foundation-02", 1)
	other := createPinnedLinkedGitRepository(t, uuid.NewString(), "pa-foundation-02", 1)
	t.Chdir(root)
	var output bytes.Buffer
	if err := setConfig(context.Background(), []string{"auto-checks", "true"}, &output); err != nil {
		t.Fatal(err)
	}
	enabled, err := projectAutoChecks(context.Background(), root)
	if err != nil || !enabled {
		t.Fatalf("setting=%v %v", enabled, err)
	}
	enabled, err = projectAutoChecks(context.Background(), other)
	if err != nil || enabled {
		t.Fatalf("setting leaked=%v %v", enabled, err)
	}
	repo, err := inspectGitRepository(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(repo.ArchivePath)
	if !repo.Clean {
		t.Fatal("setting dirtied project")
	}
	for _, file := range repo.Files {
		if strings.HasPrefix(file.Path, ".git/") {
			t.Fatal("Git settings entered submission")
		}
	}
	if err := setConfig(context.Background(), []string{"auto-checks", "false"}, &output); err != nil {
		t.Fatal(err)
	}
	enabled, err = projectAutoChecks(context.Background(), root)
	if err != nil || enabled {
		t.Fatalf("disable=%v %v", enabled, err)
	}
}

// lessonBumpServer answers for a workspace that a release moved from v1 to v2
// of the first lesson while the project folder still holds v1.
func lessonBumpServer(t *testing.T, workspaceID string, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		if request.URL.Path == "/v1/workspaces/"+workspaceID+"/current-assignment" {
			writeTestJSON(writer, map[string]any{
				"workspace": map[string]any{
					"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
					"base_revision_id": nil, "support_mode": nil, "created_at": now, "updated_at": now,
				},
				"assignment": map[string]any{
					"id": "pa-foundation-01", "version": 2, "title": "Pricing",
					"state": "available", "local_checks": testLocalChecks(),
				},
				"latest_submission": nil,
			})
			return
		}
		extra(writer, request)
	}))
}

func commitLearnerWork(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"add", "--all"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--no-verify", "-m", "work in progress"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", root}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	return path
}

// A text-only bump has an empty lesson version update: `update` rewrites the
// pin and the public checks in place, downloads nothing, and leaves the
// learner's unfinished work exactly as it is.
func TestUpdateRefreshesTheLessonVersionWithoutTouchingLearnerFiles(t *testing.T) {
	workspaceID := uuid.NewString()
	server := lessonBumpServer(t, workspaceID, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/workspaces/"+workspaceID+"/current-assignment/lesson-version-update" ||
			request.URL.Query().Get("from_version") != "1" {
			http.NotFound(writer, request)
			return
		}
		writeTestJSON(writer, map[string]any{
			"ref": "pa-foundation-01-v1-to-pa-foundation-01-v2@1.0.0", "assignment_id": "pa-foundation-01",
			"from_version": 1, "to_version": 2, "archive_size": 0, "files": []any{}, "operations": []any{},
		})
	})
	defer server.Close()
	client := savedTestClient(t, server.URL)
	repositoryRoot := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	learnerFile := commitLearnerWork(t, repositoryRoot, "solution.py", "# unfinished work\n")

	var output bytes.Buffer
	if err := updateLinkedProject(context.Background(), client, repositoryRoot, "", &output); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(output.String(), "v1 → v2") {
		t.Fatalf("update output = %q", output.String())
	}
	link, err := learnercli.LoadProjectLink(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if link.AssignmentID != "pa-foundation-01" || link.AssignmentVersion != 2 {
		t.Fatalf("project link = %+v, want the republished version", link)
	}
	contents, err := os.ReadFile(learnerFile)
	if err != nil || string(contents) != "# unfinished work\n" {
		t.Fatalf("learner file = %q, %v", contents, err)
	}
}

// A bump that refactored author-owned files ships them in the lesson version
// update. `update` replaces exactly those files and nothing the learner wrote.
func TestUpdateReplacesRefactoredAuthorFilesAndKeepsLearnerWork(t *testing.T) {
	workspaceID := uuid.NewString()
	payloadRoot := t.TempDir()
	refactored := []byte("# refactored author module\n")
	if err := os.WriteFile(filepath.Join(payloadRoot, "main.py"), refactored, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(refactored)
	var archive bytes.Buffer
	metadata, err := starterbundle.Build(payloadRoot, []starterbundle.File{
		{Path: "main.py", SHA256: hex.EncodeToString(digest[:])},
	}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	const ref = "pa-foundation-01-v1-to-pa-foundation-01-v2@1.0.0"
	server := lessonBumpServer(t, workspaceID, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/workspaces/" + workspaceID + "/current-assignment/lesson-version-update":
			writeTestJSON(writer, map[string]any{
				"ref": ref, "assignment_id": "pa-foundation-01", "from_version": 1, "to_version": 2,
				"archive_sha256": metadata.SHA256, "archive_size": metadata.Size,
				"files":      []any{map[string]any{"path": "main.py", "sha256": hex.EncodeToString(digest[:])}},
				"operations": []any{map[string]any{"kind": "replace", "path": "main.py"}},
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment/lesson-version-update/archive":
			if request.URL.Query().Get("from_version") != "1" {
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Content-Type", "application/gzip")
			writer.Header().Set("Content-Length", fmt.Sprint(metadata.Size))
			writer.Header().Set("X-Softpractice-Course-Update-SHA256", metadata.SHA256)
			writer.Header().Set("X-Softpractice-Course-Update-Ref", ref)
			_, _ = writer.Write(archive.Bytes())
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()
	client := savedTestClient(t, server.URL)
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	if output, err := exec.Command("git", "-C", root, "update-index", "--assume-unchanged", ".softpractice/project.json").CombinedOutput(); err != nil {
		t.Fatalf("ignore local project link in test repository: %v: %s", err, output)
	}
	learnerFile := commitLearnerWork(t, root, "solution.py", "# unfinished work\n")

	var output bytes.Buffer
	if err := updateLinkedProject(context.Background(), client, root, "", &output); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "main.py")); err != nil || string(content) != string(refactored) {
		t.Fatalf("author file = %q, %v; want the refactored bytes", content, err)
	}
	if content, err := os.ReadFile(learnerFile); err != nil || string(content) != "# unfinished work\n" {
		t.Fatalf("learner file = %q, %v", content, err)
	}
	link, err := learnercli.LoadProjectLink(root)
	if err != nil || link.AssignmentVersion != 2 {
		t.Fatalf("project link = %+v, %v", link, err)
	}
	if !strings.Contains(output.String(), "v1 → v2") || !strings.Contains(output.String(), "main.py") {
		t.Fatalf("update output = %q", output.String())
	}
	repository, err := inspectGitRepository(context.Background(), root, false)
	if err != nil || !repository.Clean {
		t.Fatalf("updated project is not clean: %+v, %v", repository, err)
	}
}

// A lesson version update never carries anything but replacements of lesson
// files; anything else leaves the project untouched.
func TestUpdateRefusesALessonVersionUpdateThatAddsFiles(t *testing.T) {
	workspaceID := uuid.NewString()
	server := lessonBumpServer(t, workspaceID, func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{
			"ref": "pa-foundation-01-v1-to-pa-foundation-01-v2@1.0.0", "assignment_id": "pa-foundation-01",
			"from_version": 1, "to_version": 2, "archive_sha256": strings.Repeat("a", 64), "archive_size": 10,
			"files":      []any{map[string]any{"path": "extra.py", "sha256": strings.Repeat("b", 64)}},
			"operations": []any{map[string]any{"kind": "add", "path": "extra.py"}},
		})
	})
	defer server.Close()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	var output bytes.Buffer
	err := updateLinkedProject(context.Background(), savedTestClient(t, server.URL), root, "", &output)
	if err == nil {
		t.Fatal("a lesson version update that adds files was applied")
	}
	if _, statErr := os.Stat(filepath.Join(root, "extra.py")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("extra file exists: %v", statErr)
	}
}

// A folder still on the retired version submits the version its tree was
// made for: the server picks the profile that accepts that tree, and the
// learner is told an update exists without being blocked by it.
func TestSubmitFromRetiredVersionNamesTheTreeVersionAndPrintsTheNotice(t *testing.T) {
	workspaceID := uuid.NewString()
	var submittedVersion string
	server := lessonBumpServer(t, workspaceID, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/workspaces/"+workspaceID+"/assignments/pa-foundation-01/submissions" {
			http.NotFound(writer, request)
			return
		}
		submittedVersion = request.Header.Get("X-Softpractice-Assignment-Version")
		_, _ = io.Copy(io.Discard, request.Body)
		submissionID := uuid.NewString()
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"submission_id": submissionID, "revision_id": uuid.NewString(), "evaluation_job_id": uuid.NewString(),
			"job_state": "queued", "replayed": false, "submitted_at": time.Now().UTC(),
			"submission_url": "/v1/submissions/" + submissionID, "evaluation_url": "/v1/submissions/" + submissionID + "/evaluation",
			"lesson_update": map[string]any{"assignment_id": "pa-foundation-01", "submitted_version": 1, "current_version": 2},
		})
	})
	defer server.Close()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	var output, errorOutput bytes.Buffer
	err := submit(context.Background(), savedTestClient(t, server.URL), root, []string{"--yes"},
		strings.NewReader(""), &output, &errorOutput)
	if err != nil {
		t.Fatalf("submit from the retired version: %v\n%s", err, errorOutput.String())
	}
	if submittedVersion != "1" {
		t.Fatalf("submitted assignment version = %q, want the version of the tree", submittedVersion)
	}
	if !strings.Contains(output.String(), "v1 → v2") || !strings.Contains(output.String(), "softpractice update") {
		t.Fatalf("submit output = %q, want the lesson update notice", output.String())
	}
}

// Only a pin older than the server's version is a republished lesson; a newer
// pin (rolled-back release, edited link) must not be offered as an update.
func TestPendingLessonVersionOnlyForAnOlderPin(t *testing.T) {
	link := learnercli.ProjectLink{SchemaVersion: 2, AssignmentID: "ga-foundation-01", AssignmentVersion: 1}
	var workspace learnercli.WorkspaceStatus
	workspace.Assignment.ID, workspace.Assignment.Version = "ga-foundation-01", 2
	if !pendingLessonVersion(link, workspace) {
		t.Fatal("older pin is not reported as a lesson update")
	}
	link.AssignmentVersion = 3
	if pendingLessonVersion(link, workspace) {
		t.Fatal("newer pin is reported as a lesson update")
	}
}
