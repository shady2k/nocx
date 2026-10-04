package session

// Round 8 (nocx-zg3k3.5.3): the resend's walk is bounded from below by the
// running interval's own first streamed row — not only by the confirmed
// mark. A coordinator whose block opened late acks past rows its artifact
// never held (the mark leaps); the head rows are still this command's own,
// and the resend must offer them again. The coordinator trims what it
// already holds.

import "testing"

func TestTheResendWalksFromTheRunningIntervalsStart(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)

	// The interval opens with the shell's output mark, and its first rows
	// stream to the watching coordinator: absolute [0..16) depart and are
	// confirmed.
	if err := rt.Ingest([]byte("\x1b]133;C\x07")); err != nil {
		t.Fatalf("ingest the output mark: %v", err)
	}
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	if err := hs.confirmRows(sink, "coord-1", 16); err != nil {
		t.Fatalf("confirm the stored rows: %v", err)
	}

	// The coordinator goes away; the command keeps printing for nobody.
	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()
	rowsFeed(t, rt, 100, 100)

	// It comes back, and the resend runs.
	sink2 := newRowsSink()
	hs.mu.Lock()
	hs.subs["coord-2"] = &subscriber{id: "coord-2", raw: mintRaw(t), sink: sink2}
	hs.mu.Unlock()
	hs.wakeRows()
	sink2.waitFor(4, 0, 0)

	batches := decodeResentRows(t, sink2.rowFrames())
	if len(batches) == 0 {
		t.Fatalf("the resend delivered nothing")
	}
	if batches[0].from != 16 {
		t.Fatalf("the resend starts at %d, want the prefix-confirmed watermark 16", batches[0].from)
	}
}

// The interval ENDS before the coordinator returns — the tail-unwatched
// ordering, where the release happens after the shutdown and the command
// has completed by the time the re-adopt attaches. The running interval's
// own start dies with it, and the mark's own leap (the late ack above) is
// then the only bound left: the head rows are lost for good unless the
// walk's lower bound survives the interval's end.
func TestTheResendWalksFromTheEarliestIntervalEvenAfterItEnds(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)

	// The interval opens, streams [0..16), and the coordinator confirms
	// that retained prefix. Those bytes can now be reclaimed.
	if err := rt.Ingest([]byte("\x1b]133;C\x07")); err != nil {
		t.Fatalf("ingest the output mark: %v", err)
	}
	rowsFeed(t, rt, 0, 40)
	sink.waitFor(1, 0, 0)
	if err := hs.confirmRows(sink, "coord-1", 16); err != nil {
		t.Fatalf("confirm the stored rows: %v", err)
	}

	// The command ends while the coordinator is away. The retained window
	// still defines the unconfirmed resend span.
	if err := rt.Ingest([]byte("\x1b]133;D\x07")); err != nil {
		t.Fatalf("ingest the command end: %v", err)
	}

	// The coordinator goes away; rows depart for nobody.
	hs.mu.Lock()
	delete(hs.subs, "coord-1")
	hs.mu.Unlock()
	rowsFeed(t, rt, 100, 100)

	// It comes back, and the resend runs.
	sink2 := newRowsSink()
	hs.mu.Lock()
	hs.subs["coord-2"] = &subscriber{id: "coord-2", raw: mintRaw(t), sink: sink2}
	hs.mu.Unlock()
	hs.wakeRows()
	sink2.waitFor(1, 0, 0)

	batches := decodeResentRows(t, sink2.rowFrames())
	if len(batches) == 0 {
		t.Fatalf("the resend delivered nothing")
	}
	if batches[0].from != 16 {
		t.Fatalf("the resend starts at %d, want the prefix-confirmed watermark 16", batches[0].from)
	}
}
