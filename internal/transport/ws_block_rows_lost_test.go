package transport

// A block whose boundary never arrived whole (nocx-2v80t.3.29): its
// completion was accepted here and finally failed to reach the helper, so the
// helper can never seal it; or the helper settled it without its fence
// (ADR-0074 decision 3). Either way the block is sealed with what reached the
// store and stored as a gap — the artifact's truncated 'gap', which is what
// the renderer paints "Output incomplete" from — and it is said closed
// exactly once, whatever else closes the session around it.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// blockArtifact is the block rows artifact an entry carries, as ledger.get
// reads its metadata.
func blockArtifact(t *testing.T, db content.ContentDB, entryID string) *content.Artifact {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil || row == nil {
		t.Fatalf("Entry(%s) = %v, %v", entryID, row, err)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType != content.MediaBlockRows {
				continue
			}
			art, err := db.Ledger().Artifact(context.Background(), ex.Artifacts[i].ID)
			if err != nil || art == nil {
				t.Fatalf("Artifact(%s) = %v, %v", ex.Artifacts[i].ID, art, err)
			}
			return art
		}
	}
	t.Fatalf("entry %s has no block rows artifact", entryID)
	return nil
}

func assertSealedAs(t *testing.T, db content.ContentDB, entryID string, want *content.Truncation) {
	t.Helper()
	art := blockArtifact(t, db, entryID)
	if art.State != content.ArtifactSealed {
		t.Fatalf("block %s is %q, want sealed", entryID, art.State)
	}
	switch {
	case want == nil && art.Truncated != nil:
		t.Fatalf("block %s is stored truncated %q, want whole", entryID, *art.Truncated)
	case want != nil && (art.Truncated == nil || *art.Truncated != *want):
		t.Fatalf("block %s is stored truncated %v, want %q", entryID, art.Truncated, *want)
	}
}

// closedCount is how many block.closed the subscriber was sent for the entry.
// A sentinel notification is sent last, and the socket is one ordered stream,
// so once it arrives every block.closed sent before it is already in hand —
// counted without waiting on a duration.
func closedCount(t *testing.T, e *lifecycleTestEnv, sid session.ID, entryID string) int {
	t.Helper()
	e.ws.notifyBlockSubscriber(sid, "test.sentinel", struct{}{})
	if _, err := awaitFrame(e.conn, time.Now().Add(wantWithin), isNotification("test.sentinel")); err != nil {
		t.Fatalf("the sentinel never arrived: %v", err)
	}
	n := 0
	for {
		msg, err := awaitFrame(e.conn, time.Time{}, isNotification("block.closed"))
		if err != nil {
			return n
		}
		frame, _ := decodeFrame(msg)
		var got blockClosedParams
		if err := json.Unmarshal(frame.Params, &got); err != nil {
			t.Fatalf("block.closed params: %v", err)
		}
		if got.EntryID == entryID {
			n++
		}
	}
}

var gap = func() *content.Truncation { g := content.TruncGap; return &g }()

// completedWithRows starts a command, streams one row of it and has the
// kernel accept its completion — the state a lost delivery leaves: the fence
// is known here, and the helper will never be told.
func completedWithRows(t *testing.T, fence lifecycle.FenceNonce) (*lifecycleTestEnv, session.ID, string, content.ContentDB) {
	t.Helper()
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "make")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("building")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	return e, session.ID(sid), attempt, db
}

func TestABoundaryWhoseDeliveryWasLostSealsItsBlockAsAGap(t *testing.T) {
	fence := lifecycleFence(0x61)
	e, sid, attempt, db := completedWithRows(t, fence)

	e.ws.BlockBoundaryLost(sid, fence)

	assertSealedAs(t, db, attempt, gap)
	if rows := streamRows(t, db, attempt); len(rows) != 1 || rows[0].Text != "building" {
		t.Fatalf("the lost block's rows = %+v, want what reached the store", rows)
	}
	if n := closedCount(t, e, sid, attempt); n != 1 {
		t.Fatalf("block.closed sent %d times, want once", n)
	}
}

// Paired: the same completion delivered, its end marker arriving whole.
func TestABoundaryDeliveredSealsItsBlockWhole(t *testing.T) {
	fence := lifecycleFence(0x62)
	e, sid, attempt, db := completedWithRows(t, fence)

	e.ws.BlockIntervalEnded(sid, fence, 1, []emulator.Row{aStreamRow("$ ")}, false)

	assertSealedAs(t, db, attempt, nil)
	if n := closedCount(t, e, sid, attempt); n != 1 {
		t.Fatalf("block.closed sent %d times, want once", n)
	}
}

// An interval the helper settled without its fence says so on its end
// marker, and the block is stored as a gap (ADR-0074 decision 3).
func TestAnIntervalSettledWithoutItsFenceIsStoredAsAGap(t *testing.T) {
	fence := lifecycleFence(0x63)
	e, sid, attempt, db := completedWithRows(t, fence)

	e.ws.BlockIntervalEnded(sid, fence, 1, nil, true)

	assertSealedAs(t, db, attempt, gap)
	if n := closedCount(t, e, sid, attempt); n != 1 {
		t.Fatalf("block.closed sent %d times, want once", n)
	}
}

// A lost environment entry: the block current at the entry is the one it
// would have sealed, so that one is sealed as a gap, and the attempt that
// runs on under the child does not open a second block.
func TestAnEntryWhoseDeliveryWasLostSealsTheCurrentBlockAsAGap(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "ssh host")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("Welcome")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	e.ws.BlockBoundaryLost(sid, [32]byte{})

	assertSealedAs(t, db, attempt, gap)
	e.ws.blockStream.mu.Lock()
	_, entered := e.ws.blockStream.entered[sid][attempt]
	e.ws.blockStream.mu.Unlock()
	if !entered {
		t.Fatal("the attempt a lost entry sealed is not remembered as entered; its reopening would open a second block")
	}
	if n := closedCount(t, e, sid, attempt); n != 1 {
		t.Fatalf("block.closed sent %d times, want once", n)
	}
}

// THE ORDER AGAINST THE SESSION'S END. A loss and a detach name the same
// unended block; whichever comes first settles it, and the other neither
// seals it again nor says it closed again.
func TestALossAndADetachSettleOneBlockOnce(t *testing.T) {
	t.Run("the loss first", func(t *testing.T) {
		fence := lifecycleFence(0x64)
		e, sid, attempt, db := completedWithRows(t, fence)

		e.ws.BlockBoundaryLost(sid, fence)
		e.ws.DetachBlockRows(sid)

		assertSealedAs(t, db, attempt, gap)
		if n := closedCount(t, e, sid, attempt); n != 1 {
			t.Fatalf("block.closed sent %d times, want once", n)
		}
	})
	t.Run("the detach first", func(t *testing.T) {
		fence := lifecycleFence(0x65)
		e, sid, attempt, db := completedWithRows(t, fence)

		e.ws.DetachBlockRows(sid)
		e.ws.BlockBoundaryLost(sid, fence)

		// The detach sealed it as the session's end seals everything it
		// holds, and the loss arriving after found nothing left to settle.
		assertSealedAs(t, db, attempt, nil)
		if n := closedCount(t, e, sid, attempt); n != 1 {
			t.Fatalf("block.closed sent %d times, want once", n)
		}
	})
}

// An end marker for a fence already settled as lost — the attempt that
// timed out had landed after all — is dropped rather than parked: parked, it
// would hold every later row back as the next interval's for good.
func TestAnEndForALostBoundaryIsDropped(t *testing.T) {
	fence := lifecycleFence(0x66)
	e, sid, attempt, db := completedWithRows(t, fence)

	e.ws.BlockBoundaryLost(sid, fence)
	e.ws.BlockIntervalEnded(sid, fence, 1, []emulator.Row{aStreamRow("$ ")}, false)

	e.ws.blockStream.mu.Lock()
	parked := len(e.ws.blockStream.ends[sid])
	e.ws.blockStream.mu.Unlock()
	if parked != 0 {
		t.Fatalf("an end for a lost boundary was parked (%d ends), want dropped", parked)
	}
	assertSealedAs(t, db, attempt, gap)
	if n := closedCount(t, e, sid, attempt); n != 1 {
		t.Fatalf("block.closed sent %d times, want once", n)
	}
}

// detachDuringCloseStore runs the session's detach from inside the first
// seal it is asked for: the interleaving where a close is still writing its
// block when the session ends around it.
type detachDuringCloseStore struct {
	closeFailureBlockStore
	detach func()
	once   bool
}

func (s *detachDuringCloseStore) CloseBlockRows(ctx context.Context, in content.CloseBlockRows) (content.BlockRowsSummary, error) {
	if !s.once {
		s.once = true
		s.detach()
	}
	return s.closeFailureBlockStore.CloseBlockRows(ctx, in)
}

// The same, interleaved: the session's detach lands while the loss is still
// sealing the block. The detach finds the block unsettled and settles it; the
// loss's own seal then changes nothing, and it does not say closed again.
func TestADetachDuringALostBoundarysSealSaysClosedOnce(t *testing.T) {
	fence := lifecycleFence(0x67)
	e, sid, attempt, db := completedWithRows(t, fence)
	store := &detachDuringCloseStore{closeFailureBlockStore: closeFailureBlockStore{ledger: db.Ledger()}}
	store.detach = func() { e.ws.DetachBlockRows(sid) }
	e.ws.blockRowsStore = store

	e.ws.BlockBoundaryLost(sid, fence)

	assertBlockSealed(t, db, attempt)
	if n := closedCount(t, e, sid, attempt); n != 1 {
		t.Fatalf("block.closed sent %d times, want once", n)
	}
}
