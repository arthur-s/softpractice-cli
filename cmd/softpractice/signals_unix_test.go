//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/arthur-s/softpractice-cli/internal/localchecks"
)

func TestInterruptStopsPublicCheckTree(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPublicCheckSignalHelper$", "--", "runner")
	cmd.Env = append(os.Environ(), "SOFTPRACTICE_SIGNAL_TEST="+root)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	heartbeat := filepath.Join(root, "heartbeat")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("check tree did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Kill the check group on test failure as well, without touching other processes.
	pidBytes, err := os.ReadFile(filepath.Join(root, "check.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-pid, syscall.SIGKILL)
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("signal-aware CLI context failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not finish cancellation")
	}
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("public check descendant survived Ctrl+C")
	}
}

func TestPublicCheckSignalHelper(t *testing.T) {
	root := os.Getenv("SOFTPRACTICE_SIGNAL_TEST")
	if root == "" {
		t.Skip("subprocess helper")
	}
	switch os.Args[len(os.Args)-1] {
	case "runner":
		ctx, stop := commandContext()
		defer stop()
		err := localchecks.Run(ctx, root, localchecks.Config{Checks: []localchecks.Check{{Name: "process tree", Executable: os.Args[0], Args: []string{"-test.run=^TestPublicCheckSignalHelper$", "--", "child"}, TimeoutSeconds: 20}}}, os.Stdout, os.Stderr)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
	case "child":
		if err := os.WriteFile(filepath.Join(root, "check.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestPublicCheckSignalHelper$", "--", "leaf")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	case "leaf":
		for {
			if err := os.WriteFile(filepath.Join(root, "heartbeat"), []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), 0600); err != nil {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		t.Fatal("unknown helper role")
	}
}
