package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

const reviewerDirectionMarker = "DIRECTION-TEXT-MUST-NOT-LEAK"

// fakeClock stands in for the wall clock and the sleep between polls.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) useCases(client *learnercli.Client, startDirectory string) learnerUseCases {
	useCases := newLearnerUseCases(client, startDirectory)
	useCases.now = func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.now
	}
	useCases.sleep = func(_ context.Context, delay time.Duration) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.sleeps = append(c.sleeps, delay)
		c.now = c.now.Add(delay)
		return nil
	}
	return useCases
}

func pendingEvaluationBody(submissionID string) map[string]any {
	return map[string]any{
		"submission_id": submissionID, "evaluation_job_id": "018f47a2-8b54-7d52-8f6f-1b4f01c61a25",
		"job_state": "leased", "attempt": 1, "max_attempts": 3, "next_poll_seconds": 2,
		"updated_at": "2026-07-29T09:32:03Z",
	}
}

func reviseEvaluationBody(submissionID string) map[string]any {
	return map[string]any{
		"submission_id": submissionID, "evaluation_job_id": "018f47a2-8b54-7d52-8f6f-1b4f01c61a25",
		"job_state": "completed", "attempt": 1, "max_attempts": 3,
		"updated_at": "2026-07-29T09:32:15Z",
		"evaluation": map[string]any{
			"id": "018f47a3-551a-7d52-8f6f-1b4f01c61a26", "schema_version": 1,
			"exercise_id": "pa-foundation-05", "evaluator_version": "0.7",
			"profile_id": "local", "profile_version": "4", "profile_sha256": strings.Repeat("a", 64),
			"status": "revise", "completed_at": "2026-07-29T09:32:15Z",
			"evidence": map[string]any{"sha256": strings.Repeat("b", 64)},
			"deterministic": map[string]any{"status": "passed", "checks": []any{
				map[string]any{"id": "open-scenarios", "kind": "public_tests", "status": "pass", "summary": "Open scenarios passed."},
				map[string]any{
					"id": "required-scenarios", "kind": "required_behavior", "status": "pass", "summary": "Required behavior confirmed.",
					"counterexample": nil,
				},
			}},
			"review": map[string]any{
				"projection_version": 1, "status": "completed", "mode": "llm",
				"authoritative": true, "verdict": "revise",
				"defense_questions": []any{"DEFENSE-QUESTION-TEXT"},
				"uncertainty":       map[string]any{"blocking": false, "reason": nil},
				"rubric": []any{
					map[string]any{"criterion": "responsibilities", "level": 1, "rationale": "RUBRIC-RATIONALE-TEXT", "evidence_refs": []any{"app.py"}},
					map[string]any{"criterion": "tests", "level": 2, "rationale": "RUBRIC-RATIONALE-TEXT", "evidence_refs": []any{"test_app.py"}},
				},
				"feedback": []any{
					map[string]any{
						"priority": "high", "title": "FINDING-TITLE-TEXT", "observation": "OBSERVATION-TEXT",
						"risk": "RISK-TEXT", "direction": reviewerDirectionMarker, "evidence_refs": []any{"app.py"},
					},
					map[string]any{
						"priority": "medium", "title": "FINDING-TITLE-TEXT", "observation": "OBSERVATION-TEXT",
						"risk": "RISK-TEXT", "direction": reviewerDirectionMarker, "evidence_refs": []any{"app.py"},
					},
				},
			},
			"technical_error": nil,
		},
	}
}

// evaluationServer answers the evaluation endpoint of submissionID with the
// scripted responses in order, repeating the last one.
func evaluationServer(t *testing.T, submissionID string, script ...func(http.ResponseWriter)) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/submissions/"+submissionID+"/evaluation" {
			http.NotFound(writer, request)
			return
		}
		step := script[min(calls, len(script)-1)]
		calls++
		step(writer)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func respondPending(retryAfter string) func(http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		writer.Header().Set("Retry-After", retryAfter)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(writer).Encode(pendingEvaluationBody(currentSubmissionID))
	}
}

func respondRevise(writer http.ResponseWriter) {
	writeTestJSON(writer, reviseEvaluationBody(currentSubmissionID))
}

// currentSubmissionID keeps the scripted handlers short; tests do not run in
// parallel.
var currentSubmissionID = "018f47a2-165d-7d52-8f6f-1b4f01c61a23"

func runEvaluation(t *testing.T, useCases learnerUseCases, args ...string) (string, string, error) {
	t.Helper()
	var output, errorOutput bytes.Buffer
	ctx := withSettings(context.Background(), runtimeSettings{
		Language: languageEnglish, WebURL: "https://softpractice.example",
	})
	err := evaluationCommand(ctx, useCases, args, &output, &errorOutput)
	return output.String(), errorOutput.String(), err
}

func TestEvaluationWaitHonoursRetryAfterAndHidesDirections(t *testing.T) {
	server, calls := evaluationServer(t, currentSubmissionID,
		respondPending("7"), respondPending("5"), respondRevise)
	clock := &fakeClock{now: time.Unix(0, 0)}
	useCases := clock.useCases(savedTestClient(t, server.URL), "")

	output, _, err := runEvaluation(t, useCases, "--id", currentSubmissionID, "--wait", "--timeout", "1m", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Fatalf("calls = %d, want 3", *calls)
	}
	if want := []time.Duration{7 * time.Second, 5 * time.Second}; !reflect.DeepEqual(clock.sleeps, want) {
		t.Fatalf("sleeps = %v, want %v", clock.sleeps, want)
	}
	for _, secret := range []string{
		reviewerDirectionMarker, "OBSERVATION-TEXT", "RISK-TEXT", "FINDING-TITLE-TEXT",
		"RUBRIC-RATIONALE-TEXT", "DEFENSE-QUESTION-TEXT", `"direction"`,
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("JSON output contains %q:\n%s", secret, output)
		}
	}
	var payload machineEvaluationResult
	if err := decodeTestJSON([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	review := payload.Feedback.Review
	if payload.Outcome != evaluationReady || payload.Evaluation.Status != "revise" ||
		payload.Feedback.Detail != reviewFeedbackSummary || review.FindingsCount != 2 ||
		review.FindingsByPriority["high"] != 1 || review.FindingsByPriority["medium"] != 1 ||
		*review.Verdict != "revise" || len(review.Rubric) != 2 || review.Rubric[0].Level != 1 ||
		payload.ResultURL != "https://softpractice.example/submissions/"+currentSubmissionID+"/result" {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	text, _, err := runEvaluation(t, useCases, "--id", currentSubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, reviewerDirectionMarker) || strings.Contains(text, "FINDING-TITLE-TEXT") ||
		!strings.Contains(text, "Reviewer findings: 2 (high 1, medium 1)") {
		t.Fatalf("unexpected text output:\n%s", text)
	}
}

func TestMachineReviewFeedbackDefaultsToSummary(t *testing.T) {
	if machineReviewFeedback != reviewFeedbackSummary {
		t.Fatalf("machineReviewFeedback = %q; reviewer directions must stay out of agent output by default", machineReviewFeedback)
	}
	raw, err := json.Marshal(reviseEvaluationBody(currentSubmissionID)["evaluation"])
	if err != nil {
		t.Fatal(err)
	}
	full, err := buildMachineFeedback(raw, reviewFeedbackFull)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Review.Findings) != 2 || full.Review.Findings[0].Direction != reviewerDirectionMarker {
		t.Fatalf("full detail lost the findings: %+v", full.Review)
	}
}

func TestEvaluationTimeoutReportsNotReady(t *testing.T) {
	server, calls := evaluationServer(t, currentSubmissionID, respondPending("30"))
	clock := &fakeClock{now: time.Unix(0, 0)}
	useCases := clock.useCases(savedTestClient(t, server.URL), "")

	output, _, err := runEvaluation(t, useCases, "--id", currentSubmissionID, "--wait", "--timeout", "45s", "--format", "json")
	if !errors.Is(err, exitNotReady) {
		t.Fatalf("err = %v, want exit status 3", err)
	}
	// It never polls sooner than Retry-After and stops when the next allowed
	// poll would fall after the timeout.
	if *calls != 2 || !reflect.DeepEqual(clock.sleeps, []time.Duration{30 * time.Second}) {
		t.Fatalf("calls = %d, sleeps = %v", *calls, clock.sleeps)
	}
	var payload machineEvaluationResult
	if err := decodeTestJSON([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Outcome != evaluationPending || payload.Terminal || payload.JobState != "leased" ||
		payload.Evaluation != nil || payload.Feedback != nil {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	// Without --wait a pending evaluation is the same outcome after one poll.
	_, _, err = runEvaluation(t, useCases, "--id", currentSubmissionID)
	if !errors.Is(err, exitNotReady) || *calls != 3 {
		t.Fatalf("err = %v, calls = %d", err, *calls)
	}
}

func TestEvaluationWaitsOutRateLimit(t *testing.T) {
	server, _ := evaluationServer(t, currentSubmissionID,
		respondPending("2"),
		func(writer http.ResponseWriter) {
			writer.Header().Set("Retry-After", "11")
			writeTestAPIError(writer, http.StatusTooManyRequests, "rate_limited")
		},
		respondRevise)
	clock := &fakeClock{now: time.Unix(0, 0)}
	useCases := clock.useCases(savedTestClient(t, server.URL), "")
	if _, _, err := runEvaluation(t, useCases, "--id", currentSubmissionID, "--wait"); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{2 * time.Second, 11 * time.Second}; !reflect.DeepEqual(clock.sleeps, want) {
		t.Fatalf("sleeps = %v, want %v", clock.sleeps, want)
	}
}

func TestEvaluationSupersededIsASeparateOutcome(t *testing.T) {
	server, _ := evaluationServer(t, currentSubmissionID, func(writer http.ResponseWriter) {
		writeTestAPIError(writer, http.StatusConflict, "evaluation_superseded")
	})
	clock := &fakeClock{now: time.Unix(0, 0)}
	output, _, err := runEvaluation(t, clock.useCases(savedTestClient(t, server.URL), ""),
		"--id", currentSubmissionID, "--wait", "--format", "json")
	if !errors.Is(err, exitSuperseded) {
		t.Fatalf("err = %v, want exit status 4", err)
	}
	var payload machineEvaluationResult
	if err := decodeTestJSON([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Outcome != evaluationSuperseded || payload.JobState != "superseded" || len(clock.sleeps) != 0 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestEvaluationUnauthorizedAsksForLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
	}))
	defer server.Close()
	output, _, err := runEvaluation(t, newLearnerUseCases(savedTestClient(t, server.URL), ""),
		"--id", currentSubmissionID, "--format", "json")
	if err == nil {
		t.Fatal("expected an error")
	}
	var payload machineError
	if err := decodeTestJSON([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Kind != "softpractice.error" || payload.Error.Code != "login_required" || payload.Error.HTTPStatus != 401 {
		t.Fatalf("unexpected error payload: %+v", payload)
	}
	if strings.Contains(output, "access-token") || strings.Contains(output, "refresh-token") {
		t.Fatalf("error output leaks a token:\n%s", output)
	}

	// With no stored login at all the same code is reported and nothing
	// reaches the API.
	store := learnercli.CredentialStore{
		Path: filepath.Join(t.TempDir(), "credentials.json"), Secrets: &mainMemorySecretStore{values: map[string]string{}},
	}
	client, err := learnercli.NewClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	output, _, err = runEvaluation(t, newLearnerUseCases(client, ""), "--id", currentSubmissionID, "--format", "json")
	if !errors.Is(err, learnercli.ErrLoginRequired) || !strings.Contains(output, `"code": "login_required"`) {
		t.Fatalf("err = %v, output:\n%s", err, output)
	}
}

func TestAgentCommandsWithoutLinkedProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("unexpected API request %s", request.URL.Path)
		http.NotFound(writer, request)
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)

	unlinked := t.TempDir()
	if output, err := exec.Command("git", "-C", unlinked, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", unlinked,
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "--quiet", "--allow-empty", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	for _, tc := range []struct {
		directory string
		code      string
	}{
		{directory: unlinked, code: "project_not_linked"},
		{directory: t.TempDir(), code: "not_git_repository"},
	} {
		output, _, err := runEvaluation(t, newLearnerUseCases(client, tc.directory), "--format", "json")
		if err == nil || !strings.Contains(output, `"code": "`+tc.code+`"`) {
			t.Fatalf("%s: err = %v, output:\n%s", tc.code, err, output)
		}
		var assignmentOutput, errorOutput bytes.Buffer
		err = assignmentCommand(context.Background(), newLearnerUseCases(client, tc.directory),
			[]string{"show", "--format", "json"}, &assignmentOutput, &errorOutput)
		if err == nil || !strings.Contains(assignmentOutput.String(), `"code": "`+tc.code+`"`) {
			t.Fatalf("assignment %s: err = %v, output:\n%s", tc.code, err, assignmentOutput.String())
		}
	}
}

func assignmentServer(t *testing.T, workspaceID string, withMaterial bool) *httptest.Server {
	t.Helper()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v1/workspaces/"+workspaceID+"/current-assignment":
			writeTestJSON(writer, map[string]any{
				"workspace": map[string]any{
					"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
					"base_revision_id": nil, "support_mode": nil, "created_at": now, "updated_at": now,
				},
				"assignment": map[string]any{
					"id": "pa-foundation-01", "version": 2, "title": "Price rule",
					"state": "available", "local_checks": testLocalChecks(),
				},
				"latest_submission": nil,
			})
		case request.URL.Path == "/v1/assignments/pa-foundation-01" && request.URL.Query().Get("version") == "1":
			writeTestJSON(writer, map[string]any{
				"id": "pa-foundation-01", "version": 1, "title": "Price rule", "kind": "foundation",
				"project_setup": "download", "exercise_id": "pa-foundation-01", "estimated_minutes": 45,
				"instructions_markdown": "Extract the price rule.", "starter_ref": "starter@1.0.0",
				"content_sha256": strings.Repeat("c", 64), "published_at": now,
			})
		case request.URL.Path == "/v1/assignments/pa-foundation-01/material" && request.URL.Query().Get("version") == "1":
			if !withMaterial {
				writeTestAPIError(writer, http.StatusNotFound, "resource_not_found")
				return
			}
			writeTestJSON(writer, map[string]any{
				"id": "pa-theory-rules", "title": "Rules", "estimated_minutes": 6,
				"markdown": "# Rules\n\nTheory text.\n",
				"diagram": map[string]any{"title": "Flow", "text_alternative": "A calls B.", "nodes": []any{
					map[string]any{"title": "A", "text": "a"}, map[string]any{"title": "B", "text": "b"},
				}},
			})
		default:
			t.Errorf("unexpected API request %s?%s", request.URL.Path, request.URL.RawQuery)
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAssignmentShowReadsTheFolderLessonVersion(t *testing.T) {
	workspaceID := uuid.NewString()
	// The folder is pinned to v1 while the server has published v2: the
	// requirement the tree is being written for is v1.
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	for _, withMaterial := range []bool{true, false} {
		server := assignmentServer(t, workspaceID, withMaterial)
		var output, errorOutput bytes.Buffer
		if err := assignmentCommand(context.Background(), newLearnerUseCases(savedTestClient(t, server.URL), root),
			[]string{"show", "--format", "json"}, &output, &errorOutput); err != nil {
			t.Fatal(err)
		}
		var payload machineAssignment
		if err := decodeTestJSON(output.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Kind != "softpractice.assignment" || payload.Assignment.Version != 1 ||
			payload.Assignment.InstructionsMarkdown != "Extract the price rule." ||
			payload.LessonVersionUpdate == nil || payload.LessonVersionUpdate.ToVersion != 2 {
			t.Fatalf("unexpected payload: %+v", payload)
		}
		if withMaterial != (payload.Material != nil) {
			t.Fatalf("material = %+v, want present=%t", payload.Material, withMaterial)
		}
		if !withMaterial && !strings.Contains(output.String(), `"material": null`) {
			t.Fatalf("absent material must be null:\n%s", output.String())
		}
	}
}

// jsonKeyPaths lists every object key path in a JSON document, with array
// elements folded into "[]", so a test can pin the shape of a contract.
func jsonKeyPaths(t *testing.T, data []byte) []string {
	t.Helper()
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var walk func(prefix string, value any)
	walk = func(prefix string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				seen[path] = true
				walk(path, child)
			}
		case []any:
			for _, child := range typed {
				walk(prefix+"[]", child)
			}
		}
	}
	walk("", document)
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// TestAgentJSONContractsAreStable pins the key sets of the agent-facing JSON
// documents. A change here is a contract change: add keys only, and bump
// contract_version when a key is removed, renamed, or changes meaning.
func TestAgentJSONContractsAreStable(t *testing.T) {
	server, _ := evaluationServer(t, currentSubmissionID, respondRevise)
	clock := &fakeClock{now: time.Unix(0, 0)}
	output, _, err := runEvaluation(t, clock.useCases(savedTestClient(t, server.URL), ""),
		"--id", currentSubmissionID, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"attempt", "contract_version", "evaluation", "evaluation.deterministic_status",
		"evaluation.exercise_id", "evaluation.review", "evaluation.review.authoritative",
		"evaluation.review.mode", "evaluation.review.status", "evaluation.review.verdict",
		"evaluation.schema_version", "evaluation.status", "evaluation_job_id",
		"feedback", "feedback.detail", "feedback.deterministic", "feedback.deterministic.checks",
		"feedback.deterministic.checks[].has_counterexample", "feedback.deterministic.checks[].id",
		"feedback.deterministic.checks[].kind", "feedback.deterministic.checks[].status",
		"feedback.deterministic.checks[].summary", "feedback.deterministic.status",
		"feedback.review", "feedback.review.authoritative", "feedback.review.blocking_uncertainty",
		"feedback.review.findings_by_priority", "feedback.review.findings_by_priority.high",
		"feedback.review.findings_by_priority.medium", "feedback.review.findings_count",
		"feedback.review.mode", "feedback.review.rubric", "feedback.review.rubric[].criterion",
		"feedback.review.rubric[].level", "feedback.review.status", "feedback.review.verdict",
		"job_state", "kind", "max_attempts", "outcome", "result_path", "result_url",
		"submission_id", "terminal", "updated_at",
	}
	if got := jsonKeyPaths(t, []byte(output)); !reflect.DeepEqual(got, want) {
		t.Fatalf("softpractice.evaluation keys changed:\ngot  %q\nwant %q", got, want)
	}

	var errorOutput bytes.Buffer
	if err := writeMachineError(&errorOutput, &learnercli.HTTPError{
		Status: 503, Code: "service_unavailable", Message: "down", SupportID: uuid.NewString(),
	}); err != nil {
		t.Fatal(err)
	}
	wantError := []string{
		"contract_version", "error", "error.api_code", "error.code", "error.http_status",
		"error.message", "error.support_id", "kind",
	}
	if got := jsonKeyPaths(t, errorOutput.Bytes()); !reflect.DeepEqual(got, wantError) {
		t.Fatalf("softpractice.error keys changed:\ngot  %q\nwant %q", got, wantError)
	}

	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	var assignmentOutput bytes.Buffer
	if err := assignmentCommand(context.Background(),
		newLearnerUseCases(savedTestClient(t, assignmentServer(t, workspaceID, true).URL), root),
		[]string{"show", "--format", "json"}, &assignmentOutput, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	wantAssignment := []string{
		"assignment", "assignment.content_sha256", "assignment.estimated_minutes",
		"assignment.exercise_id", "assignment.id", "assignment.instructions_markdown",
		"assignment.kind", "assignment.project_setup", "assignment.title", "assignment.version",
		"contract_version", "kind", "lesson_version_update", "lesson_version_update.assignment_id",
		"lesson_version_update.from_version", "lesson_version_update.to_version", "material",
		"material.diagram", "material.diagram.nodes", "material.diagram.nodes[].text",
		"material.diagram.nodes[].title", "material.diagram.text_alternative", "material.diagram.title",
		"material.estimated_minutes", "material.id", "material.markdown", "material.title",
		"project_id", "workspace_id",
	}
	if got := jsonKeyPaths(t, assignmentOutput.Bytes()); !reflect.DeepEqual(got, wantAssignment) {
		t.Fatalf("softpractice.assignment keys changed:\ngot  %q\nwant %q", got, wantAssignment)
	}
}

func TestStatusJSONReportsTitleAndLatestEvaluationStatus(t *testing.T) {
	workspaceID := uuid.NewString()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/me":
			writeTestJSON(writer, map[string]any{
				"id": uuid.NewString(), "email": "ada@example.com", "display_name": "Ada",
				"email_verified": true, "cli_last_session_at": nil, "created_at": now,
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, map[string]any{
				"workspace": map[string]any{
					"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
					"base_revision_id": nil, "support_mode": nil, "created_at": now, "updated_at": now,
				},
				"assignment": map[string]any{
					"id": "pa-foundation-05", "version": 1, "title": "Module boundaries",
					"state": "submitted", "local_checks": testLocalChecks(),
				},
				"latest_submission": map[string]any{
					"id": currentSubmissionID, "revision_id": uuid.NewString(),
					"job_state": "completed", "submitted_at": now,
				},
			})
		case "/v1/submissions/" + currentSubmissionID + "/evaluation":
			respondRevise(writer)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-05", 1)
	var output, errorOutput bytes.Buffer
	if err := status(context.Background(), savedTestClient(t, server.URL), root,
		[]string{"--format", "json"}, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	var payload machineStatus
	if err := decodeTestJSON(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Assignment.Title != "Module boundaries" || payload.LatestSubmission == nil ||
		payload.LatestSubmission.EvaluationStatus != "revise" || payload.Transition != nil ||
		payload.LessonVersionUpdate != nil {
		t.Fatalf("unexpected status: %+v", payload)
	}
}
