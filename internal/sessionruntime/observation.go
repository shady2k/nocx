package sessionruntime

import "github.com/shady2k/nocx/internal/emulator"

// The observation record (nocx-zg3k3.5.2; ADR-0072, design §6.3): ONE record
// per authenticated execution interval, owned by the runtime and built ON the
// cell model. A block in the transcript is one command — the interval between
// its authenticated start and its authenticated completion — and only the
// runtime was there for the whole of it: the screen as the interval opened,
// and the screen at the authenticated boundary. The rows the output pushed
// off the live rectangle while it ran are NOT here: since nocx-2v80t.3.6
// they leave through [Session.SetRowStream] the moment they leave the screen,
// because the owner's decision gives the helper no copy of them — ghostty's
// own scrollback is the buffer, and the record keeps boundaries, counts and
// the two screens only.
//
// The record shares the cell vocabulary and the revision identity with the
// live model and is NOT one object with it (ADR-0072): the live model answers
// which cells exist at revision R; the record answers what was observed
// during interval I, at its two ends. Its screens are [emulator.Row] — the
// same cells the frames carry — and it names no wire type of its own; what
// crosses the wire later is another task's (nocx-2v80t.3.4).
//
// A record's interval is a boundary-to-boundary span of the session's one
// output stream. The first interval opens at the session's first ingest; a
// sealed record opens the next; the authenticated completion is what closes
// one. Two commands whose intervals overlap (ADR-0024 decision 7 lets two
// meetings pend at once) share the span they interleave in, because one
// output stream cannot tell their bytes apart — the boundaries are what the
// runtime can honestly see.
//
// Nothing here is a wire shape. Opening and Closing are reads of the
// emulator's own rows at instants the runtime's lock makes one instant
// rather than a span.

// ObservationScreen is one instant of a screen: the whole grid as
// [emulator.Row] — one row per screen row, cells WITH style, spacers
// included — with the caret, the size it was read at, and which buffer was
// active. Lines is nil when the screen could not be read, the same honest
// silence Snapshot.Rows keeps: a screen nobody read is not a screen nobody
// may be told about.
type ObservationScreen struct {
	Geometry  Geometry
	AltScreen bool
	Cursor    emulator.Cursor
	Lines     []emulator.Row
}

// ObservationLoss is what the interval could not hand anyone. Each field
// is one CAUSE — the two the evidence names, not one boolean — and each is
// a COUNT, because "bounded" and "lost" are claims a reader checks against
// numbers:
//
//   - [ObservationLoss.RetentionFeeds] with RetentionFeedBytes: the
//     emulator's OWN retention pruned scrollback inside a feed, so rows that
//     left the live rectangle in that feed cannot be read (the port's
//     departed report answers with a hole: "the rows that left in that feed
//     cannot be read"). The rows themselves are unknowable — the ABI carries
//     no departure counter and pruning mixes with the feed's own
//     departures — so the honest count is the FEEDS struck and the bytes
//     those feeds carried, which is the most output the lost rows could have
//     been. Attribution is exact: the runtime drains the report after every
//     ingest, so a hole names the feed that produced it — and the stream
//     carries the same struck-feed marker on the row batch that follows the
//     hole (rowstream.go).
//   - [ObservationLoss.IngestLostBytes]: output lost BEFORE the emulator saw
//     it — [Session.ReportHole]'s count, exact in bytes, because bytes that
//     never reached the emulator never became rows to count.
//
// There is no eviction cause: the record holds no rows, so nothing is ever
// pushed out of one.
type ObservationLoss struct {
	RetentionFeeds     uint64
	RetentionFeedBytes uint64
	IngestLostBytes    uint64
}

// ObservationRecord is one authenticated execution interval: the screen the
// interval opened on and the screen at the authenticated boundary, with the
// losses the interval counted. The rows that departed in between streamed
// out as they left (rowstream.go); this record is the interval's boundaries,
// counts and screens — the helper's keyed evidence that the interval ran,
// not a copy of its output.
//
// While the interval is still running the record is what a reader can
// honestly claim so far — [ObservationRecord.Open] — and Closing names
// nothing.
//
// Opened is the revision the interval's opening screen was read at; Sealed is
// the revision the authenticated boundary closed it at — the same clock the
// frames carry, so the frames between Opened and Sealed are exactly what this
// interval observed. Nonce is the boundary's meeting; zero while the interval
// runs. Completeness is what the record may honestly claim, computed at the
// boundary and never defaulted: the session's own completeness, degraded —
// never upgraded — by the losses the record carries (a retention hole makes
// [CompletenessEvicted] of a claim that read [CompletenessComplete]).
type ObservationRecord struct {
	Nonce        FenceNonce
	At           Incarnation
	Opened       Revision
	Sealed       Revision
	Completeness Completeness
	Opening      ObservationScreen
	Closing      ObservationScreen
	Loss         ObservationLoss
}

// Open reports whether the interval the record observes is still running:
// no authenticated boundary has closed it, so Sealed names nothing and the
// record's content is the interval as it stands.
func (r ObservationRecord) Open() bool { return r.Sealed == 0 }

// observationOpen is the runtime's builder for the interval in flight — the
// record as it stands, without the boundary that would seal it. The rows
// that depart during it stream straight out; what accumulates here is the
// loss counts the stream's markers are folded into.
type observationOpen struct {
	Opened  Revision
	Opening ObservationScreen
	Loss    ObservationLoss
}

// takeObservationScreenLocked reads one instant of the emulator. It assumes
// the session lock, the way every locked screen read here does — the lock is
// what makes the several reads (geometry, buffer, caret, rows) ONE instant
// rather than a span a concurrent ingest could tear.
func (s *Session) takeObservationScreenLocked() (ObservationScreen, bool) {
	geom, err := s.emulator.Geometry()
	if err != nil {
		return ObservationScreen{}, false
	}
	alt, err := s.emulator.Screen()
	if err != nil {
		return ObservationScreen{}, false
	}
	cursor, err := s.emulator.Cursor()
	if err != nil {
		return ObservationScreen{}, false
	}
	screen := ObservationScreen{
		Geometry:  geom,
		AltScreen: alt == emulator.ScreenAlternate,
		Cursor:    cursor,
	}
	grid := make([]emulator.Row, geom.Rows)
	for y := range grid {
		row, err := s.emulator.Row(y)
		if err != nil {
			// A screen nobody could read whole is no snapshot: the caller
			// answers honest silence rather than a partial one.
			return ObservationScreen{}, false
		}
		grid[y] = row
	}
	screen.Lines = grid
	return screen, true
}

// openObservationLocked opens the interval in flight, at the first ingest of
// an interval — the earliest instant anything could have been drawn, BEFORE
// the feed that triggered it is applied, so the opening screen is the screen
// the interval really began on and carries none of its output. The opening
// read is best-effort in the same way every screen read here is: an emulator
// that cannot be read right now still leaves the interval a record, because
// a record is per interval and not per successful read.
func (s *Session) openObservationLocked() {
	if s.observation != nil {
		return
	}
	o := &observationOpen{Opened: s.rev}
	if scr, ok := s.takeObservationScreenLocked(); ok {
		o.Opening = scr
	}
	// A hole reported before the first ingest, or in the gap after one
	// sealed interval and before the next output, reached no record when it
	// was reported ([Session.ReportHole] could only add to a record in
	// flight). The bytes are the interval's nonetheless — they are output
	// this record's stream is short — so the record that opens now carries
	// everything the session has lost and no record has yet carried. The
	// session marks the amount carried, so the same bytes are never counted
	// into a second record.
	if gap := s.ingestLost - s.obsCarried; gap > 0 {
		o.Loss.IngestLostBytes = gap
		s.obsCarried = s.ingestLost
	}
	s.observation = o
}

// drainObservationLocked moves the emulator's departure report OUT, to the
// session's row stream (nocx-2v80t.3.6). It runs under the session lock
// after every ingest, before the effects that may carry the fence that seals
// the record — so a feed's own departures stream before the end marker of
// the interval that feed belongs to, and a boundary arriving in the same
// feed follows every row the feed departed.
//
// The runtime is the report's one reader ("a row is reported exactly once
// and by one reader"): draining at every ingest is what keeps the emulator's
// own accumulation empty. The rows handed out are the caller's — freshly
// copied by the port — and nothing of them is kept here; with no stream
// bound they are read and released, because the report must be emptied
// either way and ghostty's scrollback is the buffer the owner's decision
// names.
//
// A holed report is both a loss on the record and a marker on the stream:
// the feeds struck and the bytes they carried are counted here, and the row
// batch that follows the hole carries lost=1 (rowstream.go). A feed that
// departed nothing but was struck still carries its marker, so a hole at the
// very end of an interval is never silently dropped.
func (s *Session) drainObservationLocked(feedBytes int) {
	if s.observation == nil {
		// No interval is in flight; nothing may claim the report's rows, so
		// they stay where they are for the interval that opens next.
		return
	}
	rows, err := s.emulator.DepartedRows()
	lost := uint64(0)
	if err != nil {
		s.observation.Loss.RetentionFeeds++
		s.observation.Loss.RetentionFeedBytes += uint64(feedBytes) // #nosec G115 -- a feed is len(bytes), never negative
		lost = 1
	}
	if len(rows) == 0 && lost == 0 {
		return
	}
	// The interval sealed last left a screen behind, and its rows leave the
	// screen during the command that follows — reported as departures, at
	// their own pace. The block holds them as its closing screen already, so
	// they are not streamed again: a reported row that still equals the head
	// of what the boundary left is that row leaving, and everything after the
	// first row that is not one of them is the next command's own output.
	// The window is cleared at the first row that is not one of them, so a
	// cleared or rewritten screen cannot suppress the next command's rows
	// (nocx-2v80t.3.9). A struck feed suppresses nothing: the hole it names
	// rides the batch that follows it, and a batch withheld whole would
	// swallow that marker.
	if lost == 0 && len(s.pendingScreen) > 0 {
		for len(rows) > 0 && len(s.pendingScreen) > 0 && sameScreenRow(rows[0], s.pendingScreen[0]) {
			rows = rows[1:]
			s.pendingScreen = s.pendingScreen[1:]
			s.suppressedScreenRows++
		}
	}
	if len(rows) == 0 && lost == 0 {
		// A struck feed with an empty report still carries its marker: a
		// batch withheld whole would swallow the hole it names.
		return
	}
	s.pendingScreen = nil
	from := s.departedRows
	s.departedRows += uint64(len(rows)) // #nosec G115 -- len is never negative
	if rs := s.rowStream; rs != nil {
		rs.OutputRows(from, rows, lost)
	}
}

// observationCompleteness folds a record's losses into the claim it may
// honestly make. It starts from the session's own completeness — the claim
// about the stream the runtime has established — and only ever goes DOWN:
// the emulator's pruning turns a complete claim into
// [CompletenessEvicted], the enum's name for "retention deliberately kept
// less than the whole", and nothing here ever raises one or manufactures
// specificity.
func observationCompleteness(session Completeness, loss ObservationLoss) Completeness {
	c := session
	if loss.RetentionFeeds > 0 {
		// Only a COMPLETE claim degrades to Evicted. LostIngest and NoFence
		// are already below it and say so; Unknown says nothing about the
		// stream, and a loss does not make it say more — an eviction is a
		// more specific claim than an unknown, and specificity is never
		// manufactured here.
		if c == CompletenessComplete {
			c = CompletenessEvicted
		}
	}
	return c
}

// sealObservationLocked closes the interval at an authenticated boundary: the
// meeting whose two halves JUST joined. The closing screen is read under the
// same lock the join holds, so the record's two ends are instants. The end
// marker follows every row the interval streamed, carrying the screen at the
// boundary; the record that closes opens the next one on the screen it
// closed on — what the boundary holds is exactly where the next record's
// story starts — and a command that produced no output at all still leaves a
// record: the interval ran, the boundary arrived, and the record opens and
// closes on the same screen with nothing departed.
func (s *Session) sealObservationLocked(nonce FenceNonce) {
	o := s.observation
	if o == nil {
		o = &observationOpen{Opened: s.rev}
		if scr, ok := s.takeObservationScreenLocked(); ok {
			o.Opening = scr
		}
	}
	rec := ObservationRecord{
		Nonce:        nonce,
		At:           s.inc,
		Opened:       o.Opened,
		Sealed:       s.rev,
		Completeness: observationCompleteness(s.completeness, o.Loss),
		Opening:      o.Opening,
		Closing:      ObservationScreen{},
		Loss:         o.Loss,
	}
	if closing, ok := s.takeObservationScreenLocked(); ok {
		rec.Closing = closing
	}
	s.pendingScreen = cloneObservationRows(boundaryRowsThatLeave(rec.Closing))
	s.emitIntervalEndLocked(nonce, s.departedRows, boundaryRowsThatLeave(rec.Closing))
	// The next interval opens on the boundary screen, at the boundary
	// revision.
	s.observation = &observationOpen{Opened: rec.Sealed, Opening: rec.Closing}
	s.storeSealedObservationLocked(rec)
}

// sameScreenRow is whether two rows are the same row of the screen: every
// cell's grapheme, footprint and hasText, and the row's own wrap flags. The
// styles are not part of it on purpose: a re-departing screen row comes back
// out of the scrollback with the cells it had, and a strict style comparison
// would miss it on a theme change, while a false match costs at most one
// suppressed row — a row that arrived at the position the boundary's screen
// occupies, which is the ambiguity the block's closing screen already
// absorbs (nocx-2v80t.3.9).
func sameScreenRow(a, b emulator.Row) bool {
	if a.Wrap != b.Wrap || a.Continuation != b.Continuation || len(a.Cells) != len(b.Cells) {
		return false
	}
	for i := range a.Cells {
		if a.Cells[i] != b.Cells[i] {
			return false
		}
	}
	return true
}

// SuppressedScreenRows is how many rows the runtime declined to stream a
// second time because they were the interval before's closing screen leaving
// it again — rows a block already holds. A count, so "nothing left the
// screen" and "rows were suppressed" are never confused.
func (s *Session) SuppressedScreenRows() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suppressedScreenRows
}

// boundaryRowsThatLeave is the rows of a boundary screen that are going to
// leave it: the rows at and above the cursor, in order. Everything below the
// cursor is written over in place by the next command's output and never
// departs, so a consumer that took the whole screen for the interval's tail
// would claim absolute rows no departure ever fills — and eat that command's
// own first rows instead (nocx-2v80t.3.9). A screen the alternate buffer owns
// leaves nothing at all: a full-screen program's rows never enter the
// scrollback, which is the same reason nothing departs while it holds the
// pane.
//
// The record keeps the WHOLE screen — this trims what the end marker carries,
// because only that is a claim about rows that are going to leave — and the
// two are read at one instant with the interval's departure count
// (TestTheClosingScreenCarriesTheRowsThatLeaveNext).
func boundaryRowsThatLeave(scr ObservationScreen) []emulator.Row {
	if scr.AltScreen || len(scr.Lines) == 0 {
		return nil
	}
	end := scr.Cursor.Y + 1
	if end <= 0 || end > len(scr.Lines) {
		end = len(scr.Lines)
	}
	return scr.Lines[:end]
}

// emitIntervalEndLocked hands the row stream one interval's end marker: the
// nonce, the absolute row index one past the interval's last departed row,
// and the closing screen's rows. It is called with the session lock held,
// after the rows — the emission order is the stream's order (rowstream.go).
func (s *Session) emitIntervalEndLocked(nonce FenceNonce, endRow uint64, closing []emulator.Row) {
	if rs := s.rowStream; rs != nil {
		rs.IntervalEnd(nonce, endRow, closing)
	}
}

// storeSealedObservationLocked appends one sealed record to the session's
// store: bounded by [MaxObservations], the oldest going first, counted.
// A record holds boundaries, counts and two screens — no rows — so the
// bound is the record of a session's intervals at a cost somebody can check,
// the way every bound in this file is a number rather than a policy
// statement.
func (s *Session) storeSealedObservationLocked(rec ObservationRecord) {
	s.observations = append(s.observations, rec)
	if excess := len(s.observations) - MaxObservations; excess > 0 {
		s.observationsEvicted += uint64(excess)
		kept := make([]ObservationRecord, len(s.observations)-excess)
		copy(kept, s.observations[excess:])
		s.observations = kept
	}
}

// observationCapture is what a parking sighting took at the fence's instant:
// the interval's boundaries up to the fence, the losses it had counted, the
// absolute row index one past its last departed row, and the screen as the
// fence sat on it. The boundary is where the fence sits in the byte stream,
// so the [Session.Completed] that joins the sighting seals THIS — the screen
// as it was at the fence — and never a fresh read of a screen the stream has
// since moved past. The rows up to the fence are not here either: they
// streamed when they left; EndRow is what tells the stream where they stop.
type observationCapture struct {
	Opened       Revision
	SightRev     Revision
	EndRow       uint64
	Opening      ObservationScreen
	Loss         ObservationLoss
	Closing      ObservationScreen
	Completeness Completeness
}

// splitObservationAtFenceLocked takes the boundary capture at a parking
// sighting and rebases the interval in flight to start here: the capture
// carries the record's boundaries up to the fence, its losses, the row index
// it had departed to, and the screen at it; the record in flight keeps
// collecting the output that FOLLOWS the fence, as the next record, opened
// at the fence's revision on the fence's screen. The fence itself is painted
// by nothing, so the closing screen ends where the command's visible output
// ended.
func (s *Session) splitObservationAtFenceLocked() *observationCapture {
	o := s.observation
	if o == nil {
		o = &observationOpen{Opened: s.rev}
	}
	cap := &observationCapture{
		Opened:       o.Opened,
		SightRev:     s.rev,
		EndRow:       s.departedRows,
		Opening:      o.Opening,
		Loss:         o.Loss,
		Completeness: s.completeness,
	}
	if scr, ok := s.takeObservationScreenLocked(); ok {
		cap.Closing = scr
	}
	s.observation = &observationOpen{Opened: s.rev, Opening: cap.Closing}
	return cap
}

// sealObservationFromCaptureLocked seals the record a parking sighting
// captured: the content was taken AT the fence, so Sealed is the sighting's
// revision and nothing is re-read, and the end marker stops the interval at
// the row index the sighting measured — the rows that streamed while the
// authenticated half was on its way belong to the interval that follows.
// The interval in flight already IS the next record — the split rebased it
// at the fence — so this seals the capture and stores it, and touches
// nothing else.
func (s *Session) sealObservationFromCaptureLocked(nonce FenceNonce, cap *observationCapture) {
	rec := ObservationRecord{
		Nonce:        nonce,
		At:           s.inc,
		Opened:       cap.Opened,
		Sealed:       cap.SightRev,
		Completeness: observationCompleteness(cap.Completeness, cap.Loss),
		Opening:      cap.Opening,
		Closing:      cap.Closing,
		Loss:         cap.Loss,
	}
	s.pendingScreen = cloneObservationRows(boundaryRowsThatLeave(cap.Closing))
	s.emitIntervalEndLocked(nonce, cap.EndRow, boundaryRowsThatLeave(cap.Closing))
	s.storeSealedObservationLocked(rec)
}

// returnObservationCaptureLocked hands a capture nobody authenticated back
// to the interval in flight. An expired sighting, or one evicted at the
// bound, was never a boundary — the output it fenced is nobody's but the
// session's own stream — so the split un-does itself: the record in flight
// resumes its ORIGINAL opening and the losses summed. The rows need no
// un-doing: they streamed when they left and their indices never moved, and
// the interval's end marker will stop at whatever the session has departed
// to when that boundary finally arrives.
func (s *Session) returnObservationCaptureLocked(cap *observationCapture) {
	o := s.observation
	if o == nil {
		s.observation = &observationOpen{
			Opened:  cap.Opened,
			Opening: cap.Opening,
			Loss:    cap.Loss,
		}
		return
	}
	o.Loss.RetentionFeeds += cap.Loss.RetentionFeeds
	o.Loss.RetentionFeedBytes += cap.Loss.RetentionFeedBytes
	o.Loss.IngestLostBytes += cap.Loss.IngestLostBytes
	o.Opened, o.Opening = cap.Opened, cap.Opening
}

// Observations is every sealed record the session retains, oldest first. The
// store is bounded by [MaxObservations]; records beyond it were evicted
// oldest first and [Session.ObservationsEvicted] counts them — a record that
// is gone is never described as present. What a read hands out is a COPY:
// the runtime's record is evidence, and a caller that wrote through a
// returned row would be editing it.
func (s *Session) Observations() []ObservationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ObservationRecord, len(s.observations))
	for i, r := range s.observations {
		out[i] = cloneObservationRecord(r)
	}
	return out
}

// ObservationsEvicted is how many sealed records the session's store has
// dropped to keep to [MaxObservations], oldest first. A count, because a
// vanished record must not look like a command that never ran.
func (s *Session) ObservationsEvicted() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observationsEvicted
}

// ObservationFor answers the record sealed by the meeting the nonce names, or
// false when no record for it exists — an expired boundary sealed nothing,
// and a nonce the session never authenticated names nothing. It is the keyed
// read: a caller that saw the completion names its nonce.
func (s *Session) ObservationFor(nonce FenceNonce) (ObservationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.observations) - 1; i >= 0; i-- {
		if s.observations[i].Nonce == nonce {
			return cloneObservationRecord(s.observations[i]), true
		}
	}
	return ObservationRecord{}, false
}

// OpenObservation answers the interval still running — its boundaries, its
// losses and the screen NOW as its so-far Closing — or false when no
// interval is in flight. It changes nothing: the read takes the same lock
// every read takes, and what it hands out is a copy.
func (s *Session) OpenObservation() (ObservationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.observation
	if o == nil {
		return ObservationRecord{}, false
	}
	rec := ObservationRecord{
		At:      s.inc,
		Opened:  o.Opened,
		Opening: o.Opening,
		Loss:    o.Loss,
		Closing: ObservationScreen{},
	}
	if scr, ok := s.takeObservationScreenLocked(); ok {
		rec.Closing = scr
	}
	rec.Completeness = observationCompleteness(s.completeness, o.Loss)
	return cloneObservationRecord(rec), true
}

// cloneObservationRecord copies a record for handing out. The rows are the
// runtime's evidence: a caller that wrote through a returned slice would be
// editing what the record observed, which is the one thing the copy exists
// to prevent. Graphemes are strings and share fine; the slices do not.
func cloneObservationRecord(r ObservationRecord) ObservationRecord {
	r.Opening.Lines = cloneObservationRows(r.Opening.Lines)
	r.Closing.Lines = cloneObservationRows(r.Closing.Lines)
	return r
}

func cloneObservationRows(rows []emulator.Row) []emulator.Row {
	if rows == nil {
		return nil
	}
	out := make([]emulator.Row, len(rows))
	for i, r := range rows {
		out[i] = r
		if r.Cells != nil {
			cells := make([]emulator.Cell, len(r.Cells))
			copy(cells, r.Cells)
			out[i].Cells = cells
		}
	}
	return out
}
