package session

// Stage review round 6 (codex, nocx-2v80t.3.52), findings 5 and 6: shutdown
// used to remove every row subscriber (releaseConnection) BEFORE
// runtime.Fail gave a forged sighting's held rows back to the bridge
// (nocx-2v80t.3.47), and the pump's own exit raced rowsDone's close against
// whatever Fail had just enqueued — a select with both arms ready picks
// either one, so the pump could take the closed-channel arm and exit before
// ever dequeuing what Fail produced. 3.47's own test proved Fail gives rows
// back; it called Session.Fail directly and never went through
// hostSession.stop, which is the seam that actually drops them. These tests
// drive the real seam: a real owner, a real sessionruntime.Session and the
// real row pump, stopped through hostSession.stop.

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// stopTestSession builds a hostSession the way finishSpawn wires production
// — a real proc, runtime, screen, owner and row pump — so hs.stop() exercises
// the actual shutdown sequence rather than a stand-in for it. The one
// subscriber given is the only reader the test's sink stands in for.
func stopTestSession(t *testing.T, sink Sink) (*hostSession, *sessionruntime.Session) {
	t.Helper()
	const sessionID = "0123456789abcdef0123456789abcdef"
	proc := newRawReaderFakeProcess()
	rt, screen, err := newSessionRuntime(defaultScreen, proc, sessionID, 80, 24, 0, 0)
	if err != nil {
		t.Fatalf("build the session runtime: %v", err)
	}
	win := newWindow(2 * creditLimit)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	owner := newSessionOwner(proc, rt, win, log)
	rt.SetReplies(owner)
	hs := &hostSession{
		id:          proto.HostSessionID{Generation: "testhash", Session: sessionID},
		raw:         mintRaw(t),
		proc:        proc,
		win:         win,
		runtime:     rt,
		screen:      screen,
		owner:       owner,
		log:         log,
		now:         time.Now,
		subs:        make(map[proto.SubscriberID]*subscriber),
		attachments: make(map[proto.AttachmentID]*attachment),
		rowWake:     make(chan struct{}, 1),
		rowsDone:    make(chan struct{}),
	}
	rt.SetRowStream(&rowBridge{hs: hs})
	go owner.run()
	go hs.serveRows()
	// attach, not a hand-built subscriber map entry: releaseConnection (stop's
	// own teardown) calls stopSubscriber, which needs the stop func and the
	// gates and done channels attach wires — a subscriber missing them is not
	// a shape stop() ever sees in production.
	const subscriberID = proto.SubscriberID("1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a")
	if _, err := hs.attach(proto.AttachParams{Subscriber: subscriberID, Fresh: true}, sink,
		func() proto.AttachmentID { return "att-1" }, log); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return hs, rt
}

// stopBounded calls hs.stop() off the test goroutine and fails with a named
// reason rather than hanging the suite if it never returns — the drain this
// bead adds is event-driven (requestRowsDrain/signalRowsDrainedIfWaiting), so
// a hang here is a real defect, never a slow machine.
func stopBounded(t *testing.T, hs *hostSession) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		hs.stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(hangLimit):
		t.Fatal("hostSession.stop never returned")
	}
}

// A fence sighted with nobody ever authenticating it only HOLDS the rows it
// matches (ADR-0024 decision 1) — ten lines printed, then twenty more force
// seven of the first ten off the screen while the sighting's window still
// holds them (the exact shape TestASightingNobodyAuthenticatesDropsNothing
// proves at the sessionruntime layer: thirty lines on an 80x24 screen departs
// seven). Nobody ever completes that fence, so stop()'s own runtime.Fail is
// what finally gives those seven rows back — and they must reach the
// still-attached sink, not the subscriber-less pump the old order left
// behind.
func TestStopDeliversAnUnauthenticatedCapturesHeldRows(t *testing.T) {
	sink := newRowsSink()
	hs, rt := stopTestSession(t, sink)

	rowsFeed(t, rt, 0, 10) // ten lines: nothing has left the empty screen yet
	forged := endNonce(0x77)
	if err := rt.SightFence(forged, []byte("forged-fence")); err != nil {
		t.Fatalf("sight a fence nobody will authenticate: %v", err)
	}
	if got := rt.RendezvousFor(forged).State; got != sessionruntime.RendezvousAwaitingAuthenticated {
		t.Fatalf("the sighting reads %v, want parked awaiting the authenticated half", got)
	}
	rowsFeed(t, rt, 10, 20) // seven of the first ten depart while the window holds them

	stopBounded(t, hs)

	select {
	case <-sink.changed:
	case <-time.After(hangLimit):
		t.Fatal("stop() ended without the held rows ever reaching the still-attached sink")
	}
	frames := sink.rowFrames()
	if len(frames) == 0 {
		t.Fatal("stop() delivered no rows-plane frame for the capture Fail gave back")
	}
	var total int
	for i, f := range frames {
		if i == 0 && f.FromRow != 0 {
			t.Fatalf("the given-back rows start at %d, want 0 — the screen as the forged fence sat on it", f.FromRow)
		}
		total += countEncodedRows(t, f.Payload)
	}
	if total != 7 {
		t.Fatalf("stop() delivered %d rows for the unauthenticated capture, want 7 (the ones the sighting's window held)", total)
	}
}

// Paired: an ordinary stop with nothing owed — no fence sighted, nothing left
// parked for Fail to give back — ends cleanly with no extra frame invented.
func TestStopWithNoCaptureOwedEndsCleanly(t *testing.T) {
	sink := newRowsSink()
	hs, rt := stopTestSession(t, sink)

	rowsFeed(t, rt, 0, 30) // thirty lines on a twenty-four row screen: seven depart normally
	sink.waitFor(1, 0, 0)
	before := len(sink.rowFrames())

	stopBounded(t, hs)

	if got := len(sink.rowFrames()); got != before {
		t.Fatalf("an ordinary stop with nothing owed produced %d more rows-plane frame(s), want none invented", got-before)
	}
}

// stallUntilOverflow parks the pump inside its first send and only then feeds
// the rows behind it, so the buffer provably overflows and the incomplete
// marker exists before anything waits on one.
//
// It is the precondition the drain tests could not previously STATE. With the
// pump free it is not a race that happens to go one way but an outcome: the
// FIFO charge of a delivered row is released as the pump delivers it, so a
// pump that keeps up spends one row's bytes at a time and five rows against
// bufferOf(2) never overflow AT ALL — no marker, nothing owed, and both waits
// below unbounded rather than merely slow. A burst feed hides that on an idle
// machine and a loaded runner pays it (nocx-xn63t.6.17: 2 runs in 2 on the
// ci-mac runner, green 4/4 on Linux). Wedging the pump makes the overflow the
// only thing that can happen — with the stalled row's own charges never
// released, the third row behind it cannot fit and everything after that is
// dropped without a charge — so the loss is counted before a single delivery
// is attempted and the assertion below reads it directly instead of inferring
// it.
func stallUntilOverflow(t *testing.T, sink *orderedStallingSink, hs *hostSession, bridge *rowBridge) {
	t.Helper()
	const rows = 5
	bridge.OutputRows(0, []emulator.Row{textRow("x")}, 0)
	select {
	case <-sink.stalled:
	case <-time.After(hangLimit):
		t.Fatal("the pump never reached the sink, so nothing queued behind it could overflow")
	}
	for i := uint64(1); i <= rows; i++ {
		bridge.OutputRows(i, []emulator.Row{textRow("x")}, 0)
	}
	if got := hs.rowsIncomplete.Load(); got != 1 {
		t.Fatalf("rowsIncomplete = %d behind a wedged pump, want 1 — the overflow these tests need never happened", got)
	}
}

// A failed send of the row buffer's own incomplete marker (nocx-2v80t.3.38)
// leaves it owed; stopping must still deliver it — or, failing that, record
// the loss — rather than let releaseConnection and the pump's own end
// silently swallow the one statement that output was discarded (finding 6).
//
// The sink refuses every marker send while the session is not draining, so it
// is owed and every retry the pump makes on its own fails, however many stale
// wakes rowWake happens to have banked — an earlier version counted on exactly
// one and hung 1 run in ~2000 under -race when there was none (nocx-2v80t.9).
// It starts taking it only while stop() is draining, which is the property
// under test and not a flag flipped from this goroutine: a shared counter set
// after the first failure races the pump's own immediate retry, and when that
// retry wins, the marker lands without stop() having drained anything and the
// test passes having proved nothing (the pump's side of nocx-2v80t.9). stop()
// may not end with it undelivered.
func TestStopDeliversAnOwedIncompleteMarkerBeforeEnding(t *testing.T) {
	sink := newOrderedStallingSink()
	hs, _ := stopTestSession(t, sink)
	sink.takeIncompleteOnlyWhileDraining(hs.drainRequested)
	bufferOf(hs, 2)
	bridge := &rowBridge{hs: hs}

	stallUntilOverflow(t, sink, hs, bridge)
	close(sink.release) // the rows queued behind the stall, and then the marker, may go
	select {
	case <-sink.failed: // the marker's first send failed: it is owed
	case <-time.After(hangLimit):
		t.Fatal("the marker's send never happened")
	}

	stopBounded(t, hs)

	log := sink.snapshot()
	marks := 0
	for _, d := range log {
		if d.incomplete {
			marks++
		}
	}
	if marks != 1 {
		t.Fatalf("stop() left %d incomplete markers delivered, want exactly 1 (the one owed, retried and landed)", marks)
	}
}

// Paired: an ordinary stop with nothing owed — the buffer never overflowed,
// so there is no marker for stop() to retry or lose.
func TestStopWithNoMarkerOwedEndsCleanly(t *testing.T) {
	sink := newOrderedStallingSink()
	close(sink.release)
	hs, _ := stopTestSession(t, sink)
	bridge := &rowBridge{hs: hs}
	row := []emulator.Row{textRow("x")}
	bridge.OutputRows(1, row, 0)
	sink.waitUntil(func(log []recordedDelivery) bool { return len(log) >= 1 })

	stopBounded(t, hs)

	for _, d := range sink.snapshot() {
		if d.incomplete {
			t.Fatal("an ordinary stop with nothing owed produced an incomplete marker")
		}
	}
}

// A sink that refuses EVERY marker send — not merely the three
// TestStopDeliversAnOwedIncompleteMarkerBeforeEnding needs to genuinely park
// it, but every attempt forever — must still let stop() return
// (nocx-2v80t.3.52, round 2): once draining, a failed send is resolved
// rather than retried forever, because the loss it names was already stated
// the instant the buffer overflowed (enqueueRowEmission's rowsIncomplete
// count and its own warning), independent of whether delivery ever lands on
// a connection that is not coming back.
func TestStopReturnsWhenTheSinkRefusesEveryMarkerSend(t *testing.T) {
	sink := newOrderedStallingSink()
	sink.failIncompleteTimes = -1 // never succeeds
	hs, _ := stopTestSession(t, sink)
	bufferOf(hs, 2)
	bridge := &rowBridge{hs: hs}

	stallUntilOverflow(t, sink, hs, bridge)
	close(sink.release) // every non-marker send may still proceed at once
	select {
	case <-sink.failed:
	case <-time.After(hangLimit):
		t.Fatal("the marker's own first send never happened")
	}

	stopBounded(t, hs)

	if got := hs.rowsIncomplete.Load(); got != 1 {
		t.Fatalf("rowsIncomplete = %d, want 1 — the loss is counted at the overflow, independent of whether delivery ever lands", got)
	}
	for _, d := range sink.snapshot() {
		if d.incomplete {
			t.Fatal("a sink that refuses every send recorded a DELIVERED incomplete marker")
		}
	}
}

// A sink whose send never returns at all — a wedged connection, which looks
// identical to a working one that simply never answers — must not hang
// stop() forever either (nocx-2v80t.3.52, round 2): the Sink interface takes
// no context and its one production implementation
// (internal/helper/host.Host.write) is a plain, deadline-less io.Writer
// call, so there is no cancellation this package can reach into for an
// in-flight send. deliverForPump bounds a drain-time attempt to stopGrace —
// the same grace the process's own tail already gets from owner.stop — and
// abandons it past that.
//
// Three scheduled failures first, exactly as
// TestStopDeliversAnOwedIncompleteMarkerBeforeEnding needs them, so the
// marker is genuinely parked (not resolved by 3.49's immediate retry or the
// one stale wake rowWake's single slot can bank) before stop() is ever
// called and its own drain-time retry is the one that blocks. Both halves of
// that are construction rather than luck here, which is what
// nocx-xn63t.6.17 is about. The overflow is stallUntilOverflow's, read off
// rowsIncomplete before anything is released; the third failure needs one
// wake, and the feed behind the wedged pump leaves exactly one — coalesced
// into rowWake's single slot, and the pump performs no select between the
// stall and the park after the second refusal, so that park is what consumes
// it. Nothing else calls wakeRows between here and stop(), so the pump is
// parked with an empty slot when stop() arms the drain, and the attempt that
// blocks past stopGrace is provably the drain's own rather than a straggler
// the drain had to sweep up.
func TestStopReturnsWhenTheOwedMarkersSendNeverReturns(t *testing.T) {
	sink := newOrderedStallingSink()
	sink.failIncompleteTimes = 3
	sink.blockAfterExhausted = true
	hs, _ := stopTestSession(t, sink)
	bufferOf(hs, 2)
	bridge := &rowBridge{hs: hs}

	stallUntilOverflow(t, sink, hs, bridge)
	close(sink.release)
	select {
	case <-sink.exhausted: // all three scheduled failures have happened; the marker is genuinely parked
	case <-time.After(hangLimit):
		t.Fatal("the marker's three scheduled sends never all happened")
	}
	for _, d := range sink.snapshot() {
		if d.incomplete {
			t.Fatalf("the marker reached the sink while it was still refusing: %+v", d)
		}
	}

	stopBounded(t, hs) // must return even though the drain's own retry never does

	for _, d := range sink.snapshot() {
		if d.incomplete {
			t.Fatalf("a sink whose send never returns recorded a DELIVERED incomplete marker: %+v", d)
		}
	}
}

// countEncodedRows decodes one rows-plane payload and answers how many rows
// it carries — the bridge's own encoding (sessionruntime.EncodeRows), read
// back rather than assumed.
func countEncodedRows(t *testing.T, payload []byte) int {
	t.Helper()
	var doc proto.OutputRowsDoc
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("decode a rows-plane payload: %v", err)
	}
	if len(doc.Rows) == 0 {
		return 0
	}
	var rows []struct{}
	if err := json.Unmarshal(doc.Rows, &rows); err != nil {
		t.Fatalf("decode a rows-plane payload's rows: %v", err)
	}
	return len(rows)
}
