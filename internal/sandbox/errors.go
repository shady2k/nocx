package sandbox

import (
	"errors"
	"fmt"
)

// ErrInvalidWorkspace wraps workspace validation failures. The transport
// maps it to JSON-RPC -32602 "Invalid params".
var ErrInvalidWorkspace = errors.New("sandbox: invalid workspace")

// ValidationError is a workspace validation failure with a human detail.
// The detail is safe for the wire: it never contains a filesystem path.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return "sandbox: invalid workspace: " + e.msg }
func (e *ValidationError) Unwrap() error { return ErrInvalidWorkspace }

// NewValidationErrorf builds a ValidationError.
func NewValidationErrorf(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// SetupError is returned by Service.Prepare when policy construction, the
// helper handshake, or the native launch fails. The transport maps it to
// JSON-RPC -32012 with data.reason "setup-failed".
type SetupError struct {
	msg string
}

func (e *SetupError) Error() string { return "sandbox: setup failed: " + e.msg }

// NewSetupErrorf builds a SetupError.
func NewSetupErrorf(format string, args ...any) error {
	return &SetupError{msg: fmt.Sprintf(format, args...)}
}

// StatusError carries the backend Status a launch could not proceed with.
// The transport maps it to JSON-RPC -32011 with the status reason.
type StatusError struct {
	Status Status
}

func (e *StatusError) Error() string {
	return "sandbox: backend unavailable: " + e.Status.Reason
}
