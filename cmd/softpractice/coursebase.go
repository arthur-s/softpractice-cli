package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/submission"
)

// acceptedBaseSearchDepth bounds the walk behind HEAD when naming the commit
// that carries the accepted solution. One lesson is a handful of commits, and
// every candidate costs one archive, so an unbounded walk would make a help
// message slower than the update it explains.
const acceptedBaseSearchDepth = 50

// courseUpdateMismatchError explains, in the learner's own terms, why the
// project was left unchanged: the current commit does not carry the solution
// the server accepted for the previous lesson. It names both commits and the
// exact commands that continue the lesson, because the bare statement of the
// mismatch leaves nothing to act on.
func courseUpdateMismatchError(
	ctx context.Context,
	repository gitRepository,
	update learnercli.CourseUpdate,
	staged bool,
) error {
	if staged {
		// This folder is the staging copy of `project restore`, not the
		// learner's own project. Naming its commits or offering another
		// restore would send them in a circle; the restore is simply racing a
		// workspace that moved, exactly like the base-revision guard above it.
		return errors.New(text(ctx,
			"принятая ревизия изменилась во время восстановления проекта; повторите команду",
			"the accepted revision changed while the project was being restored; run the command again"))
	}
	accepted, examined := findAcceptedBaseCommit(ctx, repository.Root, update.BaseContentSHA256)
	if accepted == "" {
		return fmt.Errorf(text(ctx,
			"проект не обновлён: этот коммит не содержит решение, принятое на уроке %s.\n\n"+
				"  Текущий HEAD: %s\n\n"+
				"Принятое решение не найдено в истории этого репозитория (проверено коммитов: %d).\n"+
				"Восстановите проект в новую папку и продолжайте работу там:\n\n"+
				"  softpractice project restore",
			"project was left unchanged: this commit does not carry the solution accepted for lesson %s.\n\n"+
				"  Current HEAD: %s\n\n"+
				"The accepted solution was not found in this repository history (commits checked: %d).\n"+
				"Restore the project into a new folder and continue there:\n\n"+
				"  softpractice project restore"),
			update.FromAssignmentID, repository.CommitSHA, examined)
	}
	return fmt.Errorf(text(ctx,
		"проект не обновлён: этот коммит не содержит решение, принятое на уроке %s.\n\n"+
			"  Принятое решение: %s\n"+
			"  Текущий HEAD:     %s\n\n"+
			"Принятый коммит есть в этом репозитории. Ничего не потеряется: текущий HEAD остаётся в нём же.\n"+
			"Перейдите на принятый коммит и повторите обновление:\n\n"+
			"  git switch -c %s %s\n"+
			"  softpractice update",
		"project was left unchanged: this commit does not carry the solution accepted for lesson %s.\n\n"+
			"  Accepted solution: %s\n"+
			"  Current HEAD:      %s\n\n"+
			"The accepted commit is in this repository. Nothing is lost: the current HEAD stays in it too.\n"+
			"Switch to the accepted commit and run the update again:\n\n"+
			"  git switch -c %s %s\n"+
			"  softpractice update"),
		update.FromAssignmentID, accepted, repository.CommitSHA, update.ToAssignmentID, accepted)
}

// findAcceptedBaseCommit reports which commit of this repository carries the
// accepted solution, or "" when the bounded search does not find one, together
// with how many commits it examined so the message can state a fact instead of
// the bound. Identity is the server-normalized archive content, never a commit
// SHA, so an amended or cherry-picked commit is still recognised by what it
// contains. Every failure answers "" on purpose: this only builds a help
// message and must never replace the mismatch it is explaining with an error
// of its own.
func findAcceptedBaseCommit(ctx context.Context, root, contentSHA256 string) (string, int) {
	if contentSHA256 == "" {
		return "", 0
	}
	// One more than the bound: the first entry is HEAD, whose content is what
	// failed to match in the first place, so it is skipped rather than paid for.
	output, err := gitOutput(ctx, root,
		"rev-list", "--max-count="+strconv.Itoa(acceptedBaseSearchDepth+1), "HEAD")
	if err != nil {
		return "", 0
	}
	commits := strings.Fields(string(output))
	if len(commits) > 0 {
		commits = commits[1:]
	}
	for index, commit := range commits {
		if ctx.Err() != nil {
			return "", index
		}
		if commitContentSHA256(ctx, root, commit) == contentSHA256 {
			return commit, index + 1
		}
	}
	return "", len(commits)
}

func commitContentSHA256(ctx context.Context, root, commit string) string {
	archive, err := os.CreateTemp("", "softpractice-accepted-base-*.tar.gz")
	if err != nil {
		return ""
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	if err := archive.Close(); err != nil {
		return ""
	}
	if err := gitArchiveCommit(ctx, root, commit, archivePath); err != nil {
		return ""
	}
	normalized, err := submission.NormalizeTarGz(archivePath, os.TempDir(), submission.DefaultArchiveLimits())
	if err != nil {
		return ""
	}
	defer os.Remove(normalized.Path)
	return normalized.ContentSHA256
}
