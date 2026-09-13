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

// readFrame reads the session's screen, the runtime's revision and what the
// runtime can claim about the stream.
//
// IT TAKES NO LOCK OF THE HOST SESSION, and that is load-bearing rather than a
// shortcut. hostSession.write holds s.mu from lease validation through the
// return of proc.Write, so a frame read that waited on that mutex would park
// behind a program that has stopped reading — the same deadlock the pump's own
// doc names, arriving from the other side. It does not need the lock: the
// emulator serialises its own access (the port's contract), and a runtime that
// has been ended answers [emulator.ErrClosed] rather than a screen, which is
// the honest answer for a session nobody can read any more.
func (hs *hostSession) readFrame() (paneview.Frame, uint64, sessionruntime.Completeness, error) {
	frame, err := paneview.From(hs.screen)
	if err != nil {
		return paneview.Frame{}, 0, sessionruntime.CompletenessUnknown, err
	}
	return frame, uint64(hs.runtime.Revision()), hs.runtime.Completeness(), nil
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
