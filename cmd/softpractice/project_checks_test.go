package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// CheckPublished runs the checks the server publishes for the lesson and
// refuses a configuration that differs from it, without running anything.
func TestCheckPublishedRunsOnlyThePublishedConfiguration(t *testing.T) {
	workspaceID := uuid.NewString()
	server := lessonBumpServer(t, workspaceID, http.NotFound)
	defer server.Close()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 2)
	useCases := newLearnerUseCases(savedTestClient(t, server.URL), root)
	ctx := context.Background()

	var output, errorOutput bytes.Buffer
	if err := useCases.CheckPublished(ctx, &output, &errorOutput); err != nil {
		t.Fatalf("published checks: %v\n%s%s", err, output.String(), errorOutput.String())
	}
	if !strings.Contains(output.String(), "git version") {
		t.Fatalf("check output = %q; want the command output", output.String())
	}

	marker := filepath.Join(root, "marker")
	edited := `{"schema_version":1,"checks":[{"name":"test check","executable":"git","args":["init","` +
		filepath.ToSlash(marker) + `"],"timeout_seconds":30}]}`
	if err := os.WriteFile(filepath.Join(root, ".softpractice", "checks.json"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err := useCases.CheckPublished(ctx, &output, &errorOutput)
	if !errors.Is(err, errChecksNotPublished) {
		t.Fatalf("edited checks = %v; want errChecksNotPublished", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("an edited check ran: %v", statErr)
	}
}
