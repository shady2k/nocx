package client

// The coordinator's side of the streamed block output (nocx-2v80t.3.6): the
// rows a session's helper streams as they leave the screen, and the
// interval end markers that close them, decoded here into the Go shapes the
// coordinator's history consumes. This file is the seam both halves of the
// epic agreed on: OutputRows and IntervalEnd are Go types carrying
// emulator rows — the same vocabulary the frames carry — and nothing above
// this boundary sees a wire shape.
//
// Delivery is in stream order, from the connection's own read loop, exactly
// as OnOutputHole's is: the coordinator's block for a running command is
// appended by the same goroutine that reads the wire, so a row can never be
// attributed to the wrong interval by a delivery race. An observer must
// therefore not block — it appends and returns, as the hole observer does.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// OutputRows is one batch of the rows that left a session's screen, oldest
// first, exactly as the helper handed them over. FromRow is the absolute
// index of Rows[0]: the count of rows the session had seen depart before
// this batch, whether or not a consumer was bound when they did. LostRows is
// the exact gap between FromRow and the row count after the delivery before
// it: the FEEDS the emulator struck immediately before FromRow (a symbolic
// one per struck feed — the ABI cannot count the rows a prune actually took;
// the helper contract spells the whole argument out) plus any rows the
// helper's own bridge had to drop under backpressure before this batch ever
// left it (an exact row count there, nocx-2v80t.3.15). An ordinary batch,
// with a keeping-up wire and no struck feed, carries none.
type OutputRows struct {
	FromRow  uint64
	Rows     []emulator.Row
	LostRows uint64
}

// IntervalEnd closes the interval the nonce names, after every row that
// belongs to it. EndRow is the absolute index one PAST the interval's last
// departed row — rows at it and above streamed after the boundary was taken
// and belong to the interval that follows, which is how a fence split keeps
// a post-sighting row out of the fenced command. Closing is the screen as
// the boundary sat on it, and nil when the screen could not be read: a
// screen nobody read is not a screen nobody may be told about.
type IntervalEnd struct {
	Nonce   sessionruntime.FenceNonce
	EndRow  uint64
	Closing []emulator.Row
}

// OnOutputRows registers the coordinator's consumer for this session's
// streamed rows. Nil means nobody is watching: the batches are then dropped
// at this door, on the same terms OnScreenFrame drops unwatched screens.
func (a *AttachedSession) OnOutputRows(f func(OutputRows)) {
	a.mu.Lock()
	a.outputRowsObs = f
	a.mu.Unlock()
}

// OnIntervalEnd registers the coordinator's consumer for this session's
// interval end markers, on the same ordered stream OnOutputRows registers
// for — the two callbacks fire from the same read loop in wire order, which
// is what makes the ordering between a row and the end marker that closes
// its interval observable.
func (a *AttachedSession) OnIntervalEnd(f func(IntervalEnd)) {
	a.mu.Lock()
	a.intervalEndObs = f
	a.mu.Unlock()
}

// ConfirmWritten advances the helper's confirmed-written mark to upToRow:
// the coordinator's acknowledgement that every row and end marker through
// this absolute index is written where it must survive. The helper keeps no
// copy of departed rows — ghostty's scrollback is the buffer — so this mark
// is what the helper's eventual resend (the epic's next step) reads the
// scrollback against. A mark ahead of what the session departed is refused
// by name.
func (a *AttachedSession) ConfirmWritten(ctx context.Context, upToRow uint64) error {
	return a.client.Call(ctx, proto.ServiceSession, proto.OpConfirmRows,
		proto.ConfirmRowsParams{
			Subscriber: proto.SubscriberID(hex.EncodeToString(a.subscriber[:])),
			Session:    proto.HostSessionID{Generation: a.generation, Session: proto.SessionHex(a.session)},
			UpToRow:    upToRow,
		}, nil)
}

// deliverOutputRows hands one decoded batch to the observer.
func (a *AttachedSession) deliverOutputRows(rows OutputRows) {
	a.mu.Lock()
	obs := a.outputRowsObs
	a.mu.Unlock()
	if obs == nil {
		return
	}
	obs(rows)
}

// deliverIntervalEnd hands one decoded end marker to the observer.
func (a *AttachedSession) deliverIntervalEnd(end IntervalEnd) {
	a.mu.Lock()
	obs := a.intervalEndObs
	a.mu.Unlock()
	if obs == nil {
		return
	}
	obs(end)
}

// outputRows is one TypeOutputRows frame arriving: decode, find the
// attachment the frame names, deliver. A frame whose attachment is gone is
// dropped with a log line — the rows belonged to a reader that left, the
// same rule sessionData applies to bytes.
func (c *Client) outputRows(payload []byte) {
	f, err := proto.DecodeOutputRowsFrame(payload)
	if err != nil {
		c.log.Warn("malformed rows frame", "err", err, "bytes", len(payload))
		return
	}
	var doc proto.OutputRowsDoc
	if uerr := json.Unmarshal(f.Payload, &doc); uerr != nil {
		c.log.Warn("malformed rows document", "err", uerr,
			"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber))
		return
	}
	rows, err := decodeWireRows(doc.Rows)
	if err != nil {
		c.log.Warn("malformed rows in a rows document", "err", err,
			"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber))
		return
	}
	c.mu.Lock()
	a := c.attachments[f.Subscriber]
	c.mu.Unlock()
	if a == nil || a.session != f.Session {
		c.log.Warn("rows frame dropped: no matching attachment",
			"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber))
		return
	}
	a.deliverOutputRows(OutputRows{FromRow: doc.FromRow, Rows: rows, LostRows: doc.LostRows})
}

// intervalEnd is one TypeIntervalEnd frame arriving, on the same terms the
// rows frame is.
func (c *Client) intervalEnd(payload []byte) {
	f, err := proto.DecodeIntervalEndFrame(payload)
	if err != nil {
		c.log.Warn("malformed interval end frame", "err", err, "bytes", len(payload))
		return
	}
	var doc proto.IntervalEndDoc
	if uerr := json.Unmarshal(f.Payload, &doc); uerr != nil {
		c.log.Warn("malformed interval end document", "err", uerr,
			"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber))
		return
	}
	var nonce sessionruntime.FenceNonce
	raw, err := hex.DecodeString(doc.Nonce)
	if err != nil || len(raw) != len(nonce) {
		c.log.Warn("malformed nonce in an interval end document",
			"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber))
		return
	}
	copy(nonce[:], raw)
	var closing []emulator.Row
	if doc.Closing != nil {
		closing, err = decodeWireRows(doc.Closing)
		if err != nil {
			c.log.Warn("malformed closing rows in an interval end document", "err", err,
				"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber))
			return
		}
	}
	c.mu.Lock()
	a := c.attachments[f.Subscriber]
	c.mu.Unlock()
	if a == nil || a.session != f.Session {
		c.log.Warn("interval end dropped: no matching attachment",
			"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber))
		return
	}
	a.deliverIntervalEnd(IntervalEnd{Nonce: nonce, EndRow: doc.EndRow, Closing: closing})
}

// ErrShortNonce names a nonce that does not spell 32 bytes. It exists so the
// decode errors say what was wrong with the bytes rather than only that
// something was.
var ErrShortNonce = errors.New("client: the nonce does not spell 32 bytes")

// The wire row's shape, as session.frame declares it (nocx-zg3k3.2.12): the
// row's text, a sparse list of the positions whose codepoint count or
// column width departs from the default, and style runs measured in
// columns. The Go encoder is sessionruntime's (and internal/content's
// mirror for the stored form); this is the decode half, and the schema is
// what keeps every side one vocabulary.
type wireRow struct {
	Text         string   `json:"text"`
	Marks        [][3]int `json:"marks"`
	Runs         [][2]any `json:"runs"`
	Wrap         bool     `json:"wrap"`
	Continuation bool     `json:"continuation"`
}

// decodeWireRows turns the frame contract's rows array into the emulator
// rows the coordinator's history stores. Positions are derived from the
// row's own text and marks (never from a column count nobody sends here —
// a departed row carries no frame geometry to pad against), and a wide
// mark's spacer is synthesised back in so the result is exactly the
// column-indexed shape sessionruntime.EncodeRows read to build the wire in
// the first place.
func decodeWireRows(raw json.RawMessage) ([]emulator.Row, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var wires []wireRow
	if err := json.Unmarshal(raw, &wires); err != nil {
		return nil, err
	}
	out := make([]emulator.Row, 0, len(wires))
	for i, w := range wires {
		cells, err := decodeWireRowCells(w)
		if err != nil {
			return nil, fmt.Errorf("client: row %d: %w", i, err)
		}
		out = append(out, emulator.Row{Cells: cells, Wrap: w.Wrap, Continuation: w.Continuation})
	}
	return out, nil
}

// decodeWireRowCells rebuilds one row's column-indexed cells from its
// text+marks+runs. `positions` is derived by arithmetic rather than by
// walking text until it runs out: a trailing position whose mark declares
// zero codepoints (a styled or spacer-head blank at the row's own end,
// which the trimming rule at the source does not omit) consumes nothing
// from text, so exhausting text is not a valid stopping rule.
func decodeWireRowCells(w wireRow) ([]emulator.Cell, error) {
	codepoints := []rune(w.Text)
	byPos := make(map[int][2]int, len(w.Marks))
	explicitCodepoints := 0
	for _, m := range w.Marks {
		if len(m) != 3 {
			return nil, fmt.Errorf("a mark is not a [position, codepoints, width] triple")
		}
		if _, dup := byPos[m[0]]; dup {
			return nil, fmt.Errorf("two marks name position %d", m[0])
		}
		byPos[m[0]] = [2]int{m[1], m[2]}
		explicitCodepoints += m[1]
	}
	positions := len(codepoints) - explicitCodepoints + len(w.Marks)
	if positions < 0 {
		return nil, fmt.Errorf("marks claim more codepoints than the row's text has")
	}

	var runStyle emulator.Style
	runLeft := 0
	runIdx := 0
	nextStyle := func() error {
		if runIdx >= len(w.Runs) {
			if runIdx == 0 && len(w.Runs) == 0 {
				// No runs at all: the implicit single default run, covering
				// however many columns this row turns out to need.
				runStyle = emulator.Style{}
				runLeft = int(^uint(0) >> 1) // the rest of the row, whatever that is
				return nil
			}
			return fmt.Errorf("a row's runs do not cover its columns")
		}
		pair := w.Runs[runIdx]
		runIdx++
		lengthF, ok := pair[1].(float64)
		if !ok {
			return fmt.Errorf("a run's length is not a number")
		}
		runLeft = int(lengthF)
		style, ok := decodeWireStyle(pair[0])
		if !ok {
			return fmt.Errorf("a run's style does not decode")
		}
		runStyle = style
		return nil
	}

	cells := make([]emulator.Cell, 0, positions)
	idx := 0
	for pos := 0; pos < positions; pos++ {
		span, width := 1, 1
		if m, ok := byPos[pos]; ok {
			span, width = m[0], m[1]
		}
		if idx+span > len(codepoints) {
			return nil, fmt.Errorf("position %d needs %d codepoints past the row's text", pos, span)
		}
		grapheme := string(codepoints[idx : idx+span])
		idx += span
		hasText := span > 0
		if !hasText {
			grapheme = ""
		}
		columns := 1
		if width == int(emulator.WidthWide) {
			columns = 2
		}
		for c := 0; c < columns; c++ {
			if runLeft == 0 {
				if err := nextStyle(); err != nil {
					return nil, err
				}
			}
			runLeft--
			if c == 0 {
				cells = append(cells, emulator.Cell{
					Grapheme: grapheme,
					Width:    emulator.Width(width), // #nosec G115 -- the contract bounds width to {1,2,4}
					HasText:  hasText,
					Style:    runStyle,
				})
			} else {
				cells = append(cells, emulator.Cell{Width: emulator.WidthSpacerTail, Style: runStyle})
			}
		}
	}
	return cells, nil
}

// decodeWireStyle turns one run's style value into the emulator's Style:
// the bare integer 0 (the all-default style) or the 5-element tuple
// [foreground, background, underlineColor, attributes, underline].
func decodeWireStyle(v any) (emulator.Style, bool) {
	if f, ok := v.(float64); ok {
		if f != 0 {
			return emulator.Style{}, false
		}
		return emulator.Style{}, true
	}
	tuple, ok := v.([]any)
	if !ok || len(tuple) != 5 {
		return emulator.Style{}, false
	}
	fg, ok := decodeWireColorValue(tuple[0])
	if !ok {
		return emulator.Style{}, false
	}
	bg, ok := decodeWireColorValue(tuple[1])
	if !ok {
		return emulator.Style{}, false
	}
	ul, ok := decodeWireColorValue(tuple[2])
	if !ok {
		return emulator.Style{}, false
	}
	attrs, ok := tuple[3].(float64)
	if !ok {
		return emulator.Style{}, false
	}
	underline, ok := tuple[4].(float64)
	if !ok {
		return emulator.Style{}, false
	}
	return emulator.Style{
		Foreground:     fg,
		Background:     bg,
		UnderlineColor: ul,
		Attributes:     emulator.Attributes(attrs),    // #nosec G115 -- the contract bounds the attributes bitset to 16 bits
		Underline:      emulator.Underline(underline), // #nosec G115 -- the contract bounds the underline enum to 8 bits
	}, true
}

// decodeWireColorValue unpacks one colour's packed integer (session.frame's
// $defs/color): 0 default, 1-256 a palette index plus one, 257+ a packed
// RGB triple offset by 257 — the same three ranges
// internal/sessionruntime's encodeColorValue writes.
func decodeWireColorValue(v any) (emulator.Color, bool) {
	f, ok := v.(float64)
	if !ok {
		return emulator.Color{}, false
	}
	n := int(f)
	switch {
	case n == 0:
		return emulator.Color{Kind: emulator.ColorDefault}, true
	case n < 257:
		return emulator.Color{Kind: emulator.ColorPalette, Palette: uint8(n - 1)}, true // #nosec G115 -- bounded to 0-255 by the range check
	default:
		packed := n - 257
		return emulator.Color{
			Kind: emulator.ColorRGB,
			RGB: emulator.RGB{
				R: uint8(packed >> 16 & 0xFF), // #nosec G115 -- masked to one byte
				G: uint8(packed >> 8 & 0xFF),  // #nosec G115 -- masked to one byte
				B: uint8(packed & 0xFF),       // #nosec G115 -- masked to one byte
			},
		}, true
	}
}
