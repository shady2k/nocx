package sessionruntime

import (
	"context"
	"sort"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/log"
)

// A GEOMETRY COMMIT RE-LAYS THE ROWS THE BOUNDARY BOOKKEEPING NAMES
// (nocx-2v80t.5).
//
// Two pieces of this runtime describe rows on the screen as they stood at an
// earlier instant: the suppression window (pendingScreen), which recognises a
// departing row by its CONTENT, and the output mark's position
// (observationOpen.OutputStartRow), which is a row INDEX. A width change
// re-lays every soft-wrapped line out, so after it both describe rows that no
// longer exist: a command line typed at 150 columns is three different rows at
// 80, none of which reads like either of the two the window holds, and the row
// the output starts at has moved down. Measured on the e2e: a command whose
// window closed before its output, the session returning to 80x24 on the
// detach, stored its own command line as its first three output rows.
//
// Both are repaired here, at the commit, from row IDENTITY — the
// [emulator.RowTrack] pins the window and the output mark already hold, which
// follow their rows through the reflow — never by re-matching text.

// reflowBoundaryLocked brings the suppression windows and output prefixes
// up to the geometry just committed. The pin locates the cut, but the prefix
// must describe that same geometry for outputPrefixStands (nocx-l3evv).
//
// While the alternate screen holds the pane the primary's rows were re-laid
// out too, but no pin on them can be located (RowTrack.ActiveRow answers
// ErrUnsupported), so the repair is OWED rather than skipped: it runs the
// first time rows are drained with the primary back in front
// (settleOwedReflowLocked).
func (s *Session) reflowBoundaryLocked() {
	if scr, err := s.emulator.Screen(); err != nil || scr != emulator.ScreenPrimary {
		s.reflowOwed = true
		return
	}
	s.reflowOwed = false
	s.reflowOutputPrefixesLocked()
	s.pendingScreen = s.reflowWindowLocked(s.pendingScreen)
	// A capture holding its sighting's window keeps the window it replaced,
	// to put back if nobody authenticates the fence; that one names the same
	// re-laid rows and must describe them as they are when it is put back.
	if c := s.pendingCapture; c != nil && c.Holding {
		c.Prior = s.reflowWindowLocked(c.Prior)
	}
}

// settleOwedReflowLocked runs a repair a commit owed while the alternate
// screen was in front, once the primary is back. The feed that brought the
// primary back may already have scrolled the window's rows away; those are
// refreshed through their own pins (reflowWindowLocked) and recognised whole
// by continuation as they depart.
func (s *Session) settleOwedReflowLocked() {
	if s.reflowOwed {
		s.reflowBoundaryLocked()
	}
}

// reflowWindowLocked rebuilds a window from where its pins now are. Each
// surviving pin names one physical row, but a narrower grid splits one row
// into several and a wider one joins several into one, so the window is
// rebuilt by LOGICAL line: every row the pin's line now occupies, walking up
// through continuations and down through wraps, read afresh and pinned
// afresh. Rows a pin names more than once (a widening joined them) are one
// entry.
//
// An entry that is alive but no longer on the active screen stays ahead of
// the rest with its content read afresh through its pin: it is in the
// history, where the only question left about it is whether it departs. A
// pinless or dead entry is left for the next purge.
func (s *Session) reflowWindowLocked(window []pendingBoundaryRow) []pendingBoundaryRow {
	if len(window) == 0 {
		return window
	}
	var kept []pendingBoundaryRow
	onScreen := map[int]bool{}
	cursorLine := -1
	for _, p := range window {
		if !p.alive() {
			kept = append(kept, p)
			continue
		}
		y, err := p.Track.ActiveRow()
		if err != nil {
			// Already off the active screen: the feed that brought the
			// primary back scrolled it into the history before the owed
			// repair could run. Its head reads as its pin's row reads now,
			// and the rest of its line is recognised by continuation as it
			// departs (suppressBoundaryScreenLocked).
			if row, rerr := p.Track.Row(); rerr == nil {
				p.Row = cloneObservationRows([]emulator.Row{row})[0]
			}
			kept = append(kept, p)
			continue
		}
		first, last := s.logicalLineLocked(y)
		for r := first; r <= last; r++ {
			onScreen[r] = true
		}
		if p.Cursor {
			cursorLine = last
		}
		p.Track.Release()
	}
	rows := make([]int, 0, len(onScreen))
	for y := range onScreen {
		rows = append(rows, y)
	}
	sort.Ints(rows)
	for _, y := range rows {
		row, err := s.emulator.Row(y)
		if err != nil {
			continue
		}
		track, err := s.emulator.TrackRow(y)
		if err != nil {
			track = nil
		}
		kept = append(kept, pendingBoundaryRow{Row: cloneObservationRows([]emulator.Row{row})[0], Track: track, Cursor: y == cursorLine})
	}
	return kept
}

// logicalLineLocked is the span of active rows the soft-wrapped line through
// row y occupies: up while a row continues the one above it, down while a row
// wraps into the one below. A read past either edge fails and ends the walk.
func (s *Session) logicalLineLocked(y int) (first, last int) {
	first, last = y, y
	for first > 0 {
		row, err := s.emulator.Row(first)
		if err != nil || !row.Continuation {
			break
		}
		first--
	}
	for {
		row, err := s.emulator.Row(last)
		if err != nil || !row.Wrap {
			break
		}
		last++
	}
	return first, last
}

// outputPrefixBasis is the last observable primary-screen eligibility and
// its coordinates. refreshOutputPrefixesLocked writes it; sightEraseDisplayLocked
// clears it. Transfers between an open observation and a capture preserve it.
type outputPrefixBasis struct {
	Standing bool
	Row      int
	Top      int
}

// refreshOutputPrefixesLocked remembers the last primary-screen check before
// a resize. Reject an alternate or unreadable screen before reading its rows:
// neither can confirm primary pins. Leave the last check alone so a commit
// there can pay its repair when primary returns.
//
// Limitation (nocx-7epsi, follow-up to nocx-l3evv): a rewrite followed by
// 1049h in one sub-feed leaves no observable primary state: its pins now
// answer ErrUnsupported. A primary rewrite in the sub-feed restoring it also
// precedes the owed repair's first readable screen. Retaining the last
// eligibility fixes the unchanged-prefix P0, but cannot detect those rewrites.
// An emulator transition observation must close this gap, not a raw byte
// sniff. A changed boundary top still declines repair conservatively.
func (s *Session) refreshOutputPrefixesLocked() {
	screen, err := s.emulator.Screen()
	if err != nil {
		return
	}
	// Even a prefix whose rows we do not need still observes a buffer
	// transition: targets minted before it must become incomparable.
	s.observeBufferLocked(screen == emulator.ScreenAlternate)
	if screen != emulator.ScreenPrimary {
		return
	}
	scr, ok := s.takeObservationScreenLocked()
	if !ok || scr.AltScreen {
		return
	}
	check := func(pin emulator.RowTrack, prefix []emulator.Row) outputPrefixBasis {
		if pin == nil {
			return outputPrefixBasis{}
		}
		y, err := pin.ActiveRow()
		top := boundaryRowsTop(scr)
		if err != nil || y < top || y > len(scr.Lines) {
			return outputPrefixBasis{}
		}
		// Eligibility covers the whole prefix, even when a program parked
		// its cursor above a row it rewrote. The closing-screen slice ends
		// at that cursor and would silently clamp away the changed row.
		return outputPrefixBasis{
			Standing: outputPrefixStands(scr.Lines[top:y], y-top, prefix),
			Row:      y,
			Top:      top,
		}
	}
	if o := s.observation; o != nil {
		o.OutputPrefixBasis = check(o.OutputStartTrack, o.OutputPrefix)
	}
	for _, e := range s.rendezvous {
		if c := e.captured; c != nil {
			c.OutputPrefixBasis = check(c.OutputStartTrack, c.OutputPrefix)
		}
	}
}

// reflowOutputPrefixesLocked repairs only prefixes that stood before the
// reflow. Re-reading a rewritten prefix would bless the command's own output
// as old content and hide it. A capture's frozen Closing and OutputSkip stay
// untouched; its prefix is for a possible return to the open interval.
func (s *Session) reflowOutputPrefixesLocked() {
	scr, ok := s.takeObservationScreenLocked()
	if !ok || scr.AltScreen {
		return
	}
	repair := func(pin emulator.RowTrack, prefix []emulator.Row, basis outputPrefixBasis) []emulator.Row {
		top := boundaryRowsTop(scr)
		if !basis.Standing || pin == nil || basis.Top != top || basis.Row < basis.Top {
			return prefix
		}
		y, err := pin.ActiveRow()
		if err != nil || y < top || y > len(scr.Lines) {
			return prefix
		}
		log.From(context.Background()).Debug("reflowed output prefix",
			"session", s.inc.Session, "generation", s.inc.Generation,
			"prior_pin_row", basis.Row, "pin_row", y, "boundary_top", top,
			"prior_prefix_rows", len(prefix), "prefix_rows", y-top)
		return cloneObservationRows(scr.Lines[top:y])
	}
	if o := s.observation; o != nil {
		o.OutputPrefix = repair(o.OutputStartTrack, o.OutputPrefix, o.OutputPrefixBasis)
	}
	for _, e := range s.rendezvous {
		if c := e.captured; c != nil {
			c.OutputPrefix = repair(c.OutputStartTrack, c.OutputPrefix, c.OutputPrefixBasis)
		}
	}
}
