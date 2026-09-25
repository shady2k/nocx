package sessionruntime

import (
	"strings"

	"github.com/shady2k/nocx/internal/emulator"
)

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
	// EndRow is the departure count an interval parked at its completion
	// carried: the boundary of its own streamed rows. Only a parked interval
	// sets it; the end marker that finally settles it carries exactly this
	// count and no closing screen (the screen is never read at the
	// completion).
	EndRow uint64
	// Nonce is set only on an interval that authenticated COMPLETE but whose
	// fence's sighting has not arrived: the screen is NOT read at the
	// completion (the next command's rows may already be printed), so its
	// seal waits for the sighting that carries the boundary. Settled by an
	// event, never a timer: the next interval's start or the session's end
	// seals it without a closing screen (sealPendingWithoutScreenLocked).
	Nonce FenceNonce
	// Rebased names the fence whose SIGHTING opened this interval in flight —
	// splitObservationAtFenceLocked took its screen and its row window at that
	// fence, so this interval belongs to that fence's command. A completion
	// for any OTHER nonce is older than it, and parking it here would give one
	// record two overlapping row spans and emit an end marker behind the
	// stream (nocx-2v80t.3.9). Zero when the interval opened any other way: at
	// a seal, or at the session's first ingest.
	Rebased FenceNonce
	// OutputMarked is whether this interval's FIRST OSC 133 C has already
	// been sighted (nocx-2v80t.3.12; sightOutputMarkLocked). Everything
	// still on screen at that sighting — the prompt this interval opened
	// with, and (for an app-submitted command) the echoed command line the
	// bytes that follow it draw before the shell even starts running it —
	// is this interval's own PREFIX, never its output, and gets installed
	// as the suppression window a second time, from the mark's own screen
	// rather than the last fence's. A second and later C within the same
	// interval is ordinary output and does nothing: only the first ever
	// arms this, and it never rearms. False for the whole interval when no
	// C is ever sighted in it — a shell with no preexec support, or one
	// whose integration never activated — leaves the fence-installed window
	// (if any) as the only one, exactly as before this bead.
	OutputMarked bool
	// OutputStartRow is the cursor's row index at the instant OutputMarked was
	// set (nocx-2v80t.3.12, reopened): the boundary between this interval's
	// own prefix (rows [0, OutputStartRow), never its output) and its real
	// output (row OutputStartRow onward), BY POSITION — never by re-matching
	// text — because a command whose own output repeats its prefix's text
	// must still keep every line it wrote (the paired case the original bead
	// named). Meaningless while OutputMarked is false.
	OutputStartRow int
	// OutputMarkDeparted is [Session.departedRows] at the same instant: how
	// many rows the session had ALREADY departed when OutputStartRow was
	// measured. A command that never scrolls departs none of its own rows
	// before it seals, so OutputStartRow still names exactly the row its own
	// output began on when the closing screen is built — the gap the
	// original fix left, because it protected rows that later DEPART (the
	// stream) but never the rows an interval that never scrolls hands
	// straight to its own closing screen (closingRowsForStream). A command
	// that DOES scroll past its own prefix has that prefix reported as
	// departures like any other row, and the difference between the two
	// counts is exactly how much of the prefix a scroll has since carried
	// off — still by position, still never by text (outputMarkSkipLocked).
	OutputMarkDeparted uint64
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

// pendingBoundaryRow is one row of the window: the text a departing row is
// matched against, exactly as before, and a [emulator.RowTrack] pinned to the
// physical row at the instant the window was installed — the identity content
// alone cannot give (nocx-2v80t.3.10). Track is nil when the port refused to
// track the row (TrackRow returned an error, e.g. [emulator.ErrOutOfRange] on
// a screen too short to have a row there); a nil Track is treated as already
// dead, never as alive, because a window entry this port could not name at
// all can name nothing today either.
type pendingBoundaryRow struct {
	Row   emulator.Row
	Track emulator.RowTrack
	// Cursor marks the entry captured at the boundary's own cursor position —
	// the LAST of the window, since boundaryRowsThatLeave ends there. It is
	// the one row a shell can extend WITHOUT a preceding newline, so once
	// every other entry has left (matched) or ceased (purged), this one may
	// still be rewritten in place — the new prompt, or an app-submitted
	// command's own echo, printed over what the fence saw as blank — before
	// it finally departs (nocx-2v80t.3.12; see suppressBoundaryScreenLocked).
	Cursor bool
}

// alive answers whether the physical row a pendingBoundaryRow names can still
// be named at all — false for a nil Track (never pinned) and for one whose
// row has been destroyed since (an erase the pin's own coordinate space
// survives, a reset, or the library's retention pruning it beyond recall).
func (p pendingBoundaryRow) alive() bool {
	return p.Track != nil && p.Track.Alive()
}

// releasePendingScreenLocked frees every track the current window holds and
// empties it. The one place [Session.pendingScreen] is ever set to nil or
// replaced wholesale, so a track is never simply dropped on the floor: every
// path that used to write `s.pendingScreen = nil` calls this instead.
func (s *Session) releasePendingScreenLocked() {
	for _, p := range s.pendingScreen {
		if p.Track != nil {
			p.Track.Release()
		}
	}
	s.pendingScreen = nil
	s.pendingEntered = false
}

// purgeDestroyedPendingScreenLocked drops, and releases, every window entry
// whose row has been destroyed since the window was installed — content that
// ceased rather than left, and can therefore never legitimately depart again
// (nocx-2v80t.3.10). It runs before the window is ever consulted for a match,
// so a row a `clear` destroyed cannot be mistaken for the next command's own
// output merely because that output reads the same.
//
// This is the identity half of the fix and the reflow-safe half: a
// [emulator.RowTrack] follows its row across a resize's reflow exactly as
// content does (TestAClosingScreenIsNotStoredAgainAfterAGeometryCommit stays
// green), and unlike content it also survives a coincidental repeat, because
// it names the row rather than reading it. It is not the WHOLE fix: an erase
// that leaves the row's own page slot in place (ordinary ED, verified against
// the real library — GhosttyTrackedGridRef stays alive through it) is
// destruction by this port's own contract (emulator.Terminal.DepartedRows:
// "an erase... destroyed ceased rather than left") that the library's tracked
// reference does not surface as one; sealPendingWithoutScreenLocked's
// reconciliation against a freshly read screen is the other half, for exactly
// that case.
func (s *Session) purgeDestroyedPendingScreenLocked() {
	if len(s.pendingScreen) == 0 {
		return
	}
	kept := s.pendingScreen[:0]
	for _, p := range s.pendingScreen {
		if !p.alive() {
			if p.Track != nil {
				p.Track.Release()
			}
			continue
		}
		kept = append(kept, p)
	}
	s.pendingScreen = kept
	if len(s.pendingScreen) == 0 {
		s.pendingEntered = false
	}
}

// suppressBoundaryScreenLocked declines to stream the rows the interval sealed
// last already handed over as its closing screen.
//
// The window is that boundary's screen, and a candidate row is matched BY
// CONTENT rather than by position, because a geometry commit can land between
// the boundary and the rows that leave next: a pane that SHRINKS pushes the
// screen's top row into history WITHOUT it departing (the port's rule — a
// pushed row is not a departure), so the first row that leaves is not the
// window's first row. Matching only the head is what this used to do, and a
// commit therefore killed the whole window on its first comparison: the
// closing screen the block before already holds was streamed again into the
// block after it, 26 and 27 rows at a time on the e2e, and deterministically
// in TestAClosingScreenIsNotStoredAgainAfterAGeometryCommit.
//
// Content is ambiguous by design, though: a row a `clear` destroyed and a row
// the NEXT command legitimately prints can read the same, and no comparison
// of the two texts can ever tell them apart (nocx-2v80t.3.10). That is what
// purgeDestroyedPendingScreenLocked runs for, first, every time: an entry
// still standing here has already been confirmed nameable — not necessarily
// UNCHANGED (an in-place rewrite the library does not treat as destruction is
// the harder case sealPendingWithoutScreenLocked's reconciliation closes),
// but not a row that ceased to exist. Content is what decides WHICH remaining
// entry a departing row belongs to; identity is what decides whether an entry
// may be matched against at all.
//
// A row that matches an entry consumes THAT entry — a row leaves once — and the
// entries before it STAY: they are rows a commit pushed off the screen, and one
// may still come back (a later growth pulls history back in) and leave, in
// which case it is suppressed like any other row of the boundary's screen. The
// window is cleared by the first arriving row that matches nothing in it, which
// is the first row of the next command's own output: that is what keeps a
// window from swallowing output that merely LOOKS like the boundary's screen,
// and a prompt row repeats all session long.
//
// The caller has already established that the interval in flight is the one
// that follows the boundary, and that the feed was not struck: a struck feed
// suppresses nothing, because the hole it names rides the batch that follows.
func (s *Session) suppressBoundaryScreenLocked(rows []emulator.Row) []emulator.Row {
	s.purgeDestroyedPendingScreenLocked()
	if len(s.pendingScreen) == 0 {
		// Nothing the interval before left is on the screen: every row that
		// leaves now is the next command's own.
		return rows
	}
	kept := rows[:0]
	for i, row := range rows {
		at := -1
		for j := range s.pendingScreen {
			if sameVisibleRow(row, s.pendingScreen[j].Row) {
				at = j
				break
			}
		}
		if at < 0 && len(s.pendingScreen) == 1 && s.pendingScreen[0].Cursor {
			// The one entry left is the boundary's own cursor row (see
			// pendingBoundaryRow.Cursor): every other entry has already
			// either matched and left, or ceased and been purged, so this
			// is the LAST resort, never the first. It is matched by
			// IDENTITY — it is simply whichever entry survives to be the
			// window's sole member — and never by content, because content
			// is exactly what it may no longer carry: a shell can only
			// extend the cursor's own row without a preceding newline, so
			// the next prompt or an app-submitted command's own echo lands
			// there before the row finally departs, reading nothing like
			// the blank (or partial) line the fence saw (nocx-2v80t.3.12).
			// A row a shell writes anywhere else first needs a newline,
			// which makes it a brand-new row this window never named, so
			// the wildcard can never reach past this one entry.
			at = 0
		}
		if at < 0 {
			if !s.pendingEntered {
				// BEFORE the window's first row: this is a row from ABOVE the
				// boundary's screen — history a geometry commit pulled back onto
				// the screen, whose departure the emulator reports again because
				// the row left once already. It is not the boundary's screen, and
				// it must not END the window: one stray row would otherwise cost
				// every row the window still holds.
				//
				// That is not hypothetical. Measured on the e2e: a shrink and a
				// growth around a boundary handed back one row the interval
				// before already stored, the window died on it, and the block
				// after stored the whole closing screen a second time — 27
				// foreign rows (nocx-2v80t.3.9). The stray row itself is still
				// streamed, and it is the EMULATOR's contract that it should not
				// have been reported at all; what this guards is that the
				// boundary's own rows do not go with it.
				kept = append(kept, row)
				continue
			}
			// The boundary's screen is done: from here on, what leaves the
			// screen is the next command's own output.
			kept = append(kept, rows[i:]...)
			s.releasePendingScreenLocked()
			return kept
		}
		if t := s.pendingScreen[at].Track; t != nil {
			t.Release()
		}
		s.pendingScreen = append(s.pendingScreen[:at], s.pendingScreen[at+1:]...)
		s.suppressedScreenRows++
		s.pendingEntered = true
	}
	return kept
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
	if lost == 0 {
		rows = s.suppressBoundaryScreenLocked(rows)
	}
	if len(rows) == 0 && lost == 0 {
		// A struck feed with an empty report still carries its marker: a
		// batch withheld whole would swallow the hole it names.
		return
	}
	s.releasePendingScreenLocked()
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

// sealObservationLocked closes the interval in flight at the SIGHTING that
// just joined its meeting — the half that arrives in the ordered stream. The
// closing screen is the boundary's own: the screen as the fence sat on it.
// It is never a screen read at the authentication: the completion is on a
// different carrier and may win the race by whole feeds, and a screen read at
// it would name rows the next command had already printed (measured in the
// e2e: a block carrying the next command's rows at the head of the interval
// before it, nocx-2v80t.3.9).
//
// Two shapes meet here. When the fence was sighted FIRST the boundary is that
// sighting's capture — the screen taken at the fence in one instant with the
// count, carried whole by splitObservationAtFenceLocked — and the join seals
// the capture rather than re-reading anything (sealObservationFromCaptureLocked).
// When the COMPLETION arrived first the interval in flight was parked with no
// screen read at all (parkObservationLocked), and this join is the event that
// seals it: the screen read now is the screen the fence is sitting on, at the
// instant of the sighting's own ingest, before any byte of the next command's
// output has been fed.
//
// The parked interval's other ending is an EVENT, never a timer: when the
// fence's sighting never arrives at all, the next interval's start or the
// session's end seals it with no closing screen
// (sealPendingWithoutScreenLocked), and the block's `output may be
// incomplete` carries what that honestly means.
//
// A command that produced no output at all still leaves a record: the
// interval ran, the boundary arrived, and the record opens and closes on the
// same screen with nothing departed.
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
	if scr, ok := s.takeObservationScreenLocked(); ok {
		rec.Closing = scr
	}
	skip := outputMarkSkipLocked(o.OutputStartRow, o.OutputMarkDeparted, s.departedRows)
	s.expectBoundaryScreenLocked(rec.Closing)
	s.emitIntervalEndLocked(nonce, s.departedRows, closingRowsForStream(rec.Closing, skip))
	// The next interval opens on the boundary screen, at the boundary
	// revision.
	s.observation = &observationOpen{Opened: rec.Sealed, Opening: rec.Closing}
	s.storeSealedObservationLocked(rec)
}

// parkObservationLocked parks the interval in flight at its AUTHENTICATED
// completion, before any sighting: the screen is deliberately not read (the
// next command may already have printed), the boundary is the count the
// interval departed to, and the seal waits for the fence's sighting — which
// is in the ordered stream and arrives in the ordinary case. At most one
// interval is parked, and it is settled by an event, never a timer: the next
// interval's start, or the session's end (nocx-2v80t.3.9).
func (s *Session) parkObservationLocked(nonce FenceNonce) {
	o := s.observation
	if o == nil {
		return
	}
	if o.Nonce != (FenceNonce{}) && o.Nonce == nonce {
		// A duplicate completion must not refresh the parked boundary: the
		// interval's own end row is the count it had departed to when its
		// completion first arrived, and a second copy of the same event
		// changes nothing.
		return
	}
	s.observation = &observationOpen{
		Opened:             o.Opened,
		Opening:            o.Opening,
		Loss:               o.Loss,
		Nonce:              nonce,
		EndRow:             s.departedRows,
		OutputMarked:       o.OutputMarked,
		OutputStartRow:     o.OutputStartRow,
		OutputMarkDeparted: o.OutputMarkDeparted,
	}
}

// sealPendingWithoutScreenLocked settles an interval that authenticated
// complete but whose fence's sighting never arrived. It is settled by an
// EVENT, never a timer: the next interval's start, or the session's end.
//
// Its end marker carries the row count the interval departed to — the rows
// that streamed before the boundary are the interval's own — and NO closing
// screen: the screen was not read at the completion, and a screen read now
// would name rows the next command had already printed (nocx-2v80t.3.9). The
// record says its fence never arrived: the settle degrades the session's
// completeness to [CompletenessNoFence] before sealing, so the record's own
// Completeness reads no-fence — the block's "output may be incomplete" —
// while the rows the interval did stream stand in its summary. The parked
// record is evidence of what the command printed up to its own end, and its
// Completeness rides the record's own losses.
func (s *Session) sealPendingWithoutScreenLocked(nonce FenceNonce, parked *observationOpen) {
	rec := ObservationRecord{
		Nonce:        nonce,
		At:           s.inc,
		Opened:       parked.Opened,
		Sealed:       s.rev,
		Completeness: observationCompleteness(s.completeness, parked.Loss),
		Opening:      parked.Opening,
		Closing:      ObservationScreen{},
		Loss:         parked.Loss,
	}
	// The boundary screen this interval would have expected is EMPTY — no screen
	// was read at all — and an empty expectation must not wipe the one in force:
	// the rows the interval before left may still be about to leave the screen,
	// and losing their window is how a whole closing screen is stored twice
	// (nocx-2v80t.3.9). expectBoundaryScreenLocked says the same for a seal whose
	// screen read failed or was the alternate buffer's.
	// The parked count is the completion's own measurement and the last
	// trustworthy evidence of where the command's output ended — but the
	// interval kept streaming after it, and an end marker BEHIND rows the
	// runtime has already handed over puts the block's closing append behind
	// its own cursor and leaves the block unfrozen (measured at the wire: two
	// ends at one count while that interval's rows were already out,
	// nocx-2v80t.3.9). The rule is that an end marker is never less than what
	// the interval streamed: the rows it covers are the interval's own, and
	// the closing screen stays empty because no screen was ever read.
	s.emitIntervalEndLocked(nonce, max(s.departedRows, parked.EndRow), nil)
	// The next record opens on the screen read at the settle event — one
	// read under the lock, at the event's instant — never on the parked
	// interval's Opening: the parked opening predates the boundary's own
	// output, and the screen as it stands at the event is the earliest
	// instant the next interval can honestly be said to begin on.
	next := &observationOpen{Opened: rec.Sealed}
	if scr, ok := s.takeObservationScreenLocked(); ok {
		next.Opening = scr
		// The window this settle leaves standing was never re-captured, so a
		// row it names may since have been overwritten by exactly this
		// interval's own output — a `clear`, plainly, but any rewrite that
		// happens to read the same as what departs next is the same defect
		// (nocx-2v80t.3.10). This read is the one chance to notice before the
		// next command's real content arrives and a stale entry swallows it.
		s.reconcilePendingScreenLocked(scr)
	}
	s.observation = next
	s.storeSealedObservationLocked(rec)
}

// reconcilePendingScreenLocked drops whatever part of the window a screen
// read FRESHER than the window's own capture disagrees with — the check
// [Session.expectBoundaryScreenLocked] cannot make, because the boundary that
// just sealed had no screen of its own to compare (sealPendingWithoutScreenLocked;
// nocx-2v80t.3.10).
//
// It compares BY POSITION, at the same index, entry for entry — which is
// exactly the identity that breaks across a reflow (departed_window_geometry_test.go)
// — so it runs only when scr's geometry is the one the window was captured or
// last reconciled at: unchanged means nothing has pushed a row into history or
// pulled one back, so index i still names the same slot it did, and a
// position that no longer reads what the window recorded there was written to
// in between — an erase a [emulator.RowTrack] does not see as one
// (purgeDestroyedPendingScreenLocked's own comment has the library evidence),
// since content is the only signal that survives an in-place rewrite at all.
// A geometry that HAS moved leaves the window exactly as it stood: reconciling
// positionally across a reflow is the defect this file already fixed once,
// and suppressBoundaryScreenLocked's content match, which a reflow does not
// confuse, is what still guards it.
//
// Kept is the longest PREFIX still confirmed unwritten; the conservative
// direction, matching every other cut in this file — an occasional duplicate
// the block absorbs costs less than a false match swallowing real output.
func (s *Session) reconcilePendingScreenLocked(scr ObservationScreen) {
	if len(s.pendingScreen) == 0 {
		return
	}
	if scr.Geometry != s.pendingScreenGeom {
		return
	}
	candidates := boundaryRowsThatLeave(scr)
	n := len(s.pendingScreen)
	if len(candidates) < n {
		n = len(candidates)
	}
	cut := n
	for i := 0; i < n; i++ {
		if !sameVisibleRow(s.pendingScreen[i].Row, candidates[i]) {
			cut = i
			break
		}
	}
	if cut == len(s.pendingScreen) {
		// Every entry still reads exactly what it did when captured, at the
		// same position: nothing has overwritten it, so the window stands
		// whole.
		return
	}
	for _, p := range s.pendingScreen[cut:] {
		if p.Track != nil {
			p.Track.Release()
		}
	}
	s.pendingScreen = s.pendingScreen[:cut]
	if len(s.pendingScreen) == 0 {
		s.pendingEntered = false
	}
}

// settlePendingLocked settles the interval that is parked waiting for its
// fence's sighting, if there is one, and answers whether it settled one. It
// is the event the review's no-timer rule asks for: the next interval's
// start, or the session's end, is what closes an interval whose sighting
// never arrived (nocx-2v80t.3.9).
//
// The arriving nonce is what makes the call safe at every event: a sighting
// or a completion for the PARKED nonce is the parked interval's own join,
// not a lost sighting, and settles nothing — the no-op the join relies on.
// The zero nonce means the session itself is ending, which settles whatever
// is parked.
func (s *Session) settlePendingLocked(arriving FenceNonce) bool {
	parked := s.observation
	if parked == nil || parked.Nonce == (FenceNonce{}) || parked.Nonce == arriving {
		return false
	}
	return s.settleParkedLocked(parked.Nonce)
}

// settleParkedLocked settles the interval parked with the nonce: its
// authenticated completion arrived and its fence's sighting never will. The
// meeting is marked [RendezvousExpired] — settled, still record — the
// session's completeness degrades to [CompletenessNoFence] because an
// authenticated boundary went unmet, and the parked record seals with NO
// closing screen at its parked EndRow (sealPendingWithoutScreenLocked). It
// does not tick: every caller is an event that ticks for its own change.
func (s *Session) settleParkedLocked(nonce FenceNonce) bool {
	parked := s.observation
	if parked == nil || parked.Nonce != nonce {
		return false
	}
	if e := s.rendezvous[nonce]; e != nil && e.pending() {
		e.State = RendezvousExpired
		e.PinnedSource = nil
		s.rendezvousLatest, s.rendezvousHasLatest = nonce, true
	}
	if s.completeness == CompletenessComplete {
		s.completeness = CompletenessNoFence
	}
	s.sealPendingWithoutScreenLocked(nonce, parked)
	return true
}

// expectBoundaryScreenLocked installs the rows the interval just sealed left on
// the screen as the window the interval that follows must not stream again.
//
// A boundary that carries NOTHING — the screen read failed, the alternate buffer
// held the pane, or the interval was settled with no screen at all — leaves the
// window IN FORCE rather than clearing it: the rows the interval before left may
// still be about to leave, and an empty expectation that wipes them is how a
// whole closing screen is stored a second time. Measured on the e2e: a block
// holding the interval before's twenty-nine closing rows, the block after it
// starting at the very row the window should have held (nocx-2v80t.3.9).
func (s *Session) expectBoundaryScreenLocked(screen ObservationScreen) {
	s.installPendingScreenLocked(boundaryRowsThatLeave(screen), screen.Geometry, true)
}

// installPendingScreenLocked is the one place s.pendingScreen is ever
// (re)installed wholesale, shared by expectBoundaryScreenLocked (a
// boundary's closing screen, cursor row included and marked as the
// last-resort wildcard) and sightOutputMarkLocked (an output mark's screen,
// cursor row excluded entirely — see that method for why the two must
// differ there). rows carries whichever of those a caller already decided
// on; includeCursor only controls whether its LAST entry is flagged as the
// wildcard candidate, since rows itself has already been trimmed or not by
// the caller.
//
// A caller that hands in no rows leaves the window IN FORCE rather than
// clearing it: the rows an earlier boundary left may still be about to
// leave, and wiping them on an empty expectation is how a whole closing
// screen was stored a second time (nocx-2v80t.3.9).
func (s *Session) installPendingScreenLocked(rows []emulator.Row, geom Geometry, includeCursor bool) {
	if len(rows) == 0 {
		return
	}
	s.releasePendingScreenLocked()
	cloned := cloneObservationRows(rows)
	pending := make([]pendingBoundaryRow, len(cloned))
	for i, row := range cloned {
		// A row this port cannot track (ErrOutOfRange on a screen this call
		// itself just measured would be a contradiction, but TrackRow can
		// still refuse for a reason this comment does not need to guess) is
		// carried as an entry with no pin: pendingBoundaryRow.alive treats a
		// nil Track as already dead, so it is dropped by the very next purge
		// rather than trusted on content alone.
		track, err := s.emulator.TrackRow(i)
		if err != nil {
			track = nil
		}
		pending[i] = pendingBoundaryRow{Row: row, Track: track, Cursor: includeCursor && i == len(cloned)-1}
	}
	s.pendingScreen = pending
	s.pendingScreenGeom = geom
	s.pendingEntered = false
}

// sightOutputMarkLocked is the runtime's join for a sighted OSC 133 C
// (nocx-2v80t.3.12): the FIRST one inside the interval in flight locates
// where that interval's own output begins, exactly the way a fence's
// sighting locates where one ends (ADR-0024 decision 1 — a sighted marker
// authorises nothing, and this one authorises less than the fence, since it
// does not even locate an authenticated event: it only marks a position
// inside an interval sessionruntime already authenticated by other means).
//
// Everything ABOVE the cursor at this instant — the prompt this interval
// opened with, and an app-submitted command's own echoed line (the app
// writes it to the pty only AFTER the attempt already exists, decision 5,
// so the shell's own preexec, and this sighting, always follow it) — is
// this interval's own prefix, never its output, and is installed as the
// suppression window rows still there when the mark is sighted are pinned
// by identity, exactly as a boundary's closing screen is
// (expectBoundaryScreenLocked).
//
// The cursor's OWN row is deliberately EXCLUDED, and this is the one place
// this join must NOT mirror the fence's: at a fence, whatever later
// overwrites the cursor row is the NEXT prompt or echo — never this
// interval's own output — so the wildcard match is correct there. At an
// output mark, the cursor's row is where control returns the instant preexec
// finishes: bash goes straight to running the command (decision 5's own
// ordering), so whatever gets written there next is the command's OWN FIRST
// LINE OF REAL OUTPUT. Installing it as a suppression candidate — even
// content-matched rather than wildcarded — would swallow a command whose
// first line happens to read blank, exactly the row this cursor position
// already does before anything runs. So the row is never a candidate at all.
//
// Reading the CURRENT screen here rather than reusing whatever the last
// fence installed is deliberate: for a session's very first interval there
// is no prior fence to have installed anything at all, which is the gap
// this join exists to close, and for every later interval the current
// screen is a superset of whatever survived from the fence-installed window
// (nothing departs merely by drawing a prompt and an echoed line), so
// replacing it loses nothing and corrects for anything that changed in
// between.
//
// Called at most once per interval: OutputMarked is checked and set here,
// and a later C within the SAME interval is silently ordinary output —
// sessionruntime's own rule for "only the first counts" (ADR-0024 decision
// 1 leaves C no authority to begin with; this is the runtime's own
// bookkeeping, not a second authenticator). Without it, a nested command's
// own preexec (a shell function, a subshell) would re-arm the window from
// whatever the outer command had already printed, suppressing genuine
// output the instant it departed.
func (s *Session) sightOutputMarkLocked() {
	o := s.observation
	if o == nil || o.OutputMarked {
		return
	}
	o.OutputMarked = true
	scr, ok := s.takeObservationScreenLocked()
	if !ok {
		return
	}
	rows := boundaryRowsThatLeave(scr)
	// OutputStartRow and OutputMarkDeparted are recorded regardless of
	// whether there is a prefix to suppress: they are the POSITION this
	// interval's own closing screen must be cut at (outputMarkSkipLocked),
	// and a cursor already at the top (startRow 0) is a true fact about that
	// position, not merely "nothing to do" for this method's other job below.
	startRow := len(rows) - 1
	if startRow < 0 {
		startRow = 0
	}
	o.OutputStartRow = startRow
	o.OutputMarkDeparted = s.departedRows
	if len(rows) <= 1 {
		// Nothing above the cursor to protect: a blank screen with the
		// cursor already at the top, or the alternate buffer. The window
		// stands exactly as it was.
		return
	}
	s.installPendingScreenLocked(rows[:len(rows)-1], scr.Geometry, false)
}

// sightClearBoundaryLocked joins a sighted EffectClearBoundary: the program
// erased the display and its saved lines (nocx-2v80t.3.17). Unlike a fence or
// an output mark this needs no rendezvous with anything sessionruntime
// authenticated — the erase is a real fact the emulator's own state already
// reflects (ADR-0066: the backend owns the whole VT grammar, not a private
// second reading) — so the whole of this method is handing the sighting to
// the row stream, on the SAME ordered carrier OutputRows and IntervalEnd
// travel on. That is what keeps it from ever being attributed to the wrong
// side of a row: everything the stream has already carried when this fires
// is before it, and everything still to come is after, in the one order the
// stream keeps.
//
// It does not touch s.observation, s.pendingScreen or any loss counter: an
// erase does not open, close or seal an interval (that is still only an
// authenticated boundary's to do), and the rows it destroyed were never
// "departed" in the first place — DepartedRows' own contract already says an
// erase ceases rows rather than handing them off, so there is nothing here
// for a departure count to reconcile against.
//
// With no row stream bound (a session with nobody watching its rows yet)
// this is a no-op, exactly as drainObservationLocked is: the fact has nobody
// to reach, and there is nothing else for it to do.
func (s *Session) sightClearBoundaryLocked() {
	if rs := s.rowStream; rs != nil {
		rs.ClearBoundary()
	}
}

// sameVisibleRow reports whether two rows are the same LINE of text, whatever
// width the screen held when each was read.
//
// The suppression window below compares rows that were captured before a
// geometry commit with rows that leave after it, and a width change re-lays
// every row out: each row's cell slice is as wide as the pane was at the time,
// so the same line is never cell-equal across a reflow (80 cells against 148
// for the same eleven characters, measured — nocx-2v80t.3.9). What the window
// is deciding is whether the block before already holds this line, and that is
// a fact about the line, not about the layout: the comparison is on the
// graphemes, with the trailing blanks a wider screen pads with removed.
//
// The row's wrap flags are not compared either: a width change re-wraps rows, so
// one logical line may be one physical row before the change and two after, and a
// comparison that failed on wrapping would kill the window on the very reflow it
// exists for. Styles are not part of it for the reason a cell comparison never
// had them: a re-departing row comes back with the cells it had, a strict style
// comparison would miss it on a theme change, and a false match costs at most one
// suppressed row at the position the boundary's screen occupies — the ambiguity
// the block's own closing screen already absorbs.
//
// Two different rows that read the same are therefore one row to this window.
// That is the same tradeoff the order of the window already makes — a row that
// matches an entry consumes it — and it is bounded by the window being the
// boundary's own screen: a row the next command writes is not suppressed unless
// it reads exactly like a line the boundary left AND the window still expects
// one, which the clear-on-first-unmatched-row rule ends as soon as the next
// command's own output starts.
func sameVisibleRow(a, b emulator.Row) bool {
	return visibleRowText(a) == visibleRowText(b)
}

// visibleRowText is a row's visible line: the graphemes its cells carry, with
// the trailing blanks a wider screen pads with removed.
func visibleRowText(r emulator.Row) string {
	var sb strings.Builder
	for _, c := range r.Cells {
		if c.Grapheme != "" {
			sb.WriteString(c.Grapheme)
		}
	}
	return strings.TrimRight(sb.String(), " ")
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

// closingRowsForStream is the rows a boundary's own end marker actually
// carries — boundaryRowsThatLeave, minus a trailing entry this interval
// never wrote a single grapheme to (nocx-2v80t.3.12). The LAST of those rows
// sits at the cursor's own position, the one place a shell can still extend
// without a preceding newline: the next prompt, or an app-submitted
// command's own echoed line, both land there before that row finally
// departs (suppressBoundaryScreenLocked's Cursor wildcard is what catches
// either one on its way out, by identity, once it does). A cursor row this
// interval genuinely wrote into — its last output line with no trailing
// newline — is kept: the cut is on ABSENCE at the instant the fence sat
// there, never on what the row happens to read once something else writes
// into it, and a command whose entire output is one blank line still keeps
// that line, because it occupies the row BEFORE the cursor's own, not this
// one.
//
// This trims only what the interval's OWN closing append claims as its
// output; the suppression window (expectBoundaryScreenLocked) still installs
// the untrimmed rows, because the cursor's placeholder must still be
// recognised — and suppressed — when something else departs there next.
//
// skip is the count outputMarkSkipLocked computed: rows still on screen from
// BEFORE this interval's own output began (nocx-2v80t.3.12, reopened) — a
// command that never scrolls hands its closing append the whole screen
// otherwise, prior intervals' rows included, because nothing ever reported
// them as departed for boundaryRowsThatLeave to have excluded. Cut from the
// FRONT, by count, never by re-matching either end's text: skip names a
// position, not a pattern, so a command whose own output repeats its
// prefix's text keeps every line it actually wrote.
func closingRowsForStream(scr ObservationScreen, skip int) []emulator.Row {
	rows := boundaryRowsThatLeave(scr)
	if skip > 0 {
		if skip > len(rows) {
			skip = len(rows)
		}
		rows = rows[skip:]
	}
	if len(rows) == 0 {
		return rows
	}
	if visibleRowText(rows[len(rows)-1]) == "" {
		return rows[:len(rows)-1]
	}
	return rows
}

// outputMarkSkipLocked is the count of leading rows closingRowsForStream must
// cut for an interval whose output-mark was sighted (nocx-2v80t.3.12,
// reopened): how much of the prefix outputMarkLocked measured is STILL on
// screen at closing, by position.
//
// startRow is the cursor's row index at the sighting (observationOpen's
// OutputStartRow / observationCapture's own copy) — the number of rows that
// sat above this interval's own first output row at that instant, every one
// of them either a prior interval's still-undeparted content or this
// interval's own prompt and echo. markDeparted is [Session.departedRows] at
// that same instant; departed is the same counter at closing (or, for a
// parked interval sealed from its capture, the capture's own EndRow — the
// departedRows the fence's sighting had measured, which is exactly this
// count for that instant).
//
// A row can only leave the prefix by ACTUALLY departing — DepartedRows
// always takes from the top of the active area, in order — so each row
// counted between markDeparted and departed is one fewer row of the prefix
// still standing, never a row of this interval's own output: those rows sit
// below the prefix at the sighting instant and cannot depart before it does.
// A command that scrolls past its whole prefix (departed-markDeparted >=
// startRow) leaves nothing to skip: every foreign row already left through
// the ordinary departure stream, exactly as before this bead, and the
// interval's closing screen is the screen as it stands, unbounded.
func outputMarkSkipLocked(startRow int, markDeparted, departed uint64) int {
	if startRow <= 0 {
		return 0
	}
	delta := departed - markDeparted
	if delta >= uint64(startRow) { // #nosec G115 -- startRow > 0, checked above
		return 0
	}
	return startRow - int(delta) // #nosec G115 -- delta < startRow, checked above
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
	// OutputMarked is the SPLIT interval's own flag at the instant of the
	// split — carried here so an undo (returnObservationCaptureLocked, an
	// expired or evicted fence that authorised nothing) restores it rather
	// than leaving the rebased interval's fresh "false" standing in for an
	// interval that, since nothing about it actually ended, may already
	// have sighted its own C before this fence ever arrived.
	OutputMarked bool
	// OutputStartRow and OutputMarkDeparted are the split interval's own copy
	// of observationOpen's fields of the same name, same reason as
	// OutputMarked above: sealObservationFromCaptureLocked needs them to cut
	// the capture's closing screen at the right position (outputMarkSkipLocked,
	// against EndRow rather than a fresh read of [Session.departedRows], since
	// EndRow IS departedRows at this capture's own instant), and an undone
	// split must hand them back rather than leave the rebased interval's zero
	// values standing in for a mark it may already have sighted.
	OutputStartRow     int
	OutputMarkDeparted uint64
}

// splitObservationAtFenceLocked takes the boundary capture at a parking
// sighting and rebases the interval in flight to start here: the capture
// carries the record's boundaries up to the fence, its losses, the row index
// it had departed to, and the screen at it; the record in flight keeps
// collecting the output that FOLLOWS the fence, as the next record, opened
// at the fence's revision on the fence's screen. The fence itself is painted
// by nothing, so the closing screen ends where the command's visible output
// ended.
func (s *Session) splitObservationAtFenceLocked(rebase FenceNonce) *observationCapture {
	o := s.observation
	if o == nil {
		o = &observationOpen{Opened: s.rev}
	}
	cap := &observationCapture{
		Opened:             o.Opened,
		SightRev:           s.rev,
		EndRow:             s.departedRows,
		Opening:            o.Opening,
		Loss:               o.Loss,
		Completeness:       s.completeness,
		OutputMarked:       o.OutputMarked,
		OutputStartRow:     o.OutputStartRow,
		OutputMarkDeparted: o.OutputMarkDeparted,
	}
	if scr, ok := s.takeObservationScreenLocked(); ok {
		cap.Closing = scr
	}
	s.observation = &observationOpen{Opened: s.rev, Opening: cap.Closing, Rebased: rebase}
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
	skip := outputMarkSkipLocked(cap.OutputStartRow, cap.OutputMarkDeparted, cap.EndRow)
	s.expectBoundaryScreenLocked(cap.Closing)
	s.emitIntervalEndLocked(nonce, cap.EndRow, closingRowsForStream(cap.Closing, skip))
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
			Opened:             cap.Opened,
			Opening:            cap.Opening,
			Loss:               cap.Loss,
			OutputMarked:       cap.OutputMarked,
			OutputStartRow:     cap.OutputStartRow,
			OutputMarkDeparted: cap.OutputMarkDeparted,
		}
		return
	}
	o.Loss.RetentionFeeds += cap.Loss.RetentionFeeds
	o.Loss.RetentionFeedBytes += cap.Loss.RetentionFeedBytes
	o.Loss.IngestLostBytes += cap.Loss.IngestLostBytes
	o.Opened, o.Opening = cap.Opened, cap.Opening
	// The split is un-done, so the interval belongs to no fence again, and
	// its own C-sighting status is whatever it was before the split rather
	// than the rebased interval's fresh "false" (nocx-2v80t.3.12) — nothing
	// about the interval this fence sat inside actually ended.
	o.Rebased = FenceNonce{}
	o.OutputMarked = cap.OutputMarked
	o.OutputStartRow = cap.OutputStartRow
	o.OutputMarkDeparted = cap.OutputMarkDeparted
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
