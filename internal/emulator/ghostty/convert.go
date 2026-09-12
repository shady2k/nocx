package ghostty

/*
#include "bridge.h"
*/
import "C"

import (
	"fmt"

	"github.com/shady2k/nocx/internal/emulator"
)

// This file is the boundary, and ADR-0065 point 3 is the rule it follows: the
// conversions are EXPLICIT and keyed on the header's own NAMES. Nothing here
// casts an upstream value into a port value, because a cast is correct only by
// the coincidence that two enumerations agree today — and the spike that
// preceded this package did exactly that with Key, Mods and CellWidth, which is
// what the bead exists to undo.
//
// Where a value has no name in the port, the conversion says so and the caller
// decides: an unrecognized width, colour tag, underline or key is a port that
// does not know the answer, not an answer.

// cellWidth converts the upstream width class.
func cellWidth(w C.GhosttyCellWide) emulator.Width {
	switch w {
	case C.GHOSTTY_CELL_WIDE_NARROW:
		return emulator.WidthNarrow
	case C.GHOSTTY_CELL_WIDE_WIDE:
		return emulator.WidthWide
	case C.GHOSTTY_CELL_WIDE_SPACER_TAIL:
		return emulator.WidthSpacerTail
	case C.GHOSTTY_CELL_WIDE_SPACER_HEAD:
		return emulator.WidthSpacerHead
	default:
		return emulator.WidthUnknown
	}
}

// colorKind converts a colour tag. ok is false for a tag this port has no
// kind for, which is an upstream change rather than a colour.
func colorKind(tag C.GhosttyStyleColorTag) (emulator.ColorKind, bool) {
	switch tag {
	case C.GHOSTTY_STYLE_COLOR_NONE:
		return emulator.ColorDefault, true
	case C.GHOSTTY_STYLE_COLOR_PALETTE:
		return emulator.ColorPalette, true
	case C.GHOSTTY_STYLE_COLOR_RGB:
		return emulator.ColorRGB, true
	default:
		return emulator.ColorDefault, false
	}
}

// colorOf converts one style colour. The palette index and the RGB triple are
// read only in the shape its tag names: a struct that carried both would invite
// a consumer to read the one that is zero.
func colorOf(tag C.GhosttyStyleColorTag, palette C.GhosttyColorPaletteIndex,
	rgb C.GhosttyColorRgb,
) (emulator.Color, bool) {
	kind, ok := colorKind(tag)
	if !ok {
		return emulator.Color{}, false
	}
	out := emulator.Color{Kind: kind}
	switch kind {
	case emulator.ColorPalette:
		out.Palette = uint8(palette)
	case emulator.ColorRGB:
		out.RGB = emulator.RGB{R: uint8(rgb.r), G: uint8(rgb.g), B: uint8(rgb.b)}
	}
	return out, true
}

// underlineOf converts the underline shape.
func underlineOf(u C.GhosttySgrUnderline) (emulator.Underline, bool) {
	switch u {
	case C.GHOSTTY_SGR_UNDERLINE_NONE:
		return emulator.UnderlineNone, true
	case C.GHOSTTY_SGR_UNDERLINE_SINGLE:
		return emulator.UnderlineSingle, true
	case C.GHOSTTY_SGR_UNDERLINE_DOUBLE:
		return emulator.UnderlineDouble, true
	case C.GHOSTTY_SGR_UNDERLINE_CURLY:
		return emulator.UnderlineCurly, true
	case C.GHOSTTY_SGR_UNDERLINE_DOTTED:
		return emulator.UnderlineDotted, true
	case C.GHOSTTY_SGR_UNDERLINE_DASHED:
		return emulator.UnderlineDashed, true
	default:
		return emulator.UnderlineNone, false
	}
}

// styleOf converts one cell's whole style.
//
// The upstream style is eight separate booleans and the port's is a bitset;
// assembling it HERE, one named field at a time, is what keeps the port's bit
// numbering out of the C shim. A shim that produced the bitmask would be a
// second place that knows which bit is bold.
func styleOf(f C.nocxStyleFacts) (emulator.Style, error) {
	fg, ok := colorOf(f.fg_tag, f.fg_palette, f.fg_rgb)
	if !ok {
		return emulator.Style{}, fmt.Errorf("ghostty: foreground tag %d: %w", int32(f.fg_tag), emulator.ErrUnsupported)
	}
	bg, ok := colorOf(f.bg_tag, f.bg_palette, f.bg_rgb)
	if !ok {
		return emulator.Style{}, fmt.Errorf("ghostty: background tag %d: %w", int32(f.bg_tag), emulator.ErrUnsupported)
	}
	ul, ok := colorOf(f.ul_tag, f.ul_palette, f.ul_rgb)
	if !ok {
		return emulator.Style{}, fmt.Errorf("ghostty: underline colour tag %d: %w", int32(f.ul_tag), emulator.ErrUnsupported)
	}
	underline, ok := underlineOf(f.underline)
	if !ok {
		return emulator.Style{}, fmt.Errorf("ghostty: underline %d: %w", int32(f.underline), emulator.ErrUnsupported)
	}
	return emulator.Style{
		Foreground:     fg,
		Background:     bg,
		UnderlineColor: ul,
		Underline:      underline,
		Attributes:     attrsOf(f),
	}, nil
}

func attrsOf(f C.nocxStyleFacts) emulator.Attributes {
	var a emulator.Attributes
	if f.bold {
		a |= emulator.AttrBold
	}
	if f.italic {
		a |= emulator.AttrItalic
	}
	if f.faint {
		a |= emulator.AttrFaint
	}
	if f.blink {
		a |= emulator.AttrBlink
	}
	if f.inverse {
		a |= emulator.AttrInverse
	}
	if f.invisible {
		a |= emulator.AttrInvisible
	}
	if f.strikethrough {
		a |= emulator.AttrStrikethrough
	}
	if f.overline {
		a |= emulator.AttrOverline
	}
	return a
}

// cKey converts a port key identity. ok is false for a key this package has no
// upstream name for, which the caller reports as [emulator.ErrUnsupported]
// rather than encoding as something else.
func cKey(k emulator.Key) (C.GhosttyKey, bool) {
	switch k {
	case emulator.KeyLeft:
		return C.GHOSTTY_KEY_ARROW_LEFT, true
	case emulator.KeyRight:
		return C.GHOSTTY_KEY_ARROW_RIGHT, true
	case emulator.KeyUp:
		return C.GHOSTTY_KEY_ARROW_UP, true
	case emulator.KeyDown:
		return C.GHOSTTY_KEY_ARROW_DOWN, true
	case emulator.KeyHome:
		return C.GHOSTTY_KEY_HOME, true
	case emulator.KeyEnd:
		return C.GHOSTTY_KEY_END, true
	case emulator.KeyPageUp:
		return C.GHOSTTY_KEY_PAGE_UP, true
	case emulator.KeyPageDown:
		return C.GHOSTTY_KEY_PAGE_DOWN, true
	case emulator.KeyInsert:
		return C.GHOSTTY_KEY_INSERT, true
	case emulator.KeyDelete:
		return C.GHOSTTY_KEY_DELETE, true
	case emulator.KeyBackspace:
		return C.GHOSTTY_KEY_BACKSPACE, true
	case emulator.KeyEnter:
		return C.GHOSTTY_KEY_ENTER, true
	case emulator.KeyTab:
		return C.GHOSTTY_KEY_TAB, true
	case emulator.KeyEscape:
		return C.GHOSTTY_KEY_ESCAPE, true
	case emulator.KeySpace:
		return C.GHOSTTY_KEY_SPACE, true

	case emulator.KeyA:
		return C.GHOSTTY_KEY_A, true
	case emulator.KeyB:
		return C.GHOSTTY_KEY_B, true
	case emulator.KeyC:
		return C.GHOSTTY_KEY_C, true
	case emulator.KeyD:
		return C.GHOSTTY_KEY_D, true
	case emulator.KeyE:
		return C.GHOSTTY_KEY_E, true
	case emulator.KeyF:
		return C.GHOSTTY_KEY_F, true
	case emulator.KeyG:
		return C.GHOSTTY_KEY_G, true
	case emulator.KeyH:
		return C.GHOSTTY_KEY_H, true
	case emulator.KeyI:
		return C.GHOSTTY_KEY_I, true
	case emulator.KeyJ:
		return C.GHOSTTY_KEY_J, true
	case emulator.KeyK:
		return C.GHOSTTY_KEY_K, true
	case emulator.KeyL:
		return C.GHOSTTY_KEY_L, true
	case emulator.KeyM:
		return C.GHOSTTY_KEY_M, true
	case emulator.KeyN:
		return C.GHOSTTY_KEY_N, true
	case emulator.KeyO:
		return C.GHOSTTY_KEY_O, true
	case emulator.KeyP:
		return C.GHOSTTY_KEY_P, true
	case emulator.KeyQ:
		return C.GHOSTTY_KEY_Q, true
	case emulator.KeyR:
		return C.GHOSTTY_KEY_R, true
	case emulator.KeyS:
		return C.GHOSTTY_KEY_S, true
	case emulator.KeyT:
		return C.GHOSTTY_KEY_T, true
	case emulator.KeyU:
		return C.GHOSTTY_KEY_U, true
	case emulator.KeyV:
		return C.GHOSTTY_KEY_V, true
	case emulator.KeyW:
		return C.GHOSTTY_KEY_W, true
	case emulator.KeyX:
		return C.GHOSTTY_KEY_X, true
	case emulator.KeyY:
		return C.GHOSTTY_KEY_Y, true
	case emulator.KeyZ:
		return C.GHOSTTY_KEY_Z, true

	case emulator.KeyDigit0:
		return C.GHOSTTY_KEY_DIGIT_0, true
	case emulator.KeyDigit1:
		return C.GHOSTTY_KEY_DIGIT_1, true
	case emulator.KeyDigit2:
		return C.GHOSTTY_KEY_DIGIT_2, true
	case emulator.KeyDigit3:
		return C.GHOSTTY_KEY_DIGIT_3, true
	case emulator.KeyDigit4:
		return C.GHOSTTY_KEY_DIGIT_4, true
	case emulator.KeyDigit5:
		return C.GHOSTTY_KEY_DIGIT_5, true
	case emulator.KeyDigit6:
		return C.GHOSTTY_KEY_DIGIT_6, true
	case emulator.KeyDigit7:
		return C.GHOSTTY_KEY_DIGIT_7, true
	case emulator.KeyDigit8:
		return C.GHOSTTY_KEY_DIGIT_8, true
	case emulator.KeyDigit9:
		return C.GHOSTTY_KEY_DIGIT_9, true

	case emulator.KeyBackquote:
		return C.GHOSTTY_KEY_BACKQUOTE, true
	case emulator.KeyBackslash:
		return C.GHOSTTY_KEY_BACKSLASH, true
	case emulator.KeyBracketLeft:
		return C.GHOSTTY_KEY_BRACKET_LEFT, true
	case emulator.KeyBracketRight:
		return C.GHOSTTY_KEY_BRACKET_RIGHT, true
	case emulator.KeyComma:
		return C.GHOSTTY_KEY_COMMA, true
	case emulator.KeyEqual:
		return C.GHOSTTY_KEY_EQUAL, true
	case emulator.KeyMinus:
		return C.GHOSTTY_KEY_MINUS, true
	case emulator.KeyPeriod:
		return C.GHOSTTY_KEY_PERIOD, true
	case emulator.KeyQuote:
		return C.GHOSTTY_KEY_QUOTE, true
	case emulator.KeySemicolon:
		return C.GHOSTTY_KEY_SEMICOLON, true
	case emulator.KeySlash:
		return C.GHOSTTY_KEY_SLASH, true

	case emulator.KeyF1:
		return C.GHOSTTY_KEY_F1, true
	case emulator.KeyF2:
		return C.GHOSTTY_KEY_F2, true
	case emulator.KeyF3:
		return C.GHOSTTY_KEY_F3, true
	case emulator.KeyF4:
		return C.GHOSTTY_KEY_F4, true
	case emulator.KeyF5:
		return C.GHOSTTY_KEY_F5, true
	case emulator.KeyF6:
		return C.GHOSTTY_KEY_F6, true
	case emulator.KeyF7:
		return C.GHOSTTY_KEY_F7, true
	case emulator.KeyF8:
		return C.GHOSTTY_KEY_F8, true
	case emulator.KeyF9:
		return C.GHOSTTY_KEY_F9, true
	case emulator.KeyF10:
		return C.GHOSTTY_KEY_F10, true
	case emulator.KeyF11:
		return C.GHOSTTY_KEY_F11, true
	case emulator.KeyF12:
		return C.GHOSTTY_KEY_F12, true

	default:
		return 0, false
	}
}

// cMods converts the port's modifier state. The upstream bits are joined BY
// NAME: the port carries four booleans precisely so that no bitmask of another
// library's design has to be mirrored, and this is the one place the two meet.
func cMods(m emulator.Mods) C.GhosttyMods {
	var out C.GhosttyMods
	if m.Shift {
		out |= C.GHOSTTY_MODS_SHIFT
	}
	if m.Ctrl {
		out |= C.GHOSTTY_MODS_CTRL
	}
	if m.Alt {
		out |= C.GHOSTTY_MODS_ALT
	}
	if m.Super {
		out |= C.GHOSTTY_MODS_SUPER
	}
	return out
}

// cAction converts a key action.
func cAction(a emulator.KeyAction) (C.GhosttyKeyAction, bool) {
	switch a {
	case emulator.KeyPress:
		return C.GHOSTTY_KEY_ACTION_PRESS, true
	case emulator.KeyRepeat:
		return C.GHOSTTY_KEY_ACTION_REPEAT, true
	case emulator.KeyRelease:
		return C.GHOSTTY_KEY_ACTION_RELEASE, true
	default:
		return 0, false
	}
}

// resultError converts an upstream result code into the port's errors.
//
// Every code the header names has an arm, and the arm names the SENTINEL the
// port uses rather than repeating the upstream name: a caller branches on
// emulator.ErrUnsupported and never on GHOSTTY_NO_VALUE. A code the header
// grows later falls to [emulator.ErrFailure], which is the honest answer — the
// adapter cannot classify a failure it has never seen.
func resultError(op string, r C.GhosttyResult) error {
	if r == C.GHOSTTY_SUCCESS {
		return nil
	}
	sentinel := emulator.ErrFailure
	switch r {
	case C.GHOSTTY_OUT_OF_MEMORY:
		sentinel = emulator.ErrExhausted
	case C.GHOSTTY_INVALID_VALUE:
		sentinel = emulator.ErrRefused
	case C.GHOSTTY_OUT_OF_SPACE:
		sentinel = emulator.ErrFailure
	case C.GHOSTTY_NO_VALUE:
		sentinel = emulator.ErrUnsupported
	case C.GHOSTTY_IO_ERROR:
		sentinel = emulator.ErrFailure
	case C.GHOSTTY_LIMIT_EXCEEDED:
		sentinel = emulator.ErrRefused
	case C.GHOSTTY_REJECTED:
		sentinel = emulator.ErrRefused
	}
	return fmt.Errorf("ghostty: %s: %w", op, sentinel)
}
