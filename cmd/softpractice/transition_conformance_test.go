package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

type cliTransitionSuite struct {
	SchemaVersion   int                 `json:"schema_version"`
	SemanticVersion string              `json:"semantic_version"`
	Cases           []cliTransitionCase `json:"cases"`
}

type cliTransitionCase struct {
	ID              string                 `json:"id"`
	Initial         map[string]string      `json:"initial"`
	InitialDirs     []string               `json:"initial_dirs"`
	InitialSymlinks map[string]string      `json:"initial_symlinks"`
	Payload         []cliTransitionPayload `json:"payload"`
	Operations      []struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	} `json:"operations"`
	Inject    string            `json:"inject"`
	Outcome   string            `json:"outcome"`
	Expected  map[string]string `json:"expected"`
	Unchanged bool              `json:"unchanged"`
}

type cliTransitionPayload struct {
	Path     string `json:"path"`
	Body     string `json:"body"`
	Kind     string `json:"kind"`
	Hash     string `json:"hash"`
	Declared *bool  `json:"declared"`
}

func TestCourseUpdateTransitionConformanceV1(t *testing.T) {
	vectorPath := localTransitionConformancePath("transition-conformance-v1.json")
	raw, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.ReadFile(localTransitionConformancePath("transition-conformance-v1.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	fields := strings.Fields(string(identity))
	if len(fields) != 2 || fields[0] != hex.EncodeToString(digest[:]) || fields[1] != filepath.Base(vectorPath) {
		t.Fatal("transition conformance identity does not match its vectors")
	}
	var suite cliTransitionSuite
	if err := json.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	if suite.SchemaVersion != 1 || suite.SemanticVersion != "1.1.0" || len(suite.Cases) != 38 {
		t.Fatalf("unexpected conformance suite identity: %#v", suite)
	}
	seen := map[string]bool{}
	for _, test := range suite.Cases {
		t.Run(test.ID, func(t *testing.T) {
			if test.ID == "" || seen[test.ID] {
				t.Fatalf("missing or duplicate case ID %q", test.ID)
			}
			seen[test.ID] = true
			runCLITransitionCase(t, test)
		})
	}
}

func TestCourseUpdateTransitionConformanceMatchesCanonicalCheckout(t *testing.T) {
	softpracticeRoot := os.Getenv("SOFTPRACTICE_REPOSITORY")
	if softpracticeRoot == "" {
		t.Skip("SOFTPRACTICE_REPOSITORY is not set; standalone CLI tests use the derived snapshot")
	}
	for _, name := range []string{"transition-conformance-v1.json", "transition-conformance-v1.sha256"} {
		local, err := os.ReadFile(localTransitionConformancePath(name))
		if err != nil {
			t.Fatal(err)
		}
		canonicalPath := filepath.Join(softpracticeRoot, "internal", "authoring", "lessonbuilder", "testdata", name)
		canonical, err := os.ReadFile(canonicalPath)
		if err != nil {
			t.Fatalf("read canonical transition conformance file %s: %v", canonicalPath, err)
		}
		if !reflect.DeepEqual(local, canonical) {
			t.Fatalf("derived transition conformance file %s differs from canonical checkout", name)
		}
	}
}

func TestCourseUpdateReportsRollbackFailure(t *testing.T) {
	project := t.TempDir()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "decision.md"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "decision.md"), []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("after"))
	update := learnercli.CourseUpdate{
		Files: []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{{Path: "decision.md", SHA256: hex.EncodeToString(digest[:])}},
		Operations: []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{{Kind: "replace", Path: "decision.md"}},
	}

	err := applyCourseUpdateWithWriter(
		context.Background(),
		project,
		stage,
		update,
		func(root *os.Root, relative string, _ []byte, _ os.FileMode) error {
			if err := root.Remove(filepath.FromSlash(relative)); err != nil {
				return err
			}
			if err := root.Mkdir(filepath.FromSlash(relative), 0o700); err != nil {
				return err
			}
			return errors.New("injected apply failure")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "injected apply failure") {
		t.Fatalf("error = %v, want original apply failure", err)
	}
	if !strings.Contains(err.Error(), "restore course update file decision.md") {
		t.Fatalf("error = %v, want rollback failure", err)
	}
}

func TestCourseUpdateRemovesDirectoriesCreatedBeforeMkdirAllFailure(t *testing.T) {
	project := t.TempDir()
	stage := t.TempDir()
	target := filepath.Join(stage, "nested", "deeper", "new.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("new"))
	update := learnercli.CourseUpdate{
		Files: []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{{Path: "nested/deeper/new.txt", SHA256: hex.EncodeToString(digest[:])}},
		Operations: []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{{Kind: "add", Path: "nested/deeper/new.txt"}},
	}
	err := applyCourseUpdateWithFilesystem(
		context.Background(), project, stage, update,
		func(root *os.Root, relative string, data []byte, mode os.FileMode) error {
			return root.WriteFile(filepath.FromSlash(relative), data, mode)
		},
		func(root *os.Root, _ string, _ os.FileMode) error {
			if err := root.Mkdir("nested", 0o755); err != nil {
				return err
			}
			return errors.New("injected mkdir failure")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "injected mkdir failure") {
		t.Fatalf("error = %v, want injected mkdir failure", err)
	}
	if _, err := os.Lstat(filepath.Join(project, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partially created directory remains after rollback: %v", err)
	}
}

func localTransitionConformancePath(name string) string {
	return filepath.Join("testdata", name)
}

func runCLITransitionCase(t *testing.T, test cliTransitionCase) {
	t.Helper()
	base := t.TempDir()
	root, stage := filepath.Join(base, "project"), filepath.Join(base, "stage")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCLIFixtureFiles(t, root, test.Initial)
	for _, relative := range test.InitialDirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(relative)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for relative, target := range test.InitialSymlinks {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	before := cliFilesystemSnapshot(t, root)
	update := learnercli.CourseUpdate{}
	for _, payload := range test.Payload {
		declared := payload.Declared == nil || *payload.Declared
		if declared {
			digest := sha256.Sum256([]byte(payload.Body))
			hash := hex.EncodeToString(digest[:])
			if payload.Hash == "incorrect" {
				hash = strings.Repeat("0", 64)
			}
			update.Files = append(update.Files, struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			}{Path: payload.Path, SHA256: hash})
		}
		materializeCLIPayload(t, stage, payload)
	}
	for _, operation := range test.Operations {
		update.Operations = append(update.Operations, struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{Kind: operation.Kind, Path: operation.Path})
	}
	err := verifyCourseUpdateFiles(stage, update)
	if err == nil {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writes := 0
		writer := func(root *os.Root, relative string, data []byte, mode os.FileMode) error {
			writes++
			switch test.Inject {
			case "cancel-after-first-write":
				err := root.WriteFile(filepath.FromSlash(relative), data, mode)
				cancel()
				return err
			case "fail-after-second-write":
				if writes == 2 {
					return errors.New("injected write failure")
				}
			case "fail-after-partial-second-write":
				err := root.WriteFile(filepath.FromSlash(relative), data, mode)
				if err == nil && writes == 2 {
					return errors.New("injected failure after partial write")
				}
				return err
			}
			return root.WriteFile(filepath.FromSlash(relative), data, mode)
		}
		err = applyCourseUpdateWithWriter(ctx, root, stage, update, writer)
	}
	if test.Outcome == "accepted" && err != nil {
		t.Fatalf("valid transition rejected: %v", err)
	}
	if test.Outcome != "accepted" && err == nil {
		t.Fatal("invalid transition accepted")
	}
	if test.Outcome == "cancelled" && !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if test.Unchanged && !reflect.DeepEqual(before, cliFilesystemSnapshot(t, root)) {
		t.Fatal("project changed after rejected transition")
	}
	if test.Outcome == "accepted" && !reflect.DeepEqual(test.Expected, cliRegularFiles(t, root)) {
		t.Fatalf("project files differ from expected")
	}
}

func materializeCLIPayload(t *testing.T, root string, payload cliTransitionPayload) {
	t.Helper()
	if payload.Kind == "missing" || filepath.IsAbs(payload.Path) || !filepath.IsLocal(filepath.FromSlash(payload.Path)) {
		return
	}
	target := filepath.Join(root, filepath.FromSlash(payload.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	switch payload.Kind {
	case "", "regular":
		if err := os.WriteFile(target, []byte(payload.Body), 0o600); err != nil {
			t.Fatal(err)
		}
	case "symlink":
		if err := os.Symlink("missing-target", target); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	case "special":
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown payload kind %q", payload.Kind)
	}
}

func writeCLIFixtureFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for relative, contents := range files {
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func cliFilesystemSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || path == root {
			return walkErr
		}
		relative, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(data)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":" + target
		}
		result[filepath.ToSlash(relative)] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func cliRegularFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || path == root || !entry.Type().IsRegular() {
			return walkErr
		}
		relative, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
