package session

import (
	"bytes"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The window is D1's bound made a mechanism: output produced while no
// coordinator was attached is kept up to it, and beyond it the bytes are gone
// and the gap is REPORTED. Everything below is that sentence, asserted.

// readAll drains the window from offset until it is caught up, returning the
// bytes and the last resume verdict. It exists because a single read is
// deliberately bounded (D8), so "what does the window hold" is a loop.
func readAll(t *testing.T, w *window, from proto.StreamOffset) ([]byte, proto.Resume) {
	t.Helper()
	var out bytes.Buffer
	at := from
	for i := 0; ; i++ {
		if i > 10000 {
			t.Fatal("read loop did not converge")
		}
		data, r := w.read(at)
		if r.Reset {
			return out.Bytes(), r
		}
		if len(data) == 0 {
			return out.Bytes(), r
		}
		out.Write(data)
		at = r.From + proto.StreamOffset(len(data))
	}
}

// TestTheWindowServesWhatItStillHolds is the paired positive AGENTS.md asks
// for beside every loss case below: on an ordinary session that has produced
// less than its bound, a reader attaching at zero gets every byte.
func TestTheWindowServesWhatItStillHolds(t *testing.T) {
	w := newWindow(64 * 1024)
	defer w.close()

	want := bytes.Repeat([]byte("abcdefgh"), 1000) // 8000 bytes, inside the bound
	w.write(want)

	got, r := readAll(t, w, 0)
	if r.Reset {
		t.Fatalf("a reader inside the window was reset: %+v", r)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, want %d", len(got), len(want))
	}
	if base, written := w.span(); base != 0 || written != proto.StreamOffset(len(want)) {
		t.Errorf("span = [%d,%d], want [0,%d]", base, written, len(want))
	}
}

// TestTheProducerIsNeverThrottledByAnAbsentReader is D1 and D8's amendment to
// AD-10 in one assertion: the window is capacity-reclaimed rather than
// blocking, so a three-hour build continues while nobody is watching instead
// of stopping because nobody is watching. The coordinator's own ring makes the
// opposite trade deliberately — it is lossless and throttles the source — and
// the two must not be confused.
func TestTheProducerIsNeverThrottledByAnAbsentReader(t *testing.T) {
	w := newWindow(64 * 1024)
	defer w.close()

	// Twenty times the bound, with nobody reading at all. If a write ever
	// parked on a consumer, this never returns and the test's own timeout
	// reports it — no duration is waited on.
	chunk := bytes.Repeat([]byte("x"), 8*1024)
	for i := 0; i < 160; i++ {
		w.write(chunk)
	}

	_, written := w.span()
	if written != proto.StreamOffset(160*len(chunk)) { //nolint:gosec // a test's own 1.25 MiB constant
		t.Fatalf("written = %d, want every byte accounted for", written)
	}
}

// TestAReclaimedOffsetIsResetToTheBaseAndTheLossIsStated is the whole of D1's
// bound: beyond the window the bytes are gone, and the gap is REPORTED. A
// window that quietly served the oldest bytes it happened to still have would
// splice a hole into the reader's stream — the silent degrade AGENTS.md
// forbids, and here it would corrupt a UTF-8 sequence as well.
func TestAReclaimedOffsetIsResetToTheBaseAndTheLossIsStated(t *testing.T) {
	w := newWindow(64 * 1024)
	defer w.close()

	chunk := bytes.Repeat([]byte("y"), 8*1024)
	for i := 0; i < 40; i++ { // 320 KiB through a 64 KiB window
		w.write(chunk)
	}

	base, written := w.span()
	if base == 0 {
		t.Fatal("the window reclaimed nothing: it is not bounded")
	}
	data, r := w.read(0)
	if !r.Reset {
		t.Fatalf("a reclaimed offset was not reset: %+v", r)
	}
	if len(data) != 0 {
		t.Errorf("a reset answer carried %d bytes: the reader must clear first", len(data))
	}
	if r.From != base {
		t.Errorf("reset to %d, want the window's base %d — the bytes that still exist", r.From, base)
	}
	if r.Gap == nil || r.Gap.Start != 0 || r.Gap.End != base {
		t.Fatalf("gap = %+v, want [0,%d)", r.Gap, base)
	}
	if r.Gap.Reason != proto.GapReasonWindow {
		t.Errorf("reason = %q, want %q: nobody ever held these bytes", r.Gap.Reason, proto.GapReasonWindow)
	}
	// And the reader restarting where it was told is served, rather than
	// reset a second time — which is the loop a wrong restart point produces.
	got, r2 := readAll(t, w, r.From)
	if r2.Reset {
		t.Fatalf("the reader was reset again at the base it was given: %+v", r2)
	}
	if proto.StreamOffset(len(got)) != written-base {
		t.Errorf("read %d bytes from the base, want %d", len(got), written-base)
	}
}

// TestTheWindowsAllocationIsBoundedAndReclaimed is D8's assertion, phrased the
// way D8 insists it be phrased: on ALLOCATED bytes after fill → consume →
// idle, not on a buffer's length. A slice whose backing array is never
// released satisfies a length assertion and holds the peak forever, which is
// exactly the defect a naive port of the coordinator's ring would have shipped.
func TestTheWindowsAllocationIsBoundedAndReclaimed(t *testing.T) {
	const bound = 64 * 1024
	w := newWindow(bound)
	defer w.close()

	chunk := bytes.Repeat([]byte("z"), 8*1024)
	for i := 0; i < 200; i++ { // 1.6 MiB through a 64 KiB window
		w.write(chunk)
		// Consume as we go: a reader keeping up must not change the bound
		// either, and this is where a "grows by append, trims by reslice"
		// representation would quietly retain its peak.
		base, _ := w.span()
		_, _ = readAll(t, w, base)
	}

	if got := w.allocated(); got > bound+pageSize {
		t.Fatalf("the window holds %d allocated bytes for a %d-byte bound", got, bound)
	}
}

// TestASinglePullIsBounded is D8's second representation obligation: a reader
// must never make the window copy everything it holds in order to send one
// frame. A snapshot-the-whole-window read would transiently double a 4 MiB
// window to deliver 8 KiB.
func TestASinglePullIsBounded(t *testing.T) {
	w := newWindow(1024 * 1024)
	defer w.close()
	w.write(bytes.Repeat([]byte("w"), 1024*1024))

	data, r := w.read(0)
	if r.Reset {
		t.Fatalf("unexpected reset: %+v", r)
	}
	if len(data) == 0 {
		t.Fatal("a full window served nothing")
	}
	if len(data) > pageSize {
		t.Fatalf("one pull copied %d bytes, want at most one page (%d)", len(data), pageSize)
	}
}

// TestReadingPastTheEndWaitsRatherThanResetting is the failure path on the far
// side of the window, and it matters because the two are easy to conflate: a
// cursor ahead of what was produced has lost nothing, so telling it that bytes
// were lost would be a false statement in the product.
func TestReadingPastTheEndWaitsRatherThanResetting(t *testing.T) {
	w := newWindow(64 * 1024)
	defer w.close()
	w.write([]byte("hello"))

	data, r := w.read(5)
	if r.Reset || len(data) != 0 {
		t.Fatalf("a cursor at the end got reset=%v data=%q", r.Reset, data)
	}
	// The changed channel is what a pump waits on, so that it waits on an
	// observable state change rather than on a duration.
	waiting := w.changed()
	w.write([]byte("!"))
	select {
	case <-waiting:
	default:
		t.Fatal("a write did not wake the reader parked at the end of the stream")
	}
	if data, r := w.read(5); r.Reset || string(data) != "!" {
		t.Fatalf("after the write: data=%q reset=%v", data, r.Reset)
	}
}

// TestAClosedWindowStopsServingAndWakesItsReaders — the interval, both ends
// named: from the moment the window is created until close, a parked reader is
// woken by a write or by the close and never by anything else; after close it
// is told the window is over rather than being left parked on a channel
// nothing will ever signal.
func TestAClosedWindowStopsServingAndWakesItsReaders(t *testing.T) {
	w := newWindow(64 * 1024)
	w.write([]byte("tail"))
	waiting := w.changed()
	w.close()

	select {
	case <-waiting:
	default:
		t.Fatal("close did not wake a parked reader")
	}
	if !w.isClosed() {
		t.Fatal("the window does not report itself closed")
	}
	// A closed window still serves what it holds: the process ended, the
	// bytes it produced did not stop existing, and a reader attaching after
	// an exit must still be able to read the last thing the shell printed.
	if data, r := w.read(0); r.Reset || string(data) != "tail" {
		t.Fatalf("a closed window lost its bytes: data=%q reset=%v", data, r.Reset)
	}
}
