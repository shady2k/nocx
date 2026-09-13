package ssh

import (
	"context"
	"time"
)

// The session side of the bootstrap: the byte streams it runs on, and the
// input quarantine that owns the terminal while it does.
//
// internal/ssh knows nothing about frames. It owns the transport — a started
// session, its stdin and its stdout — and hands both to whoever does know,
// through the interfaces below. The frame protocol, the deadlines and every
// outcome name live in internal/shellintegration, and the composition root
// adapts the two declarations exactly as it does for the launcher.

// The two errors this reader can raise are internal/bootstrapstream's, not
// this package's. They cross the seam to a consumer that tests them with
// errors.Is, so there is exactly one value per fact; see that package for the
// three-sentinel defect this replaced.

// ErrInputQuarantined is returned by Write while the session is
// bootstrapping. A keystroke in that window is REFUSED, not buffered: a
// buffered keystroke is a command the user did not knowingly run, executed
// later, at a prompt they were not looking at (design §5.3).
//
// It is a distinct error rather than a silent short write because the session
// layer logs a failed input write as "the user typed into nothing", and
// telling that apart from a dead channel is the difference between a warning
// worth acting on and one that is expected.
type ErrInputQuarantined struct{}

func (e *ErrInputQuarantined) Error() string {
	return "ssh channel is bootstrapping: input refused"
}

// BootstrapStream is what the bootstrap driver sees of the session: a line
// reader with a deadline, and a writer that bypasses the quarantine.
//
// The deadline is enforced HERE, on this side of the seam, because an
// io.Reader cannot be interrupted — a blocked Read would otherwise hold the
// stream after the driver had given up, and the bytes it eventually consumed
// would be the user's.
type BootstrapStream interface {
	ReadLine(ctx context.Context, timeout time.Duration) (string, error)
	Write(p []byte) (int, error)
}

// BootstrapRun drives one session's bootstrap to a terminal outcome and
// returns why integration did not happen, or ReasonNone when it did.
type BootstrapRun func(ctx context.Context, s BootstrapStream) RefusalReason

// BootstrapGate is the ssh side of design §6.1's ordering: the two facts that
// must both be in before the far side is handed a bearer.
//
// It is declared here, and driven from here, because this package is the one
// that knows both of them — it opens the lifecycle transport and it runs the
// publish. It is CONSUMED on the other side of the seam, where the frame is
// built; the composition root adapts the two declarations exactly as it does
// for BootstrapStream and the launcher.
//
// Why the publish outcome does not cross this interface. §6.1 names four
// terminal outcomes — committed, unchanged, failed and contended — and every
// one of them opens the gate, because "after a failed publish the far side may
// still accept a generation installed earlier, so a failed publish is not a
// refusal". The far side is the owner of "is this installation valid" and
// re-proves it after the frame arrives. So what the gate needs is that the
// attempt SETTLED, and the error is carried only so the failure can be named
// in a diagnosis.
type BootstrapGate interface {
	// ReceiverReady records §6.1 step 4: the lifecycle transport and its
	// receiver are fully ready.
	ReceiverReady()
	// ReceiverUnavailable records that step 4 will never be true, so
	// nothing is minted and the far side is handed a non-secret refusal
	// rather than a bearer it has no channel to use.
	ReceiverUnavailable(err error)
	// PublishSettled records §6.1 step 5: the publish attempt reached a
	// terminal outcome. err is nil when it committed and non-nil otherwise,
	// and either way the gate opens.
	PublishSettled(err error)
}
