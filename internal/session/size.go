package session

// Size is a terminal's geometry: the grid a channel is created at and the
// pixel dimensions that go with it (AD-1's `{cols, rows, xpixel, ypixel}`).
//
// It is one value rather than four parameters because the four are never
// meaningful apart — a resize that carried three of them would be a size
// nothing can be created at — and because the four travelled as a loose
// tuple through Config, the pty factory, the ssh options and the resize
// lane, which is four places that each had to remember the order.
type Size struct {
	Cols   uint16
	Rows   uint16
	XPixel uint16
	YPixel uint16
}

// Valid reports whether s names a grid a channel can be created at. Pixels
// are deliberately not part of it: a client that knows its cell grid and
// not its pixel geometry has reported a usable size, and every caller in
// this repo sends 0 for both pixel fields today.
func (s Size) Valid() bool { return s.Cols > 0 && s.Rows > 0 }

// defaultCols/defaultRows are the size a session runs at while no client
// has reported one. 80x24 is the terminal's own historical default and the
// value herdr's headless server holds for the same case (MIN_COLS x
// MIN_ROWS in src/server/headless.rs) — a session whose window has gone
// away keeps running at a shape programs can render into, rather than at
// 0x0, which is what "the client owns the size" degrades to once there is
// no client.
const (
	defaultCols uint16 = 80
	defaultRows uint16 = 24
)

// DefaultSize is that default, named once. It is a function rather than a
// package variable so no caller can assign a new default from somewhere
// else in the process: there is one owner of this decision and it is this
// package (AD-8).
func DefaultSize() Size { return Size{Cols: defaultCols, Rows: defaultRows} }

// NoClient is the report a session takes when the client that owned its size
// has gone: nobody is measuring it any more. It is deliberately not a size —
// Valid is false for it — because "no client is attached" is a state, not a
// grid, and a caller that mistook it for one would be asking for 0x0.
//
// It exists so the two callers can spell the two different facts differently.
// A client that attaches without reporting its geometry has not measured
// itself yet — a fresh window reclaiming a pane it has never rendered — and
// the transport reports nothing at all for it, leaving the session at the size
// it is running at. A subscriber slot that has EMPTIED is this, and the
// session returns to the named default. Answering both with the default would
// put a live window's terminal on 80x24 for no reason.
func NoClient() Size { return Size{} }

// effectiveSize is the whole of the ownership move (nocx-eidfb.1): the
// client MEASURES — only a webview knows its own font metrics and pane
// geometry — and the backend DECIDES. What a client sends is a report, and
// this function is the single place that turns reports into the size a
// session actually runs at.
//
// The report it takes is the FOREGROUND client's — the one that attached
// last, which is the client the shared channel follows (nocx-eidfb.2). The
// reference is herdr's headless server, where the shared pane runtime is
// derived from the foreground client and the newcomer becomes foreground on
// connect (src/server/headless.rs); rendering stays each client's own, and
// for nocx that half is free because every window is a DOM that lays itself
// out. Explicitly NOT tmux's rule, which fits the shared runtime to the
// smallest attached client and so punishes the big window for the small
// one's existence.
//
// WHICH client is foreground is the transport's to know, not this package's:
// the subscriber slot is the attachment record and it already displaces its
// occupant out loud (session.displaced). What arrives here is that client's
// report, and the two facts a report can carry are both answered in this one
// place — a usable grid becomes the session's size, and NoClient, the report
// a session gets when the slot empties, becomes the named default. That
// default arm is not a defensive check: it is the answer for a session with
// no client attached at all, which is the state a backend outliving its
// window produces and the state that previously had no size whatsoever.
func effectiveSize(reported Size) Size {
	if reported.Valid() {
		return reported
	}
	return DefaultSize()
}
