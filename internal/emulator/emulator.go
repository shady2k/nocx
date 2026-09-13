package emulator

import "errors"

// The port's own errors. An implementation returns these, never an upstream
// result code and never an upstream error: ADR-0065 point 3 says nocx defines
// its own errors, and the whole value of that is that a caller can branch on a
// sentinel without knowing which library produced it.
var (
	// ErrClosed is returned for any use of a terminal after Close. It is a
	// programming error rather than a terminal condition, and it is a named
	// error rather than a panic because a runtime shutting down races its own
	// readers and must not take the process with it.
	ErrClosed = errors.New("emulator: terminal is closed")
	// ErrOutOfRange means the request named a position or a size the terminal
	// cannot have: a column outside the grid, a row outside the screen, a
	// geometry of zero or of a dimension no terminal represents.
	ErrOutOfRange = errors.New("emulator: outside the terminal")
	// ErrUnsupported means the request is a thing this port has no value for —
	// a key identity it does not carry, a mode it cannot report. It is not a
	// failure of the terminal: it is the port saying what it does not know,
	// which is the only alternative to inventing an answer.
	ErrUnsupported = errors.New("emulator: not carried by this port")
	// ErrExhausted means the emulator could not allocate.
	ErrExhausted = errors.New("emulator: the emulator ran out of memory")
	// ErrRefused means the emulator rejected the request as invalid. It is
	// distinct from ErrUnsupported: the port knows what was asked and the
	// emulator declined it.
	ErrRefused = errors.New("emulator: the emulator refused the request")
	// ErrFailure is the emulator failing at something it should be able to do.
	// It carries no more meaning than that, and deliberately: an implementation
	// that cannot classify a failure says so rather than choosing a category.
	ErrFailure = errors.New("emulator: the emulator failed")
)

// Screen names which of the two buffers a read belongs to. A terminal that has
// never been asked for the alternate screen is on the primary one, which is why
// the zero value means that and not "unknown": the state a fresh terminal is in
// is a real answer.
type Screen uint8

const (
	// ScreenPrimary is the normal buffer: the scrollback, and the screen a
	// shell prints on.
	ScreenPrimary Screen = iota
	// ScreenAlternate is the buffer a full-screen program owns (DECSET 1049
	// and its ancestors). Reads come from whatever is active.
	ScreenAlternate
)

// Width is a cell's column footprint, which is the thing a renderer must have
// right and the thing a font measurement cannot be asked about.
//
// It is an enumeration of its own rather than a copy of the upstream values,
// and its zero value is deliberately not one of the real widths: a Cell that
// was never read must not claim to be a narrow cell.
type Width uint8

const (
	// WidthUnknown is the zero value: no cell was read.
	WidthUnknown Width = iota
	// WidthNarrow is a cluster one column wide.
	WidthNarrow
	// WidthWide is a cluster two columns wide. The cell after it is its
	// continuation.
	WidthWide
	// WidthSpacerTail is the second column of a wide cluster. It is not
	// rendered: the cluster to its left already covered it.
	WidthSpacerTail
	// WidthSpacerHead is the column a wide cluster would have needed at the
	// end of a soft-wrapped line. It carries nothing and is not rendered.
	WidthSpacerHead
)

// ColorKind is which of the three shapes a colour is in. The distinction is
// load-bearing rather than decorative: a palette index means "this theme's
// colour 3" and repaints when the theme changes, an RGB value means "exactly
// this" and does not, and a default colour means the theme's own choice for the
// role. Collapsing them loses the reading design §6.3 says a restored block
// needs.
type ColorKind uint8

const (
	// ColorDefault is the terminal's own colour for the role. It is the zero
	// value, which is what an unstyled cell holds.
	ColorDefault ColorKind = iota
	// ColorPalette is an index into the terminal's 256-colour palette.
	ColorPalette
	// ColorRGB is an exact colour.
	ColorRGB
)

// RGB is an exact colour, one byte per channel.
type RGB struct {
	R, G, B uint8
}

// Color is one style colour, in whichever of the three shapes it is in. The
// fields not named by Kind are meaningless and are left zero: a Color is read
// through its Kind, which is why the three shapes stay apart instead of being
// flattened into an RGB triple on the way out of the emulator.
type Color struct {
	Kind    ColorKind
	Palette uint8
	RGB     RGB
}

// Attributes are the on/off text decorations of a style, one bit each.
//
// A bitset rather than eight booleans because that is how they are compared,
// stored and sent, and because the set is closed: a terminal has these eight
// and no ninth.
type Attributes uint16

const (
	AttrBold Attributes = 1 << iota
	AttrItalic
	AttrFaint
	AttrBlink
	AttrInverse
	AttrInvisible
	AttrStrikethrough
	AttrOverline
)

// Underline is the shape of the underline decoration, which SGR 4:0–4:5 can
// choose and which is not a boolean.
type Underline uint8

const (
	UnderlineNone Underline = iota
	UnderlineSingle
	UnderlineDouble
	UnderlineCurly
	UnderlineDotted
	UnderlineDashed
)

// Style is the complete visual style of one cell.
type Style struct {
	Foreground     Color
	Background     Color
	UnderlineColor Color
	Attributes     Attributes
	Underline      Underline
}

// Cell is one grid position, copied out of the emulator.
//
// Grapheme is the whole cluster — the base codepoint followed by every
// combining codepoint the terminal assembled into it — and not one rune. A
// consumer that wants a character takes this; a consumer that wants a position
// takes the column index, because Width says how many columns this cell
// occupies and the next cell may be its spacer.
//
// HasText is separate from Grapheme being empty because the two are different
// facts: a cell can carry a background colour and no text, and a renderer must
// paint the first and not the second.
type Cell struct {
	Grapheme string
	Width    Width
	HasText  bool
	Style    Style
}

// Row is one physical line of the screen, copied out of the emulator.
//
// Cells is one entry per column, including the spacers of wide clusters and the
// blank cells of trailing space: a row is a rectangle, and a consumer that
// wants its text skips WidthSpacerTail cells rather than receiving a shorter
// slice.
//
// Wrap and Continuation are what let a serialiser join the physical lines of
// one logical line without losing a hard newline. They are not each other's
// negation: the last row of a wrapped sequence has Wrap false and Continuation
// true, and the row before it has Wrap true and Continuation false.
type Row struct {
	Cells        []Cell
	Wrap         bool
	Continuation bool
}

// Geometry is a terminal's size: the cell grid, and the pixel size of one cell.
//
// The pixel size is carried because it is not decoration. The emulator answers
// the program's own size queries (XTWINOPS) and reports its size in pixels for
// image placement, so a port that carried only columns and rows would force an
// implementation to invent a cell size and then answer a program's question
// with the invention.
type Geometry struct {
	Cols, Rows                int
	CellWidthPx, CellHeightPx int
}

// Valid reports whether a geometry is one a terminal can be built or resized
// to. Zero columns or rows are not a terminal, and neither is a dimension no
// terminal protocol can express.
func (g Geometry) Valid() bool {
	return g.Cols > 0 && g.Cols <= maxDimension &&
		g.Rows > 0 && g.Rows <= maxDimension &&
		g.CellWidthPx >= 0 && g.CellWidthPx <= maxPixel &&
		g.CellHeightPx >= 0 && g.CellHeightPx <= maxPixel
}

// maxDimension is the largest cell count a terminal's geometry can carry. The
// upstream C API takes uint16 columns and rows, and a resize beyond it would be
// silently truncated rather than refused, which is the failure mode this bound
// exists to prevent.
const maxDimension = 1<<16 - 1

// maxPixel is the largest cell pixel dimension the C API's uint32 carries. A
// pixel size this large is not a real display, but the bound is written as the
// representable limit rather than as a plausible one, so that the check refuses
// only what cannot cross.
const maxPixel = 1<<32 - 1

// Terminal is the port: one terminal instance, and everything a session runtime
// does to it and reads from it.
//
// Every method is safe for concurrent use. The implementation serialises access
// — ADR-0065 point 3 — and a caller may therefore read a row from a renderer
// while ingesting program output from a reader goroutine without any lock of
// its own. What a caller may NOT do is assume the two are ordered: an ingest
// concurrent with a read may or may not be reflected in it, exactly as the
// program's own output is concurrent with the read of it.
type Terminal interface {
	// Geometry is the size the terminal is running at. A closed terminal has
	// no size, so this reports [ErrClosed] rather than the last one it had —
	// a size a caller could read after Close would be a size nobody can act on.
	Geometry() (Geometry, error)

	// Resize changes the size and returns the replies the resize produced, for
	// the same reason Ingest does: a terminal with in-band size reports enabled
	// (mode 2048) answers a resize by writing to the PTY, and a port that
	// dropped those bytes would lose a report the program is waiting for.
	//
	// A refused geometry returns an error and leaves the terminal at the size it
	// had, so a caller that treats resize as a commit has two ends to the
	// interval rather than a hope.
	Resize(g Geometry) (replies []byte, err error)

	// Ingest feeds program output to the terminal's parser and returns the
	// bytes the program asked for in reply — a cursor report, a device status,
	// a mode query — for the caller to write back to the PTY.
	//
	// The replies are a COPY. The emulator's callbacks receive borrowed memory
	// that dies when the call returns, so an implementation hands back bytes it
	// owns and the caller may hold them for as long as it likes.
	//
	// Replies are returned rather than pushed to a callback because the program
	// is usually blocked waiting for one: the caller writes them to the PTY on
	// the same path it took to get them, with no queue and no goroutine in
	// between.
	Ingest(b []byte) (replies []byte, err error)

	// Row returns one physical line of the active screen, copied.
	//
	// A row is counted from the top of the ACTIVE AREA — the grid the cursor
	// moves in — and not from the scrollback and not from a viewport somebody
	// has scrolled: scrollback is a different reading with a different
	// retention question (design §6.3), and this port neither scrolls a
	// viewport nor reads one. A row outside the active area is
	// [ErrOutOfRange].
	Row(y int) (Row, error)

	// Cell returns one position of the active screen, copied.
	//
	// It exists beside Row because the two granularities are both wanted — a
	// renderer paints lines, a query asks about a position — and because they
	// are the same read rather than two implementations of one: an
	// implementation that reads a cell reads it here and uses it for both.
	Cell(x, y int) (Cell, error)

	// Screen reports which buffer is active. It is the answer to "what is the
	// client being sent while a full-screen program owns the pane", and reads
	// follow it: Row and Cell read whichever screen this names.
	//
	// The read can fail, and a failure is not the primary screen: a buffer the
	// emulator could not report is one nobody read, and reporting it as primary
	// would be a claim about the screen a client is being sent.
	Screen() (Screen, error)

	// EncodeKey encodes one key event into the bytes to write to the PTY,
	// DRIVEN FROM THE TERMINAL'S OWN STATE. A program that turned on
	// application cursor keys gets ESC O D for Left; the same key in a program
	// that did not gets ESC [ D, with nothing in the caller's request saying
	// which — because a client cannot know, and a client that encoded its own
	// keys would send differently encoded input while the screen looked correct
	// (design §6.1).
	//
	// A key press the encoder produces nothing for returns an empty slice and
	// no error: a modifier key on its own is a real event with no bytes.
	EncodeKey(ev KeyEvent) ([]byte, error)

	// Effects returns the non-visual effects the program's output produced
	// since the previous call, oldest first, and starts a fresh list.
	//
	// It is the other half of Ingest: a bell, a title, a clipboard write and a
	// reported directory change no cell, so no read of the screen can find
	// them, and a program that sets a title and rings has asked for two things
	// a runtime must act on (design §6.2). They come out of the emulator
	// rather than going to a callback the caller installs because they arrive
	// on whatever goroutine is feeding the program's output, and a runtime
	// that was called back from inside an ingest could not answer without
	// re-entering a terminal whose lock the caller is holding.
	//
	// The list is the CALLER'S once it is returned: the bytes in it were copied
	// out of the emulator's borrowed memory, and the next call starts empty, so
	// an effect is read exactly once and by one reader.
	Effects() []Effect

	// Paste hands the terminal a paste of text and returns the bytes the
	// program is to be sent, framed per the TERMINAL'S OWN state: bracketed
	// when the program enabled mode 2004 and passed through when it did not.
	// A paste of nothing produces no bytes and no error.
	//
	// "Passed through" is the framing and not a promise about every byte. A
	// paste is not typing: the terminal removes the bytes that cannot travel
	// through one — a control byte that would be read as input rather than as
	// text is replaced — and a newline in an unbracketed paste becomes the
	// carriage return that keeps the text on the program's current line. The
	// returned bytes are the terminal's own answer for the text, and a caller
	// must read them rather than assume they are the slice it handed over.
	//
	// The caller cannot do this itself. Bracketed paste is a mode the program
	// set, mode 2004 is only one of the things a paste's framing depends on,
	// and the sequences that have to be wrapped around the text are the
	// terminal's own — a caller that wrapped them by itself would eventually
	// wrap a paste the program did not ask to be bracketed, which turns a
	// paste into typed input at a shell prompt.
	//
	// The bytes are RETURNED rather than written, exactly as Ingest returns the
	// program's replies: the caller owns the one ordered write path to the PTY,
	// and a port that wrote here would put a paste's bytes in front of a
	// reply's or behind them depending on which goroutine won.
	Paste(text []byte) ([]byte, error)

	// Mouse hands the terminal one mouse event and returns the bytes the
	// program is to be sent, in the tracking mode and output format the
	// PROGRAM set. With no tracking mode enabled it reports [ErrUnsupported]
	// and no bytes: the event is not input, and a caller must not have to
	// invent that answer.
	//
	// Which is the whole reason this is a method and not a caller's string
	// concatenation. A program chooses between X10, normal, button and
	// any-event tracking, and between the legacy byte form, UTF-8, SGR and
	// the pixel form; the same click is four different sequences depending on
	// what it chose and nothing in the caller's event says which. The
	// terminal knows, because it is where the modes were set.
	//
	// Returned rather than written, for the same reason Paste's bytes are.
	Mouse(ev MouseEvent) ([]byte, error)

	// Focus hands the terminal one focus report and returns the bytes the
	// program is to be sent when it asked for focus reporting (DEC private
	// mode 1004), and [ErrUnsupported] with no bytes when it did not: a program
	// that did not ask is not told.
	//
	// The mode is the whole content of the method. A surface knows its window
	// gained or lost focus and can say so; only the terminal knows whether the
	// program running inside it asked to hear about it, and a caller that sent
	// the report anyway would be typing into the program's input stream.
	Focus(gained bool) ([]byte, error)

	// Close releases the terminal and everything it owns. It is idempotent:
	// a runtime closing deliberately and a deferred close are both normal, and
	// the second must be harmless. After it returns, every method that reads or
	// writes the terminal returns [ErrClosed]; the two value reads — Geometry
	// and Screen — have no error to return and report their zero values, which
	// is a size no terminal can have and therefore no answer at all.
	Close()
}
