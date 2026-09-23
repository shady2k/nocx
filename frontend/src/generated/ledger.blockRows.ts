/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ledger.blockRows.schema.json
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
 * The stored form of a streamed block's output (nocx-2v80t.3.7): an artifact of media type application/x-nocx-rows whose body is JSON Lines — ONE line per row that left the screen, each carrying the absolute index the row departed at and the row itself in the SAME cell vocabulary the live screen frame declares (session.frame.schema.json). The row, cell, run, style and color definitions here are an IDENTICAL COPY of session.frame's, not a second vocabulary: a stored row must paint with the same painter and decode against nothing outside itself, which is what lets a block survive without the frame it arrived in. The copy is deliberate — cross-file $refs generate named renderer types the dead-export ratchet counts — and it is guarded by a test that compares the two schemas' definitions canonically, so the copies cannot drift. A client reads a block's rows through the existing read path: ledger.artifact joins the chunks in seq order into this body, and ledger.get carries the artifact's metadata — truncated 'cap' when the per-command cap dropped the middle, and a payload sidecar (LedgerBlockRowsSummary) recording how many rows the cap dropped and how many the emulator lost. One line parses on its own; the whole body is the concatenation of its lines, in order. Stored rows may omit trailing ordinary default blank cells; styled blanks and wide-cluster spacer cells remain, and the reader pads the omitted default tail before painting.
 */
export interface LedgerBlockRowsLine {
  /**
   * The row's absolute index — rows ever departed in the session, not rows in this block — so a stored block names where each of its rows sits even after the cap has taken a middle stretch. The first line's from is where the block begins, which may be past zero: rows that departed before the command's authenticated start belonged to no block.
   */
  from: number
  row: Row
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
