package session

// The session I/O owner (nocx-6q1uh.3, spec §5).
//
// # Why
//
// The runtime used to hold its lock across a PTY write (sessionruntime's old
// Execute), and hostSession.write held ITS OWN mutex from validating the
// writer through the return of proc.Write, so that a lease transition could
// never land between the check and the write. Both were the same defect
// stated twice: the pump needed the write's lock to ingest, so a write
// blocked on a program that floods output and never reads its input stalled
// the whole pane (nocx-6q1uh.1). A mutex cannot give both "one indivisible
// validate-and-decide step" and "the pump never waits on a write" at once.
//
// # What replaces it
//
// Exactly one goroutine per session — this one — decides the order of
// everything that changes the terminal or its model: output ingest, runtime
// replies, client frames, intents and resize. It never blocks on I/O itself:
// reading happens either through a raw, non-blocking read(2) loop
// (internal/pty's RawReadChunk, for a local PTY) with a SEPARATE
// readiness goroutine that consumes nothing, or — for a Process with no such
// seam, today's SSH channels among them — a plain blocking reader goroutine
// that hands chunks back to this one, the same shape internal/pty's old pump
// had. Writing happens on a SEPARATE writer goroutine, one item at a time,
// so the owner's own loop is free to keep draining and ingesting while a
// write sits blocked on a program that is not reading.
//
// Every input item — a reply, a client frame, an intent, a resize — is
// queued here in arrival order, and the owner is the ONLY thing that ever
// touches sessionOwner's own bookkeeping fields (pending, writerBusy,
// inFlight, eofSeen, replyBytes, nextFence): every one of them is read and
// written exclusively from run()'s goroutine, which is what makes this
// object the lock spec §5 replaces mu with, rather than a second lock beside
// it. The one exception is completedFence, read from other goroutines
// through inputFence() (a future caller stamping a snapshot, nocx-6q1uh.4),
// and it is an atomic for exactly that reason.
//
// # SSH sessions: no barrier, and a detach rather than a join
//
// SSH sessions run through the fallback reader-goroutine path (rawReader is
// unimplemented on sshProcess), which is what makes hasReadBarrier answer
// false for them (owner_ssh.go): golang.org/x/crypto/ssh's Channel gives no
// readiness boundary, so a blocking Read may be holding bytes this owner has
// not ingested when a check function would run, and commitIntent refuses
// every intent on such a session before Admit is ever asked
// (ErrNoReadBarrier). Reads and snapshots are unaffected.
//
// golang.org/x/crypto/ssh also offers no PER-CHANNEL interrupt (unlike a
// local PTY's SetWriteDeadline), so a write stuck behind a peer holding its
// window at zero cannot be unblocked the way interruptWriter unblocks a
// local one. stop's forced-deadline branch detaches instead of joining
// forever: triggerDetach (below) signals run's own goroutine, and
// performDetach (owner_ssh.go) resolves the stuck item delivery_unknown and
// clears writerBusy/inFlight without ever hearing back from the writer
// goroutine, which keeps running, abandoned, until the pool's own taint
// mechanism ends its connection (internal/ssh's ConnPool.Taint /
// CloseTainted, reached through sshsvc.ShellChannel.Taint).
import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// ownerItemKind is which of the four ordered lanes an item travels
// (spec §5.3); itemAccessBump is named here for the wire op a later task
// wires (nocx-6q1uh.6's session.access-bump) so ownerItem's shape does not
// change again when that task adds it.
type ownerItemKind int

const (
	itemReply ownerItemKind = iota
	itemClientFrame
	itemIntent
	itemResize
	itemAccessBump
)

// pendingIntent is one conditional write awaiting its commit point. Check is
// run under the runtime's lock, inside Session.Commit, against a fresh
// Snapshot — the seam a token's digest and an access epoch are judged
// through (nocx-6q1uh.4); this task supplies the seam and a caller with
// nothing to check yet passes nil.
//
// Token, Canonical and CommitBy are nocx-6q1uh.4's own addition: the one-shot
// target this intent is spending, the canonical form [tokenBook.Consume]
// binds it to, and the monotonic deadline past which the commit point
// refuses `commit_deadline` rather than admit it at all. Whoever submits a
// wire-driven session.intent (nocx-6q1uh.6 wires the op) fills these in; a
// caller with no token — Task 2's own tests, and any intent this package
// admits without one — leaves Token's ID at its zero value, which
// commitIntent reads as "no token gate applies".
//
// admittedID is filled in by commitIntent once sessionruntime.Admit answers,
// so the owner can correct the runtime's record through ReportOutcome if the
// write that follows fails or falls short — Commit itself never writes, so
// it cannot know that yet.
type pendingIntent struct {
	Intent sessionruntime.Intent
	Check  func(sessionruntime.Snapshot) error

	Token       Token
	Interactive bool
	Canonical   canonicalIntent
	CommitBy    int64

	admittedID sessionruntime.IntentID
}

// The interactive lane relaxes draining only for truly unconditional input.
// A token, check or precondition always requires the actual read barrier.
func (pi *pendingIntent) requiresReadBarrier() bool {
	return !pi.Interactive || pi.Token.ID != (TokenID{}) || pi.Check != nil || pi.Intent.Precondition != nil
}

// ownerItem is one thing waiting for its turn at the commit point.
type ownerItem struct {
	kind    ownerItemKind
	payload []byte
	intent  *pendingIntent
	resize  *sessionruntime.Geometry
	// above is itemAccessBump's own payload (nocx-6q1uh.6, spec §7.2): the
	// epoch this bump supersedes. Meaningless for every other kind.
	above uint64
	// done is buffered(1): resolve never blocks on a caller that stopped
	// listening, and a reply (itemReply) carries none — nothing submitted it
	// through submit, and nothing is waiting on it.
	done      chan ownerResult
	queueSlot bool
}

// ownerResult is what became of one item.
type ownerResult struct {
	State        sessionruntime.IntentState
	BytesWritten int
	FenceAfter   sessionruntime.Fence
	Err          error
	// Epoch is itemAccessBump's own answer (nocx-6q1uh.6, spec §7.2): the
	// access epoch now in force. Meaningless for every other kind.
	Epoch uint64
	// Cause is the wire's own refusal-cause spelling (spec §6.5), set only by
	// resultFromStored (tokens.go) when this result is a REPLAY of a
	// previously recorded outcome. It exists because storedResult.Cause is
	// the one thing resultFromStored cannot round-trip through Err alone:
	// Err there is rebuilt as fmt.Errorf("session: %s", cause), which
	// causeOf's errors.Is checks can never match back to a sentinel (a
	// %s-formatted error wraps nothing). A caller rendering the wire result
	// reads Cause first and falls back to causeOf(Err) only when it is
	// empty — which is also how it tells a replay from a fresh refusal
	// (nocx-6q1uh.6's own wire layer, session.intent's regionOmitted).
	Cause string
}

var (
	// errOwnerClosing names an item that arrived, or was still waiting, once
	// the owner began shutting down. It was never handed to the writer.
	errOwnerClosing = errors.New("session: the owner is closing")
	// errBusy names an item refused because the owner's queue is already
	// full (intentQueueMax), before it existed as anything the owner could
	// later resolve.
	errBusy = errors.New("session: busy")
)

const (
	// replyReserveBytes bounds how much of a program's own unread answers
	// this owner will hold before it says so (spec §5.5): the ceiling that,
	// once crossed, moves an incarnation's completeness to lost-ingest
	// rather than growing without bound.
	replyReserveBytes = 64 << 10
	// intentQueueMax bounds ordinary submitted work across incoming,
	// pending and in-flight items. Access bumps stay bounded by incoming
	// alone; emulator replies have their own byte reserve.
	intentQueueMax = 64
)

// rawReader splits readiness from bounded, owner-only reads. A data read is
// not a barrier: only an actual EAGAIN establishes that all currently readable
// output has been ingested. Processes without this seam use the SSH fallback.
type rawReader interface {
	WaitReadable(ctx context.Context) error
	RawReadChunk(buf []byte) (n int, barrier bool, err error)
}

// writeInterrupter unblocks a write in flight (spec §5.7): a local PTY's
// master answers it (internal/pty.LocalPty.InterruptWrite), an SSH channel
// does not — golang.org/x/crypto/ssh's Channel offers no per-channel
// interrupt, which is why the next task detaches a stuck SSH writer instead
// of interrupting it.
type writeInterrupter interface {
	InterruptWrite() error
}

// readinessWaker lets the owner unpark a WaitReadable that ctx cancellation
// alone cannot reach (nocx-6q1uh.18). A real local PTY blocks a parked
// WaitReadable inside the kernel's poll(2) (internal/pty.LocalPty), a
// syscall ctx cancellation is not itself part of — nothing wakes it just
// because readCtx's Done channel closed, so the owner calls WakeReadiness
// itself, every time it cancels readCtx. A test fixture whose WaitReadable
// already selects on ctx.Done() directly needs no such nudge and simply
// does not implement this — the same optional-interface shape
// writeInterrupter uses for the same reason (not every Process, and not
// every fixture standing in for one, needs the same escape hatch).
type readinessWaker interface {
	WakeReadiness()
}

// readEvent is one report from the fallback reader goroutine: a chunk, or —
// once, as the last thing it ever sends — the error its blocking Read ended
// on (io.EOF for an ordinary close).
type readEvent struct {
	chunk []byte
	err   error
}

// writeOutcome is what one call to proc.Write answered.
type writeOutcome struct {
	n   int
	err error
}

// inFlightItem is the one item currently handed to the writer, with the
// fence it was assigned the moment that handoff happened — spec §5.3 step
// 3's linearisation point.
type inFlightItem struct {
	item  ownerItem
	fence sessionruntime.Fence
}

// sessionOwner is the one I/O owner of one host session (spec §5.2). Every
// field below run() itself is touched by is commented at its own
// declaration; nothing outside this file may reach into any of them.
type sessionOwner struct {
	proc Process
	rt   *sessionruntime.Session
	win  *window
	log  *slog.Logger

	// admissionMu fences submit's check-and-enqueue against beginClosing.
	// After admissionClosed is set, incoming is a finite, immutable backlog
	// that the owner must settle before it can publish stopped.
	admissionMu     sync.Mutex
	admissionClosed bool

	incoming      chan ownerItem
	queueUsed     atomic.Int64
	closingSignal chan struct{}
	closeOnce     sync.Once
	stopped       chan struct{}

	readCtx    context.Context
	cancelRead context.CancelFunc

	writeReq chan []byte
	writeRes chan writeOutcome

	// completedFence is the only field another goroutine reads
	// (inputFence, from whichever future caller stamps a frame — spec
	// §5.4). Everything else below is run()'s alone.
	completedFence atomic.Uint64

	// ingest is the watchdog over this owner's one ingest in flight
	// (ingestwatch.go, nocx-xn63t.6.47). Its fields are atomics for the
	// reason completedFence's are: run()'s own goroutine begins and ends a
	// hold, and the watchdog goroutine reads it, so the two must not share
	// plain memory.
	ingest ingestWatch

	// tokens is this session's one-shot token book (nocx-6q1uh.4, spec
	// §6.2), bound once by SetTokens before the first token-bearing intent
	// can arrive — the same one-time-binding shape [sessionruntime.Session
	// .SetReplies] uses, for the same reason: the book is built the moment
	// after this owner is, over the very incarnation it already serves.
	tokens *tokenBook
	// accessEpoch is this session's current access epoch (nocx-6q1uh.6,
	// spec §7.2): 1 for a fresh incarnation, raised only by applyAccessBump
	// (access.go). It is an atomic for the same reason completedFence is —
	// takeSnapshot (snapshots.go) reads it from outside this owner's own
	// goroutine, through currentAccessEpoch (access.go) — even though every
	// WRITE to it happens on run()'s own goroutine, same as every other
	// field below this comment.
	accessEpoch atomic.Uint64
	// nowMono answers the commit point's monotonic clock, in the units a
	// pendingIntent's CommitBy is expressed in: internal/monoclock.Now
	// (CLOCK_MONOTONIC on Linux, CLOCK_MONOTONIC_RAW on Darwin), never
	// time.Now — a wall clock can jump (NTP, a suspend/resume) in either
	// direction, and a deadline built on one could be defeated or tripped by
	// an event that has nothing to do with how long the intent actually
	// waited (nocx-6q1uh.6, spec §7.2). A test wanting a fake clock sets the
	// field directly, in-package.
	nowMono func() int64

	// deadlineAfterFunc supplies the owner-only pending deadline wakeup.
	deadlineAfterFunc lifecycleAfterFunc
	deadlineTimer     lifecycleTimer
	deadlineWake      chan struct{}
	deadlineMono      int64
	deadlineWall      time.Time
	// detachSignal, detachOnce and detached are the SSH detach path (spec
	// §5.7, nocx-6q1uh.3, owner_ssh.go): stop closes detachSignal, once,
	// when its own deadline fires against a Process it cannot interrupt
	// (writeInterrupter); run's own goroutine answers it (performDetach),
	// because inFlight and writerBusy below are touched from nowhere else.
	// detached is read from outside run()'s goroutine (writerDetached), an
	// atomic for the same reason completedFence is.
	detachSignal chan struct{}
	detachOnce   sync.Once
	detached     atomic.Bool

	// inFlightFlag publishes whether writerBusy/inFlight are currently
	// non-empty, for a caller outside run()'s own goroutine that needs to
	// know a write has actually reached the writer before it acts on that —
	// an atomic for the same reason completedFence is. writeStart,
	// completeWrite and performDetach (owner_ssh.go) are its only writers,
	// each right where they touch writerBusy/inFlight themselves. Its only
	// reader today is owner_ssh_export_test.go's writeInFlight/
	// TestWriteInFlight (the nocx_local_ssh test bridge) — a production
	// caller does not exist yet, and the field is still on this struct
	// rather than in that test file because it is written from run()'s own
	// goroutine alongside writerBusy/inFlight, the same as completedFence
	// and detached beside it.
	inFlightFlag atomic.Bool

	// --- run()'s own state; touched from nowhere else ----------------------
	pending    []ownerItem
	closing    bool
	writerBusy bool
	inFlight   *inFlightItem
	eofSeen    bool
	replyBytes int
	nextFence  sessionruntime.Fence
	// readableCh is where the readiness goroutine reports back, for a
	// rawReader Process only (run creates it, and starts runReadiness, only
	// then); nil otherwise, which makes the read on it in run's own select
	// block forever — exactly like a case that is not there.
	readableCh       chan struct{}
	readRearm        chan struct{}
	readNotified     bool
	localReadPending bool
	readBuf          [pageSize]byte
	readEndErr       error
}

// newSessionOwner builds the owner over a process that already exists and a
// runtime already directing it. It does not start anything — run does — so a
// caller can bind this owner as the runtime's ReplySink (Session.SetReplies)
// before the first byte is ever read.
func newSessionOwner(proc Process, rt *sessionruntime.Session, win *window, lg *slog.Logger) *sessionOwner {
	readCtx, cancelRead := context.WithCancel(context.Background())
	// THE SESSION'S LOGGER RIDES ITS CONTEXT (nocx-xn63t.6.47). The daemon's
	// sink is its own *slog.Logger (cmd/nocx-helper/servelog.go), not
	// internal/log's root, so a line written through log.From would otherwise
	// go nowhere this helper can see. Binding it here means the watchdog can
	// log through the context like every other module — module, trace and
	// span included — and still reach helper.log.
	readCtx = log.WithLogger(readCtx, log.NewSlogAdapter(lg))
	o := &sessionOwner{
		proc:          proc,
		rt:            rt,
		win:           win,
		log:           lg,
		incoming:      make(chan ownerItem, intentQueueMax),
		closingSignal: make(chan struct{}),
		stopped:       make(chan struct{}),
		readCtx:       readCtx,
		cancelRead:    cancelRead,
		writeReq:      make(chan []byte),
		writeRes:      make(chan writeOutcome, 1),
		nowMono:       func() int64 { return int64(monoclock.Now()) },
		detachSignal:  make(chan struct{}),
		deadlineWake:  make(chan struct{}, 1),
		deadlineAfterFunc: func(d time.Duration, f func()) lifecycleTimer {
			return time.AfterFunc(d, f)
		},
	}
	// A fresh incarnation starts at epoch 1 (spec §7.2, §6.1): the zero
	// value an unset atomic.Uint64 would otherwise report is not a real
	// epoch anything ever grants, and a snapshot reporting 0 would look
	// indistinguishable from "no epoch protocol applies" to a coordinator
	// that has never seen a bump.
	o.accessEpoch.Store(1)
	return o
}

// SetTokens binds this session's one-shot token book. It must run before the
// first token-bearing intent can be submitted — finishSpawn does this
// immediately after constructing the owner, the same moment it binds
// Session.SetReplies — and is never rebound afterward: one incarnation, one
// book, for the owner's whole life.
func (o *sessionOwner) SetTokens(tb *tokenBook) {
	o.tokens = tb
}

// run is the owner goroutine. It returns once shutdown (stop) has been asked
// for, the read side has ended, nothing is in flight and nothing is left
// queued — see the package doc for the one case that leaves it blocked
// anyway.
func (o *sessionOwner) run() {
	defer close(o.stopped)
	defer func() {
		if o.deadlineTimer != nil {
			o.deadlineTimer.Stop()
		}
	}()
	defer func() { _ = o.proc.Close() }()
	defer o.cancelReadAndWake()

	go o.runWriter()
	go o.watchIngest()

	var readEvents chan readEvent
	// readinessEnded carries the error the readiness goroutine stopped on.
	// Buffered, so that goroutine's last send never waits on a run that has
	// already returned.
	var readinessEnded chan error
	if rr, ok := o.proc.(rawReader); ok {
		o.readableCh = make(chan struct{})
		o.readRearm = make(chan struct{}, 1)
		readinessEnded = make(chan error, 1)
		go o.runReadiness(rr, o.readableCh, readinessEnded)
	} else {
		readEvents = make(chan readEvent, 1)
		go o.runReaderGoroutine(readEvents)
	}

	// closingSignal is nilled out the first time it is ever received: a
	// receive on a CLOSED channel never blocks, so leaving o.closingSignal
	// itself in this select would make that case fire, and o.advance() run
	// behind it, on every single iteration from then on — a busy loop
	// pegging one core at 100% until eofSeen finally lets run return
	// (nocx-6q1uh.18, found in the same merged-tree run as the deadlock
	// above: goroutine 9 sat "runnable" in advance at shutdown, not blocked
	// anywhere, because this select had nothing left to wait ON). A nil
	// channel blocks forever, which is exactly "this case is not here any
	// more" — the same trick readableCh and readEvents already rely on for
	// a Process that never uses them.
	closingSignal := o.closingSignal
	detachSignal := o.detachSignal
	readMore := make(chan struct{})
	close(readMore)

	for {
		// Shutdown and detach are checked between every bounded read, before
		// offering another input item to the writer.
		select {
		case <-closingSignal:
			closingSignal = nil
			if !o.closing {
				o.beginClosing()
			}
		default:
		}
		select {
		case <-detachSignal:
			detachSignal = nil
			o.performDetach()
		default:
		}
		if o.closing && o.eofSeen && !o.writerBusy && o.inFlight == nil && len(o.pending) == 0 {
			close(o.writeReq)
			return
		}

		// Drain everything already sitting on o.incoming BEFORE looking at
		// any other event, every iteration — in particular before o.writeRes,
		// which a select giving both cases equal odds would otherwise let
		// win the race on nothing but luck. A bump sent to o.incoming ahead
		// of a write's completion is "already queued" the instant it is
		// sent, by spec §7.2's own words ("ahead of everything already
		// queued"), but Go's select carries no memory of WHICH channel's
		// send happened first in wall-clock time between two DIFFERENT
		// channels — only FIFO order within the SAME one. Without this,
		// applyAccessBump's sweep (access.go) can run one iteration too
		// late: a completeWrite that frees the writer in the same iteration
		// as a not-yet-looked-at bump lets advance() dispatch an item the
		// bump would have refused, handing it to the writer before the
		// bump is ever read off the channel — the intent has then left
		// o.pending for o.inFlight, and no sweep reaches it there. Draining
		// o.incoming first, unconditionally, closes that gap: every item
		// already sent is dispatched (and any bump's sweep already run)
		// before this iteration ever asks whether the writer is free.
		for remaining := intentQueueMax; remaining > 0; remaining-- {
			select {
			case it := <-o.incoming:
				o.dispatchIncoming(it)
			default:
				remaining = 0
			}
		}
		o.expirePending()
		if len(o.incoming) == 0 {
			// Refilled incoming may contain a priority revocation. Delay
			// only writer advancement, never the event turn below: reads,
			// write completions and lifecycle work must still progress.
			o.advance()
		}
		if o.closing && o.eofSeen && !o.writerBusy && o.inFlight == nil && len(o.pending) == 0 {
			close(o.writeReq)
			return
		}
		o.armPendingDeadline()
		var continueRead <-chan struct{}
		if o.localReadPending {
			continueRead = readMore
		}

		select {
		case <-closingSignal:
			closingSignal = nil
			if !o.closing {
				o.beginClosing()
			}
		case it := <-o.incoming:
			o.dispatchIncoming(it)
		case <-o.readableCh:
			o.readNotified = true
			o.drainLocal()
		case <-continueRead:
			o.drainLocal()
		case err := <-readinessEnded:
			readinessEnded = nil
			o.readinessEnded(err)
		case ev := <-readEvents:
			o.handleReadEvent(ev)
		case <-o.deadlineWake:
			// The callback may be stale; stop the current timer before rearming.
			// Only expirePending judges deadlines on the next owner turn.
			if o.deadlineTimer != nil {
				o.deadlineTimer.Stop()
				o.deadlineTimer = nil
			}
		case res := <-o.writeRes:
			o.completeWrite(res)
		case <-detachSignal:
			detachSignal = nil
			o.performDetach()
		}
	}
}

// dispatchIncoming is what run() does with one item off o.incoming, whether
// reached through the priority drain above or the main select's own
// o.incoming case: an access-bump is applied at once (access.go), a closing
// owner cancels anything further outright, a token-bearing intent may be
// resolved directly by tokenGate (tokens.go) without ever being queued, and
// everything else joins o.pending for advance() to reach in its turn.
func (o *sessionOwner) dispatchIncoming(it ownerItem) {
	switch {
	case it.kind == itemAccessBump:
		// Ahead of everything already queued, whether or not the owner is
		// closing (spec §7.2: a bump always applies, and carries no refusal
		// of its own) — see access.go's own doc for why this cannot wait
		// for its turn in o.pending.
		o.applyAccessBump(it)
	case o.closing:
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateCancelled, Err: errOwnerClosing})
	case it.kind == itemIntent && it.intent.Interactive && o.interactiveGate(it):
		// Interactive input has no screen target, but its caller's access
		// epoch is still checked before queue admission.
	case it.kind == itemIntent && it.intent.Token.ID != (TokenID{}) && o.tokenGate(it, it.intent):
		// tokenGate (tokens.go) resolved it directly: a replay of an
		// already-recorded or in-flight token, or a refusal decided before
		// this intent is ever admitted (forged, expired, token_spent,
		// access_revoked, commit_deadline). Nothing left to queue.
	default:
		o.pending = append(o.pending, it)
	}
}

// cancelReadAndWake cancels readCtx and, when this owner's Process supports
// it, wakes a WaitReadable that may be parked somewhere ctx cancellation
// alone cannot reach — a real local PTY's poll(2) wait (nocx-6q1uh.18,
// readinessWaker's own doc). Every path that ends the read side goes through
// this rather than calling o.cancelRead directly, so the wake is never
// forgotten on one of them: run's own deferred teardown and finishRead's
// "the read side is over" both need it equally, and a readiness goroutine
// left parked in the kernel after either would hold this session's last
// goroutine open with nothing left that could ever wake it.
func (o *sessionOwner) cancelReadAndWake() {
	o.cancelRead()
	if rw, ok := o.proc.(readinessWaker); ok {
		rw.WakeReadiness()
	}
}

// beginClosing is stop admission (spec §5.7's first step), run exactly once:
// every item still waiting — never handed to the writer — is cancelled at
// once, termination is requested and a write already in flight is
// interrupted. What is already in flight resolves on its own terms
// (completeWrite), never cancelled: bytes on a PTY cannot be recalled.
//
// A token-bearing itemIntent can be sitting in o.pending here with its token
// already Consumed: tokenGate (tokens.go) binds the token at RECEIPT, well
// before an item ever reaches o.pending, and "unused -> proceed" is exactly
// how it gets here. Cancelling it without recording an outcome would be the
// same wedge nocx-6q1uh.15 fixes at tokenGate's own no-read-barrier
// refusal — a bound slot with nothing ever recorded against it, so
// session.intent.status can answer nothing but in_progress forever. So this
// records before it resolves, the same order finishItem and commitIntent's
// own early refusals already use.
func (o *sessionOwner) beginClosing() {
	// A submit holding this fence must finish its enqueue before shutdown
	// captures incoming. Later submitters are refused, even if they started
	// before shutdown. Never hold the fence across process I/O or settlement.
	o.admissionMu.Lock()
	o.admissionClosed = true
	o.admissionMu.Unlock()
	o.closing = true
	for _, it := range o.pending {
		res := ownerResult{State: sessionruntime.IntentStateCancelled, Err: errOwnerClosing}
		if it.kind == itemIntent {
			o.recordTokenOutcome(it.intent, res)
		}
		o.resolve(it, res)
	}
	o.pending = nil
	// No producer can refill incoming past the admission fence. Its capacity
	// bounds this pass; ordinary input is cancelled and accepted access bumps
	// still apply, including when EOF was observed before shutdown began.
	for remaining := len(o.incoming); remaining > 0; remaining-- {
		o.dispatchIncoming(<-o.incoming)
	}
	o.requestTermination()
	o.interruptWriter()
}

// requestTermination signals the process group the way internal/pty's own
// Close does (SIGHUP, the signal a terminal sends when it goes away), but
// WITHOUT closing the master — this owner still wants to read the tail. It
// is best-effort and silent on the ordinary refusal (nothing to signal, or a
// Process — a fake in a test — that does not support it at all): a session
// ending is not a fault, and the read loop closing the readable side moments
// later is what actually ends things either way.
func (o *sessionOwner) requestTermination() {
	pgs, ok := o.proc.(ProcessGroupSignaller)
	if !ok {
		return
	}
	pgid := o.proc.Pid()
	if pgid <= 0 {
		return
	}
	if err := pgs.SignalProcessGroup(pgid, syscall.SIGHUP); err != nil {
		o.log.Debug("session owner: request termination", "err", err)
	}
}

// interruptWriter unblocks a write in flight, when the Process supports it
// (writeInterrupter — a local PTY only, see that interface's doc).
func (o *sessionOwner) interruptWriter() {
	wi, ok := o.proc.(writeInterrupter)
	if !ok {
		return
	}
	if err := wi.InterruptWrite(); err != nil {
		o.log.Debug("session owner: interrupt write", "err", err)
	}
}

// drainLocal performs one read quantum and ingests it losslessly. False means
// work remains, NOT that the screen is current. Only EAGAIN (or an ended read
// side) lets a checked head proceed. The reusable buffer is owned by run alone.
func (o *sessionOwner) drainLocal() bool {
	if o.eofSeen {
		return true
	}
	rr, ok := o.proc.(rawReader)
	if !ok {
		return true
	}
	n, barrier, err := rr.RawReadChunk(o.readBuf[:])
	if n > 0 {
		o.ingestOne(o.readBuf[:n])
	}
	o.localReadPending = !barrier && err == nil
	if err != nil {
		o.finishRead(err)
	} else if barrier && o.readEndErr != nil {
		o.finishRead(o.readEndErr)
	}
	if barrier && o.readNotified {
		o.readNotified = false
		select {
		case o.readRearm <- struct{}{}:
		default:
		}
	}
	return barrier || o.eofSeen
}

// runReadiness permits one outstanding notification. It rearms only when the
// owner reaches EAGAIN; a continuous producer therefore wakes the owner without
// spinning poll(2) while that owner is busy ingesting or handling input. The
// acknowledgement carries no PTY read lock, and opportunistic head reads may
// establish the barrier just as ordinary readable notifications can.
func (o *sessionOwner) runReadiness(rr rawReader, readableCh chan<- struct{}, ended chan<- error) {
	for {
		if err := rr.WaitReadable(o.readCtx); err != nil {
			ended <- err
			return
		}
		select {
		case readableCh <- struct{}{}:
		case <-o.readCtx.Done():
			ended <- o.readCtx.Err()
			return
		}
		select {
		case <-o.readRearm:
		case <-o.readCtx.Done():
			ended <- o.readCtx.Err()
			return
		}
	}
}

// readinessEnded is run's answer to the readiness goroutine returning. When
// readCtx ended it, finishRead has already run and this finds eofSeen true.
// Otherwise the PTY was closed under the wait (a stop's deadline forcing
// proc.Close) or the wait failed, and no readiness signal will ever arrive
// again — so whatever is still readable is drained now, and if that drain
// did not itself report the end, the read side is ended here. Relying on the
// drain alone is what hung a helper Service.Close for ten minutes
// (nocx-mrfe5): the last drain could have answered EAGAIN just before the
// master closed, and nothing was left to ask again.
func (o *sessionOwner) readinessEnded(err error) {
	o.readEndErr = err
	if o.readEndErr == nil {
		o.readEndErr = io.EOF
	}
	o.drainLocal()
}

// runReaderGoroutine is the fallback for a Process with no raw read seam —
// today, an SSH channel — the same blocking shape internal/pty's old pump
// had. Its last send is always the error its Read ended on (io.EOF for an
// ordinary close), and it returns immediately after: this owner's run loop
// is guaranteed still alive to receive it, because run cannot exit before
// eofSeen is true, and that flag is set only in response to this send.
func (o *sessionOwner) runReaderGoroutine(events chan<- readEvent) {
	buf := make([]byte, pageSize)
	for {
		n, err := o.proc.Read(buf)
		if n > 0 {
			events <- readEvent{chunk: append([]byte(nil), buf[:n]...)}
		}
		if err != nil {
			events <- readEvent{err: err}
			return
		}
	}
}

// handleReadEvent is runReaderGoroutine's delivery reaching the owner's own
// goroutine — the same ingest path drainLocal feeds for a local PTY.
func (o *sessionOwner) handleReadEvent(ev readEvent) {
	if len(ev.chunk) > 0 {
		o.ingestOne(ev.chunk)
	}
	if ev.err != nil {
		o.finishRead(ev.err)
	}
}

// ingestOne feeds one chunk into the runtime and then the output window, IN
// THAT ORDER — the runtime's ingest is lossless and the window is
// capacity-reclaimed, so what a later attacher can rebuild a screen from
// must never depend on what the window still happens to hold
// (internal/helper/session.hostSession.pump's own doc, before this task
// retired it, said the same thing about the same two calls).
func (o *sessionOwner) ingestOne(b []byte) {
	if len(b) == 0 {
		return
	}
	o.ingest.begin(len(b))
	err := o.rt.Ingest(b)
	o.ingest.end()
	if err != nil {
		if errors.Is(err, emulator.ErrClosed) || errors.Is(err, sessionruntime.ErrUnavailable) {
			o.log.Debug("session output arrived after the terminal closed", "bytes", len(b), "err", err)
		} else {
			o.log.Warn("session output not ingested", "bytes", len(b), "err", err)
		}
	}
	o.win.write(b)
}

// watchIngest samples the ingest watchdog once a second, so a call that
// passes ingestWatchAfter is reported while it is still happening
// (ingestwatch.go, nocx-xn63t.6.47). The runtime's own hold record rides
// along: only it can say the call is INSIDE the critical section, which is
// what makes the call the lock's holder rather than a waiter. It costs
// nothing when no ingest is in flight and returns with the owner.
func (o *sessionOwner) watchIngest() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-o.stopped:
			return
		case <-ticker.C:
			o.ingest.sample(log.From(o.readCtx), o.ingestHold)
		}
	}
}

// ingestHold answers how long the runtime's Ingest critical section has been
// open, and whether one is open at all — the fact that separates a call that
// HOLDS Session.mu from one queued behind another holder
// (internal/sessionruntime's IngestHold, written after the lock and cleared
// before it is released).
func (o *sessionOwner) ingestHold() (time.Duration, bool) {
	hold, ok := o.rt.IngestHold()
	if !ok {
		return 0, false
	}
	return time.Since(hold.Entered), true
}

// finishRead marks the read side over — EOF or an error, either is terminal
// for this session's input — closes the output window (no more bytes will
// ever arrive, the one event window.close exists to report) and stops the
// readiness goroutine from waiting on an fd nothing will ever read again.
func (o *sessionOwner) finishRead(err error) {
	if o.eofSeen {
		return
	}
	o.eofSeen = true
	o.localReadPending = false
	if err != nil && !errors.Is(err, io.EOF) {
		o.log.Warn("session output ended", "err", err)
	}
	o.win.close()
	o.cancelReadAndWake()
}

// advance is where an idle writer picks up the next item, if there is one —
// and, when the head resolves WITHOUT ever engaging the writer, the one
// after it, in a loop rather than a single step. A resize (commitResize)
// resolves synchronously and never touches o.writeReq; so does an intent
// that encodes to no payload, one commitIntent refuses outright before
// Admit, or one whose payload would have gone to a writer already detached
// (writeStart's own check, finishNeverOffered — nocx-q502e: this owner's
// writer, once gone, is gone for every item from then on, not just the one
// in flight when it went). Any of those leaves o.writerBusy exactly as it
// found it — false —
// so a single dequeue-and-return here left whatever was queued BEHIND it
// stuck until some unrelated event (a write completing, a new incoming
// item) next drove run()'s own select, which a quiet session may never
// produce again: TestResizeRacingARealTokenCommitIsIncomparable
// (owner_adversarial_test.go, nocx-6q1uh.18) hung its full 20s watchdog on
// exactly this — a token-bearing intent queued right behind a resize that
// had already resolved, with nothing left to wake run() a second time.
// Looping while the writer stays free is what commitIntent's admission
// point already promises ("hands it the next item only after the previous
// completed") for the write case; this is that same promise for the
// no-write ones.
func (o *sessionOwner) advance() {
	for !o.writerBusy && len(o.pending) > 0 {
		it := o.pending[0]
		if it.kind == itemIntent && o.refuseExpiredIntent(it) {
			o.pending = o.pending[1:]
			continue
		}
		barrier := o.drainLocal()
		if len(o.incoming) != 0 {
			// A revocation can arrive while the bounded read ingests. Let the
			// next owner turn dispatch it before committing this pending head.
			return
		}
		if it.kind == itemIntent && it.intent.requiresReadBarrier() && !barrier {
			// Keep the head in place. Later input must not overtake a
			// conditional write while it waits for a genuine read barrier.
			return
		}
		o.pending = o.pending[1:]
		o.processHead(it)
	}
}

// expirePending also runs when admission keeps refilling incoming or the
// writer is occupied, independently of whether a write may advance this turn.
func (o *sessionOwner) expirePending() {
	// Deadlines must settle even behind an occupied writer. Refusal writes
	// no bytes, so removing expired items does not reorder surviving input.
	kept := o.pending[:0]
	for _, it := range o.pending {
		if it.kind != itemIntent || !o.refuseExpiredIntent(it) {
			kept = append(kept, it)
		}
	}
	o.pending = kept
}

// armPendingDeadline schedules the next pending deadline.
func (o *sessionOwner) armPendingDeadline() {
	var earliest time.Duration
	var nextMono int64
	var nextWall time.Time
	found := false
	consider := func(d time.Duration, mono int64, wall time.Time) {
		if !found || d < earliest {
			earliest, nextMono, nextWall, found = d, mono, wall, true
		}
	}
	for _, it := range o.pending {
		if it.kind != itemIntent {
			continue
		}
		pi := it.intent
		if pi.CommitBy != 0 {
			consider(time.Duration(pi.CommitBy-o.nowMono()), pi.CommitBy, time.Time{})
		}
		if pi.Token.ID != (TokenID{}) && o.tokens != nil {
			consider(pi.Token.ExpiresAt.Sub(o.tokens.now()), 0, pi.Token.ExpiresAt)
		}
	}
	if found && o.deadlineTimer != nil && nextMono == o.deadlineMono && nextWall.Equal(o.deadlineWall) {
		return
	}
	if o.deadlineTimer != nil {
		o.deadlineTimer.Stop()
		o.deadlineTimer = nil
	}
	o.deadlineMono, o.deadlineWall = nextMono, nextWall
	if !found {
		return
	}
	// Refusal is strictly past the deadline, not at equality.
	if earliest < 0 {
		earliest = 0
	} else if earliest < time.Duration(1<<63-1) {
		earliest++
	}
	o.deadlineTimer = o.deadlineAfterFunc(earliest, func() {
		select {
		case o.deadlineWake <- struct{}{}:
		default:
		}
	})
}

// refuseExpiredIntent runs between read quanta, never against the screen.
// Caller cancellation deliberately does not cancel an admitted wire intent
// (intent_ops.go, D11); shutdown and access bumps retain their existing paths.
func (o *sessionOwner) refuseExpiredIntent(it ownerItem) bool {
	pi := it.intent
	var err error
	if pi.CommitBy != 0 && o.nowMono() > pi.CommitBy {
		err = errCommitDeadline
	} else if pi.Token.ID != (TokenID{}) && o.tokens != nil && o.tokens.now().After(pi.Token.ExpiresAt) {
		err = ErrExpired
	}
	if err == nil {
		return false
	}
	res := ownerResult{State: sessionruntime.IntentStateRefused, Err: err}
	o.recordTokenOutcome(pi, res)
	o.resolve(it, res)
	return true
}

// processHead handles an item only after advance's bounded drain. Checked
// intents require the genuine EAGAIN barrier; interactive/unconditional input
// can proceed after a data quantum and encodes against the ingested modes.
func (o *sessionOwner) processHead(it ownerItem) {
	switch it.kind {
	case itemReply, itemClientFrame:
		o.writeStart(it, it.payload)
	case itemResize:
		o.commitResize(it)
	case itemIntent:
		o.commitIntent(it)
	default:
		// itemAccessBump never reaches here: run()'s own incoming case
		// (nocx-6q1uh.6) intercepts it before it is ever appended to
		// o.pending — see access.go's doc for why. Reaching this arm at all
		// means an ownerItemKind was queued that nothing above recognises,
		// which is a defect in whatever constructed it, not a wire fact.
		o.resolve(it, ownerResult{
			State: sessionruntime.IntentStateRefused,
			Err:   fmt.Errorf("session: owner item kind %d has no handler yet", it.kind),
		})
	}
}

// commitIntent is Session.Admit (a production caller, at last — ADR-0066)
// followed by Session.Commit: admission assigns the intent its id and
// enters the runtime's own record of it, and Commit revalidates, checks and
// encodes without writing. Either refusing leaves nothing to write, and the
// item resolves on the spot.
//
// A token-bearing intent has ALREADY cleared tokenGate (tokens.go,
// nocx-6q1uh.4/.5) by the time it reaches here: run() (this file) gates it
// at receipt, before it is ever appended to o.pending, so nothing this
// method dequeues as an itemIntent can be a replay, an in-flight duplicate,
// or one refused for a reason tokenGate already decided. What tokenGate
// cannot decide at receipt is whether time itself has since run out: an
// intent within its commitBy when it arrived can still fall past it while
// queued behind other input, with no bump involved at all — so this method
// re-checks the deadline, alone, right before ever calling Admit (spec
// §7.2: "refuses AT RECEIPT and AT THE COMMIT POINT").
func (o *sessionOwner) commitIntent(it ownerItem) {
	if o.refuseExpiredIntent(it) {
		return
	}
	// A session with no read barrier (spec §5.2 — today, an SSH channel: a
	// blocking reader may be holding bytes this owner has not ingested yet)
	// cannot prove the screen a check function is about to read is the
	// screen right now, so every intent on it is refused before Admit is
	// ever asked. Reads and snapshots are unaffected: they read whatever
	// has already been ingested, not "as of this instant".
	if it.intent.requiresReadBarrier() && !o.hasReadBarrier() {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateRefused, Err: ErrNoReadBarrier})
		return
	}
	pi := it.intent
	if pi.Interactive && pi.Canonical.AccessEpoch != o.currentAccessEpoch() {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateRefused, Err: errAccessRevoked})
		return
	}
	if pi.Interactive && pi.CommitBy != 0 && o.nowMono() > pi.CommitBy {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateRefused, Err: errCommitDeadline})
		return
	}
	if pi.Token.ID != (TokenID{}) && o.nowMono() > pi.CommitBy {
		res := ownerResult{State: sessionruntime.IntentStateRefused, Err: errCommitDeadline}
		o.recordTokenOutcome(pi, res)
		o.resolve(it, res)
		return
	}
	id, err := o.rt.Admit(pi.Intent)
	if err != nil {
		res := ownerResult{State: sessionruntime.IntentStateRefused, Err: err}
		o.recordTokenOutcome(pi, res)
		o.resolve(it, res)
		return
	}
	pi.admittedID = id
	encoded, err := o.rt.Commit(sessionruntime.Intent{ID: id}, pi.Check)
	if err != nil {
		res := ownerResult{State: o.rt.IntentState(id), Err: err}
		o.recordTokenOutcome(pi, res)
		o.resolve(it, res)
		return
	}
	o.writeStart(it, encoded)
}

// commitResize asks the runtime to commit a geometry (spec §5.6): the PTY
// resize and any reply the emulator's own answer produces both happen
// through this one call, on this one goroutine, so a resize can never land
// between a validated intent and the write it is about to make — the
// runtime's own repairLocked hands its reply bytes to this owner's Reply,
// which queues them here exactly like any other reply.
func (o *sessionOwner) commitResize(it ownerItem) {
	_, err := o.rt.CommitGeometry(*it.resize)
	state := sessionruntime.IntentStateExecuted
	if err != nil {
		state = sessionruntime.IntentStateFailed
	}
	o.resolve(it, ownerResult{State: state, Err: err})
}

// writeStart assigns the item its fence — spec §5.3 step 3's linearisation
// point, the moment nothing else may be written before it — and hands it to
// the writer goroutine. An empty payload (an intent that encoded to nothing)
// still gets a fence and resolves at once: fences order EVERY input item,
// not only the ones that reach the kernel.
//
// A payload never reaches o.writeReq once this owner's writer has been
// detached (o.writerDetached, owner_ssh.go): that writer is gone for good,
// not momentarily busy, and o.writeReq is unbuffered — sending on it to a
// writer nobody will ever again receive from would block run()'s own
// goroutine forever, for this item and every one behind it (nocx-q502e).
// finishNeverOffered resolves it the way spec §5.7's own shutdown pass
// resolves anything "not handed to the writer": cancelled, definitely, never
// left waiting on an answer that cannot come.
func (o *sessionOwner) writeStart(it ownerItem, payload []byte) {
	o.nextFence++
	fence := o.nextFence
	it.payload = payload
	switch {
	case len(payload) == 0:
		o.finishItem(it, fence, writeOutcome{})
	case o.writerDetached():
		o.finishNeverOffered(it, fence)
	default:
		o.writerBusy = true
		o.inFlight = &inFlightItem{item: it, fence: fence}
		o.inFlightFlag.Store(true)
		o.writeReq <- payload
	}
}

// runWriter performs exactly one item's Write at a time, on the same
// non-blocking file the owner reads: the Go runtime poller absorbs EAGAIN
// for a pollable descriptor, so this call blocks the WRITER goroutine only,
// never the owner's own loop. It ends when writeReq is closed, which run
// does only once nothing is in flight — see run's exit condition.
func (o *sessionOwner) runWriter() {
	for payload := range o.writeReq {
		n, err := o.proc.Write(payload)
		o.writeRes <- writeOutcome{n: n, err: err}
	}
}

// completeWrite is the writer reporting back. It is the ONLY place
// writerBusy clears for an ORDINARY write, which is what lets advance hand
// the writer its next item.
//
// fi can be nil here without that being this write's own outcome at all: a
// detached writer's abandoned goroutine is never stopped (performDetach,
// owner_ssh.go), only forgotten, and its eventual send on o.writeRes can
// still land in run's own select while run() keeps looping. That send
// belongs to no in-flight item — performDetach already resolved and cleared
// the one it had — so it is ignored rather than attributed to whatever
// o.inFlight holds next, which used to be exactly this line dereferencing a
// nil pointer (nocx-q502e).
func (o *sessionOwner) completeWrite(res writeOutcome) {
	fi := o.inFlight
	if fi == nil {
		return
	}
	o.writerBusy = false
	o.inFlight = nil
	o.inFlightFlag.Store(false)
	o.finishItem(fi.item, fi.fence, res)
}

// finishItem is the one place an item's fate becomes final: the fence is
// published (inputFence, for whatever reads a frame next), a short write
// becomes io.ErrShortWrite the way Session's own writeLocked used to decide
// it, an intent's record is corrected through ReportOutcome if the write did
// not land whole, and the caller is told.
//
// A failed or short write is reported as FAILED and never as executed —
// nobody may claim those bytes reached the program — and never as cancelled
// either: a write can fail part-way, and the bytes it did take are beyond
// recall.
func (o *sessionOwner) finishItem(it ownerItem, fence sessionruntime.Fence, res writeOutcome) {
	o.completedFence.Store(uint64(fence))

	if it.kind == itemReply {
		o.replyBytes -= len(it.payload)
		if o.replyBytes < 0 {
			o.replyBytes = 0
		}
	}

	writeErr := res.err
	if writeErr == nil && res.n != len(it.payload) {
		writeErr = io.ErrShortWrite
	}
	state := sessionruntime.IntentStateExecuted
	if writeErr != nil {
		state = sessionruntime.IntentStateFailed
	}
	result := ownerResult{State: state, BytesWritten: res.n, FenceAfter: fence, Err: writeErr}
	if it.kind == itemIntent {
		o.rt.ReportOutcome(it.intent.admittedID, writeErr)
		// tokens.go, nocx-6q1uh.4: a token-bearing intent's outcome is
		// recorded so a same-token, same-intent replay (or a
		// session.intent.status poll) sees the SAME answer this write just
		// produced, rather than re-attempting or reporting in_progress
		// forever.
		o.recordTokenOutcome(it.intent, result)
	}
	o.resolve(it, result)
}

// finishNeverOffered resolves an item that will never reach any writer — the
// counterpart to finishItem for the one case finishItem cannot represent:
// this owner's writer is permanently gone (o.detached, set by performDetach,
// owner_ssh.go), so there is no writeOutcome to report and never will be.
// Spec §5.7 draws this exact line for shutdown's own resolution pass ("not
// handed to the writer -> cancelled"); a detached writer being gone for good
// puts every item that reaches writeStart afterward on that same side of it,
// whether it was already queued at the moment of detach or arrives later.
//
// It still publishes the fence — nothing else will, and a caller stamping a
// frame against inputFence must not stall behind one this item was assigned
// but never completed — and it still reports the outcome to the runtime for
// a token-bearing intent, exactly as finishItem does: o.rt.Admit already
// registered it there the moment commitIntent ran, before writeStart ever
// saw it, so the runtime's own record needs closing out the same way.
func (o *sessionOwner) finishNeverOffered(it ownerItem, fence sessionruntime.Fence) {
	o.completedFence.Store(uint64(fence))

	if it.kind == itemReply {
		o.replyBytes -= len(it.payload)
		if o.replyBytes < 0 {
			o.replyBytes = 0
		}
	}

	result := ownerResult{State: sessionruntime.IntentStateCancelled, FenceAfter: fence, Err: errWriterGone}
	if it.kind == itemIntent {
		o.rt.ReportOutcome(it.intent.admittedID, errWriterGone)
		o.recordTokenOutcome(it.intent, result)
	}
	o.resolve(it, result)
}

// resolve answers one item's caller, if there is one to answer: a reply
// carries no done channel, because nothing submitted it through submit and
// nothing is waiting on it.
func (o *sessionOwner) resolve(it ownerItem, res ownerResult) {
	if it.queueSlot {
		o.queueUsed.Add(-1)
	}
	if it.done != nil {
		it.done <- res
	}
}

// Reply implements sessionruntime.ReplySink. It is called REENTRANTLY, from
// inside Session.Ingest, which this owner's own goroutine calls (drainLocal,
// handleReadEvent) — so it must never block, and it does not: it appends
// directly to pending, the same ordered queue submit feeds from outside,
// bypassing the incoming channel because there is nothing to hand off to —
// this call already IS the owner's goroutine.
func (o *sessionOwner) Reply(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if o.replyBytes+len(p) > replyReserveBytes {
		return sessionruntime.ErrReplyReserveFull
	}
	o.replyBytes += len(p)
	o.pending = append(o.pending, ownerItem{kind: itemReply, payload: append([]byte(nil), p...)})
	return nil
}

// PromptBoundary synchronously records OSC 133 B in the session output
// window. Runtime.Ingest calls this before it returns to ingestOne, which
// writes the raw PTY bytes; the subscriber pump can therefore split exactly
// at B even if it is already active and the screen consumer is stalled.
func (o *sessionOwner) PromptBoundary(effect sessionruntime.Effect) error {
	if effect.Kind != sessionruntime.EffectPromptBoundary {
		return errors.New("session owner: ordered boundary callback received another effect kind")
	}
	dropped, err := o.win.markPromptBoundary(effect)
	if err != nil {
		o.log.Warn("prompt boundary could not be ordered with session output", "stream_offset", effect.StreamOffset, "error", err)
		return err
	}
	if dropped {
		o.log.Warn("oldest retained prompt boundary was evicted from the bounded output window", "stream_offset", effect.StreamOffset)
	}
	return nil
}

// submit hands one item to the owner from any OTHER goroutine — hostSession's
// write, resize and (later tasks') intent submission. It never blocks: a
// full queue is errBusy, a closing owner is errOwnerClosing, and either way
// the caller learns at once rather than waiting behind whatever is ahead of
// it.
func (o *sessionOwner) submit(it ownerItem) (<-chan ownerResult, error) {
	o.admissionMu.Lock()
	defer o.admissionMu.Unlock()
	if o.admissionClosed {
		return nil, errOwnerClosing
	}
	select {
	case <-o.closingSignal:
		return nil, errOwnerClosing
	default:
	}
	// One bound spans incoming, pending and in-flight ordinary work. Moving
	// an item into pending must not refund admission while a checked head
	// waits. Access bumps never join pending: incoming's own finite capacity
	// bounds them without letting saturated input prevent revocation.
	if it.kind != itemAccessBump {
		if o.queueUsed.Add(1) > intentQueueMax {
			o.queueUsed.Add(-1)
			return nil, errBusy
		}
		it.queueSlot = true
	}
	done := make(chan ownerResult, 1)
	it.done = done
	select {
	case o.incoming <- it:
		return done, nil
	default:
		if it.queueSlot {
			o.queueUsed.Add(-1)
		}
		return nil, errBusy
	}
}

// inputFence is the highest fence whose write had completed the moment this
// is called (spec §5.4) — read from outside this owner's own goroutine
// (a snapshot reader, nocx-6q1uh.4), which is why completedFence is an
// atomic rather than a plain field like everything else here.
func (o *sessionOwner) inputFence() sessionruntime.Fence {
	return sessionruntime.Fence(o.completedFence.Load())
}

// stop is shutdown, in the order spec §5.7 names: stop admission, request
// termination, interrupt the writer (beginClosing, above, run once
// closingSignal closes), keep reading until EOF, resolve queued items, join,
// close the readable side (run's own defers) — and, only once all of that is
// done, THIS returns, so the caller (hostSession.stop) knows it may safely
// close the runtime and the screen next.
//
// graceful or a zero deadline waits as long as that takes. A deadline that
// fires first forces the readable side closed — which unblocks a read in
// flight and, for a local PTY, a write already interrupted — and reports
// tailLost: true, because the drain that would have picked up the rest
// cannot now occur. It still waits for run to actually finish afterward: a
// caller that got tailLost back is told exactly what could not be promised,
// never handed a session whose goroutines are still deciding its fate.
func (o *sessionOwner) stop(graceful bool, deadline time.Time) (tailLost bool) {
	o.closeOnce.Do(func() { close(o.closingSignal) })
	// Spec §5.7: a stop always has a deadline. No caller may wait on an EOF
	// the process can never be asked to produce: a process that answers a
	// signal (a local PTY, SIGHUP) gets stopGrace for its tail; one that
	// cannot be asked (an ssh channel, a test double) is closed at once.
	// graceful only chooses whether the tail is wanted, never whether the
	// wait is bounded.
	_, askable := o.proc.(ProcessGroupSignaller)
	promisedTail := true
	if deadline.IsZero() {
		deadline = time.Now()
		promisedTail = askable && graceful
		if promisedTail {
			deadline = deadline.Add(stopGrace)
		}
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-o.stopped:
		return false
	case <-timer.C:
		_ = o.proc.Close()
		// A Process with no writeInterrupter (an SSH channel — see that
		// interface's doc) may have a write stuck behind a peer holding its
		// window at zero, which proc.Close's channel close cannot unblock
		// (spec §5.7). Detaching is what lets run's own exit condition
		// become true regardless; for a Process that DOES support
		// interruption, beginClosing already interrupted the write when
		// closing began, and this signal finds nothing in flight to detach.
		if _, ok := o.proc.(writeInterrupter); !ok {
			o.triggerDetach()
		}
		<-o.stopped
		return promisedTail
	}
}
