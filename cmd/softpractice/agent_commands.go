package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

// exitStatus ends the process with its code after the command has already
// written its result, so main prints nothing more.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

const (
	// exitNotReady: the evaluation is still queued or running.
	exitNotReady exitStatus = 3
	// exitSuperseded: the evaluation was superseded and has no result.
	exitSuperseded exitStatus = 4
)

const defaultEvaluationTimeout = 10 * time.Minute

type machineEvaluationResult struct {
	ContractVersion int                `json:"contract_version"`
	Kind            string             `json:"kind"`
	Outcome         evaluationOutcome  `json:"outcome"`
	SubmissionID    string             `json:"submission_id"`
	EvaluationJobID string             `json:"evaluation_job_id,omitempty"`
	JobState        string             `json:"job_state"`
	Terminal        bool               `json:"terminal"`
	Attempt         int                `json:"attempt,omitempty"`
	MaxAttempts     int                `json:"max_attempts,omitempty"`
	NextPollSeconds int                `json:"next_poll_seconds,omitempty"`
	UpdatedAt       *time.Time         `json:"updated_at,omitempty"`
	ResultPath      string             `json:"result_path"`
	ResultURL       string             `json:"result_url"`
	Evaluation      *machineEvaluation `json:"evaluation,omitempty"`
	Feedback        *machineFeedback   `json:"feedback,omitempty"`
}

func buildMachineEvaluationResult(
	result evaluationResult,
	webURL string,
	detail reviewFeedbackDetail,
) (machineEvaluationResult, error) {
	payload := machineEvaluationResult{
		ContractVersion: 1, Kind: "softpractice.evaluation",
		Outcome: result.Outcome, SubmissionID: result.SubmissionID,
		ResultPath: "/submissions/" + result.SubmissionID + "/result",
	}
	payload.ResultURL = strings.TrimRight(webURL, "/") + payload.ResultPath
	if result.Outcome == evaluationSuperseded {
		payload.JobState = "superseded"
		payload.Terminal = true
		return payload, nil
	}
	submission := result.Submission
	updatedAt := submission.UpdatedAt
	payload.EvaluationJobID = submission.EvaluationJobID
	payload.JobState = submission.JobState
	payload.Terminal = submission.Terminal
	payload.Attempt = submission.Attempt
	payload.MaxAttempts = submission.MaxAttempts
	payload.NextPollSeconds = submission.NextPollSeconds
	payload.UpdatedAt = &updatedAt
	payload.Evaluation = submission.Evaluation
	if result.Outcome == evaluationReady && len(result.RawEvaluation) != 0 {
		feedback, err := buildMachineFeedback(result.RawEvaluation, detail)
		if err != nil {
			return machineEvaluationResult{}, err
		}
		payload.Feedback = feedback
	}
	return payload, nil
}

func evaluationCommand(
	ctx context.Context,
	useCases learnerUseCases,
	args []string,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("evaluation", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	id := flags.String("id", "", "submission ID; defaults to the latest linked submission")
	wait := flags.Bool("wait", false, "poll until the evaluation is ready")
	timeout := flags.Duration("timeout", defaultEvaluationTimeout, "maximum wait with --wait")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	usage := text(ctx,
		"использование: softpractice evaluation [--id ID] [--wait] [--timeout DURATION] [--format text|json]",
		"usage: softpractice evaluation [--id ID] [--wait] [--timeout DURATION] [--format text|json]")
	if flags.NArg() != 0 || *timeout < 0 {
		return errors.New(usage)
	}
	outputFormat, err := parseOutputFormat(*format)
	if err != nil {
		return err
	}
	payload, err := evaluationPayload(ctx, useCases, strings.TrimSpace(*id), *wait, *timeout, errorOutput)
	if err != nil {
		if outputFormat == outputFormatJSON {
			_ = writeMachineError(output, err)
		}
		return err
	}
	if outputFormat == outputFormatJSON {
		if err := writeMachineJSON(output, payload); err != nil {
			return err
		}
	} else {
		writeEvaluationText(ctx, output, payload)
	}
	switch payload.Outcome {
	case evaluationPending:
		return exitNotReady
	case evaluationSuperseded:
		return exitSuperseded
	}
	return nil
}

func evaluationPayload(
	ctx context.Context,
	useCases learnerUseCases,
	submissionID string,
	wait bool,
	timeout time.Duration,
	errorOutput io.Writer,
) (machineEvaluationResult, error) {
	if submissionID == "" {
		latest, err := useCases.LatestSubmission(ctx)
		if err != nil {
			return machineEvaluationResult{}, err
		}
		if latest.PredecessorAssignmentID != "" {
			fmt.Fprintf(errorOutput, text(ctx,
				"По %s отправок ещё нет; показан принятый результат %s.\n",
				"%s has no submission yet; showing the accepted %s result.\n",
			), latest.CurrentAssignmentID, latest.PredecessorAssignmentID)
		}
		submissionID = latest.SubmissionID
	}
	result, err := useCases.Evaluation(ctx, submissionID, evaluationWait{Wait: wait, Timeout: timeout})
	if err != nil {
		return machineEvaluationResult{}, err
	}
	return buildMachineEvaluationResult(result, settingsFromContext(ctx).WebURL, machineReviewFeedback)
}

func writeEvaluationText(ctx context.Context, output io.Writer, payload machineEvaluationResult) {
	fmt.Fprintf(output, text(ctx, "Отправка: %s\n", "Submission: %s\n"), payload.SubmissionID)
	switch payload.Outcome {
	case evaluationSuperseded:
		fmt.Fprintln(output, text(ctx,
			"Проверка этой отправки заменена; результата у неё не будет.",
			"This evaluation was superseded; it will not have a result."))
		return
	case evaluationPending:
		fmt.Fprintf(output, text(ctx,
			"Проверка ещё не готова (%s). Повторите: softpractice evaluation --wait\n",
			"The evaluation is not ready yet (%s). Check again: softpractice evaluation --wait\n"),
			payload.JobState)
		return
	}
	if payload.Evaluation != nil {
		fmt.Fprintf(output, text(ctx, "Результат: %s\n", "Result: %s\n"), payload.Evaluation.Status)
		if technical := payload.Evaluation.TechnicalError; technical != nil {
			fmt.Fprintf(output, text(ctx, "Техническая ошибка: %s (%s), support %s\n", "Technical error: %s (%s), support %s\n"),
				technical.ErrorCode, technical.Stage, technical.SupportID)
		}
	}
	if feedback := payload.Feedback; feedback != nil {
		if deterministic := feedback.Deterministic; deterministic != nil {
			fmt.Fprintf(output, text(ctx, "Автоматические проверки: %s\n", "Automated checks: %s\n"), deterministic.Status)
			for _, check := range deterministic.Checks {
				fmt.Fprintf(output, "  %-4s %s: %s\n", check.Status, check.ID, check.Summary)
			}
		}
		if review := feedback.Review; review != nil && review.Status != "skipped" {
			verdict := "-"
			if review.Verdict != nil {
				verdict = *review.Verdict
			}
			fmt.Fprintf(output, text(ctx, "Ревью: %s (%s)\n", "Review: %s (%s)\n"), verdict, review.Mode)
			fmt.Fprintf(output, text(ctx, "Замечаний рецензента: %d%s\n", "Reviewer findings: %d%s\n"),
				review.FindingsCount, formatPriorityCounts(review.FindingsByPriority))
			for _, finding := range review.Findings {
				fmt.Fprintf(output, "  [%s] %s\n", finding.Priority, finding.Title)
			}
		}
	}
	fmt.Fprintf(output, text(ctx, "Полный разбор: %s\n", "Full review: %s\n"), payload.ResultURL)
}

func formatPriorityCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	order := map[string]int{"high": 0, "medium": 1, "low": 2}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftKnown := order[keys[i]]
		right, rightKnown := order[keys[j]]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

type machineAssignment struct {
	ContractVersion     int                         `json:"contract_version"`
	Kind                string                      `json:"kind"`
	WorkspaceID         string                      `json:"workspace_id"`
	ProjectID           string                      `json:"project_id"`
	Assignment          assignmentDetail            `json:"assignment"`
	Material            *theoryMaterial             `json:"material"`
	Transition          *machineTransition          `json:"transition,omitempty"`
	LessonVersionUpdate *machineLessonVersionUpdate `json:"lesson_version_update,omitempty"`
}

func assignmentCommand(
	ctx context.Context,
	useCases learnerUseCases,
	args []string,
	output, errorOutput io.Writer,
) error {
	usage := text(ctx,
		"использование: softpractice assignment show [--format text|json]",
		"usage: softpractice assignment show [--format text|json]")
	if len(args) == 0 || args[0] != "show" {
		return errors.New(usage)
	}
	flags := flag.NewFlagSet("assignment show", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(usage)
	}
	outputFormat, err := parseOutputFormat(*format)
	if err != nil {
		return err
	}
	snapshot, err := useCases.Assignment(ctx)
	if err != nil {
		if outputFormat == outputFormatJSON {
			_ = writeMachineError(output, err)
		}
		return err
	}
	payload := machineAssignment{
		ContractVersion: 1, Kind: "softpractice.assignment",
		WorkspaceID: snapshot.Workspace.Workspace.ID, ProjectID: snapshot.Link.ProjectID,
		Assignment: snapshot.Assignment, Material: snapshot.Material,
	}
	if pendingTransition(snapshot.Link, snapshot.Workspace) {
		payload.Transition = &machineTransition{
			FromAssignmentID: snapshot.Link.AssignmentID, FromAssignmentVersion: snapshot.Link.AssignmentVersion,
			ToAssignmentID: snapshot.Workspace.Assignment.ID, ToAssignmentVersion: snapshot.Workspace.Assignment.Version,
		}
	}
	if pendingLessonVersion(snapshot.Link, snapshot.Workspace) {
		payload.LessonVersionUpdate = &machineLessonVersionUpdate{
			AssignmentID: snapshot.Link.AssignmentID,
			FromVersion:  snapshot.Link.AssignmentVersion, ToVersion: snapshot.Workspace.Assignment.Version,
		}
	}
	if outputFormat == outputFormatJSON {
		return writeMachineJSON(output, payload)
	}
	fmt.Fprintf(output, "# %s (%s v%d)\n\n", payload.Assignment.Title, payload.Assignment.ID, payload.Assignment.Version)
	if payload.Transition != nil {
		fmt.Fprintf(output, text(ctx,
			"Переход не применён: файлы урока %s ещё не в этой папке (выполните `softpractice update`).\n\n",
			"Transition pending: the %s lesson files are not in this folder yet (run `softpractice update`).\n\n"),
			payload.Transition.ToAssignmentID)
	}
	fmt.Fprintln(output, strings.TrimRight(payload.Assignment.InstructionsMarkdown, "\n"))
	if material := payload.Material; material != nil {
		fmt.Fprintf(output, "\n---\n\n%s\n", strings.TrimRight(material.Markdown, "\n"))
	}
	return nil
}

// machineError is written to standard output by the agent-facing commands in
// JSON mode, so a caller that reads only stdout still learns why the command
// failed. The same message also goes to standard error.
type machineError struct {
	ContractVersion int    `json:"contract_version"`
	Kind            string `json:"kind"`
	Error           struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		HTTPStatus int    `json:"http_status,omitempty"`
		APICode    string `json:"api_code,omitempty"`
		SupportID  string `json:"support_id,omitempty"`
	} `json:"error"`
}

// errorCode classifies an error for machine output. login_required and
// project_not_linked tell the caller that a person has to act: sign in with
// `softpractice login`, or work inside the linked project.
func errorCode(err error) string {
	var statusError *learnercli.HTTPError
	switch {
	case errors.Is(err, learnercli.ErrLoginRequired):
		return "login_required"
	case errors.As(err, &statusError) && statusError.Status == http.StatusUnauthorized:
		return "login_required"
	case errors.Is(err, learnercli.ErrProjectNotLinked):
		return "project_not_linked"
	case errors.Is(err, errNotGitRepository):
		return "not_git_repository"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	case errors.As(err, &statusError):
		return "api_error"
	default:
		return "error"
	}
}

func writeMachineError(output io.Writer, err error) error {
	payload := machineError{ContractVersion: 1, Kind: "softpractice.error"}
	payload.Error.Code = errorCode(err)
	payload.Error.Message = err.Error()
	var statusError *learnercli.HTTPError
	if errors.As(err, &statusError) {
		payload.Error.HTTPStatus = statusError.Status
		payload.Error.APICode = statusError.Code
		payload.Error.SupportID = statusError.SupportID
	}
	return writeMachineJSON(output, payload)
}
