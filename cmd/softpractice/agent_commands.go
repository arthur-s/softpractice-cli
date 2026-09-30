package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
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
	Kind            string            `json:"kind"`
	Outcome         evaluationOutcome `json:"outcome"`
	SubmissionID    string            `json:"submission_id"`
	EvaluationJobID string            `json:"evaluation_job_id,omitempty"`
	JobState        string            `json:"job_state"`
	Attempt         int               `json:"attempt,omitempty"`
	MaxAttempts     int               `json:"max_attempts,omitempty"`
	NextPollSeconds int               `json:"next_poll_seconds,omitempty"`
	UpdatedAt       *time.Time        `json:"updated_at,omitempty"`
	ResultURL       string            `json:"result_url"`
	// Result is present only for the ready outcome.
	Result *machineResult `json:"result,omitempty"`
}

func buildMachineEvaluationResult(
	result evaluationResult,
	webURL string,
	detail reviewFeedbackDetail,
) (machineEvaluationResult, error) {
	payload := machineEvaluationResult{
		Kind: "softpractice.evaluation", Outcome: result.Outcome, SubmissionID: result.SubmissionID,
		ResultURL: strings.TrimRight(webURL, "/") + "/submissions/" + result.SubmissionID + "/result",
	}
	if result.Outcome == evaluationSuperseded {
		payload.JobState = "superseded"
		return payload, nil
	}
	submission := result.Submission
	updatedAt := submission.UpdatedAt
	payload.EvaluationJobID = submission.EvaluationJobID
	payload.JobState = submission.JobState
	payload.Attempt = submission.Attempt
	payload.MaxAttempts = submission.MaxAttempts
	payload.NextPollSeconds = submission.NextPollSeconds
	payload.UpdatedAt = &updatedAt
	if result.Outcome == evaluationReady {
		built, err := buildMachineResult(result.RawEvaluation, detail)
		if err != nil {
			return machineEvaluationResult{}, err
		}
		payload.Result = built
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
	directions := flags.Bool("directions", false, "also print the reviewer's directions and next steps")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	usage := text(ctx,
		"использование: softpractice evaluation [--id ID] [--wait] [--timeout DURATION] [--directions] [--format text|json]",
		"usage: softpractice evaluation [--id ID] [--wait] [--timeout DURATION] [--directions] [--format text|json]")
	if flags.NArg() != 0 || *timeout < 0 {
		return errors.New(usage)
	}
	outputFormat, err := parseOutputFormat(*format)
	if err != nil {
		return err
	}
	detail := defaultReviewFeedback
	if *directions {
		detail = feedbackWithDirections
	}
	payload, err := evaluationPayload(ctx, useCases, strings.TrimSpace(*id), *wait, *timeout, detail, errorOutput)
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
		writeEvaluationMarkdown(ctx, output, payload)
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
	detail reviewFeedbackDetail,
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
	return buildMachineEvaluationResult(result, settingsFromContext(ctx).WebURL, detail)
}

// writeEvaluationMarkdown prints the evaluation as Markdown, which reads as
// plain text in a terminal and renders in an agent's chat.
func writeEvaluationMarkdown(ctx context.Context, output io.Writer, payload machineEvaluationResult) {
	switch payload.Outcome {
	case evaluationSuperseded:
		fmt.Fprintf(output, text(ctx,
			"Проверка отправки `%s` заменена; результата у неё не будет.\n",
			"The evaluation of submission `%s` was superseded; it will not have a result.\n"),
			payload.SubmissionID)
		return
	case evaluationPending:
		fmt.Fprintf(output, text(ctx,
			"Проверка отправки `%s` ещё не готова (%s). Дождаться: `softpractice evaluation --wait`\n",
			"The evaluation of submission `%s` is not ready yet (%s). To wait: `softpractice evaluation --wait`\n"),
			payload.SubmissionID, payload.JobState)
		return
	}
	result := payload.Result
	fmt.Fprintf(output, "# %s\n\n", evaluationStatusTitle(ctx, result.Status))
	fmt.Fprintf(output, text(ctx, "Отправка `%s`, попытка проверки %d из %d.\n", "Submission `%s`, evaluation attempt %d of %d.\n"),
		payload.SubmissionID, payload.Attempt, payload.MaxAttempts)
	if technical := result.TechnicalError; technical != nil {
		fmt.Fprintf(output, text(ctx, "\nТехническая ошибка: `%s` на этапе `%s`, support ID `%s`.\n",
			"\nTechnical error: `%s` at stage `%s`, support ID `%s`.\n"),
			technical.ErrorCode, technical.Stage, technical.SupportID)
	}
	if deterministic := result.Deterministic; deterministic != nil {
		fmt.Fprintf(output, text(ctx, "\n## Автоматические проверки: %s\n\n", "\n## Automated checks: %s\n\n"),
			deterministicStatusLabel(ctx, deterministic.Status))
		for _, check := range deterministic.Checks {
			fmt.Fprintf(output, "- [%s] %s: %s\n", check.Status, check.ID, check.Summary)
			if example := check.Counterexample; example != nil {
				fmt.Fprintf(output, text(ctx, "  - Пример: %s\n", "  - Example: %s\n"), example.Title)
				fmt.Fprintf(output, text(ctx, "  - Сценарий: %s\n", "  - Scenario: %s\n"), example.Scenario)
				fmt.Fprintf(output, text(ctx, "  - Вход: %s\n", "  - Input: %s\n"), example.Input)
				fmt.Fprintf(output, text(ctx, "  - Ожидалось: %s\n", "  - Expected: %s\n"), example.Expected)
				if example.NextStep != "" {
					fmt.Fprintf(output, text(ctx, "  - Следующий шаг: %s\n", "  - Next step: %s\n"), example.NextStep)
				}
			}
		}
	}
	if review := result.Review; review != nil && review.Status != "skipped" {
		fmt.Fprint(output, text(ctx, "\n## Ревью\n", "\n## Review\n"))
		if !review.Authoritative {
			fmt.Fprint(output, text(ctx,
				"\nПредварительное ревью: оно не определяет результат.\n",
				"\nPreliminary review: it does not decide the result.\n"))
		}
		if review.Summary != "" {
			fmt.Fprintf(output, "\n%s\n", review.Summary)
		}
		if uncertainty := review.Uncertainty; uncertainty != nil && uncertainty.Reason != nil && *uncertainty.Reason != "" {
			fmt.Fprintf(output, text(ctx, "\nЧто осталось неясным: %s\n", "\nWhat remains unclear: %s\n"), *uncertainty.Reason)
		}
		if len(review.Rubric) > 0 {
			fmt.Fprint(output, text(ctx, "\n### Критерии\n\n", "\n### Criteria\n\n"))
			for _, item := range review.Rubric {
				fmt.Fprintf(output, "- **%s**: %s. %s%s\n", item.Criterion, rubricLevelLabel(ctx, item.Level),
					item.Rationale, formatEvidence(ctx, item.EvidenceRefs))
			}
		}
		if len(review.Findings) > 0 {
			fmt.Fprint(output, text(ctx, "\n### Замечания\n", "\n### Findings\n"))
			for index, finding := range review.Findings {
				fmt.Fprintf(output, "\n%d. **%s** (%s)\n", index+1, finding.Title, priorityLabel(ctx, finding.Priority))
				fmt.Fprintf(output, text(ctx, "   - Наблюдение: %s\n", "   - Observation: %s\n"), finding.Observation)
				fmt.Fprintf(output, text(ctx, "   - Риск: %s\n", "   - Risk: %s\n"), finding.Risk)
				if finding.Direction != "" {
					fmt.Fprintf(output, text(ctx, "   - Направление: %s\n", "   - Direction: %s\n"), finding.Direction)
				}
				if len(finding.EvidenceRefs) > 0 {
					fmt.Fprintf(output, text(ctx, "   - Где: %s\n", "   - Where: %s\n"), strings.Join(finding.EvidenceRefs, ", "))
				}
			}
		}
		if review.QuestionsCount > 0 {
			fmt.Fprintf(output, text(ctx,
				"\nВопросов рецензента к решению: %d. Они на странице результата.\n",
				"\nReviewer questions about the solution: %d. They are on the result page.\n"),
				review.QuestionsCount)
		}
	}
	if result.DirectionsHidden {
		fmt.Fprint(output, text(ctx,
			"\nРекомендации рецензента не показаны. Показать: `softpractice evaluation --directions`\n",
			"\nThe reviewer's recommendations are not shown. To show them: `softpractice evaluation --directions`\n"))
	}
	fmt.Fprintf(output, text(ctx, "\nПолный разбор: %s\n", "\nFull review: %s\n"), payload.ResultURL)
}

func evaluationStatusTitle(ctx context.Context, status string) string {
	switch status {
	case "accepted":
		return text(ctx, "Решение принято", "Solution accepted")
	case "revise":
		return text(ctx, "Нужна доработка", "Revision needed")
	case "uncertain":
		return text(ctx, "Рецензенту нужно пояснение", "The reviewer needs an explanation")
	case "deterministic_failed":
		return text(ctx, "Автоматические проверки не пройдены", "Automated checks failed")
	case "ready_for_review":
		return text(ctx, "Автоматические проверки пройдены", "Automated checks passed")
	case "technical_failure":
		return text(ctx, "Проверка не завершилась из-за технической ошибки", "The evaluation failed for a technical reason")
	default:
		return status
	}
}

func deterministicStatusLabel(ctx context.Context, status string) string {
	switch status {
	case "passed":
		return text(ctx, "пройдены", "passed")
	case "failed":
		return text(ctx, "не пройдены", "failed")
	case "not_run":
		return text(ctx, "не запускались", "not run")
	case "technical_failure":
		return text(ctx, "техническая ошибка", "technical failure")
	default:
		return status
	}
}

// rubricLevelLabel uses the level names of the result page.
func rubricLevelLabel(ctx context.Context, level int) string {
	switch level {
	case 1:
		return text(ctx, "нужно доработать", "needs work")
	case 2:
		return text(ctx, "выполнено", "met")
	case 3:
		return text(ctx, "выполнено и закреплено", "met and reinforced")
	default:
		return text(ctx, "не подтверждено", "not confirmed")
	}
}

func priorityLabel(ctx context.Context, priority string) string {
	switch priority {
	case "high":
		return text(ctx, "высокий приоритет", "high priority")
	case "medium":
		return text(ctx, "средний приоритет", "medium priority")
	case "low":
		return text(ctx, "низкий приоритет", "low priority")
	default:
		return priority
	}
}

func formatEvidence(ctx context.Context, refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return text(ctx, " Где: ", " Where: ") + strings.Join(refs, ", ")
}

type machineAssignment struct {
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
		Kind:        "softpractice.assignment",
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
	Kind  string `json:"kind"`
	Error struct {
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
	payload := machineError{Kind: "softpractice.error"}
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
