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
//  4. A claimed resize always has an answer to "which session". From the
//     moment the worker claims an op out of the pending slot until that op is
//     settled, the lane holds either a usable session or the decision not to
//     use one — never the absence of both. The claim reads the session under
//     the same mu hold that reads the closed flag, so the pair cannot be
//     torn; the open half of the interval is the constructor's, and it is why
//     a tombstone is born closed rather than closed a moment after it is
//     published (newClosedLane); the closing half is apply's explicit nil
//     branch, which abandons the resize instead of dereferencing.
//
// Map lifecycle: entries are never deleted. A lane that outlives its session
// is a tombstone — closed, sess nil, no worker — and it is the single
// authority that refuses every later resize for that session, from ANY
// connection. It is CLOSED BEFORE IT IS PUBLISHED: a tombstone that reached
// the map open would be a lane holding no session that still accepts work,
// and the worker started by that enqueue dereferenced a nil session and took
// the whole backend down (nocx-44349).
//
// Deleting an entry would reopen the race the gate exists to close: a resize
// from a second connection that passed state.has and registry.Get before the
// close could then create a fresh open lane for a session the close already
// removed. Tombstones are a few words each and are bounded by
// the number of sessions ever resized or closed in the server's lifetime.

import (
	"context"
	"errors"
	"runtime/debug"
	"sync"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// resizeOp is one admitted resize request. done completes the JSON-RPC
// request exactly once, on whichever goroutine settles the op (the read loop
// for a superseded op, the lane worker for an applied or cancelled one).
type resizeOp struct {
	// reported is the geometry the CLIENT measured. It is not the size the
	// session will take: the session decides that (session.Size,
	// nocx-eidfb.1), and what it decided is read back off the session after
	// the apply rather than assumed here.
	reported session.Size
	done     func(err error) // err nil = success
}

// sessionLane serializes one session's resize work behind a terminal close
// gate. See the package comment for the invariants and the map lifecycle.
type sessionLane struct {
	// sid names the session this lane serves. It is here for the log line
	// below, which is the only audience a dropped resize has: no JSON-RPC
	// request is left waiting on it, so a lane that abandons work without
	// saying which session it was is a degrade nobody can see.
	sid session.ID
	log log.Logger
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

	mu sync.Mutex
	// sess is the session this lane resizes, read and written only under mu
	// and always in the same critical section as closed — the two are one
	// fact (invariant 4) and reading them apart is what tore them. It is nil
	// exactly for a lane whose close has been admitted, including a tombstone
	// that never had one.
	sess    session.Session
	pending *resizeOp // latest unapplied resize; nil when idle
}

func newSessionLane(sid session.ID, sess session.Session, logger log.Logger, resized func(cols, rows uint16)) *sessionLane {
	// Lane-owned lifetime: the lane context is the session's own and is
	// deliberately derived from Background — its owner is the per-session
	// resize lane, and its closing event is closeLane's admission (see the
	// package comment's invariant 2: the worker's exit after the cancelled
	// resize returns). It must NOT die with any one connection: a session
	// is resized by whichever connection is attached (AD-9).
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionLane{
		sid:     sid,
		log:     logger,
		ctx:     ctx,
		cancel:  cancel,
		wake:    make(chan struct{}, 1),
		sess:    sess,
		resized: resized,
	}
}

// newClosedLane returns the tombstone for a session whose close is being
// admitted before any lane existed: closed and cancelled BEFORE it is
// returned, so there is no instant, however short, at which it is reachable
// and still accepting work.
//
// The distinction is the whole of nocx-44349. closeLane used to publish an
// ordinary lane into the map and admit its close on the next line, and the
// gap between those two statements is a real interval: a resize that was
// already blocked on lanesMu takes it the moment closeLane releases it, finds
// an OPEN lane holding no session, enqueues, and starts a worker that reaches
// apply with a nil session.Session. The dereference panicked a goroutine
// nobody recovers, which is the whole backend rather than one failed resize.
// A tombstone born closed cannot be caught half-built, so the interval has no
// inside.
func newClosedLane(sid session.ID, logger log.Logger) *sessionLane {
	l := newSessionLane(sid, nil, logger, nil)
	l.closed = true
	l.cancel()
	return l
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
		// The session and the closed flag are read in ONE critical section:
		// they are two halves of one fact (invariant 4), and admitClose writes
		// both under the same mu. Claiming them apart would let a lane report
		// itself open and hand out the nil session it had already dropped.
		sess := l.sess
		l.mu.Unlock()

		l.run(sess, op)
	}
}

// errResizePanicked is what a request is answered with when the op that would
// have answered it panicked. It says the resize did not happen without
// pretending to know why, and it exists because "the backend crashed" is not
// an answer a JSON-RPC request can be given.
var errResizePanicked = errors.New("transport: the resize panicked in the session lane")

// run applies one claimed op and settles it exactly once, on this goroutine,
// whatever the op does.
//
// The recover is deliberate and is defence in depth, not the fix for
// nocx-44349 (that is the nil branch in apply and the tombstone born closed).
// It is here because of WHERE this goroutine is: the lane worker is started
// by enqueue and is owned by nobody, so a panic on it is not a failed resize,
// it is the death of the process — every session, every connection, on any
// future defect in anything apply touches (session.Resize is an interface
// call into a local PTY or an SSH channel, and l.resized reaches the pane
// grid). Trading a crash for a failed resize plus a logged stack is the
// right trade for an operation whose worst honest outcome is a terminal at
// the wrong size.
//
// It swallows rather than re-panicking, which is the opposite of
// internal/shellintegration's publisher recover: that one restores an
// invariant and lets the crash stand, because a publish that panicked has
// nothing to fall back to. This one HAS a fallback — the request is answered,
// the lane goes back to serving the next resize — and the process surviving
// is the point.
func (l *sessionLane) run(sess session.Session, op *resizeOp) {
	settled := false
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if l.log != nil {
			l.log.Error("a resize panicked in the session lane; the resize failed and the lane keeps serving",
				"session_id", string(l.sid),
				"cols", op.reported.Cols, "rows", op.reported.Rows,
				"panic", r, "stack", string(debug.Stack()))
		}
		if !settled {
			op.done(errResizePanicked)
		}
	}()
	err := l.apply(sess, op)
	// Set before settling, not after: settle's job is to complete the request
	// exactly once, so a panic inside it must not produce a second completion.
	settled = true
	l.settle(op, err)
}

// apply runs one resize against the session with the lane's context and
// reports what the request should be answered with. It never settles the op
// itself — run does that, exactly once.
//
// sess is the reference claimed under mu. admitClose may nil the lane's copy
// out from under a running apply without a race; this local one keeps the
// session alive until the resize returns.
func (l *sessionLane) apply(sess session.Session, op *resizeOp) error {
	if sess == nil {
		// The closing half of invariant 4: a claim with no session is a
		// decision NOT to use one, and it is written down here rather than
		// assumed, because assuming it dereferenced nil on a goroutine nobody
		// recovers and took the backend with it (nocx-44349).
		//
		// The request is answered as admitted, exactly as a resize cancelled
		// by close is: a lane with no session is a lane whose session is
		// going away, and there is no failure of the client's to report. It
		// is said out loud because nothing else would say it — no request is
		// left waiting, and a resize that silently evaporates is how a
		// terminal ends up at a grid nobody chose.
		if l.log != nil {
			l.log.Warn("a resize was claimed for a lane that holds no session; it was dropped",
				"session_id", string(l.sid),
				"cols", op.reported.Cols, "rows", op.reported.Rows)
		}
		return nil
	}
	err := sess.Resize(l.ctx, op.reported)
	if err == nil && l.resized != nil {
		// The size the session TOOK, not the one the client reported: the
		// backend owns the decision (nocx-eidfb.1) and the grid follows the
		// pane's real geometry, so a report the session did not adopt must
		// not be what the grid is resized to.
		applied := sess.EffectiveSize()
		l.resized(applied.Cols, applied.Rows)
	}
	return err
}

// settle completes op once, with the outcome the lane's state says it has.
func (l *sessionLane) settle(op *resizeOp, err error) {
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
	l := newSessionLane(sid, sess, s.log, func(cols, rows uint16) { s.resizePaneGrid(sid, cols, rows) })
	s.lanes[sid] = l
	return l
}

// takeSize hands a session the geometry of the client that now owns it — the
// one that attached last, which is the client the shared channel follows
// (nocx-eidfb.2) — and the report NoClient when the last client has gone.
//
// One owner of that hand-off, because it has two callers whose facts must not
// drift: the attach that takes a session, and the connection teardown that
// empties its subscriber slot. It goes through the session's resize lane
// rather than calling sess.Resize, for the reason the lane exists: a
// window-change on a dead SSH channel blocks, and neither an attach on the
// read loop nor a teardown that other sessions are queued behind may wait on
// one. So this ADMITS a resize and returns; the lane applies it, and
// EffectiveSize reports the new grid only once the channel has taken it.
//
// The failure is said out loud rather than swallowed. Nothing on the wire
// answers it — no JSON-RPC request is waiting, and in the teardown case there
// is by definition no client left to tell — so the log is the only audience
// there is, and "the terminal is at a different grid than the window" is
// exactly the state a silent degrade would hide.
func (s *WSServer) takeSize(sid session.ID, sess session.Session, reported session.Size) {
	op := &resizeOp{
		reported: reported,
		done: func(err error) {
			if err != nil {
				s.log.Warn("the session could not be resized to the client that owns it; the terminal is running at a different grid than the window",
					"session_id", string(sid),
					"cols", reported.Cols, "rows", reported.Rows, "error", err)
			}
		},
	}
	if !s.laneFor(sid, sess).enqueue(op) {
		// The lane is a tombstone: this session's close was already admitted,
		// so there is no channel left to resize. Not a failure — the same
		// refusal a client's own resize would get.
		s.log.Debug("session size not taken; the session is closing",
			"session_id", string(sid))
	}
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
		// Born closed, and only then published: between the map write and the
		// admitClose below there is a window in which another goroutine —
		// one already blocked on lanesMu inside laneFor — takes the lock and
		// enqueues. A lane that was open in that window accepted the resize
		// and started a worker on a session it did not have (nocx-44349).
		l = newClosedLane(sid, s.log)
		s.lanes[sid] = l
	}
	s.lanesMu.Unlock()
	// Idempotent, and a no-op for the tombstone just created: what it is here
	// for is the lane that already existed, which is the ordinary case.
	l.admitClose()
}
