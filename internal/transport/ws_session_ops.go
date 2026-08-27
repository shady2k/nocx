package transport

// Per-session resize lane with a terminal close gate (worker G brief).
//
// resize is the only control method the renderer sends fire-and-forget, and
// on an SSH session it is a window-change round trip over the network that
// can block on a dead transport. The two halves of the defect forbid both
// obvious implementations:
//
//   - running resize inline on the read loop lets a dead channel freeze
//     every tab on the connection — the stall defect this lane replaces;
//   - serializing close behind resize recreates head-of-line blocking one
//     level down: a dead resize would own the session's lane and the one
//     operation that could tear the dead channel down would never run.
//
// The lane is per session, never per connection: the same physical PTY is
// resized once, whatever connection asks (the session:<id> conflict grain of
// the control contract), and the close gate is therefore session-wide.
//
// Invariants, stated as intervals with both ends:
//
//  1. Coalescing. From the moment a resize is enqueued until the moment the
//     worker applies a resize whose dimensions are at least as new (the
//     enqueued one, or a superseding one), at most one resize is in flight
//     and the pending slot holds at most the latest. An enqueue REPLACES the
//     pending op and completes the replaced request immediately — its
//     dimensions are subsumed, and the terminal settles on the latest truth.
//     The renderer never reads resize responses, so a subsumed request's
//     completion is admission, not application; what a user observes is the
//     PTY ending at the last dimensions.
//  2. Close terminality. From the moment admitClose returns (the lane's
//     closed flag set and its context cancelled) until the worker's exit,
//     no enqueue succeeds, the pending op is dropped (its request completed
//     as admitted), and any in-flight sess.Resize runs against a cancelled
//     context — a conforming Channel.Resize performs no work and returns
//     promptly. The closing event of the interval is the worker's exit after
//     the cancelled resize returns.
//  3. Close never waits. admitClose holds the lane mutex only to flip state,
//     drop the pending op and cancel the context; the worker never holds the
//     mutex across sess.Resize. closeSession, the only thing that can tear
//     down a dead channel, therefore never queues behind a blocked resize.
//
// Map lifecycle: entries are never deleted. A lane that outlives its session
// is a tombstone — closed, sess nil, no worker — and it is the single
// authority that refuses every later resize for that session, from ANY
// connection. Deleting it would reopen the race the gate exists to close: a
// resize from a second connection that passed state.has and registry.Get
// before the close could then create a fresh open lane for a session the
// close already removed. Tombstones are a few words each and are bounded by
// the number of sessions ever resized or closed in the server's lifetime.

import (
	"context"
	"sync"

	"github.com/shady2k/nocx/internal/session"
)

// resizeOp is one admitted resize request. done completes the JSON-RPC
// request exactly once, on whichever goroutine settles the op (the read loop
// for a superseded op, the lane worker for an applied or cancelled one).
type resizeOp struct {
	cols, rows, xpixel, ypixel uint16
	done                       func(err error) // err nil = success
}

// sessionLane serializes one session's resize work behind a terminal close
// gate. See the package comment for the invariants and the map lifecycle.
type sessionLane struct {
	// closed is set once, under mu, by admitClose — before any other
	// mutation — so enqueue refuses from that instant.
	closed bool
	// ctx is the lane's cancellation scope, derived from Background because
	// the session outlives its connection (AD-9). admitClose cancels it:
	// the in-flight resize's Channel.Resize must observe it (that is the
	// point of the context parameter) and return promptly.
	ctx    context.Context
	cancel context.CancelFunc
	// wake carries one token per pending resize, so the worker parked on it
	// cannot miss work enqueued just before it parked.
	wake chan struct{}
	// startOnce launches the worker on the first successful enqueue. A lane
	// that is only ever closed (a tombstone) never starts one.
	startOnce sync.Once

	// resized is called after a resize the session accepted, with the size it
	// accepted. It exists so the pane's backend grid can follow the pane's
	// geometry (nocx-szb40.5): both powers the AD-6 amendment grants a grid
	// are POSITIONAL, so a grid left at the size it was enrolled at answers
	// about a screen that never existed. It hangs off the APPLY rather than
	// off the request because resizes coalesce — a lane that dropped three
	// superseded sizes must tell the grid the one that landed, not all four.
	// nil for a tombstone lane, which applies nothing.
	resized func(cols, rows uint16)

	mu      sync.Mutex
	sess    session.Session // nil once close is admitted
	pending *resizeOp       // latest unapplied resize; nil when idle
}

func newSessionLane(sess session.Session, resized func(cols, rows uint16)) *sessionLane {
	// Lane-owned lifetime: the lane context is the session's own and is
	// deliberately derived from Background — its owner is the per-session
	// resize lane, and its closing event is closeLane's admission (see the
	// package comment's invariant 2: the worker's exit after the cancelled
	// resize returns). It must NOT die with any one connection: a session
	// is resized by whichever connection is attached (AD-9).
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionLane{
		ctx:     ctx,
		cancel:  cancel,
		wake:    make(chan struct{}, 1),
		sess:    sess,
		resized: resized,
	}
}

// enqueue coalesces op into the lane. It reports false when the lane is
// closed (close admitted): the op is not enqueued and the caller must refuse
// the request itself. The superseded op, if any, is completed here — its
// dimensions are subsumed by the newer request.
func (l *sessionLane) enqueue(op *resizeOp) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	if l.pending != nil {
		l.pending.done(nil)
	}
	l.pending = op
	l.startOnce.Do(func() { go l.worker() })
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return true
}

// admitClose makes the close terminal for this lane: future enqueues are
// refused, the pending op is dropped (answered as admitted — the session is
// closing, so the terminal will not settle on those dimensions), and the
// lane context is cancelled so an in-flight resize aborts. It never waits
// for the worker. Idempotent: the second admission of a tombstone does
// nothing.
func (l *sessionLane) admitClose() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	if l.pending != nil {
		l.pending.done(nil)
		l.pending = nil
	}
	l.sess = nil
	l.cancel()
}

// worker applies pending resizes one at a time, in order, coalescing through
// the single pending slot. It exits when the close gate is admitted.
func (l *sessionLane) worker() {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return
		}
		op := l.pending
		if op == nil {
			l.mu.Unlock()
			select {
			case <-l.wake:
				continue
			case <-l.ctx.Done():
				return
			}
		}
		l.pending = nil
		sess := l.sess
		l.mu.Unlock()

		l.apply(sess, op)
	}
}

// apply runs one resize against the session with the lane's context. The
// session reference is read under mu at claim time, so admitClose may nil it
// out from under a running apply without a race; the local copy keeps the
// session alive until the resize returns.
func (l *sessionLane) apply(sess session.Session, op *resizeOp) {
	err := sess.Resize(l.ctx, op.cols, op.rows, op.xpixel, op.ypixel)
	if err == nil && l.resized != nil {
		l.resized(op.cols, op.rows)
	}

	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		// Close was admitted while the resize was in flight: the
		// cancellation is the terminal outcome. The request is answered as
		// admitted — the session is gone, there is no failure to report.
		op.done(nil)
		return
	}
	op.done(err)
}

// laneFor returns the session's operation lane, creating it on first use
// with the given session. Callers reach it only after a successful
// registry.Get, so a lane is never created for a session the registry does
// not hold; entries are never deleted (see the package comment).
func (s *WSServer) laneFor(sid session.ID, sess session.Session) *sessionLane {
	s.lanesMu.Lock()
	defer s.lanesMu.Unlock()
	if l, ok := s.lanes[sid]; ok {
		return l
	}
	l := newSessionLane(sess, func(cols, rows uint16) { s.resizePaneGrid(sid, cols, rows) })
	s.lanes[sid] = l
	return l
}

// closeLane admits the terminal close for the session's lane, creating the
// tombstone when none exists. Creating it matters: a close must not leave a
// window in which a resize from another connection — one that passed
// state.has and registry.Get before the close — can mint a fresh open lane
// for a session the close is removing. A tombstone refuses every enqueue,
// so closeLane MUST run before the registry entry is dropped (it is called
// at the top of every teardown path). Idempotent.
func (s *WSServer) closeLane(sid session.ID) {
	s.lanesMu.Lock()
	l, ok := s.lanes[sid]
	if !ok {
		l = newSessionLane(nil, nil)
		s.lanes[sid] = l
	}
	s.lanesMu.Unlock()
	l.admitClose()
}
