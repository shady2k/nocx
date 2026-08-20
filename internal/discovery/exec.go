package discovery

// The exec seam — what the ladder runs probes through, transport-neutral.
//
// The Detector never names a transport: it samples through ExecConn, and
// the classification of a failed exec (ExecError) is a discovery fact, not
// an SSH fact. The sshAdapter wraps an owned lease on a pooled SSH
// connection (via internal/ssh); the local machine does not use this seam
// at all — its listeners come from the kernel through internal/nativeports
// (see provider.go). The same ladder, the same five result states and the
// same three-valued process evidence describe the remote host.
import (
	"context"
)

// ExecResult is one auxiliary command's outcome: the captured stdout and
// stderr, the exit status, and whether a capture bound was hit (Truncated —
// the output is not complete, so a partial table must not surface as "no
// ports"). The shape mirrors the ssh lease's result so the adapter is a
// field-for-field copy.
type ExecResult struct {
	Stdout     []byte
	Stderr     []byte
	ExitStatus int
	Truncated  bool
}

// ExecErrorKind classifies an exec failure without naming the transport.
// "Why did the command not run" is a discovery fact: a refused session and
// a lost connection map to different result states, and both differ from a
// tool that simply is not installed.
type ExecErrorKind int

const (
	// ExecErrOther is an exec failure with no transport-specific meaning.
	ExecErrOther ExecErrorKind = iota
	// ExecErrSessionRefused: the target refused the extra command channel
	// (an SSH server at MaxSessions, or policy). Terminal until Retry.
	ExecErrSessionRefused
	// ExecErrExecProhibited: the target refused the exec request itself
	// (restricted shell, forced-command-style policy). Terminal until Retry.
	ExecErrExecProhibited
	// ExecErrConnectionLost: the transport died during the exec.
	ExecErrConnectionLost
	// ExecErrLeaseClosed: the exec surface was closed while the command was
	// in flight — the caller discarded the sample, never a host fact.
	ExecErrLeaseClosed
	// ExecErrCommandTooLong: NOCX refused the command, before sending it,
	// because it is at or above the bound internal/ssh declares for a remote
	// command (nocx-e4ir3). It is the one kind here that is not a fact about
	// the host at all, and it is terminal for the reason the others are not:
	// the command is exactly as long on every retry, so a transient
	// classification would schedule a backoff for a probe that can never
	// succeed.
	ExecErrCommandTooLong
)

func (k ExecErrorKind) String() string {
	switch k {
	case ExecErrSessionRefused:
		return "session refused"
	case ExecErrExecProhibited:
		return "exec prohibited"
	case ExecErrConnectionLost:
		return "connection lost"
	case ExecErrLeaseClosed:
		return "lease closed"
	case ExecErrCommandTooLong:
		return "command refused by nocx: longer than the bound"
	default:
		return "exec failed"
	}
}

// ExecError is a classified exec failure. Kind is the transport-neutral
// fact; Err carries the underlying error (a sentinel or the raw cause) so
// errors.Is/errors.As still reach it.
type ExecError struct {
	Kind ExecErrorKind
	Err  error
}

func (e *ExecError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "discovery: " + e.Kind.String()
}

func (e *ExecError) Unwrap() error { return e.Err }

// ExecConn is the exec seam: one auxiliary command channel on a target.
// Exec runs cmd and returns the captured outcome; Close releases whatever
// the seam holds and stops any exec still in flight (an in-flight Exec
// returns ExecErrLeaseClosed).
type ExecConn interface {
	Exec(ctx context.Context, cmd string) (*ExecResult, error)
	Close() error
}
