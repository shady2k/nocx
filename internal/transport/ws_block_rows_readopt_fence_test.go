package transport

// nocx-zg3k3.35: a coordinator replaced between a command's completion and
// its interval end. The completion's frame is applied and stored — the
// lifecycle cursor the replacing coordinator resumes from is past it, so the
// completion is never offered again — but the fence it published lived only
// in the gone process's memory. The helper resends the unconfirmed end; the
// replacing coordinator must still be able to match it, or the re-bound block
// stays current forever and every later command's block queues behind it.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

func TestAnEndWhoseCompletionTheGoneCoordinatorStoredSealsTheReboundBlock(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	createSessionRow(t, db, sidStr)
	e.ws.AttachBlockRows(sid)

	first := startsACommand(t, e, pub, lane, h, 2, "answer the question")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("asked")}, ""); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	fence := lifecycleFence(0x5a)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, fence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))

	// The coordinator is replaced before the end marker reached it: the
	// completion is stored, its in-memory fence is gone with the process.
	e.ws.DetachBlockRows(sid)
	e.ws.AttachBlockRows(sid)

	// The helper resends the end the gone coordinator never confirmed.
	e.ws.BlockIntervalEnded(sid, fence, 1, []emulator.Row{aStreamRow("$ ")}, false)

	if art := blockArtifact(t, db, first); art.State != content.ArtifactSealed {
		t.Fatalf("the re-bound block is %q after its resent end, want sealed", art.State)
	}

	// And what a person sees: the next command's block is not queued behind
	// a block that can never close — its own end seals it.
	second := startsACommand(t, e, pub, lane, h, 5, "echo AFTER")
	if _, confirm := e.ws.BlockRowsArrived(sid, 1, 0, []emulator.Row{aStreamRow("AFTER")}, ""); !confirm {
		t.Fatal("the next command's row was not confirmed")
	}
	next := lifecycleFence(0x5b)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, next)))
	e.ws.BlockIntervalEnded(sid, next, 2, []emulator.Row{aStreamRow("$ ")}, false)
	if art := blockArtifact(t, db, second); art.State != content.ArtifactSealed {
		t.Fatalf("the command after the replacement is %q after its end, want sealed", art.State)
	}
}

// fenceRefusingStore is the real ledger with the fence write refused.
type fenceRefusingStore struct {
	closeFailureBlockStore
}

func (s *fenceRefusingStore) RecordBlockRowsFence(context.Context, content.RecordBlockRowsFence) error {
	return errors.New("fence write refused")
}

// The paired failure half: a store that refuses the fence costs only the
// replacement case. The coordinator that saw the completion still holds the
// fence in memory, and the end it is owed seals the block as before.
func TestARefusedFenceWriteStillSealsTheBlockInTheSameCoordinator(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	createSessionRow(t, db, sidStr)
	e.ws.blockRowsStore = &fenceRefusingStore{closeFailureBlockStore{ledger: db.Ledger()}}
	e.ws.AttachBlockRows(sid)

	attempt := startsACommand(t, e, pub, lane, h, 2, "answer the question")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("asked")}, ""); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	fence := lifecycleFence(0x5c)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 1, []emulator.Row{aStreamRow("$ ")}, false)

	if art := blockArtifact(t, db, attempt); art.State != content.ArtifactSealed {
		t.Fatalf("the block is %q after its end with the fence write refused, want sealed", art.State)
	}
}

// A recovered completion can outlive the helper end that would normally close
// its interval. Once a later interval has itself ended, that later authenticated
// boundary proves the recovered block cannot still own the stream; settle it as
// a gap and let the later block close rather than leaving both queued forever.
func TestRecoveredOpenBlockWithoutResentEndSettlesAsGapBeforeLaterEnd(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	createSessionRow(t, db, sidStr)
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)

	first := startsACommand(t, e, pub, lane, h, 2, "printf recovered")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("recovered")}, ""); !confirm {
		t.Fatal("the recovered block row was not confirmed")
	}
	fenceA := lifecycleFence(0x71)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(first), 0, fenceA)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))

	// The old coordinator goes away after storing A's completion but before
	// seeing its end. The replacement has no end to replay for A.
	e.ws.BlockOutputStartPlaneAttached(sid)
	e.ws.DetachBlockRows(sid)
	e.ws.AttachBlockRows(sid)

	// The helper's retained-output stream marks this as a recovered interval;
	// model that state directly because the transport fixture does not run a
	// real helper reader.
	e.ws.blockStream.mu.Lock()
	e.ws.blockStream.current[sid].recoveredOutputInterval = true
	e.ws.blockStream.mu.Unlock()

	second := startsACommand(t, e, pub, lane, h, 5, "printf later")
	if _, confirm := e.ws.BlockRowsArrived(sid, 1, 0, []emulator.Row{aStreamRow("later")}, ""); !confirm {
		t.Fatal("the later block row was not confirmed")
	}
	fenceB := lifecycleFence(0x72)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(second), 0, fenceB)))
	e.ws.BlockIntervalEnded(sid, fenceB, 2, []emulator.Row{aStreamRow("$ ")}, false)

	if art := blockArtifact(t, db, first); art.State != content.ArtifactSealed || art.Truncated == nil || *art.Truncated != content.TruncGap {
		t.Fatalf("recovered block A = state %q truncated %v, want sealed gap", art.State, art.Truncated)
	}
	if art := blockArtifact(t, db, second); art.State != content.ArtifactSealed {
		t.Fatalf("later block B is %q after its end, want sealed", art.State)
	}
}

func TestRecoveredBlockEndClosesWhileRowsAwaitOutputReplay(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	createSessionRow(t, db, sidStr)
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf recovered")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("stored")}, ""); !confirm {
		t.Fatal("the stored row was not confirmed")
	}
	fence := lifecycleFence(0x73)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))
	e.ws.DetachBlockRows(sid)
	e.ws.AttachBlockRows(sid)
	// Model the replacement's recovered-output replay gate: retained rows
	// cannot reach the artifact until the helper repeats its output mark.
	e.ws.blockStream.mu.Lock()
	block := e.ws.blockStream.current[sid]
	block.recoveredOutputInterval = true
	block.outputStartKnown = true
	block.outputStartMarked = true
	block.outputStartRow = 1
	block.awaitingOutputReplay = true
	e.ws.blockStream.mu.Unlock()

	if up, confirm := e.ws.BlockRowsArrived(sid, 1, 0, []emulator.Row{aStreamRow("held replay")}, ""); confirm || up != 0 {
		t.Fatalf("row awaiting replay ack = (%d, %v), want held", up, confirm)
	}
	e.ws.BlockIntervalEnded(sid, fence, 2, []emulator.Row{aStreamRow("$ ")}, false)
	if art := blockArtifact(t, db, attempt); art.State != content.ArtifactSealed {
		t.Fatalf("recovered block is %q after its end during held replay, want sealed", art.State)
	}
}
