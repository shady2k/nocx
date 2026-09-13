package emulator

// EffectKind is which non-visual effect a program's output asked for.
//
// An effect is a thing a program's output asked the terminal to DO — ring, set
// a title, copy to the clipboard, report a directory — as opposed to a thing it
// asked the terminal to show. It is an enumeration of its own rather than a copy
// of an upstream one for the same reason every other type here is: the set is
// nocx's, it is closed, and the zero value is deliberately not one of the real
// kinds, so an Effect that was never filled in claims nothing.
type EffectKind uint8

const (
	// EffectNone is the zero value: no effect. An [Effect] never carries it —
	// an implementation that produced nothing either appends nothing or
	// reports the failure — and it exists so that a zero Effect is not
	// silently a bell.
	EffectNone EffectKind = iota
	// EffectBell is the audible bell: BEL (0x07), and the bell of a terminal
	// that the program's own output rang. Its body is empty, because there is
	// nothing to say: the effect IS the ring.
	EffectBell
	// EffectNotification is a desktop notification request (OSC 9 without a
	// parameter, and OSC 777). Its body is the message text. The port
	// deliberately carries the text and not a title/body pair: the protocols
	// that have no title put the whole message in the body, and a program that
	// supplies one is a formatting decision the surface can make from the text
	// it has.
	EffectNotification
	// EffectClipboard is a request to write the clipboard (OSC 52 and its
	// Kitty descendant). Its body is the payload the program supplied, copied.
	// Whether the write is performed, and by whom, is the runtime's decision
	// (design §6.2) — an effect is the request crossing the port, not a
	// promise that the clipboard changed.
	EffectClipboard
	// EffectTitle is a new window title (OSC 0 and OSC 2) and its body is the
	// whole title, not a delta: it is what the program says the title now is.
	EffectTitle
	// EffectCwdReport is the program reporting its working directory (OSC 7
	// and its relatives) and its body is what the program reported. The port
	// does NOT decode it: a report is usually a file:// URI and the decoding,
	// the host check and the "is this even a local path" question belong to
	// the surface that has a filesystem to compare it against, which the
	// emulator does not.
	EffectCwdReport
)

// Effect is one non-visual effect the program asked for: a thing that
// changes no cell. Body is the effect's argument, copied out of the
// emulator: the title for a title, the clipboard payload for a clipboard
// write, the reported directory for a cwd report, the message text for a
// notification request. Kind is never EffectNone.
//
// # Why a list and not a callback
//
// The program's effects arrive with the program's output, on whatever
// goroutine is feeding it, but they are not the runtime's business to act on
// there: ringing, retitling and clipboard writes are the surface's, and a busy
// session must be able to ingest a megabyte of output without the emulator
// calling into the runtime a thousand times from inside its lock. Effects
// therefore accumulate and are DRAINED: the runtime reads the list when it is
// ready to act, each effect once, oldest first.
//
// Body is copied before it is stored. The emulator's callbacks receive borrowed
// memory that dies when the call returns, so an implementation that kept the
// pointer would hand a caller a title that was freed a moment ago — and the
// caller could not tell.
type Effect struct {
	Kind EffectKind
	Body []byte
}
