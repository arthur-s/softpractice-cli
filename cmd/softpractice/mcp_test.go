package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The protocol versions a client may speak: the current one, where the
// confirmation travels as a multi round-trip tool result, and the previous
// one, where the server sends elicitation/create itself.
var mcpTestProtocols = []string{"2026-07-28", "2025-11-25"}

// leakMarkers are texts of the evaluation the MCP server must never return:
// the reviewer's directions, the counterexamples' next steps, and the text of
// the reviewer's questions.
var leakMarkers = []string{reviewerDirectionMarker, "NEXT-STEP-TEXT", "DEFENSE-QUESTION-TEXT"}

// mcpLessonAPI is a fake API for a folder on pa-foundation-01 v2 that accepts
// submissions and answers every evaluation with the revise projection.
type mcpLessonAPI struct {
	server      *httptest.Server
	workspaceID string
	submissions atomic.Int32
}

func newMCPLessonAPI(t *testing.T) *mcpLessonAPI {
	t.Helper()
	api := &mcpLessonAPI{workspaceID: uuid.NewString()}
	api.server = lessonBumpServer(t, api.workspaceID, func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/workspaces/"+api.workspaceID+"/assignments/pa-foundation-01/submissions":
			_, _ = io.Copy(io.Discard, request.Body)
			api.submissions.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"submission_id": currentSubmissionID, "revision_id": uuid.NewString(), "evaluation_job_id": uuid.NewString(),
				"job_state": "queued", "replayed": false, "submitted_at": time.Now().UTC(),
				"submission_url": "/v1/submissions/" + currentSubmissionID,
				"evaluation_url": "/v1/submissions/" + currentSubmissionID + "/evaluation",
			})
		case request.URL.Path == "/v1/submissions/"+currentSubmissionID+"/evaluation":
			writeTestJSON(writer, reviseEvaluationBody(currentSubmissionID))
		default:
			http.NotFound(writer, request)
		}
	})
	t.Cleanup(api.server.Close)
	return api
}

// connectMCP starts the server on useCases and connects an in-process client
// speaking protocol.
func connectMCP(t *testing.T, useCases learnerUseCases, options *mcp.ClientOptions, protocol string) *mcp.ClientSession {
	t.Helper()
	ctx := withSettings(context.Background(), runtimeSettings{Language: languageEnglish, WebURL: "https://softpractice.example"})
	server, err := newMCPServer(ctx, useCases, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, options)
	session, err := client.Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Wait()
	})
	return session
}

func elicitationAnswer(action string, before func()) *mcp.ClientOptions {
	return &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		if before != nil {
			before()
		}
		return &mcp.ElicitResult{Action: action}, nil
	}}
}

// callTool calls a tool and returns its result as JSON, failing the test on
// a protocol error.
func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	if arguments == nil {
		arguments = map[string]any{}
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return result, string(body)
}

// toolErrorCode is the code of a tool error result.
func toolErrorCode(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("result is not a tool error: %+v", result)
	}
	var payload struct {
		Error mcpToolError `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &payload); err != nil {
		t.Fatalf("tool error is not JSON: %v", err)
	}
	return payload.Error.Code
}

func TestMCPListsTheLessonLoopToolsOnly(t *testing.T) {
	session := connectMCP(t, newLearnerUseCases(nil, t.TempDir()), nil, mcpTestProtocols[0])
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		wantReadOnly := !slices.Contains([]string{"check", "submit", "update"}, tool.Name)
		if readOnly != wantReadOnly {
			t.Errorf("tool %s read-only = %v, want %v", tool.Name, readOnly, wantReadOnly)
		}
		if tool.Description == "" || tool.OutputSchema == nil {
			t.Errorf("tool %s has no description or output schema", tool.Name)
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if strings.Contains(string(schema), "direction") {
			t.Errorf("tool %s takes a directions parameter: %s", tool.Name, schema)
		}
	}
	slices.Sort(names)
	want := []string{"check", "hints", "material", "result", "status", "submissions", "submit", "task", "update"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	resources, err := session.ListResources(context.Background(), nil)
	if err != nil || len(resources.Resources) != 2 {
		t.Fatalf("resources = %+v, %v", resources, err)
	}
}

// Neither result nor submit with wait_seconds returns the reviewer's
// directions, the counterexamples' next steps, or the questions' text, while
// what the review found is there.
func TestMCPNeverReturnsDirectionsOrQuestions(t *testing.T) {
	api := newMCPLessonAPI(t)
	root := createPinnedLinkedGitRepository(t, api.workspaceID, "pa-foundation-01", 2)
	session := connectMCP(t, newLearnerUseCases(savedTestClient(t, api.server.URL), root),
		elicitationAnswer("accept", nil), mcpTestProtocols[0])

	result, body := callTool(t, session, "result", map[string]any{"submission_id": currentSubmissionID})
	_, submitted := callTool(t, session, "submit", map[string]any{"wait_seconds": 30})
	for _, output := range []string{body, submitted} {
		for _, marker := range leakMarkers {
			if strings.Contains(output, marker) {
				t.Fatalf("tool output contains %q:\n%s", marker, output)
			}
		}
	}
	if result.IsError || !strings.Contains(body, "FINDING-TITLE-TEXT") || !strings.Contains(body, `"questions_count":1`) ||
		!strings.Contains(body, `"directions_hidden":true`) {
		t.Fatalf("result lacks the review:\n%s", body)
	}
	if !strings.Contains(submitted, "REVIEW-SUMMARY-TEXT") || api.submissions.Load() != 1 {
		t.Fatalf("submit with wait_seconds = %s (submissions %d)", submitted, api.submissions.Load())
	}
}

func TestMCPSubmitAsksForConfirmation(t *testing.T) {
	for _, protocol := range mcpTestProtocols {
		t.Run(protocol, func(t *testing.T) {
			api := newMCPLessonAPI(t)
			root := createPinnedLinkedGitRepository(t, api.workspaceID, "pa-foundation-01", 2)
			useCases := newLearnerUseCases(savedTestClient(t, api.server.URL), root)

			declined := connectMCP(t, useCases, elicitationAnswer("decline", nil), protocol)
			result, _ := callTool(t, declined, "submit", nil)
			if code := toolErrorCode(t, result); code != "declined" || api.submissions.Load() != 0 {
				t.Fatalf("declined submit: code %q, submissions %d", code, api.submissions.Load())
			}

			var shown string
			accepted := connectMCP(t, useCases, &mcp.ClientOptions{
				ElicitationHandler: func(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					shown = request.Params.Message
					return &mcp.ElicitResult{Action: "accept"}, nil
				},
			}, protocol)
			result, body := callTool(t, accepted, "submit", nil)
			if result.IsError || api.submissions.Load() != 1 || !strings.Contains(body, currentSubmissionID) ||
				!strings.Contains(body, `"local_checks":"not_run"`) {
				t.Fatalf("accepted submit = %s (submissions %d)", body, api.submissions.Load())
			}
			if !strings.Contains(shown, "pa-foundation-01 v2") || !strings.Contains(shown, "Commit: ") {
				t.Fatalf("confirmation message = %q", shown)
			}
		})
	}
}

// A client that cannot ask the person is refused: nothing is sent, and the
// person is told to use the terminal.
func TestMCPSubmitWithoutElicitationIsRefused(t *testing.T) {
	for _, protocol := range mcpTestProtocols {
		t.Run(protocol, func(t *testing.T) {
			api := newMCPLessonAPI(t)
			root := createPinnedLinkedGitRepository(t, api.workspaceID, "pa-foundation-01", 2)
			session := connectMCP(t, newLearnerUseCases(savedTestClient(t, api.server.URL), root), nil, protocol)
			result, body := callTool(t, session, "submit", nil)
			if code := toolErrorCode(t, result); code != "confirmation_unavailable" ||
				!strings.Contains(body, "softpractice submit") {
				t.Fatalf("submit without elicitation: %s", body)
			}
			if api.submissions.Load() != 0 {
				t.Fatalf("submitted %d times without a confirmation", api.submissions.Load())
			}
		})
	}
}

// When HEAD moves while the person is looking at the confirmation, the
// confirmed commit is not the one that would be sent: nothing is sent.
func TestMCPSubmitRefusesAChangeAfterConfirmation(t *testing.T) {
	api := newMCPLessonAPI(t)
	root := createPinnedLinkedGitRepository(t, api.workspaceID, "pa-foundation-01", 2)
	session := connectMCP(t, newLearnerUseCases(savedTestClient(t, api.server.URL), root),
		elicitationAnswer("accept", func() { commitLearnerWork(t, root, "late.py", "# late change\n") }), mcpTestProtocols[0])
	result, _ := callTool(t, session, "submit", nil)
	if code := toolErrorCode(t, result); code != "changed_since_confirmation" || api.submissions.Load() != 0 {
		t.Fatalf("code %q, submissions %d", code, api.submissions.Load())
	}
}

// update shows the files it will change and applies them only on accept.
func TestMCPUpdateConfirmsTheFileList(t *testing.T) {
	workspaceID := uuid.NewString()
	server, refactored := refactoredLessonServer(t, workspaceID)
	defer server.Close()
	root := createPinnedLinkedGitRepository(t, workspaceID, "pa-foundation-01", 1)
	var shown string
	session := connectMCP(t, newLearnerUseCases(savedTestClient(t, server.URL), root), &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			shown = request.Params.Message
			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	}, mcpTestProtocols[0])
	result, body := callTool(t, session, "update", nil)
	if result.IsError || !strings.Contains(body, `"update_kind":"lesson_version"`) {
		t.Fatalf("update = %s", body)
	}
	if !strings.Contains(shown, "replace main.py") || !strings.Contains(shown, "v1 → v2") {
		t.Fatalf("confirmation message = %q", shown)
	}
	if content, err := os.ReadFile(filepath.Join(root, "main.py")); err != nil || string(content) != string(refactored) {
		t.Fatalf("main.py = %q after an accepted update", content)
	}
}

func TestMCPLoginRequiredDoesNotLeakTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/v1/auth/device") {
			t.Errorf("the MCP server started a sign-in: %s", request.URL.Path)
		}
		writeTestAPIError(writer, http.StatusUnauthorized, "invalid_credentials")
	}))
	defer server.Close()
	root := createPinnedLinkedGitRepository(t, uuid.NewString(), "pa-foundation-01", 2)
	session := connectMCP(t, newLearnerUseCases(savedTestClient(t, server.URL), root), nil, mcpTestProtocols[0])
	for _, tool := range []string{"status", "task", "result"} {
		result, body := callTool(t, session, tool, nil)
		if code := toolErrorCode(t, result); code != "login_required" {
			t.Fatalf("%s: code %q: %s", tool, code, body)
		}
		if strings.Contains(body, "access-token") || strings.Contains(body, "refresh-token") {
			t.Fatalf("%s leaks a token: %s", tool, body)
		}
		if !strings.Contains(body, "softpractice login") {
			t.Fatalf("%s does not tell the person how to sign in: %s", tool, body)
		}
	}
}

// status points each next step at an MCP tool, not at a CLI command.
func TestMCPNextActionsNameTools(t *testing.T) {
	actions := mcpNextActions([]nextAction{
		{Code: "submit", Command: "softpractice submit --wait"},
		{Code: "wait_result", Command: "softpractice result --wait"},
		{Code: "stash_changes", Command: "git stash"},
		{Code: "answer_questions", URL: "https://softpractice.example/submissions/x/result"},
	})
	if actions[0].Tool != "submit" || actions[0].Command != "" ||
		actions[1].Tool != "result" || actions[1].Arguments["wait_seconds"] != mcpSuggestedWait ||
		actions[2].Tool != "" || actions[2].Command != "git stash" ||
		actions[3].Tool != "" || actions[3].URL == "" {
		t.Fatalf("next actions = %+v", actions)
	}
}

// `softpractice mcp` speaks JSON-RPC on standard output and writes nothing
// else there.
func TestMCPCommandWritesOnlyJSONRPCToStdout(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(configDirectoryEnv, directory)
	t.Setenv("SOFTPRACTICE_CREDENTIALS_DIR", directory)
	t.Setenv("SOFTPRACTICE_API_URL", "http://127.0.0.1:9")
	t.Setenv("SOFTPRACTICE_LANGUAGE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")

	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	var errorOutput lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), []string{"mcp", "--project", t.TempDir()}, inputReader, outputWriter, &errorOutput)
		_ = outputWriter.Close()
	}()
	lines := bufio.NewScanner(outputReader)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	send := func(message string) {
		if _, err := io.WriteString(inputWriter, message+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() map[string]any {
		if !lines.Scan() {
			t.Fatalf("no response: %v; stderr: %s", lines.Err(), errorOutput.String())
		}
		var message map[string]any
		if err := json.Unmarshal(lines.Bytes(), &message); err != nil || message["jsonrpc"] != "2.0" {
			t.Fatalf("stdout line is not JSON-RPC: %q", lines.Text())
		}
		return message
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	receive()
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if tools := receive()["result"].(map[string]any)["tools"].([]any); len(tools) != 9 {
		t.Fatalf("tools/list returned %d tools", len(tools))
	}
	// Outside a Git repository the error is a tool result, in Russian by
	// default when no language is configured anywhere. The system locale
	// still counts: Windows reports its display language.
	wantAction := "Запустите"
	if languageFromLocale(systemLocaleName()) == languageEnglish {
		wantAction = "Start the MCP server"
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"task","arguments":{}}}`)
	response := receive()["result"].(map[string]any)
	text := response["content"].([]any)[0].(map[string]any)["text"].(string)
	if response["isError"] != true || !strings.Contains(text, "not_git_repository") || !strings.Contains(text, wantAction) {
		t.Fatalf("task outside a project = %v", response)
	}
	_ = inputWriter.Close()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, mcp.ErrConnectionClosed) {
			t.Fatalf("mcp command: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the MCP server did not stop when standard input closed")
	}
	for lines.Scan() {
		t.Fatalf("unexpected stdout after shutdown: %q", lines.Text())
	}
}
