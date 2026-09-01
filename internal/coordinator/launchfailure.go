package coordinator

import (
	"errors"
	"fmt"
)

// FailureKind names why a launch cannot succeed by being repeated as-is.
type FailureKind string

const (
	FailureProfileUnusable      FailureKind = "profile-unusable"
	FailureServerBinaryUnusable FailureKind = "server-binary-unusable"
	FailureIncompatible         FailureKind = "incompatible-coordinator"
	FailureNotReady             FailureKind = "not-ready"
)

// LaunchFailure is what a person is told, not what a log records.
//
// Message and Remedy are deliberately separate from Cause. The first two are
// stable, human-readable text for the UI; Cause retains the diagnostic detail
// for logs and errors.Is/errors.As callers.
type LaunchFailure struct {
	Kind    FailureKind
	Message string
	Remedy  string
	Cause   error
}

// NewLaunchFailure constructs a classified launch failure. Callers at the
// composition root use it for failures discovered before [Launcher.Launch]
// can run, while the launcher uses the same type for its own failures.
func NewLaunchFailure(kind FailureKind, message, remedy string, cause error) *LaunchFailure {
	return &LaunchFailure{
		Kind:    kind,
		Message: message,
		Remedy:  remedy,
		Cause:   cause,
	}
}

// Error implements error with the person-readable message and, when present,
// the diagnostic cause for existing logging callers. UI code should render
// [LaunchFailure.Message] and [LaunchFailure.Remedy] separately.
func (f *LaunchFailure) Error() string {
	if f == nil {
		return ""
	}
	if f.Cause == nil {
		return f.Message
	}
	return fmt.Sprintf("%s: %v", f.Message, f.Cause)
}

// Unwrap exposes the diagnostic cause without adding it to the rendered text.
func (f *LaunchFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// AsLaunchFailure reports whether err carries a classified failure.
func AsLaunchFailure(err error) (*LaunchFailure, bool) {
	var failure *LaunchFailure
	if !errors.As(err, &failure) || failure == nil {
		return nil, false
	}
	return failure, true
}
