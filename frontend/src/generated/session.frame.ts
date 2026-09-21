/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.frame.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * One full screen of cells, as the session runtime already holds it — the shape the client will paint instead of parsing terminal bytes (nocx-zg3k3.2.1). Every name and semantic here is taken from the Go types the frame is read from: Snapshot (internal/sessionruntime/contract.go) for revision, rows, cursor and committed geometry, and emulator.Row, Cell, Style, Color, Cursor, Width (internal/emulator/emulator.go) for the cells themselves. Nothing sends this yet; the task that makes the runtime send a frame inherits this declaration rather than coining a second one. A cell-shaped wire format already exists — contracts/helper/identities.schema.json $defs screenCell and screenFrame, the FROZEN coordinator-to-helper ABI — and it was considered and does not fit: it carries no style and no has-text fact, so a cell painted with a background and no text loses both; its rows carry no wrap/continuation, which is what lets a scrollback renderer join the physical lines of one logical line; and its width vocabulary is 0-2, while emulator.Width is a five-value enumeration whose spacer head the helper ABI has no spelling for. Extending a frozen ABI for a renderer need was the wrong direction, so this schema declares the renderer's own vocabulary once, from the same Go source the helper's was derived from.
 */
export interface SessionFrame {
  /**
   * The runtime's monotonic clock this frame belongs to (Snapshot.Revision, Revision uint64). Everything a client is handed — the cards, the committed geometry, the live frame — is relative to one revision, because a card sealed between two independent reads is otherwise in neither of them.
   */
  revision: number
  /**
   * The committed geometry (sessionruntime.GeometryCommit, emulator.Geometry): a size the PTY and the emulator BOTH took, and the geometry every cell of this frame was read at. The pixel size travels because it is not decoration — the emulator answers a program's own size queries from it, so a frame without it forces the receiver to invent a cell size. A rows array whose length is not this rows count, or a row whose cells are not this cols long, is malformed at the source.
   */
  geometry: {
    /**
     * Columns of the cell grid. A commit is published only for a geometry both sides took, and a size with zero columns is not a terminal (Geometry.Valid).
     */
    cols: number
    /**
     * Rows of the cell grid.
     */
    rows: number
    /**
     * One cell's width in pixels.
     */
    cellWidthPx: number
    /**
     * One cell's height in pixels.
     */
    cellHeightPx: number
    /**
     * The revision at which this geometry was committed (GeometryCommit.Revision), so a receiver can tell a frame read at the commit in force from one read across a resize.
     */
    revision: number
  }
  /**
   * The caret (emulator.Cursor): position and visibility are one read, because a position from one moment and a visibility from another describe a caret that never existed — DECTCEM (mode 25) lets a program hide the cursor while it composes a frame, and a renderer that painted a visible one there would show the person something the program deliberately withdrew.
   */
  cursor: {
    /**
     * Cursor column, zero-indexed from the top-left of the ACTIVE AREA — the same grid the rows were read from, not the scrollback and not a viewport somebody has scrolled. Home is (0, 0).
     */
    x: number
    /**
     * Cursor row, zero-indexed like x.
     */
    y: number
    /**
     * Whether the program has left the caret visible at all (DECTCEM, mode 25).
     */
    visible: boolean
  }
  /**
   * Every row of the active screen (Snapshot.Rows), in order, as the emulator holds it: a RECTANGLE and not trimmed text — the array is geometry.rows long and every row's cells is geometry.cols long, so a receiver can index a column without knowing where a program stopped writing.
   */
  rows: Row[]
}
/**
 * One physical line of the screen (emulator.Row).
 */
export interface Row {
  /**
   * One entry per column, including the spacers of wide clusters and the blank cells of trailing space (emulator.Row.Cells): geometry.cols long, every row, every frame. A receiver that wants text skips the spacer cells rather than receiving a shorter array.
   */
  cells: Cell[]
  /**
   * The row is not the last physical line of its logical line: a program's output wrapped here. Together with continuation this is what lets a receiver join the physical lines of one logical line without losing a hard newline.
   */
  wrap: boolean
  /**
   * The row continues the logical line of the row above it. Wrap and continuation are not each other's negation: the last row of a wrapped sequence has wrap false and continuation true, and the row before it has wrap true and continuation false.
   */
  continuation: boolean
}
/**
 * One grid position, copied out of the emulator (emulator.Cell).
 */
export interface Cell {
  /**
   * The whole cluster — the base codepoint followed by every combining codepoint the terminal assembled into it — and not one rune (Cell.Grapheme). Empty for a blank cell or a continuation column. A receiver that wants a character takes this; one that wants a position takes the column index, because width says how many columns this cell occupies and the next cell may be its spacer.
   */
  grapheme: string
  /**
   * The cell's column footprint (Cell.Width, emulator.Width) — the thing a renderer must have right and the thing a font measurement must never be asked about. The enumeration mirrors the Go constants exactly: 0 unknown, the zero value of a cell that was never read and one a committed frame does not carry; 1 narrow, one column; 2 wide, two columns, the cell after it the continuation; 3 spacerTail, the second column of a wide cluster, not rendered because the cluster to its left already covered it; 4 spacerHead, the column a wide cluster would have needed at the end of a soft-wrapped line, carries nothing, is not rendered.
   */
  width: 0 | 1 | 2 | 3 | 4
  /**
   * Separate from grapheme being empty because the two are different facts (Cell.HasText): a cell can carry a background colour and no text, and a renderer must paint the first and not the second.
   */
  hasText: boolean
  style: Style
}
/**
 * The complete visual style of one cell (emulator.Style).
 */
export interface Style {
  foreground: Color
  background: Color
  underlineColor: Color
  /**
   * The on/off text decorations as one bitset (emulator.Attributes, uint16) — a bitset rather than eight booleans because that is how they are compared, stored and sent, and because the set is closed: a terminal has these eight and no ninth. Bit 0 bold, 1 italic, 2 faint, 3 blink, 4 inverse, 5 invisible, 6 strikethrough, 7 overline.
   */
  attributes: number
  /**
   * The shape of the underline decoration, which SGR 4:0-4:5 can choose and which is not a boolean (emulator.Underline). 0 none, 1 single, 2 double, 3 curly, 4 dotted, 5 dashed.
   */
  underline: 0 | 1 | 2 | 3 | 4 | 5
}
/**
 * One style colour, in whichever of the three shapes it is in (emulator.Color). The distinction is load-bearing rather than decorative: a palette index means 'this theme's colour N' and repaints when the theme changes, an RGB value means 'exactly this' and does not, and a default colour means the theme's own choice for the role. The fields not named by kind are meaningless and ride the wire zeroed, exactly as the Go struct holds them; a receiver reads a colour through its kind.
 */
export interface Color {
  /**
   * Which of the three shapes this colour is in (emulator.ColorKind). 0 default, the terminal's own colour for the role and the zero value an unstyled cell holds; 1 palette, an index into the terminal's 256-colour palette; 2 rgb, an exact colour.
   */
  kind: 0 | 1 | 2
  /**
   * The palette index, meaningful when kind is 1.
   */
  palette: number
  /**
   * An exact colour, one byte per channel (emulator.RGB), meaningful when kind is 2.
   */
  rgb: {
    r: number
    g: number
    b: number
  }
}
