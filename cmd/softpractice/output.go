package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/arthur-s/softpractice-cli/internal/learnercli"
)

// exitStatus ends the process with its code after the command has already
// written its result, so main prints nothing more.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// Exit codes shared by every command. 0 is success and 1 is any other error.
const (
	// exitUsage: the arguments are invalid. main uses it for usageError.
	exitUsage exitStatus = 2
	// exitNotReady: the evaluation is still queued or running.
	exitNotReady exitStatus = 3
	// exitSuperseded: the evaluation was superseded and has no result.
	exitSuperseded exitStatus = 4
)

// usageError is an invalid command line. main prints it and exits with
// exitUsage.
type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

func usage(ctx context.Context, russian, english string) error {
	return usageError{message: text(ctx, russian, english)}
}

// parseFlags parses args and reports a malformed flag as a usageError. The
// flag package has already printed the flag's own help by then.
func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{message: err.Error()}
	}
	return nil
}

func jsonFlag(flags *flag.FlagSet) *bool {
	return flags.Bool("json", false, "print JSON instead of text")
}

func writeMachineJSON(output io.Writer, payload any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

// machineError is written to standard output in JSON mode, so a caller that
// reads only stdout still learns why the command failed. The same message
// also goes to standard error.
type machineError struct {
	Kind  string `json:"kind"`
	Error struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		HTTPStatus int    `json:"http_status,omitempty"`
		APICode    string `json:"api_code,omitempty"`
		SupportID  string `json:"support_id,omitempty"`
	} `json:"error"`
}

// errorCode classifies an error for machine output. login_required and
// project_not_linked tell the caller that a person has to act: sign in with
// `softpractice login`, or work inside the linked project.
func errorCode(err error) string {
	var statusError *learnercli.HTTPError
	var usageErr usageError
	switch {
	case errors.As(err, &usageErr):
		return "usage"
	case errors.Is(err, learnercli.ErrLoginRequired):
		return "login_required"
	case errors.As(err, &statusError) && statusError.Status == http.StatusUnauthorized:
		return "login_required"
	case errors.Is(err, learnercli.ErrProjectNotLinked):
		return "project_not_linked"
	case errors.Is(err, errNotGitRepository):
		return "not_git_repository"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	case errors.As(err, &statusError):
		return "api_error"
	default:
		return "error"
	}
}

func writeMachineError(output io.Writer, err error) error {
	payload := machineError{Kind: "softpractice.error"}
	payload.Error.Code = errorCode(err)
	payload.Error.Message = err.Error()
	var statusError *learnercli.HTTPError
	if errors.As(err, &statusError) {
		payload.Error.HTTPStatus = statusError.Status
		payload.Error.APICode = statusError.Code
		payload.Error.SupportID = statusError.SupportID
	}
	return writeMachineJSON(output, payload)
}

// reportJSONError writes err as a softpractice.error document when the
// command runs with --json. An exit status is a result, not an error, and a
// request for help is neither.
func reportJSONError(jsonOutput bool, output io.Writer, err error) error {
	var status exitStatus
	if jsonOutput && err != nil && !errors.As(err, &status) && !errors.Is(err, flag.ErrHelp) {
		_ = writeMachineError(output, err)
	}
	return err
}
