package session

// nocx-zg3k3.5.11 Round 7, the 19/20 loaded residual: the coordinator's log
// proved that after a re-bind at cursor=177 exactly one delivery ([177..200))
// crossed, nothing else did, and the interval's end sealed the block at what
// had arrived (endRow=277). This seam reproduces that ordering at the helper's
// own pump — the walk runs against the departed snapshot D=200 (the re-adopt
// raced the command's completion), the command then runs on to its real end,
// and everything in [mark, endRow) must reach the SAME subscriber: the walk's
// rows, the live tail, and the end marker, in stream order — or an honestly
// counted loss. Whichever arm strands the tail is the defect this test pins.

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

func TestTheTailAfterAReturnedWalkStreamsAndEndsOnTheSameSubscriber(t *testing.T) {
	hs, rt, sink1 := rowsBridgeSession(t, 80, 24)

	// The command prints 224 lines on a 24-row screen: departures reach 200
	// while coord-1 is bound. It confirms through 177 and dies the way a
	// coordinator's death does — the session's own detach, which arms the
	// resend once.
	rowsFeed(t, rt, 0, 224)
	sink1.waitFor(1, 0, 0)
	if err := hs.confirmRows(sink1, "coord-1", 177); err != nil {
		t.Fatalf("confirm through 177: %v", err)
	}

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
	att := proto.AttachmentID("att-tail")
	hs.attachments[att] = &attachment{id: att, subscriber: "coord-1", sink: sink1}
	hs.mu.Unlock()
	if _, ok := hs.detach(sink1, att); !ok {
		t.Fatal("the detach did not find the reader it was given")
	}

	// The return: coord-2 binds, the pump wakes, and the walk runs against
	// the departed snapshot D=200.
	sink2 := newRowsSink()
	rowsAttachReader(t, hs, "22222222222222222222222222222222", sink2)
	sink2.waitFor(1, 0, 0)

	// THE COMMAND RUNS ON: 77 more lines depart 200..277, live to the same
	// subscriber, and the interval ends at 277.
	rowsFeed(t, rt, 224, 77)
	var nonce sessionruntime.FenceNonce
	for i := range nonce {
		nonce[i] = byte(9)
	}
	// The end names where departures actually are: a feed's trailing
	// newline scrolls one extra row (the sibling test pins the same
	// arithmetic), so the departed head after these feeds is 278.
	hs.enqueueRowEmission(rowEmission{end: true, nonce: nonce, from: 278})
	hs.wakeRows()

	// The end frame is the FIFO's last item, so its arrival means every
	// row ahead of it has been handed to this sink already.
	sink2.waitFor(0, 1, 0)

	// THE INVARIANT: the walk may dip below the mark (Round 8: the
	// interval's own surviving start; the coordinator trims the overlap it
	// holds), but the deliveries must be CONTIGUOUS through 277 — the
	// walk's rows and the live tail on one index line, none stated lost —
	// with exactly the interval's own end marker after them.
	batches := decodeResentRows(t, sink2.rowFrames())
	if len(batches) == 0 {
		t.Fatal("the subscriber received no rows at all")
	}
	if batches[0].from > 177 {
		t.Fatalf("the walk started at %d, above the confirmed mark 177 — the head rows are this command's own", batches[0].from)
	}
	next := batches[0].from
	for i, b := range batches {
		if b.from != next {
			t.Fatalf("delivery %d names FromRow %d, want %d — the index never skips and no row in [177, 277) may vanish", i, b.from, next)
		}
		if b.lost != 0 {
			t.Fatalf("delivery %d claims %d lost: the tail's rows were streamed, and an unstreamed absence must be stated, not implied", i, b.lost)
		}
		next = b.from + uint64(len(b.texts))
	}
	if next != 278 {
		t.Fatalf("the subscriber holds rows through %d, want 278 — the tail after the returned walk never reached it", next)
	}
	ends := sink2.endFrames()
	if len(ends) != 1 {
		t.Fatalf("the subscriber saw %d end markers, want exactly the interval's own", len(ends))
	}
	var endDoc struct {
		EndRow uint64 `json:"endRow"`
	}
	if err := json.Unmarshal(ends[0].Payload, &endDoc); err != nil {
		t.Fatalf("decode the end marker: %v", err)
	}
	if endDoc.EndRow != 278 {
		t.Fatalf("the end marker names endRow %d, want 278", endDoc.EndRow)
	}
}
