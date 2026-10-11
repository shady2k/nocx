package session

// The screen reads (ADR-0066, design §6.5 and §6.7): the frame a session's
// runtime holds, and the PTY-less replay the calibration path reads a capture
// through.
//
// They live on the session service because the EMULATOR does. A caller may not
// derive a screen from the bytes it receives: the helper's output window is
// bounded and reclaims what nobody read, so an emulator fed the surviving
// suffix is not authoritative. What is authoritative is the runtime beside the
// PTY, and this is how a question reaches it.

import (
	"fmt"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// readScreen answers the screen a session's runtime holds.
func (s *Service) readScreenCapture(p proto.ScreenCaptureParams) (proto.ScreenCaptureResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.ScreenCaptureResult{}, err
	}
	if hs.runtime == nil {
		return proto.ScreenCaptureResult{}, fmt.Errorf("session %s has no runtime to capture a screen from", p.Session.Session)
	}
	var region *sessionruntime.ScreenRange
	if p.Range != nil {
		region = &sessionruntime.ScreenRange{Start: p.Range.Start, End: p.Range.End}
	}
	capture, err := hs.runtime.ReadScreenCapture(region)
	if err != nil {
		return proto.ScreenCaptureResult{}, err
	}
	rows := make([][]proto.ScreenCell, len(capture.Rows))
	for y, row := range capture.Rows {
		cells := make([]proto.ScreenCell, len(row.Cells))
		for x, cell := range row.Cells {
			text := ""
			if cell.HasText {
				text = cell.Grapheme
			}
			cells[x] = proto.ScreenCell{Text: text, Width: int(cell.Width)}
		}
		rows[y] = cells
	}
	kind := "normal"
	if capture.Identity.AltScreen {
		kind = "alternate"
	}
	return proto.ScreenCaptureResult{
		Floor: capture.Floor, Start: capture.Start, End: capture.End, Total: capture.Total,
		Rows: rows, CursorX: capture.Cursor.X, CursorY: capture.CursorRow,
		CursorVisible: capture.Cursor.Visible,
		Identity: proto.ScreenCaptureIdentity{
			Kind: kind, Instance: capture.Identity.BufferInstance,
			Cols: capture.Identity.Cols, Rows: capture.Identity.Rows,
			Generation: capture.Generation,
		},
		Revision: uint64(capture.Revision), Completeness: completenessName(capture.Completeness),
	}, nil
}

func (s *Service) readScreen(p proto.ScreenParams) (proto.ScreenResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.ScreenResult{}, err
	}
	frame, revision, completeness, err := hs.readFrame()
	if err != nil {
		return proto.ScreenResult{}, err
	}
	return proto.ScreenResult{
		Frame:        screenFrame(frame),
		Revision:     revision,
		Completeness: completenessName(completeness),
	}, nil
}

// resendScreen has a session's runtime hand every consumer its latest frame —
// the runtime's own Resend, cells and never an effect. The frame travels the
// screen drain like any other, so it reaches the coordinator in order with
// the frames around it (proto.OpScreenResend says why it is not the result).
func (s *Service) resendScreen(p proto.ScreenResendParams) (proto.ScreenResendResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.ScreenResendResult{}, err
	}
	if hs.runtime == nil {
		return proto.ScreenResendResult{}, fmt.Errorf("session %s has no runtime to resend a screen from", p.Session.Session)
	}
	if err := hs.runtime.Consumers().Resend(); err != nil {
		return proto.ScreenResendResult{}, err
	}
	return proto.ScreenResendResult{}, nil
}

// readFrame reads the session's screen, the runtime's revision and what the
// runtime can claim about the stream — ALL THREE AT ONE INSTANT.
//
// That instant is the whole of [sessionruntime.Session.ReadScreen]'s reason for
// existing. A frame is several reads of one terminal and the emulator's own
// port orders none of them against the pump: a chunk ingested between the caret
// read and the rows is a frame whose rows carry text the caret it reports has
// not reached, and a revision read after the frame is a revision the frame does
// not describe. Held under the runtime's lock there is no such instant to land
// in, and what a caller may therefore rely on is exactly this: the frame, its
// revision and its completeness describe one state of that session's screen.
//
// IT TAKES NO LOCK OF THE HOST SESSION, and that is load-bearing rather than a
// shortcut. hostSession.write holds s.mu from lease validation through the
// return of proc.Write, so a frame read that waited on that mutex would park
// behind a program that has stopped reading — the same deadlock the pump's own
// doc names, arriving from the other side. It does not need that lock: the lock
// it takes is the runtime's, which is the one the ingest path takes, and a
// runtime that has been ended answers [emulator.ErrClosed] rather than a
// screen, which is the honest answer for a session nobody can read any more.
func (hs *hostSession) readFrame() (paneview.Frame, uint64, sessionruntime.Completeness, error) {
	// Read BEFORE the cells, never after (spec §5.4, nocx-6q1uh.3): the
	// fence this frame carries must never claim to be newer than what the
	// screen it is paired with actually reflects. Reading it first can only
	// under-report — a write that completed in the gap between this line and
	// ReadScreen's callback is one the frame is honestly silent about — and
	// under-reporting is the direction §5.4 already tolerates ("may still
	// have been produced before the program read our input"); over-reporting
	// would tell a caller their write landed before the screen could
	// possibly show it.
	fence := hs.owner.inputFence()
	var frame paneview.Frame
	rev, completeness, err := hs.runtime.ReadScreen(func(term emulator.Terminal) error {
		f, ferr := paneview.From(term)
		frame = f
		return ferr
	})
	if err != nil {
		return paneview.Frame{}, 0, sessionruntime.CompletenessUnknown, err
	}
	frame.InputFence = fence
	return frame, uint64(rev), completeness, nil
}

// replay feeds a capture's bytes to a fresh terminal and answers one frame per
// mark.
//
// The terminal is built from the SAME factory the session runtimes are built
// from, so the emulator this helper replays through is the emulator it directs
// sessions with — one choice, named once (ADR-0065/0066). Nothing is registered
// in the inventory and no PTY exists: a replay is a reading of bytes, not a
// session, and giving it a session id would put something in the inventory that
// no shell is behind.
func (s *Service) replay(p proto.ReplayParams) (proto.ReplayResult, error) {
	if p.Cols <= 0 || p.Rows <= 0 || p.Cols*p.Rows > proto.MaxReplayCells {
		return proto.ReplayResult{}, fmt.Errorf(
			"%w: geometry %dx%d is not a terminal this helper will allocate (%d cells at most)",
			ErrReplay, p.Cols, p.Rows, proto.MaxReplayCells)
	}
	frames, err := paneview.Replay(s.screen, emulator.Geometry{Cols: p.Cols, Rows: p.Rows}, p.Chunks, p.Through)
	if err != nil {
		return proto.ReplayResult{}, fmt.Errorf("%w: %v", ErrReplay, err)
	}
	out := make([]proto.ScreenFrame, 0, len(frames))
	for _, f := range frames {
		out = append(out, screenFrame(f))
	}
	return proto.ReplayResult{Frames: out}, nil
}

// screenFrame renders a frame onto the wire shape.
//
// It is the helper's ONE conversion in this direction, and it drops what the
// wire does not carry rather than inventing a place for it: the styles a cell
// holds are not in a Frame at all (a Frame is text, width and caret), so
// nothing is lost here that a consumer was ever offered.
func screenFrame(f paneview.Frame) proto.ScreenFrame {
	out := proto.ScreenFrame{
		Cols:          f.Cols,
		Rows:          f.Rows,
		CursorX:       f.CursorX,
		CursorY:       f.CursorY,
		CursorVisible: f.CursorVisible,
		AltScreen:     f.AltScreen,
		Lines:         make([][]proto.ScreenCell, len(f.Lines)),
	}
	for y, line := range f.Lines {
		cells := make([]proto.ScreenCell, len(line))
		for x, c := range line {
			cells[x] = proto.ScreenCell{Text: c.Text, Width: c.Width}
		}
		out.Lines[y] = cells
	}
	return out
}

// completenessName spells what a runtime claims for the wire. It is a switch
// rather than a cast because the two vocabularies are deliberately separate
// types — one is the contract's, one is the wire's — and a value neither knows
// is refused as unknown rather than passed through: an unrecognised claim must
// never arrive at a caller looking stronger than it is.
func completenessName(c sessionruntime.Completeness) proto.Completeness {
	switch c {
	case sessionruntime.CompletenessComplete:
		return proto.CompletenessComplete
	case sessionruntime.CompletenessLostIngest:
		return proto.CompletenessLostIngest
	case sessionruntime.CompletenessNoFence:
		return proto.CompletenessNoFence
	case sessionruntime.CompletenessEvicted:
		return proto.CompletenessEvicted
	case sessionruntime.CompletenessUnknown:
		return proto.CompletenessUnknown
	default:
		return proto.CompletenessUnknown
	}
}
