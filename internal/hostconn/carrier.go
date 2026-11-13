package hostconn

// The seam a connection is made through (design §5.1.2).
//
// A Carrier is one implementation of an SSH transport — the x/crypto client
// this helper links today, and whatever replaces it — and everything the
// machine and the supervisor say about a connection is said without knowing
// which one is behind them. That is the whole reason this package imports
// neither internal/ssh nor x/crypto: a state machine that could reach the
// library would grow its own opinion about the library's errors, and the next
// transport would find that opinion baked into the policy.

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/monoclock"
)

// Capabilities says what a carrier can do, so that no surface has to test a
// carrier's NAME to find out (design §5.1.2). A carrier that reports no
// liveness never reads stalled: it is honest about what it cannot measure
// rather than silent about a probe it never runs.
type Capabilities struct {
	// Stages reports whether the carrier calls the sink's StageReached while
	// it opens, so a person can be shown where a dial is.
	Stages bool
	// Liveness reports whether the carrier can answer a probe.
	Liveness bool
	// Multiplex reports whether one connection carries several channels.
	Multiplex bool
}

// Carrier is one transport, as the supervisor uses it.
type Carrier interface {
	Name() string
	Capabilities() Capabilities
	// Dial opens one connection and reports its stages through the sink; it
	// returns once the host is usable, however long that takes. A refusal a
	// retry cannot fix is reported to the sink as Refused AND returned: the
	// state is what a person is shown, and the error is what the caller acts
	// on.
	Dial(ctx context.Context, plan Plan, sink EventSink) (Link, error)
}

// EventSink receives what a carrier learns while it dials. It is used during
// Dial only, and it is the carrier's only way to say anything about an
// attempt: the supervisor owns the state, so a carrier that wrote it directly
// would be a second owner of the same fact.
type EventSink func(Event)

// Link is one live connection.
//
// It has no OpenShell yet, deliberately. The shell's channel semantics — pty,
// environment, signals, an exit cause — move behind this seam with the open
// order (design §5.2, stage 8); a method here today would be a promise the
// carrier does not keep, so the shell keeps acquiring through the pool whose
// reference this Link holds. What the seam owes NOW is the one thing the
// supervisor cannot get any other way: the transport's end.
type Link interface {
	// Done closes when the transport has ended, whoever ended it. It is the
	// evidence the supervisor watches: nothing else in the process learns that
	// x/crypto's client died until somebody writes to it.
	Done() <-chan struct{}
	// Err is why the transport ended, and is meaningful only after Done is
	// closed.
	Err() error
	// Close ends the connection and releases what holds it. The last holder's
	// release is its ordinary caller; the supervisor calls it too, the moment
	// Done is observed, so the connection leaves the table at once rather than
	// when the last holder happens to unwind.
	Close() error
}

// Holds is what a holder declares about the connection it acquires. It exists
// because silence must never end a shell that would die with the connection
// (design §5.3), and the supervisor cannot ask who holds a shell without
// importing the package that knows (helper/session, whose import of sshsvc
// would close a cycle): so the holder says so itself, at the moment it takes
// its reference.
type Holds uint8

const (
	// HoldsNone is a channel that loses nothing a reattach could not bring
	// back: an sftp stream, a lane, a tunnel, a probe lease.
	HoldsNone Holds = 0
	// HoldsPlainShell is the person's terminal — the shell the far side runs
	// for them. Its death loses work.
	HoldsPlainShell Holds = 1 << 0
)

// Observation is what one probe found: whether the far side answered and how
// long it took. It is the supervisor's own vocabulary, not x/crypto's, so a
// carrier that cannot measure liveness simply never reports one.
type Observation struct {
	Answered  bool
	RoundTrip time.Duration
}

// WakeSource reports that the machine has woken from sleep.
//
// The interface is the seam the design asks for (§5.3, "On a wake signal"): the
// supervisor says "prove yourself now" and knows nothing about how the wake was
// seen. The helper's implementation is internal/wakesource — the two kernel
// clocks on macOS and Linux — and a test source is the fault harness's.
type WakeSource interface {
	// Wakes answers the channel that receives once per wake, carrying the
	// reading of the supervisor's own clock at the moment the wake was SEEN.
	//
	// The moment travels with the signal and is not taken when the supervisor
	// gets to it: a fan-out that is a moment late must still measure the
	// policy's five seconds from the signal, or a slow scheduler would buy a
	// connection extra time it has not earned. The reading is monoclock's, the
	// clock every supervisor interval is measured on.
	//
	// The channel is the source's to own and is never closed while the process
	// runs; a source with nothing to report answers a channel that never
	// receives.
	Wakes() <-chan monoclock.Nanos
}

// SleepSource is what a wake source offers when the operating system also says
// that the machine is GOING to sleep. It is optional: a source that cannot see
// the edge simply never sends one, and the wake signal is then the only news.
//
// The supervisor uses it to stop asking its connections for answers before the
// machine freezes. Nothing will be heard while it is away, so a probe sent into
// the sleep would be the first thing to answer on resume, and the silence it
// spent parked is not evidence of anything.
type SleepSource interface {
	// Sleeps answers the channel that receives once when the machine is about
	// to sleep, with the clock reading at which it was said.
	Sleeps() <-chan monoclock.Nanos
}

// Prober is what a carrier offers when it can ask the far side a question.
// Optional, found by capability: a carrier without it never reads stalled.
type Prober interface {
	// Probe wants an answer, and reports how long it took.
	Probe(ctx context.Context) (time.Duration, error)
	// Nudge sends bytes without wanting an answer (§5.3): it does not take
	// the lock a parked probe holds, so it is the one request that can reach
	// a connection whose probe is still waiting.
	Nudge() error
}

// Looker is the look-only dial (§5.3): TCP and the SSH version exchange on a
// new connection, no key exchange and no sign-in. It answers whether the host
// accepts new connections, which is evidence and never a verdict.
type Looker interface {
	Look(ctx context.Context) error
}
