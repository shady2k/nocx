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
}

// Authorizer turns an accepted peer into the already-bound invocation it may
// use. The endpoint supplies Method and RawParams for each request; the
// authorizer owns Context, RunContext, Grant and the session they imply. The
// release function closes exactly the admission interval opened by this call;
// it is nil when admission is refused and is idempotent when returned.
//
// Enrollment is the admission act here, not D13 human approval. That
// product-level approval remains deliberately out of this endpoint
// (nocx-rowqt.12); the authority ceiling is the existing A12 session grant.
type Authorizer interface {
	Admit(Peer) (assistant.ToolInvocation, func(), error)
}

// ErrSessionCallerActive means another live connection already owns the
// session's coordinator slot.
var ErrSessionCallerActive = errors.New("session already has a worker caller")

// ErrNotEnrolled means the peer is not inside a process tree enrolled for a
// live worker session.
var ErrNotEnrolled = errors.New("toolendpoint: caller is not in an enrolled process tree")
