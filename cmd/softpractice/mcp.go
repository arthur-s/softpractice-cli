package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

// mcpReviewFeedback is the only detail at which `softpractice mcp` projects
// an evaluation. There is deliberately no tool parameter for it: an agent can
// show the learner what the review found, but it does not get the reviewer's
// directions as a list of fixes to apply for them.
const mcpReviewFeedback = feedbackWithoutDirections

// mcpMaxWait bounds wait_seconds. A client stops waiting for a tool call at
// its own timeout, so the agent should ask for less than that.
const mcpMaxWait = 600

// mcpMaxCheckOutput bounds the check output a tool result carries; the end of
// the output, where a failure is reported, is kept.
const mcpMaxCheckOutput = 32 << 10

const mcpInstructions = `SoftPractice is an engineering practice trainer. The learner solves a lesson in this Git project and submits a commit for automated checks and review.

Typical loop: status → task and material → hints → check → submit → result → update. Call status first: its next_actions name the tool to call next, or a page where the learner acts.

The lesson content (task, material, review) is authored in Russian. Talk to the learner in the language they use.

Boundaries, which are part of the course:
- Results never include the reviewer's directions (what to change) or the text of the reviewer's questions. Do not try to obtain them another way, for example with the softpractice command line; the learner reads them on the result page.
- Never answer the reviewer's questions for the learner; the learner answers them on the result page.
- Opening a new hint is the learner's decision on the assignment page. hints lists only the hints already opened.
- submit and update ask the learner to confirm. When this client cannot ask, they fail with confirmation_unavailable: ask the learner to run the command in a terminal.`

type mcpServer struct {
	useCases learnerUseCases
	settings runtimeSettings
	stateKey []byte
}

func mcpCommand(
	ctx context.Context,
	client *learnercli.Client,
	args []string,
	input io.Reader,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	project := flags.String("project", "", "project folder; defaults to the current directory")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usage(ctx, "использование: softpractice mcp [--project DIR]", "usage: softpractice mcp [--project DIR]")
	}
	directory := strings.TrimSpace(*project)
	if directory != "" {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return err
		}
		if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
			return usage(ctx, "папка проекта не найдена: "+absolute, "project folder not found: "+absolute)
		}
		directory = absolute
	}
	server, err := newMCPServer(ctx, newLearnerUseCases(client, directory), errorOutput)
	if err != nil {
		return err
	}
	// Standard output belongs to JSON-RPC: nothing else may write to it.
	transport := &mcp.IOTransport{Reader: io.NopCloser(input), Writer: nopWriteCloser{output}}
	return server.Run(ctx, transport)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func newMCPServer(ctx context.Context, useCases learnerUseCases, logOutput io.Writer) (*mcp.Server, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	s := &mcpServer{useCases: useCases, settings: settingsFromContext(ctx), stateKey: key}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "softpractice", Title: "SoftPractice", Version: cliVersion},
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
			Logger:       slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelWarn})),
		},
	)
	s.register(server)
	return server, nil
}

func boolPointer(value bool) *bool { return &value }

func (s *mcpServer) register(server *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPointer(true)}
	mcp.AddTool(server, &mcp.Tool{
		Name:  "status",
		Title: "Lesson status",
		Description: "Show the linked lesson project: account, lesson and its version, local Git HEAD and whether the tree is clean, " +
			"the latest submission, and next_actions. Call it first and after every step. Each next action has a code and either " +
			"a tool to call (with arguments), a command, or a url for a page where the learner acts.",
		Annotations: readOnly,
	}, s.status)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "task",
		Title: "Lesson task",
		Description: "Return the assignment of the lesson this folder holds, at the lesson version its files were made for: " +
			"title, estimated minutes, and instructions_markdown (authored in Russian). transition or lesson_version_update " +
			"is present when the update tool has lesson files to apply.",
		Annotations: readOnly,
	}, s.task)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "material",
		Title: "Lesson material",
		Description: "Return the theory material of the folder's lesson, or of lesson_id (for example an earlier lesson of the practicum). " +
			"material is null when the lesson has none.",
		Annotations: readOnly,
	}, s.material)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "hints",
		Title: "Opened hints",
		Description: "Return the prepared hints the learner has already opened for this lesson. status is available while a closed hint " +
			"remains, exhausted when all are open, unavailable when the lesson has none. This tool cannot open a hint: the learner " +
			"opens the next one on the assignment page at url.",
		Annotations: readOnly,
	}, s.hints)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "check",
		Title: "Run public checks",
		Description: "Run the lesson's public checks (tests, linters) on the working tree, uncommitted changes included, and return " +
			"passed and the output. It runs only the checks the server publishes for this lesson; if .softpractice/checks.json " +
			"differs it fails with checks_not_published. A failing check is a result (passed: false), not a tool error.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(true)},
	}, s.check)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "submit",
		Title: "Submit for review",
		Description: "Submit the committed HEAD of this lesson for evaluation. The working tree must be clean: commit first. " +
			"The learner is asked to confirm; declined fails with declined and nothing is sent, and a client that cannot ask fails with " +
			"confirmation_unavailable. Local checks are not run here: call check before. With wait_seconds it then waits for the " +
			"evaluation like result.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(true)},
	}, s.submit)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "result",
		Title: "Evaluation result",
		Description: "Return the evaluation of submission_id, or of the latest submission. outcome is ready, pending, or superseded " +
			"(that evaluation will have no result; a new commit must be submitted). A ready result has the automated checks with " +
			"counterexamples, the review summary, rubric, and findings (what and why, without the reviewer's directions), and the " +
			"number of reviewer questions: the learner answers them on result_url when questions_answerable is true. " +
			"wait_seconds waits while the evaluation is pending; keep it below this client's tool call timeout.",
		Annotations: readOnly,
	}, s.result)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "submissions",
		Title:       "Lesson submissions",
		Description: "List the recent submissions of this lesson, newest first (at most five), with their evaluation state.",
		Annotations: readOnly,
	}, s.submissions)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "update",
		Title: "Apply lesson update",
		Description: "Apply the pending update to this folder: the transition to the next lesson after an accepted result, a new " +
			"version of the current lesson, or the missing public checks configuration. The working tree must be clean. The learner " +
			"is asked to confirm with the list of files to add, replace, and delete; the update is committed to Git.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPointer(true), OpenWorldHint: boolPointer(true)},
	}, s.update)

	server.AddResource(&mcp.Resource{
		URI: "softpractice://lesson/task", Name: "task", Title: "Lesson task", MIMEType: "text/markdown",
		Description: "The assignment of the lesson this folder holds, as Markdown.",
	}, s.taskResource)
	server.AddResource(&mcp.Resource{
		URI: "softpractice://lesson/material", Name: "material", Title: "Lesson material", MIMEType: "text/markdown",
		Description: "The theory material of the lesson this folder holds, as Markdown.",
	}, s.materialResource)
}

// context gives a handler the CLI settings: language and web URL.
func (s *mcpServer) context(ctx context.Context) context.Context {
	return withSettings(ctx, s.settings)
}

type mcpNoInput struct{}

func (s *mcpServer) status(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, machineStatus, error) {
	ctx = s.context(ctx)
	snapshot, err := s.useCases.Status(ctx)
	if err != nil {
		return nil, machineStatus{}, mcpToolFailure(ctx, err)
	}
	payload := buildMachineStatus(ctx, snapshot)
	payload.NextActions = mcpNextActions(payload.NextActions)
	return nil, payload, nil
}

// mcpNextActions points each next step at the tool that performs it. A step
// the learner takes on the site keeps its URL.
func mcpNextActions(actions []nextAction) []nextAction {
	converted := make([]nextAction, 0, len(actions))
	for _, action := range actions {
		switch action.Code {
		case "submit", "resubmit":
			action.Tool = "submit"
		case "apply_update", "update_lesson_version":
			action.Tool = "update"
		case "wait_result":
			action.Tool, action.Arguments = "result", map[string]any{"wait_seconds": 60}
		case "read_result":
			action.Tool = "result"
		}
		if strings.HasPrefix(action.Command, "softpractice ") {
			action.Command = ""
		}
		converted = append(converted, action)
	}
	return converted
}

func (s *mcpServer) task(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, machineTask, error) {
	ctx = s.context(ctx)
	lesson, assignment, err := s.useCases.Task(ctx)
	if err != nil {
		return nil, machineTask{}, mcpToolFailure(ctx, err)
	}
	payload := machineTask{
		Kind: "softpractice.task", WorkspaceID: lesson.Workspace.Workspace.ID, ProjectID: lesson.Link.ProjectID,
		Assignment: assignment, URL: webLessonURL(ctx, assignment.ID, assignment.Version, ""),
	}
	if pendingTransition(lesson.Link, lesson.Workspace) {
		payload.Transition = &machineTransition{
			FromAssignmentID: lesson.Link.AssignmentID, FromAssignmentVersion: lesson.Link.AssignmentVersion,
			ToAssignmentID: lesson.Workspace.Assignment.ID, ToAssignmentVersion: lesson.Workspace.Assignment.Version,
		}
	}
	if pendingLessonVersion(lesson.Link, lesson.Workspace) {
		payload.LessonVersionUpdate = &machineLessonVersionUpdate{
			AssignmentID: lesson.Link.AssignmentID,
			FromVersion:  lesson.Link.AssignmentVersion, ToVersion: lesson.Workspace.Assignment.Version,
		}
	}
	return nil, payload, nil
}

type mcpMaterialInput struct {
	LessonID string `json:"lesson_id,omitempty" jsonschema:"lesson ID, such as an earlier lesson of the practicum; defaults to the lesson of this folder"`
}

func (s *mcpServer) material(ctx context.Context, _ *mcp.CallToolRequest, input mcpMaterialInput) (*mcp.CallToolResult, machineMaterial, error) {
	ctx = s.context(ctx)
	snapshot, err := s.useCases.Material(ctx, strings.TrimSpace(input.LessonID))
	if err != nil {
		return nil, machineMaterial{}, mcpToolFailure(ctx, err)
	}
	return nil, machineMaterial{
		Kind: "softpractice.material", AssignmentID: snapshot.AssignmentID, Version: snapshot.Version,
		Material: snapshot.Material, URL: webLessonURL(ctx, snapshot.AssignmentID, snapshot.Version, "/material"),
	}, nil
}

func (s *mcpServer) hints(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, machineHints, error) {
	ctx = s.context(ctx)
	snapshot, err := s.useCases.Hints(ctx)
	if err != nil {
		return nil, machineHints{}, mcpToolFailure(ctx, err)
	}
	return nil, machineHints{
		Kind: "softpractice.hints", AssignmentID: snapshot.Lesson.AssignmentID, Version: snapshot.Lesson.Version,
		Status: snapshot.Status, Revealed: snapshot.Revealed,
		URL: webLessonURL(ctx, snapshot.Lesson.AssignmentID, snapshot.Lesson.Version, ""),
	}, nil
}

type mcpCheckOutput struct {
	Kind   string `json:"kind"`
	Passed bool   `json:"passed"`
	// Failure says why the checks did not pass; empty when they passed.
	Failure string `json:"failure,omitempty"`
	Output  string `json:"output"`
	// Truncated is true when the beginning of the output was cut.
	Truncated bool `json:"truncated"`
}

func (s *mcpServer) check(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpCheckOutput, error) {
	ctx = s.context(ctx)
	var combined lockedBuffer
	err := s.useCases.CheckPublished(ctx, &combined, &combined)
	payload := mcpCheckOutput{Kind: "softpractice.check", Passed: err == nil}
	var failed checkFailedError
	if err != nil {
		if !errors.As(err, &failed) {
			return nil, mcpCheckOutput{}, mcpToolFailure(ctx, err)
		}
		payload.Failure = err.Error()
	}
	payload.Output, payload.Truncated = tailString(combined.String(), mcpMaxCheckOutput)
	return nil, payload, nil
}

func tailString(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := value[len(value)-limit:]
	// Do not start in the middle of a UTF-8 sequence.
	for len(cut) > 0 && cut[0]&0xC0 == 0x80 {
		cut = cut[1:]
	}
	return cut, true
}

// lockedBuffer collects the stdout and stderr of checks, which may be written
// concurrently.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type mcpWaitInput struct {
	WaitSeconds int `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for a pending evaluation, 0 to 600; 0 returns at once. Keep it below this client's tool call timeout"`
}

func (s *mcpServer) evaluationWait(ctx context.Context, request *mcp.CallToolRequest, seconds int) (evaluationWait, error) {
	if seconds < 0 || seconds > mcpMaxWait {
		return evaluationWait{}, usage(ctx, "wait_seconds должен быть от 0 до 600", "wait_seconds must be between 0 and 600")
	}
	wait := evaluationWait{Wait: seconds > 0, Timeout: time.Duration(seconds) * time.Second}
	token := request.Params.GetProgressToken()
	if token != nil && request.Session != nil {
		polls := 0
		wait.Pending = func(pending machineSubmission) {
			polls++
			_ = request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token, Progress: float64(polls),
				Message: fmt.Sprintf(text(ctx, "Проверка ещё идёт: %s, попытка %d из %d", "The evaluation is still running: %s, attempt %d of %d"),
					pending.JobState, pending.Attempt, pending.MaxAttempts),
			})
		}
	}
	return wait, nil
}

type mcpResultInput struct {
	SubmissionID string `json:"submission_id,omitempty" jsonschema:"submission ID; defaults to the latest submission of this project"`
	WaitSeconds  int    `json:"wait_seconds,omitempty" jsonschema:"seconds to wait while the evaluation is pending, 0 to 600; 0 returns at once. Keep it below this client's tool call timeout"`
}

type mcpEvaluation struct {
	machineEvaluationResult
	// PreviousLessonID is set when the current lesson has no submission yet
	// and the accepted submission of the previous lesson is shown instead.
	PreviousLessonID string `json:"previous_lesson_id,omitempty"`
}

func (s *mcpServer) result(ctx context.Context, request *mcp.CallToolRequest, input mcpResultInput) (*mcp.CallToolResult, mcpEvaluation, error) {
	ctx = s.context(ctx)
	wait, err := s.evaluationWait(ctx, request, input.WaitSeconds)
	if err != nil {
		return nil, mcpEvaluation{}, mcpToolFailure(ctx, err)
	}
	submissionID := strings.TrimSpace(input.SubmissionID)
	previousLesson := ""
	if submissionID != "" && validateSubmissionID(submissionID) != nil {
		return nil, mcpEvaluation{}, mcpToolFailure(ctx,
			usage(ctx, "ID отправки должен быть каноническим UUID", "submission ID must be a canonical UUID"))
	}
	if submissionID == "" {
		latest, err := s.useCases.LatestSubmission(ctx)
		if err != nil {
			return nil, mcpEvaluation{}, mcpToolFailure(ctx, err)
		}
		submissionID, previousLesson = latest.SubmissionID, latest.PredecessorAssignmentID
	}
	evaluation, err := s.useCases.Evaluation(ctx, submissionID, wait, mcpReviewFeedback)
	if err != nil {
		return nil, mcpEvaluation{}, mcpToolFailure(ctx, err)
	}
	return nil, mcpEvaluation{
		machineEvaluationResult: buildMachineEvaluationResult(evaluation, webResultURL(ctx, submissionID)),
		PreviousLessonID:        previousLesson,
	}, nil
}

func (s *mcpServer) submissions(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, machineSubmissions, error) {
	ctx = s.context(ctx)
	snapshot, err := s.useCases.Submissions(ctx)
	if err != nil {
		return nil, machineSubmissions{}, mcpToolFailure(ctx, err)
	}
	return nil, machineSubmissions{
		Kind: "softpractice.submissions", AssignmentID: snapshot.Lesson.AssignmentID,
		Version: snapshot.Lesson.Version, History: snapshot.History,
	}, nil
}

type mcpSubmission struct {
	machineSubmissionReceipt
	// LocalChecks is always not_run: submit does not run them, check does.
	LocalChecks string `json:"local_checks"`
	// Evaluation is present when wait_seconds was set.
	Evaluation *machineEvaluationResult `json:"evaluation,omitempty"`
	// EvaluationError says why waiting failed after the submission was sent;
	// the result tool reads the evaluation later.
	EvaluationError string `json:"evaluation_error,omitempty"`
}

func (s *mcpServer) submit(ctx context.Context, request *mcp.CallToolRequest, input mcpWaitInput) (*mcp.CallToolResult, mcpSubmission, error) {
	ctx = s.context(ctx)
	wait, err := s.evaluationWait(ctx, request, input.WaitSeconds)
	if err != nil {
		return nil, mcpSubmission{}, mcpToolFailure(ctx, err)
	}
	prepared, err := s.useCases.PrepareSubmission(ctx)
	if err != nil {
		return nil, mcpSubmission{}, mcpToolFailure(ctx, err)
	}
	defer prepared.Close()
	link, workspace, repository := prepared.Link, prepared.Workspace, prepared.Repository
	version := submittedLessonVersion(link, workspace)
	fingerprint := strings.Join([]string{link.WorkspaceID, workspace.Assignment.ID, fmt.Sprint(version), repository.CommitSHA}, "\n")
	var message strings.Builder
	fmt.Fprintf(&message, text(ctx,
		"Отправить решение на проверку?\n\nУрок: %s v%d — %s\nCommit: %s\nФайлов: %d, сжатый размер: %s\n",
		"Submit this solution for evaluation?\n\nLesson: %s v%d — %s\nCommit: %s\nFiles: %d, compressed size: %s\n"),
		workspace.Assignment.ID, version, workspace.Assignment.Title, repository.CommitSHA,
		len(repository.Files), formatBytes(repository.CompressedBytes))
	for _, path := range suspiciousPaths(repository.Files) {
		fmt.Fprintf(&message, text(ctx, "Внимание: commit содержит потенциально чувствительный файл: %s\n",
			"Warning: the commit contains a potentially sensitive file: %s\n"), path)
	}
	message.WriteString(text(ctx, "\nОтправленную попытку нельзя отозвать.", "\nA submission cannot be withdrawn."))
	confirmation, err := s.confirm(ctx, request, "submit", fingerprint, message.String())
	if err != nil || confirmation != nil {
		return confirmation, mcpSubmission{}, err
	}
	receipt, err := s.useCases.SendSubmission(ctx, prepared)
	if err != nil {
		return nil, mcpSubmission{}, mcpToolFailure(ctx, err)
	}
	payload := mcpSubmission{
		machineSubmissionReceipt: machineSubmissionReceipt{
			Kind: "softpractice.submission", SubmissionID: receipt.SubmissionID, RevisionID: receipt.RevisionID,
			EvaluationJobID: receipt.EvaluationJobID, JobState: receipt.JobState, Replayed: receipt.Replayed,
			SubmittedAt: receipt.SubmittedAt, ResultURL: webResultURL(ctx, receipt.SubmissionID),
			LessonUpdate: receipt.LessonUpdate,
		},
		LocalChecks: "not_run",
	}
	if wait.Wait {
		evaluation, err := s.useCases.Evaluation(ctx, receipt.SubmissionID, wait, mcpReviewFeedback)
		if err != nil {
			// The submission was sent: report it, and let result fetch the
			// evaluation later.
			payload.EvaluationError = err.Error()
			return nil, payload, nil
		}
		built := buildMachineEvaluationResult(evaluation, payload.ResultURL)
		payload.Evaluation = &built
	}
	return nil, payload, nil
}

type mcpUpdate struct {
	Kind                  string            `json:"kind"`
	UpdateKind            updateKind        `json:"update_kind"`
	FromAssignmentID      string            `json:"from_assignment_id"`
	FromAssignmentVersion int               `json:"from_assignment_version"`
	ToAssignmentID        string            `json:"to_assignment_id"`
	ToAssignmentVersion   int               `json:"to_assignment_version"`
	Operations            []updateOperation `json:"operations"`
	// Report is what `softpractice update` prints for the learner.
	Report string `json:"report"`
}

func (s *mcpServer) update(ctx context.Context, request *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpUpdate, error) {
	ctx = s.context(ctx)
	plan, err := s.useCases.PrepareUpdate(ctx)
	if err != nil {
		return nil, mcpUpdate{}, mcpToolFailure(ctx, err)
	}
	var fingerprint, message strings.Builder
	fmt.Fprintf(&fingerprint, "%s\n%s\n%s\n%d\n%s\n%d\n%s", plan.Kind, plan.HeadCommit, plan.FromAssignmentID,
		plan.FromAssignmentVersion, plan.ToAssignmentID, plan.ToAssignmentVersion, plan.Ref)
	switch plan.Kind {
	case updateLessonTransition:
		fmt.Fprintf(&message, text(ctx, "Перейти к следующему уроку в этой папке?\n\n%s v%d → %s v%d\n",
			"Move this folder to the next lesson?\n\n%s v%d → %s v%d\n"),
			plan.FromAssignmentID, plan.FromAssignmentVersion, plan.ToAssignmentID, plan.ToAssignmentVersion)
	case updateLessonVersion:
		fmt.Fprintf(&message, text(ctx, "Перейти на новую версию урока?\n\n%s v%d → v%d\n",
			"Move to the new version of the lesson?\n\n%s v%d → v%d\n"),
			plan.FromAssignmentID, plan.FromAssignmentVersion, plan.ToAssignmentVersion)
	case updateLocalChecks:
		message.WriteString(text(ctx, "Добавить в проект конфигурацию публичных проверок?\n",
			"Add the public checks configuration to the project?\n"))
	}
	if len(plan.Operations) == 0 {
		message.WriteString(text(ctx, "\nФайлы проекта не меняются.\n", "\nNo project file changes.\n"))
	} else {
		message.WriteString(text(ctx, "\nФайлы:\n", "\nFiles:\n"))
	}
	for _, operation := range plan.Operations {
		fmt.Fprintf(&fingerprint, "\n%s %s", operation.Kind, operation.Path)
		fmt.Fprintf(&message, "  %s %s\n", mcpOperationLabel(ctx, operation.Kind), operation.Path)
	}
	message.WriteString(text(ctx, "\nИзменения будут закоммичены в Git; прежнее состояние останется в истории.",
		"\nThe changes are committed to Git; the previous state stays in history."))
	confirmation, err := s.confirm(ctx, request, "update", fingerprint.String(), message.String())
	if err != nil || confirmation != nil {
		return confirmation, mcpUpdate{}, err
	}
	var report bytes.Buffer
	if err := s.useCases.ApplyUpdate(ctx, plan, &report); err != nil {
		return nil, mcpUpdate{}, mcpToolFailure(ctx, err)
	}
	return nil, mcpUpdate{
		Kind: "softpractice.update", UpdateKind: plan.Kind,
		FromAssignmentID: plan.FromAssignmentID, FromAssignmentVersion: plan.FromAssignmentVersion,
		ToAssignmentID: plan.ToAssignmentID, ToAssignmentVersion: plan.ToAssignmentVersion,
		Operations: plan.Operations, Report: strings.TrimSpace(report.String()),
	}, nil
}

func mcpOperationLabel(ctx context.Context, kind string) string {
	switch kind {
	case "add":
		return text(ctx, "добавить", "add")
	case "replace":
		return text(ctx, "заменить", "replace")
	case "delete":
		return text(ctx, "удалить", "delete")
	default:
		return kind
	}
}

// mcpConfirmationState is what a confirmation request carries to the retry
// that answers it: which tool asked, and what exactly the learner was shown.
type mcpConfirmationState struct {
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint"`
}

const mcpConfirmationID = "confirm"

// confirm asks the learner to confirm an action through elicitation, as a
// multi round-trip tool result: the client asks the learner and calls the
// tool again with the answer. It returns the input request to send on the
// first call; nil and nil once the learner accepted exactly this action on
// the retry; and a tool error when the client cannot ask, the learner did not
// accept, or the action changed since the learner saw it.
func (s *mcpServer) confirm(
	ctx context.Context,
	request *mcp.CallToolRequest,
	tool, fingerprint, message string,
) (*mcp.CallToolResult, error) {
	answer, answered := request.Params.InputResponses[mcpConfirmationID]
	if !answered {
		if capabilities := request.ClientCapabilities(); capabilities == nil || capabilities.Elicitation == nil ||
			(capabilities.Elicitation.Form == nil && capabilities.Elicitation.URL != nil) {
			return nil, mcpFailure("confirmation_unavailable", fmt.Sprintf(text(ctx,
				"этот клиент не умеет запрашивать подтверждение у человека; ничего не сделано. Выполните в терминале: `softpractice %s`",
				"this client cannot ask a person to confirm; nothing was done. Run in a terminal: `softpractice %s`"), tool))
		}
		state, err := s.sealState(mcpConfirmationState{Tool: tool, Fingerprint: fingerprint})
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{mcpConfirmationID: &mcp.ElicitParams{
				Mode: "form", Message: message,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			RequestState: state,
		}, nil
	}
	state, err := s.openState(request.Params.RequestState)
	if err != nil || state.Tool != tool {
		return nil, mcpFailure("confirmation_invalid", text(ctx,
			"подтверждение не относится к этому действию; ничего не сделано, вызовите инструмент заново",
			"the confirmation does not belong to this action; nothing was done, call the tool again"))
	}
	result, ok := answer.(*mcp.ElicitResult)
	if !ok || result.Action != "accept" {
		return nil, mcpFailure("declined", text(ctx,
			"человек не подтвердил действие; ничего не сделано",
			"the person did not confirm; nothing was done"))
	}
	if !hmac.Equal([]byte(state.Fingerprint), []byte(fingerprint)) {
		return nil, mcpFailure("changed_since_confirmation", text(ctx,
			"проект или урок изменились после подтверждения; ничего не сделано, вызовите инструмент заново",
			"the project or lesson changed after the confirmation; nothing was done, call the tool again"))
	}
	return nil, nil
}

// sealState signs the confirmation state with a key that lives only in this
// server process, so a retry cannot carry a state the server did not issue.
func (s *mcpServer) sealState(state mcpConfirmationState) (string, error) {
	body, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *mcpServer) openState(sealed string) (mcpConfirmationState, error) {
	encodedBody, encodedMAC, found := strings.Cut(sealed, ".")
	body, bodyErr := base64.RawURLEncoding.DecodeString(encodedBody)
	signature, macErr := base64.RawURLEncoding.DecodeString(encodedMAC)
	if !found || bodyErr != nil || macErr != nil {
		return mcpConfirmationState{}, errors.New("malformed confirmation state")
	}
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return mcpConfirmationState{}, errors.New("confirmation state was not issued by this server")
	}
	var state mcpConfirmationState
	if err := json.Unmarshal(body, &state); err != nil {
		return mcpConfirmationState{}, err
	}
	return state, nil
}

// mcpToolError is a tool error the agent can act on: a stable code, a
// message for the person, and what the person has to do when it is theirs.
type mcpToolError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Action     string `json:"action,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	APICode    string `json:"api_code,omitempty"`
	SupportID  string `json:"support_id,omitempty"`
}

// Error is the JSON the tool result carries as its text.
func (e *mcpToolError) Error() string {
	body, err := json.Marshal(map[string]*mcpToolError{"error": e})
	if err != nil {
		return e.Message
	}
	return string(body)
}

func mcpFailure(code, message string) error {
	return &mcpToolError{Code: code, Message: message}
}

// mcpToolFailure classifies err with the codes of `--json` errors and adds
// what the person has to do for those that are theirs.
func mcpToolFailure(ctx context.Context, err error) error {
	failure := &mcpToolError{Code: errorCode(err), Message: err.Error()}
	var statusError *learnercli.HTTPError
	if errors.As(err, &statusError) {
		failure.HTTPStatus, failure.APICode, failure.SupportID = statusError.Status, statusError.Code, statusError.SupportID
	}
	switch failure.Code {
	case "login_required":
		failure.Action = text(ctx,
			"Попросите человека войти в терминале: `softpractice login`. Этот сервер вход не запускает.",
			"Ask the person to sign in from a terminal: `softpractice login`. This server does not start a sign-in.")
	case "project_not_linked", "not_git_repository":
		failure.Action = text(ctx,
			"Запустите MCP-сервер в папке проекта урока или передайте её: `softpractice mcp --project DIR`.",
			"Start the MCP server in the lesson project folder, or pass it: `softpractice mcp --project DIR`.")
	case "checks_not_published":
		failure.Action = text(ctx,
			"Попросите человека посмотреть команды в .softpractice/checks.json и запустить `softpractice check` в терминале.",
			"Ask the person to review the commands in .softpractice/checks.json and run `softpractice check` in a terminal.")
	}
	return failure
}

func (s *mcpServer) taskResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	ctx = s.context(ctx)
	_, assignment, err := s.useCases.Task(ctx)
	if err != nil {
		return nil, mcpToolFailure(ctx, err)
	}
	body := fmt.Sprintf("# %s\n\n%s\n", assignment.Title, strings.TrimSpace(assignment.InstructionsMarkdown))
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: request.Params.URI, MIMEType: "text/markdown", Text: body,
	}}}, nil
}

func (s *mcpServer) materialResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	ctx = s.context(ctx)
	snapshot, err := s.useCases.Material(ctx, "")
	if err != nil {
		return nil, mcpToolFailure(ctx, err)
	}
	var body strings.Builder
	if material := snapshot.Material; material == nil {
		fmt.Fprintf(&body, text(ctx, "У урока `%s` нет теоретического материала.\n", "Lesson `%s` has no theory material.\n"),
			snapshot.AssignmentID)
	} else {
		fmt.Fprintf(&body, "# %s\n\n%s\n", material.Title, strings.TrimSpace(material.Markdown))
		if diagram := material.Diagram; diagram.Title != "" {
			fmt.Fprintf(&body, "\n## %s\n\n", diagram.Title)
			for _, node := range diagram.Nodes {
				fmt.Fprintf(&body, "- **%s**: %s\n", node.Title, node.Text)
			}
			if diagram.TextAlternative != "" {
				fmt.Fprintf(&body, "\n%s\n", diagram.TextAlternative)
			}
		}
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: request.Params.URI, MIMEType: "text/markdown", Text: body.String(),
	}}}, nil
}
