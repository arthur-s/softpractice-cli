package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLocalMCPBridgeAndProjectSwitch(t *testing.T) {
	t.Setenv(configDirectoryEnv, t.TempDir())
	path, err := localMCPTokenPath()
	if err != nil {
		t.Fatal(err)
	}
	var previousToken string
	for attempt := range 2 {
		root := createLinkedGitRepository(t, uuid.NewString())
		ctx, cancel := context.WithCancel(context.Background())
		var banner lockedBuffer
		done := make(chan error, 1)
		args := []string{"--listen", "--project", root}
		if attempt == 0 {
			t.Chdir(root)
			args = []string{"--listen"}
		} else {
			t.Chdir(t.TempDir())
		}
		go func() {
			done <- mcpCommand(ctx, nil, args, strings.NewReader(""), io.Discard, &banner)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(banner.String(), "39473") {
			select {
			case err := <-done:
				cancel()
				t.Fatalf("server startup: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("server startup timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
		token, err := os.ReadFile(path)
		if err != nil || len(token) != 64 || string(token) == previousToken {
			cancel()
			t.Fatalf("token: %q, %v", token, err)
		}
		previousToken = string(token)
		if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			cancel()
			t.Fatalf("secret permissions: %v, %v", info, err)
		}
		resolved, _ := filepath.EvalSymlinks(root)
		if !strings.Contains(banner.String(), resolved) {
			cancel()
			t.Fatalf("project missing: %s", banner.String())
		}
		// Starting another project cannot replace the active server's secret.
		if err := mcpCommand(ctx, nil, []string{"--listen", "--project", root}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			cancel()
			t.Fatal("second server started")
		}
		sameToken, _ := os.ReadFile(path)
		if !bytes.Equal(token, sameToken) {
			cancel()
			t.Fatal("second server changed the secret")
		}
		// An unauthenticated process never gets an MCP session.
		connection, err := net.DialTimeout("tcp4", localMCPAddress, time.Second)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_ = connection.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(connection, "wrong-secret\n")
		response, _ := io.ReadAll(connection)
		_ = connection.Close()
		if len(response) != 0 {
			cancel()
			t.Fatalf("unauthorized response: %q", response)
		}

		input, writer := io.Pipe()
		output, outputWriter := io.Pipe()
		bridgeDone := make(chan error, 1)
		go func() { bridgeDone <- connectLocalMCP(ctx, input, outputWriter); _ = outputWriter.Close() }()
		reader := bufio.NewReader(output)
		send := func(message string) {
			t.Helper()
			if _, err := io.WriteString(writer, message+"\n"); err != nil {
				cancel()
				t.Fatal(err)
			}
		}
		read := func() map[string]any {
			t.Helper()
			line, err := reader.ReadBytes('\n')
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			var message map[string]any
			if err := json.Unmarshal(line, &message); err != nil || message["jsonrpc"] != "2.0" {
				cancel()
				t.Fatalf("MCP response: %q, %v", line, err)
			}
			return message
		}
		send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"bridge-test","version":"1"}}}`)
		read()
		send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		if tools := read()["result"].(map[string]any)["tools"].([]any); len(tools) != 9 {
			cancel()
			t.Fatalf("tools: %v", tools)
		}
		cancel()
		_ = writer.Close()
		_ = output.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server did not stop")
		}
		select {
		case <-bridgeDone:
		case <-time.After(5 * time.Second):
			t.Fatal("bridge did not stop")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("token was not removed: %v", err)
		}
	}
}

func TestLocalMCPRejectsMissingProjectAndExplainsOfflineServer(t *testing.T) {
	t.Setenv(configDirectoryEnv, t.TempDir())
	var output bytes.Buffer
	err := mcpCommand(context.Background(), nil, []string{"--listen", "--project", t.TempDir()}, strings.NewReader(""), &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--project") || output.Len() != 0 {
		t.Fatalf("missing project: %v, stdout=%s", err, output.String())
	}
	err = connectLocalMCP(context.Background(), strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "softpractice mcp --project DIR") || output.Len() != 0 {
		t.Fatalf("offline bridge: %v, stdout=%s", err, output.String())
	}
}
