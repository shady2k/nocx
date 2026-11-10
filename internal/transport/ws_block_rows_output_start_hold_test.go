package transport

// nocx-4y82c.1.11: a block's artifact begins at the block's OUTPUT start.
// The rows that depart before the shell's ordered output mark — the typed
// command's own echo, and whatever the composer's redraw leaves behind — are
// the renderer's business, never the store's. A recovered interval already
// held them (the retained-window replay); a FRESH block stored them as they
// streamed, so a command that outran its mark carried its own echo twice.
// These tests pin the one rule for both: a block holds what departs before
// its output start, the mark trims it, and a block that closes without ever
// seeing a mark is flushed unchanged — the shell with no shell integration
// keeps exactly the artifact it has today.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecommit"
	"github.com/shady2k/nocx/internal/session"
)

func TestAFreshBlockHoldsItsRowsUntilTheOutputStartMark(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(sid, func(upTo uint64) { confirmed = append(confirmed, upTo) })
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf 'echoonce'; printf out")

	// The command's own echo departs before the shell's preexec mark, so at
	// the moment it streams its output start is not known yet.
	echo := []emulator.Row{aStreamRow("$ printf 'echo"), aStreamRow("once'; printf out")}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, echo, ""); confirm || up != 0 {
		t.Fatalf("echo rows before the output mark = (%d, %v), want held", up, confirm)
	}
	if rows := streamRows(t, db, attempt); len(rows) != 0 {
		t.Fatalf("artifact holds the echo before the output mark: %+v", rows)
	}

	// The mark is the block's first output row.
	e.ws.BlockOutputStartRow(sid, 2)
	if len(confirmed) == 0 || confirmed[len(confirmed)-1] != 2 {
		t.Fatalf("confirmations after the mark = %v, want the echo settled through row 2", confirmed)
	}

	if up, confirm := e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("OUT-ONCE")}, ""); !confirm || up != 3 {
		t.Fatalf("first output row ack = (%d, %v), want stored through row 3", up, confirm)
	}
	fence := lifecycleFence(0x81)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 3, []emulator.Row{aStreamRow("$ ")}, false)

	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].From != 2 || rows[0].Text != "OUT-ONCE" || rows[1].Text != "$ " {
		t.Fatalf("frozen block rows = %+v, want the output from row 2 and the closing screen, no echo", rows)
	}
	assertSealedAs(t, db, attempt, nil)
}

func TestABlockThatNeverSeesAMarkStoresItsRowsUnchanged(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(sid, func(upTo uint64) { confirmed = append(confirmed, upTo) })
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "echo no-integration")

	// A shell whose integration never emits OSC 133 C: nothing ever names an
	// output start, so the rows still wait when the block closes.
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("$ echo no-integration"), aStreamRow("no-integration"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows of a never-marked shell = (%d, %v), want held", up, confirm)
	}
	fence := lifecycleFence(0x82)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, 2, []emulator.Row{aStreamRow("$ ")}, false)

	// The artifact is exactly what it was before the hold existed: every
	// departed row, in order, then the closing screen.
	rows := streamRows(t, db, attempt)
	want := []struct {
		from uint64
		text string
	}{{0, "$ echo no-integration"}, {1, "no-integration"}, {2, "$ "}}
	if len(rows) != len(want) {
		t.Fatalf("never-marked block holds %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].From != w.from || rows[i].Text != w.text {
			t.Fatalf("row %d = (%d, %q), want (%d, %q)", i, rows[i].From, rows[i].Text, w.from, w.text)
		}
	}
	if len(confirmed) == 0 || confirmed[len(confirmed)-1] != 2 {
		t.Fatalf("confirmations after the close = %v, want the two rows settled", confirmed)
	}
	assertSealedAs(t, db, attempt, nil)
}

func TestTheOutputStartHoldIsBounded(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(sid, func(upTo uint64) { confirmed = append(confirmed, upTo) })
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "yes")

	// A block may wait for its mark with `maxHeldBeforeOutputStartRows` rows
	// and not one more: past the bound the hold is released and every row is
	// stored, unchanged, as it would have been before the hold existed.
	held := make([]emulator.Row, maxHeldBeforeOutputStartRows)
	for i := range held {
		held[i] = aStreamRow("held")
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, held, ""); confirm || up != 0 {
		t.Fatalf("rows up to the bound = (%d, %v), want held", up, confirm)
	}
	if rows := streamRows(t, db, attempt); len(rows) != 0 {
		t.Fatalf("artifact holds %d rows up to the bound, want none held there", len(rows))
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, maxHeldBeforeOutputStartRows, 0, []emulator.Row{aStreamRow("overflowing")}, ""); !confirm || up != maxHeldBeforeOutputStartRows+1 {
		t.Fatalf("row past the bound ack = (%d, %v), want stored through %d", up, confirm, maxHeldBeforeOutputStartRows+1)
	}
	rows := streamRows(t, db, attempt)
	if len(rows) != maxHeldBeforeOutputStartRows+1 {
		t.Fatalf("artifact holds %d rows after the bound, want %d", len(rows), maxHeldBeforeOutputStartRows+1)
	}
	if rows[len(rows)-1].Text != "overflowing" || rows[len(rows)-1].From != maxHeldBeforeOutputStartRows {
		t.Fatalf("last stored row = (%d, %q), want the row that crossed the bound", rows[len(rows)-1].From, rows[len(rows)-1].Text)
	}

	// The block still knows its start is unknown: a late mark is the block's
	// own output start and only trims what arrives after the bound.
	e.ws.BlockOutputStartRow(sid, maxHeldBeforeOutputStartRows+1)
	if up, confirm := e.ws.BlockRowsArrived(sid, maxHeldBeforeOutputStartRows+1, 0, []emulator.Row{aStreamRow("after")}, ""); !confirm || up != maxHeldBeforeOutputStartRows+2 {
		t.Fatalf("row after the late mark ack = (%d, %v)", up, confirm)
	}
	fence := lifecycleFence(0x83)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(sid, fence, maxHeldBeforeOutputStartRows+2, []emulator.Row{aStreamRow("$ ")}, false)
	assertSealedAs(t, db, attempt, nil)
}

// ── The three review findings on the hold (nocx-4y82c.1.11) ────────────────
//
// Each is a data-loss sequence: output the coordinator had already received
// never reached the artifact. They are fixed test-first, under -race.

// holdReleaseProbe is how long the session-end test lets the end reach its
// seal before it concludes the release is holding it. It is a probe, not an
// assertion: the test's claim is the artifact's content, and this only decides
// when the store gate opens.
const holdReleaseProbe = 2 * time.Second

// A session end must not seal between a mark taking the held rows out of the
// hold and the release delivering them: the rows are in neither place, and a
// seal that runs there closes the artifact short and forgets them.
func TestASessionEndWaitsForAnInFlightHoldRelease(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	store := newBlockingAppendStore(db.Ledger())
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf output")

	// Two rows wait in the hold. The mark names row zero as the block's
	// output start, so the release delivers both.
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("$ printf output"), aStreamRow("output"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows before the mark = (%d, %v), want held", up, confirm)
	}

	// The mark starts the release, which parks in the store.
	markDone := make(chan struct{})
	go func() {
		defer close(markDone)
		e.ws.BlockOutputStartRow(sid, 0)
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("the release never reached the store")
	}

	// The helper reports the session's end while the release is in the store.
	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		e.ws.HelperSessionEnded(sid)
	}()

	// Wait for the end to do the thing the finding is about — reach its seal —
	// rather than for a duration: with the defect it seals now, with the fix
	// it waits for the release and reaches no seal until the gate below opens.
	// The assertion is the artifact; this only decides when the gate opens.
	select {
	case <-store.sealed:
	case <-endDone:
	case <-time.After(holdReleaseProbe):
	}

	// The release finishes; the end must then seal over its rows.
	close(store.release)
	select {
	case <-markDone:
	case <-time.After(wantWithin):
		t.Fatal("the release did not finish")
	}
	select {
	case <-endDone:
	case <-time.After(wantWithin):
		t.Fatal("the session end did not finish")
	}

	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].Text != "$ printf output" || rows[1].Text != "output" {
		t.Fatalf("artifact holds %+v, want the released prefix the session end sealed over", rows)
	}
	assertBlockSealed(t, db, attempt)
}

// A helper overflow ends the block incomplete from that row on. The rows the
// coordinator received BEFORE the overflow were stored before this hold
// existed and must stay stored: the release is not a row arriving after the
// marker, so it must not be answered by the unrecorded gate.
func TestAHeldPrefixSurvivesAHelperOverflow(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "make")

	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("kept-1"), aStreamRow("kept-2"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows before the overflow = (%d, %v), want held", up, confirm)
	}
	// The helper's buffer overflows at row 2: everything from there is lost,
	// and the two rows received before it are not.
	e.ws.BlockOutputIncomplete(sid, 2)
	fence := lifecycleFence(0x86)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))

	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].Text != "kept-1" || rows[1].Text != "kept-2" {
		t.Fatalf("artifact holds %+v, want the two rows received before the overflow", rows)
	}
	assertSealedAs(t, db, attempt, gap)
}

// The bound is stated where rows ENTER the hold, so the rows that accumulate
// while an open is in flight are bounded too: 300 of them cannot wait for a
// mark that may never come.
func TestTheHoldBoundCoversRowsThatArriveDuringTheOpen(t *testing.T) {
	e, _, _, _, sid, attempt, store, db := lateOpenSetup(t)
	e.ws.BlockOutputStartPlaneAttached(sid)
	release, done := heldReopen(t, e, sid, attempt, store)

	rows := make([]emulator.Row, maxHeldBeforeOutputStartRows+44)
	for i := range rows {
		rows[i] = aStreamRow("during-open")
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, rows, ""); confirm || up != 0 {
		t.Fatalf("rows during the open = (%d, %v), want them held pending the block", up, confirm)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(wantWithin):
		t.Fatal("the open did not finish")
	}

	stored := streamRows(t, db, attempt)
	if len(stored) != len(rows) {
		t.Fatalf("artifact holds %d rows after the open, want all %d: the bound is enforced where rows enter the hold", len(stored), len(rows))
	}
	e.ws.blockStream.mu.Lock()
	held := len(e.ws.blockStream.current[sid].preOutputStart)
	armed := e.ws.blockStream.current[sid].holdBeforeOutputStart
	e.ws.blockStream.mu.Unlock()
	if held != 0 || armed {
		t.Fatalf("hold after the open = %d rows, armed %v, want the bound to have released it", held, armed)
	}
}

// ── Round 2 of the review (nocx-4y82c.1.11) ───────────────────────────────
//
// Two sequences survived round 1: the bound's own release was not one
// critical section with sealing, and a close's flush lost the lifecycle
// frame's context (and told the helper before the frame committed).

// The 256-row bound starts a release too, so it must be the same critical
// section as a mark's: the extraction and the delivery cannot be interleaved
// with a session end's seal.
func TestASessionEndWaitsForABoundTriggeredRelease(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	store := newBlockingAppendStore(db.Ledger())
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "yes")

	held := make([]emulator.Row, maxHeldBeforeOutputStartRows)
	for i := range held {
		held[i] = aStreamRow("held")
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, held, ""); confirm || up != 0 {
		t.Fatalf("rows up to the bound = (%d, %v), want held", up, confirm)
	}

	// The row past the bound starts the release, which parks in the store.
	boundDone := make(chan struct{})
	go func() {
		defer close(boundDone)
		e.ws.BlockRowsArrived(sid, maxHeldBeforeOutputStartRows, 0, []emulator.Row{aStreamRow("overflowing")}, "")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("the bound's release never reached the store")
	}

	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		e.ws.HelperSessionEnded(sid)
	}()
	select {
	case <-store.sealed:
	case <-endDone:
	case <-time.After(holdReleaseProbe):
	}

	close(store.release)
	select {
	case <-boundDone:
	case <-time.After(wantWithin):
		t.Fatal("the bound's release did not finish")
	}
	select {
	case <-endDone:
	case <-time.After(wantWithin):
		t.Fatal("the session end did not finish")
	}

	rows := streamRows(t, db, attempt)
	if len(rows) != maxHeldBeforeOutputStartRows+1 {
		t.Fatalf("artifact holds %d rows, want all %d the release had extracted", len(rows), maxHeldBeforeOutputStartRows+1)
	}
	assertBlockSealed(t, db, attempt)
}

// ctxRecordingStore records the context every append carries, so a test can
// say which caller's context a flush ran under.
type ctxRecordingStore struct {
	closeFailureBlockStore
	mu   sync.Mutex
	ctxs []context.Context
}

func (s *ctxRecordingStore) AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error {
	s.mu.Lock()
	s.ctxs = append(s.ctxs, ctx)
	s.mu.Unlock()
	return s.closeFailureBlockStore.AppendBlockRows(ctx, in)
}

type frameMarkerKey struct{}

// A close's flush runs on the caller's context — the lifecycle frame's, in
// production — and tells the helper nothing before that frame commits
// (nocx-4y82c.1.11 review 2). A background context here is a defect: under
// -tags nocx_framecheck the store refuses the append, and in a shipped build
// the append waits for the frame's hold bound and lands outside its
// transaction.
func TestACloseFlushesTheHoldOnTheCallersContextAndDefersItsConfirmation(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(sid, func(upTo uint64) { confirmed = append(confirmed, upTo) })
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "make")

	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("kept-1"), aStreamRow("kept-2"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows before the close = (%d, %v), want held", up, confirm)
	}

	store := &ctxRecordingStore{closeFailureBlockStore: closeFailureBlockStore{ledger: db.Ledger()}}
	e.ws.blockRowsStore = store

	// The shape abandonAttempt runs in: the close inside a lifecycle frame,
	// whose context carries the frame's post-commit queue.
	fctx, queue := lifecyclecommit.Begin(context.WithValue(context.Background(), frameMarkerKey{}, true))
	e.ws.closeBlockRows(fctx, sid, attempt, 2, nil, "frame-close", false)

	store.mu.Lock()
	carried := append([]context.Context(nil), store.ctxs...)
	store.mu.Unlock()
	if len(carried) == 0 {
		t.Fatal("the flush made no append")
	}
	for _, got := range carried {
		if got.Value(frameMarkerKey{}) == nil {
			t.Fatalf("the flush appended on a context that is not the caller's: %v", got)
		}
	}
	if len(confirmed) != 0 {
		t.Fatalf("the flush confirmed %v inside the frame, before it committed", confirmed)
	}

	queue.End(true)
	if len(confirmed) == 0 || confirmed[len(confirmed)-1] != 2 {
		t.Fatalf("confirmations after the commit = %v, want the two rows settled once the frame committed", confirmed)
	}
	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].Text != "kept-1" || rows[1].Text != "kept-2" {
		t.Fatalf("artifact holds %+v, want the held prefix stored by the flush", rows)
	}
}

// The hold is emptied by ONE function: releaseHold. Anything else that
// clears it splits the extraction from the delivery the seal must wait for,
// which is the defect both rounds of the review found.
func TestTheHoldIsEmptiedByOneFunction(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "ws_block_rows.go"))
	if err != nil {
		t.Fatalf("read ws_block_rows.go: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	emptiedIn := map[string]int{}
	fn := ""
	for _, ln := range lines {
		if strings.HasPrefix(ln, "func ") {
			fn = ln
		}
		if strings.Contains(ln, "preOutputStart = nil") {
			emptiedIn[fn]++
		}
	}
	if len(emptiedIn) != 1 {
		t.Fatalf("the hold is emptied in %d functions, want exactly one (releaseHold): %v", len(emptiedIn), emptiedIn)
	}
	for name := range emptiedIn {
		if !strings.Contains(name, "releaseHoldStep(") {
			t.Fatalf("the hold is emptied in %q, want only releaseHoldStep (releaseHold's own body)", name)
		}
	}
}

// ── Round 3 of the review (nocx-4y82c.1.11) ───────────────────────────────
//
// The shape, not another mutex: one ordered sequence per session owns the
// hold, a settle is a later step in that sequence, and no store call is made
// while holding a lock another store caller can need.

// gateNthAppendStore passes the first n-1 appends and parks the rest, so a
// test can put the delivery that follows a release in the store while it
// starts a session end.
type gateNthAppendStore struct {
	closeFailureBlockStore
	mu      sync.Mutex
	calls   int
	pass    int
	entered chan struct{}
	release chan struct{}
}

func newGateNthAppendStore(ledger content.LedgerRepository, pass int) *gateNthAppendStore {
	return &gateNthAppendStore{
		closeFailureBlockStore: closeFailureBlockStore{ledger: ledger},
		pass:                   pass, entered: make(chan struct{}, 64), release: make(chan struct{}),
	}
}

func (s *gateNthAppendStore) AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call > s.pass {
		s.entered <- struct{}{}
		<-s.release
	}
	return s.closeFailureBlockStore.AppendBlockRows(ctx, in)
}

// The delivery that CROSSED the bound is part of the release's ordered span:
// a session end cannot seal between the prefix and the row that triggered it.
func TestASessionEndCannotSealBetweenABoundReleaseAndItsTriggeringRow(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	// The prefix lands (call 1); the triggering row's append parks (call 2).
	store := newGateNthAppendStore(db.Ledger(), 1)
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "yes")

	held := make([]emulator.Row, maxHeldBeforeOutputStartRows)
	for i := range held {
		held[i] = aStreamRow("held")
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, held, ""); confirm || up != 0 {
		t.Fatalf("rows up to the bound = (%d, %v), want held", up, confirm)
	}

	boundDone := make(chan struct{})
	go func() {
		defer close(boundDone)
		e.ws.BlockRowsArrived(sid, maxHeldBeforeOutputStartRows, 0, []emulator.Row{aStreamRow("overflowing")}, "")
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("the row that crossed the bound never reached the store")
	}

	// The session ends while that row is still in the store. It must be a
	// later step than the delivery, not a seal over it.
	e.ws.HelperSessionEnded(sid)

	close(store.release)
	select {
	case <-boundDone:
	case <-time.After(wantWithin):
		t.Fatal("the bound's delivery did not finish")
	}

	rows := streamRows(t, db, attempt)
	if len(rows) != maxHeldBeforeOutputStartRows+1 {
		t.Fatalf("artifact holds %d rows, want all %d the release and its trigger had", len(rows), maxHeldBeforeOutputStartRows+1)
	}
	assertBlockSealed(t, db, attempt)
}

// A close does not wait for a release that is in the store: the release
// holds the store's one connection, so a close that waited for it would hold
// the connection's counterpart and stall until the frame's own bound expired.
// The close is a later step in the session's sequence instead. Inside a
// lifecycle frame that step rides the frame's post-commit queue, because a
// seal of a frame that rolls back must not happen.
func TestACloseDoesNotWaitForAnInFlightRelease(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	store := newBlockingAppendStore(db.Ledger())
	e.ws.blockRowsStore = store
	e.ws.AttachBlockRows(sid)
	e.ws.BlockOutputStartPlaneAttached(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf output")

	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
		aStreamRow("kept-1"), aStreamRow("kept-2"),
	}, ""); confirm || up != 0 {
		t.Fatalf("rows before the mark = (%d, %v), want held", up, confirm)
	}

	// The mark starts the release, which parks in the store.
	markDone := make(chan struct{})
	go func() {
		defer close(markDone)
		e.ws.BlockOutputStartRow(sid, 0)
	}()
	select {
	case <-store.entered:
	case <-time.After(wantWithin):
		t.Fatal("the release never reached the store")
	}

	// The close arrives while the release is parked. It must return without
	// waiting for the store the release holds.
	fctx, queue := lifecyclecommit.Begin(context.Background())
	closeReturned := make(chan struct{})
	go func() {
		defer close(closeReturned)
		e.ws.closeBlockRows(fctx, sid, attempt, 2, nil, "frame-close", false)
	}()
	select {
	case <-closeReturned:
	case <-time.After(holdReleaseProbe):
		t.Fatal("the close waited for the in-flight release")
	}
	if art := blockArtifact(t, db, attempt); art.State == content.ArtifactSealed {
		t.Fatal("the close sealed while its release was still in the store")
	}

	// The release finishes; the frame commits; the close runs as the next
	// step and seals over the released rows.
	close(store.release)
	select {
	case <-markDone:
	case <-time.After(wantWithin):
		t.Fatal("the release did not finish")
	}
	queue.End(true)
	rows := streamRows(t, db, attempt)
	if len(rows) != 2 || rows[0].Text != "kept-1" || rows[1].Text != "kept-2" {
		t.Fatalf("artifact holds %+v, want the released rows before the close's seal", rows)
	}
	assertBlockSealed(t, db, attempt)
}

// The byte bound counts the rows that are already in the pending queue once:
// heldBytesLocked owns that accounting, and a caller adds only bytes the
// queue does not hold yet.
func TestTheOpenHoldCheckCountsPendingRowsOnce(t *testing.T) {
	e, _, _, _, sid, attempt, store, db := lateOpenSetup(t)
	e.ws.SetBlockRowsBufferBytes(MinBlockRowsBufferBytes)
	e.ws.blockStream.mu.Lock()
	e.ws.blockStream.budgets[sid] = MinBlockRowsBufferBytes
	e.ws.blockStream.mu.Unlock()
	e.ws.BlockOutputStartPlaneAttached(sid)
	release, done := heldReopen(t, e, sid, attempt, store)

	// 200 wide rows: about three quarters of the 4 MiB floor, and far below
	// the 256-row bound.
	rows := make([]emulator.Row, 200)
	for i := range rows {
		rows[i] = aStreamRow(strings.Repeat("x", 400))
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, rows, ""); confirm || up != 0 {
		t.Fatalf("rows during the open = (%d, %v), want them held pending the block", up, confirm)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(wantWithin):
		t.Fatal("the open did not finish")
	}

	// Neither bound is exceeded, so the hold must still have them: the echo
	// of the command is not stored before its mark.
	if stored := streamRows(t, db, attempt); len(stored) != 0 {
		t.Fatalf("artifact holds %d rows before any mark, want the hold to keep them", len(stored))
	}
	e.ws.blockStream.mu.Lock()
	heldRows := len(e.ws.blockStream.current[sid].preOutputStart)
	e.ws.blockStream.mu.Unlock()
	if heldRows != 1 {
		t.Fatalf("hold holds %d deliveries, want the open's head in one", heldRows)
	}
}
