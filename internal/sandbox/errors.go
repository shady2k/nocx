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
// JSON-RPC -32007; Reason, when set, is the wire-safe discriminator that
// reaches data.reason instead of the generic "setup-failed".
//
// Reason exists because the generic answer is unactionable in the one case a
// user can do something about: a machine whose derived policy does not fit
// the bounds. It is a fixed token from the list below, never a message and
// never a path.
type SetupError struct {
	msg    string
	Reason string
}

func (e *SetupError) Error() string { return "sandbox: setup failed: " + e.msg }

// NewSetupErrorf builds a SetupError with the generic reason.
func NewSetupErrorf(format string, args ...any) error {
	return &SetupError{msg: fmt.Sprintf(format, args...)}
}

// NewSetupErrorReasonf builds a SetupError carrying a typed reason.
func NewSetupErrorReasonf(reason, format string, args ...any) error {
	return &SetupError{msg: fmt.Sprintf(format, args...), Reason: reason}
}

// ErrPolicyTooLarge marks the two bounds a MACHINE can breach without the
// user having asked for anything unusual: the derived root count and the
// serialized policy size. It is separated from every other setup failure so
// the renderer can say which one happened.
var ErrPolicyTooLarge = errors.New("sandbox: policy exceeds its bounds")

// StatusError carries the backend Status a launch could not proceed with.
// The transport maps it to JSON-RPC -32006 with the status reason.
type StatusError struct {
	Status Status
}

func (e *StatusError) Error() string {
	return "sandbox: backend unavailable: " + e.Status.Reason
}
