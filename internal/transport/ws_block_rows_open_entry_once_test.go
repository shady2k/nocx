package transport

// An open's result races the fact that ends its own attempt or its session
// (nocx-2v80t.3.48): openAttemptFor records only a session-wide opening flag
// and calls the store without an attempt token. If the attempt's command
// completes, or the session detaches, while that call is still in flight, the
// close or the detach sees no block yet and settles the attempt without one —
// and the open's eventual success used to install the block anyway,
// resurrecting one the renderer was already told is gone, or repopulating a
// detached session's maps. And the slot that remembers an open asked for
// while another was in flight held only one: with A's open in flight and B
// then C asking, B was overwritten and never made.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
)

// raceOpenStore answers OpenBlockOutput with ErrNoSuchEntry until the test
// says the row is bound — the same bind-race shape bindRaceStore gives
// nocx-2v80t.3.42 — and can additionally hold the FIRST open past the bind in
// flight until told to continue, counting every seal so a test can tell
// whether a row a late, discarded open created was sealed through the
// ordinary path rather than left standing open in history.
type raceOpenStore struct {
	ledger content.LedgerRepository
	bound  chan struct{} // closed once the ledger row is durable

	mu      sync.Mutex
	hold    chan struct{} // non-nil: the next post-bind open waits on it
	entered chan struct{} // signalled when a held open is in flight
	order   []string
	seals   int
	// closeFailures is how many of the FIRST CloseBlockRows calls answer an
	// injected failure before calls succeed for real (nocx-2v80t.3.51's
	// orphan-seal retry); zero (the default) never fails.
	closeFailures int
	closeCalls    int
}

func (s *raceOpenStore) OpenBlockRowsForSession(ctx context.Context, sessionID string) (content.OpenBlockRowsEntry, error) {
	return s.ledger.OpenBlockRowsForSession(ctx, sessionID)
}

func (s *raceOpenStore) OpenBlockOutput(ctx context.Context, in content.OpenBlockOutput) (string, error) {
	select {
	case <-s.bound:
	default:
		// The read was taken before the bind: it cannot see the row.
		return "", content.ErrNoSuchEntry
	}
	s.mu.Lock()
	h := s.hold
	s.hold = nil
	s.mu.Unlock()
	if h != nil {
		s.entered <- struct{}{}
		<-h
	}
	art, err := s.ledger.OpenBlockOutput(ctx, in)
	if err == nil {
		s.mu.Lock()
		s.order = append(s.order, in.EntryID)
		s.mu.Unlock()
	}
	return art, err
}

func (s *raceOpenStore) AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error {
	return s.ledger.AppendBlockRows(ctx, in)
}

func (s *raceOpenStore) CloseBlockRows(ctx context.Context, in content.CloseBlockRows) (content.BlockRowsSummary, error) {
	s.mu.Lock()
	s.closeCalls++
	fail := s.closeCalls <= s.closeFailures
	s.mu.Unlock()
	if fail {
		return content.BlockRowsSummary{}, errors.New("injected close failure")
	}
	s.mu.Lock()
	s.seals++
	s.mu.Unlock()
	return s.ledger.CloseBlockRows(ctx, in)
}

func (s *raceOpenStore) RecordClearBoundary(ctx context.Context, in content.RecordClearBoundary) (content.ClearBoundaryRecorded, error) {
	return s.ledger.RecordClearBoundary(ctx, in)
}

func (s *raceOpenStore) sealCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seals
}

// orderSoFar answers the entry ids of every OpenBlockOutput that reached the
// ledger and succeeded, in order — the same record queueingOpenStore keeps,
// for tests driving this store's bind gate rather than its queue.
func (s *raceOpenStore) orderSoFar() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

func (s *raceOpenStore) closeAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

// lateOpenSetup starts a command before its ledger row is bound — the submit
// and the shell's authenticated start both answer ErrNoSuchEntry, exactly as
// commandBeforeItsRow does for nocx-2v80t.3.42 — leaving the waiting
// reservation open for a caller to drive the retry that finally reaches the
// store, held there until the test releases it.
func lateOpenSetup(t *testing.T) (
	e *lifecycleTestEnv, pub *lifecyclepub.Publisher, lane lifecycle.LaneID, h lifecycle.DomainHandle,
	sid session.ID, attempt string, store *raceOpenStore, db content.ContentDB,
) {
	t.Helper()
	db = newLedgerStore(t)
	e, pub, lane, h, sidStr, _ := newLifecycleLedgerEnvWithStore(t, db)
	store = &raceOpenStore{ledger: db.Ledger(), bound: make(chan struct{}), entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	sid = session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	attempt = startsACommand(t, e, pub, lane, h, 2, "make")
	return e, pub, lane, h, sid, attempt, store, db
}

// heldReopen releases store.bound (the row is now durable) and drives the
// retry that reaches the store, holding it there until the test tells it to
// continue; it answers once that open is in flight.
func heldReopen(t *testing.T, e *lifecycleTestEnv, sid session.ID, attempt string, store *raceOpenStore) (release chan struct{}, done chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	store.mu.Lock()
	store.hold = release
	store.mu.Unlock()
	close(store.bound)
	done = make(chan struct{})
	go func() {
		defer close(done)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, attempt)
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("the retried open never reached the store")
	}
	return release, done
}

func TestALateOpenAfterItsCommandClosedInstallsNothing(t *testing.T) {
	e, pub, lane, h, sid, attempt, store, db := lateOpenSetup(t)
	release, done := heldReopen(t, e, sid, attempt, store)

	// The command completes and its interval ends while the retried open is
	// still in the store: close sees no block yet for this attempt.
	fence := lifecycleFence(0x11)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 0, nil, false)
	closed := awaitBlockClosed(t, e)
	if closed.EntryID != attempt || closed.Kept {
		t.Fatalf("block.closed = %+v, want %q not kept", closed, attempt)
	}

	// The open's store call now succeeds — after the command already closed.
	close(release)
	<-done

	bs := e.ws.blockStream
	bs.mu.Lock()
	_, hasBlock := bs.open[sid][attempt]
	current := bs.current[sid]
	bs.mu.Unlock()
	if hasBlock || current != nil {
		t.Fatalf("the late open installed a block after its command closed: hasBlock=%v current=%+v", hasBlock, current)
	}
	if n := closedCount(t, e, sid, attempt); n != 0 {
		t.Fatalf("a resurrected block sent block.closed again: %d extra", n)
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("the row the late open created was sealed %d times, want exactly 1 (the discard's own seal)", n)
	}
	assertBlockSealed(t, db, attempt)
}

// Paired: an open with nothing racing it opens the block and the command ends
// like any other — the discard path added above must not touch this.
func TestAnOrdinaryOpenStillInstallsAndCloses(t *testing.T) {
	e, pub, lane, h, sid, attempt, store, db := lateOpenSetup(t)
	close(store.bound)
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, attempt)

	fence := lifecycleFence(0x12)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 0, nil, false)

	got := awaitBlockClosed(t, e)
	if got.EntryID != attempt || !got.Kept {
		t.Fatalf("block.closed = %+v, want %q kept", got, attempt)
	}
	assertBlockSealed(t, db, attempt)
}

func TestALateOpenAfterDetachInstallsNothing(t *testing.T) {
	e, _, _, _, sid, attempt, store, db := lateOpenSetup(t)
	release, done := heldReopen(t, e, sid, attempt, store)

	// The session detaches while the retried open is still in the store.
	e.ws.DetachBlockRows(sid)

	close(release)
	<-done

	bs := e.ws.blockStream
	bs.mu.Lock()
	_, hasOpen := bs.open[sid]
	_, hasCurrent := bs.current[sid]
	_, hasSources := bs.sources[sid]
	bs.mu.Unlock()
	if hasOpen || hasCurrent || hasSources {
		t.Fatalf("detached session state was recreated by the late open: open=%v current=%v sources=%v",
			hasOpen, hasCurrent, hasSources)
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("the row the late open created was sealed %d times, want exactly 1 (the discard's own seal)", n)
	}
	assertBlockSealed(t, db, attempt)
}

// queueingOpenStore is a store with no ledger behind it at all: OpenBlockOutput
// always succeeds, recording the order entries were opened in, and can pause
// on its next call, one release at a time. It also counts seals, so a test
// can tell a discarded open's row was sealed exactly once.
type queueingOpenStore struct {
	mu      sync.Mutex
	order   []string
	hold    chan struct{}
	entered chan struct{}
	seals   int
}

// OpenBlockRowsForSession: no ledger behind this fake holds an open
// block, so the honest answer is the zero value — what every fresh
// session's re-adopt read answers.
func (s *queueingOpenStore) OpenBlockRowsForSession(context.Context, string) (content.OpenBlockRowsEntry, error) {
	return content.OpenBlockRowsEntry{}, nil
}

func (s *queueingOpenStore) OpenBlockOutput(_ context.Context, in content.OpenBlockOutput) (string, error) {
	s.mu.Lock()
	h := s.hold
	s.hold = nil
	s.mu.Unlock()
	if h != nil {
		s.entered <- struct{}{}
		<-h
	}
	s.mu.Lock()
	s.order = append(s.order, in.EntryID)
	s.mu.Unlock()
	return "artifact-" + in.EntryID, nil
}

func (s *queueingOpenStore) AppendBlockRows(context.Context, content.AppendBlockRows) error {
	return nil
}

func (s *queueingOpenStore) CloseBlockRows(context.Context, content.CloseBlockRows) (content.BlockRowsSummary, error) {
	s.mu.Lock()
	s.seals++
	s.mu.Unlock()
	return content.BlockRowsSummary{}, nil
}

func (s *queueingOpenStore) RecordClearBoundary(context.Context, content.RecordClearBoundary) (content.ClearBoundaryRecorded, error) {
	return content.ClearBoundaryRecorded{}, nil
}

func (s *queueingOpenStore) orderSoFar() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

func (s *queueingOpenStore) sealCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seals
}

// TestThreeAttemptsQueuedBehindOneOpenAllOpenOnceEachInOrder is Major 3
// (nocx-2v80t.3.48): the reopen slot held only the last of several requests
// asked for while an open was in flight. With A's open in flight and B, then
// C, then D asking, the old single slot let C overwrite B, and B was never
// made. Each must now be made exactly once, in the order asked.
func TestThreeAttemptsQueuedBehindOneOpenAllOpenOnceEachInOrder(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	release := make(chan struct{})
	store := &queueingOpenStore{hold: release, entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("A's open never reached the store")
	}

	// B, then C, then D ask while A is still in flight.
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "B")
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "C")
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "D")

	close(release)
	<-done

	got := store.orderSoFar()
	want := []string{"A", "B", "C", "D"}
	if len(got) != len(want) {
		t.Fatalf("opened %v, want %v — each exactly once, in order", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("opened %v, want %v — each exactly once, in order", got, want)
		}
	}
}

// Paired: a single attempt asked for while another was in flight still opens
// once the one in flight finishes — the ordinary shape the queue must not
// break.
func TestOneAttemptQueuedBehindOneOpenStillOpens(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	release := make(chan struct{})
	store := &queueingOpenStore{hold: release, entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("A's open never reached the store")
	}

	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "B")

	close(release)
	<-done

	got := store.orderSoFar()
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("opened %v, want [A B]", got)
	}
}

// TestAQueuedAttemptClosedWhileAnotherOpensInstallsNothing is the same defect
// one handoff earlier (nocx-2v80t.3.48, review round 5's follow-up): B is not
// yet even the session's opening attempt when its close arrives, only queued
// behind A. markClosedWhileOpeningLocked used to check ONLY bs.opening[sid],
// so a close for a merely-queued attempt was not remembered at all — its
// later open, once the queue reached it, installed unconditionally, exactly
// the resurrection this bead started from. Fixed by having the close search
// the queue too, and by making the queue's advance (dequeueNextOpenLocked)
// reserve the next attempt atomically under the same lock it is dequeued in,
// so there is no gap where it is neither queued nor yet the opening attempt
// for a close arriving in the handoff to miss. Paired with
// TestOneAttemptQueuedBehindOneOpenStillOpens, the ordinary queued open with
// no close racing it.
func TestAQueuedAttemptClosedWhileAnotherOpensInstallsNothing(t *testing.T) {
	e := newLifecycleTestEnv(t)
	sid := session.ID(e.openSession(t, 1))
	release := make(chan struct{})
	store := &queueingOpenStore{hold: release, entered: make(chan struct{}, 1)}
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "A")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("A's open never reached the store")
	}

	// B asks while A is still in flight: queued behind A, not yet opening.
	e.ws.blockStream.openAttemptFor(context.Background(), e.ws, sid, "B")
	bs := e.ws.blockStream
	bs.mu.Lock()
	queued := append([]string(nil), bs.reopen[sid]...)
	bs.mu.Unlock()
	if len(queued) != 1 || queued[0] != "B" {
		t.Fatalf("reopen queue = %v, want [B] before its close", queued)
	}

	// B's own command completes and its interval ends while B is still only
	// queued — well before A's open resolves, let alone before B's own open
	// is ever attempted.
	e.ws.closeBlockRows(context.Background(), sid, "B", 0, nil, "", false)
	closedB := awaitBlockClosed(t, e)
	if closedB.EntryID != "B" || closedB.Kept {
		t.Fatalf("block.closed = %+v, want %q not kept", closedB, "B")
	}

	// A's open now succeeds, and the queue advances to B's — which must
	// still be ATTEMPTED (not silently skipped: the store call and its seal
	// are the thing under test), just not installed.
	close(release)
	<-done

	got := store.orderSoFar()
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("opened %v, want [A B] — B's own open must still be attempted", got)
	}

	bs.mu.Lock()
	_, hasBlock := bs.open[sid]["B"]
	current := bs.current[sid]
	bs.mu.Unlock()
	if hasBlock {
		t.Fatalf("B's late open installed a block after it was closed while still queued")
	}
	if current == nil || current.attempt != "A" {
		t.Fatalf("A's own block should be current, got %+v", current)
	}
	if n := closedCount(t, e, sid, "B"); n != 0 {
		t.Fatalf("a resurrected block sent block.closed again for B: %d extra", n)
	}
	if n := store.sealCount(); n != 1 {
		t.Fatalf("B's row was sealed %d times, want exactly 1 (the discard's own seal)", n)
	}
}
