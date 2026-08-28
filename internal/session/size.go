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

// effectiveSize is the whole of the ownership move (nocx-eidfb.1): the
// client MEASURES — only a webview knows its own font metrics and pane
// geometry — and the backend DECIDES. What a client sends is a report, and
// this function is the single place that turns reports into the size a
// session actually runs at.
//
// Today the decision is small, because a session has at most one client:
// the report if it is usable, the named default otherwise. The default arm
// is not a defensive check — it is the answer for a session with no client
// attached at all, which is the state a backend outliving its window
// produces and the state that previously had no size whatsoever.
//
// Choosing among SEVERAL attached clients is nocx-eidfb.2 and lands here,
// as more of this decision, rather than as a second decision somewhere a
// caller can reach.
func effectiveSize(reported Size) Size {
	if reported.Valid() {
		return reported
	}
	return DefaultSize()
}
