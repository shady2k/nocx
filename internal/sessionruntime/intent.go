package sessionruntime

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/emulator"
)

// ErrIntentUnsupported names an intent this runtime cannot turn into bytes: a
// kind that names no encoding, or an argument that is not the shape its kind
// needs. It is deliberately NOT one of contract.go's sentinels — those name
// events the VOCABULARY refuses, and this one names a container the vocabulary
// declares but a wire that has not arrived. The intent is REFUSED and
// consumed, so a caller is told now rather than when it is executed.
var ErrIntentUnsupported = errors.New("sessionruntime: the intent has no encoding")

// # The intent payload vocabulary, until the client's protocol declares one
//
// An [Intent] carries its argument as bytes, and nothing in the contract says
// what those bytes are: the wire is the client epic's (nocx-zg3k3), and it
// arrives with the frame protocol that replaces Snapshot.Screen's stand-in.
// What a runtime still needs is a way to be DRIVEN, so that the encoding rules
// this runtime owns can be exercised against a program that set a mode — which
// is what these three parsers are, and the whole of them.
//
// The spellings are deliberately the ones the reference model already uses for
// the one kind it encodes ([]byte("Up") in model_test.go), so the schedules
// and the real runtime send the same key with the same payload rather than two
// dialects of one vocabulary.
//
//	Key    "Up", "Enter", "F1", … and the punctuation names below, optionally
//	       prefixed by modifiers: "Ctrl+Up", "Shift+Alt+A". The name is the
//	       PHYSICAL key; what it becomes is the emulator's decision against the
//	       modes the program set.
//	Text   the text itself, verbatim.
//	Paste  the pasted body, verbatim.
//	Mouse  "<action> <button> <x> <y>" in CELL coordinates, e.g. "press left 12 5",
//	       "release left 12 5", "motion none 12 5", "press wheel-up 4 9".
//	Focus  "in" or "out".
//
// A name this vocabulary does not carry is REFUSED rather than guessed: a
// key-encoding table that fell back to "send the payload" is exactly the defect
// ADR-0066 exists against.

// keyNames is the one table from a payload's spelling to the port's key
// identity. The port owns the identities and carries no names of its own — a
// name is a wire question and the wire is not declared yet — so the table
// lives where the parsing does, and it is exhaustive over the alphabet, the
// digits, the punctuation of a US layout, the control pad, the arrow pad and
// F1–F12.
var keyNames = map[string]emulator.Key{
	"left": emulator.KeyLeft, "right": emulator.KeyRight,
	"up": emulator.KeyUp, "down": emulator.KeyDown,
	"home": emulator.KeyHome, "end": emulator.KeyEnd,
	"pageup": emulator.KeyPageUp, "pagedown": emulator.KeyPageDown,
	"insert": emulator.KeyInsert, "delete": emulator.KeyDelete,
	"backspace": emulator.KeyBackspace, "enter": emulator.KeyEnter,
	"tab": emulator.KeyTab, "escape": emulator.KeyEscape, "space": emulator.KeySpace,

	"a": emulator.KeyA, "b": emulator.KeyB, "c": emulator.KeyC, "d": emulator.KeyD,
	"e": emulator.KeyE, "f": emulator.KeyF, "g": emulator.KeyG, "h": emulator.KeyH,
	"i": emulator.KeyI, "j": emulator.KeyJ, "k": emulator.KeyK, "l": emulator.KeyL,
	"m": emulator.KeyM, "n": emulator.KeyN, "o": emulator.KeyO, "p": emulator.KeyP,
	"q": emulator.KeyQ, "r": emulator.KeyR, "s": emulator.KeyS, "t": emulator.KeyT,
	"u": emulator.KeyU, "v": emulator.KeyV, "w": emulator.KeyW, "x": emulator.KeyX,
	"y": emulator.KeyY, "z": emulator.KeyZ,

	"0": emulator.KeyDigit0, "1": emulator.KeyDigit1, "2": emulator.KeyDigit2,
	"3": emulator.KeyDigit3, "4": emulator.KeyDigit4, "5": emulator.KeyDigit5,
	"6": emulator.KeyDigit6, "7": emulator.KeyDigit7, "8": emulator.KeyDigit8,
	"9": emulator.KeyDigit9,

	"backquote": emulator.KeyBackquote, "backslash": emulator.KeyBackslash,
	"bracketleft": emulator.KeyBracketLeft, "bracketright": emulator.KeyBracketRight,
	"comma": emulator.KeyComma, "equal": emulator.KeyEqual, "minus": emulator.KeyMinus,
	"period": emulator.KeyPeriod, "quote": emulator.KeyQuote,
	"semicolon": emulator.KeySemicolon, "slash": emulator.KeySlash,

	"f1": emulator.KeyF1, "f2": emulator.KeyF2, "f3": emulator.KeyF3, "f4": emulator.KeyF4,
	"f5": emulator.KeyF5, "f6": emulator.KeyF6, "f7": emulator.KeyF7, "f8": emulator.KeyF8,
	"f9": emulator.KeyF9, "f10": emulator.KeyF10, "f11": emulator.KeyF11, "f12": emulator.KeyF12,
}

// keyText is the character each PRINTABLE key carries. It is not decoration:
// the port's encoder takes the text the press generated (KeyEvent.Text) and
// cannot derive it from the key — that is what makes a layout-dependent key
// encodable at all — so a key named without one would encode to no bytes at
// all, which is how a printable key reaches a program as silence.
//
// The characters are the unshifted US layout, and a shift over a letter
// upper-cases it here because that is the layout's own truth. Anything a
// layout can make of a shifted digit or a punctuation key is the CLIENT's to
// say — it is the side that knows which key produced which character — and
// arrives with the intent protocol the wire epic declares (nocx-zg3k3).
var keyText = map[string]string{
	"space": " ", "backquote": "`", "backslash": "\\", "bracketleft": "[", "bracketright": "]",
	"comma": ",", "equal": "=", "minus": "-", "period": ".", "quote": "'",
	"semicolon": ";", "slash": "/",

	"a": "a", "b": "b", "c": "c", "d": "d", "e": "e", "f": "f", "g": "g", "h": "h",
	"i": "i", "j": "j", "k": "k", "l": "l", "m": "m", "n": "n", "o": "o", "p": "p",
	"q": "q", "r": "r", "s": "s", "t": "t", "u": "u", "v": "v", "w": "w", "x": "x",
	"y": "y", "z": "z",

	"0": "0", "1": "1", "2": "2", "3": "3", "4": "4",
	"5": "5", "6": "6", "7": "7", "8": "8", "9": "9",
}

// keyEvent reads a key intent's payload. The name is matched case-insensitively
// so that a payload may be written the way a person says a key.
func keyEvent(payload []byte) (emulator.KeyEvent, error) {
	name, mods, err := splitMods(string(payload))
	if err != nil {
		return emulator.KeyEvent{}, err
	}
	lowered := strings.ToLower(name)
	key, ok := keyNames[lowered]
	if !ok {
		return emulator.KeyEvent{}, fmt.Errorf("%w: key %q", ErrIntentUnsupported, name)
	}
	ev := emulator.KeyEvent{Key: key, Mods: mods}
	ev.Text = keyText[lowered]
	if mods.Shift && len(ev.Text) == 1 && ev.Text[0] >= 'a' && ev.Text[0] <= 'z' {
		ev.Text = strings.ToUpper(ev.Text)
	}
	return ev, nil
}

// splitMods peels the modifier prefixes off a key's name. A modifier the table
// does not know is refused rather than ignored: silently dropping a Ctrl is
// how a program receives a different key than the one that was pressed.
func splitMods(name string) (string, emulator.Mods, error) {
	var mods emulator.Mods
	for {
		head, rest, found := strings.Cut(name, "+")
		if !found {
			return name, mods, nil
		}
		switch strings.ToLower(head) {
		case "ctrl":
			mods.Ctrl = true
		case "alt":
			mods.Alt = true
		case "shift":
			mods.Shift = true
		case "super":
			mods.Super = true
		default:
			return name, mods, fmt.Errorf("%w: modifier %q", ErrIntentUnsupported, head)
		}
		name = rest
	}
}

// mouseActions and mouseButtons are the payload's spellings for the port's two
// mouse vocabularies, exhaustive over both.
var (
	mouseActions = map[string]emulator.MouseAction{
		"press": emulator.MousePress, "release": emulator.MouseRelease, "motion": emulator.MouseMotion,
	}
	mouseButtons = map[string]emulator.MouseButton{
		"none": emulator.MouseNone, "left": emulator.MouseLeft, "middle": emulator.MouseMiddle,
		"right": emulator.MouseRight, "wheel-up": emulator.MouseWheelUp, "wheel-down": emulator.MouseWheelDown,
		"wheel-left": emulator.MouseWheelLeft, "wheel-right": emulator.MouseWheelRight,
		"8": emulator.MouseButton8, "9": emulator.MouseButton9,
		"10": emulator.MouseButton10, "11": emulator.MouseButton11,
	}
)

// mouseEvent reads a mouse intent's payload: "<action> <button> <x> <y>", in
// cell coordinates.
func mouseEvent(payload []byte) (emulator.MouseEvent, error) {
	fields := strings.Fields(string(payload))
	if len(fields) != 4 {
		return emulator.MouseEvent{}, fmt.Errorf("%w: mouse %q, want \"<action> <button> <x> <y>\"",
			ErrIntentUnsupported, payload)
	}
	action, ok := mouseActions[strings.ToLower(fields[0])]
	if !ok {
		return emulator.MouseEvent{}, fmt.Errorf("%w: mouse action %q", ErrIntentUnsupported, fields[0])
	}
	button, ok := mouseButtons[strings.ToLower(fields[1])]
	if !ok {
		return emulator.MouseEvent{}, fmt.Errorf("%w: mouse button %q", ErrIntentUnsupported, fields[1])
	}
	x, err := strconv.Atoi(fields[2])
	if err != nil {
		return emulator.MouseEvent{}, fmt.Errorf("%w: mouse column %q", ErrIntentUnsupported, fields[2])
	}
	y, err := strconv.Atoi(fields[3])
	if err != nil {
		return emulator.MouseEvent{}, fmt.Errorf("%w: mouse row %q", ErrIntentUnsupported, fields[3])
	}
	return emulator.MouseEvent{Action: action, Button: button, X: x, Y: y}, nil
}

// focusEvent reads a focus intent's payload: "in" for the terminal gaining
// focus and "out" for it losing it. Whether the program is told at all is the
// emulator's decision against the mode the program set.
func focusEvent(payload []byte) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(string(payload))) {
	case "in":
		return true, nil
	case "out":
		return false, nil
	default:
		return false, fmt.Errorf("%w: focus %q, want \"in\" or \"out\"", ErrIntentUnsupported, payload)
	}
}
