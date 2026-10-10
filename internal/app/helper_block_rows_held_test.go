package app

import (
	"context"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/waittest"
)

// orderedSink records every call the rows bridge makes, in order, so a test
// can see a row reach the transport before the stream it belongs to exists.
type orderedSink struct {
	*fakeSink
	mu     sync.Mutex
	events []string
}

func (o *orderedSink) note(e string) {
	o.mu.Lock()
	o.events = append(o.events, e)
	o.mu.Unlock()
}

func (o *orderedSink) AttachBlockRows(sid session.ID) {
	o.note("attach")
	o.fakeSink.AttachBlockRows(sid)
}

func (o *orderedSink) BlockRowsArrived(sid session.ID, fromRow, lost uint64, rows []emulator.Row, cause string) (uint64, bool) {
	o.note("rows")
	return o.fakeSink.BlockRowsArrived(sid, fromRow, lost, rows, cause)
}

func (o *orderedSink) BlockIntervalEnded(sid session.ID, nonce [32]byte, endRow uint64, closing []emulator.Row, noFence bool) {
	o.note("end")
	o.fakeSink.BlockIntervalEnded(sid, nonce, endRow, closing, noFence)
}

func (o *orderedSink) BlockClearBoundary(sid session.ID) {
	o.note("clear")
	o.fakeSink.BlockClearBoundary(sid)
}

// THE ROWS PLANE IS HELD FROM THE ATTACH TO THE BIND (nocx-zg3k3.5.11). The
// helper can send a returning coordinator its owed rows before it has even
// answered the attach, and the transport's stream for the session is bound
// only after the attach returns. Whatever arrives in between is held, and
// reaches the transport in arrival order once the stream exists — after the
// re-bind, so the rows land in the block the store still holds open — and
// everything after passes straight through.
func TestRowsArrivingBetweenTheAttachAndTheBindReachTheStreamInOrder(t *testing.T) {
	sink := &orderedSink{fakeSink: &fakeSink{answer: func(uint64, int) (uint64, bool) { return 0, false }}}
	src := &fakeSource{}
	held := &heldRows{}
	held.observe(src)

	var nonce sessionruntime.FenceNonce
	nonce[0] = 0xcd
	src.deliverRows(client.OutputRows{FromRow: 177, Rows: rowsN(23)})
	src.deliverRows(client.OutputRows{FromRow: 200, Rows: rowsN(77)})
	src.deliverEnd(client.IntervalEnd{Nonce: nonce, EndRow: 277, Closing: rowsN(23)})

	conf := &gatedConfirmer{asked: make(chan uint64, 8), release: make(chan struct{})}
	stop := held.bindAfter(context.Background(), sink, "s1", conf, nil)
	defer stop()
	src.deliverClear()

	sink.mu.Lock()
	events := append([]string(nil), sink.events...)
	sink.mu.Unlock()
	want := []string{"attach", "rows", "rows", "end", "clear"}
	if len(events) != len(want) {
		t.Fatalf("the transport saw %v, want %v: rows that arrived before the stream was bound were lost or reordered", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("the transport saw %v, want %v", events, want)
		}
	}
	sink.fakeSink.mu.Lock()
	defer sink.fakeSink.mu.Unlock()
	if sink.rows[0].FromRow != 177 || sink.rows[1].FromRow != 200 || sink.ends[0].EndRow != 277 {
		t.Fatalf("the held frames reached the transport as rows %+v, end %+v", sink.rows, sink.ends)
	}
}

// A RE-ADOPTED PANE'S ROWS WAIT FOR ITS REPLAYED LIFECYCLE WINDOW
// (nocx-zg3k3.5.11, the loaded bar after the teardown fix: 8 of 20 sealed at
// 23 rows of 300). The helper sends a returning coordinator its read-back —
// every row from the command's start — the moment the subscriber exists, but
// the command's block is opened by the start frame the replayed window
// carries, which the adopted leg applies only afterwards when the previous
// coordinator left that frame to it. Rows released at the bind found no
// block, were dropped unconfirmed, and a read-back is sent once. So the held
// rows, and everything behind them, reach the stream only once the leg has
// applied the window it was handed — and then in arrival order.
func TestAReadoptedPanesRowsWaitForItsReplayedWindow(t *testing.T) {
	sink := &orderedSink{fakeSink: &fakeSink{answer: func(uint64, int) (uint64, bool) { return 0, false }}}
	src := &fakeSource{}
	held := &heldRows{}
	held.observe(src)
	src.deliverRows(client.OutputRows{FromRow: 0, Rows: rowsN(32)}) // the read-back, before the attach answered

	replayed := make(chan struct{})
	conf := &gatedConfirmer{asked: make(chan uint64, 8), release: make(chan struct{})}
	stop := held.bindAfter(context.Background(), sink, "s1", conf, replayed)
	defer stop()
	src.deliverRows(client.OutputRows{FromRow: 32, Rows: rowsN(32)}) // after the bind, before the replay

	sink.mu.Lock()
	early := append([]string(nil), sink.events...)
	sink.mu.Unlock()
	for _, e := range early {
		if e == "rows" {
			t.Fatalf("the stream saw %v before the replayed window was applied: rows reached it with no block to take them", early)
		}
	}

	close(replayed) // the leg applied its window: the start opened the block
	waittest.WaitFor(t, "the held rows to reach the stream once the window was applied", func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		n := 0
		for _, e := range sink.events {
			if e == "rows" {
				n++
			}
		}
		return n == 2
	})
	src.deliverRows(client.OutputRows{FromRow: 64, Rows: rowsN(32)})
	sink.mu.Lock()
	events := append([]string(nil), sink.events...)
	sink.mu.Unlock()
	want := []string{"attach", "rows", "rows", "rows"}
	if len(events) != len(want) {
		t.Fatalf("the transport saw %v, want %v", events, want)
	}
}
