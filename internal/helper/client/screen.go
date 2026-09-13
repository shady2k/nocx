package client

// The screen reads, coordinator side: what a session's runtime holds, and the
// PTY-less replay a capture is read through.
//
// The two exist because the emulator is the helper's (ADR-0066) and the
// coordinator may not hold one: cmd/nocx-server is built CGO_ENABLED=0, and an
// emulator fed the bytes this process receives would be fed a stream whose
// middle the helper's bounded window reclaimed.

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// ErrScreenUnsupported is a helper generation that does not answer OpScreen.
//
// It is a fact about the GENERATION and not about the pane: two generations are
// resident at once by design, and one of them predates the runtime this read
// asks about. A caller that could not tell it from "the session is gone" would
// report a live pane as missing.
var ErrScreenUnsupported = errors.New("helper: this generation does not report a session's screen")

// Screen reads the frame a session's runtime holds now, with the runtime's
// revision and what it claims about the stream it holds.
//
// Three answers, and they are three different situations:
//
//	(frame, nil)               the screen as the runtime holds it
//	(nil, ErrScreenUnsupported) this generation has no runtime to ask
//	(nil, ErrNoSuchSession…)    the generation does not hold that session
//
// The second is why the error is typed: an older generation answering
// unknown_op means a pane that cannot be READ at all, and a coordinator that
// treated it as a transient failure would retry a question nothing will ever
// answer.
func (c *Client) Screen(ctx context.Context, id HostSessionID) (paneview.Frame, error) {
	var result proto.ScreenResult
	err := c.Call(ctx, proto.ServiceSession, proto.OpScreen, proto.ScreenParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(id.Generation),
			Session:    id.Session,
		},
	}, &result)
	if err != nil {
		var refusal *RefusalError
		if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeUnknownOp {
			return paneview.Frame{}, ErrScreenUnsupported
		}
		return paneview.Frame{}, err
	}
	frame := frameFromWire(result.Frame)
	frame.Revision = sessionruntime.Revision(result.Revision)
	frame.Completeness = completenessFromWire(result.Completeness)
	return frame, nil
}

// Replay feeds a capture's bytes to the helper's PTY-less emulator and answers
// the screen after each mark.
//
// `through` is how many chunks have been consumed at each mark, computed by the
// caller that owns the capture format (internal/agentcapture.ChunksThrough):
// the arithmetic is not repeated here, because two derivations of "which chunk
// belongs to this mark" agree until one of them moves.
//
// The chunks travel as RAW BYTES and are base64 on the wire (proto.ReplayParams
// says why): a PTY stream is not UTF-8, and a JSON string would replace every
// byte that is not with U+FFFD before the emulator ever saw it.
func (c *Client) Replay(ctx context.Context, cols, rows int, chunks [][]byte, through []int) ([]paneview.Frame, error) {
	var result proto.ReplayResult
	err := c.Call(ctx, proto.ServiceSession, proto.OpReplay, proto.ReplayParams{
		Cols:    cols,
		Rows:    rows,
		Through: through,
		Chunks:  chunks,
	}, &result)
	if err != nil {
		var refusal *RefusalError
		if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeUnknownOp {
			return nil, ErrScreenUnsupported
		}
		return nil, err
	}
	out := make([]paneview.Frame, 0, len(result.Frames))
	for _, f := range result.Frames {
		frame := frameFromWire(f)
		// A replay is complete by construction: every byte the caller asked to
		// be shown reached the terminal it was fed to, and there was no window
		// between them. It is the same statement paneview.Replay makes where
		// the emulator is local.
		frame.Completeness = sessionruntime.CompletenessComplete
		out = append(out, frame)
	}
	return out, nil
}

// frameFromWire renders the wire shape into the frame callers act on.
//
// It is the coordinator's ONE conversion in this direction, and it does not
// invent what the wire does not carry: completeness and revision are the
// runtime's and are set by the two readers that know them.
func frameFromWire(w proto.ScreenFrame) paneview.Frame {
	f := paneview.Frame{
		Cols:          w.Cols,
		Rows:          w.Rows,
		CursorX:       w.CursorX,
		CursorY:       w.CursorY,
		CursorVisible: w.CursorVisible,
		AltScreen:     w.AltScreen,
		Lines:         make([][]paneview.Cell, len(w.Lines)),
	}
	for y, line := range w.Lines {
		cells := make([]paneview.Cell, len(line))
		for x, c := range line {
			cells[x] = paneview.Cell{Text: c.Text, Width: c.Width}
		}
		f.Lines[y] = cells
	}
	return f
}

// completenessFromWire spells the wire's claim as the contract's, and THIS
// function is what makes an unrecognised spelling UNKNOWN — the state a write
// gate refuses in. It cannot be left to the decode: a named string type keeps
// whatever string it was handed, so a peer's invented claim would arrive at a
// caller looking like something the caller recognises. A claim nobody
// understands must never look stronger than it is.
func completenessFromWire(c proto.Completeness) sessionruntime.Completeness {
	switch c {
	case proto.CompletenessComplete:
		return sessionruntime.CompletenessComplete
	case proto.CompletenessLostIngest:
		return sessionruntime.CompletenessLostIngest
	case proto.CompletenessNoFence:
		return sessionruntime.CompletenessNoFence
	case proto.CompletenessEvicted:
		return sessionruntime.CompletenessEvicted
	case proto.CompletenessUnknown:
		return sessionruntime.CompletenessUnknown
	default:
		return sessionruntime.CompletenessUnknown
	}
}
