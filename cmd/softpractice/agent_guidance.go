package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

// installMissingAgentGuidance is the compatibility path for published starters
// and restored revisions without root instructions. New starters carry both
// files in their verified archive and never request the separate endpoint.
// Existing instructions belong to the project and are preserved.
func installMissingAgentGuidance(ctx context.Context, client *learnercli.Client, root, assignmentID string, version int) ([]string, error) {
	missing := make(map[string]bool)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			missing[name] = true
		} else if err != nil {
			return nil, err
		} else if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("agent guidance destination %q must be a regular file", name)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	guidance, err := client.GetAgentGuidance(ctx, assignmentID, version)
	if err != nil {
		return nil, fmt.Errorf("load legacy project guidance: %w", err)
	}
	if guidance == nil {
		return nil, nil
	}
	var installed []string
	for _, instruction := range guidance.Files {
		if !missing[instruction.Path] {
			continue
		}
		file, err := os.OpenFile(filepath.Join(root, instruction.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return nil, err
		}
		_, writeErr := file.WriteString(instruction.Content)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return nil, err
		}
		installed = append(installed, instruction.Path)
	}
	return installed, nil
}

func installStarterGuidance(ctx context.Context, client *learnercli.Client, root, assignmentID string, version int) error {
	_, err := installMissingAgentGuidance(ctx, client, root, assignmentID, version)
	return err
}

// Restore missing instructions after preserving the accepted Git baseline.
func restoreAgentGuidance(ctx context.Context, client *learnercli.Client, root, assignmentID string, version int) error {
	installed, err := installMissingAgentGuidance(ctx, client, root, assignmentID, version)
	if err != nil || len(installed) == 0 {
		return err
	}
	args := append([]string{"-C", root, "add", "--force", "--"}, installed...)
	if output, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("stage agent guidance: %v: %s", err, output)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", root, "-c", "user.name=Softpractice", "-c", "user.email=starter@softpractice.invalid", "commit", "--no-verify", "-m", "Restore Softpractice project instructions").CombinedOutput(); err != nil {
		return fmt.Errorf("commit agent guidance: %v: %s", err, output)
	}
	return nil
}
