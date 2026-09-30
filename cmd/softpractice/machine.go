package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

type machineStatus struct {
	Kind    string `json:"kind"`
	Account struct {
		ID            string `json:"id"`
		EmailVerified bool   `json:"email_verified"`
	} `json:"account"`
	Workspace struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
		State     string `json:"state"`
	} `json:"workspace"`
	Assignment struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
		Title   string `json:"title"`
		State   string `json:"state"`
	} `json:"assignment"`
	Local struct {
		Head  string `json:"head"`
		Clean bool   `json:"clean"`
	} `json:"local"`
	// Transition is present only while the server has already opened the next
	// lesson and this folder still holds the previous one.
	Transition *machineTransition `json:"transition,omitempty"`
	// LessonVersionUpdate is present while the server publishes a newer
	// version of the lesson this folder holds.
	LessonVersionUpdate *machineLessonVersionUpdate `json:"lesson_version_update,omitempty"`
	LatestSubmission    *machineSubmissionSummary   `json:"latest_submission"`
	// NextActions lists what to do next, most important first.
	NextActions []nextAction `json:"next_actions"`
}

// nextAction is one step the learner can take now. Command is a CLI command;
// URL is a page for an action that happens on the site.
type nextAction struct {
	Code    string `json:"code"`
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
}

// nextActions derives the learner's next steps from the status, most
// important first. The list is never empty.
func nextActions(ctx context.Context, snapshot statusSnapshot) []nextAction {
	link, workspace, repository := snapshot.Link, snapshot.Workspace, snapshot.Repository
	var actions []nextAction
	submit := nextAction{Code: "submit", Command: "softpractice submit --wait"}
	if !repository.Clean {
		submit = nextAction{Code: "commit_changes"}
	}
	latest := workspace.LatestSubmission
	switch {
	case pendingTransition(link, workspace):
		actions = append(actions, nextAction{Code: "apply_update", Command: "softpractice update"})
	case latest == nil:
		actions = append(actions, submit)
	case latest.JobState == "queued" || latest.JobState == "leased":
		actions = append(actions, nextAction{Code: "wait_result", Command: "softpractice result --wait"})
	default:
		result := snapshot.Latest
		readResult := nextAction{Code: "read_result", Command: "softpractice result"}
		switch {
		case result != nil && result.QuestionsAnswerable:
			actions = append(actions,
				nextAction{Code: "answer_questions", URL: webResultURL(ctx, latest.ID)}, readResult)
		case !repository.Clean || (result != nil && result.CommitSHA != "" && result.CommitSHA != repository.CommitSHA):
			// Work continued after this submission.
			actions = append(actions, submit)
		default:
			actions = append(actions, readResult)
		}
	}
	if pendingLessonVersion(link, workspace) {
		actions = append(actions, nextAction{Code: "update_lesson_version", Command: "softpractice update"})
	}
	return actions
}

func nextActionText(ctx context.Context, action nextAction) string {
	switch action.Code {
	case "apply_update":
		return text(ctx, "примените переход к следующему уроку: `softpractice update`",
			"apply the transition to the next lesson: `softpractice update`")
	case "commit_changes":
		return text(ctx, "сделайте commit изменений и отправьте решение: `softpractice submit --wait`",
			"commit your changes and submit: `softpractice submit --wait`")
	case "submit":
		return text(ctx, "когда решение готово и закоммичено, отправьте его: `softpractice submit --wait`",
			"when the solution is committed, submit it: `softpractice submit --wait`")
	case "wait_result":
		return text(ctx, "дождитесь результата проверки: `softpractice result --wait`",
			"wait for the evaluation: `softpractice result --wait`")
	case "read_result":
		return text(ctx, "прочитайте результат проверки: `softpractice result`",
			"read the evaluation result: `softpractice result`")
	case "answer_questions":
		return text(ctx, "ответьте на вопросы рецензента на странице результата: ",
			"answer the reviewer's questions on the result page: ") + action.URL
	case "update_lesson_version":
		return text(ctx, "можно перейти на новую версию урока: `softpractice update`",
			"you can move to the new lesson version: `softpractice update`")
	default:
		return action.Code
	}
}

type machineLessonVersionUpdate struct {
	AssignmentID string `json:"assignment_id"`
	FromVersion  int    `json:"from_version"`
	ToVersion    int    `json:"to_version"`
}

type machineTransition struct {
	FromAssignmentID      string `json:"from_assignment_id"`
	FromAssignmentVersion int    `json:"from_assignment_version"`
	ToAssignmentID        string `json:"to_assignment_id"`
	ToAssignmentVersion   int    `json:"to_assignment_version"`
}

type machineSubmissionSummary struct {
	ID          string    `json:"id"`
	RevisionID  string    `json:"revision_id"`
	JobState    string    `json:"job_state"`
	SubmittedAt time.Time `json:"submitted_at"`
	// EvaluationStatus is the evaluation result status (accepted, revise,
	// ...) once the job is terminal; omitted while it is not.
	EvaluationStatus string `json:"evaluation_status,omitempty"`
}

func writeStatusJSON(ctx context.Context, output io.Writer, snapshot statusSnapshot) error {
	user, link, workspace, repository := snapshot.User, snapshot.Link, snapshot.Workspace, snapshot.Repository
	payload := machineStatus{Kind: "softpractice.status"}
	payload.Account.ID = user.ID
	payload.Account.EmailVerified = user.EmailVerified
	payload.Workspace.ID = workspace.Workspace.ID
	payload.Workspace.ProjectID = link.ProjectID
	payload.Workspace.State = workspace.Workspace.State
	payload.Assignment.ID = workspace.Assignment.ID
	payload.Assignment.Version = workspace.Assignment.Version
	payload.Assignment.Title = workspace.Assignment.Title
	payload.Assignment.State = workspace.Assignment.State
	payload.Local.Head = repository.CommitSHA
	payload.Local.Clean = repository.Clean
	if pendingTransition(link, workspace) {
		payload.Transition = &machineTransition{
			FromAssignmentID: link.AssignmentID, FromAssignmentVersion: link.AssignmentVersion,
			ToAssignmentID: workspace.Assignment.ID, ToAssignmentVersion: workspace.Assignment.Version,
		}
	}
	if pendingLessonVersion(link, workspace) {
		payload.LessonVersionUpdate = &machineLessonVersionUpdate{
			AssignmentID: link.AssignmentID,
			FromVersion:  link.AssignmentVersion, ToVersion: workspace.Assignment.Version,
		}
	}
	if workspace.LatestSubmission != nil {
		payload.LatestSubmission = &machineSubmissionSummary{
			ID: workspace.LatestSubmission.ID, RevisionID: workspace.LatestSubmission.RevisionID,
			JobState:    workspace.LatestSubmission.JobState,
			SubmittedAt: workspace.LatestSubmission.SubmittedAt,
		}
		if snapshot.Latest != nil {
			payload.LatestSubmission.EvaluationStatus = snapshot.Latest.Status
		}
	}
	payload.NextActions = nextActions(ctx, snapshot)
	return writeMachineJSON(output, payload)
}

type evaluationAPIResponse struct {
	SubmissionID    string          `json:"submission_id"`
	EvaluationJobID string          `json:"evaluation_job_id"`
	JobState        string          `json:"job_state"`
	Attempt         int             `json:"attempt"`
	MaxAttempts     int             `json:"max_attempts"`
	NextPollSeconds int             `json:"next_poll_seconds,omitempty"`
	Evaluation      json.RawMessage `json:"evaluation,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type evaluationProjection struct {
	SchemaVersion int    `json:"schema_version"`
	ExerciseID    string `json:"exercise_id"`
	Status        string `json:"status"`
	Deterministic *struct {
		Status string `json:"status"`
	} `json:"deterministic"`
	Review *struct {
		Status        string  `json:"status"`
		Mode          string  `json:"mode"`
		Authoritative bool    `json:"authoritative"`
		Verdict       *string `json:"verdict"`
	} `json:"review"`
	TechnicalError *struct {
		ErrorCode string `json:"error_code"`
		Stage     string `json:"stage"`
		SupportID string `json:"support_id"`
	} `json:"technical_error"`
}

type machineEvaluation struct {
	SchemaVersion       int                    `json:"schema_version"`
	ExerciseID          string                 `json:"exercise_id"`
	Status              string                 `json:"status"`
	DeterministicStatus string                 `json:"deterministic_status,omitempty"`
	Review              *machineReview         `json:"review,omitempty"`
	TechnicalError      *machineTechnicalError `json:"technical_error,omitempty"`
}

type machineReview struct {
	Status        string  `json:"status"`
	Mode          string  `json:"mode"`
	Authoritative bool    `json:"authoritative"`
	Verdict       *string `json:"verdict"`
}

type machineTechnicalError struct {
	ErrorCode string `json:"error_code"`
	Stage     string `json:"stage"`
	SupportID string `json:"support_id"`
}

type machineSubmission struct {
	SubmissionID    string             `json:"submission_id"`
	EvaluationJobID string             `json:"evaluation_job_id"`
	JobState        string             `json:"job_state"`
	Terminal        bool               `json:"terminal"`
	Attempt         int                `json:"attempt"`
	MaxAttempts     int                `json:"max_attempts"`
	NextPollSeconds int                `json:"next_poll_seconds,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at"`
	Evaluation      *machineEvaluation `json:"evaluation,omitempty"`
}

func downloadSubmissionRevision(
	ctx context.Context,
	client *learnercli.Client,
	args []string,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("submissions download", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	id := flags.String("id", "", "accepted submission ID from the result page")
	destination := flags.String("output", "", "destination ZIP path")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*id) == "" {
		return usage(ctx,
			"использование: softpractice submissions download --id ID [--output PATH]",
			"usage: softpractice submissions download --id ID [--output PATH]")
	}
	submissionID := strings.TrimSpace(*id)
	parsedID, err := uuid.Parse(submissionID)
	if err != nil || parsedID.String() != submissionID {
		return errors.New(text(ctx, "ID отправки должен быть каноническим UUID", "submission ID must be a canonical UUID"))
	}
	target := strings.TrimSpace(*destination)
	if target == "" {
		target = "softpractice-" + submissionID + ".zip"
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	archive, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf(text(ctx,
				"файл %s уже существует; выберите другой путь", "destination %s already exists; choose another path"), target)
		}
		return fmt.Errorf(text(ctx, "создать %s: %w", "create %s: %w"), target, err)
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.Remove(target)
		}
	}()
	result, downloadErr := client.DownloadRevisionArchive(
		ctx,
		"/v1/submissions/"+submissionID+"/revision/archive",
		archive,
	)
	closeErr := archive.Close()
	if downloadErr != nil || closeErr != nil {
		return fmt.Errorf(text(ctx, "скачать проект: %w", "download project: %w"), errors.Join(downloadErr, closeErr))
	}
	completed = true
	fmt.Fprintf(output, text(ctx, "Проект сохранён: %s (%s)\n", "Project saved: %s (%s)\n"), target, formatBytes(result.Size))
	return nil
}

func normalizeSubmissionResponse(response evaluationAPIResponse) (machineSubmission, error) {
	submissionID, submissionErr := uuid.Parse(response.SubmissionID)
	evaluationJobID, evaluationJobErr := uuid.Parse(response.EvaluationJobID)
	if submissionErr != nil || submissionID.String() != response.SubmissionID ||
		evaluationJobErr != nil || evaluationJobID.String() != response.EvaluationJobID ||
		response.UpdatedAt.IsZero() ||
		response.Attempt < 0 || response.MaxAttempts < 1 {
		return machineSubmission{}, errors.New("evaluation response is incomplete")
	}
	payload := machineSubmission{
		SubmissionID: response.SubmissionID, EvaluationJobID: response.EvaluationJobID,
		JobState: response.JobState, Attempt: response.Attempt, MaxAttempts: response.MaxAttempts,
		NextPollSeconds: response.NextPollSeconds, UpdatedAt: response.UpdatedAt,
	}
	switch response.JobState {
	case "queued", "leased":
		if response.NextPollSeconds < 1 || len(response.Evaluation) != 0 {
			return machineSubmission{}, errors.New("pending evaluation response is inconsistent")
		}
		return payload, nil
	case "completed", "failed":
		if response.Attempt < 1 || response.NextPollSeconds != 0 {
			return machineSubmission{}, errors.New("terminal evaluation response is inconsistent")
		}
		payload.Terminal = true
	default:
		return machineSubmission{}, errors.New("evaluation response has an unsupported job state")
	}
	if len(response.Evaluation) == 0 {
		return machineSubmission{}, errors.New("terminal evaluation response has no evaluation")
	}
	var evaluation evaluationProjection
	if err := json.Unmarshal(response.Evaluation, &evaluation); err != nil {
		return machineSubmission{}, fmt.Errorf("decode evaluation projection: %w", err)
	}
	if evaluation.SchemaVersion != 1 || evaluation.ExerciseID == "" || evaluation.Status == "" {
		return machineSubmission{}, errors.New("evaluation projection is incomplete")
	}
	allowedStatuses := map[string]bool{
		"deterministic_failed": true,
		"ready_for_review":     true,
		"accepted":             true,
		"revise":               true,
		"uncertain":            true,
		"technical_failure":    true,
	}
	if !allowedStatuses[evaluation.Status] ||
		(response.JobState == "failed") != (evaluation.Status == "technical_failure") {
		return machineSubmission{}, errors.New("evaluation projection status is inconsistent")
	}
	machine := machineEvaluation{
		SchemaVersion: evaluation.SchemaVersion,
		ExerciseID:    evaluation.ExerciseID,
		Status:        evaluation.Status,
	}
	if evaluation.Deterministic != nil {
		machine.DeterministicStatus = evaluation.Deterministic.Status
	}
	if evaluation.Review != nil {
		machine.Review = &machineReview{
			Status: evaluation.Review.Status, Mode: evaluation.Review.Mode,
			Authoritative: evaluation.Review.Authoritative, Verdict: evaluation.Review.Verdict,
		}
	}
	if evaluation.TechnicalError != nil {
		machine.TechnicalError = &machineTechnicalError{
			ErrorCode: evaluation.TechnicalError.ErrorCode,
			Stage:     evaluation.TechnicalError.Stage,
			SupportID: evaluation.TechnicalError.SupportID,
		}
	}
	payload.Evaluation = &machine
	return payload, nil
}
