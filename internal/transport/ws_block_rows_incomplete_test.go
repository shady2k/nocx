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
	"encoding/hex"
	"strings"
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
			// A wide row, so the floor-sized buffer overflows within a few
			// dozen of them.
			wide := aStreamRow(strings.Repeat("r", 2000))
			bound := MinBlockRowsBufferBytes
			e.ws.SetBlockRowsBufferBytes(bound)
			e.ws.AttachBlockRows(sid)
			a := startsACommand(t, e, pub, lane, h, 2, "make a")

			// The store is slow: a deferred append is in flight, so what
			// arrives is held here.
			e.ws.blockStream.mu.Lock()
			e.ws.blockStream.flushing[sid] = true
			e.ws.blockStream.mu.Unlock()
			for i := range tc.rows {
				e.ws.BlockRowsArrived(sid, uint64(i), 0, []emulator.Row{wide}) //nolint:gosec // a small index
				assertHeldWithin(t, e, sid, bound)
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

	e.ws.SetBlockRowsBufferBytes(5 << 20)
	e.ws.AttachBlockRows(first)
	e.ws.SetBlockRowsBufferBytes(7 << 20)
	e.ws.AttachBlockRows(second)

	e.ws.blockStream.mu.Lock()
	defer e.ws.blockStream.mu.Unlock()
	if got := e.ws.blockStream.budgets[first]; got != 5<<20 {
		t.Fatalf("the first session's buffer is %d bytes, want the 5 MB it was opened with", got)
	}
	if got := e.ws.blockStream.budgets[second]; got != 7<<20 {
		t.Fatalf("a session opened after the change has %d bytes, want 7 MB", got)
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

// assertHeldWithin fails when the session's buffer holds more than bound.
func assertHeldWithin(t *testing.T, e *lifecycleTestEnv, sid session.ID, bound int64) {
	t.Helper()
	e.ws.blockStream.mu.Lock()
	held := e.ws.blockStream.heldBytesLocked(sid)
	e.ws.blockStream.mu.Unlock()
	if held > bound {
		t.Fatalf("the coordinator holds %d bytes, past its %d-byte buffer", held, bound)
	}
}

// The coordinator owns a floor too (nocx-2v80t.3.38): a buffer that cannot
// hold one closing screen would end every block incomplete at its first end.
// A one-byte setting gets the floor; one above it is kept
// (TestTheCoordinatorsBufferIsTheOneTheSessionWasOpenedWith).
func TestTheCoordinatorsBufferHasAFloor(t *testing.T) {
	e, _, _, _, sidStr, _ := newLifecycleLedgerEnv(t, true)
	e.ws.SetBlockRowsBufferBytes(1)
	e.ws.AttachBlockRows(session.ID(sidStr))
	e.ws.blockStream.mu.Lock()
	defer e.ws.blockStream.mu.Unlock()
	if got := e.ws.blockStream.budgets[session.ID(sidStr)]; got != MinBlockRowsBufferBytes {
		t.Fatalf("a one-byte setting gave a %d-byte buffer, want the floor %d", got, MinBlockRowsBufferBytes)
	}
}

// A completion may arrive before its output does (ADR-0024 decision 7), so
// it can precede the overflow marker (nocx-2v80t.3.38, the review's blocker).
// The marker still settles that block — the one in flight, whose end the
// helper will never send — and after recovery the next command's rows go to
// the next command's block, not to the stale one. Paired with the marker
// arriving first (TestAnIncompleteMarkerEndsTheBlockInFlightIncomplete).
func TestAnIncompleteMarkerAfterItsCompletionStillSettlesTheBlock(t *testing.T) {
	e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)

	a := startsACommand(t, e, pub, lane, h, 2, "make a")
	if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{aStreamRow("a0")}); !confirm {
		t.Fatal("A's row was not confirmed")
	}
	fenceA := lifecycleFence(0x61)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(a), 0, fenceA)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecyclePromptEvt()))

	e.ws.BlockOutputIncomplete(sid, 1)

	assertSealedAs(t, db, a, gap)
	if n := closedCount(t, e, sid, a); n != 1 {
		t.Fatalf("A was said closed %d times, want once", n)
	}

	// Recovery: the next end the helper carries is B's, B ran through the
	// overflow and is incomplete; C, after it, is recorded whole.
	b := startsACommand(t, e, pub, lane, h, 5, "make b")
	fenceB := lifecycleFence(0x62)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 6, lifecycleCompleteEvt(lifecycle.AttemptID(b), 0, fenceB)))
	e.ws.BlockIntervalEnded(sid, fenceB, 2, nil, false)
	assertSealedAs(t, db, b, gap)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 7, lifecyclePromptEvt()))

	c := startsACommand(t, e, pub, lane, h, 8, "make c")
	if _, confirm := e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("c0")}); !confirm {
		t.Fatal("C's row was not confirmed")
	}
	if rows := streamRows(t, db, a); len(rows) != 1 {
		t.Fatalf("A holds %+v after recovery, want only its own row — the next command's rows went to it", rows)
	}
	if rows := streamRows(t, db, c); len(rows) != 1 || rows[0].Text != "c0" {
		t.Fatalf("C holds %+v, want its own row", rows)
	}
}

// Every queue that holds a closing screen counts against the coordinator's
// buffer (nocx-2v80t.3.38): an end parked for its completion, an end held
// behind a deferred append, and an end queued for a block not yet current.
// A closing screen that would take the buffer past its bound is not kept —
// that end closes its block incomplete — and the end itself is never lost.
// Paired: a closing screen within the bound is kept whole.
func TestEveryEndQueueCountsAgainstTheCoordinatorsBuffer(t *testing.T) {
	bound := MinBlockRowsBufferBytes
	screen := func(rows int) []emulator.Row {
		out := make([]emulator.Row, rows)
		for i := range out {
			out[i] = aStreamRow(strings.Repeat("s", 2000))
		}
		return out
	}
	big := screen(40)  // ~3.3 MB: one fits, two do not
	small := screen(1) // well within

	t.Run("parked for their completions", func(t *testing.T) {
		e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
		sid := session.ID(sidStr)
		e.ws.SetBlockRowsBufferBytes(bound)
		e.ws.AttachBlockRows(sid)
		a := startsACommand(t, e, pub, lane, h, 2, "make a")
		fenceA, fenceX := lifecycleFence(0x71), lifecycleFence(0x72)
		// Two ends whose completions have not arrived: both parked.
		e.ws.BlockIntervalEnded(sid, fenceX, 0, big, false)
		e.ws.BlockIntervalEnded(sid, fenceA, 0, big, false)
		assertHeldWithin(t, e, sid, bound)
		mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(a), 0, fenceA)))
		assertSealedAs(t, db, a, gap)
	})
	t.Run("held behind a deferred append", func(t *testing.T) {
		for _, closing := range [][]emulator.Row{big, small} {
			e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
			sid := session.ID(sidStr)
			e.ws.SetBlockRowsBufferBytes(bound)
			e.ws.AttachBlockRows(sid)
			a := startsACommand(t, e, pub, lane, h, 2, "make a")
			fenceA := lifecycleFence(0x73)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(a), 0, fenceA)))
			e.ws.blockStream.mu.Lock()
			e.ws.blockStream.flushing[sid] = true
			e.ws.blockStream.pending[sid] = []pendingRows{{from: 0, rows: big}}
			e.ws.blockStream.mu.Unlock()
			e.ws.BlockIntervalEnded(sid, fenceA, 40, closing, false)
			assertHeldWithin(t, e, sid, bound)
			e.ws.blockStream.mu.Lock()
			pending := e.ws.blockStream.pending[sid]
			delete(e.ws.blockStream.pending, sid)
			e.ws.blockStream.flushing[sid] = false
			block := e.ws.blockStream.current[sid]
			e.ws.blockStream.mu.Unlock()
			e.ws.blockStream.flushPendingRows(e.ws, sid, block, pending, nil)
			if len(closing) == len(small) {
				assertSealedAs(t, db, a, nil)
			} else {
				assertSealedAs(t, db, a, gap)
			}
		}
	})
	t.Run("queued for a block not yet current", func(t *testing.T) {
		for _, closing := range [][]emulator.Row{big, small} {
			fenceFirst := lifecycleFence(0x74)
			e, sid, _, second := twoCommands(t, fenceFirst)
			db := e.ws.contentDB
			e.ws.blockStream.mu.Lock()
			e.ws.blockStream.budgets[sid] = bound
			e.ws.blockStream.beyond[sid] = []pendingRows{{from: 1, rows: big}}
			e.ws.blockStream.mu.Unlock()
			fenceSecond := lifecycleFence(0x75)
			e.ws.blockStream.publishFence(e.ws, sid, hex.EncodeToString(fenceSecond[:]), second)
			e.ws.BlockIntervalEnded(sid, fenceSecond, 2, closing, false)
			assertHeldWithin(t, e, sid, bound)
			e.ws.blockStream.mu.Lock()
			delete(e.ws.blockStream.beyond, sid)
			e.ws.blockStream.mu.Unlock()
			e.ws.BlockIntervalEnded(sid, fenceFirst, 1, nil, false)
			if len(closing) == len(small) {
				assertSealedAs(t, db, second, nil)
			} else {
				assertSealedAs(t, db, second, gap)
			}
		}
	})
}
