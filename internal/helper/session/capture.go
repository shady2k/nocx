package session

// The helper's half of the capture path (nocx-2v80t.2.2): the session runtime
// builds ONE record per settled execution interval, and this file hands it up
// to the coordinator on proto.OpCapture — a reverse ask on the SAME
// connection the session was spawned on, because the coordinator that spawned
// the session is the one whose ledger stores it.
//
// # Why the ask is relayed, and never inline
//
// The runtime hands records over AFTER its settle critical section, but on
// the caller's goroutine — the pump that feeds the emulator. An ask that
// waited on a network round trip would park the pump behind the coordinator,
// and a pane whose output stalls because history is slow is the exact defect
// the one-owner delivery rules exist to prevent. So Capture queues to a
// goroutine of its own, bounded by one ask timeout; a record that cannot be
// delivered is REPORTED and dropped, because a lost capture is a gap history
// can name, while a pane wedged behind an ask is a pane lost.

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// captureAskTimeout bounds ONE capture ask. The record has already been
// built; the ask is delivery, not the capture, and a coordinator that cannot
// answer in this window is a coordinator to report rather than wait on — the
// same scale the completion downlink bounds its one op with.
const captureAskTimeout = 5 * time.Second

// CoordinatorAsker is the connection a session pushes its capture records up
// through. host.Host is it in production; the connection is resolved at
// spawn, from the request the spawn arrived on, because a host serves several
// coordinators and "the coordinator" is a property of the request.
type CoordinatorAsker interface {
	Ask(ctx context.Context, service, op string, params, out any) error
}

// askerFromContext resolves the connection a request arrived on, or nil when
// this path had none (a test driving ops without a host behind them). A nil
// asker means records are reported and dropped — never a session that cannot
// run because history had nowhere to go.
func askerFromContext(ctx context.Context) CoordinatorAsker {
	if asker, ok := host.ConnectionFrom(ctx).(CoordinatorAsker); ok {
		return asker
	}
	return nil
}

// captureRelay is the sink the runtime is built with. It exists because the
// runtime is created before the hostSession that owns the session id and the
// connection — the runtime's Config needs its sink at New, and the session's
// identity is minted into the runtime itself. Bind happens before the pump
// starts, so no record can ever be built unbound.
type captureRelay struct {
	mu     sync.Mutex
	target captureSinkTarget
}

type captureSinkTarget interface {
	deliverCapture(sessionruntime.CaptureRecord)
}

func newCaptureRelay() *captureRelay { return &captureRelay{} }

// bind names the hostSession records are delivered through.
func (r *captureRelay) bind(t captureSinkTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target = t
}

// Capture implements sessionruntime.CaptureSink.
func (r *captureRelay) Capture(rec sessionruntime.CaptureRecord) {
	r.mu.Lock()
	t := r.target
	r.mu.Unlock()
	if t == nil {
		// Unreachable in production (Bind precedes the pump), and a record
		// nobody owns is dropped loudly rather than sent to nobody.
		panic("session: a capture record arrived before its session was bound")
	}
	t.deliverCapture(rec)
}

// deliverCapture maps the runtime's record onto the wire and sends it on its
// own goroutine. The mapping is the helper's ONE conversion in this
// direction, and it is total: every style fact the record carries crosses,
// because the record is the durable content the card's wire format derives
// views from.
func (hs *hostSession) deliverCapture(rec sessionruntime.CaptureRecord) {
	params := proto.CaptureParams{
		Session:      hs.id,
		Incarnation:  proto.Incarnation{Session: string(rec.At.Session), Generation: uint64(rec.At.Generation)},
		Nonce:        hex.EncodeToString(rec.Nonce[:]),
		Revision:     uint64(rec.Revision),
		Completeness: completenessName(rec.Completeness),
		Opening:      captureScreenOf(rec.Opening),
		Departed:     captureRowsOf(rec.Departed),
		DepartedHole: rec.DepartedHole,
		Closing:      captureScreenOf(rec.Closing),
	}
	go hs.sendCapture(params)
}

// sendCapture performs one ask and reports the answer. The ack is the
// coordinator's storage answer — kept, or why nothing was kept — and it is
// LOGGED either way: the helper built the record, and the person who owns
// this machine's helper deserves to know what became of it, but no code path
// here can do more with it than report.
func (hs *hostSession) sendCapture(params proto.CaptureParams) {
	hs.mu.Lock()
	asker := hs.captureAsk
	hs.mu.Unlock()
	if asker == nil {
		hs.log.Warn("helper: a capture record had no coordinator connection to travel on",
			"session", hs.id.Session)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), captureAskTimeout)
	defer cancel()
	var result proto.CaptureResult
	if err := asker.Ask(ctx, proto.ServiceSession, proto.OpCapture, params, &result); err != nil {
		hs.log.Warn("helper: the capture record did not reach the coordinator",
			"session", hs.id.Session, "nonce", params.Nonce, "error", err)
		return
	}
	if result.Kept {
		hs.log.Info("helper: the capture record was stored",
			"session", hs.id.Session, "nonce", params.Nonce)
		return
	}
	hs.log.Info("helper: the capture record was not kept",
		"session", hs.id.Session, "nonce", params.Nonce, "reason", result.Reason)
}

// captureScreenOf maps one instant of a screen onto the wire.
func captureScreenOf(s sessionruntime.CaptureScreen) proto.CaptureScreen {
	return proto.CaptureScreen{
		Cols:          s.Cols,
		Rows:          s.Rows,
		AltScreen:     s.AltScreen,
		CursorX:       s.Cursor.X,
		CursorY:       s.Cursor.Y,
		CursorVisible: s.Cursor.Visible,
		Lines:         captureRowsOf(s.Lines),
	}
}

// captureRowsOf maps rows onto the wire, wrap flags and style verbatim.
func captureRowsOf(rows []emulator.Row) []proto.CaptureRow {
	out := make([]proto.CaptureRow, len(rows))
	for i, row := range rows {
		cells := make([]proto.CaptureCell, len(row.Cells))
		for j, c := range row.Cells {
			cells[j] = proto.CaptureCell{
				Text:           c.Grapheme,
				Width:          captureWidthOf(c.Width),
				HasText:        c.HasText,
				Fg:             captureColorOf(c.Style.Foreground),
				Bg:             captureColorOf(c.Style.Background),
				UnderlineColor: captureColorOf(c.Style.UnderlineColor),
				Attrs:          int(c.Style.Attributes),
				Underline:      int(c.Style.Underline),
			}
		}
		out[i] = proto.CaptureRow{Wrap: row.Wrap, Continuation: row.Continuation, Cells: cells}
	}
	return out
}

// captureWidthOf maps the emulator's width class onto the column count a
// consumer reads: both spacer classes are zero — they carry no text and are
// not rendered — and an unknown class renders as nothing rather than as a
// guess, the same reading paneview's cellWidth states.
func captureWidthOf(w emulator.Width) int {
	switch w {
	case emulator.WidthWide:
		return 2
	case emulator.WidthNarrow:
		return 1
	default:
		return 0
	}
}

// captureColorOf maps the emulator's three colour shapes onto the wire,
// kinds kept apart because they mean different things: a palette index means
// "this theme's colour N" and repaints when the theme changes, an RGB value
// means "exactly this" and does not, and a default colour means the theme's
// own choice for the role. A default colour crosses as nil — the absence of
// a claim — because the emulator's zero colour and "no colour" are the same
// fact.
func captureColorOf(c emulator.Color) *proto.CaptureColor {
	switch c.Kind {
	case emulator.ColorRGB:
		return &proto.CaptureColor{Kind: "rgb", RGB: fmt.Sprintf("#%02x%02x%02x", c.RGB.R, c.RGB.G, c.RGB.B)}
	case emulator.ColorPalette:
		return &proto.CaptureColor{Kind: "palette", Palette: int(c.Palette)}
	default:
		return nil
	}
}
