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
	// EffectFence is the shell's render fence (ADR-0024 §7 carve-out): OSC
	// 1337 with the exact payload NOCX_FENCE;<64 lowercase hex>, written to
	// the pty after a command's output. Body is the nonce exactly as the
	// stream carried it (the 64 hex chars, undecoded — which bytes they are
	// is the wire's, and decoding is the consumer's); Source is the text of
	// the screen rows the fence was drawn over, captured at the moment the
	// fence completed.
	//
	// A fence LOCATES an event that was authenticated elsewhere and authorises
	// nothing: completing a command, closing a block and choosing an endpoint
	// are decisions for the authenticated half of the handshake, and a fence
	// with no authenticated event behind it does nothing (ADR-0024 decision
	// 1). The content is pinned rather than a row number because later output
	// can overwrite or scroll those rows before the authenticated half
	// arrives, and the content must not change when it does.
	EffectFence
)

// Effect is one non-visual effect the program asked for: a thing that
// changes no cell. What each field carries is the kind's to say — see
// [EffectKind] — and Kind is never EffectNone.
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
// Body and Source are copied before they are stored. The emulator's callbacks
// receive borrowed memory that dies when the call returns, so an
// implementation that kept the pointer would hand a caller a title that was
// freed a moment ago — and the caller could not tell.
type Effect struct {
	Kind EffectKind
	Body []byte
	// Source is the content an EffectFence was drawn over: the text of the
	// screen rows at the fence, captured when the fence completed. Nil for
	// every other kind, and never read except through Kind — a consumer
	// branches on the kind first, exactly as it does for Body's meaning.
	Source []byte
}
