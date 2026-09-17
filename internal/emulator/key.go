package emulator

// Key is a key identity: the physical key, not the character it produced.
//
// It is deliberately neither the frontend's KeyboardEvent.code nor a codepoint,
// because a terminal program receives a key EVENT and the emulator decides what
// bytes that event is worth given the modes the program set. A client that
// encoded its own key would send differently encoded input while the screen
// looked correct (design §6.1), which is the defect this enumeration exists to
// close.
//
// The set is the keys a program receives as events: the alphabet, the digits,
// the punctuation of a US layout, the control pad, the arrow pad, and F1–F12.
// Numpad keys, media keys, international keys and the modifier keys themselves
// are deliberately out — the first three because the layout's own layer is a
// client question and the last because a modifier alone is not a key event a
// program is sent. The zero value is [KeyUnknown], so a Key that was never set
// is a key no caller meant.
type Key uint16

const (
	KeyUnknown Key = iota

	KeyLeft
	KeyRight
	KeyUp
	KeyDown
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyInsert
	KeyDelete
	KeyBackspace
	KeyEnter
	KeyTab
	KeyEscape
	KeySpace

	KeyA
	KeyB
	KeyC
	KeyD
	KeyE
	KeyF
	KeyG
	KeyH
	KeyI
	KeyJ
	KeyK
	KeyL
	KeyM
	KeyN
	KeyO
	KeyP
	KeyQ
	KeyR
	KeyS
	KeyT
	KeyU
	KeyV
	KeyW
	KeyX
	KeyY
	KeyZ

	KeyDigit0
	KeyDigit1
	KeyDigit2
	KeyDigit3
	KeyDigit4
	KeyDigit5
	KeyDigit6
	KeyDigit7
	KeyDigit8
	KeyDigit9

	KeyBackquote
	KeyBackslash
	KeyBracketLeft
	KeyBracketRight
	KeyComma
	KeyEqual
	KeyMinus
	KeyPeriod
	KeyQuote
	KeySemicolon
	KeySlash

	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

// Mods is the modifier state of a key event: one field per modifier rather than
// a mirror of an upstream bitmask.
//
// A struct and not a bitset on purpose. A bitset invites a caller to cast, and
// the values of any other terminal library are not nocx's; four booleans have
// no numbering for an upstream enum to redefine under us. Key sides (left shift
// against right) are not carried: they are optional in every input protocol
// that has them, and nothing downstream has asked.
type Mods struct {
	Shift bool
	Ctrl  bool
	Alt   bool
	Super bool
}

// KeyAction is what happened to the key.
//
// It is carried because the Kitty keyboard protocol reports releases and repeats
// as events of their own, and a runtime that could not say "this is a release"
// could not drive a program that asked for them. The zero value is a press,
// which is what a caller that says nothing about an action means.
type KeyAction uint8

const (
	// KeyPress is the key going down. It is the zero value.
	KeyPress KeyAction = iota
	// KeyRepeat is the key held down and repeating.
	KeyRepeat
	// KeyRelease is the key coming up. Only a program that asked for release
	// events receives it.
	KeyRelease
)

// KeyEvent is one key event to encode.
//
// Text is the text the press generated, when the caller knows it — the
// frontend's key event carries it. It is what makes a layout-dependent key
// encodable at all: the encoder cannot derive 'ä' from [KeyA], and the protocols
// that report associated text want the character rather than the key. An empty
// Text is a legitimate event (a key with no text) and not a missing field.
type KeyEvent struct {
	Key    Key
	Mods   Mods
	Action KeyAction
	Text   string
}
