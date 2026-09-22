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
 * One grid position, copied out of the emulator (emulator.Cell), as the positional tuple [grapheme, width, hasText]: position 0 the whole cluster, position 1 its column footprint, position 2 whether it carries text. Positional, not named, because at thousands of cells a frame the field-name bytes are the payload — measured 2026-09-22, named-field cells with shared runs still bust the 256 KiB lifecycle bound at 200x50 (444,433 B) while this tuple form fits (152,937 B). The style is not here: it is the thing a run shares across adjacent cells.
 *
 * @minItems 3
 * @maxItems 3
 */
export type Cell = [string, 0 | 1 | 2 | 3 | 4, boolean]
/**
 * One maximal stretch of adjacent cells that share one style: [style, length], the style first and the number of cells it covers second. Length is at least 1 — a run that covered nothing would be sender noise, and two adjacent runs are never the same style.
 *
 * @minItems 2
 * @maxItems 2
 */
export type Run = [Style, number]

/**
 * One full screen of cells, as the session runtime already holds it — the shape the client will paint instead of parsing terminal bytes (nocx-zg3k3.2.1). Every name and semantic here is taken from the Go types the frame is read from: Snapshot (internal/sessionruntime/contract.go) for revision, rows, cursor and committed geometry, and emulator.Row, Cell, Style, Color, Cursor, Width (internal/emulator/emulator.go) for the cells themselves. Nothing sends this yet; the task that makes the runtime send a frame inherits this declaration rather than coining a second one. A cell-shaped wire format already exists — contracts/helper/identities.schema.json $defs screenCell and screenFrame, the FROZEN coordinator-to-helper ABI — and it was considered and does not fit: it carries no style and no has-text fact, so a cell painted with a background and no text loses both; its rows carry no wrap/continuation, which is what lets a scrollback renderer join the physical lines of one logical line; and its width vocabulary is 0-2, while emulator.Width is a five-value enumeration whose spacer head the helper ABI has no spelling for. Extending a frozen ABI for a renderer need was the wrong direction, so this schema declares the renderer's own vocabulary once, from the same Go source the helper's was derived from. SHAPE (nocx-zg3k3.2.6): a style is shared across a run of adjacent cells rather than repeated per cell, and a cell rides as a positional tuple [grapheme, width, hasText] rather than a named-field object. Both were forced by measurement, recorded 2026-09-22 against a realistic dense screen (prompt, coloured output, truecolour banner, CJK wide clusters, ZWJ emoji, cell-fit-boxed glyph, inverse status bar): with the original per-cell style object a frame was 521,989 B at 80x24, 1,303,538 B at 120x40 and 2,713,770 B at 200x50 — over the 256 KiB lifecycle bound (internal/lifecycle/protocol.go MaxFrameBytes) at EVERY geometry and over the 1 MiB helper bound (internal/helper/proto/frame.go MaxFrameBytes) at 120x40 and 200x50. Cells as named-field objects with per-row style runs still measured 444,433 B at 200x50 — the bare per-cell payload alone busts the lifecycle bound — so the tuple form is what makes the rectangle fit: 35,967 B, 81,124 B and 152,937 B at the three geometries. Two run shapes were measured: runs-per-row (each row carries its styles as [style, length] runs alongside its cells) and a frame-level style table (the frame carries styles: [Style, ...], each run an index into it). The frame table is smaller — 28,541 / 67,121 / 135,423 B, an 11.5-20.6% saving — but both shapes sit 40-70% below the binding bound at every geometry, so the saving buys nothing the bound does not already give, and it costs every row its self-description: a row stored for scrollback would decode only against the frame-level table it rode in with, and one wrong index corrupts the whole frame instead of one run. Runs-per-row was taken. This schema declares the draft-07 dialect because positional tuples are how draft-07 spells a fixed-shape array (items as a schema list, additionalItems: false) — the same declaration agent.dump.schema.json already carries — and the type generator (json-schema-to-typescript) emits tuple types only for that spelling.
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
 * One physical line of the screen (emulator.Row). Its styles ride as runs — maximal adjacent stretches of one style — not per cell: the runs are adjacent, in order, and partition the row's cells exactly (the sum of run lengths is cells' length), so a receiver walks runs in parallel with cells, reading each cell's style from the run that covers it. A row stays self-describing: it decodes against nothing outside itself, which is what lets a stored row (scrollback) survive without the frame it arrived in.
 */
export interface Row {
  /**
   * One entry per column, including the spacers of wide clusters and the blank cells of trailing space (emulator.Row.Cells): geometry.cols long, every row, every frame. A receiver that wants text skips the spacer cells rather than receiving a shorter array. Each cell is the positional tuple [grapheme, width, hasText] — the genuinely per-cell facts; the style that used to be a fourth field here is the thing a run shares across adjacent cells.
   */
  cells: Cell[]
  /**
   * The styles of this row as [style, length] tuples, each covering that many consecutive cells starting where the previous run ended. Maximal by construction: two adjacent runs never carry the same style, or the sender would have merged them. A malformed row (runs whose lengths do not sum to the cells length) is malformed at the source.
   *
   * @minItems 1
   */
  runs: [Run, ...Run[]]
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
 * The complete visual style of one cell (emulator.Style), shared across a run of adjacent cells rather than repeated per cell — the 2026-09-12 design's measured expectation is frame traffic at about 1.04x raw output, which is unreachable while a style object rides every cell.
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
