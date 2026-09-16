package session

// The owner's SSH-specific behaviour (nocx-6q1uh.3, spec §5.2, §5.7): the
// no-read-barrier refusal and the detached-writer path. Kept out of owner.go
// on purpose — nocx-6q1uh.4 (structural digest, snapshot ring, one-shot
// tokens) also lands a small commit-point hook there, and the two tasks
// editing one file at once is only survivable if each keeps to its own
// region of it. Everything that does not need to be a struct field or a
// case in run's own select lives here instead.

import (
	"errors"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// readMode names which read seam a session's Process answers (spec §5.2).
// readRawLocal has the poll-then-nonblocking-read barrier a local PTY's
// master gives (rawReader, owner.go): the owner's OWN read is what a check
// function's snapshot is built from, so the screen it sees can be proven
// current. readViaReader is the fallback blocking-goroutine shape — today,
// an SSH channel (sshsvc.ShellChannel) — where a blocking Read may already
// be holding bytes this owner has not ingested, so nothing minted against
// "the screen right now" can be trusted.
type readMode int

const (
	readRawLocal readMode = iota
	readViaReader
)

// mode is which seam this session's Process answers — the same type
// assertion run already makes (to choose between drainLocal and
// runReaderGoroutine), named here so a caller can ask the question without
// re-deriving it.
func (o *sessionOwner) mode() readMode {
	if _, ok := o.proc.(rawReader); ok {
		return readRawLocal
	}
	return readViaReader
}

// hasReadBarrier reports whether this session's read side can be drained
// without ever blocking (spec §5.2). false means commitIntent refuses every
// state-changing intent on it (ErrNoReadBarrier) — reads and snapshots are
// unaffected, since they read whatever the runtime has already ingested
// rather than requiring proof of "right now".
func (o *sessionOwner) hasReadBarrier() bool {
	return o.mode() == readRawLocal
}

// ErrNoReadBarrier is why an intent on a session with no read barrier is
// refused — spec §5.2 and §6.5's `no_read_barrier` cause. Exported: a later
// task's wire translation (nocx-6q1uh.4/.5) and a test in another package
// both need to recognise it by value, and a second string spelling the same
// cause would be a second answer to "why was this refused" (AGENTS.md, "Look
// for the existing answer").
var ErrNoReadBarrier = errors.New("session: no_read_barrier: this session's read side gives no barrier to validate an intent against")

// errDeliveryUnknown is what a detached write resolves with (spec §6.5's
// `delivery_unknown`): its completion was never reported, because this
// owner gave up joining the writer before the write returned. The bytes may
// or may not have reached the program; neither this owner nor its caller
// may claim either.
var errDeliveryUnknown = errors.New("session: delivery_unknown: the writer was detached before its outcome was observed")

// errWriterGone is what an item resolves with when it reaches writeStart
// AFTER this owner's writer has already been detached (nocx-q502e): unlike
// errDeliveryUnknown — reserved for the one item that WAS already handed to
// the abandoned writer, whose fate is genuinely unknown — this item was
// never offered to any writer at all, so its fate is certain: it did not
// happen. Spec §5.7 draws exactly this line for shutdown's own resolution
// pass ("not handed to the writer -> cancelled; in flight -> ... or
// delivery_unknown"); a detached writer is permanently gone, so every item
// behind it falls on the "not handed" side from the moment of detach on.
var errWriterGone = errors.New("session: cancelled: the writer was detached before this item could ever reach it")

// writerDetacher is asked, once, to give up on a write this owner cannot
// interrupt (spec §5.7): golang.org/x/crypto/ssh offers no per-channel
// interrupt, so the only way to stop waiting on a write stuck behind a
// zero-sized window is to abandon it. Detach taints the pooled connection it
// was writing to (internal/ssh's ConnPool.Taint, reached through
// sshsvc.ShellChannel.Taint) so the pool hands it to no new caller, and —
// past the helper's detached-writer cap — closes it at once instead,
// ending every sibling channel on that connection too. It is called from
// run's own goroutine (performDetach) and must not block.
type writerDetacher interface {
	Detach()
}

// triggerDetach is stop's own signal (owner.go) that its deadline fired
// against a Process it cannot interrupt. It only asks; performDetach is what
// actually mutates state, and it runs on run's own goroutine because
// inFlight, writerBusy and the rest of run's bookkeeping belong to nobody
// else (owner.go's own package doc). Safe to call more than once — only the
// first close of detachSignal has any effect.
func (o *sessionOwner) triggerDetach() {
	o.detachOnce.Do(func() { close(o.detachSignal) })
}

// performDetach is run's own reaction to triggerDetach (spec §5.7's last
// resort). The in-flight write, if there still is one, resolves
// delivery_unknown and is forgotten: writerBusy and inFlight clear, but —
// unlike an ordinary completeWrite — PERMANENTLY: o.detached, set below, is
// what writeStart now checks (owner.go) before ever sending on o.writeReq
// again, so clearing writerBusy here does not reopen the writer to advance().
// This owner's one writer goroutine is gone for good the instant this method
// runs, never merely idle.
//
// That writer goroutine is not stopped here — it keeps running, abandoned,
// blocked on the real write, until the Process's own Detach ends its
// connection (the peer closing the channel, or the pool closing a tainted
// one). Its eventual send on writeRes is NOT necessarily read by nobody: if
// run() is still looping when that send lands, run's own select still has a
// case reading it (owner.go's run), and completeWrite recognises an outcome
// belonging to no in-flight item (o.inFlight nil, this method having already
// cleared it) and ignores it rather than acting on it — which is the fix
// nocx-q502e is. What "never blocks" actually describes is narrower than
// that former comment claimed: writeRes's one-slot buffer guarantees the
// abandoned goroutine's own send can always complete, whether or not run()
// is still around to read it.
//
// It is a no-op past the first call (o.detached) and a no-op when nothing
// was actually in flight: a forced stop against an idle SSH session's
// writer must not report writerDetached when there was nothing to detach.
func (o *sessionOwner) performDetach() {
	if o.detached.Load() {
		return
	}
	fi := o.inFlight
	if fi == nil {
		return
	}
	o.inFlight = nil
	o.writerBusy = false
	o.inFlightFlag.Store(false)
	// Marked detached BEFORE resolve: resolve can run a caller's own
	// continuation synchronously (a buffered channel send, never blocking,
	// but still code this owner does not control), and nothing after this
	// point may observe a window where the in-flight item is already gone
	// but writeStart would still treat the writer as available.
	o.detached.Store(true)
	o.resolve(fi.item, ownerResult{
		State:      sessionruntime.IntentStateFailed,
		FenceAfter: fi.fence,
		Err:        errDeliveryUnknown,
	})
	if wd, ok := o.proc.(writerDetacher); ok {
		wd.Detach()
	}
}

// writerDetached reports whether stop gave up on joining a stuck writer
// rather than waiting for it to actually finish (spec §5.7). Meaningful only
// after stop has returned, for an external caller — but it is also read from
// run's own goroutine, inside writeStart (owner.go), the instant o.detached
// is set (performDetach, above), which is what keeps a detached owner from
// ever handing its permanently-gone writer another payload.
func (o *sessionOwner) writerDetached() bool {
	return o.detached.Load()
}
