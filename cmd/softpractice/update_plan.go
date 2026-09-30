package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/localchecks"
	"github.com/arthur-s/softpractice-cli/internal/submission"
)

// updateKind names what `update` will do to the linked folder.
type updateKind string

const (
	// updateLessonVersion moves the folder to the republished version of the
	// lesson it holds, replacing only author-owned lesson files.
	updateLessonVersion updateKind = "lesson_version"
	// updateLessonTransition applies the transition from the accepted
	// previous lesson to the current one.
	updateLessonTransition updateKind = "lesson_transition"
	// updateLocalChecks adds the missing public checks configuration.
	updateLocalChecks updateKind = "local_checks"
)

// updateOperation is one change to a project file: add, replace, or delete.
type updateOperation struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// updatePlan is a verified project update that has not touched the folder
// yet. The learner can review Operations before ApplyUpdate; the Softpractice
// metadata under .softpractice/ is rewritten as well and is not listed.
type updatePlan struct {
	Kind                  updateKind
	Root                  string
	HeadCommit            string
	FromAssignmentID      string
	FromAssignmentVersion int
	ToAssignmentID        string
	ToAssignmentVersion   int
	Ref                   string
	Operations            []updateOperation

	link        learnercli.ProjectLink
	localChecks []byte
	course      learnercli.CourseUpdate
	archivePath string
}

// prepareProjectUpdate makes every check `softpractice update` makes and asks
// the server for the update, but changes nothing on disk. A non-empty
// expectedBaseRevisionID pins the accepted revision `project restore` staged.
func prepareProjectUpdate(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	expectedBaseRevisionID string,
) (updatePlan, error) {
	repository, link, workspace, err := linkedWorkspace(ctx, client, startDirectory, true)
	if err != nil {
		return updatePlan{}, err
	}
	defer os.Remove(repository.ArchivePath)
	if !repository.Clean {
		return updatePlan{}, errors.New(text(ctx, "рабочее дерево не чистое; сделайте commit или stash перед обновлением проекта", "working tree is not clean; commit or stash changes before updating the course project"))
	}
	if workspace.Workspace.State != "active" {
		return updatePlan{}, fmt.Errorf(text(ctx, "workspace находится в состоянии %s и не имеет обновления курса", "workspace is %s and has no course update"), workspace.Workspace.State)
	}
	plan := updatePlan{
		Root: repository.Root, HeadCommit: repository.CommitSHA,
		FromAssignmentID: link.AssignmentID, FromAssignmentVersion: link.AssignmentVersion,
		ToAssignmentID: workspace.Assignment.ID, ToAssignmentVersion: workspace.Assignment.Version,
		Operations: []updateOperation{},
		link:       link, localChecks: workspace.Assignment.LocalChecks,
	}
	if pendingLessonVersion(link, workspace) {
		return prepareLessonVersionUpdate(ctx, client, plan)
	}
	if link.SchemaVersion == 2 && workspace.Assignment.ID == link.AssignmentID && workspace.Assignment.Version == link.AssignmentVersion {
		checksPath := filepath.Join(repository.Root, filepath.FromSlash(localchecks.RelativePath))
		if _, statErr := os.Stat(checksPath); errors.Is(statErr, os.ErrNotExist) {
			plan.Kind = updateLocalChecks
			plan.Operations = append(plan.Operations, updateOperation{Kind: "add", Path: localchecks.RelativePath})
			return plan, nil
		} else if statErr != nil {
			return updatePlan{}, fmt.Errorf("inspect local checks: %w", statErr)
		}
		return updatePlan{}, fmt.Errorf(text(ctx, "урок %s v%d всё ещё текущий; отправьте решение и дождитесь принятого результата перед обновлением", "lesson %s v%d is still current; submit and receive an accepted result before updating"), link.AssignmentID, link.AssignmentVersion)
	}
	update, err := client.PrepareCourseUpdate(ctx, link.WorkspaceID)
	if err != nil {
		return updatePlan{}, fmt.Errorf("prepare course update: %w", err)
	}
	// The transition may start from a newer version of the lesson this folder
	// holds: the lesson was republished after this tree was made, and the tree
	// was accepted on its own version. The server offers that only when the
	// transition itself replaces every lesson file the republication changed;
	// the base content check below still pins the exact accepted tree.
	if (link.SchemaVersion == 2 && (update.FromAssignmentID != link.AssignmentID || update.FromAssignmentVersion < link.AssignmentVersion)) ||
		update.ToAssignmentID != workspace.Assignment.ID || update.ToAssignmentVersion != workspace.Assignment.Version {
		return updatePlan{}, errors.New(text(ctx, "сервер вернул обновление для другого урока; проект не изменён", "server returned a course update for a different lesson; project was left unchanged"))
	}
	if expectedBaseRevisionID != "" && update.BaseRevisionID != expectedBaseRevisionID {
		return updatePlan{}, errors.New("workspace base revision changed while restoring the project; retry")
	}
	normalized, err := submission.NormalizeTarGz(repository.ArchivePath, os.TempDir(), submission.DefaultArchiveLimits())
	if err != nil {
		return updatePlan{}, fmt.Errorf("verify local project content: %w", err)
	}
	defer os.Remove(normalized.Path)
	if normalized.ContentSHA256 != update.BaseContentSHA256 {
		// A non-empty startDirectory means this is the staging copy driven by
		// `project restore`, not a folder the learner is working in.
		return updatePlan{}, courseUpdateMismatchError(ctx, repository, update, startDirectory != "")
	}
	plan.Kind = updateLessonTransition
	plan.FromAssignmentID, plan.FromAssignmentVersion = update.FromAssignmentID, update.FromAssignmentVersion
	plan.Ref = update.Ref
	plan.course = update
	plan.archivePath = "/v1/workspaces/" + link.WorkspaceID + "/current-assignment/course-update/archive?format=tar.gz"
	for _, operation := range update.Operations {
		plan.Operations = append(plan.Operations, updateOperation{Kind: operation.Kind, Path: operation.Path})
	}
	return plan, nil
}

func prepareLessonVersionUpdate(ctx context.Context, client *learnercli.Client, plan updatePlan) (updatePlan, error) {
	link := plan.link
	update, err := client.LessonVersionUpdate(ctx, link.WorkspaceID, link.AssignmentVersion)
	if err != nil {
		return updatePlan{}, fmt.Errorf("prepare lesson version update: %w", err)
	}
	if update.AssignmentID != link.AssignmentID || update.FromVersion != link.AssignmentVersion ||
		update.ToVersion != plan.ToAssignmentVersion {
		return updatePlan{}, errors.New(text(ctx, "сервер вернул обновление для другого урока; проект не изменён", "server returned a course update for a different lesson; project was left unchanged"))
	}
	for _, operation := range update.Operations {
		if operation.Kind != "replace" {
			return updatePlan{}, errors.New(text(ctx, "обновление урока может только заменять файлы урока; проект не изменён", "a lesson update may only replace lesson files; project was left unchanged"))
		}
		plan.Operations = append(plan.Operations, updateOperation{Kind: operation.Kind, Path: operation.Path})
	}
	plan.Kind = updateLessonVersion
	plan.Ref = update.Ref
	if len(update.Operations) > 0 {
		plan.course = learnercli.CourseUpdate{
			Ref:              update.Ref,
			FromAssignmentID: update.AssignmentID, FromAssignmentVersion: update.FromVersion,
			ToAssignmentID: update.AssignmentID, ToAssignmentVersion: update.ToVersion,
			ArchiveSHA256: update.ArchiveSHA256, ArchiveSize: update.ArchiveSize,
			Files: update.Files, Operations: update.Operations,
		}
		plan.archivePath = fmt.Sprintf("/v1/workspaces/%s/current-assignment/lesson-version-update/archive?from_version=%d&format=tar.gz",
			link.WorkspaceID, link.AssignmentVersion)
	}
	return plan, nil
}

// applyProjectUpdate applies a prepared update. It refuses when the folder
// moved since the plan was made: the learner confirmed that exact change.
func applyProjectUpdate(ctx context.Context, client *learnercli.Client, plan updatePlan, output io.Writer) error {
	current, err := inspectGitRepository(ctx, plan.Root, false)
	if err != nil {
		return err
	}
	if current.CommitSHA != plan.HeadCommit || !current.Clean {
		return errors.New(text(ctx,
			"HEAD или рабочее дерево изменились после подготовки обновления; проект не изменён, повторите обновление",
			"HEAD or the working tree changed after the update was prepared; project was left unchanged, run the update again"))
	}
	root := plan.Root
	switch plan.Kind {
	case updateLocalChecks:
		if err := writeLocalChecks(root, plan.localChecks); err != nil {
			return err
		}
		if err := commitLocalChecks(ctx, root, "Add Softpractice public checks"); err != nil {
			return err
		}
		fmt.Fprintln(output, text(ctx,
			"Конфигурация публичных проверок добавлена в проект. Теперь повторите `softpractice submit`.",
			"Public checks configuration was added to the project. Now run `softpractice submit` again."))
		return nil
	case updateLessonVersion:
		link := plan.link
		if len(plan.Operations) == 0 {
			return refreshLessonVersion(ctx, root, link, plan.ToAssignmentVersion, plan.localChecks, output)
		}
		if err := downloadAndApplyCourseUpdate(ctx, client, root, link, plan.course, plan.localChecks, plan.archivePath); err != nil {
			return err
		}
		replaced := make([]string, 0, len(plan.Operations))
		for _, operation := range plan.Operations {
			replaced = append(replaced, operation.Path)
		}
		fmt.Fprintf(output,
			text(ctx, "Урок %s обновлён: v%d → v%d. Заменены файлы урока: %s. Ваши файлы не изменены; посмотрите обновлённое задание и продолжайте.\n",
				"Lesson %s updated: v%d → v%d. Lesson files replaced: %s. Your files are unchanged; review the updated assignment and continue.\n"),
			link.AssignmentID, plan.FromAssignmentVersion, plan.ToAssignmentVersion, strings.Join(replaced, ", "))
		return nil
	case updateLessonTransition:
		if err := downloadAndApplyCourseUpdate(ctx, client, root, plan.link, plan.course, plan.localChecks, plan.archivePath); err != nil {
			return err
		}
		fmt.Fprintf(output, text(ctx, "Проект курса обновлён в этой папке: %s\n", "Course project updated in place: %s\n"), root)
		fmt.Fprintln(output, text(ctx,
			"Принятая предыдущая ревизия осталась в истории Git. Просмотрите изменения урока и продолжайте с `softpractice submit`.",
			"The accepted previous revision remains in Git history. Review the lesson changes, then continue with `softpractice submit`."))
		return nil
	default:
		return fmt.Errorf("unknown project update %q", plan.Kind)
	}
}

func updateLinkedProject(
	ctx context.Context,
	client *learnercli.Client,
	startDirectory string,
	expectedBaseRevisionID string,
	output io.Writer,
) error {
	plan, err := prepareProjectUpdate(ctx, client, startDirectory, expectedBaseRevisionID)
	if err != nil {
		return err
	}
	return applyProjectUpdate(ctx, client, plan, output)
}
