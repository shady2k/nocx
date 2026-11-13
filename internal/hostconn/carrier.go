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
