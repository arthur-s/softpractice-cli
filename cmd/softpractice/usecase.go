package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

// learnerUseCases holds the learner operations that CLI commands format for a
// terminal and that `softpractice mcp` will expose as tools. They write
// nothing to standard output: results are returned, and the few operations
// that report progress take the writer explicitly.
type learnerUseCases struct {
	client         *learnercli.Client
	startDirectory string
	now            func() time.Time
	sleep          func(context.Context, time.Duration) error
}

func newLearnerUseCases(client *learnercli.Client, startDirectory string) learnerUseCases {
	return learnerUseCases{
		client: client, startDirectory: startDirectory,
		now: time.Now, sleep: sleepContext,
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// statusSnapshot is the linked project, its workspace, and the signed-in
// learner. LatestEvaluationStatus is set only when it was requested and the
// latest submission already has a terminal evaluation.
type statusSnapshot struct {
	Repository             gitRepository
	Link                   learnercli.ProjectLink
	Workspace              learnercli.WorkspaceStatus
	User                   currentUser
	LatestEvaluationStatus string
}

func (u learnerUseCases) Status(ctx context.Context, withEvaluation bool) (statusSnapshot, error) {
	repository, link, workspace, err := linkedWorkspace(ctx, u.client, u.startDirectory, false)
	if err != nil {
		return statusSnapshot{}, err
	}
	var user currentUser
	if err := u.client.AuthorizedJSON(ctx, "GET", "/v1/me", nil, &user); err != nil {
		return statusSnapshot{}, err
	}
	snapshot := statusSnapshot{Repository: repository, Link: link, Workspace: workspace, User: user}
	if withEvaluation && workspace.LatestSubmission != nil {
		switch workspace.LatestSubmission.JobState {
		case "completed", "failed":
			// The status stays available when only this optional field fails.
			result, err := u.Evaluation(ctx, workspace.LatestSubmission.ID, evaluationWait{})
			if err == nil && result.Outcome == evaluationReady && result.Submission.Evaluation != nil {
				snapshot.LatestEvaluationStatus = result.Submission.Evaluation.Status
			}
		}
	}
	return snapshot, nil
}

// assignmentSnapshot is the requirement the linked folder is working on: the
// lesson version its tree was made for, and the theory material linked to it
// when the lesson has one.
type assignmentSnapshot struct {
	Link       learnercli.ProjectLink
	Workspace  learnercli.WorkspaceStatus
	Assignment assignmentDetail
	Material   *theoryMaterial
}

type assignmentDetail struct {
	ID                   string `json:"id"`
	Version              int    `json:"version"`
	Title                string `json:"title"`
	Kind                 string `json:"kind"`
	ProjectSetup         string `json:"project_setup"`
	ExerciseID           string `json:"exercise_id"`
	EstimatedMinutes     int    `json:"estimated_minutes"`
	InstructionsMarkdown string `json:"instructions_markdown"`
	ContentSHA256        string `json:"content_sha256"`
}

type theoryMaterial struct {
	ID               string          `json:"id"`
	Title            string          `json:"title"`
	EstimatedMinutes int             `json:"estimated_minutes"`
	Markdown         string          `json:"markdown"`
	Diagram          json.RawMessage `json:"diagram"`
}

func (u learnerUseCases) Assignment(ctx context.Context) (assignmentSnapshot, error) {
	_, link, workspace, err := linkedWorkspace(ctx, u.client, u.startDirectory, false)
	if err != nil {
		return assignmentSnapshot{}, err
	}
	id := workspace.Assignment.ID
	version := submittedLessonVersion(link, workspace)
	query := "?version=" + strconv.Itoa(version)
	var assignment assignmentDetail
	if err := u.client.AuthorizedJSON(
		ctx, "GET", "/v1/assignments/"+url.PathEscape(id)+query, nil, &assignment,
	); err != nil {
		return assignmentSnapshot{}, err
	}
	if assignment.ID != id || assignment.Version != version {
		return assignmentSnapshot{}, errors.New("assignment response does not match the requested lesson")
	}
	snapshot := assignmentSnapshot{Link: link, Workspace: workspace, Assignment: assignment}
	var material theoryMaterial
	err = u.client.AuthorizedJSON(
		ctx, "GET", "/v1/assignments/"+url.PathEscape(id)+"/material"+query, nil, &material,
	)
	var statusError *learnercli.HTTPError
	switch {
	case err == nil:
		snapshot.Material = &material
	case errors.As(err, &statusError) && statusError.Status == http.StatusNotFound:
		// The lesson has no linked theory material.
	default:
		return assignmentSnapshot{}, err
	}
	return snapshot, nil
}

// preparedSubmission is a linked, clean, submittable commit with its archive
// already built. Close removes the archive.
type preparedSubmission struct {
	Repository gitRepository
	Link       learnercli.ProjectLink
	Workspace  learnercli.WorkspaceStatus
}

func (p preparedSubmission) Close() {
	if p.Repository.ArchivePath != "" {
		_ = os.Remove(p.Repository.ArchivePath)
	}
}

// PrepareSubmission checks that HEAD can be submitted and packs it. It sends
// nothing: the caller shows the learner what will be submitted and asks for
// confirmation before SendSubmission.
func (u learnerUseCases) PrepareSubmission(ctx context.Context) (preparedSubmission, error) {
	repository, link, workspace, err := linkedWorkspace(ctx, u.client, u.startDirectory, true)
	if err != nil {
		return preparedSubmission{}, err
	}
	prepared := preparedSubmission{Repository: repository, Link: link, Workspace: workspace}
	if !repository.Clean {
		prepared.Close()
		return preparedSubmission{}, errors.New(text(ctx, "рабочее дерево не чистое; сделайте commit всех изменений перед отправкой", "working tree is not clean; commit all changes before submitting"))
	}
	if workspace.Workspace.State != "active" {
		prepared.Close()
		return preparedSubmission{}, fmt.Errorf(text(ctx, "workspace находится в состоянии %s и не принимает отправки", "workspace is %s and cannot accept a submission"), workspace.Workspace.State)
	}
	if workspace.Assignment.State != "available" && workspace.Assignment.State != "submitted" {
		prepared.Close()
		return preparedSubmission{}, fmt.Errorf(
			text(ctx, "урок находится в состоянии %s и не принимает отправки", "assignment is %s and cannot accept a submission"),
			workspace.Assignment.State,
		)
	}
	return prepared, nil
}

func (u learnerUseCases) SendSubmission(ctx context.Context, prepared preparedSubmission) (submissionReceipt, error) {
	requestPath, headers, idempotencyKey := submissionRequest(
		prepared.Link,
		prepared.Workspace,
		prepared.Repository.CommitSHA,
	)
	var receipt submissionReceipt
	if err := u.client.Submit(ctx, requestPath, prepared.Repository.ArchivePath, headers, &receipt); err != nil {
		return submissionReceipt{}, fmt.Errorf(
			"submit %s at %s (idempotency key %s): %w",
			prepared.Repository.Root,
			prepared.Repository.CommitSHA,
			idempotencyKey,
			err,
		)
	}
	return receipt, nil
}

// Update applies the pending lesson transition or lesson version update to
// the linked folder, with every check `softpractice update` makes.
func (u learnerUseCases) Update(ctx context.Context, output io.Writer) error {
	return updateLinkedProject(ctx, u.client, u.startDirectory, "", output)
}

// latestSubmission names the linked workspace's newest submission. Right
// after an acceptance the current lesson has none yet, and the newest one is
// the accepted submission of the previous lesson: predecessorAssignmentID
// then names that lesson.
type latestSubmission struct {
	SubmissionID            string
	CurrentAssignmentID     string
	PredecessorAssignmentID string
}

func (u learnerUseCases) LatestSubmission(ctx context.Context) (latestSubmission, error) {
	_, link, workspace, err := linkedWorkspace(ctx, u.client, u.startDirectory, false)
	if err != nil {
		return latestSubmission{}, err
	}
	latest := latestSubmission{CurrentAssignmentID: workspace.Assignment.ID}
	if workspace.LatestSubmission != nil {
		latest.SubmissionID = workspace.LatestSubmission.ID
		return latest, nil
	}
	assignmentID, submissionID, found, err := u.client.LatestPracticumSubmission(ctx, link.ProjectID)
	if err != nil {
		return latestSubmission{}, err
	}
	if !found {
		return latestSubmission{}, errors.New("the linked workspace has no submission")
	}
	latest.SubmissionID = submissionID
	latest.PredecessorAssignmentID = assignmentID
	return latest, nil
}

type evaluationOutcome string

const (
	// evaluationReady: the evaluation finished, as a result or as a safe
	// technical failure.
	evaluationReady evaluationOutcome = "ready"
	// evaluationPending: the evaluation is still queued or running.
	evaluationPending evaluationOutcome = "pending"
	// evaluationSuperseded: the server replaced this evaluation job; it will
	// never have a result.
	evaluationSuperseded evaluationOutcome = "superseded"
)

// evaluationWait selects one poll (the zero value) or polling until the
// evaluation is ready, superseded, or Timeout would pass before the next poll
// the server allows.
type evaluationWait struct {
	Wait    bool
	Timeout time.Duration
}

// defaultEvaluationPoll is used when the server names no interval.
const defaultEvaluationPoll = 2 * time.Second

type evaluationResult struct {
	Outcome      evaluationOutcome
	SubmissionID string
	// Submission and RawEvaluation are set for ready and pending outcomes.
	Submission    machineSubmission
	RawEvaluation json.RawMessage
}

func (u learnerUseCases) Evaluation(
	ctx context.Context,
	submissionID string,
	wait evaluationWait,
) (evaluationResult, error) {
	parsedID, err := uuid.Parse(submissionID)
	if err != nil || parsedID.String() != submissionID {
		return evaluationResult{}, errors.New("submission ID must be a canonical UUID")
	}
	deadline := u.now().Add(wait.Timeout)
	path := "/v1/submissions/" + submissionID + "/evaluation"
	// pending is the last pending answer; a rate limit does not replace it.
	var pending evaluationResult
	for {
		var response evaluationAPIResponse
		poll, err := u.client.AuthorizedPoll(ctx, path, &response)
		var delay time.Duration
		var statusError *learnercli.HTTPError
		switch {
		case err == nil:
			payload, normalizeErr := normalizeSubmissionResponse(response)
			if normalizeErr != nil {
				return evaluationResult{}, normalizeErr
			}
			if payload.SubmissionID != submissionID {
				return evaluationResult{}, errors.New("evaluation response does not match the requested submission")
			}
			if payload.Terminal {
				return evaluationResult{
					Outcome: evaluationReady, SubmissionID: submissionID,
					Submission: payload, RawEvaluation: response.Evaluation,
				}, nil
			}
			pending = evaluationResult{Outcome: evaluationPending, SubmissionID: submissionID, Submission: payload}
			delay = poll.RetryAfter
			if delay == 0 {
				delay = time.Duration(payload.NextPollSeconds) * time.Second
			}
		case errors.As(err, &statusError) && statusError.Status == http.StatusConflict &&
			statusError.Code == "evaluation_superseded":
			return evaluationResult{Outcome: evaluationSuperseded, SubmissionID: submissionID}, nil
		case wait.Wait && errors.As(err, &statusError) && statusError.Status == http.StatusTooManyRequests &&
			statusError.RetryAfter > 0:
			delay = statusError.RetryAfter
		default:
			return evaluationResult{}, err
		}
		if delay < time.Second {
			delay = defaultEvaluationPoll
		}
		if !wait.Wait || u.now().Add(delay).After(deadline) {
			// Only a known pending state can be reported when the wait ends,
			// so a rate limit before the first answer stays an error.
			if pending.Outcome == "" {
				return evaluationResult{}, err
			}
			return pending, nil
		}
		if err := u.sleep(ctx, delay); err != nil {
			return evaluationResult{}, err
		}
	}
}
