package transport

// Ends the helper's row bridge FOLDED past its marker budget
// (nocx-2v80t.3.31) reach this stream as zero-nonce ends settled without a
// fence. They are no longer rare, so the zero nonce's resolution has to hold
// for every state a block can be in (nocx-2v80t.3.32): a folded end with no
// current block settles the block queued next, and settling a block by the
// zero nonce leaves nothing of that block behind — not its fence, not an
// environment entry's mark it never had.

import (
	"encoding/hex"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// twoCommands starts a command, streams a row of it and has the kernel accept
// its completion with the fence, then starts a second one, which is opened
// and queued behind the first: the first's end marker has not arrived.
func twoCommands(t *testing.T, fence lifecycle.FenceNonce) (*lifecycleTestEnv, session.ID, string, string) {
	t.Helper()
	e, pub, lane, h, sidStr, _ := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	first := startsACommand(t, e, pub, lane, h, 2, "make one")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("one")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, fence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	second := startsACommand(t, e, pub, lane, h, 5, "make two")
	e.ws.blockStream.mu.Lock()
	queued := e.ws.blockStream.queued[sid]
	e.ws.blockStream.mu.Unlock()
	if queued != second {
		t.Fatalf("the second command is queued as %q, want %q", queued, second)
	}
	return e, sid, first, second
}

// A folded end arriving while no block is current — the promotion of the
// queued one held back by an open still in flight — settles the queued
// block rather than being dropped, which left it open for good.
func TestAFoldedEndWithNoCurrentBlockSettlesTheQueuedOne(t *testing.T) {
	e, sid, first, second := twoCommands(t, lifecycleFence(0x70))
	db := e.ws.contentDB
	// The first block closed and the second could not be promoted yet.
	e.ws.blockStream.mu.Lock()
	delete(e.ws.blockStream.current, sid)
	delete(e.ws.blockStream.open[sid], first)
	e.ws.blockStream.mu.Unlock()

	e.ws.BlockIntervalEnded(sid, [32]byte{}, 1, nil, true)

	assertSealedAs(t, db, second, gap)
	if n := closedCount(t, e, sid, second); n != 1 {
		t.Fatalf("block.closed for the queued block sent %d times, want once", n)
	}
	e.ws.blockStream.mu.Lock()
	queued := e.ws.blockStream.queued[sid]
	e.ws.blockStream.mu.Unlock()
	if queued != "" {
		t.Fatalf("the settled block is still queued as %q", queued)
	}
}

// Paired: the ordinary fenced end closes the CURRENT block and promotes the
// queued one, which stays open for its own end.
func TestAFencedEndClosesTheCurrentBlockAndPromotesTheQueuedOne(t *testing.T) {
	fence := lifecycleFence(0x71)
	e, sid, first, second := twoCommands(t, fence)
	db := e.ws.contentDB

	e.ws.BlockIntervalEnded(sid, fence, 1, []emulator.Row{aStreamRow("$ ")}, false)

	assertSealedAs(t, db, first, nil)
	e.ws.blockStream.mu.Lock()
	current := e.ws.blockStream.current[sid]
	e.ws.blockStream.mu.Unlock()
	if current == nil || current.attempt != second {
		t.Fatalf("current after the fenced end = %+v, want the queued %q promoted", current, second)
	}
}

// A folded end settles a block whose completion came first: its fence is
// published and no end with it will ever come, so the settle drops it, and a
// folded end is not an environment entry, so the attempt is not marked as
// one — either would sit in the stream until detach.
func TestAFoldedEndLeavesNoFenceOrEntryMarkBehind(t *testing.T) {
	fence := lifecycleFence(0x72)
	e, sid, first, _ := twoCommands(t, fence)
	db := e.ws.contentDB

	e.ws.BlockIntervalEnded(sid, [32]byte{}, 1, nil, true)

	assertSealedAs(t, db, first, gap)
	e.ws.blockStream.mu.Lock()
	_, fenceLeft := e.ws.blockStream.fences[sid][hex.EncodeToString(fence[:])]
	_, entered := e.ws.blockStream.entered[sid][first]
	e.ws.blockStream.mu.Unlock()
	if fenceLeft {
		t.Fatal("the settled block's fence is still held, waiting for an end that will never come")
	}
	if entered {
		t.Fatal("a folded end marked its block as an environment entry's")
	}
}

// Paired: an environment entry's own end — the zero nonce WITH its screen,
// not settled as lost — still marks the attempt entered, so the attempt
// running on under the child opens no second block.
func TestAnEntrysOwnEndStillMarksItsAttemptEntered(t *testing.T) {
	e, sid, first, _ := twoCommands(t, lifecycleFence(0x73))

	e.ws.BlockIntervalEnded(sid, [32]byte{}, 1, []emulator.Row{aStreamRow("password:")}, false)

	e.ws.blockStream.mu.Lock()
	_, entered := e.ws.blockStream.entered[sid][first]
	e.ws.blockStream.mu.Unlock()
	if !entered {
		t.Fatal("an environment entry's end did not mark its attempt entered")
	}
}
