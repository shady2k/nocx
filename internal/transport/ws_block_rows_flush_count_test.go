package transport

// The auto-flush handoff, through the product's own entry point
// (nocx-2v80t.3.51, reopened part 7): BlockRowsArrived's own flush branch
// takes the batch out of bs.pending and counts it against the buffer bound
// in the same lock hold — takeForFlushLocked — so a full-budget arrival in
// the batch's flight overflows (the block in flight ends incomplete) instead
// of being admitted behind it. The stage review's mutation removed the count
// at that branch and every test stayed green because they drove hand-built
// flushes; this one drives BlockRowsArrived until the branch fires, with the
// store's append parked at a gate the test controls.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/session"
)

// failFirstAppendStore refuses the FIRST AppendBlockRows once — a store that
// refuses one write is the real shape requeuePendingRows exists for — and
// hands every later append to the blocking store's gate.
type failFirstAppendStore struct {
	blocking *blockingAppendStore
	spent    atomic.Bool
}

func newFailFirstAppendStore(ledger content.LedgerRepository) *failFirstAppendStore {
	return &failFirstAppendStore{blocking: newBlockingAppendStore(ledger)}
}

func (s *failFirstAppendStore) OpenBlockOutput(ctx context.Context, in content.OpenBlockOutput) (string, error) {
	return s.blocking.OpenBlockOutput(ctx, in)
}

func (s *failFirstAppendStore) AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error {
	if s.spent.CompareAndSwap(false, true) {
		return fmt.Errorf("injected first append refusal")
	}
	return s.blocking.AppendBlockRows(ctx, in)
}

func (s *failFirstAppendStore) CloseBlockRows(ctx context.Context, in content.CloseBlockRows) (content.BlockRowsSummary, error) {
	return s.blocking.CloseBlockRows(ctx, in)
}

func (s *failFirstAppendStore) RecordClearBoundary(ctx context.Context, in content.RecordClearBoundary) (content.ClearBoundaryRecorded, error) {
	return s.blocking.RecordClearBoundary(ctx, in)
}

func (s *failFirstAppendStore) OpenBlockRowsForSession(ctx context.Context, sessionID string) (content.OpenBlockRowsEntry, error) {
	return s.blocking.OpenBlockRowsForSession(ctx, sessionID)
}

func TestBlockRowsArrived_AutoFlushCountsTheBatchAgainstTheBound(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	store := newFailFirstAppendStore(db.Ledger())
	e.ws.blockRowsStore = store
	confirmed := make(chan uint64, 8)
	e.ws.AttachBlockRowsWithConfirmation(session.ID(sid), func(upTo uint64) {
		confirmed <- upTo
	})

	rowBytes := heldRowsBytes([]emulator.Row{aStreamRow("x")})
	e.ws.blockStream.mu.Lock()
	// Exactly two rows: the requeued batch plus the arrival that triggers
	// the auto-flush fill the bound, so any further row overflows it.
	e.ws.blockStream.budgets[session.ID(sid)] = rowBytes * 2
	e.ws.blockStream.mu.Unlock()

	// Pre-bind: the command is submitted, its ledger row not yet bound, so
	// the open reserves the attempt and refuses; rows hold in bs.pending.
	const command = "printf auto-flush"
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h.Domain), command), 43))
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, session.ID(sid), got.ID)

	if written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("x")}, ""); confirm {
		t.Fatalf("pre-bind row was acknowledged through %d; it must wait in bs.pending", written)
	}

	// The bind lands: the install flush of the held row is the store's one
	// refused append, and the batch comes back to bs.pending with no flush
	// in flight — the exact state the auto-flush branch exists to take.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleStartEvt(nil, command)))
	e.ws.blockStream.mu.Lock()
	block := e.ws.blockStream.current[session.ID(sid)]
	requeued := len(e.ws.blockStream.pending[session.ID(sid)])
	stillFlushing := e.ws.blockStream.flushing[session.ID(sid)]
	e.ws.blockStream.mu.Unlock()
	if block == nil {
		t.Fatal("the bind never installed the block")
	}
	if requeued != 1 || stillFlushing {
		t.Fatalf("post-refusal state = pending:%d flushing:%v, want the held row requeued with no flush in flight", requeued, stillFlushing)
	}

	// The next arrival takes the auto-flush branch: it extracts the requeued
	// batch plus itself and parks in the store. From that instant a full
	// budget is in flight.
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.BlockRowsArrived(session.ID(sid), 1, 0, []emulator.Row{aStreamRow("x")}, "")
	}()
	select {
	case <-store.blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the auto-flush never reached the store")
	}

	e.ws.blockStream.mu.Lock()
	held := e.ws.blockStream.heldBytesLocked(session.ID(sid))
	e.ws.blockStream.mu.Unlock()
	if held != rowBytes*2 {
		t.Fatalf("held bytes with the batch in flight = %d, want %d — the batch must count against the bound from the same lock hold that takes it out of bs.pending", held, rowBytes*2)
	}

	// A further full-budget arrival overflows: the block in flight ends
	// incomplete and the row is dropped, unconfirmed — the mark means
	// stored (nocx-zg3k3.5.3), and the resend offers the row again once
	// the coordinator returns and the buffer has drained.
	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 2, 0, []emulator.Row{aStreamRow("x")}, "")
	if written != 0 || confirm {
		t.Fatalf("overflow arrival answered (%d, %v), want (0, false): dropped, and the mark never claims it", written, confirm)
	}
	e.ws.blockStream.mu.Lock()
	overflowAt, overflowed := e.ws.blockStream.unrecorded[session.ID(sid)]
	admitted := len(e.ws.blockStream.pending[session.ID(sid)])
	e.ws.blockStream.mu.Unlock()
	if !overflowed || overflowAt != 2 {
		t.Fatalf("unrecorded = (%d, %v), want the overflow to fire at row 2", overflowAt, overflowed)
	}
	if admitted != 0 {
		t.Fatalf("%d deliveries were admitted behind the in-flight batch; the bound must refuse them", admitted)
	}

	// The ordinary path: the append lands, the batch drains, the budget is
	// released, and exactly the counted rows reach the store.
	close(store.blocking.release)
	<-done
	if c := <-confirmed; c != 1 {
		t.Fatalf("first confirmed watermark = %d, want 1", c)
	}
	if c := <-confirmed; c != 2 {
		t.Fatalf("second confirmed watermark = %d, want 2", c)
	}
	e.ws.blockStream.mu.Lock()
	held = e.ws.blockStream.heldBytesLocked(session.ID(sid))
	stillFlushing = e.ws.blockStream.flushing[session.ID(sid)]
	e.ws.blockStream.mu.Unlock()
	if held != 0 || stillFlushing {
		t.Fatalf("after the append landed: held=%d flushing=%v, want the budget fully released", held, stillFlushing)
	}
	kept := streamRows(t, db, got.ID)
	if len(kept) != 2 {
		t.Fatalf("stored rows = %+v, want exactly the two the in-flight batch carried — the overflowed row was never admitted", kept)
	}
	for i, row := range kept {
		if row.From != uint64(i) || row.Text != "x" { //nolint:gosec // a row index, not a byte count
			t.Fatalf("stored row %d = %+v, want from=%d text=x", i, row, i)
		}
	}
}
