package sessionruntime

import "github.com/shady2k/nocx/internal/emulator"

// The row stream (nocx-2v80t.3.6): how a command's output leaves the helper
// as it leaves the screen. The owner's decision of 2026-09-23 removes the
// helper's copy of departed rows — the observation record keeps boundaries,
// counts and the closing screen, and ghostty's own scrollback is the buffer
// — so the runtime hands each row exactly once, in order, to this stream the
// moment its feed is drained, and an interval's end marker follows that
// interval's last row on the SAME stream. One ordered stream is the whole
// attribution story: a row belongs to the interval whose end marker follows
// it, so a row can never be attributed to the wrong command, and the end
// marker's endRow fixes the boundary even when the next interval's rows
// stream while the authenticated half is still on its way (the fence split).
//
// FromRow is the absolute index of Rows[0]: the count of rows this SESSION
// has seen depart before this batch, whether or not any consumer was bound
// when they did. Indices are the session's, never the consumer's, so a
// consumer bound late continues where the session left off and the
// coordinator's acknowledgement (the confirmed-written mark) names a
// position both ends can check.
//
// LostRows counts the FEEDS the emulator struck, never rows: the ABI carries
// no departure counter, and a prune inside a feed mixes with that feed's own
// departures in one depth reading, so the rows a prune took are unknowable
// by contract (emulator.Terminal.DepartedRows). One struck feed reads as
// lost=1 immediately before FromRow; an ordinary feed carries none. The
// exact-row spelling of this field would be a manufactured count, and no
// count is manufactured here.
type RowStream interface {
	// OutputRows carries one feed's departed rows, oldest first, with the
	// absolute index of the first and the struck-feed count immediately
	// before it.
	OutputRows(from uint64, rows []emulator.Row, lost uint64)
	// IntervalEnd closes the interval the nonce names, AFTER every row that
	// belongs to it: endRow is the absolute index one past the interval's
	// last departed row, and closing is the screen as the boundary sat on
	// it (nil when the screen could not be read — the same honest silence
	// every screen read keeps).
	IntervalEnd(nonce FenceNonce, endRow uint64, closing []emulator.Row)
}

// SetRowStream binds the session's row stream, exactly as [SetReplies] binds
// the reply sink and for the same shape: internal/helper/session's spawn
// builds the runtime before the host session that bridges the stream to the
// wire exists, and binds a moment later — before the read loop that would
// produce the first ingest. A nil stream is ordinary: the rows are still
// read off the emulator's report (the report is the runtime's to empty) and
// their indices still spent, but nobody receives them — the scrollback is
// the buffer, and the resend that reads it is the epic's next step.
func (s *Session) SetRowStream(r RowStream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rowStream = r
}
