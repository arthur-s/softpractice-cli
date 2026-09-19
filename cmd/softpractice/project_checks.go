package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/arthur-s/softpractice-cli/internal/learnercli"
	"github.com/arthur-s/softpractice-cli/internal/localchecks"
	"io"
	"os"
	"os/exec"
	"strings"
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
