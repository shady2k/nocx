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
// (internal/pty's RawReadUntilAgain, for a local PTY) with a SEPARATE
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
// # What this task does not build
//
// SSH sessions run through the fallback reader-goroutine path today, which
// keeps them working, but this task does not give them a read barrier, a
// detach on a stuck writer, or a tainted pool — nocx-6q1uh.3's sibling task
// does. Concretely: if an SSH channel's write blocks forever (no per-channel
// interrupt exists on golang.org/x/crypto/ssh — spec §5.7), this owner's
// writer goroutine blocks with it, and stop() with a deadline that fires
// still waits on it after forcing proc.Close(), because nothing here can
// abandon it safely. The fix is spec §5.7's detached writer, and it belongs
// in the next task alongside the no_read_barrier refusal.
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

	Token     Token
	Canonical canonicalIntent
	CommitBy  int64

	admittedID sessionruntime.IntentID
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
	done chan ownerResult
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
	// intentQueueMax bounds the owner's whole ordered queue — client frames
	// and intents alike, a simplification this task makes deliberately: the
	// spec draws a different bound for intents specifically ("busy") from
	// client frames ("today's lease backpressure"), and one channel serving
	// both is the cheapest thing that keeps either bound meaningful without
	// two queues claiming to order the same items (AGENTS.md, "two surfaces
	// may never own the same input"). Revisit if a schedule ever needs the
	// two bounds to differ.
	intentQueueMax = 64
)

// rawReader is the local-PTY read seam (internal/pty.LocalPty, spec §5.2): a
// readiness half that consumes nothing and a raw non-blocking read(2) loop
// that delivers everything available in one call. A Process without it — an
// SSH channel today — is read through runReaderGoroutine instead, the
// blocking shape internal/pty's old pump used.
type rawReader interface {
	WaitReadable(ctx context.Context) error
	RawReadUntilAgain(buf []byte, deliver func([]byte)) (eof bool, err error)
}

// writeInterrupter unblocks a write in flight (spec §5.7): a local PTY's
// master answers it (internal/pty.LocalPty.InterruptWrite), an SSH channel
// does not — golang.org/x/crypto/ssh's Channel offers no per-channel
// interrupt, which is why the next task detaches a stuck SSH writer instead
// of interrupting it.
type writeInterrupter interface {
	InterruptWrite() error
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

	incoming      chan ownerItem
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

	// --- run()'s own state; touched from nowhere else ----------------------
	pending    []ownerItem
	closing    bool
	writerBusy bool
	inFlight   *inFlightItem
	eofSeen    bool
	replyBytes int
	nextFence  sessionruntime.Fence
}

// newSessionOwner builds the owner over a process that already exists and a
// runtime already directing it. It does not start anything — run does — so a
// caller can bind this owner as the runtime's ReplySink (Session.SetReplies)
// before the first byte is ever read.
func newSessionOwner(proc Process, rt *sessionruntime.Session, win *window, log *slog.Logger) *sessionOwner {
	readCtx, cancelRead := context.WithCancel(context.Background())
	o := &sessionOwner{
		proc:          proc,
		rt:            rt,
		win:           win,
		log:           log,
		incoming:      make(chan ownerItem, intentQueueMax),
		closingSignal: make(chan struct{}),
		stopped:       make(chan struct{}),
		readCtx:       readCtx,
		cancelRead:    cancelRead,
		writeReq:      make(chan []byte),
		writeRes:      make(chan writeOutcome, 1),
		nowMono:       func() int64 { return int64(monoclock.Now()) },
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
	defer func() { _ = o.proc.Close() }()
	defer o.cancelRead()

	go o.runWriter()

	var readableCh chan struct{}
	var readEvents chan readEvent
	if rr, ok := o.proc.(rawReader); ok {
		readableCh = make(chan struct{}, 1)
		go o.runReadiness(rr, readableCh)
	} else {
		readEvents = make(chan readEvent, 1)
		go o.runReaderGoroutine(readEvents)
	}

	for {
		if o.closing && o.eofSeen && !o.writerBusy && o.inFlight == nil && len(o.pending) == 0 {
			close(o.writeReq)
			return
		}
		select {
		case <-o.closingSignal:
			if !o.closing {
				o.beginClosing()
			}
		case it := <-o.incoming:
			switch {
			case it.kind == itemAccessBump:
				// Ahead of everything already queued, whether or not the
				// owner is closing (spec §7.2: a bump always applies, and
				// carries no refusal of its own) — see access.go's own doc
				// for why this cannot wait for its turn in o.pending.
				o.applyAccessBump(it)
			case o.closing:
				o.resolve(it, ownerResult{State: sessionruntime.IntentStateCancelled, Err: errOwnerClosing})
			case it.kind == itemIntent && it.intent.Token.ID != (TokenID{}) && o.tokenGate(it, it.intent):
				// tokenGate (tokens.go) resolved it directly: a replay of an
				// already-recorded or in-flight token, or a refusal decided
				// before this intent is ever admitted (forged, expired,
				// token_spent, access_revoked, commit_deadline). Nothing
				// left to queue.
			default:
				o.pending = append(o.pending, it)
			}
		case <-readableCh:
			o.drainLocal()
		case ev := <-readEvents:
			o.handleReadEvent(ev)
		case res := <-o.writeRes:
			o.completeWrite(res)
		}
		o.advance()
	}
}

// beginClosing is stop admission (spec §5.7's first step), run exactly once:
// every item still waiting — never handed to the writer — is cancelled at
// once, termination is requested and a write already in flight is
// interrupted. What is already in flight resolves on its own terms
// (completeWrite), never cancelled: bytes on a PTY cannot be recalled.
func (o *sessionOwner) beginClosing() {
	o.closing = true
	for _, it := range o.pending {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateCancelled, Err: errOwnerClosing})
	}
	o.pending = nil
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

// drainLocal is the commit point's drain step (spec §5.3.1) for a local PTY:
// read to EAGAIN, ingesting every chunk as it arrives, without blocking. It
// is a no-op once EOF has been seen, and a no-op for a Process with no raw
// read seam — that Process is read by runReaderGoroutine instead, which
// delivers through readEvents rather than through this call.
func (o *sessionOwner) drainLocal() {
	if o.eofSeen {
		return
	}
	rr, ok := o.proc.(rawReader)
	if !ok {
		return
	}
	buf := make([]byte, pageSize)
	eof, err := rr.RawReadUntilAgain(buf, o.ingestOne)
	if eof || err != nil {
		o.finishRead(err)
	}
}

// runReadiness is the readiness half spec §5.2 splits from reading, for a
// Process whose master supports it: it consumes nothing, ever — every read
// is this owner's own, through drainLocal — and it exists only to wake the
// owner when there is something to drain.
func (o *sessionOwner) runReadiness(rr rawReader, readableCh chan<- struct{}) {
	for {
		if err := rr.WaitReadable(o.readCtx); err != nil {
			return
		}
		select {
		case readableCh <- struct{}{}:
		default:
			// A signal is already pending; the owner has not caught up to it
			// yet, and a second one would tell it nothing a drain has not
			// already answered.
		}
	}
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
	if err := o.rt.Ingest(b); err != nil {
		if errors.Is(err, emulator.ErrClosed) || errors.Is(err, sessionruntime.ErrUnavailable) {
			o.log.Debug("session output arrived after the terminal closed", "bytes", len(b), "err", err)
		} else {
			o.log.Warn("session output not ingested", "bytes", len(b), "err", err)
		}
	}
	o.win.write(b)
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
	if err != nil && !errors.Is(err, io.EOF) {
		o.log.Warn("session output ended", "err", err)
	}
	o.win.close()
	o.cancelRead()
}

// advance is where an idle writer picks up the next item, if there is one.
func (o *sessionOwner) advance() {
	if o.writerBusy || len(o.pending) == 0 {
		return
	}
	it := o.pending[0]
	o.pending = o.pending[1:]
	o.processHead(it)
}

// processHead is the commit point (spec §5.3): an opportunistic extra drain
// so validation sees the freshest possible screen, then the item's own
// handling. A reply and a client frame are unconditional bytes; a resize
// asks the runtime to commit a geometry; an intent is validated and encoded
// through Session.Commit before anything is handed to the writer.
func (o *sessionOwner) processHead(it ownerItem) {
	o.drainLocal()
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
	pi := it.intent
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
func (o *sessionOwner) writeStart(it ownerItem, payload []byte) {
	o.nextFence++
	fence := o.nextFence
	it.payload = payload
	if len(payload) == 0 {
		o.finishItem(it, fence, writeOutcome{})
		return
	}
	o.writerBusy = true
	o.inFlight = &inFlightItem{item: it, fence: fence}
	o.writeReq <- payload
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
// writerBusy clears, which is what lets advance hand the writer its next
// item.
func (o *sessionOwner) completeWrite(res writeOutcome) {
	o.writerBusy = false
	fi := o.inFlight
	o.inFlight = nil
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

// resolve answers one item's caller, if there is one to answer: a reply
// carries no done channel, because nothing submitted it through submit and
// nothing is waiting on it.
func (o *sessionOwner) resolve(it ownerItem, res ownerResult) {
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

// hasReadBarrier reports whether this session's process offers spec §5.2's
// read barrier: a local PTY's readiness-plus-drain-to-EAGAIN loop (rawReader,
// above) guarantees the owner has ingested everything a program could have
// produced before this instant, and an SSH channel (internal/helper/sshsvc,
// nocx-6q1uh.3) does not, because ssh.Channel offers no readiness boundary a
// blocking reader can be drained against.
//
// o.proc is set once at construction and never reassigned, so this is safe
// to call from any goroutine — the same reasoning rawReader's own two call
// sites (run, drainLocal) already rely on, just read here instead of
// switched on.
func (o *sessionOwner) hasReadBarrier() bool {
	_, ok := o.proc.(rawReader)
	return ok
}

// submit hands one item to the owner from any OTHER goroutine — hostSession's
// write, resize and (later tasks') intent submission. It never blocks: a
// full queue is errBusy, a closing owner is errOwnerClosing, and either way
// the caller learns at once rather than waiting behind whatever is ahead of
// it.
func (o *sessionOwner) submit(it ownerItem) (<-chan ownerResult, error) {
	select {
	case <-o.closingSignal:
		return nil, errOwnerClosing
	default:
	}
	done := make(chan ownerResult, 1)
	it.done = done
	select {
	case o.incoming <- it:
		return done, nil
	default:
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
	if graceful || deadline.IsZero() {
		<-o.stopped
		return false
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-o.stopped:
		return false
	case <-timer.C:
		_ = o.proc.Close()
		<-o.stopped
		return true
	}
}
