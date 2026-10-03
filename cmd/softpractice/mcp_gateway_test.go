package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectGateway(t *testing.T, client *learnercli.Client, options *mcp.ClientOptions, protocol string) *mcp.ClientSession {
	t.Helper()
	ctx := withSettings(context.Background(), runtimeSettings{Language: languageEnglish, WebURL: "https://softpractice.example"})
	server, err := newMCPGateway(ctx, client, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "desktop-test", Version: "1"}, options).Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = serverSession.Wait() })
	return session
}

func startGatewayLesson(t *testing.T, root string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var banner lockedBuffer
	go func() {
		done <- mcpCommand(ctx, nil, []string{"--project", root}, strings.NewReader(""), io.Discard, &banner)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(banner.String(), "39473") {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("lesson startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("lesson startup timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("lesson shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("lesson shutdown timed out")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestMCPGatewayOfflineThenLessonThenOfflineOnSameConnection(t *testing.T) {
	for _, protocol := range mcpTestProtocols {
		t.Run(protocol, func(t *testing.T) {
			t.Setenv(configDirectoryEnv, t.TempDir())
			api := newMCPLessonAPI(t)
			root := createPinnedLinkedGitRepository(t, api.workspaceID, "pa-foundation-01", 2)
			session := connectGateway(t, savedTestClient(t, api.server.URL), elicitationAnswer("accept", nil), protocol)
			tools, err := session.ListTools(context.Background(), nil)
			if err != nil || len(tools.Tools) != 9 {
				t.Fatalf("offline tools: %+v, %v", tools, err)
			}
			result, _ := callTool(t, session, "status", nil)
			if code := toolErrorCode(t, result); code != "lesson_server_not_running" {
				t.Fatalf("offline status: %s", code)
			}
			stop := startGatewayLesson(t, root)
			result, body := callTool(t, session, "task", nil)
			if result.IsError || !strings.Contains(body, "pa-foundation-01") {
				t.Fatalf("active task: %s", body)
			}
			result, body = callTool(t, session, "submit", nil)
			if result.IsError || api.submissions.Load() != 1 {
				t.Fatalf("gateway submit: %s, submissions=%d", body, api.submissions.Load())
			}

			stop()
			result, _ = callTool(t, session, "task", nil)
			if code := toolErrorCode(t, result); code != "lesson_server_not_running" {
				t.Fatalf("stopped lesson: %s", code)
			}
			// The same client session recovers without another initialize handshake.
			startGatewayLesson(t, root)
			result, body = callTool(t, session, "task", nil)
			if result.IsError || !strings.Contains(body, "pa-foundation-01") {
				t.Fatalf("restarted task: %s", body)
			}
		})
	}
}

func TestMCPGatewayRestartInvalidatesSubmissionConfirmation(t *testing.T) {
	for _, protocol := range mcpTestProtocols {
		t.Run(protocol, func(t *testing.T) {
			t.Setenv(configDirectoryEnv, t.TempDir())
			api := newMCPLessonAPI(t)
			root := createPinnedLinkedGitRepository(t, api.workspaceID, "pa-foundation-01", 2)
			stop := startGatewayLesson(t, root)
			options := elicitationAnswer("accept", func() { stop(); startGatewayLesson(t, root) })
			session := connectGateway(t, savedTestClient(t, api.server.URL), options, protocol)
			result, body := callTool(t, session, "submit", nil)
			if code := toolErrorCode(t, result); code != "changed_since_confirmation" || api.submissions.Load() != 0 {
				t.Fatalf("stale confirmation: %s, submissions=%d", body, api.submissions.Load())
			}
		})
	}
}

func TestMCPGatewayStdioInitializesWithoutLesson(t *testing.T) {
	t.Setenv(configDirectoryEnv, t.TempDir())
	input, writer := io.Pipe()
	output, outputWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { _ = writer.Close(); _ = output.Close() })
	done := make(chan error, 1)
	go func() {
		if runtime.GOOS == "darwin" {
			done <- mcpCommand(ctx, nil, []string{"connect"}, input, outputWriter, io.Discard)
		} else {
			done <- runMCPGateway(ctx, nil, input, outputWriter, io.Discard)
		}
		_ = outputWriter.Close()
	}()
	scanner := bufio.NewScanner(output)
	send := func(line string) {
		t.Helper()
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("no JSON-RPC reply: %v", scanner.Err())
		}
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil || value["jsonrpc"] != "2.0" {
			t.Fatalf("stdout: %s, %v", scanner.Text(), err)
		}
		return value
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"desktop","version":"1"}}}`)
	read()
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if tools := read()["result"].(map[string]any)["tools"].([]any); len(tools) != 9 {
		t.Fatalf("tools: %d", len(tools))
	}
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not stop when Desktop closed stdin")
	}
}
