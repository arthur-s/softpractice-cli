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

type outputFormat string

const (
	outputFormatText outputFormat = "text"
	outputFormatJSON outputFormat = "json"
)

func parseOutputFormat(value string) (outputFormat, error) {
	switch outputFormat(strings.ToLower(strings.TrimSpace(value))) {
	case outputFormatText:
		return outputFormatText, nil
	case outputFormatJSON:
		return outputFormatJSON, nil
	default:
		return "", errors.New("format must be text or json")
	}
}

type machineStatus struct {
	ContractVersion int    `json:"contract_version"`
	Kind            string `json:"kind"`
	Account         struct {
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
		State   string `json:"state"`
	} `json:"assignment"`
	Local struct {
		Head  string `json:"head"`
		Clean bool   `json:"clean"`
	} `json:"local"`
	// Transition is present only while the server has already opened the next
	// lesson and this folder still holds the previous one. It is additive: a
	// contract_version 1 reader that ignores unknown keys is unaffected.
	Transition       *machineTransition        `json:"transition,omitempty"`
	LatestSubmission *machineSubmissionSummary `json:"latest_submission"`
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
}

func writeStatusJSON(
	output io.Writer,
	user currentUser,
	link learnercli.ProjectLink,
	workspace learnercli.WorkspaceStatus,
	repository gitRepository,
) error {
	payload := machineStatus{ContractVersion: 1, Kind: "softpractice.status"}
	payload.Account.ID = user.ID
	payload.Account.EmailVerified = user.EmailVerified
	payload.Workspace.ID = workspace.Workspace.ID
	payload.Workspace.ProjectID = link.ProjectID
	payload.Workspace.State = workspace.Workspace.State
	payload.Assignment.ID = workspace.Assignment.ID
	payload.Assignment.Version = workspace.Assignment.Version
	payload.Assignment.State = workspace.Assignment.State
	payload.Local.Head = repository.CommitSHA
	payload.Local.Clean = repository.Clean
	if pendingTransition(link, workspace) {
		payload.Transition = &machineTransition{
			FromAssignmentID: link.AssignmentID, FromAssignmentVersion: link.AssignmentVersion,
			ToAssignmentID: workspace.Assignment.ID, ToAssignmentVersion: workspace.Assignment.Version,
		}
	}
	if workspace.LatestSubmission != nil {
		payload.LatestSubmission = &machineSubmissionSummary{
			ID: workspace.LatestSubmission.ID, RevisionID: workspace.LatestSubmission.RevisionID,
			JobState:    workspace.LatestSubmission.JobState,
			SubmittedAt: workspace.LatestSubmission.SubmittedAt,
		}
	}
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
	ContractVersion int                `json:"contract_version"`
	Kind            string             `json:"kind"`
	SubmissionID    string             `json:"submission_id"`
	EvaluationJobID string             `json:"evaluation_job_id"`
	JobState        string             `json:"job_state"`
	Terminal        bool               `json:"terminal"`
	Attempt         int                `json:"attempt"`
	MaxAttempts     int                `json:"max_attempts"`
	NextPollSeconds int                `json:"next_poll_seconds,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at"`
	ResultPath      string             `json:"result_path"`
	Evaluation      *machineEvaluation `json:"evaluation,omitempty"`
}

// machineSubmissionV2 preserves the entire learner-safe evaluation projection
// returned by the public API. Version 1 intentionally exposes a compact
// summary; v2 is for automation that must assert individual safe checks and
// review fields without parsing human-oriented CLI output.
type machineSubmissionV2 struct {
	ContractVersion int             `json:"contract_version"`
	Kind            string          `json:"kind"`
	SubmissionID    string          `json:"submission_id"`
	EvaluationJobID string          `json:"evaluation_job_id"`
	JobState        string          `json:"job_state"`
	Terminal        bool            `json:"terminal"`
	Attempt         int             `json:"attempt"`
	MaxAttempts     int             `json:"max_attempts"`
	NextPollSeconds int             `json:"next_poll_seconds,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
	ResultPath      string          `json:"result_path"`
	Evaluation      json.RawMessage `json:"evaluation,omitempty"`
}

func submissionCommand(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	args []string,
	output, errorOutput io.Writer,
) error {
	if len(args) == 0 {
		return errors.New("usage: softpractice submission show|download ...")
	}
	switch args[0] {
	case "show":
		return showSubmission(ctx, client, startDirectory, args[1:], output, errorOutput)
	case "download":
		return downloadSubmissionRevision(ctx, client, args[1:], output, errorOutput)
	default:
		return errors.New("usage: softpractice submission show|download ...")
	}
}

func showSubmission(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	args []string,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("submission show", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	id := flags.String("id", "", "submission ID; defaults to the latest linked submission")
	format := flags.String("format", "text", "output format: text, json, or json-v2")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: softpractice submission show [--id ID] [--format text|json|json-v2]")
	}
	formatValue := strings.ToLower(strings.TrimSpace(*format))
	outputFormat := outputFormatText
	jsonV2 := formatValue == "json-v2"
	if !jsonV2 {
		var err error
		outputFormat, err = parseOutputFormat(formatValue)
		if err != nil {
			return errors.New("format must be text, json, or json-v2")
		}
	}
	submissionID := strings.TrimSpace(*id)
	if submissionID == "" {
		_, _, workspace, linkErr := linkedWorkspace(ctx, client, startDirectory, false)
		if linkErr != nil {
			return linkErr
		}
		if workspace.LatestSubmission == nil {
			return errors.New("the linked workspace has no submission")
		}
		submissionID = workspace.LatestSubmission.ID
	}
	parsedID, err := uuid.Parse(submissionID)
	if err != nil || parsedID.String() != submissionID {
		return errors.New("submission ID must be a canonical UUID")
	}
	var response evaluationAPIResponse
	if err := client.AuthorizedJSON(
		ctx, "GET", "/v1/submissions/"+submissionID+"/evaluation", nil, &response,
	); err != nil {
		return err
	}
	payload, err := normalizeSubmissionResponse(response)
	if err != nil {
		return err
	}
	if payload.SubmissionID != submissionID {
		return errors.New("evaluation response does not match the requested submission")
	}
	if jsonV2 {
		return writeMachineJSON(output, normalizeSubmissionResponseV2(payload, response.Evaluation))
	}
	if outputFormat == outputFormatJSON {
		return writeMachineJSON(output, payload)
	}
	fmt.Fprintf(
		output,
		text(ctx, "Отправка: %s\nСостояние проверки: %s\n", "Submission: %s\nEvaluation state: %s\n"),
		payload.SubmissionID,
		payload.JobState,
	)
	if payload.Evaluation != nil {
		fmt.Fprintf(output, text(ctx, "Результат: %s\n", "Result: %s\n"), payload.Evaluation.Status)
	}
	return nil
}

func downloadSubmissionRevision(
	ctx context.Context,
	client *learnercli.Client,
	args []string,
	output, errorOutput io.Writer,
) error {
	flags := flag.NewFlagSet("submission download", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	id := flags.String("id", "", "accepted submission ID from the result page")
	destination := flags.String("output", "", "destination ZIP path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*id) == "" {
		return errors.New(text(ctx,
			"использование: softpractice submission download --id ID [--output PATH]",
			"usage: softpractice submission download --id ID [--output PATH]"))
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
		ContractVersion: 1, Kind: "softpractice.submission",
		SubmissionID: response.SubmissionID, EvaluationJobID: response.EvaluationJobID,
		JobState: response.JobState, Attempt: response.Attempt, MaxAttempts: response.MaxAttempts,
		NextPollSeconds: response.NextPollSeconds, UpdatedAt: response.UpdatedAt,
		ResultPath: "/submissions/" + response.SubmissionID + "/result",
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

func normalizeSubmissionResponseV2(
	payload machineSubmission,
	evaluation json.RawMessage,
) machineSubmissionV2 {
	result := machineSubmissionV2{
		ContractVersion: 2,
		Kind:            payload.Kind,
		SubmissionID:    payload.SubmissionID,
		EvaluationJobID: payload.EvaluationJobID,
		JobState:        payload.JobState,
		Terminal:        payload.Terminal,
		Attempt:         payload.Attempt,
		MaxAttempts:     payload.MaxAttempts,
		NextPollSeconds: payload.NextPollSeconds,
		UpdatedAt:       payload.UpdatedAt,
		ResultPath:      payload.ResultPath,
	}
	if payload.Terminal {
		result.Evaluation = append(json.RawMessage(nil), evaluation...)
	}
	return result
}

func writeMachineJSON(output io.Writer, payload any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}
