package transport

// The zero nonce is an environment entry's own end and nothing else: the
// helper's row bridge no longer folds ends into zero-nonce stand-ins
// (nocx-2v80t.3.36). A fenced end closes the current block and promotes the
// queued one; an entry's end marks its attempt entered.

import (
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

// The ordinary fenced end closes the CURRENT block and promotes the queued
// one, which stays open for its own end.
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

// An environment entry's own end — the zero nonce WITH its screen,
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
