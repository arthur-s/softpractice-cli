package localchecks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunStopsInOrderAtFirstFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the portable Unix test executable")
	}
	config := Config{SchemaVersion: 1, Checks: []Check{
		{Name: "first", Executable: "sh", Args: []string{"-c", "printf first"}, TimeoutSeconds: 5},
		{Name: "second", Executable: "sh", Args: []string{"-c", "printf second; exit 7"}, TimeoutSeconds: 5},
		{Name: "third", Executable: "sh", Args: []string{"-c", "printf third"}, TimeoutSeconds: 5},
	}}
	var output bytes.Buffer
	err := Run(context.Background(), t.TempDir(), config, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("Run error = %v", err)
	}
	if got := output.String(); !strings.Contains(got, "first") || !strings.Contains(got, "second") || strings.Contains(got, "third") {
		t.Fatalf("output = %q", got)
	}
}

func TestRunReportsMissingExecutableAndEmptyOutputContract(t *testing.T) {
	err := Run(context.Background(), t.TempDir(), Config{Checks: []Check{{
		Name: "missing", Executable: "softpractice-definitely-missing-executable", TimeoutSeconds: 1,
	}}}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing executable error = %v", err)
	}
	if runtime.GOOS != "windows" {
		err = Run(context.Background(), t.TempDir(), Config{Checks: []Check{{
			Name: "format", Executable: "sh", Args: []string{"-c", "printf dirty.go"}, TimeoutSeconds: 1, RequireEmptyStdout: true,
		}}}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "expected no output") {
			t.Fatalf("empty output error = %v", err)
		}
	}
}

func TestRunTimeoutAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the portable Unix test executable")
	}
	config := Config{Checks: []Check{{Name: "slow", Executable: "sh", Args: []string{"-c", "sleep 30"}, TimeoutSeconds: 1}}}
	started := time.Now()
	err := Run(context.Background(), t.TempDir(), config, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") || time.Since(started) > 5*time.Second {
		t.Fatalf("timeout error = %v after %s", err, time.Since(started))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Run(ctx, t.TempDir(), config, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestLoadFailsClosed(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "softpractice update") {
		t.Fatalf("missing config error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".softpractice"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, RelativePath), []byte(`{"schema_version":9,"checks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown version error = %v", err)
	}
}
