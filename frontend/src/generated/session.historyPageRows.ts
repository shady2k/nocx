/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.historyPageRows.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * One position whose codepoint count or column width departs from the default (1, 1): [position, codepoints, width].
 *
 * @minItems 3
 * @maxItems 3
 */
export type Mark = [number, number, 1 | 2 | 4]
/**
 * One maximal stretch of adjacent columns that share one style: [style, length], the style first and the number of columns it covers second. Length is at least 1 — a run that covered nothing would be sender noise, and two adjacent runs are never the same style.
 *
 * @minItems 2
 * @maxItems 2
 */
export type Run = [Style, number]
/**
 * The complete visual style of one run (emulator.Style), shared across every column the run covers rather than repeated per cell. Either the bare integer 0 — every field is the zero value, the terminal's own default colours, no attributes, no underline, by far the common case for plain output — or the 5-element tuple [foreground, background, underlineColor, attributes, underline] when anything departs from that.
 */
export type Style = 0 | [Color, Color, Color, number, 0 | 1 | 2 | 3 | 4 | 5]
/**
 * One style colour, packed into a single integer (emulator.Color) rather than a named-field object — the distinction between a palette index, an exact RGB value and the terminal's own default is load-bearing (a palette index repaints when the theme changes, RGB does not), but at a run per style change spelling it as three ranges of one integer costs nothing a receiver cannot undo cheaply. 0 is the terminal's own default colour for the role (emulator.ColorDefault, the zero value an unstyled style holds). 1 through 256 is a palette index plus one (emulator.ColorPalette, the index is the value minus one) — shifted up because 0 is taken by default. 257 and above is an exact colour (emulator.ColorRGB): subtract 257 and read the low 24 bits as r<<16 | g<<8 | b, one byte per channel.
 */
export type Color = number

/**
 * One page of a session's LIVE history, as the screen carrier carries it (ADR-0073): the rows of the emulator's own scrollback, in the frame contract's own text+marks+runs vocabulary (nocx-zg3k3.2.12), keyed by the page id the session.historyPage result named. The rows ride the BINARY data plane on the screen frame's own carrier and never a JSON-RPC result (AD-1, ADR-0066, nocx-zg3k3.10.3); this document is that carrier's cargo, exactly as session.frame.schema.json is the published screen's. The $defs below are the frame contract's, copied: one rows vocabulary, encoded by ONE encoder (sessionruntime.EncodeRows), consumed by the same cell model that paints a frame. A guard test keeps the copy identical to the original rather than trusting either hand.
 */
export interface SessionHistoryPageRows {
  /**
   * The page this document carries, minted by the coordinator for one session.historyPage call. The JSON-RPC result names it, so a client matches the rows to the answer they belong to without ordering the two planes against each other.
   */
  pageId: string
  /**
   * The page's rows, OLDEST FIRST, in the frame contract's rows shape: rows[i] is absolute history row Start+i of the result that named this page. A row's own explicit content may be shorter than the geometry's columns; the trailing run of default blanks is implied, exactly as a frame's is.
   */
  rows: Row[]
}
/**
 * One physical line of the screen (emulator.Row), in the compact text+marks+runs shape (nocx-zg3k3.2.12). A POSITION is one surviving column-owning entry once the spacer that follows a wide cluster is folded away — text and marks are indexed by position. A COLUMN is the grid's own unit — runs are measured in columns, and a wide cluster is one position but two columns. `text`, `marks` and `runs` together decode against nothing outside themselves (self-describing), which is what lets a stored row (scrollback) survive without the frame it arrived in; the ONLY external fact a receiver needs is geometry.cols, to pad the implied trailing default blanks a shorter row does not carry.
 */
export interface Row {
  /**
   * The row's clusters, in column order, concatenated: one grapheme per surviving position (a wide cluster's spacer is not repeated, and a position with no text — Cell.HasText false — contributes nothing, an empty string). A receiver splits this back into per-position graphemes using `marks`' codepoint counts (default one codepoint per position) and Unicode CODEPOINTS as the unit — Go's []rune and JavaScript's Array.from agree on that unit without either re-deriving cluster boundaries the runtime already decided (ADR-0065). A trailing run of ordinary default blank positions is not represented here at all: the row's explicit position count (derived from this string's length and `marks`) may be less than geometry.cols, and the remainder is implied.
   */
  text: string
  /**
   * The sparse positions whose codepoint count or column footprint is not the default (one codepoint, one column): [position, codepoints, width] triples, position 0-based into the surviving-position sequence `text` encodes. codepoints is the position's grapheme length in Unicode codepoints — 0 for a cell with no text (Cell.HasText false; its grapheme is the empty string), 2 or more for a cluster built of several codepoints (a combining mark, a ZWJ sequence). width is the cell's column footprint (Cell.Width, emulator.Width): 2 wide (the position occupies this column and the next, whose own spacer contributes no position of its own) or 4 spacerHead (the column a wide cluster would have needed at the end of a soft-wrapped line, carries nothing, is not rendered) — 1 narrow is the default and never appears here, and 3 spacerTail never appears here because a spacer is not a position at all in this vocabulary, only the extra column its preceding wide cluster's mark declares. Absent (or a position it does not name) means (1, 1): one codepoint, one column. A malformed row (a mark whose codepoints exceeds what `text` has left, or two marks for the same position) is malformed at the source.
   */
  marks?: Mark[]
  /**
   * The styles of this row as [style, length] tuples, length measured in COLUMNS (a wide cluster's synthesised spacer column counts, and carries the SAME style its cluster's run covers it with — the two are read independently at the source and a run is who says whether they in fact agree). Each run covers that many consecutive columns starting where the previous run ended, and the lengths sum to the row's own explicit column count (positions, plus one more for every wide-marked position) — never geometry.cols, which a shorter row does not reach. Maximal by construction: two adjacent runs never carry the same style, or the sender would have merged them. Absent means the implicit single run: the row's whole explicit width, in the default style (the bare integer 0) — the shortcut a fully unstyled row, or the unstyled remainder of an otherwise-trimmed one, takes. A malformed row (present runs whose lengths do not sum to the explicit column count) is malformed at the source.
   */
  runs?: Run[]
  /**
   * The row is not the last physical line of its logical line: a program's output wrapped here. Together with continuation this is what lets a receiver join the physical lines of one logical line without losing a hard newline. Absent means false.
   */
  wrap?: boolean
  /**
   * The row continues the logical line of the row above it. Wrap and continuation are not each other's negation: the last row of a wrapped sequence has wrap false and continuation true, and the row before it has wrap true and continuation false. Absent means false.
   */
  continuation?: boolean
}
