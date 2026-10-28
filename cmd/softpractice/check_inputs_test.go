package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arthur-s/softpractice-cli/internal/localchecks"
	"github.com/arthur-s/softpractice-cli/internal/submission"
	"github.com/google/uuid"
)

func TestHistoricalInputChild(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--historical-child" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	mode := os.Args[index+1]
	root := os.Getenv("SOFTPRACTICE_CHECK_INPUTS_ROOT")
	fmt.Println("INPUT_ROOT=" + root)
	path := filepath.Join(root, "lesson-06", "sql", "ingest.sql")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "SELECT 6" {
		t.Fatalf("historical SQL missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lesson-06", "tools", "__init__.py")); !os.IsNotExist(err) {
		t.Fatal("historical Python projected")
	}
	if mode == "snapshot" {
		if _, err := os.Stat(".git"); !os.IsNotExist(err) {
			t.Fatal("snapshot has Git")
		}
	}
	if mode == "cancel" {
		time.Sleep(20 * time.Second)
		t.Fatal("check was not cancelled")
	}
	if mode == "mutate" {
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func historicalZIP(t *testing.T) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range []struct{ name, data string }{{"sql/ingest.sql", "SELECT 6"}, {"loading.json", "{}"}, {"sql/migrations/001.sql", "SELECT 1"}, {"tools/__init__.py", "raise RuntimeError('must not execute')"}, {"AGENTS.md", "legacy hash bytes"}} {
		f, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(f, entry.data)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.zip")
	os.WriteFile(path, buffer.Bytes(), 0600)
	materialized, err := submission.MaterializeZIP(path, "", submission.DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(materialized.Path)
	return buffer.Bytes(), materialized.ContentSHA256
}

func TestHistoricalInputsHTTPAndSnapshot(t *testing.T) {
	for _, mode := range []string{"working", "published", "snapshot", "mutate", "stale", "bad-hash", "wrong-base", "truncated", "tamper", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			workspaceID, base, revision := uuid.NewString(), uuid.NewString(), uuid.NewString()
			root := createPinnedLinkedGitRepository(t, workspaceID, "lesson-08", 1)
			config := localchecks.Config{SchemaVersion: 2, PriorRevisions: []string{"lesson-06"}, Checks: []localchecks.Check{{Name: "history", Executable: os.Args[0], Args: []string{"-test.run=^TestHistoricalInputChild$", "--", "--historical-child", mode}, TimeoutSeconds: 10}}}
			body, err := localchecks.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".softpractice", "checks.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "checks"}} {
				if _, err := gitOutput(context.Background(), root, args...); err != nil {
					t.Fatal(err)
				}
			}
			data, digest := historicalZIP(t)
			statusCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer access-token" {
					t.Error("missing authorization")
				}
				if r.URL.Path == "/v1/workspaces/"+workspaceID+"/current-assignment" {
					statusCalls++
					currentBase := base
					if mode == "stale" && statusCalls > 1 {
						currentBase = uuid.NewString()
					}
					writeTestJSON(w, map[string]any{"workspace": map[string]any{"id": workspaceID, "project_id": "equipment-rental-python", "state": "active", "base_revision_id": currentBase}, "assignment": map[string]any{"id": "lesson-08", "version": 1, "local_checks": json.RawMessage(body)}})
					return
				}
				if r.URL.Path != "/v1/workspaces/"+workspaceID+"/current-assignment/check-inputs/lesson-06/archive" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("base_revision_id") != base || r.URL.Query().Get("assignment_id") != "lesson-08" {
					t.Error("missing freshness conditions")
				}
				w.Header().Set("Content-Type", "application/zip")
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				for key, value := range map[string]string{"Cache-Control": "no-store", "X-Softpractice-Assignment-ID": "lesson-08", "X-Softpractice-Assignment-Version": "1", "X-Softpractice-Base-Revision-ID": base, "X-Softpractice-Prior-Assignment-ID": "lesson-06", "X-Softpractice-Revision-ID": revision, "X-Softpractice-Content-SHA256": digest} {
					w.Header().Set(key, value)
				}
				if mode == "bad-hash" {
					w.Header().Set("X-Softpractice-Content-SHA256", strings.Repeat("a", 64))
				}
				if mode == "wrong-base" {
					w.Header().Set("X-Softpractice-Base-Revision-ID", uuid.NewString())
				}
				if mode == "truncated" {
					w.Write(data[:len(data)/2])
					return
				}
				w.Write(data)
			}))
			defer server.Close()
			client := savedTestClient(t, server.URL)
			t.Setenv("SOFTPRACTICE_CHECK_INPUTS_ROOT", "/untrusted-inherited-root")
			if mode == "tamper" {
				config.PriorRevisions = []string{"lesson-07"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output bytes.Buffer
			if mode == "snapshot" {
				repo, err := inspectGitRepository(context.Background(), root, true)
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(repo.ArchivePath)
				archiveBefore, err := os.ReadFile(repo.ArchivePath)
				if err != nil {
					t.Fatal(err)
				}
				err = runSubmissionChecks(context.Background(), client, repo, &output, &output)
				archiveAfter, _ := os.ReadFile(repo.ArchivePath)
				if !bytes.Equal(archiveBefore, archiveAfter) {
					t.Fatal("input entered submission archive")
				}
				if err != nil {
					t.Fatalf("snapshot checks: %v\n%s", err, output.String())
				}
			} else if mode == "published" {
				// The MCP check tool reaches history through the same materializer.
				if err := newLearnerUseCases(client, root).CheckPublished(context.Background(), &output, &output); err != nil {
					t.Fatalf("published checks: %v\n%s", err, output.String())
				}
				if !strings.Contains(output.String(), "INPUT_ROOT=") {
					t.Fatalf("published checks did not run the history check: %s", output.String())
				}
			} else {
				if mode == "cancel" {
					recorder := &cancelInputWriter{buffer: &output, cancel: cancel}
					err = runChecksWithInputs(ctx, client, root, root, config, recorder, recorder)
				} else {
					err = runChecksWithInputs(ctx, client, root, root, config, &output, &output)
				}
				wantError := mode != "working"
				if (err != nil) != wantError {
					t.Fatalf("checks error=%v output=%s", err, output.String())
				}
				expectedError := map[string]string{"mutate": "changed historical inputs", "stale": "workspace changed", "bad-hash": "hash does not match", "wrong-base": "invalid X-Softpractice-Base-Revision-ID", "truncated": "incomplete", "tamper": "differ from the published", "cancel": "context canceled"}[mode]
				if expectedError != "" && (err == nil || !strings.Contains(err.Error(), expectedError)) {
					t.Fatalf("unexpected failure %v (want %s)", err, expectedError)
				}

			}
			for _, line := range strings.Split(output.String(), "\n") {
				if strings.HasPrefix(line, "INPUT_ROOT=") {
					path := strings.TrimPrefix(line, "INPUT_ROOT=")
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("input directory not cleaned")
					}
				}
			}
		})
	}
}

// The child signals that materialization is complete before cancellation, so
// cleanup is verified on the process failure path rather than before setup.
type cancelInputWriter struct {
	buffer *bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelInputWriter) Write(data []byte) (int, error) {
	n, err := w.buffer.Write(data)
	if bytes.Contains(data, []byte("INPUT_ROOT=")) {
		w.cancel()
	}
	return n, err
}

func TestHistoricalDataPathProjectsOnlyDeclaredSQLData(t *testing.T) {
	for path, want := range map[string]bool{
		"loading.json":           true,
		"sql/ingest.sql":         true,
		"sql/migrations/001.sql": true,
		"sql/queries/top.sql":    true,
		"sql/queries/a/b.sql":    false,
		"sql/migrations/a/b.sql": false,
		"sql/queries/notes.md":   false,
		"sql/migrations/001.py":  false,
		"sql/other/x.sql":        false,
		"nested/loading.json":    false,
		"tools/__init__.py":      false,
		"sql/queries/":           false,
		"sql/ingest.sql.bak":     false,
		"AGENTS.md":              false,
	} {
		if got := historicalDataPath(path); got != want {
			t.Errorf("historicalDataPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestRemoveCheckInputsClearsReadOnlyDirectories(t *testing.T) {
	root, err := os.MkdirTemp(t.TempDir(), "inputs-")
	if err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "lesson-06", "sql")
	if err := os.MkdirAll(locked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "ingest.sql"), []byte("SELECT 6"), 0400); err != nil {
		t.Fatal(err)
	}
	// A check may tighten permissions on the projected tree it was given.
	for _, dir := range []string{locked, filepath.Dir(locked)} {
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeCheckInputs(root); err != nil {
		t.Fatalf("remove inputs: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("input directory remains: %v", err)
	}
}
