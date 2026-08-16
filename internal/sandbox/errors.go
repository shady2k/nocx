package sandbox

import (
	"errors"
	"fmt"
)

// ErrInvalidPermissions identifies malformed sandbox request parameters: the
// workspace or the per-tab add/remove deltas. The transport maps it to
// JSON-RPC -32602 "Invalid params".
var ErrInvalidPermissions = errors.New("sandbox: invalid permissions")

// ValidationError is a request-parameter validation failure with a human
// detail. The detail is safe for the wire: it never contains a filesystem
// path.
type ValidationError struct {
	msg string
}

func (e *ValidationError) Error() string { return "sandbox: invalid permissions: " + e.msg }
func (e *ValidationError) Unwrap() error { return ErrInvalidPermissions }

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

// LaunchError says the fixed backend-owned launch intent cannot be fulfilled.
// Reason is a stable, path-free wire value; the transport maps it to -32012
// before any underlying diagnostic can reach the log or renderer.
type LaunchError struct {
	Reason string
}

func (e *LaunchError) Error() string {
	return "sandbox: launch unavailable: " + e.Reason
}

// StatusError carries the backend Status a launch could not proceed with.
// The transport maps it to JSON-RPC -32011 with the status reason.
type StatusError struct {
	Status Status
}

func (e *StatusError) Error() string {
	return "sandbox: backend unavailable: " + e.Status.Reason
}
