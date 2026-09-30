package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/localchecks"
)

const autoChecksKey = "softpractice.autoChecks"

// Git-local configuration survives lesson updates and never enters submissions.
func projectAutoChecks(ctx context.Context, root string) (bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "config", "--local", "--type=bool", "--get", autoChecksKey).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("read auto-checks setting: %w", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}
func setProjectAutoChecks(ctx context.Context, value string, output io.Writer) error {
	if value != "true" && value != "false" {
		return errors.New("auto-checks must be true or false")
	}
	repo, err := inspectGitRepository(ctx, "", false)
	if err != nil {
		return err
	}
	if _, err := learnercli.LoadProjectLink(repo.Root); err != nil {
		return err
	}
	if _, err := gitOutput(ctx, repo.Root, "config", "--local", autoChecksKey, value); err != nil {
		return err
	}
	fmt.Fprintf(output, text(ctx, "Автозапуск публичных проверок: %s (только этот проект).\n", "Automatic public checks: %s (this project only).\n"), value)
	return nil
}

func checkProject(ctx context.Context, startDirectory string, args []string, output, errorOutput io.Writer) error {
	if len(args) != 0 {
		return usage(ctx, "использование: softpractice check", "usage: softpractice check")
	}
	repository, err := inspectGitRepository(ctx, startDirectory, false)
	if err != nil {
		return err
	}
	if _, err := learnercli.LoadProjectLink(repository.Root); err != nil {
		return err
	}
	checks, err := localchecks.Load(repository.Root)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, text(ctx,
		"\nЛокальные публичные проверки рабочего дерева:",
		"\nLocal public checks for the working tree:"))
	if err := localchecks.Run(ctx, repository.Root, checks, output, errorOutput); err != nil {
		return err
	}
	fmt.Fprintln(output, text(ctx,
		"Все локальные публичные проверки прошли.",
		"All local public checks passed."))
	return nil
}

func runSubmissionChecks(ctx context.Context, repository gitRepository, output, errorOutput io.Writer) error {
	snapshot, err := prepareCheckSnapshot(repository)
	if err != nil {
		return err
	}
	defer os.RemoveAll(snapshot.root)
	checks, err := localchecks.Load(snapshot.root)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, text(ctx, "\nЛокальные публичные проверки:", "\nLocal public checks:"))
	if err := localchecks.Run(ctx, snapshot.root, checks, output, errorOutput); err != nil {
		return err
	}
	if err := snapshot.verify(); err != nil {
		return err
	}
	verified, err := inspectGitRepository(ctx, repository.Root, false)
	if err != nil {
		return err
	}
	if verified.CommitSHA != repository.CommitSHA || !verified.Clean {
		return errors.New(text(ctx, "HEAD или рабочее дерево изменились во время локальных проверок; отправка остановлена", "HEAD or the working tree changed during local checks; submission stopped"))
	}
	fmt.Fprintln(output, text(ctx, "Все локальные публичные проверки прошли.", "All local public checks passed."))
	return nil
}

// errChecksNotPublished means the folder's public checks configuration is not
// the one the server publishes for its lesson: it was edited, or the folder
// holds another lesson version. Running it would run commands nobody but the
// folder vouches for.
var errChecksNotPublished = errors.New("local checks configuration differs from the one published for this lesson")

// CheckPublished runs the public checks on the working tree of the linked
// folder, like `softpractice check`, but only when .softpractice/checks.json
// is exactly the configuration the server publishes for the folder's lesson.
// Callers that cannot see what a check runs, such as an MCP client, use it.
func (u learnerUseCases) CheckPublished(ctx context.Context, output, errorOutput io.Writer) error {
	repository, link, workspace, err := linkedWorkspace(ctx, u.client, u.startDirectory, false)
	if err != nil {
		return err
	}
	checks, err := localchecks.Load(repository.Root)
	if err != nil {
		return err
	}
	published, err := localchecks.Parse(workspace.Assignment.LocalChecks)
	if err != nil || pendingTransition(link, workspace) || pendingLessonVersion(link, workspace) ||
		!reflect.DeepEqual(checks, published) {
		return fmt.Errorf("%w: %s", errChecksNotPublished, text(ctx,
			"запустите `softpractice check` в терминале, посмотрев, какие команды он выполнит, или примените обновление урока",
			"run `softpractice check` in a terminal after reviewing the commands it runs, or apply the lesson update"))
	}
	fmt.Fprintln(output, text(ctx,
		"Локальные публичные проверки рабочего дерева:",
		"Local public checks for the working tree:"))
	if err := localchecks.Run(ctx, repository.Root, checks, output, errorOutput); err != nil {
		return checkFailedError{err}
	}
	fmt.Fprintln(output, text(ctx,
		"Все локальные публичные проверки прошли.",
		"All local public checks passed."))
	return nil
}

// checkFailedError is a check that ran and did not pass, as opposed to checks
// that could not be run at all.
type checkFailedError struct{ err error }

func (e checkFailedError) Error() string { return e.err.Error() }
func (e checkFailedError) Unwrap() error { return e.err }
