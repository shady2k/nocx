package transport

// A buffer that overflows ends the block in flight INCOMPLETE and records
// nothing until the next command starts after the stream is healthy
// (nocx-2v80t.3.36, the owner's rule). The helper's buffer says so with one
// marker; the coordinator's own buffer, holding rows while its store is slow,
// says so to itself. Either way: the command running through the overflow is
// stored as a gap and said closed, a command started during it ends the same
// way rather than staying open, and the command after the first end that
// arrives once the stream is healthy is recorded whole. No end is resolved to
// "whichever block is current" — each block is settled by its own fence.

import (
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

func TestAnIncompleteMarkerEndsTheBlockInFlightIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		overflow bool
	}{
		{name: "the helper's buffer overflowed", overflow: true},
		{name: "the stream kept up", overflow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
			sid := session.ID(sidStr)
			e.ws.AttachBlockRows(sid)

			// A: running when the helper's buffer overflows.
			a := startsACommand(t, e, pub, lane, h, 2, "make a")
			if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("a0"), aStreamRow("a1")}); !confirm {
				t.Fatal("A's rows were not confirmed")
			}
			if tc.overflow {
				e.ws.BlockOutputIncomplete(sid, 2)
				// What the helper still sends until it can record again is
				// confirmed and not kept.
				if up, confirm := e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("unrecorded")}); !confirm || up != 3 {
					t.Fatalf("a row after the marker was answered (%d, %v), want confirmed through 3", up, confirm)
				}
			} else {
				e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("a2")})
			}
			fenceA := lifecycleFence(0x41)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(a), 0, fenceA)))
			if !tc.overflow {
				e.ws.BlockIntervalEnded(sid, fenceA, 3, []emulator.Row{aStreamRow("$ ")}, false)
				assertSealedAs(t, db, a, nil)
				if rows := streamRows(t, db, a); len(rows) != 4 {
					t.Fatalf("A holds %d rows, want its three and the closing screen", len(rows))
				}
				return
			}
			// A's end never comes from the helper — the buffer dropped it —
			// and its own completion settles it.
			assertSealedAs(t, db, a, gap)
			if rows := streamRows(t, db, a); len(rows) != 2 {
				t.Fatalf("A holds %d rows, want the two recorded before the overflow", len(rows))
			}
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))

			// B: started and finished while nothing could be recorded.
			b := startsACommand(t, e, pub, lane, h, 5, "make b")
			e.ws.BlockRowsArrived(sid, 3, 0, []emulator.Row{aStreamRow("b-unrecorded")})
			fenceB := lifecycleFence(0x42)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(b), 0, fenceB)))
			assertSealedAs(t, db, b, gap)
			if n := closedCount(t, e, sid, b); n != 1 {
				t.Fatalf("B, started during the overflow, was said closed %d times, want once", n)
			}
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 7, lifecyclePromptEvt()))

			// C: running when the stream is healthy again. Its end is the
			// first the helper carries after the overflow, with C's own
			// fence; C started during the overflow, so it is incomplete.
			c := startsACommand(t, e, pub, lane, h, 8, "make c")
			fenceC := lifecycleFence(0x43)
			e.ws.BlockIntervalEnded(sid, fenceC, 4, []emulator.Row{aStreamRow("$ ")}, false)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 9, lifecycleCompleteEvt(lifecycle.AttemptID(c), 0, fenceC)))
			assertSealedAs(t, db, c, gap)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 10, lifecyclePromptEvt()))

			// D: the next command, recorded whole.
			d := startsACommand(t, e, pub, lane, h, 11, "make d")
			if _, confirm := e.ws.BlockRowsArrived(sid, 5, 0, []emulator.Row{aStreamRow("d0")}); !confirm {
				t.Fatal("D's row was not confirmed")
			}
			fenceD := lifecycleFence(0x44)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 12, lifecycleCompleteEvt(lifecycle.AttemptID(d), 0, fenceD)))
			e.ws.BlockIntervalEnded(sid, fenceD, 6, []emulator.Row{aStreamRow("$ ")}, false)
			assertSealedAs(t, db, d, nil)
			if rows := streamRows(t, db, d); len(rows) != 2 || rows[0].Text != "d0" {
				t.Fatalf("D holds %+v, want d0 and its closing screen", rows)
			}
			for _, entry := range []string{a, b, c, d} {
				if n := closedCount(t, e, sid, entry); n > 1 {
					t.Fatalf("%s was said closed %d times", entry, n)
				}
			}
		})
	}
}

// The coordinator's own buffer (nocx-2v80t.3.36): rows held while the store
// cannot take them yet are bounded by the session's configured bytes. Past
// them the same rule applies — the block in flight ends incomplete, nothing
// is kept until the next command after the stream recovers — and the memory
// held never passes the bound. Paired with the same rows inside the bound,
// which are all kept.
func TestTheCoordinatorsBufferEndsTheBlockInFlightIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rows     int
		overflow bool
	}{
		{name: "past the buffer", rows: 200, overflow: true},
		{name: "within the buffer", rows: 4, overflow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
			sid := session.ID(sidStr)
			one := heldRowsBytes([]emulator.Row{aStreamRow("row")})
			e.ws.SetBlockRowsBufferBytes(10 * one)
			e.ws.AttachBlockRows(sid)
			a := startsACommand(t, e, pub, lane, h, 2, "make a")

			// The store is slow: a deferred append is in flight, so what
			// arrives is held here.
			e.ws.blockStream.mu.Lock()
			e.ws.blockStream.flushing[sid] = true
			e.ws.blockStream.mu.Unlock()
			for i := range tc.rows {
				e.ws.BlockRowsArrived(sid, uint64(i), 0, []emulator.Row{aStreamRow("row")}) //nolint:gosec // a small index
				e.ws.blockStream.mu.Lock()
				held := e.ws.blockStream.heldBytesLocked(sid)
				e.ws.blockStream.mu.Unlock()
				if held > 10*one {
					t.Fatalf("the coordinator holds %d bytes, past its %d-byte buffer", held, 10*one)
				}
			}
			e.ws.blockStream.mu.Lock()
			e.ws.blockStream.flushing[sid] = false
			e.ws.blockStream.mu.Unlock()

			fenceA := lifecycleFence(0x51)
			end := uint64(tc.rows) //nolint:gosec // a small count
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(a), 0, fenceA)))
			e.ws.BlockIntervalEnded(sid, fenceA, end, []emulator.Row{aStreamRow("$ ")}, false)
			if !tc.overflow {
				assertSealedAs(t, db, a, nil)
				return
			}
			assertSealedAs(t, db, a, gap)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))

			// The next command, after the store and the stream recovered.
			d := startsACommand(t, e, pub, lane, h, 5, "make d")
			if _, confirm := e.ws.BlockRowsArrived(sid, end, 0, []emulator.Row{aStreamRow("d0")}); !confirm {
				t.Fatal("the next command's row was not confirmed")
			}
			fenceD := lifecycleFence(0x52)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(d), 0, fenceD)))
			e.ws.BlockIntervalEnded(sid, fenceD, end+1, []emulator.Row{aStreamRow("$ ")}, false)
			assertSealedAs(t, db, d, nil)
		})
	}
}

// The coordinator's buffer is the person's setting, and a session keeps the
// value it was attached with: a change applies to the next session.
func TestTheCoordinatorsBufferIsTheOneTheSessionWasOpenedWith(t *testing.T) {
	e, _, _, _, sidStr, _ := newLifecycleLedgerEnv(t, true)
	first := session.ID(sidStr)
	second := session.ID("0123456789abcdef0123456789abcdef")

	e.ws.SetBlockRowsBufferBytes(3 << 20)
	e.ws.AttachBlockRows(first)
	e.ws.SetBlockRowsBufferBytes(5 << 20)
	e.ws.AttachBlockRows(second)

	e.ws.blockStream.mu.Lock()
	defer e.ws.blockStream.mu.Unlock()
	if got := e.ws.blockStream.budgets[first]; got != 3<<20 {
		t.Fatalf("the first session's buffer is %d bytes, want the 3 MB it was opened with", got)
	}
	if got := e.ws.blockStream.budgets[second]; got != 5<<20 {
		t.Fatalf("a session opened after the change has %d bytes, want 5 MB", got)
	}
}

// Paired: with nothing configured, a session gets the default.
func TestTheCoordinatorsBufferDefaultsWhenNothingIsConfigured(t *testing.T) {
	e, _, _, _, sidStr, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sidStr))
	e.ws.blockStream.mu.Lock()
	defer e.ws.blockStream.mu.Unlock()
	if got := e.ws.blockStream.budgets[session.ID(sidStr)]; got != DefaultBlockRowsBufferBytes {
		t.Fatalf("an unconfigured session's buffer is %d bytes, want the default %d", got, DefaultBlockRowsBufferBytes)
	}
}
