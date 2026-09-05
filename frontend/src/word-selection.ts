// One word-selection policy for BOTH surfaces that show command text
// (nocx-w7h.8): the live xterm terminal, which has its own `wordSeparator`
// option, and the frozen command block, whose serialized DOM otherwise
// falls back to the browser's native word segmentation — which is what
// stopped at the `-` and `.` in `profile-usage.json`.
//
// The set is xterm's own default, made explicit and shared so the two
// surfaces cannot drift: whitespace and the bracket/quote/comma/backtick
// punctuation separate tokens. `-`, `.`, `/`, `:`, `=` and `@` are
// deliberately NOT separators — a filename, a path, a URL, an scp-style
// `user@host:~/path` and a flag like `--no-verify` are one thing to a
// terminal user, and stopping inside them makes double-click useless for
// the case people actually use it for. (A pipe or ampersand is likewise
// not a separator, matching xterm's default exactly.)
export const WORD_SEPARATORS = ' ()[]{}\',"`'

/** The maximal [start, end) run of non-separator chars around `offset`.
 *  A click on a separator yields start === end — nothing to select. The
 *  offset is clamped to the last char so a click at the very end of the
 *  text selects the final word instead of an empty range. */
export function wordBounds(text: string, offset: number): { start: number; end: number } {
  if (text.length === 0) return { start: 0, end: 0 }
  const at = Math.max(0, Math.min(offset, text.length - 1))
  // A click ON a separator selects nothing; a click at the very end of the
  // text clamps to the last char, which is a word char, so the final word
  // is selected rather than an empty range.
  if (WORD_SEPARATORS.includes(text[at])) return { start: at, end: at }
  let start = at
  let end = at
  while (start > 0 && !WORD_SEPARATORS.includes(text[start - 1])) start--
  while (end < text.length && !WORD_SEPARATORS.includes(text[end])) end++
  return { start, end }
}

/** A frozen line's text flattened into one string with a per-char map back
 *  to (text node, offset). The serializer emits one `<span>` per colour run,
 *  so a token can straddle span boundaries in colourised output; a node-local
 *  walk would truncate it at the markup. Built on demand per double-click. */
export interface FlattenedLine {
  text: string
  /** One entry per char of `text`, in document order. */
  chars: Array<{ node: Text; offset: number }>
}

export function flattenLine(root: Element): FlattenedLine | null {
  const chars: Array<{ node: Text; offset: number }> = []
  let text = ''
  const walker = root.ownerDocument.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let n: Node | null
  while ((n = walker.nextNode())) {
    const t = n as Text
    const content = t.textContent ?? ''
    for (let i = 0; i < content.length; i++) chars.push({ node: t, offset: i })
    text += content
  }
  if (chars.length === 0) return null
  return { text, chars }
}

/** The flattened index of a click at (node, offset). A node's chars are
 *  contiguous in the map, so the first entry for the node anchors the rest.
 *  Returns -1 when the node is not part of the line. */
export function charIndexAt(flat: FlattenedLine, node: Text, offset: number): number {
  for (let i = 0; i < flat.chars.length; i++) {
    if (flat.chars[i].node === node) return i + offset
  }
  return -1
}

/** The (node, offset) of the char at flattened `index`, clamped to the last
 *  char so an end-of-line bound still resolves. Module-private again: the
 *  link decorator used to build one Range per link from it, and now groups
 *  the link's characters BY NODE instead (nocx-ec18), because a Range that
 *  crosses a `.term-cell` boundary splits the box when it is extracted. It
 *  still reads `flat.chars` and no second walker exists — the shared answer
 *  to "where is char N of this row" is `flattenLine`, which is exported. */
function charPos(flat: FlattenedLine, index: number): { node: Text; offset: number } {
  const clamped = Math.max(0, Math.min(index, flat.chars.length - 1))
  return flat.chars[clamped]
}

/** The Range covering the word around (node, offset) within `root` (a
 *  `.term-line` or `.cmd-header-text`), or null when the click landed on a
 *  separator or outside the line. The selection may span several text nodes. */
export function wordRangeIn(root: Element, node: Text, offset: number): Range | null {
  const flat = flattenLine(root)
  if (!flat) return null
  const idx = charIndexAt(flat, node, offset)
  if (idx < 0) return null
  const { start, end } = wordBounds(flat.text, idx)
  if (start === end) return null
  const startPos = charPos(flat, start)
  const endPos = charPos(flat, end - 1) // end is exclusive
  const range = root.ownerDocument.createRange()
  range.setStart(startPos.node, startPos.offset)
  range.setEnd(endPos.node, endPos.offset + 1)
  return range
}
