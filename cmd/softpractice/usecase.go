package main

import (
	"context"
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
// learner. Latest is set once the latest submission has a terminal
// evaluation, and only when reading it succeeded.
type statusSnapshot struct {
	Repository gitRepository
	Link       learnercli.ProjectLink
	Workspace  learnercli.WorkspaceStatus
	User       currentUser
	Latest     *latestResult
}

// latestResult is what the next step depends on in a terminal evaluation of
// the latest submission.
type latestResult struct {
	Status string
	// QuestionsAnswerable is true when the reviewer's questions can be
	// answered on the result page: an authoritative uncertain review.
	QuestionsAnswerable bool
	// CommitSHA is the commit the submission was made from; empty when the
	// API did not return it.
	CommitSHA string
}

func (u learnerUseCases) Status(ctx context.Context) (statusSnapshot, error) {
	repository, link, workspace, err := linkedWorkspace(ctx, u.client, u.startDirectory, false)
	if err != nil {
		return statusSnapshot{}, err
	}
	var user currentUser
	if err := u.client.AuthorizedJSON(ctx, "GET", "/v1/me", nil, &user); err != nil {
		return statusSnapshot{}, err
	}
	snapshot := statusSnapshot{Repository: repository, Link: link, Workspace: workspace, User: user}
	if latest := workspace.LatestSubmission; latest != nil && (latest.JobState == "completed" || latest.JobState == "failed") {
		// The status stays available when only these optional reads fail.
		if result, err := u.Evaluation(ctx, latest.ID, evaluationWait{}, defaultReviewFeedback); err == nil &&
			result.Outcome == evaluationReady {
			built := result.Result
			snapshot.Latest = &latestResult{
				Status:              built.Status,
				QuestionsAnswerable: built.Review != nil && built.Review.QuestionsAnswerable,
			}
			snapshot.Latest.CommitSHA, _ = u.SubmittedCommit(ctx, latest.ID)
		}
	}
	return snapshot, nil
}

// SubmittedCommit returns the Git commit a submission was made from.
func (u learnerUseCases) SubmittedCommit(ctx context.Context, submissionID string) (string, error) {
	if err := validateSubmissionID(submissionID); err != nil {
		return "", err
	}
	var submission struct {
		ID       string `json:"id"`
		Revision struct {
			CommitSHA string `json:"commit_sha"`
		} `json:"revision"`
	}
	if err := u.client.AuthorizedJSON(ctx, "GET", "/v1/submissions/"+submissionID, nil, &submission); err != nil {
		return "", err
	}
	if submission.ID != submissionID {
		return "", errors.New("submission response does not match the requested submission")
	}
	return submission.Revision.CommitSHA, nil
}

// lessonContext names the lesson the linked folder is working on: the
// current lesson, at the version its tree was made for.
type lessonContext struct {
	Link         learnercli.ProjectLink
	Workspace    learnercli.WorkspaceStatus
	AssignmentID string
	Version      int
}

func (u learnerUseCases) Lesson(ctx context.Context) (lessonContext, error) {
	_, link, workspace, err := linkedWorkspace(ctx, u.client, u.startDirectory, false)
	if err != nil {
		return lessonContext{}, err
	}
	return lessonContext{
		Link: link, Workspace: workspace,
		AssignmentID: workspace.Assignment.ID, Version: submittedLessonVersion(link, workspace),
	}, nil
}

func (l lessonContext) query() string {
	return "?version=" + strconv.Itoa(l.Version)
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

// Task returns the assignment text of the folder's lesson.
func (u learnerUseCases) Task(ctx context.Context) (lessonContext, assignmentDetail, error) {
	lesson, err := u.Lesson(ctx)
	if err != nil {
		return lessonContext{}, assignmentDetail{}, err
	}
	var assignment assignmentDetail
	if err := u.client.AuthorizedJSON(
		ctx, "GET", "/v1/assignments/"+url.PathEscape(lesson.AssignmentID)+lesson.query(), nil, &assignment,
	); err != nil {
		return lessonContext{}, assignmentDetail{}, err
	}
	if assignment.ID != lesson.AssignmentID || assignment.Version != lesson.Version {
		return lessonContext{}, assignmentDetail{}, errors.New("assignment response does not match the requested lesson")
	}
	return lesson, assignment, nil
}

type theoryMaterial struct {
	ID               string        `json:"id"`
	Title            string        `json:"title"`
	EstimatedMinutes int           `json:"estimated_minutes"`
	Markdown         string        `json:"markdown"`
	Diagram          theoryDiagram `json:"diagram"`
}

type theoryDiagram struct {
	Title           string `json:"title"`
	TextAlternative string `json:"text_alternative"`
	Nodes           []struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	} `json:"nodes"`
}

// materialSnapshot is the theory material of one lesson. Material is nil when
// the lesson has none. Version is zero for a lesson named explicitly: the API
// then serves its current published version.
type materialSnapshot struct {
	AssignmentID string
	Version      int
	Material     *theoryMaterial
}

// Material returns the theory material of the folder's lesson, or of
// lessonID when it is set, such as an earlier lesson of the practicum.
func (u learnerUseCases) Material(ctx context.Context, lessonID string) (materialSnapshot, error) {
	snapshot := materialSnapshot{AssignmentID: lessonID}
	query := ""
	if lessonID == "" {
		lesson, err := u.Lesson(ctx)
		if err != nil {
			return materialSnapshot{}, err
		}
		snapshot.AssignmentID, snapshot.Version, query = lesson.AssignmentID, lesson.Version, lesson.query()
	}
	var material theoryMaterial
	err := u.client.AuthorizedJSON(
		ctx, "GET", "/v1/assignments/"+url.PathEscape(snapshot.AssignmentID)+"/material"+query, nil, &material,
	)
	var statusError *learnercli.HTTPError
	switch {
	case err == nil:
		snapshot.Material = &material
	case errors.As(err, &statusError) && statusError.Status == http.StatusNotFound:
		// The lesson has no linked theory material.
	default:
		return materialSnapshot{}, err
	}
	return snapshot, nil
}

type preparedHint struct {
	ID       string `json:"id"`
	Order    int    `json:"order"`
	Markdown string `json:"markdown"`
}

// hintsSnapshot lists the prepared hints the learner has already opened for
// the folder's lesson. Status is available while a closed hint remains,
// exhausted when all are open, and unavailable when the lesson has none.
// Opening the next hint is a learner action on the assignment page.
type hintsSnapshot struct {
	Lesson   lessonContext
	Status   string
	Revealed []preparedHint
}

func (u learnerUseCases) Hints(ctx context.Context) (hintsSnapshot, error) {
	lesson, err := u.Lesson(ctx)
	if err != nil {
		return hintsSnapshot{}, err
	}
	var state struct {
		Status   string         `json:"status"`
		Revealed []preparedHint `json:"revealed"`
	}
	if err := u.client.AuthorizedJSON(
		ctx, "GET", "/v1/assignments/"+url.PathEscape(lesson.AssignmentID)+"/prepared-hints"+lesson.query(), nil, &state,
	); err != nil {
		return hintsSnapshot{}, err
	}
	if state.Revealed == nil {
		state.Revealed = []preparedHint{}
	}
	return hintsSnapshot{Lesson: lesson, Status: state.Status, Revealed: state.Revealed}, nil
}

type submissionAttempt struct {
	SubmissionID     string    `json:"submission_id"`
	LearningAttempt  int       `json:"learning_attempt"`
	JobState         string    `json:"job_state"`
	EvaluationStatus *string   `json:"evaluation_status"`
	SubmittedAt      time.Time `json:"submitted_at"`
}

// submissionsSnapshot is the recent submission history of the folder's
// lesson, newest first. The API returns at most five entries.
type submissionsSnapshot struct {
	Lesson  lessonContext
	History []submissionAttempt
}

func (u learnerUseCases) Submissions(ctx context.Context) (submissionsSnapshot, error) {
	lesson, err := u.Lesson(ctx)
	if err != nil {
		return submissionsSnapshot{}, err
	}
	snapshot := submissionsSnapshot{Lesson: lesson, History: []submissionAttempt{}}
	latest := lesson.Workspace.LatestSubmission
	if latest == nil {
		return snapshot, nil
	}
	if err := validateSubmissionID(latest.ID); err != nil {
		return submissionsSnapshot{}, err
	}
	var iteration struct {
		History []submissionAttempt `json:"history"`
	}
	if err := u.client.AuthorizedJSON(ctx, "GET", "/v1/submissions/"+latest.ID+"/iteration", nil, &iteration); err != nil {
		return submissionsSnapshot{}, err
	}
	if iteration.History != nil {
		snapshot.History = iteration.History
	}
	return snapshot, nil
}

func validateSubmissionID(submissionID string) error {
	parsed, err := uuid.Parse(submissionID)
	if err != nil || parsed.String() != submissionID {
		return errors.New("submission ID must be a canonical UUID")
	}
	return nil
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

// PrepareUpdate makes every check `softpractice update` makes and returns
// the pending lesson transition, lesson version update, or missing public
// checks as a plan. It changes nothing: the caller shows the learner what
// will change and asks for confirmation before ApplyUpdate.
func (u learnerUseCases) PrepareUpdate(ctx context.Context) (updatePlan, error) {
	return prepareProjectUpdate(ctx, u.client, u.startDirectory, "")
}

// ApplyUpdate applies a prepared plan, provided the folder has not moved
// since it was prepared. Its report for the learner goes to output.
func (u learnerUseCases) ApplyUpdate(ctx context.Context, plan updatePlan, output io.Writer) error {
	return applyProjectUpdate(ctx, u.client, plan, output)
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
	// Submission is set for ready and pending outcomes.
	Submission machineSubmission
	// Result is the evaluation projected at the requested detail; it is set
	// only for the ready outcome. The raw API projection, which carries the
	// reviewer's directions and questions, never leaves this use case.
	Result *machineResult
}

// Evaluation reads or waits for one evaluation and projects it at detail.
func (u learnerUseCases) Evaluation(
	ctx context.Context,
	submissionID string,
	wait evaluationWait,
	detail reviewFeedbackDetail,
) (evaluationResult, error) {
	if err := validateSubmissionID(submissionID); err != nil {
		return evaluationResult{}, err
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
				built, err := buildMachineResult(response.Evaluation, detail)
				if err != nil {
					return evaluationResult{}, err
				}
				return evaluationResult{
					Outcome: evaluationReady, SubmissionID: submissionID,
					Submission: payload, Result: built,
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
