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
	// EffectOutputMark is the shell's output-start mark (nocx-2v80t.3.12):
	// OSC 133 C, unconditionally written by nocx.bash's preexec hook before
	// every command a shell with authenticated integration hands off, with
	// no payload. Body is always empty.
	//
	// Like EffectFence it LOCATES rather than authorises (ADR-0024 decision
	// 1 — "C and D have no meaning" as lifecycle authority, and still do
	// not): it only tells sessionruntime where, inside an interval it has
	// already authenticated by other means, that interval's own output
	// begins. It opens nothing, closes nothing and assigns no status; a
	// sighting with no interval in flight to locate anything in does
	// nothing at all.
	EffectOutputMark
	// EffectClearBoundary is the program erasing the display AND its saved
	// lines: ED3, `CSI 3 J`, written to the pty after `CSI H` and `CSI 2 J`
	// — exactly what `clear(1)` emits (nocx-zg3k3.10.3's owner decision,
	// verified). Body is always empty.
	//
	// It is SCANNED for, the way the fence is (fence.go), rather than read
	// off a side effect of the library's own state: a first attempt read it
	// off the scrollback depth collapsing to zero (the depth
	// noteDepartedLocked already measures every feed) and measured wrong on
	// the e2e (nocx-2v80t.3.17) — a short session whose live rows never
	// scrolled into history has a depth of zero BEFORE the erase too, so
	// the transition never fires for the case this effect exists to catch:
	// a person runs one command, the screen is nowhere near full, and
	// `clear` tidies up blocks that are still on screen and never
	// departed. The library still parses and executes ED3 exactly as
	// before; scanning only locates where it sits in the feed, the way the
	// fence's own scanner does, and plain ED2 alone (a full-screen program
	// redrawing) never matches the scanned sequence at all.
	//
	// Like EffectFence and EffectOutputMark this is not a lifecycle
	// authority (ADR-0024 decision 1): it authorises nothing, opens nothing
	// and completes nothing. Unlike them it locates no event authenticated
	// elsewhere — ED3 is a real, standard VT operation the library executes
	// as part of the very vt_write that carries it (ADR-0066: the backend
	// owns the whole VT grammar now, not a private second reading), so the
	// sighting IS the fact rather than a location for one.
	EffectClearBoundary
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
