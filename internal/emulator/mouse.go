package emulator

// MouseAction is what happened to the mouse.
//
// It is the same three shapes every tracking protocol reports, and it is
// carried rather than folded into the button because a motion with no button
// held is a real event that a program in any-event tracking mode asked for: a
// type that could not say "moved, nothing pressed" could not encode it.
type MouseAction uint8

const (
	// MousePress is a button going down. It is the zero value, which is what a
	// caller that says nothing about an action means.
	MousePress MouseAction = iota
	// MouseRelease is a button coming up.
	MouseRelease
	// MouseMotion is the pointer moving.
	MouseMotion
)

// MouseButton is which button, and which wheel direction, an event names.
//
// The wheel's four directions are buttons in every protocol that encodes them
// — xterm numbers them 4 through 7 — so they are named here as directions
// rather than left as "button four", and the numbering above them (buttons 8
// through 11) is kept because a program can ask for those and a caller with a
// twelve-button mouse should not be told its extra buttons do not exist.
//
// The zero value is [MouseNone]: an event that was never given a button claims
// no button, which is what a motion carries.
type MouseButton uint8

const (
	// MouseNone is no button at all. It is the zero value, and it is what a
	// motion event carries when nothing is held.
	MouseNone MouseButton = iota
	// MouseLeft is the primary button.
	MouseLeft
	// MouseMiddle is the middle button of a three-button mouse.
	MouseMiddle
	// MouseRight is the secondary button.
	MouseRight
	// MouseWheelUp is the wheel away from the user.
	MouseWheelUp
	// MouseWheelDown is the wheel toward the user.
	MouseWheelDown
	// MouseWheelLeft is a horizontal wheel to the left.
	MouseWheelLeft
	// MouseWheelRight is a horizontal wheel to the right.
	MouseWheelRight
	// MouseButton8 through MouseButton11 are the four extra buttons a
	// twelve-button mouse reports. They are named singly rather than given a
	// range type because each is one identity, exactly as the wheel's four
	// directions are.
	MouseButton8
	MouseButton9
	MouseButton10
	MouseButton11
)

// MouseEvent is one mouse event to encode. X and Y are CELL coordinates,
// zero-based, in the terminal's own grid.
//
// # Why cells, and why zero-based
//
// The protocols PUT a number in the sequence, and that number is a cell with
// the grid's origin in it; the emulator is the only party that knows the grid,
// so it is the only party that can answer. A caller that converted its own
// pixel position to a cell would be guessing at the cell size — which is a
// font measurement, per [Geometry] — and would then have to be told about
// margins, padding and the scroll offset as well. Zero-based here and
// one-based on the wire is the emulator's business, and the port states the
// origin so that a caller can be in no doubt about which it is holding.
//
// Mods is the same modifier state a key event carries, because the protocols
// that carry mouse modifiers encode them with the same bit positions, and one
// type for one fact is the whole reason [Mods] is booleans rather than a
// bitmask of anybody's numbering.
type MouseEvent struct {
	Action MouseAction
	Button MouseButton
	Mods   Mods
	X, Y   int
}
