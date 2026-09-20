package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/arthur-s/softpractice-cli/internal/submission"
)

func TestCourseUpdateMismatchNamesTheAcceptedCommitInThisRepository(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	ignoreProjectLink(t, root)
	acceptedSHA, acceptedContentSHA256 := commitLessonSolution(t, root, "solution.py", "def price():\n    return 1\n")
	headSHA, _ := commitLessonSolution(t, root, "scratch.py", "print('later work')\n")

	server := courseUpdateMismatchServer(t, workspaceID, acceptedContentSHA256)
	defer server.Close()
	client := savedTestClient(t, server.URL)
	t.Chdir(root)

	var output, errorOutput bytes.Buffer
	err := updateProject(context.Background(), client, nil, &output, &errorOutput)
	if err == nil {
		t.Fatal("update applied a course update onto a commit that is not the accepted base")
	}
	for _, expected := range []string{
		"Accepted solution: " + acceptedSHA,
		"Current HEAD:      " + headSHA,
		"git switch -c pa-foundation-02 " + acceptedSHA,
		"softpractice update",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("mismatch message %q does not contain %q", err.Error(), expected)
		}
	}
	if _, statError := os.Stat(filepath.Join(root, "lesson_two.py")); !os.IsNotExist(statError) {
		t.Fatalf("refused update still wrote lesson files: %v", statError)
	}
	link, loadError := learnercli.LoadProjectLink(root)
	if loadError != nil || link.AssignmentID != "pa-foundation-01" {
		t.Fatalf("refused update moved the project link: %+v, %v", link, loadError)
	}
}

func TestCourseUpdateMismatchOffersRestoreWhenTheAcceptedCommitIsAbsent(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	ignoreProjectLink(t, root)
	headSHA, _ := commitLessonSolution(t, root, "solution.py", "def price():\n    return 1\n")

	// A content identity no commit of this repository can produce: the learner
	// continued the lesson in a different clone.
	absent := strings.Repeat("a", 64)
	server := courseUpdateMismatchServer(t, workspaceID, absent)
	defer server.Close()
	client := savedTestClient(t, server.URL)
	t.Chdir(root)

	var output, errorOutput bytes.Buffer
	err := updateProject(context.Background(), client, nil, &output, &errorOutput)
	if err == nil {
		t.Fatal("update applied a course update without the accepted base")
	}
	for _, expected := range []string{
		"Current HEAD: " + headSHA,
		"was not found in this repository history (commits checked: 1)",
		"softpractice project restore",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("mismatch message %q does not contain %q", err.Error(), expected)
		}
	}
	if strings.Contains(err.Error(), "Accepted solution:") {
		t.Fatalf("mismatch message names an accepted commit it did not find: %q", err.Error())
	}
}

func TestStatusReportsAPendingTransitionAndScopesTheSubmissionLine(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/me":
			writeTestJSON(writer, map[string]any{
				"id": uuid.NewString(), "email": "ada@example.com",
				"display_name": "Ada", "email_verified": true,
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, currentAssignmentPayload(workspaceID))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)

	var output, errorOutput bytes.Buffer
	if err := status(context.Background(), client, root, nil, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Transition pending: pa-foundation-01 v1 → pa-foundation-02 v1",
		"softpractice update",
		"Latest pa-foundation-02 submission: none",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status output %q does not contain %q", output.String(), expected)
		}
	}

	var machineOutput bytes.Buffer
	if err := status(
		context.Background(), client, root, []string{"--format", "json"}, &machineOutput, &errorOutput,
	); err != nil {
		t.Fatal(err)
	}
	var payload machineStatus
	if err := json.Unmarshal(machineOutput.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Transition == nil {
		t.Fatalf("machine status omits the pending transition: %s", machineOutput.String())
	}
	if payload.Transition.FromAssignmentID != "pa-foundation-01" || payload.Transition.FromAssignmentVersion != 1 ||
		payload.Transition.ToAssignmentID != "pa-foundation-02" || payload.Transition.ToAssignmentVersion != 1 {
		t.Fatalf("machine status transition = %+v", *payload.Transition)
	}
}

func TestMachineStatusOmitsTransitionWhenTheProjectIsOnTheCurrentLesson(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-02", 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/me":
			writeTestJSON(writer, map[string]any{
				"id": uuid.NewString(), "email": "ada@example.com",
				"display_name": "Ada", "email_verified": true,
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, currentAssignmentPayload(workspaceID))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)

	var output, errorOutput bytes.Buffer
	if err := status(
		context.Background(), client, root, []string{"--format", "json"}, &output, &errorOutput,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "transition") {
		t.Fatalf("machine status reports a transition for a current project: %s", output.String())
	}
}

// A restore already materialises the accepted revision into a fresh folder, so
// its failure must not hand the learner the very command that just failed, nor
// name commits of a staging directory they never see.
func TestCourseUpdateMismatchInsideRestoreDoesNotSuggestAnotherRestore(t *testing.T) {
	workspaceID := uuid.NewString()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	ignoreProjectLink(t, root)
	acceptedSHA, _ := commitLessonSolution(t, root, "solution.py", "def price():\n    return 1\n")
	repository, err := inspectGitRepository(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	update := learnercli.CourseUpdate{
		FromAssignmentID: "pa-foundation-01", ToAssignmentID: "pa-foundation-02",
		BaseContentSHA256: strings.Repeat("a", 64),
	}

	message := courseUpdateMismatchError(context.Background(), repository, update, true).Error()

	for _, forbidden := range []string{"project restore", "git switch", acceptedSHA} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("restore mismatch message %q mentions %q", message, forbidden)
		}
	}
	if !strings.Contains(message, "run the command again") {
		t.Fatalf("restore mismatch message %q does not say how to recover", message)
	}
}

func currentAssignmentPayload(workspaceID string) map[string]any {
	return map[string]any{
		"workspace": map[string]any{
			"id": workspaceID, "project_id": "equipment-rental-python", "state": "active",
			"base_revision_id": nil, "support_mode": nil,
			"created_at": time.Now(), "updated_at": time.Now(),
		},
		"assignment": map[string]any{
			"id": "pa-foundation-02", "title": "Rental period", "version": 1, "state": "available",
			"local_checks": testLocalChecks(),
		},
	}
}

// courseUpdateMismatchServer publishes a transition whose accepted base is
// acceptedContentSHA256. The archive route fails the test: a mismatched local
// project must be refused before anything is downloaded.
func courseUpdateMismatchServer(t *testing.T, workspaceID, acceptedContentSHA256 string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/workspaces/" + workspaceID + "/current-assignment":
			writeTestJSON(writer, currentAssignmentPayload(workspaceID))
		case "/v1/workspaces/" + workspaceID + "/current-assignment/course-update":
			writeTestJSON(writer, map[string]any{
				"ref":                "pa-foundation-01-to-pa-foundation-02@1.0.0",
				"from_assignment_id": "pa-foundation-01", "from_assignment_version": 1,
				"to_assignment_id": "pa-foundation-02", "to_assignment_version": 1,
				"base_revision_id": uuid.NewString(), "base_content_sha256": acceptedContentSHA256,
				"archive_sha256": strings.Repeat("b", 64), "archive_size": 1,
				"files":      []any{map[string]any{"path": "lesson_two.py", "sha256": strings.Repeat("c", 64)}},
				"operations": []any{map[string]any{"kind": "add", "path": "lesson_two.py"}},
			})
		case "/v1/workspaces/" + workspaceID + "/current-assignment/course-update/archive":
			t.Error("course update archive was downloaded for a mismatched project")
			http.NotFound(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
}

// commitLessonSolution commits one file and returns the new HEAD together with
// the server-normalized content identity of that commit, which is exactly what
// the server stores for a submitted revision.
func commitLessonSolution(t *testing.T, root, path, contents string) (string, string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", path}, {"commit", "--quiet", "-m", "add " + path}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
		}
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
	return repository.CommitSHA, normalized.ContentSHA256
}

func ignoreProjectLink(t *testing.T, root string) {
	t.Helper()
	output, err := exec.Command(
		"git", "-C", root, "update-index", "--assume-unchanged", ".softpractice/project.json",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("ignore local project link in test repository: %v: %s", err, output)
	}
}

// The version is what a bug report and a refused submission both ask for, so
// it answers through a flag, through a subcommand, and it is listed in help.
func TestVersionAnswersThroughFlagAndSubcommand(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output, errorOutput bytes.Buffer
			if err := run(context.Background(), args, nil, &output, &errorOutput); err != nil {
				t.Fatalf("run(%v) = %v", args, err)
			}
			if !strings.HasPrefix(output.String(), "softpractice ") {
				t.Fatalf("version output = %q", output.String())
			}
		})
	}
	var help, helpErrors bytes.Buffer
	if err := run(context.Background(), []string{"help"}, nil, &help, &helpErrors); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "version") {
		t.Fatalf("help does not list version: %q", help.String())
	}
	var commandHelp, commandHelpErrors bytes.Buffer
	if err := run(context.Background(), []string{"help", "version"}, nil, &commandHelp, &commandHelpErrors); err != nil {
		t.Fatalf("help version = %v", err)
	}
	if !strings.Contains(commandHelp.String(), "--version") {
		t.Fatalf("version help = %q", commandHelp.String())
	}
}

// A source build carries a placeholder the server refuses, so the version says
// so instead of letting the learner meet it as a rejected submit.
func TestVersionWarnsThatASourceBuildCannotSubmit(t *testing.T) {
	var output, errorOutput bytes.Buffer
	if err := run(context.Background(), []string{"version"}, nil, &output, &errorOutput); err != nil {
		t.Fatal(err)
	}
	if cliVersion != developmentVersion {
		t.Skipf("binary was stamped with %q", cliVersion)
	}
	if !strings.Contains(output.String(), "refuse its submissions") ||
		!strings.Contains(output.String(), "main.cliVersion=") {
		t.Fatalf("placeholder version does not explain itself: %q", output.String())
	}
}
