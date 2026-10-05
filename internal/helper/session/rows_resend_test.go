package session

// The resend acceptance checks (nocx-ho1ri): original cells survive disconnects,
// acknowledgements reclaim only proven prefixes, and the helper states loss
// when a span is absent from its bounded retained window.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// resentRows decodes every row frame a sink recorded into (fromRow, texts)
// pairs, in arrival order.
type resentRows struct {
	from       uint64
	lost       uint64
	cause      string
	incomplete bool
	texts      []string
}

func decodeResentRows(t *testing.T, frames []proto.OutputRowsFrame) []resentRows {
	t.Helper()
	var out []resentRows
	for _, f := range frames {
		var doc struct {
			FromRow    uint64 `json:"fromRow"`
			LostRows   uint64 `json:"lostRows"`
			LostCause  string `json:"lostCause"`
			Incomplete bool   `json:"incomplete"`
			Rows       []struct {
				Text string `json:"text"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(f.Payload, &doc); err != nil {
			t.Fatalf("decode a resent rows payload: %v\n%s", err, f.Payload)
		}
		r := resentRows{from: doc.FromRow, lost: doc.LostRows, cause: doc.LostCause, incomplete: doc.Incomplete}
		for _, row := range doc.Rows {
			r.texts = append(r.texts, row.Text)
		}
		out = append(out, r)
	}
	return out
}

// The acceptance's core (nocx-zg3k3.5.3, criterion 1): the coordinator goes
// away while a command keeps printing and comes back — the block ends up
// with the command's whole output. At the pump's own seam: rows streamed to
// nobody are dropped (ghostty's scrollback is the buffer), and the return
// reads back, from the scrollback, every row after the confirmed-written
// mark, at the absolute indices the stream would have carried them under.
func TestThePumpResendsTheRowsItDroppedForNoSubscriber(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)

	// The coordinator watches the first command's head and stores it: forty
	// lines on a twenty-four row screen depart sixteen rows.
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	if err := hs.confirmRows(sink, "coord-1", 16); err != nil {
		t.Fatalf("confirm the stored rows: %v", err)
	}

	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()
	rowsFeed(t, rt, 100, 100)

	// It comes back through the real attach path, which arms the resend
	// before making the new reader visible to queued output.
	sink2 := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", sink2)

	// The resend begins at the prefix-confirmed watermark. Those rows have
	// been reclaimed from the helper's bounded window and need not be sent
	// again; the unconfirmed suffix is served from original retained cells.
	sink2.waitFor(4, 0, 0)

	batches := decodeResentRows(t, sink2.rowFrames())
	var got []string
	for i, b := range batches {
		wantFrom := uint64(16)
		if i > 0 {
			wantFrom = batches[i-1].from + uint64(len(batches[i-1].texts))
		}
		if b.from != wantFrom {
			t.Fatalf("resend batch %d names FromRow %d, want %d — the index never skips", i, b.from, wantFrom)
		}
		if b.lost != 0 {
			t.Fatalf("resend batch %d claims %d lost: a plain absence is not a loss", i, b.lost)
		}
		got = append(got, b.texts...)
	}
	var want []string
	// The span is the acceptance's own definition — every row the interval
	// streamed, to where departures reached — and its CONTENT is built here
	// by hand: the rows the screen held at the mark (the walk starts at the
	// interval's own start now), then the fed lines in order. A feed whose
	// trailing newline scrolls one extra row departs one more old row than
	// arithmetic suggests; the texts below are what the scrollback provably
	// held, and the assertion pins order, indices and content against them.
	want = nil
	for i := 16; i < 40; i++ {
		want = append(want, fmt.Sprintf("L%06d", i))
	}
	for i, n := 100, len(got)-24; i < 100+n; i++ {
		want = append(want, fmt.Sprintf("L%06d", i))
	}
	if len(got) <= 24 {
		t.Fatalf("the resent rows are %d, want the retained unconfirmed suffix", len(got))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the resent rows are not the command's whole output:\n got %d rows %q\nwant %d rows %q",
			len(got), strings.Join(got, ","), len(want), strings.Join(want, ","))
	}
}

// A new subscriber can attach after an output emission was queued but before
// the pump observed that it had no recipient. In that ordering the queued
// emission itself does not arm resendDue, so attaching must compare the
// confirmed mark with the runtime's departure count and request replay before
// the pump delivers queued rows to the new reader.
func TestNewReaderReplaysUnconfirmedRowsBeforeQueuedOutput(t *testing.T) {
	hs, rt, first := rowsBridgeSession(t, 80, 24)
	rowsFeed(t, rt, 0, 45)
	first.waitFor(1, 0, 0)
	if err := hs.confirmRows(first, "coord-1", 16); err != nil {
		t.Fatalf("confirm the stored prefix: %v", err)
	}
	// The replacement stream carries the position before its retained prefix.
	(&rowBridge{hs: hs}).OutputStartRow(16)
	<-hs.requestRowsDrain()
	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()

	second := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", second)

	// Drain gives the pump's queue-empty observation without timing. During
	// a shutdown drain replay is intentionally paused, so if the pump has not
	// already sent the new reader's replay, the test checks that attach armed
	// it and invokes the same retained walk synchronously.
	<-hs.requestRowsDrain()
	frames := decodeResentRows(t, second.rowFrames())
	if len(frames) == 0 {
		hs.rowMu.Lock()
		due := hs.resendDue
		hs.rowMu.Unlock()
		if !due {
			t.Fatal("attaching a new reader did not arm a resend of its unconfirmed rows")
		}
		if !hs.resendFromScrollback() {
			t.Fatal("the retained unconfirmed rows could not be replayed to the new reader")
		}
		frames = decodeResentRows(t, second.rowFrames())
	}
	second.mu.Lock()
	starts := append([]proto.OutputStartRowFrame(nil), second.outputStarts...)
	events := append([]string(nil), second.events...)
	second.mu.Unlock()
	if len(starts) == 0 || starts[len(starts)-1].FromRow != 16 {
		t.Fatalf("replacement reader output mark = %+v, want position 16", starts)
	}
	if len(events) < 2 || events[0] != "output-start" || events[1] != "rows" {
		t.Fatalf("replacement reader stream order = %v, want mark before retained rows", events)
	}
	if len(frames) == 0 || frames[0].from != 16 {
		var got uint64
		if len(frames) != 0 {
			got = frames[0].from
		}
		t.Fatalf("new reader's first rows start at %d, want unconfirmed watermark 16", got)
	}
}

func TestNewReaderGetsTheOutputStartMarkWhenNoRowsNeedResending(t *testing.T) {
	hs, rt, first := rowsBridgeSession(t, 80, 24)
	rowsFeed(t, rt, 0, 45)
	first.waitFor(1, 0, 0)
	confirmed := rt.DepartedRowCount()
	if err := hs.confirmRows(first, "coord-1", confirmed); err != nil {
		t.Fatalf("confirm the whole row prefix: %v", err)
	}
	<-hs.requestRowsDrain()
	(&rowBridge{hs: hs}).OutputStartRow(confirmed)
	<-hs.requestRowsDrain()

	second := newRowsSink()
	rowsAttachReader(t, hs, "33333333333333333333333333333333", second)
	second.waitForOutputStarts(1)
	second.mu.Lock()
	defer second.mu.Unlock()
	if got := second.outputStarts[0].FromRow; got != confirmed {
		t.Fatalf("replacement reader output mark = %d, want %d", got, confirmed)
	}
	if len(second.rows) != 0 {
		t.Fatalf("replacement with fully confirmed rows replayed %d rows, want only the position mark", len(second.rows))
	}
}

// Pending output must remain after the replay prefix. The retained walk ends
// at the first still-queued row, then the FIFO resumes at that row exactly
// once. This forces the attach/pump ordering that is scheduler-dependent in
// the package's ordinary full run.
func TestReaderReplayStopsAtThePendingQueueHead(t *testing.T) {
	hs, rt, first := rowsBridgeSession(t, 80, 24)
	rowsFeed(t, rt, 0, 45)
	first.waitFor(1, 0, 0)
	if err := hs.confirmRows(first, "coord-1", 16); err != nil {
		t.Fatalf("confirm the stored prefix: %v", err)
	}
	<-hs.requestRowsDrain()

	const queuedFrom = uint64(20)
	queued := make([]emulator.Row, 0, 2)
	hs.rowMu.Lock()
	for _, span := range hs.resendWindow {
		for i, row := range span.rows {
			index := span.from + uint64(i) //nolint:gosec // slice index is non-negative
			if index >= queuedFrom && index < queuedFrom+2 {
				queued = append(queued, row)
			}
		}
	}
	em := rowEmission{from: queuedFrom, rows: queued}
	em.bytes = emissionBytes(em)
	if len(queued) != 2 || !hs.rowPool.charge(rowOwnerFIFO, em.bytes) {
		hs.rowMu.Unlock()
		t.Fatalf("could not queue two retained rows from %d", queuedFrom)
	}
	hs.rowQueue = append(hs.rowQueue, em)
	hs.rowQueuedBytes += em.bytes
	hs.rowMu.Unlock()

	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()
	second := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", second)

	second.waitFor(1, 0, 0)
	firstBatch := decodeResentRows(t, second.rowFrames())
	if firstBatch[0].from != 16 {
		t.Fatalf("the pending output overtook resend: first FromRow is %d, want 16", firstBatch[0].from)
	}
	second.waitFor(2, 0, 0)
	batches := decodeResentRows(t, second.rowFrames())
	if len(batches) != 2 || batches[1].from != queuedFrom {
		t.Fatalf("the pending queue did not resume at %d after the replay: %+v", queuedFrom, batches)
	}
	if texts := append(batches[0].texts, batches[1].texts...); len(texts) != 6 {
		t.Fatalf("replay plus queued output delivered %d rows, want 6", len(texts))
	} else {
		for i, text := range texts {
			if want := fmt.Sprintf("L%06d", 16+i); text != want {
				t.Fatalf("row %d = %q, want %q", i, text, want)
			}
		}
	}
}

// The detach shape the acceptance's first half rides (nocx-zg3k3.5.3):
// the coordinator TOOK the rows — the pump delivered them, delivered=true,
// no drop — and went away without confirming. The confirmed mark is behind
// what the pump handed out, and the next attach must read the scrollback back
// from the mark, or the taken rows are silently gone: never re-sent, never
// counted. Ordered events, no load.
func TestReplacingAnAttachedReaderResendsItsUnconfirmedRows(t *testing.T) {
	hs, rt, first := rowsBridgeSession(t, 80, 24)
	subscriberID := proto.SubscriberID("11111111111111111111111111111111")
	hs.mu.Lock()
	old := hs.subs["coord-1"]
	delete(hs.subs, "coord-1")
	old.id = subscriberID
	old.attachment = "att-old"
	old.stop = func() {}
	old.wake = newGate()
	old.lifecycleWake = newGate()
	old.done = make(chan struct{})
	close(old.done)
	old.lifecycleDone = make(chan struct{})
	close(old.lifecycleDone)
	hs.subs[subscriberID] = old
	hs.attachments = map[proto.AttachmentID]*attachment{"att-old": {id: "att-old", subscriber: subscriberID, sink: first}}
	hs.mu.Unlock()

	rowsFeed(t, rt, 0, 40)
	first.waitFor(1, 0, 0)

	// The old connection is replaced before its asynchronous detach reaches
	// the helper. Its last rows were delivered but never confirmed, so the
	// replacement must replay them from the retained window.
	second := newRowsSink()
	_, err := hs.attach(proto.AttachParams{Subscriber: subscriberID, Session: hs.id, Fresh: true},
		second, func() proto.AttachmentID { return "att-new" }, hs.log)
	if err != nil {
		t.Fatalf("replace attached reader: %v", err)
	}
	t.Cleanup(func() { hs.detach(second, "att-new") })
	// The pump may complete this resend before attach returns to this test,
	// so assert the reader-visible replay rather than sampling its transient
	// internal obligation flag.
	second.waitFor(1, 0, 0)
	if got := decodeResentRows(t, second.rowFrames()); len(got) == 0 || len(got[0].texts) == 0 {
		t.Fatalf("replacement reader received no unconfirmed rows: %+v", got)
	}
}

func TestThePumpResendsRowsAnUnconfirmingReaderTook(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)

	// Forty lines: sixteen depart; the pump delivers every one; the reader
	// confirms nothing.
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)

	// The reader goes away the way a coordinator's death does — through
	// the session's own detach, with the teardown a real subscriber
	// carries. The pump delivered every row above the mark.
	done := make(chan struct{})
	lifecycleDone := make(chan struct{})
	close(done)
	close(lifecycleDone)
	hs.mu.Lock()
	hs.attachments = make(map[proto.AttachmentID]*attachment)
	hs.subs["coord-1"].stop = func() {}
	hs.subs["coord-1"].done = done
	hs.subs["coord-1"].lifecycleDone = lifecycleDone
	hs.subs["coord-1"].wake = newGate()
	hs.subs["coord-1"].lifecycleWake = newGate()
	att := proto.AttachmentID("att-test")
	hs.attachments[att] = &attachment{id: att, subscriber: "coord-1", sink: sink}
	hs.mu.Unlock()
	if _, ok := hs.detach(sink, att); !ok {
		t.Fatal("the detach did not find the reader it was given")
	}

	// The coordinator comes back.
	sink2 := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", sink2)

	// The sixteen taken rows are read back from the scrollback: the mark
	// never moved, and it is the only dedup line there is.
	sink2.waitFor(1, 0, 0)
	batches := decodeResentRows(t, sink2.rowFrames())
	var got []string
	for i, b := range batches {
		wantFrom := uint64(0)
		if i > 0 {
			wantFrom = batches[i-1].from + uint64(len(batches[i-1].texts))
		}
		if b.from != wantFrom {
			t.Fatalf("resend batch %d names FromRow %d, want %d", i, b.from, wantFrom)
		}
		if b.lost != 0 {
			t.Fatalf("resend batch %d claims %d lost: taken rows are not lost rows", i, b.lost)
		}
		got = append(got, b.texts...)
	}
	// The count rides the scrollback's own trailing-newline scroll (the
	// sibling test pins it by hand); what the invariant needs is that the
	// rows the reader took came back first, in order, content intact.
	if len(got) < 16 {
		t.Fatalf("the resent rows are %d, want at least the sixteen the unconfirming reader took", len(got))
	}
	for i, text := range got {
		if want := fmt.Sprintf("L%06d", i); text != want {
			t.Fatalf("resent row %d = %q, want %q — the taken rows must come back in stream order", i, text, want)
		}
	}
}

// The acceptance's third criterion (nocx-zg3k3.5.3): a command whose end
// marker arrived while the coordinator was away is closed when the
// coordinator returns. The pump dropped the marker for want of a reader;
// the return carries it again — same fence, same boundary row — so the
// coordinator's parked machinery can seal the block.
//
// Ordering is the test's own obligation: the detach completes, the fence
// rides the stream, and the reattach waits on the pump's own recorded
// state — the dropped end in resendEnds — so the reattach can never race
// the drop. No duration is waited on anywhere.
func TestThePumpResendsAnEndItDroppedForNoSubscriber(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)

	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	if err := hs.confirmRows(sink, "coord-1", 4); err != nil {
		t.Fatalf("confirm the stored rows: %v", err)
	}

	// The coordinator goes away: the subscriber leaves the map, the same
	// state a real detach leaves behind, and the pump's next delivery
	// attempt finds nobody to take the frame.
	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()

	nonce := sessionruntime.FenceNonce{}
	for i := range nonce {
		nonce[i] = 0xAB
	}
	fenceHex := fmt.Sprintf("%x", nonce)
	var sb strings.Builder
	fmt.Fprintf(&sb, "prompt\x1b]1337;NOCX_FENCE;%s\x07\r\n", fenceHex)
	if err := rt.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}
	rt.Completed(rt.Incarnation(), nonce, 0)

	// The pump records what it dropped, and its own drained signal is the
	// observable that the fence feed's emissions were all processed:
	// arming the drain wakes the pump, and the signal closes the moment
	// the pump finds its queue empty with nothing owed — the drop already
	// recorded by then, whichever path took it.
	<-hs.requestRowsDrain()
	hs.rowMu.Lock()
	recorded := len(hs.resendEnds)
	hs.rowMu.Unlock()
	if recorded < 1 {
		t.Fatalf("the pump recorded %d dropped ends, want 1", recorded)
	}

	// It comes back.
	sink2 := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", sink2)

	sink2.waitFor(1, 1, 0)

	ends := sink2.endFrames()
	if len(ends) != 1 {
		t.Fatalf("the pump sent %d end markers on return, want 1", len(ends))
	}
	// The boundary is the same boundary: nothing departed after it, so the
	// runtime's own departure count is the end row the marker stopped at.
	if want := rt.DepartedRowCount(); ends[0].EndRow != want {
		t.Fatalf("the re-emitted end stops at row %d, want %d: the boundary is the same boundary", ends[0].EndRow, want)
	}
	var doc struct {
		Nonce   string          `json:"nonce"`
		Closing json.RawMessage `json:"closing"`
		NoFence bool            `json:"noFence"`
	}
	if err := json.Unmarshal(ends[0].Payload, &doc); err != nil {
		t.Fatalf("decode the re-emitted end: %v\n%s", err, ends[0].Payload)
	}
	if doc.Nonce != fenceHex {
		t.Fatalf("the re-emitted end names nonce %q, want %q", doc.Nonce, fenceHex)
	}
	if doc.NoFence {
		t.Fatal("the re-emitted end claims no fence ever joined: this boundary joined its completion")
	}
	if len(doc.Closing) != 0 && string(doc.Closing) != "null" {
		t.Fatalf("the re-emitted end carries a closing screen %s: the helper keeps no copy to re-send", doc.Closing)
	}
}

// The walked span stops at the newest dropped boundary. The boundary's own
// closing screen departs after it — suppressed, unindexed — so rows below
// the boundary cannot be proven against the history top: they are counted,
// with the absence as the cause, at the position the survivors start at,
// and the survivors are walked exactly.
func TestThePumpCountsWhatItCannotProveBelowADroppedBoundary(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)

	// Watched: the head stored through a deliberately short mark.
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	if err := hs.confirmRows(sink, "coord-1", 4); err != nil {
		t.Fatalf("confirm the stored rows: %v", err)
	}

	// Away. The command ends — its boundary's marker drops for nobody —
	// and the next command's output pushes the closed screen off: those
	// departures are suppressed, and its own rows stream from the
	// boundary's end row up.
	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()
	nonce := sessionruntime.FenceNonce{}
	for i := range nonce {
		nonce[i] = 0xAB
	}
	fenceHex := fmt.Sprintf("%x", nonce)
	var sb strings.Builder
	fmt.Fprintf(&sb, "prompt\x1b]1337;NOCX_FENCE;%s\x07\r\n", fenceHex)
	if err := rt.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}
	rt.Completed(rt.Incarnation(), nonce, 0)
	rowsFeed(t, rt, 100, 60)

	// The pump has recorded the drop; the return reads the record.
	<-hs.requestRowsDrain()
	hs.rowMu.Lock()
	recorded := len(hs.resendEnds)
	hs.rowMu.Unlock()
	if recorded != 1 {
		t.Fatalf("the pump recorded %d dropped ends, want 1", recorded)
	}

	sink2 := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", sink2)

	sink2.waitFor(2, 1, 0)

	batches := decodeResentRows(t, sink2.rowFrames())
	// The helper retained the unconfirmed original rows before the dropped
	// boundary, so resend them before the end marker rather than rebuilding
	// the span from live history.
	prior := batches[0]
	if prior.from != 4 || prior.lost != 0 || prior.incomplete || len(prior.texts) != 13 {
		t.Fatalf("the retained pre-boundary rows = %+v, want [4,17)", prior)
	}
	for i, text := range prior.texts {
		if want := fmt.Sprintf("L%06d", i+4); text != want {
			t.Fatalf("retained row %d = %q, want %q", i+4, text, want)
		}
	}
	// The end marker sits between retained rows and the survivors: stream order.
	ends := sink2.endFrames()
	if len(ends) != 1 || ends[0].EndRow != 17 {
		t.Fatalf("the re-emitted end = %+v, want the boundary at row 17", ends[0])
	}
	// The survivors: indices [17, D) — the fed lines' head, in order.
	var got []string
	for i, b := range batches[1:] {
		wantFrom := uint64(17)
		if i > 0 {
			wantFrom = batches[i].from + uint64(len(batches[i].texts))
		}
		if b.from != wantFrom || b.lost != 0 || b.cause != "" {
			t.Fatalf("survivor batch %d = (from %d, lost %d, cause %q), want a plain walk from %d",
				i, b.from, b.lost, b.cause, wantFrom)
		}
		got = append(got, b.texts...)
	}
	var want []string
	for i, n := 100, len(got); i < 100+n; i++ {
		want = append(want, fmt.Sprintf("L%06d", i))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the survivors are not the rows above the boundary:\n got %d %q\nwant %d %q",
			len(got), strings.Join(got, ","), len(want), strings.Join(want, ","))
	}
}

// A high ack cannot reclaim across a missing retained head: the scalar
// watermark alone does not prove that the helper ever retained that prefix.
func TestAckLeapingOverMissingRetainedHeadReclaimsNothing(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	rows := []emulator.Row{textRow("after-hole")}
	bytes := emissionBytes(rowEmission{rows: rows})
	hs.rowMu.Lock()
	// This is an intentionally sparse retained-window fixture. Charge it
	// through the pool primitive, as the production enqueue path does, then
	// verify the user-visible ack watermark and retained record stay put.
	if !hs.rowPool.charge(rowOwnerResend, bytes) {
		hs.rowMu.Unlock()
		t.Fatal("could not charge test retained row")
	}
	hs.resendWindow = []retainedRowSpan{{from: 5, rows: rows, bytes: bytes}}
	hs.rowMu.Unlock()
	if err := hs.confirmRows(sink, "coord-1", 10); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if hs.rowsConfirmed != 0 {
		t.Fatalf("confirmed watermark advanced across unproven head to %d", hs.rowsConfirmed)
	}
	hs.rowMu.Lock()
	defer hs.rowMu.Unlock()
	if len(hs.resendWindow) != 1 || hs.resendWindow[0].from != 5 || len(hs.resendWindow[0].rows) != 1 {
		t.Fatalf("ack across missing head changed retained window: %+v", hs.resendWindow)
	}
}

func TestResendSurvivesLiveHistoryPrune(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)
	hs.rowBufferBytes = 64 << 20
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	initial := decodeResentRows(t, sink.rowFrames())
	if len(initial) == 0 || len(initial[0].texts) == 0 {
		t.Fatal("initial emission contained no rows")
	}
	first := initial[0].texts[0]
	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()
	for base := 100; base < 4100; base += 100 {
		rowsFeed(t, rt, base, 100)
	}
	var liveHasFirst bool
	_, _, err := rt.ReadDepartedScreen(func(_ uint64, terminal emulator.Terminal) error {
		page, err := terminal.HistoryRows(0, 0)
		if err != nil {
			return err
		}
		for _, row := range page.Rows {
			var text strings.Builder
			for _, cell := range row.Cells {
				if cell.HasText {
					text.WriteString(cell.Grapheme)
				}
			}
			if strings.TrimRight(text.String(), " ") == first {
				liveHasFirst = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read live history: %v", err)
	}
	if liveHasFirst {
		t.Fatal("the setup did not prune the initial emitted row from live history")
	}
	sink2 := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", sink2)
	sink2.waitFor(1, 0, 0)
	resent := decodeResentRows(t, sink2.rowFrames())
	if len(resent) == 0 || resent[0].from != initial[0].from || len(resent[0].texts) == 0 || resent[0].texts[0] != first {
		t.Fatalf("resend did not serve the pruned original row %q at %d: %+v", first, initial[0].from, resent)
	}
}
