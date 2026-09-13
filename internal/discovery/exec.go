package discovery

// The probe seam — what the ladder samples through, transport-neutral.
//
// The Detector never names a transport: it samples through ExecConn, and the
// classification of a failed probe (ExecError) is a discovery fact, not an SSH
// fact. What runs the command is now this machine's HELPER (the owner's
// invariant: there is no ssh connection without one), so the seam names the
// PROBE and never the command: the text of every rung lives in
// internal/remoteprobe, which the helper links too, and a command that crossed
// this boundary would be the free-form exec D3 refuses.
//
// The local machine does not use this seam at all — its listeners come from the
// kernel through internal/nativeports (see provider.go). The same ladder, the
// same five result states and the same three-valued process evidence describe
// the remote host.
import (
	"context"

	"github.com/shady2k/nocx/internal/remoteprobe"
)

// ProbeName is one rung of the ladder, in the helper's own vocabulary. It is a
// NAME and not a command: the caller chooses which dialect to run and the
// helper owns what that dialect is.
//
// The type is remoteprobe's rather than a second set of strings here, and that
// is AD-8 rather than convenience: "which probe is this" is the same fact on
// both sides of the wire, so it has one declaration.
type ProbeName = remoteprobe.PortProbe

// The ladder's rungs, as the helper's vocabulary spells them.
const (
	ProbeSS             = remoteprobe.PortSS
	ProbeNetstat        = remoteprobe.PortNetstat
	ProbeBusyboxNetstat = remoteprobe.PortBusyboxNetstat
	ProbeLsof           = remoteprobe.PortLsof
	ProbeSockstat       = remoteprobe.PortSockstat
)

// ExecResult is one probe's outcome: the captured stdout and stderr, the exit
// status, and whether a capture bound was hit (Truncated — the output is not
// complete, so a partial table must not surface as "no ports").
//
// It is remoteprobe.Result, declared once for the reason ProbeName is: the
// helper describes what it captured and this package reads it, and two structs
// with the same four fields would be the pair that drifts on the day one gains
// a field.
type ExecResult = remoteprobe.Result

// ExecErrorKind classifies a probe failure without naming the transport. "Why
// did the command not run" is a discovery fact: a refused session and a lost
// connection map to different result states, and both differ from a tool that
// simply is not installed.
//
// The kinds are remoteprobe's own, for the reason ProbeName is: the helper
// classifies the failure where it happens and this package switches on the
// classification, so one vocabulary carries it across.
type ExecErrorKind = remoteprobe.Kind

const (
	// ExecErrSessionRefused: the target refused the extra command channel (an
	// SSH server at MaxSessions, or policy). Terminal until Retry.
	ExecErrSessionRefused = remoteprobe.KindSessionRefused
	// ExecErrExecProhibited: the target refused the exec request itself
	// (restricted shell, forced-command-style policy). Terminal until Retry.
	ExecErrExecProhibited = remoteprobe.KindExecProhibited
	// ExecErrConnectionLost: the transport died during the probe.
	ExecErrConnectionLost = remoteprobe.KindConnectionLost
	// ExecErrLeaseClosed: the exec surface was closed while the command was in
	// flight — the caller discarded the sample, never a host fact.
	ExecErrLeaseClosed = remoteprobe.KindLeaseClosed
	// ExecErrCommandTooLong: NOCX refused the command before sending it,
	// because it is at or above the bound the probe commands are held to
	// (remoteprobe.MaxCommandLen). It is the one kind here that is not a fact
	// about the host at all, and it is terminal for the reason the others are
	// not: the command is exactly as long on every retry, so a transient
	// classification would schedule a backoff for a probe that can never
	// succeed.
	ExecErrCommandTooLong = remoteprobe.KindCommandTooLong
)

// ExecError is a classified probe failure. Kind is the transport-neutral fact;
// Err carries the underlying error (a sentinel or the raw cause) so
// errors.Is/errors.As still reach it.
type ExecError = remoteprobe.Error

// ExecConn is the probe seam: one auxiliary command channel on a target. Sample
// runs one NAMED rung and returns the captured outcome; Close releases whatever
// the seam holds and stops any probe still in flight (an in-flight Sample
// returns ExecErrLeaseClosed).
type ExecConn interface {
	Sample(ctx context.Context, probe ProbeName) (*ExecResult, error)
	Close() error
}

// Lease is an ExecConn that also reports the transport's death, which the
// scheduler watches: a sampler that kept asking a dead connection would record
// a dead host where a tab merely died. It is a separate interface and not part
// of ExecConn because the Detector has no business watching anything — it
// samples, and the party that owns the target's lifetime decides what a loss
// means.
type Lease interface {
	ExecConn
	// Done closes when the connection under this lease is gone.
	Done() <-chan struct{}
	// LostErr reports why, once Done has closed. Nil while it is live.
	LostErr() error
}
