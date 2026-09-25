package transport

// A detach and an ordinary close seal a block ONCE (nocx-2v80t.3.37), and
// the block.closed that is sent says what that one seal did: kept:true only
// when it landed. Whichever reaches the seal first owns it; the other neither
// seals again nor says closed again, and nothing of the session is recreated
// after its detach. Each order is run with the seal failing and succeeding,
// and the seal calls are counted.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// sealCountingStore counts seals, fails them when told to, and runs the
// session's detach at one chosen moment of the ordinary close: inside its
// seal, or inside its closing-rows append, just before its seal.
type sealCountingStore struct {
	closeFailureBlockStore
	mu            sync.Mutex
	seals         int
	sealFails     bool
	detachInSeal  bool
	detachInClose bool
	detach        func()
	fired         bool
}

func (s *sealCountingStore) fire() {
	s.mu.Lock()
	fire := !s.fired
	s.fired = true
	s.mu.Unlock()
	if fire {
		s.detach()
	}
}

func (s *sealCountingStore) AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error {
	err := s.closeFailureBlockStore.AppendBlockRows(ctx, in)
	if s.detachInClose && len(in.Rows) == 1 && in.FromRow == 1 {
		s.fire() // the closing screen is in; the close's seal is next
	}
	return err
}

func (s *sealCountingStore) CloseBlockRows(ctx context.Context, in content.CloseBlockRows) (content.BlockRowsSummary, error) {
	s.mu.Lock()
	s.seals++
	s.mu.Unlock()
	if s.detachInSeal {
		s.fire()
	}
	if s.sealFails {
		return content.BlockRowsSummary{}, errors.New("injected seal failure")
	}
	return s.closeFailureBlockStore.CloseBlockRows(ctx, in)
}

func (s *sealCountingStore) sealCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seals
}

func TestADetachAndAnInFlightCloseSealOnce(t *testing.T) {
	for _, tc := range []struct {
		name          string
		detachInSeal  bool
		detachInClose bool
		sealFails     bool
	}{
		{name: "detach during the close's seal, which fails", detachInSeal: true, sealFails: true},
		{name: "detach during the close's seal, which lands", detachInSeal: true},
		{name: "detach before the close's seal, whose own seal fails", detachInClose: true, sealFails: true},
		{name: "detach before the close's seal, whose own seal lands", detachInClose: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newLedgerStore(t)
			e, pub, lane, h, sidStr, _ := newLifecycleLedgerEnvWithStore(t, db)
			sid := session.ID(sidStr)
			store := &sealCountingStore{
				closeFailureBlockStore: closeFailureBlockStore{ledger: db.Ledger()},
				detachInSeal:           tc.detachInSeal, detachInClose: tc.detachInClose,
			}
			store.detach = func() { e.ws.DetachBlockRows(sid) }
			e.ws.blockRowsStore = store
			e.ws.AttachBlockRows(sid)
			attempt := startsACommand(t, e, pub, lane, h, 2, "make")
			if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("building")}); !confirm {
				t.Fatal("the streamed row was not confirmed")
			}
			fence := lifecycleFence(0x3a)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
			store.sealFails = tc.sealFails

			e.ws.BlockIntervalEnded(sid, fence, 1, []emulator.Row{aStreamRow("$ ")}, false)

			if n := store.sealCount(); n != 1 {
				t.Fatalf("the block was sealed %d times, want once", n)
			}
			got := awaitBlockClosed(t, e)
			if got.EntryID != attempt || got.Kept == tc.sealFails {
				t.Fatalf("block.closed = %+v, want kept:%v — the result of the one seal", got, !tc.sealFails)
			}
			if n := closedCount(t, e, sid, attempt); n != 0 {
				t.Fatalf("block.closed was sent %d more times after the first", n)
			}
			bs := e.ws.blockStream
			bs.mu.Lock()
			defer bs.mu.Unlock()
			_, closedThrough := bs.closedThrough[sid]
			_, tries := bs.closeTries[sid]
			if len(bs.pendingCloses[sid]) != 0 || tries || closedThrough || len(bs.open[sid]) != 0 {
				t.Fatalf("state was recreated after the detach: pendingCloses=%d closeTries=%v closedThrough=%v open=%d",
					len(bs.pendingCloses[sid]), tries, closedThrough, len(bs.open[sid]))
			}
		})
	}
}
