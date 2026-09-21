package sessionruntime

// The capture record (nocx-2v80t.2.2; design §6.3 names it beside the card's
// wire format as a different thing): ONE record per settled execution
// interval, built by the runtime at the authenticated render boundary — the
// instant a rendezvous completes — because only the runtime was there for the
// whole of it. A caller that reconstructed the interval after the fact would
// be guessing at the rows that left the screen while the command ran, and
// every one of those guesses would be wrong exactly when the output did not
// fit the screen.
//
// The record is the runtime's own fact, shaped by what the runtime holds:
//
//   - Opening — the screen as the interval opened (the session's first byte
//     opens the first interval; each settled interval opens the next).
//   - Departed — the rows that LEFT the screen during the interval, oldest
//     first, exactly as the emulator's departure report carries them: a
//     soft-wrapped line stays joinable, a hard newline stays a break, and
//     each row was copied at the instant it left, so nothing that happened
//     afterwards could rewrite it.
//   - Closing — the screen at the authenticated boundary: the content the
//     artifact stores against the command's entry.
//   - Completeness — [Session.Completeness]'s own answer at the close, never
//     a default: a record whose stream lost bytes before the emulator saw
//     them says so rather than looking whole.
//
// Deliberately not here, unchanged: a rendezvous whose bounded wait EXPIRED
// produces no settled record. CompletenessNoFence marks an interval with no
// authenticated boundary, and only the authenticated boundary closes one —
// an interval that ran out to its expiry reads back as the unfinished
// record its first departure stored, never as a finished one
// (nocx-2v80t.2.4).

import (
	"github.com/shady2k/nocx/internal/emulator"
)

// CaptureState says which kind of interval a record is: a settled execution
// interval the authenticated boundary closed, or the open interval of a
// command still running (nocx-2v80t.2.4). The zero value is CaptureSettled
// because that is the shape every producer set before the unfinished one
// existed; both build sites name their state explicitly, and a reader may
// never have to guess which kind it is holding.
type CaptureState string

const (
	// CaptureSettled — the authenticated render boundary closed the
	// interval; the record carries its nonce and its closing screen.
	CaptureSettled CaptureState = "settled"
	// CaptureUnfinished — the command still runs; the record is the open
	// interval's known-so-far, taken once at its first departure. It
	// names no nonce and closes nothing.
	CaptureUnfinished CaptureState = "unfinished"
)

// CaptureSink receives one capture record per settled execution interval.
//
// Capture is called by the runtime AFTER the settle's critical section is
// over — the runtime never holds its lock across a sink, and the sink it
// hands records to must not block on the way in: queue and deliver
// elsewhere. A nil sink is a real configuration, not a missing one: a
// runtime whose composition root wired nobody to receive records settles
// exactly as a runtime with one does, and produces nothing on the way out.
type CaptureSink interface {
	Capture(CaptureRecord)
}

// CaptureScreen is one instant of a screen: the whole grid WITH style, the
// caret, and which buffer was active. The rows are a rectangle — Cells is one
// entry per column, spacers included — because a consumer that restores the
// record draws positions, not just text.
type CaptureScreen struct {
	Cols, Rows int
	AltScreen  bool
	Cursor     emulator.Cursor
	// Lines is the grid's CONTENT, one emulator.Row per screen row. The
	// size and the contents are two fields for the same reason ScreenFrame
	// names its own that way: a consumer that indexes a position reads the
	// rectangle, not the text.
	Lines []emulator.Row
}

// CaptureRecord is one execution interval's record. State says which kind
// of interval it is — a record a reader cannot tell apart from a finished
// one is the defect the unfinished state exists to prevent; both build
// sites name their state explicitly, never a default. Nonce names the
// meeting whose completion closed a settled interval — the zero fence is a
// REAL answer on an unfinished record, where no authenticated boundary has
// closed anything; At and Revision pin the record to the incarnation and
// the clock it was taken at.
type CaptureRecord struct {
	State        CaptureState
	Nonce        FenceNonce
	At           Incarnation
	Revision     Revision
	Completeness Completeness

	// Opening is the screen as the interval opened, Departed the rows the
	// interval pushed off it in order, Closing the screen at the boundary.
	Opening  CaptureScreen
	Departed []emulator.Row
	// DepartedHole is the departure report's own retention flag: true means
	// the emulator could not read some of what left, so the list holds what
	// was read and what was not is unknowable. A record that dropped this
	// flag would present a holed interval as whole.
	DepartedHole bool
	Closing      CaptureScreen
}

// takeCaptureScreenLocked reads one instant of the emulator. It assumes the
// session lock, the way every locked screen read here does — the lock is
// what makes the several reads (geometry, buffer, caret, rows) ONE instant
// rather than a span a concurrent ingest could tear.
func (s *Session) takeCaptureScreenLocked() (CaptureScreen, bool) {
	geom, err := s.emulator.Geometry()
	if err != nil {
		return CaptureScreen{}, false
	}
	alt, err := s.emulator.Screen()
	if err != nil {
		return CaptureScreen{}, false
	}
	cursor, err := s.emulator.Cursor()
	if err != nil {
		return CaptureScreen{}, false
	}
	screen := CaptureScreen{
		Cols:      geom.Cols,
		Rows:      geom.Rows,
		AltScreen: alt == emulator.ScreenAlternate,
		Cursor:    cursor,
	}
	grid := make([]emulator.Row, geom.Rows)
	for y := range grid {
		row, err := s.emulator.Row(y)
		if err != nil {
			// A screen nobody could read whole is no snapshot: the caller
			// answers honest silence rather than a partial one.
			return CaptureScreen{}, false
		}
		grid[y] = row
	}
	screen.Lines = grid
	return screen, true
}

// openCaptureIntervalLocked takes the next interval's opening snapshot. It
// runs at the session's first ingest — the earliest instant anything could
// have been drawn — and again whenever a settle hands the previous interval
// over, so an opening is always the screen the interval really began on.
func (s *Session) openCaptureIntervalLocked() {
	if s.captureOpeningValid {
		return
	}
	if screen, ok := s.takeCaptureScreenLocked(); ok {
		s.captureOpening = screen
		s.captureOpeningValid = true
	}
}

// settleCaptureLocked builds the record for a meeting that JUST completed —
// the transition is the caller's; this is the boundary read. A closing read
// that fails produces no record: the emulator was unreadable at the very
// boundary the record exists to pin, and a record of a screen nobody read
// would be a claim about a screen nobody saw.
func (s *Session) settleCaptureLocked(nonce FenceNonce) {
	closing, ok := s.takeCaptureScreenLocked()
	if !ok {
		return
	}
	departed, err := s.emulator.DepartedRows()
	rec := CaptureRecord{
		State:        CaptureSettled,
		Nonce:        nonce,
		At:           s.inc,
		Revision:     s.rev,
		Completeness: s.completeness,
		Opening:      s.captureOpening,
		Departed:     departed,
		DepartedHole: err != nil,
		Closing:      closing,
	}
	if err != nil {
		// The departure report flagged this very interval: some of what
		// left the screen could not be read, so the record's rows are not
		// the whole of what departed even when the stream itself was
		// clean. A record that kept the stream's "complete" beside a
		// holed row list would present the interval as whole — the one
		// lie this field exists to prevent. The stream's own claim can
		// only be worse, never better; an interval already lost-ingest
		// stays lost-ingest.
		rec.Completeness = CompletenessLostIngest
	}
	s.capturePending = append(s.capturePending, rec)
	// The interval that just closed opened the next one: what the boundary
	// screen holds is exactly where the next record's story starts, and
	// the new interval has made no record of its own yet.
	s.captureOpening, s.captureOpeningValid = closing, true
	s.captureUnfinishedSent = false
}

// snapshotUnfinishedLocked takes the open interval's record — the one fact
// a long unfinished command leaves behind while it still runs
// (nocx-2v80t.2.4): the opening, and a PEEK of the departure report, the
// rows the command has pushed off the screen so far. The peek spends
// nothing: the settle-time drain remains the report's one owner, and the
// record the authenticated boundary later builds still covers the whole
// interval. One record per interval, at the first departure that carries
// rows — a later byte produces no second ask, and only a boundary closes
// the interval, so only a boundary can produce the next record. A reader
// that cannot tell this record from a finished one is reading a defect:
// State names it unfinished, the nonce is the zero fence, and no closing
// screen exists. It appends to the pending list for the caller to hand out
// with [Session.takePendingCapturesLocked], exactly as the settle does.
func (s *Session) snapshotUnfinishedLocked() {
	if !s.captureOpeningValid || s.captureUnfinishedSent {
		return
	}
	departed, err := s.emulator.PeekDepartedRows()
	if len(departed) == 0 {
		return
	}
	rec := CaptureRecord{
		State:        CaptureUnfinished,
		At:           s.inc,
		Revision:     s.rev,
		Completeness: CompletenessNoFence,
		Opening:      s.captureOpening,
		Departed:     departed,
		DepartedHole: err != nil,
	}
	if s.completeness == CompletenessLostIngest || s.completeness == CompletenessEvicted {
		// The stream's own claim can only be worse than the missing
		// boundary, never better — the same rule the settle applies.
		rec.Completeness = s.completeness
	}
	s.capturePending = append(s.capturePending, rec)
	s.captureUnfinishedSent = true
}

// takePendingCapturesLocked hands the built records out and clears the
// pending list. Called under the session lock, before the caller releases it.
func (s *Session) takePendingCapturesLocked() []CaptureRecord {
	if len(s.capturePending) == 0 {
		return nil
	}
	pending := s.capturePending
	s.capturePending = nil
	return pending
}

// flushCaptures hands settled records to the sink. It runs OUTSIDE the
// session lock, every time, and a nil sink means nobody asked for records —
// the records simply die here rather than reaching nobody.
func (s *Session) flushCaptures(pending []CaptureRecord) {
	if s.captures == nil {
		return
	}
	for _, rec := range pending {
		s.captures.Capture(rec)
	}
}
