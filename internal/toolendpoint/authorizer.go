package toolendpoint

import (
	"errors"

	"github.com/shady2k/nocx/internal/assistant"
)

// Peer is the kernel-stamped identity assertion for one accepted local
// connection. PID is only meaningful when paired with the process start time
// by the authorizer; the endpoint never treats it as authority on its own.
type Peer struct {
	UID uint32
	PID int
	// Pane is the session whose pane this connection arrived on, as reported
	// by the helper on a connection that came in on the lane below
	// (nocx-50w7p.16). It is EMPTY for every caller that dialed this socket
	// itself, which is every local agent, and the authorizer decides what an
	// asserted pane may stand for — the endpoint only establishes that the
	// report was made by the process allowed to make it.
	Pane string
	// Token is the bearer the connection PRESENTED, as the pane's own agent
	// bridge wrote it (nocx-50w7p.16). It is a different party's claim from
	// Pane: the helper's record says which pane the connection arrived on and
	// no process on the far side can forge it, while this one says the caller
	// holds the bearer the coordinator minted for that pane — and only the
	// authorizer can decide whether the two belong together. EMPTY means
	// nothing was presented, which is a refusal rather than a default.
	Token string
}

// Lane is the identity of the one process whose connections may name a pane:
// this machine's helper daemon, as the coordinator observed it on its OWN end
// of the helper connection (nocx-50w7p.16). It is a LANE and not a session:
// the daemon is one process for every pane it serves, so "which pane" is never
// answered by this fact — that is what the pane record is for, and why both
// are needed. The lane says WHO may report a pane; the record says WHICH one
// arrived.
//
// The endpoint keeps the shape and the composition root keeps the knowledge:
// Config.Lane is a predicate over a Peer rather than a value, because deciding
// whether a pid IS the helper means reading that process's start time from
// /proc and comparing it with the pid the coordinator observed on its own end
// of the helper connection — two facts that live with whoever holds the helper
// connection, not with a socket that only ever sees peers.

// AdmissionEpoch names ONE INTERVAL OF AUTHORITY of one session (ADR-0058).
// The interval opens when a person's answer admits an agent in a session and
// ends when that answer stops holding — a withdrawal, a revocation, or the
// session itself. Zero means "no interval", which is why it is not a value any
// authorizer may mint.
//
// It exists because an interval is not a boolean. Withdrawing an enrolment and
// enrolling again produces two intervals with identical approval, and a check
// that asks only "is this session approved" answers yes for both: it would
// keep a connection admitted under the first interval alive across the
// withdrawal that ended it. Stamping each connection with the epoch it was
// admitted under is what lets the endpoint tell the two apart, and it is the
// shape a REMOTE caller joins later: the publication carries the epoch across
// whatever link the caller reached the endpoint over, and the endpoint refuses
// one that has already been retired (nocx-9mn6z).
type AdmissionEpoch uint64

// Authorizer turns an accepted peer into the already-bound invocation it may
// use. The endpoint supplies Method and RawParams for each request; the
// authorizer owns Context, RunContext, Grant and the session they imply. The
// release function closes exactly the admission interval opened by this call;
// it is nil when admission is refused and is idempotent when returned.
//
// Enrollment and the human approval are separate admission acts. Enrollment
// binds this call to a live process tree; the composition root checks the
// durable executable-and-scope approval before it supplies an invocation.
type Authorizer interface {
	// Admit decides authority for one connection and PUBLISHES the decision
	// through publish before it returns.
	//
	// publish is the endpoint's own admission record, and the authorizer calls
	// it with the session and the epoch the decision was taken under. Its
	// answer is the endpoint's: true when the connection is recorded, false
	// when the interval it names has already been retired — in which case the
	// endpoint has refused the connection, and Admit must return an error and
	// grant nothing.
	//
	// THE PUBLICATION IS PART OF THE DECISION, and that is the whole point
	// (nocx-9mn6z). Left to the endpoint after Admit returned, a withdrawal or
	// a revocation landing in the gap found nothing registered to close, and
	// the connection went on serving a grant nobody held. Called from inside
	// the decision, the endpoint records the admission in the same step that
	// takes it, so the interval's end can only be ordered before the decision
	// — where the decision reads the ended state — or after the publication —
	// where the retirement finds the connection and closes it.
	Admit(peer Peer, publish func(session string, epoch AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error)
}

// ErrSessionCallerActive means another live connection already owns the
// session's coordinator slot.
var ErrSessionCallerActive = errors.New("session already has a worker caller")

// ErrNotEnrolled means the peer is not inside a process tree enrolled for a
// live worker session.
var ErrNotEnrolled = errors.New("toolendpoint: caller is not in an enrolled process tree")
