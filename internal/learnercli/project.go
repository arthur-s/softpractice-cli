package learnercli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/google/uuid"
)

type PracticumCatalog struct {
	Practicums         []Practicum     `json:"practicums"`
	UpcomingPracticums json.RawMessage `json:"upcoming_practicums"`
}

type Practicum struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CanSkipEntry bool   `json:"can_skip_entry"`
	// Introduction and Lessons are evolving public catalog content. Keep their
	// full shape opaque; project restore decodes only the stable route fields it
	// needs after the surrounding response passes strict validation.
	Introduction    json.RawMessage `json:"introduction"`
	Completion      json.RawMessage `json:"completion"`
	FirstAssignment struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Version          int    `json:"version"`
		EstimatedMinutes int    `json:"estimated_minutes"`
		LearnerSurface   string `json:"learner_surface"`
	} `json:"first_assignment"`
	Workspace *struct {
		ID             string    `json:"id"`
		ProjectID      string    `json:"project_id"`
		State          string    `json:"state"`
		BaseRevisionID *string   `json:"base_revision_id"`
		SupportMode    *string   `json:"support_mode"`
		CreatedAt      time.Time `json:"created_at"`
		UpdatedAt      time.Time `json:"updated_at"`
	} `json:"workspace"`
	CurrentAssignment *struct {
		ID          string          `json:"id"`
		Title       string          `json:"title"`
		Version     int             `json:"version"`
		State       string          `json:"state"`
		LocalChecks json.RawMessage `json:"local_checks"`
	} `json:"current_assignment"`
	Lessons json.RawMessage `json:"lessons"`
}

// ProjectRestoreSource describes the public learner state needed to recreate
// one linked local project. Revision sources may require the single published
// course update from the accepted predecessor to the current assignment.
type ProjectRestoreSource struct {
	Kind                   string
	WorkspaceID            string
	ProjectID              string
	AssignmentID           string
	AssignmentVersion      int
	SubmissionID           string
	ExpectedBaseRevisionID string
	NeedsCourseUpdate      bool
}

type practicumRestoreLesson struct {
	ID                 string  `json:"id"`
	Version            int     `json:"version"`
	State              string  `json:"state"`
	ResultSubmissionID *string `json:"result_submission_id"`
}

const projectLinkRelativePath = ".softpractice/project.json"

var projectIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type ProjectLink struct {
	SchemaVersion     int    `json:"schema_version"`
	WorkspaceID       string `json:"workspace_id"`
	ProjectID         string `json:"project_id"`
	AssignmentID      string `json:"assignment_id,omitempty"`
	AssignmentVersion int    `json:"assignment_version,omitempty"`
}

func LoadProjectLink(repositoryRoot string) (ProjectLink, error) {
	path := filepath.Join(repositoryRoot, projectLinkRelativePath)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectLink{}, fmt.Errorf(
				"%s is missing; download the project linked to your workspace",
				projectLinkRelativePath,
			)
		}
		return ProjectLink{}, fmt.Errorf("inspect project link: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ProjectLink{}, errors.New(".softpractice/project.json must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectLink{}, fmt.Errorf("read project link: %w", err)
	}
	var link ProjectLink
	if err := decodeSingleJSON(data, &link); err != nil {
		return ProjectLink{}, fmt.Errorf("decode project link: %w", err)
	}
	workspaceID, err := uuid.Parse(link.WorkspaceID)
	if err != nil || workspaceID.String() != link.WorkspaceID {
		return ProjectLink{}, errors.New("project link workspace_id must be a canonical UUID")
	}
	if !projectIDPattern.MatchString(link.ProjectID) {
		return ProjectLink{}, errors.New("project link project_id is invalid")
	}
	switch link.SchemaVersion {
	case 1:
		if link.AssignmentID != "" || link.AssignmentVersion != 0 {
			return ProjectLink{}, errors.New("project link v1 must not pin an assignment")
		}
	case 2:
		if !projectIDPattern.MatchString(link.AssignmentID) || link.AssignmentVersion < 1 {
			return ProjectLink{}, errors.New("project link v2 assignment pin is invalid")
		}
	default:
		return ProjectLink{}, errors.New("project link schema_version is invalid")
	}
	return link, nil
}

type WorkspaceStatus struct {
	Workspace struct {
		ID             string    `json:"id"`
		ProjectID      string    `json:"project_id"`
		State          string    `json:"state"`
		BaseRevisionID *string   `json:"base_revision_id"`
		SupportMode    *string   `json:"support_mode"`
		CreatedAt      time.Time `json:"created_at"`
		UpdatedAt      time.Time `json:"updated_at"`
	} `json:"workspace"`
	Assignment struct {
		ID          string          `json:"id"`
		Version     int             `json:"version"`
		Title       string          `json:"title"`
		State       string          `json:"state"`
		LocalChecks json.RawMessage `json:"local_checks"`
	} `json:"assignment"`
	LatestSubmission *struct {
		ID          string    `json:"id"`
		RevisionID  string    `json:"revision_id"`
		JobState    string    `json:"job_state"`
		SubmittedAt time.Time `json:"submitted_at"`
	} `json:"latest_submission"`
}

type CourseUpdate struct {
	Ref                   string `json:"ref"`
	FromAssignmentID      string `json:"from_assignment_id"`
	FromAssignmentVersion int    `json:"from_assignment_version"`
	ToAssignmentID        string `json:"to_assignment_id"`
	ToAssignmentVersion   int    `json:"to_assignment_version"`
	BaseRevisionID        string `json:"base_revision_id"`
	BaseContentSHA256     string `json:"base_content_sha256"`
	ArchiveSHA256         string `json:"archive_sha256"`
	ArchiveSize           int64  `json:"archive_size"`
	Files                 []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
	Operations []struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	} `json:"operations"`
}

func (c *Client) PrepareCourseUpdate(ctx context.Context, workspaceID string) (CourseUpdate, error) {
	var update CourseUpdate
	if err := c.AuthorizedJSON(ctx, "POST", "/v1/workspaces/"+workspaceID+"/current-assignment/course-update", nil, &update); err != nil {
		return CourseUpdate{}, err
	}
	return update, nil
}

func (c *Client) GetLinkedWorkspace(
	ctx context.Context,
	link ProjectLink,
) (WorkspaceStatus, error) {
	status, err := c.GetWorkspaceStatus(ctx, link.WorkspaceID)
	if err != nil {
		return WorkspaceStatus{}, err
	}
	if status.Workspace.ID != link.WorkspaceID || status.Workspace.ProjectID != link.ProjectID {
		return WorkspaceStatus{}, errors.New(
			"local project link does not match the server workspace; download the project again",
		)
	}
	return status, nil
}

// GetWorkspaceStatus returns the current public projection for an owned
// workspace without requiring a local project link first.
func (c *Client) GetWorkspaceStatus(ctx context.Context, workspaceID string) (WorkspaceStatus, error) {
	parsed, err := uuid.Parse(workspaceID)
	if err != nil || parsed.String() != workspaceID {
		return WorkspaceStatus{}, errors.New("workspace ID must be a canonical UUID")
	}
	var status WorkspaceStatus
	if err := c.AuthorizedJSON(
		ctx,
		"GET",
		"/v1/workspaces/"+workspaceID+"/current-assignment",
		nil,
		&status,
	); err != nil {
		return WorkspaceStatus{}, err
	}
	if status.Workspace.ID != workspaceID {
		return WorkspaceStatus{}, errors.New("workspace response identity does not match the request")
	}
	return status, nil
}

// StartedPracticum returns the learner's started practicum. When practicumID is
// supplied, it disambiguates accounts that have more than one active practicum.
func (c *Client) StartedPracticum(ctx context.Context, practicumID string) (Practicum, error) {
	var catalog PracticumCatalog
	if err := c.AuthorizedJSON(ctx, "GET", "/v1/practicums", nil, &catalog); err != nil {
		return Practicum{}, err
	}
	var started *Practicum
	for index := range catalog.Practicums {
		practicum := &catalog.Practicums[index]
		if practicum.Workspace == nil || (practicumID != "" && practicum.ID != practicumID) {
			continue
		}
		if started != nil {
			return Practicum{}, errors.New("multiple started practicums found; run softpractice starter --practicum PRACTICUM_ID")
		}
		started = practicum
	}
	if started == nil {
		if practicumID != "" {
			return Practicum{}, fmt.Errorf("practicum %q is not started; start it in the web app first", practicumID)
		}
		return Practicum{}, errors.New("start a practicum in the web app before downloading its project")
	}
	return *started, nil
}

// ResolveProjectRestoreSource chooses the newest public learner revision that
// can safely seed a linked local project. With no submission history it falls
// back to the current published starter. When the current lesson has no
// submission yet, it restores the accepted predecessor and requires the
// ordinary authorized course update.
func (c *Client) ResolveProjectRestoreSource(
	ctx context.Context,
	practicumID string,
) (ProjectRestoreSource, error) {
	practicum, err := c.StartedPracticum(ctx, practicumID)
	if err != nil {
		return ProjectRestoreSource{}, err
	}
	if practicum.Workspace == nil || practicum.CurrentAssignment == nil {
		return ProjectRestoreSource{}, errors.New("started practicum has no current project")
	}
	workspaceID := practicum.Workspace.ID
	status, err := c.GetWorkspaceStatus(ctx, workspaceID)
	if err != nil {
		return ProjectRestoreSource{}, err
	}
	if status.Workspace.ProjectID != practicum.ID ||
		status.Assignment.ID != practicum.CurrentAssignment.ID ||
		status.Assignment.Version != practicum.CurrentAssignment.Version {
		return ProjectRestoreSource{}, errors.New("practicum progress changed while preparing the restore; retry")
	}
	base := ProjectRestoreSource{
		WorkspaceID:       workspaceID,
		ProjectID:         practicum.ID,
		AssignmentID:      status.Assignment.ID,
		AssignmentVersion: status.Assignment.Version,
	}
	if status.LatestSubmission != nil {
		if err := validateCanonicalUUID(status.LatestSubmission.ID, "submission"); err != nil {
			return ProjectRestoreSource{}, err
		}
		if err := validateCanonicalUUID(status.LatestSubmission.RevisionID, "revision"); err != nil {
			return ProjectRestoreSource{}, err
		}
		base.Kind = "revision"
		base.SubmissionID = status.LatestSubmission.ID
		return base, nil
	}
	if practicum.Workspace.BaseRevisionID == nil {
		base.Kind = "starter"
		return base, nil
	}
	if err := validateCanonicalUUID(*practicum.Workspace.BaseRevisionID, "base revision"); err != nil {
		return ProjectRestoreSource{}, err
	}
	lessons, err := decodeRestoreLessons(practicum.Lessons)
	if err != nil {
		return ProjectRestoreSource{}, err
	}
	currentIndex := -1
	for index, lesson := range lessons {
		if lesson.ID == status.Assignment.ID && lesson.Version == status.Assignment.Version {
			currentIndex = index
			break
		}
	}
	if currentIndex < 0 {
		return ProjectRestoreSource{}, errors.New("current assignment is absent from the practicum route")
	}
	for index := currentIndex - 1; index >= 0; index-- {
		lesson := lessons[index]
		if lesson.State != "accepted" || lesson.ResultSubmissionID == nil {
			continue
		}
		if !projectIDPattern.MatchString(lesson.ID) || lesson.Version < 1 {
			return ProjectRestoreSource{}, errors.New("accepted practicum lesson is invalid")
		}
		if err := validateCanonicalUUID(*lesson.ResultSubmissionID, "submission"); err != nil {
			return ProjectRestoreSource{}, err
		}
		base.Kind = "revision"
		base.AssignmentID = lesson.ID
		base.AssignmentVersion = lesson.Version
		base.SubmissionID = *lesson.ResultSubmissionID
		base.ExpectedBaseRevisionID = *practicum.Workspace.BaseRevisionID
		base.NeedsCourseUpdate = true
		return base, nil
	}
	return ProjectRestoreSource{}, errors.New("accepted base submission is unavailable for project restore")
}

// LatestPracticumSubmission returns the newest submission of the started
// practicum: the latest one for the current assignment or, before the first
// submission for it, the accepted predecessor that advanced the workspace.
// found is false while the workspace still has no submission at all.
func (c *Client) LatestPracticumSubmission(
	ctx context.Context,
	practicumID string,
) (assignmentID, submissionID string, found bool, err error) {
	source, err := c.ResolveProjectRestoreSource(ctx, practicumID)
	if err != nil {
		return "", "", false, err
	}
	if source.Kind != "revision" {
		return "", "", false, nil
	}
	return source.AssignmentID, source.SubmissionID, true, nil
}

func decodeRestoreLessons(raw json.RawMessage) ([]practicumRestoreLesson, error) {
	var lessons []practicumRestoreLesson
	if err := json.Unmarshal(raw, &lessons); err != nil || len(lessons) == 0 {
		return nil, errors.New("practicum lesson route is invalid")
	}
	return lessons, nil
}

func validateCanonicalUUID(value, label string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return fmt.Errorf("%s ID must be a canonical UUID", label)
	}
	return nil
}
