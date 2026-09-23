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
// first, exactly as the runtime handed them over. FromRow is the absolute
// index of Rows[0]: the count of rows the session had seen depart before
// this batch, whether or not a consumer was bound when they did. LostRows
// counts the FEEDS the emulator struck immediately before FromRow — never
// rows, because the ABI cannot count pruned rows (the helper contract spells
// the whole argument out); an ordinary batch carries none.
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

// The wire row's shape, as session.frame declares it: cells as the tuples
// the contract's measurement chose, styles as per-row runs. The Go encoder
// is sessionruntime's; this is the decode half, and the schema is what
// keeps the two one vocabulary.
type wireRow struct {
	Cells        [][3]any `json:"cells"`
	Runs         [][2]any `json:"runs"`
	Wrap         bool     `json:"wrap"`
	Continuation bool     `json:"continuation"`
}

type wireStyle struct {
	Foreground     wireColor `json:"foreground"`
	Background     wireColor `json:"background"`
	UnderlineColor wireColor `json:"underlineColor"`
	Attributes     int       `json:"attributes"`
	Underline      int       `json:"underline"`
}

type wireColor struct {
	Kind    int     `json:"kind"`
	Palette int     `json:"palette"`
	RGB     wireRGB `json:"rgb"`
}

type wireRGB struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// decodeWireRows turns the frame contract's rows array into the emulator
// rows the coordinator's history stores. The runs walk is the encoder's
// mirror: runs are adjacent, in order, and partition the row's cells
// exactly, so each cell's style comes from the run covering it.
func decodeWireRows(raw json.RawMessage) ([]emulator.Row, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var wires []wireRow
	if err := json.Unmarshal(raw, &wires); err != nil {
		return nil, err
	}
	out := make([]emulator.Row, 0, len(wires))
	for _, w := range wires {
		row := emulator.Row{
			Cells:        make([]emulator.Cell, 0, len(w.Cells)),
			Wrap:         w.Wrap,
			Continuation: w.Continuation,
		}
		var style emulator.Style
		runLeft := 0
		runIdx := 0
		for _, tuple := range w.Cells {
			if len(tuple) != 3 {
				return nil, fmt.Errorf("client: a cell is not a [grapheme, width, hasText] tuple")
			}
			grapheme, _ := tuple[0].(string)
			widthF, _ := tuple[1].(float64)
			hasText, _ := tuple[2].(bool)
			if runLeft == 0 {
				if runIdx >= len(w.Runs) {
					return nil, fmt.Errorf("client: a row's runs do not cover its cells")
				}
				pair := w.Runs[runIdx]
				runIdx++
				lengthF, ok := pair[1].(float64)
				if !ok {
					return nil, fmt.Errorf("client: a run's length is not a number")
				}
				runLeft = int(lengthF)
				style, ok = decodeWireStyle(pair[0])
				if !ok {
					return nil, fmt.Errorf("client: a run's style does not decode")
				}
			}
			runLeft--
			row.Cells = append(row.Cells, emulator.Cell{
				Grapheme: grapheme,
				Width:    emulator.Width(widthF),
				HasText:  hasText,
				Style:    style,
			})
		}
		out = append(out, row)
	}
	return out, nil
}

// decodeWireStyle turns one run's style object into the emulator's Style.
// The integers are the same enumerations both sides read from
// internal/emulator, passed through in the values the encoder wrote.
func decodeWireStyle(v any) (emulator.Style, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return emulator.Style{}, false
	}
	var w wireStyle
	if err := json.Unmarshal(raw, &w); err != nil {
		return emulator.Style{}, false
	}
	return emulator.Style{
		Foreground:     decodeWireColor(w.Foreground),
		Background:     decodeWireColor(w.Background),
		UnderlineColor: decodeWireColor(w.UnderlineColor),
		// The wire spells each enumeration as the plain integer the
		// emulator declares; the conversions narrow to the same bit
		// widths the contract's enums bound.
		Attributes: emulator.Attributes(w.Attributes), // #nosec G115 -- the contract bounds the attributes bitset to 16 bits
		Underline:  emulator.Underline(w.Underline),   // #nosec G115 -- the contract bounds the underline enum to 8 bits
	}, true
}

func decodeWireColor(w wireColor) emulator.Color {
	r, g, b := uint8(w.RGB.R), uint8(w.RGB.G), uint8(w.RGB.B) // #nosec G115 -- the wire's colours are one byte per channel by contract
	return emulator.Color{
		Kind:    emulator.ColorKind(w.Kind), // #nosec G115 -- the contract bounds the colour-kind enum to 8 bits
		Palette: uint8(w.Palette),           // #nosec G115 -- the wire's palette index is 0-255 by contract
		RGB:     emulator.RGB{R: r, G: g, B: b},
	}
}
