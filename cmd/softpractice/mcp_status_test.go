package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestMCPStatusOutsideProjectWithoutServer(t *testing.T) {
	t.Setenv(configDirectoryEnv, t.TempDir())
	t.Setenv("SOFTPRACTICE_CREDENTIALS_DIR", t.TempDir())
	t.Chdir(t.TempDir())
	var output bytes.Buffer
	err := run(context.Background(), []string{"mcp", "status", "--json"}, strings.NewReader(""), &output, io.Discard)
	var status localMCPStatus
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status.State != "not_running" || status.Project != "" || status.PID != 0 {
		t.Fatalf("offline JSON status: %+v, %v", status, err)
	}
	output.Reset()
	if err := mcpStatusCommand(context.Background(), nil, &output, io.Discard); err != nil || !strings.Contains(output.String(), "softpractice mcp --project DIR") {
		t.Fatalf("offline status: %v, %s", err, output.String())
	}
}

func TestMCPStatusCorruptSecretIsAnError(t *testing.T) {
	t.Setenv(configDirectoryEnv, t.TempDir())
	path, err := localMCPTokenPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := readLocalMCPStatus(context.Background())
	if err == nil || status.State == "running" {
		t.Fatalf("corrupt secret status: %+v, %v", status, err)
	}
}
