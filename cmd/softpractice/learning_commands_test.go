package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
					"counterexample": map[string]any{
						"title": "COUNTEREXAMPLE-TITLE", "scenario": "SCENARIO-TEXT", "input": "INPUT-TEXT",
						"expected": "EXPECTED-TEXT", "next_step": "NEXT-STEP-TEXT",
					},
				},
			}},
			"review": map[string]any{
				"projection_version": 1, "status": "completed", "mode": "llm",
				"authoritative": true, "verdict": "revise", "summary": "REVIEW-SUMMARY-TEXT",
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
	err := resultCommand(ctx, useCases, args, &output, &errorOutput)
	return output.String(), errorOutput.String(), err
}

func TestEvaluationWaitHonoursRetryAfterAndHidesDirections(t *testing.T) {
	server, calls := evaluationServer(t, currentSubmissionID,
		respondPending("7"), respondPending("5"), respondRevise)
	clock := &fakeClock{now: time.Unix(0, 0)}
	useCases := clock.useCases(savedTestClient(t, server.URL), "")

	output, _, err := runEvaluation(t, useCases, "--id", currentSubmissionID, "--wait", "--timeout", "1m", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Fatalf("calls = %d, want 3", *calls)
	}
	if want := []time.Duration{7 * time.Second, 5 * time.Second}; !reflect.DeepEqual(clock.sleeps, want) {
		t.Fatalf("sleeps = %v, want %v", clock.sleeps, want)
	}
	assertEvaluationOutput(t, output, false)
	var payload machineEvaluationResult
	if err := decodeTestJSON([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	result := payload.Result
	if payload.Outcome != evaluationReady || result.Status != "revise" ||
		result.Detail != feedbackWithoutDirections || !result.DirectionsHidden ||
		len(result.Review.Findings) != 2 || result.Review.Findings[0].Observation != "OBSERVATION-TEXT" ||
		result.Review.QuestionsCount != 1 || *result.Review.Verdict != "revise" ||
		len(result.Review.Rubric) != 2 || result.Review.Rubric[0].Rationale != "RUBRIC-RATIONALE-TEXT" ||
		result.Deterministic.Checks[1].Counterexample.Expected != "EXPECTED-TEXT" ||
		payload.ResultURL != "https://softpractice.example/submissions/"+currentSubmissionID+"/result" {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	text, _, err := runEvaluation(t, useCases, "--id", currentSubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	assertEvaluationOutput(t, text, false)
	for _, want := range []string{
		"# Revision needed", "## Automated checks: passed", "## Review", "REVIEW-SUMMARY-TEXT",
		"- **responsibilities**: needs work. RUBRIC-RATIONALE-TEXT Where: app.py",
		"1. **FINDING-TITLE-TEXT** (high priority)", "   - Risk: RISK-TEXT",
		"The reviewer's self-check questions (1) are on the result page.", "softpractice result --directions",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output lacks %q:\n%s", want, text)
		}
	}
}

// assertEvaluationOutput checks what the evaluation output shows at each
// detail: always what was found and why, the directions and next steps only
// on request, and the reviewer's questions never.
func assertEvaluationOutput(t *testing.T, output string, withDirections bool) {
	t.Helper()
	for _, shown := range []string{
		"OBSERVATION-TEXT", "RISK-TEXT", "FINDING-TITLE-TEXT", "RUBRIC-RATIONALE-TEXT",
		"REVIEW-SUMMARY-TEXT", "SCENARIO-TEXT", "EXPECTED-TEXT",
	} {
		if !strings.Contains(output, shown) {
			t.Fatalf("output lacks %q:\n%s", shown, output)
		}
	}
	if strings.Contains(output, "DEFENSE-QUESTION-TEXT") {
		t.Fatalf("output contains a reviewer question:\n%s", output)
	}
	for _, direction := range []string{reviewerDirectionMarker, "NEXT-STEP-TEXT"} {
		if strings.Contains(output, direction) != withDirections {
			t.Fatalf("output contains %q = %t, want %t:\n%s", direction, !withDirections, withDirections, output)
		}
	}
}

func TestEvaluationDirectionsOnRequest(t *testing.T) {
	if defaultReviewFeedback != feedbackWithoutDirections {
		t.Fatalf("defaultReviewFeedback = %q; reviewer directions must stay out of output unless requested", defaultReviewFeedback)
	}
	server, _ := evaluationServer(t, currentSubmissionID, respondRevise)
	useCases := newLearnerUseCases(savedTestClient(t, server.URL), "")
	for _, format := range []string{"json", "text"} {
		args := []string{"--id", currentSubmissionID, "--directions"}
		if format == "json" {
			args = append(args, "--json")
		}
		output, _, err := runEvaluation(t, useCases, args...)
		if err != nil {
			t.Fatal(err)
		}
		assertEvaluationOutput(t, output, true)
		if format == "text" && (!strings.Contains(output, "   - Direction: "+reviewerDirectionMarker) ||
			strings.Contains(output, "are not shown")) {
			t.Fatalf("unexpected text output:\n%s", output)
		}
	}
}

func TestEvaluationTimeoutReportsNotReady(t *testing.T) {
	server, calls := evaluationServer(t, currentSubmissionID, respondPending("30"))
	clock := &fakeClock{now: time.Unix(0, 0)}
	useCases := clock.useCases(savedTestClient(t, server.URL), "")

	output, _, err := runEvaluation(t, useCases, "--id", currentSubmissionID, "--wait", "--timeout", "45s", "--json")
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
	if payload.Outcome != evaluationPending || payload.JobState != "leased" || payload.Result != nil {
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

func TestEvaluationRateLimitWithoutAnAnswer(t *testing.T) {
	rateLimited := func(retryAfter string) func(http.ResponseWriter) {
		return func(writer http.ResponseWriter) {
			if retryAfter != "" {
				writer.Header().Set("Retry-After", retryAfter)
			}
			writeTestAPIError(writer, http.StatusTooManyRequests, "rate_limited")
		}
	}
	for name, tc := range map[string]struct {
		retryAfter string
		timeout    string
		calls      int
	}{
		// Without Retry-After there is no interval to honour, so it is not
		// retried at all.
		"no Retry-After": {retryAfter: "", timeout: "1m", calls: 1},
		// A rate limit before the first answer leaves no pending state to
		// report when the next allowed poll falls after the timeout.
		"past the timeout": {retryAfter: "90", timeout: "1m", calls: 1},
	} {
		server, calls := evaluationServer(t, currentSubmissionID, rateLimited(tc.retryAfter))
		clock := &fakeClock{now: time.Unix(0, 0)}
		output, _, err := runEvaluation(t, clock.useCases(savedTestClient(t, server.URL), ""),
			"--id", currentSubmissionID, "--wait", "--timeout", tc.timeout, "--json")
		var statusError *learnercli.HTTPError
		if !errors.As(err, &statusError) || statusError.Status != http.StatusTooManyRequests ||
			*calls != tc.calls || len(clock.sleeps) != 0 || !strings.Contains(output, `"code": "api_error"`) {
			t.Fatalf("%s: err = %v, calls = %d, sleeps = %v, output:\n%s", name, err, *calls, clock.sleeps, output)
		}
	}
}

func TestEvaluationWaitStopsWhenCancelled(t *testing.T) {
	server, calls := evaluationServer(t, currentSubmissionID, respondPending("5"))
	useCases := newLearnerUseCases(savedTestClient(t, server.URL), "")
	ctx, cancel := context.WithCancel(context.Background())
	useCases.sleep = func(sleepCtx context.Context, delay time.Duration) error {
		cancel()
		return sleepContext(sleepCtx, delay)
	}
	_, err := useCases.Evaluation(ctx, currentSubmissionID, evaluationWait{Wait: true, Timeout: time.Hour})
	if !errors.Is(err, context.Canceled) || *calls != 1 || errorCode(err) != "cancelled" {
		t.Fatalf("err = %v, calls = %d", err, *calls)
	}
}

func TestEvaluationPollRefreshesAnExpiredAccessToken(t *testing.T) {
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v1/auth/tokens/refresh":
			refreshes++
			writeTestJSON(writer, map[string]any{
				"token_type": "Bearer", "access_token": "new-access-token", "expires_in_seconds": 900,
				"refresh_token": "new-refresh-token", "refresh_idle_expires_at": time.Now().Add(time.Hour),
				"refresh_absolute_expires_at": time.Now().Add(2 * time.Hour), "scopes": []string{"workspace:read"},
			})
		case request.Header.Get("Authorization") != "Bearer new-access-token":
			writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
		default:
			respondPending("3")(writer)
		}
	}))
	defer server.Close()
	clock := &fakeClock{now: time.Unix(0, 0)}
	output, _, err := runEvaluation(t, clock.useCases(savedTestClient(t, server.URL), ""),
		"--id", currentSubmissionID, "--json")
	var payload machineEvaluationResult
	if !errors.Is(err, exitNotReady) || refreshes != 1 || decodeTestJSON([]byte(output), &payload) != nil ||
		payload.Outcome != evaluationPending {
		t.Fatalf("err = %v, refreshes = %d, output:\n%s", err, refreshes, output)
	}
}

func TestEvaluationSupersededIsASeparateOutcome(t *testing.T) {
	server, _ := evaluationServer(t, currentSubmissionID, func(writer http.ResponseWriter) {
		writeTestAPIError(writer, http.StatusConflict, "evaluation_superseded")
	})
	clock := &fakeClock{now: time.Unix(0, 0)}
	output, _, err := runEvaluation(t, clock.useCases(savedTestClient(t, server.URL), ""),
		"--id", currentSubmissionID, "--wait", "--json")
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
		"--id", currentSubmissionID, "--json")
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
	output, _, err = runEvaluation(t, newLearnerUseCases(client, ""), "--id", currentSubmissionID, "--json")
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
		output, _, err := runEvaluation(t, newLearnerUseCases(client, tc.directory), "--json")
		if err == nil || !strings.Contains(output, `"code": "`+tc.code+`"`) {
			t.Fatalf("%s: err = %v, output:\n%s", tc.code, err, output)
		}
		for name, command := range map[string]func(context.Context, learnerUseCases, []string, io.Writer, io.Writer) error{
			"task": taskCommand, "material": materialCommand, "hint": hintCommand, "submissions": submissionsCommand,
		} {
			var commandOutput, errorOutput bytes.Buffer
			err = command(context.Background(), newLearnerUseCases(client, tc.directory),
				[]string{"--json"}, &commandOutput, &errorOutput)
			if err == nil || !strings.Contains(commandOutput.String(), `"code": "`+tc.code+`"`) {
				t.Fatalf("%s %s: err = %v, output:\n%s", name, tc.code, err, commandOutput.String())
			}
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
		case request.URL.Path == "/v1/assignments/pa-foundation-01/prepared-hints" && request.URL.Query().Get("version") == "1":
			writeTestJSON(writer, map[string]any{
				"status":   "available",
				"revealed": []any{map[string]any{"id": "rule-first", "order": 1, "markdown": "HINT-ONE-TEXT"}},
			})
		case request.URL.Path == "/v1/assignments/pa-foundation-00/material" && request.URL.RawQuery == "":
			writeTestJSON(writer, map[string]any{
				"id": "pa-theory-intro", "title": "Intro", "estimated_minutes": 5,
				"markdown": "EARLIER-MATERIAL-TEXT",
				"diagram": map[string]any{"title": "Map", "text_alternative": "X then Y.", "nodes": []any{
					map[string]any{"title": "X", "text": "x"}, map[string]any{"title": "Y", "text": "y"},
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

func runLessonCommand(
	t *testing.T,
	command func(context.Context, learnerUseCases, []string, io.Writer, io.Writer) error,
	useCases learnerUseCases,
	args ...string,
) string {
	t.Helper()
	var output, errorOutput bytes.Buffer
	ctx := withSettings(context.Background(), runtimeSettings{
		Language: languageEnglish, WebURL: "https://softpractice.example",
	})
	if err := command(ctx, useCases, args, &output, &errorOutput); err != nil {
		t.Fatalf("%v\n%s", err, errorOutput.String())
	}
	return output.String()
}

func TestTaskAndMaterialReadTheFolderLessonVersion(t *testing.T) {
	workspaceID := uuid.NewString()
	// The folder is pinned to v1 while the server has published v2: the
	// requirement the tree is being written for is v1.
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	for _, withMaterial := range []bool{true, false} {
		useCases := newLearnerUseCases(savedTestClient(t, assignmentServer(t, workspaceID, withMaterial).URL), root)
		var task machineTask
		if err := decodeTestJSON([]byte(runLessonCommand(t, taskCommand, useCases, "--json")), &task); err != nil {
			t.Fatal(err)
		}
		if task.Kind != "softpractice.task" || task.Assignment.Version != 1 ||
			task.Assignment.InstructionsMarkdown != "Extract the price rule." ||
			task.URL != "https://softpractice.example/assignments/pa-foundation-01?version=1" ||
			task.LessonVersionUpdate == nil || task.LessonVersionUpdate.ToVersion != 2 {
			t.Fatalf("unexpected task: %+v", task)
		}
		output := runLessonCommand(t, materialCommand, useCases, "--json")
		var material machineMaterial
		if err := decodeTestJSON([]byte(output), &material); err != nil {
			t.Fatal(err)
		}
		if withMaterial != (material.Material != nil) || material.Version != 1 ||
			material.URL != "https://softpractice.example/assignments/pa-foundation-01/material?version=1" {
			t.Fatalf("material = %+v, want present=%t", material, withMaterial)
		}
		if !withMaterial && !strings.Contains(output, `"material": null`) {
			t.Fatalf("absent material must be null:\n%s", output)
		}
		text := runLessonCommand(t, materialCommand, useCases)
		if withMaterial && (!strings.Contains(text, "# Rules") || !strings.Contains(text, "- **A**: a")) {
			t.Fatalf("unexpected material text:\n%s", text)
		}
		if !withMaterial && !strings.Contains(text, "has no theory material") {
			t.Fatalf("unexpected material text:\n%s", text)
		}
	}
}

func TestMaterialOfAnEarlierLesson(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	useCases := newLearnerUseCases(savedTestClient(t, assignmentServer(t, workspaceID, true).URL), root)
	output := runLessonCommand(t, materialCommand, useCases, "--lesson", "pa-foundation-00")
	if !strings.Contains(output, "EARLIER-MATERIAL-TEXT") ||
		!strings.Contains(output, "https://softpractice.example/assignments/pa-foundation-00/material\n") {
		t.Fatalf("unexpected output:\n%s", output)
	}
}

func TestHintListsOpenedHintsAndLinksToTheSite(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	useCases := newLearnerUseCases(savedTestClient(t, assignmentServer(t, workspaceID, true).URL), root)
	output := runLessonCommand(t, hintCommand, useCases)
	if !strings.Contains(output, "## Hint 1\n\nHINT-ONE-TEXT") ||
		!strings.Contains(output, "open the next hint on the assignment page: https://softpractice.example/assignments/pa-foundation-01?version=1") {
		t.Fatalf("unexpected output:\n%s", output)
	}
	var hints machineHints
	if err := decodeTestJSON([]byte(runLessonCommand(t, hintCommand, useCases, "--json")), &hints); err != nil {
		t.Fatal(err)
	}
	if hints.Status != "available" || len(hints.Revealed) != 1 || hints.Version != 1 {
		t.Fatalf("unexpected hints: %+v", hints)
	}
}

func TestInvalidArgumentsAreUsageErrors(t *testing.T) {
	useCases := newLearnerUseCases(savedTestClient(t, "http://127.0.0.1:1"), t.TempDir())
	for name, args := range map[string][]string{
		"unknown flag": {"--no-such-flag"}, "extra argument": {"extra"}, "negative timeout": {"--timeout", "-1s"},
	} {
		_, _, err := runEvaluation(t, useCases, args...)
		var usageErr usageError
		if !errors.As(err, &usageErr) {
			t.Fatalf("%s: err = %v, want a usage error", name, err)
		}
	}
	for _, args := range [][]string{{"--id", "not-a-uuid"}, {"--id", strings.ToUpper(currentSubmissionID)}} {
		if _, _, err := runEvaluation(t, useCases, args...); !errors.As(err, new(usageError)) {
			t.Fatalf("%v: err = %v, want a usage error", args, err)
		}
	}

	// With --json even a command line that does not parse is reported as
	// one softpractice.error document on stdout.
	commands := map[string]func(context.Context, learnerUseCases, []string, io.Writer, io.Writer) error{
		"result": resultCommand, "task": taskCommand, "material": materialCommand,
		"hint": hintCommand, "submissions": submissionsCommand,
	}
	for name, command := range commands {
		for _, args := range [][]string{{"--json", "--no-such-flag"}, {"--json", "extra"}, {"--no-such-flag", "--json=true"}} {
			var output bytes.Buffer
			err := command(context.Background(), useCases, args, &output, io.Discard)
			var payload machineError
			if decodeErr := decodeTestJSON(output.Bytes(), &payload); decodeErr != nil || !errors.As(err, new(usageError)) ||
				payload.Kind != "softpractice.error" || payload.Error.Code != "usage" {
				t.Fatalf("%s %v: err = %v, output:\n%s", name, args, err, output.String())
			}
		}
	}
	client := savedTestClient(t, "http://127.0.0.1:1")
	for name, command := range map[string]func(io.Writer) error{
		"status": func(output io.Writer) error {
			return status(context.Background(), client, t.TempDir(), []string{"--json", "extra"}, output, io.Discard)
		},
		"submit": func(output io.Writer) error {
			return submit(context.Background(), client, t.TempDir(), []string{"--json", "--timeout", "-1s"},
				strings.NewReader(""), output, io.Discard)
		},
	} {
		var output bytes.Buffer
		err := command(&output)
		if !errors.As(err, new(usageError)) || !strings.Contains(output.String(), `"code": "usage"`) {
			t.Fatalf("%s: err = %v, output:\n%s", name, err, output.String())
		}
	}

	// Without --json, or with --json=false, stdout stays empty.
	var output bytes.Buffer
	if err := taskCommand(context.Background(), useCases, []string{"--json=false", "extra"}, &output, io.Discard); err == nil ||
		output.Len() != 0 {
		t.Fatalf("err = %v, output:\n%s", err, output.String())
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
// documents, so that a change to what an agent reads is a deliberate one.
func TestAgentJSONContractsAreStable(t *testing.T) {
	server, _ := evaluationServer(t, currentSubmissionID, respondRevise)
	clock := &fakeClock{now: time.Unix(0, 0)}
	output, _, err := runEvaluation(t, clock.useCases(savedTestClient(t, server.URL), ""),
		"--id", currentSubmissionID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"attempt", "evaluation_job_id", "job_state", "kind", "max_attempts", "outcome", "result",
		"result.detail", "result.deterministic", "result.deterministic.checks",
		"result.deterministic.checks[].counterexample",
		"result.deterministic.checks[].counterexample.expected",
		"result.deterministic.checks[].counterexample.input",
		"result.deterministic.checks[].counterexample.scenario",
		"result.deterministic.checks[].counterexample.title", "result.deterministic.checks[].id",
		"result.deterministic.checks[].kind", "result.deterministic.checks[].status",
		"result.deterministic.checks[].summary", "result.deterministic.status",
		"result.directions_hidden", "result.exercise_id", "result.review", "result.review.authoritative",
		"result.review.findings", "result.review.findings[].evidence_refs",
		"result.review.findings[].observation", "result.review.findings[].priority",
		"result.review.findings[].risk", "result.review.findings[].title", "result.review.mode",
		"result.review.questions_answerable", "result.review.questions_count", "result.review.rubric", "result.review.rubric[].criterion",
		"result.review.rubric[].evidence_refs", "result.review.rubric[].level",
		"result.review.rubric[].rationale", "result.review.status", "result.review.summary",
		"result.review.uncertainty", "result.review.uncertainty.blocking",
		"result.review.uncertainty.reason", "result.review.verdict", "result.status", "result_url",
		"submission_id", "updated_at",
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
		"error", "error.api_code", "error.code", "error.http_status",
		"error.message", "error.support_id", "kind",
	}
	if got := jsonKeyPaths(t, errorOutput.Bytes()); !reflect.DeepEqual(got, wantError) {
		t.Fatalf("softpractice.error keys changed:\ngot  %q\nwant %q", got, wantError)
	}

	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	useCases := newLearnerUseCases(savedTestClient(t, assignmentServer(t, workspaceID, true).URL), root)
	for _, tc := range []struct {
		kind    string
		command func(context.Context, learnerUseCases, []string, io.Writer, io.Writer) error
		want    []string
	}{
		{kind: "softpractice.task", command: taskCommand, want: []string{
			"assignment", "assignment.content_sha256", "assignment.estimated_minutes",
			"assignment.exercise_id", "assignment.id", "assignment.instructions_markdown",
			"assignment.kind", "assignment.project_setup", "assignment.title", "assignment.version",
			"kind", "lesson_version_update", "lesson_version_update.assignment_id",
			"lesson_version_update.from_version", "lesson_version_update.to_version",
			"project_id", "url", "workspace_id",
		}},
		{kind: "softpractice.material", command: materialCommand, want: []string{
			"assignment_id", "kind", "material",
			"material.diagram", "material.diagram.nodes", "material.diagram.nodes[].text",
			"material.diagram.nodes[].title", "material.diagram.text_alternative", "material.diagram.title",
			"material.estimated_minutes", "material.id", "material.markdown", "material.title",
			"url", "version",
		}},
		{kind: "softpractice.hints", command: hintCommand, want: []string{
			"assignment_id", "kind", "revealed", "revealed[].id", "revealed[].markdown", "revealed[].order",
			"status", "url", "version",
		}},
	} {
		if got := jsonKeyPaths(t, []byte(runLessonCommand(t, tc.command, useCases, "--json"))); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s keys changed:\ngot  %q\nwant %q", tc.kind, got, tc.want)
		}
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
		[]string{"--json"}, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	var payload machineStatus
	if err := decodeTestJSON(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Assignment.Title != "Module boundaries" || payload.LatestSubmission == nil ||
		payload.LatestSubmission.EvaluationStatus != "revise" || payload.Transition != nil ||
		len(payload.NextActions) != 1 || payload.NextActions[0].Command != "softpractice result" ||
		payload.LessonVersionUpdate != nil {
		t.Fatalf("unexpected status: %+v", payload)
	}
}

func TestNextActions(t *testing.T) {
	ctx := withSettings(context.Background(), runtimeSettings{WebURL: "https://softpractice.example"})
	submission := func(jobState string) *struct {
		ID          string    `json:"id"`
		RevisionID  string    `json:"revision_id"`
		JobState    string    `json:"job_state"`
		SubmittedAt time.Time `json:"submitted_at"`
	} {
		return &struct {
			ID          string    `json:"id"`
			RevisionID  string    `json:"revision_id"`
			JobState    string    `json:"job_state"`
			SubmittedAt time.Time `json:"submitted_at"`
		}{ID: currentSubmissionID, JobState: jobState}
	}
	snapshot := func(pinned string, pinnedVersion int, clean bool, head string) statusSnapshot {
		var result statusSnapshot
		result.Link = learnercli.ProjectLink{SchemaVersion: 2, ProjectID: "pa-foundation", AssignmentID: pinned, AssignmentVersion: pinnedVersion}
		result.Workspace.Workspace.State = "active"
		result.Workspace.Assignment.ID = "pa-foundation-02"
		result.Workspace.Assignment.Version = 2
		result.Repository = gitRepository{Clean: clean, CommitSHA: head}
		return result
	}
	codes := func(actions []nextAction) string {
		parts := make([]string, 0, len(actions))
		for _, action := range actions {
			parts = append(parts, action.Code)
		}
		return strings.Join(parts, ",")
	}

	transition := snapshot("pa-foundation-01", 1, true, "abc")
	fresh := snapshot("pa-foundation-02", 2, true, "abc")
	dirty := snapshot("pa-foundation-02", 2, false, "abc")
	queued := snapshot("pa-foundation-02", 2, true, "abc")
	queued.Workspace.LatestSubmission = submission("leased")
	questions := snapshot("pa-foundation-02", 2, true, "abc")
	questions.Workspace.LatestSubmission = submission("completed")
	questions.Latest = &latestResult{Status: "uncertain", QuestionsAnswerable: true, CommitSHA: "abc"}
	reviewed := snapshot("pa-foundation-02", 2, true, "abc")
	reviewed.Workspace.LatestSubmission = submission("completed")
	reviewed.Latest = &latestResult{Status: "revise", CommitSHA: "abc"}
	reworked := reviewed
	reworked.Repository.CommitSHA = "def"
	olderVersion := snapshot("pa-foundation-02", 1, true, "abc")
	dirtyTransition := snapshot("pa-foundation-01", 1, false, "abc")
	dirtyReviewed := reviewed
	dirtyReviewed.Repository.Clean = false
	unreadable := snapshot("pa-foundation-02", 2, true, "abc")
	unreadable.Workspace.LatestSubmission = submission("completed")
	superseded := snapshot("pa-foundation-02", 2, true, "abc")
	superseded.Workspace.LatestSubmission = submission("superseded")
	dirtySuperseded := superseded
	dirtySuperseded.Repository.Clean = false
	technical := snapshot("pa-foundation-02", 2, true, "abc")
	technical.Workspace.LatestSubmission = submission("failed")
	technical.Latest = &latestResult{Status: "technical_failure", CommitSHA: "abc"}
	technicalReworked := technical
	technicalReworked.Repository.CommitSHA = "def"
	completed := reviewed
	completed.Workspace.Workspace.State = "completed"
	completed.Workspace.Assignment.State = "accepted"
	completed.Latest = &latestResult{Status: "accepted", CommitSHA: "abc"}
	completedReworked := completed
	completedReworked.Repository = gitRepository{Clean: false, CommitSHA: "def"}
	archived := fresh
	archived.Workspace.Workspace.State = "archived"

	for name, tc := range map[string]struct {
		snapshot statusSnapshot
		want     string
	}{
		"transition":                {transition, "apply_update"},
		"no submission":             {fresh, "submit"},
		"dirty tree":                {dirty, "commit_changes"},
		"evaluating":                {queued, "wait_result"},
		"questions":                 {questions, "answer_questions,read_result"},
		"reviewed":                  {reviewed, "read_result"},
		"new commit":                {reworked, "submit"},
		"older version":             {olderVersion, "submit,update_lesson_version"},
		"dirty transition":          {dirtyTransition, "stash_changes,apply_update"},
		"dirty after review":        {dirtyReviewed, "commit_changes"},
		"result unreadable":         {unreadable, "read_result"},
		"superseded":                {superseded, "resubmit"},
		"superseded, dirty":         {dirtySuperseded, "commit_changes"},
		"technical failure":         {technical, "retry_evaluation,read_result"},
		"technical failure, rework": {technicalReworked, "submit"},
		"practicum completed":       {completed, "practicum_completed"},
		"completed, rework":         {completedReworked, "practicum_completed"},
		"archived":                  {archived, "workspace_inactive"},
	} {
		actions := nextActions(ctx, tc.snapshot)
		if got := codes(actions); got != tc.want {
			t.Fatalf("%s: next actions = %s, want %s", name, got, tc.want)
		}
		if (name == "questions" || name == "technical failure") &&
			actions[0].URL != "https://softpractice.example/submissions/"+currentSubmissionID+"/result" {
			t.Fatalf("%s: URL = %q", name, actions[0].URL)
		}
		if name == "practicum completed" && actions[0].URL != "https://softpractice.example/practicums/"+
			tc.snapshot.Link.ProjectID+"/completion" {
			t.Fatalf("completion URL = %q", actions[0].URL)
		}
		for _, action := range actions {
			if nextActionText(ctx, action) == action.Code {
				t.Fatalf("%s: action %s has no text", name, action.Code)
			}
		}
	}
}

func TestResultAsksToAnswerQuestionsOnTheSite(t *testing.T) {
	server, _ := evaluationServer(t, currentSubmissionID, func(writer http.ResponseWriter) {
		body := reviseEvaluationBody(currentSubmissionID)
		evaluation := body["evaluation"].(map[string]any)
		evaluation["status"] = "uncertain"
		review := evaluation["review"].(map[string]any)
		review["verdict"] = "uncertain"
		review["uncertainty"] = map[string]any{"blocking": true, "reason": "UNCERTAINTY-REASON"}
		writeTestJSON(writer, body)
	})
	output, _, err := runEvaluation(t, newLearnerUseCases(savedTestClient(t, server.URL), ""), "--id", currentSubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	want := "Answer them on the result page: https://softpractice.example/submissions/" + currentSubmissionID + "/result"
	if !strings.Contains(output, want) || !strings.Contains(output, "UNCERTAINTY-REASON") ||
		strings.Contains(output, "DEFENSE-QUESTION-TEXT") {
		t.Fatalf("unexpected output:\n%s", output)
	}
}
