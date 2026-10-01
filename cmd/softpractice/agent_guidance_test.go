package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/google/uuid"
)

func testAgentGuidance() *learnercli.AgentGuidance {
	files := []learnercli.AgentGuidanceFile{{Path: "AGENTS.md", Content: "Ask the learner for a bounded next step.\n"}, {Path: "CLAUDE.md", Content: "@AGENTS.md\n"}}
	data, _ := json.Marshal(files)
	digest := sha256.Sum256(data)
	return &learnercli.AgentGuidance{SchemaVersion: 1, AssignmentID: "pa-foundation-01", AssignmentVersion: 1, SHA256: hex.EncodeToString(digest[:]), Files: files}
}

func TestRestoredProjectGuidancePreservesLessonAndSolution(t *testing.T) {
	ctx := context.Background()
	root := createPinnedLinkedGitRepository(t, uuid.NewString(), "pa-foundation-01", 1)
	ignoreProjectLink(t, root)
	_, solutionHash := commitLessonSolution(t, root, "solution.py", "value = 1\n")
	guidance := testAgentGuidance()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/assignments/pa-foundation-01/agent-guidance" {
			writeTestJSON(w, guidance)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := savedTestClient(t, server.URL)
	if err := restoreAgentGuidance(ctx, client, root, "pa-foundation-01", 1); err != nil {
		t.Fatal(err)
	}
	link, err := learnercli.LoadProjectLink(root)
	if err != nil || link.AssignmentVersion != 1 {
		t.Fatal("guidance changed the lesson version")
	}
	repo, err := inspectGitRepository(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(repo.ArchivePath)
	if !repo.Clean || solutionCommitContentSHA256(ctx, root, repo.CommitSHA) != solutionHash {
		t.Fatal("guidance changed solution or left a dirty tree")
	}
	if ops, err := installMissingAgentGuidance(ctx, client, root, "pa-foundation-01", 1); err != nil || len(ops) != 0 {
		t.Fatalf("guidance reinstall not idempotent: %+v, %v", ops, err)
	}
}

func TestLegacyAcceptanceAllowsOnlyInstructionChanges(t *testing.T) {
	ctx := context.Background()
	root := createPinnedLinkedGitRepository(t, uuid.NewString(), "pa-foundation-01", 1)
	ignoreProjectLink(t, root)
	commitLessonSolution(t, root, "solution.py", "value = 1\n")
	acceptedSHA, _ := commitLessonSolution(t, root, "AGENTS.md", "legacy instructions\n")
	legacyHash := commitContentSHA256(ctx, root, acceptedSHA)
	if legacyHash == "" {
		t.Fatal("missing legacy hash")
	}
	commitLessonSolution(t, root, "AGENTS.md", "new independent instructions\n")
	commitLessonSolution(t, root, "CLAUDE.md", "@AGENTS.md\n")
	repo, err := inspectGitRepository(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	solutionHash := solutionCommitContentSHA256(ctx, root, repo.CommitSHA)
	if !matchesAcceptedSolution(ctx, repo, solutionHash, legacyHash) {
		t.Fatal("technical guidance broke legacy accepted base")
	}
	commitLessonSolution(t, root, "solution.py", "value = 2\n")
	repo, err = inspectGitRepository(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if matchesAcceptedSolution(ctx, repo, solutionCommitContentSHA256(ctx, root, repo.CommitSHA), legacyHash) {
		t.Fatal("solution change bypassed accepted base")
	}
}

func TestGuidanceRejectsUnsafeDestinationsAndTamperedContent(t *testing.T) {
	root := t.TempDir()
	guidance := testAgentGuidance()
	if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := installMissingAgentGuidance(context.Background(), nil, root, "pa-foundation-01", 1); err == nil {
		t.Fatal("instruction-shaped directory accepted")
	}
	guidance.Files[0].Content = "tampered"
	if err := guidance.Validate(guidance.AssignmentID, guidance.AssignmentVersion); err == nil {
		t.Fatal("tampered digest accepted")
	}
}

func TestStarterInstructionsNeedNoSeparateRequest(t *testing.T) {
	root := t.TempDir()
	for _, file := range testAgentGuidance().Files {
		if err := os.WriteFile(filepath.Join(root, file.Path), []byte(file.Content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("starter with instructions requested the guidance API")
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := installStarterGuidance(context.Background(), savedTestClient(t, server.URL), root, "pa-foundation-01", 1); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyGuidanceOnlyAddsMissingFiles(t *testing.T) {
	root := t.TempDir()
	const original = "Project-specific instructions\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeTestJSON(w, testAgentGuidance()) }))
	defer server.Close()
	installed, err := installMissingAgentGuidance(context.Background(), savedTestClient(t, server.URL), root, "pa-foundation-01", 1)
	if err != nil || len(installed) != 1 || installed[0] != "CLAUDE.md" {
		t.Fatalf("installed = %v, %v", installed, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil || string(body) != original {
		t.Fatalf("existing instructions replaced: %v", err)
	}
}
