package localchecks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const RelativePath = ".softpractice/checks.json"

type Config struct {
	SchemaVersion int     `json:"schema_version"`
	Checks        []Check `json:"checks"`
}

type Check struct {
	Name               string   `json:"name"`
	Executable         string   `json:"executable"`
	Args               []string `json:"args"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
	RequireEmptyStdout bool     `json:"require_empty_stdout,omitempty"`
}

func Load(root string) (Config, error) {
	path := filepath.Join(root, filepath.FromSlash(RelativePath))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("%s is missing; run softpractice update, or download a current project release", RelativePath)
	}
	if err != nil {
		return Config{}, fmt.Errorf("inspect local checks configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Config{}, errors.New("local checks configuration must be a regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read local checks configuration: %w", err)
	}
	return Parse(body)
}

func Parse(body []byte) (Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode local checks configuration: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Config{}, errors.New("local checks configuration must contain one JSON object")
	}
	return validate(config)
}

func Marshal(config Config) ([]byte, error) {
	if _, err := validate(config); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func validate(config Config) (Config, error) {
	if config.SchemaVersion != 1 {
		return Config{}, fmt.Errorf("unsupported local checks schema_version %d; update softpractice CLI", config.SchemaVersion)
	}
	if len(config.Checks) == 0 || len(config.Checks) > 20 {
		return Config{}, errors.New("local checks configuration must contain 1 to 20 checks")
	}
	for index, check := range config.Checks {
		if strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Executable) == "" ||
			strings.ContainsAny(check.Executable, "\x00\r\n") || check.TimeoutSeconds < 1 || check.TimeoutSeconds > 1800 || len(check.Args) > 64 {
			return Config{}, fmt.Errorf("local check %d has an invalid name, executable, arguments, or timeout", index+1)
		}
		for _, argument := range check.Args {
			if strings.ContainsRune(argument, 0) {
				return Config{}, fmt.Errorf("local check %d contains an invalid argument", index+1)
			}
		}
	}
	return config, nil
}

func Run(ctx context.Context, root string, config Config, output, errorOutput io.Writer) error {
	for index, check := range config.Checks {
		fmt.Fprintf(output, "[%d/%d] %s: %s %s\n", index+1, len(config.Checks), check.Name, check.Executable, strings.Join(check.Args, " "))
		checkContext, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutSeconds)*time.Second)
		command := exec.CommandContext(checkContext, check.Executable, check.Args...)
		command.Dir = root
		fmt.Fprintf(output, "Executable: %s\n", command.Path)
		configureProcess(command)
		// Only waitProcess owns cancellation, so the parent is not killed before
		// the entire process tree can be terminated.
		command.Cancel = func() error { return nil }
		var stdout bytes.Buffer
		command.Stdout = io.MultiWriter(output, &stdout)
		command.Stderr = errorOutput
		if err := command.Start(); err != nil {
			cancel()
			if errors.Is(err, exec.ErrNotFound) {
				return fmt.Errorf("check %q cannot start: executable %q was not found; install it and retry", check.Name, check.Executable)
			}
			return fmt.Errorf("check %q cannot start: %w", check.Name, err)
		}
		err := waitProcess(checkContext, command)
		cause := checkContext.Err()
		cancel()
		if cause != nil {
			return fmt.Errorf("check %q stopped: %w", check.Name, cause)
		}
		if err != nil {
			return fmt.Errorf("check %q failed: %w", check.Name, err)
		}
		if check.RequireEmptyStdout && len(bytes.TrimSpace(stdout.Bytes())) != 0 {
			return fmt.Errorf("check %q failed: expected no output", check.Name)
		}
	}
	return nil
}
