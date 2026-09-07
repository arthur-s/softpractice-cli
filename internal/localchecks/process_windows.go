//go:build windows

package localchecks

import (
	"context"
	"fmt"
	"os/exec"
)

func configureProcess(command *exec.Cmd) {}

func waitProcess(ctx context.Context, command *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if command.Process != nil {
			// taskkill /T terminates the complete process tree. Keep Process.Kill as
			// a fallback for minimal Windows environments without taskkill.
			_ = exec.Command("taskkill", "/PID", fmt.Sprint(command.Process.Pid), "/T", "/F").Run()
			_ = command.Process.Kill()
		}
		<-done
		return ctx.Err()
	}
}
