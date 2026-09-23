package sessionruntime

import "github.com/shady2k/nocx/internal/emulator"

// The observation record (nocx-zg3k3.5.2; ADR-0072, design §6.3): ONE record
// per authenticated execution interval, owned by the runtime and built ON the
// cell model. A card in the transcript is one command — the interval between
// its authenticated start and its authenticated completion — and only the
// runtime was there for the whole of it: the screen as the interval opened,
// the rows the output pushed off the live rectangle while it ran, the screen
// at the authenticated boundary. A caller that reconstructed the interval
// after the fact would be guessing at exactly the rows that were no longer
// visible, and every one of those guesses would be wrong when the output did
// not fit the screen.
//
// The record shares the cell vocabulary and the revision identity with the
// live model and is NOT one object with it (ADR-0072): the live model answers
// which cells exist at revision R; the record answers what was observed
// during interval I, including rows no longer in the live rectangle. Its rows
// are [emulator.Row] — the same cells the frames carry — and it names no
// wire type of its own; what crosses the wire later is another task's
// (nocx-2v80t.3.4), and nothing reads these records over any transport yet.
//
// A record's interval is a boundary-to-boundary span of the session's one
// output stream. The first interval opens at the session's first ingest; a
// sealed record opens the next; the authenticated completion is what closes
// one. Two commands whose intervals overlap (ADR-0024 decision 7 lets two
// meetings pend at once) share the span they interleave in, because one
// output stream cannot tell their bytes apart — the boundaries are what the
// runtime can honestly see.
//
// Nothing here is a wire shape. Opening, Closing and Departed are reads of
// the emulator's own rows at instants the runtime's lock makes one instant
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

// ObservationLoss is what the interval could not hand the record. Each field
// is one CAUSE — the three the evidence names, not one boolean — and each is
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
//     ingest, so a hole names the feed that produced it.
//   - [ObservationLoss.IngestLostBytes]: output lost BEFORE the emulator saw
//     it — [Session.ReportHole]'s count, exact in bytes, because bytes that
//     never reached the emulator never became rows to count.
//   - [ObservationLoss.EvictedRows]: rows the RECORD's own bound pushed out,
//     oldest first, exactly as many as were dropped, because the record held
//     them and counted what it let go.
type ObservationLoss struct {
	RetentionFeeds     uint64
	RetentionFeedBytes uint64
	IngestLostBytes    uint64
	EvictedRows        uint64
}

// ObservationRecord is one authenticated execution interval: the screen the
// interval opened on, the rows that left the live rectangle while it ran,
// oldest first, and the screen at the authenticated boundary. While the
// interval is still running the record is what a reader can honestly claim so
// far — [ObservationRecord.Open] — and Closing names nothing.
//
// Opened is the revision the interval's opening screen was read at; Sealed is
// the revision the authenticated boundary closed it at — the same clock the
// frames carry, so the frames between Opened and Sealed are exactly what this
// interval observed. Nonce is the boundary's meeting; zero while the interval
// runs. Completeness is what the record may honestly claim, computed at the
// boundary and never defaulted: the session's own completeness, degraded —
// never upgraded — by the losses the record carries (retention holes and
// evictions make [CompletenessEvicted] of a claim that read
// [CompletenessComplete]).
type ObservationRecord struct {
	Nonce        FenceNonce
	At           Incarnation
	Opened       Revision
	Sealed       Revision
	Completeness Completeness
	Opening      ObservationScreen
	Departed     []emulator.Row
	Closing      ObservationScreen
	Loss         ObservationLoss
}

// Open reports whether the interval the record observes is still running:
// no authenticated boundary has closed it, so Sealed names nothing and the
// record's content is the interval as it stands.
func (r ObservationRecord) Open() bool { return r.Sealed == 0 }

// observationOpen is the runtime's builder for the interval in flight — the
// record as it stands, without the boundary that would seal it. It exists so
// the runtime can drain departures into something at every ingest and hand a
// so-far read out without manufacturing a boundary.
type observationOpen struct {
	Opened   Revision
	Opening  ObservationScreen
	Departed []emulator.Row
	Loss     ObservationLoss
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
	s.observation = o
}

// drainObservationLocked moves the emulator's departure report into the open
// record. It runs under the session lock after every ingest, before the
// effects that may carry the fence that seals the record — so a feed's own
// departures belong to the interval that feed belongs to, and a boundary
// arriving in the same feed seals a record that already holds them.
//
// The runtime is the report's one reader ("a row is reported exactly once
// and by one reader"): draining at every ingest is what keeps the
// emulator's own accumulation empty and the record the place the interval's
// rows live, bounded where the record's bound can count them.
//
// A holed report is recorded as the loss it is, attributed to THIS feed:
// the rows the emulator could not read are gone, the feeds struck and the
// bytes they carried are the count the record can honestly state.
func (s *Session) drainObservationLocked(feedBytes int) {
	if s.observation == nil {
		// No interval is in flight; nothing may claim the report's rows, so
		// they stay where they are for the interval that opens next.
		return
	}
	rows, err := s.emulator.DepartedRows()
	if err != nil {
		s.observation.Loss.RetentionFeeds++
		s.observation.Loss.RetentionFeedBytes += uint64(feedBytes) // #nosec G115 -- a feed is len(bytes), never negative
	}
	if len(rows) == 0 {
		return
	}
	o := s.observation
	o.Departed = append(o.Departed, rows...)
	// The record's own bound: past [MaxObservationRows] the OLDEST rows go,
	// counted, because the newest are the ones a reader of a running or a
	// finished command still wants and a bound that kept nothing would keep
	// nothing honestly either.
	if excess := len(o.Departed) - MaxObservationRows; excess > 0 {
		o.Loss.EvictedRows += uint64(excess)
		kept := copy(o.Departed, o.Departed[excess:])
		o.Departed = o.Departed[:kept]
	}
}

// observationCompleteness folds a record's losses into the claim it may
// honestly make. It starts from the session's own completeness — the claim
// about the stream the runtime has established — and only ever goes DOWN:
// the two retention causes (the emulator's pruning, the record's own bound)
// turn a complete claim into [CompletenessEvicted], the enum's name for
// "retention deliberately kept less than the whole", and nothing here ever
// raises one or manufactures specificity.
func observationCompleteness(session Completeness, loss ObservationLoss) Completeness {
	c := session
	if loss.RetentionFeeds > 0 || loss.EvictedRows > 0 {
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
// same lock the join holds, so the record's two ends are instants and its
// departed rows are everything the interval pushed off in between. The record
// that closes opens the next one on the screen it closed on — what the
// boundary holds is exactly where the next record's story starts — and a
// command that produced no output at all still leaves a record: the interval
// ran, the boundary arrived, and the record opens and closes on the same
// screen with nothing departed.
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
		Departed:     o.Departed,
		Closing:      ObservationScreen{},
		Loss:         o.Loss,
	}
	if closing, ok := s.takeObservationScreenLocked(); ok {
		rec.Closing = closing
	}
	// The next interval opens on the boundary screen, at the boundary
	// revision.
	s.observation = &observationOpen{Opened: rec.Sealed, Opening: rec.Closing}
	s.observations = append(s.observations, rec)
	// The session's store of sealed records is bounded ([MaxObservations]):
	// the oldest go first, counted, exactly as the rows inside one record do.
	if excess := len(s.observations) - MaxObservations; excess > 0 {
		s.observationsEvicted += uint64(excess)
		kept := make([]ObservationRecord, len(s.observations)-excess)
		copy(kept, s.observations[excess:])
		s.observations = kept
	}
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

// OpenObservation answers the interval still running — the record as it
// stands, its departures drained so far and the screen NOW as its so-far
// Closing — or false when no interval is in flight. It changes nothing: the
// read takes the same lock every read takes, and what it hands out is a copy.
func (s *Session) OpenObservation() (ObservationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.observation
	if o == nil {
		return ObservationRecord{}, false
	}
	rec := ObservationRecord{
		At:       s.inc,
		Opened:   o.Opened,
		Opening:  o.Opening,
		Departed: o.Departed,
		Loss:     o.Loss,
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
	r.Departed = cloneObservationRows(r.Departed)
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
