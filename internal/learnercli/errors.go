package learnercli

import "errors"

// ErrLoginRequired marks every failure that only a new `softpractice login`
// resolves. Callers that must not start the device flow themselves, such as a
// command run by an agent, use it to report the state instead.
var ErrLoginRequired = errors.New("login required")

// ErrProjectNotLinked marks a directory that holds no SoftPractice project
// link, so there is no workspace to read or submit to.
var ErrProjectNotLinked = errors.New("project not linked")

// markedError keeps the original message and adds a sentinel for errors.Is.
type markedError struct {
	message string
	mark    error
}

func (e markedError) Error() string { return e.message }

func (e markedError) Is(target error) bool { return target == e.mark }

func loginRequired(message string) error {
	return markedError{message: message, mark: ErrLoginRequired}
}

func projectNotLinked(message string) error {
	return markedError{message: message, mark: ErrProjectNotLinked}
}
