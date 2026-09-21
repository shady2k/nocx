// Package captureview derives a stored capture record's two views
// (nocx-2v80t.2.5): the SGR grid a restored card draws, and the plain text
// search and copy read. The record — ONE body per settled interval, stored
// by the helper capture path at the authenticated boundary — is the only
// input either view takes. Neither view re-reads a terminal, and neither is
// a second body someone stored beside the record: a view is a FUNCTION of
// the stored bytes, so changing the stored capture changes both, and a read
// after the pane is gone answers exactly what a read while it lived did.
//
// The SGR dialect is sgr.ts's (frontend/src/scrollback/sgr.ts): the same
// parameters, in the same order, with the same reset-and-reopen rule — a
// restored card must not change appearance because the body it draws began
// to be produced from records instead of from renderer freezes. The flags
// the dialect does not spell (invisible) and the facts it never carried
// (the underline colour, the underline SHAPE beyond its boolean) are left
// out exactly as the renderer's own writer left them out.
package captureview

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// Kind names which view an id addresses. The kinds are closed: a view is
// the card's grid or the searchable text, and anything else is not a view.
type Kind string

const (
	// KindVT is the application/vt view — the SGR grid a restored card draws.
	KindVT Kind = "vt"
	// KindText is the text/plain view — the derived text search and copy read.
	KindText Kind = "text"
)

// ViewID derives a view's artifact id from the record's. Deterministic, so
// the same record always answers at the same address and a retried view
// write is the store's own replay no-op. The shape is the artifact-id
// vocabulary's (a digest stamped version 4, RFC 9562 variant), the same
// shaping the unfinished record's id uses.
func ViewID(recordID string, kind Kind) string {
	sum := sha256.Sum256([]byte(recordID + "\x00" + string(kind)))
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// ParseRecord decodes a stored record body. The stored bytes are the wire
// bytes the helper sent — the same shape, decoded once, here.
func ParseRecord(body []byte) (*proto.CaptureParams, error) {
	var rec proto.CaptureParams
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("captureview: the stored record does not decode: %w", err)
	}
	return &rec, nil
}

// intervalRows is the whole interval in order: the rows that left the
// screen while the command ran, oldest first, then the screen as the
// boundary closed it. The opening screen is deliberately absent — it is
// what the screen looked like BEFORE the command drew, not output.
func intervalRows(rec *proto.CaptureParams) []proto.CaptureRow {
	rows := make([]proto.CaptureRow, 0, len(rec.Departed)+len(rec.Closing.Lines))
	rows = append(rows, rec.Departed...)
	rows = append(rows, rec.Closing.Lines...)
	return rows
}

// VTBody renders the record's interval as the SGR body a restored card
// draws: one physical row per line, styled through sgr.ts's dialect, every
// row closing what it opened so a reader starting mid-body wears nothing.
func VTBody(rec *proto.CaptureParams) string {
	rows := intervalRows(rec)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, rowVT(row))
	}
	return strings.Join(lines, "\n")
}

// TextBody renders the record's interval as the plain text search and copy
// read: the SAME rows as characters, with the wrap flags joining the
// physical rows of one logical line and keeping a hard newline a break —
// the reading the store's own readback test states. A word that scrolled
// away mid-interval is findable here, not only the boundary screen.
func TextBody(rec *proto.CaptureParams) string {
	rows := intervalRows(rec)
	var sb strings.Builder
	for i, row := range rows {
		text := rowText(row)
		if i > 0 && !row.Continuation {
			sb.WriteByte('\n')
		}
		sb.WriteString(text)
	}
	return sb.String()
}

// blank is whether a cell carries nothing a reader could see: no grapheme,
// or a grapheme that is only padding. Trailing blanks are the terminal's
// own rectangle padding, not output, and no view renders them.
func blank(c proto.CaptureCell) bool {
	return c.Width == 0 || c.Text == "" || strings.TrimSpace(c.Text) == ""
}

// visible returns the row's cells up to its last visible one.
func visible(cells []proto.CaptureCell) []proto.CaptureCell {
	last := -1
	for i, c := range cells {
		if !blank(c) {
			last = i
		}
	}
	return cells[:last+1]
}

func rowText(row proto.CaptureRow) string {
	var sb strings.Builder
	for _, c := range visible(row.Cells) {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// sgrAttrs is one run's style, in the vocabulary sgr.ts writes.
type sgrAttrs struct {
	fg *sgrColor
	bg *sgrColor

	bold          bool
	dim           bool
	italic        bool
	underline     bool
	blink         bool
	inverse       bool
	strikethrough bool
	overline      bool
}

var emptyAttrs sgrAttrs

func (a sgrAttrs) equal(b sgrAttrs) bool {
	return colorEqual(a.fg, b.fg) && colorEqual(a.bg, b.bg) &&
		a.bold == b.bold && a.dim == b.dim && a.italic == b.italic &&
		a.underline == b.underline && a.blink == b.blink && a.inverse == b.inverse &&
		a.strikethrough == b.strikethrough && a.overline == b.overline
}

func colorEqual(a, b *sgrColor) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sgrColor is a raw colour: a palette index or a packed 0xRRGGBB. Resolving
// it into somebody's theme is the reader's act, never the body's.
type sgrColor struct {
	palette bool
	value   int
}

func colorOf(c *proto.CaptureColor) *sgrColor {
	if c == nil {
		return nil
	}
	switch c.Kind {
	case "palette":
		return &sgrColor{palette: true, value: c.Palette}
	case "rgb":
		var r, g, b int
		if _, err := fmt.Sscanf(c.RGB, "#%02x%02x%02x", &r, &g, &b); err != nil {
			return nil
		}
		return &sgrColor{value: r<<16 | g<<8 | b}
	default:
		return nil
	}
}

// flagParams are the boolean attributes and the SGR parameter that turns
// each on, in sgr.ts's order.
var flagParams = []struct {
	on   func(sgrAttrs) bool
	code string
}{
	{func(a sgrAttrs) bool { return a.bold }, "1"},
	{func(a sgrAttrs) bool { return a.dim }, "2"},
	{func(a sgrAttrs) bool { return a.italic }, "3"},
	{func(a sgrAttrs) bool { return a.underline }, "4"},
	{func(a sgrAttrs) bool { return a.blink }, "5"},
	{func(a sgrAttrs) bool { return a.inverse }, "7"},
	{func(a sgrAttrs) bool { return a.strikethrough }, "9"},
	{func(a sgrAttrs) bool { return a.overline }, "53"},
}

func colorParams(c *sgrColor, base int) string {
	dflt := base + 9 // 39 foreground default, 49 background default
	if c == nil {
		return fmt.Sprintf("%d", dflt)
	}
	if c.palette {
		switch {
		case c.value < 8:
			return fmt.Sprintf("%d", base+c.value)
		case c.value < 16:
			return fmt.Sprintf("%d", base+60+(c.value-8))
		default:
			return fmt.Sprintf("%d;5;%d", base+8, c.value)
		}
	}
	return fmt.Sprintf("%d;2;%d;%d;%d", base+8, (c.value>>16)&0xff, (c.value>>8)&0xff, c.value&0xff)
}

// sgrParams is the sequence that turns prev into next, or "" when they are
// the same — sgr.ts's reset-and-reopen rule: one `0` plus what is still on,
// never a pile of off codes with a reader's unknown-code state to guess.
func sgrParams(prev, next sgrAttrs) string {
	if prev.equal(next) {
		return ""
	}
	var params []string
	turnedOff := false
	for _, f := range flagParams {
		if f.on(prev) && !f.on(next) {
			turnedOff = true
			break
		}
	}
	if turnedOff {
		params = append(params, "0")
		for _, f := range flagParams {
			if f.on(next) {
				params = append(params, f.code)
			}
		}
		if next.fg != nil {
			params = append(params, colorParams(next.fg, 30))
		}
		if next.bg != nil {
			params = append(params, colorParams(next.bg, 40))
		}
	} else {
		for _, f := range flagParams {
			if !f.on(prev) && f.on(next) {
				params = append(params, f.code)
			}
		}
		if !colorEqual(prev.fg, next.fg) {
			params = append(params, colorParams(next.fg, 30))
		}
		if !colorEqual(prev.bg, next.bg) {
			params = append(params, colorParams(next.bg, 40))
		}
	}
	if len(params) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(params, ";") + "m"
}

// cellAttrs maps a record cell's style onto the dialect's vocabulary. The
// underline SHAPE collapses to its boolean and the underline colour is
// left out — the facts the renderer's own writer never carried, dropped
// the same way rather than given a second spelling.
func cellAttrs(c proto.CaptureCell) sgrAttrs {
	const (
		bold          = 1
		italic        = 2
		faint         = 4
		blink         = 8
		inverse       = 16
		strikethrough = 64
		overline      = 128
	)
	a := sgrAttrs{
		fg:            colorOf(c.Fg),
		bg:            colorOf(c.Bg),
		bold:          c.Attrs&bold != 0,
		dim:           c.Attrs&faint != 0,
		italic:        c.Attrs&italic != 0,
		underline:     c.Underline > 0,
		blink:         c.Attrs&blink != 0,
		inverse:       c.Attrs&inverse != 0,
		strikethrough: c.Attrs&strikethrough != 0,
		overline:      c.Attrs&overline != 0,
	}
	return a
}

func rowVT(row proto.CaptureRow) string {
	cells := visible(row.Cells)
	var sb strings.Builder
	current := emptyAttrs
	for _, c := range cells {
		// A zero-width cell is a wide grapheme's continuation or a spacer:
		// it carries no text and never breaks the run it sits inside.
		if c.Width == 0 {
			continue
		}
		attrs := cellAttrs(c)
		if params := sgrParams(current, attrs); params != "" {
			sb.WriteString(params)
			current = attrs
		}
		sb.WriteString(c.Text)
	}
	if !current.equal(emptyAttrs) {
		sb.WriteString("\x1b[0m")
	}
	return sb.String()
}
