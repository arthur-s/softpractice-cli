package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

// guidanceOperations describes the exact technical files shown for approval.
// Links and instruction-shaped directories are never overwritten.
func guidanceOperations(root string, guidance *learnercli.AgentGuidance) ([]updateOperation, error) {
	operations := []updateOperation{}
	if guidance == nil {
		return operations, nil
	}
	if err := guidance.Validate(guidance.AssignmentID, guidance.AssignmentVersion); err != nil {
		return nil, err
	}
	for _, file := range guidance.Files {
		path := filepath.Join(root, file.Path)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			operations = append(operations, updateOperation{Kind: "add", Path: file.Path})
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("agent guidance destination %q must be a regular file", file.Path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(data, []byte(file.Content)) {
			operations = append(operations, updateOperation{Kind: "replace", Path: file.Path})
		}
	}
	return operations, nil
}

// stageAgentGuidance prepares a separate payload containing only root instructions.
func stageAgentGuidance(root, stage string, application *learnercli.CourseUpdate, guidance *learnercli.AgentGuidance) error {
	if guidance == nil {
		return nil
	}
	if err := guidance.Validate(guidance.AssignmentID, guidance.AssignmentVersion); err != nil {
		return err
	}
	for _, file := range guidance.Files {
		kind := "replace"
		info, err := os.Lstat(filepath.Join(root, file.Path))
		if errors.Is(err, os.ErrNotExist) {
			kind = "add"
		} else if err != nil {
			return err
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("agent guidance destination %q must be a regular file", file.Path)
		}
		stagePath := filepath.Join(stage, file.Path)
		if info, err := os.Lstat(stagePath); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("invalid staged guidance %q", file.Path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.WriteFile(stagePath, []byte(file.Content), 0o644); err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(file.Content))
		application.Files = append(application.Files, struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{file.Path, hex.EncodeToString(digest[:])})
		application.Operations = append(application.Operations, struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{kind, file.Path})
	}
	return nil
}

func installStarterGuidance(ctx context.Context, client *learnercli.Client, root, assignmentID string, version int) error {
	guidance, err := client.GetAgentGuidance(ctx, assignmentID, version)
	if err != nil {
		return fmt.Errorf("load agent guidance: %w", err)
	}
	application := learnercli.CourseUpdate{}
	stage, err := os.MkdirTemp("", "softpractice-guidance-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := stageAgentGuidance(root, stage, &application, guidance); err != nil {
		return err
	}
	return applyCourseUpdate(ctx, root, stage, application)
}

// Restore root instructions after preserving the accepted tree as its Git baseline.
func restoreAgentGuidance(ctx context.Context, client *learnercli.Client, root, assignmentID string, version int) error {
	guidance, err := client.GetAgentGuidance(ctx, assignmentID, version)
	if err != nil {
		return err
	}
	operations, err := guidanceOperations(root, guidance)
	if err != nil || len(operations) == 0 {
		return err
	}
	stage, err := os.MkdirTemp("", "softpractice-guidance-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	application := learnercli.CourseUpdate{}
	if err := stageAgentGuidance(root, stage, &application, guidance); err != nil {
		return err
	}
	if err := applyCourseUpdate(ctx, root, stage, application); err != nil {
		return err
	}
	args := []string{"-C", root, "add", "--force", "--"}
	for _, operation := range operations {
		args = append(args, operation.Path)
	}
	if output, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("stage agent guidance: %v: %s", err, output)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", root, "-c", "user.name=Softpractice", "-c", "user.email=starter@softpractice.invalid", "commit", "--no-verify", "-m", "Restore Softpractice project instructions").CombinedOutput(); err != nil {
		return fmt.Errorf("commit agent guidance: %v: %s", err, output)
	}
	return nil
}
