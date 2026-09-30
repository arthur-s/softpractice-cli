package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/starterbundle"
)

// refactoredLessonServer serves a lesson version update v1 → v2 of
// pa-foundation-01 that replaces main.py with the returned bytes.
func refactoredLessonServer(t *testing.T, workspaceID string) (*httptest.Server, []byte) {
	t.Helper()
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
	return server, refactored
}

// Preparing an update lists what will change and changes nothing; applying
// it is refused once HEAD moved, because the learner confirmed that plan.
func TestPrepareUpdateChangesNothingAndApplyRefusesAMovedHead(t *testing.T) {
	workspaceID := uuid.NewString()
	server, refactored := refactoredLessonServer(t, workspaceID)
	defer server.Close()
	client := savedTestClient(t, server.URL)
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	if output, err := exec.Command("git", "-C", root, "update-index", "--assume-unchanged", ".softpractice/project.json").CombinedOutput(); err != nil {
		t.Fatalf("ignore local project link in test repository: %v: %s", err, output)
	}
	useCases := newLearnerUseCases(client, root)
	ctx := context.Background()

	plan, err := useCases.PrepareUpdate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != updateLessonVersion || plan.FromAssignmentVersion != 1 || plan.ToAssignmentVersion != 2 ||
		len(plan.Operations) != 1 || plan.Operations[0] != (updateOperation{Kind: "replace", Path: "main.py"}) {
		t.Fatalf("plan = %+v", plan)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "main.py")); string(content) == string(refactored) {
		t.Fatal("preparing the update replaced main.py")
	}
	if link, err := learnercli.LoadProjectLink(root); err != nil || link.AssignmentVersion != 1 {
		t.Fatalf("preparing the update moved the project link: %+v, %v", link, err)
	}

	commitLearnerWork(t, root, "solution.py", "# more work\n")
	var output bytes.Buffer
	if err := useCases.ApplyUpdate(ctx, plan, &output); err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("apply after HEAD moved = %v; want a refusal", err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "main.py")); string(content) == string(refactored) {
		t.Fatal("a refused update replaced main.py")
	}

	plan, err = useCases.PrepareUpdate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := useCases.ApplyUpdate(ctx, plan, &output); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "main.py")); err != nil || string(content) != string(refactored) {
		t.Fatalf("author file = %q, %v; want the refactored bytes", content, err)
	}
}
