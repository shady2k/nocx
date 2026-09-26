package transport

// A block's open races the ledger row it opens against (nocx-2v80t.3.42).
// Three callers ask for the open — the submit, the shell's authenticated
// start, and the ledger.bind that makes the row durable — and the store
// answers ErrNoSuchEntry until the bind has landed. An open still in flight
// when the bind's own open arrived used to swallow it: the in-flight read
// predated the bind and failed, and the one request that would have
// succeeded had been dropped. Nothing asked again, so the block never
// opened, its rows were held and never confirmed, its end was parked behind
// them, and block.closed never came. Measured in the 500-command
// transcript-budget spec: "open failed: no such entry" from the in-flight
// open, the bind's open answered `opening=true` and returned, and every row
// of that command pending until the run timed out.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// bindRaceStore answers OpenBlockOutput with ErrNoSuchEntry until the test
// says the row is bound, and can hold one open in flight until released.
type bindRaceStore struct {
	closeFailureBlockStore
	bound   chan struct{} // closed once the ledger row is durable
	mu      sync.Mutex
	hold    chan struct{} // when non-nil, the next open waits on it
	entered chan struct{} // signalled when a held open is in flight
}

func (s *bindRaceStore) OpenBlockOutput(ctx context.Context, in content.OpenBlockOutput) (string, error) {
	s.mu.Lock()
	h := s.hold
	s.hold = nil
	s.mu.Unlock()
	if h != nil {
		s.entered <- struct{}{}
		<-h
		// The read was taken before the bind: it cannot see the row.
		return "", content.ErrNoSuchEntry
	}
	select {
	case <-s.bound:
		return s.closeFailureBlockStore.OpenBlockOutput(ctx, in)
	default:
		return "", content.ErrNoSuchEntry
	}
}

// commandBeforeItsRow starts a command while the store cannot see its row
// yet: the submit's and the start's opens both answer no such entry.
func commandBeforeItsRow(t *testing.T) (*lifecycleTestEnv, session.ID, string, *bindRaceStore, func(uint64, lifecycle.Event)) {
	t.Helper()
	db := newLedgerStore(t)
	e, pub, lane, h, sidStr, _ := newLifecycleLedgerEnvWithStore(t, db)
	store := &bindRaceStore{
		closeFailureBlockStore: closeFailureBlockStore{ledger: db.Ledger()},
		bound:                  make(chan struct{}),
		entered:                make(chan struct{}, 1),
	}
	e.ws.blockRowsStore = store
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "make")
	ingest := func(seq uint64, evt lifecycle.Event) {
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, seq, evt))
	}
	return e, sid, attempt, store, ingest
}

// finishes streams the command's rows, completes it and ends its interval,
// and answers the block.closed it was said with.
func finishes(t *testing.T, e *lifecycleTestEnv, sid session.ID, attempt string, ingest func(uint64, lifecycle.Event)) blockClosedParams {
	t.Helper()
	e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("building")})
	fence := lifecycleFence(0x42)
	ingest(3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence))
	e.ws.BlockIntervalEnded(sid, fence, 1, []emulator.Row{aStreamRow("$ ")}, false)
	return awaitBlockClosed(t, e)
}

func TestTheBindsOpenIsNotSwallowedByAnOpenStillInFlight(t *testing.T) {
	e, sid, attempt, store, ingest := commandBeforeItsRow(t)

	// An open is in flight — a retry whose read predates the bind.
	release := make(chan struct{})
	store.mu.Lock()
	store.hold = release
	store.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.blockStream.openAttemptFor(e.ws, sid, attempt)
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("the in-flight open never reached the store")
	}

	// The bind lands and asks for the open while the other is in flight.
	close(store.bound)
	e.ws.blockStream.openAttemptFor(e.ws, sid, attempt)

	// The in-flight read fails, as a read before the bind must.
	close(release)
	<-done

	e.ws.blockStream.mu.Lock()
	current := e.ws.blockStream.current[sid]
	e.ws.blockStream.mu.Unlock()
	if current == nil || current.attempt != attempt {
		t.Fatalf("the block never opened (current = %+v): the bind's open was dropped while another was in flight", current)
	}
	got := finishes(t, e, sid, attempt, ingest)
	if got.EntryID != attempt || !got.Kept {
		t.Fatalf("block.closed = %+v, want %q kept", got, attempt)
	}
}

// Paired: the bind's open with nothing in flight opens the block, and the
// command ends like any other.
func TestTheBindsOpenOpensTheBlock(t *testing.T) {
	e, sid, attempt, store, ingest := commandBeforeItsRow(t)

	close(store.bound)
	e.ws.blockStream.openAttemptFor(e.ws, sid, attempt)

	got := finishes(t, e, sid, attempt, ingest)
	if got.EntryID != attempt || !got.Kept {
		t.Fatalf("block.closed = %+v, want %q kept", got, attempt)
	}
}
