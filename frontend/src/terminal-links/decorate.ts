// ═══════════════════════════════════════════════════════════════════════════
// Frozen scrollback → clickable links.
//
// The frozen half of a terminal is DOM, not a grid: `serializeRange` emits
// one `<span class="term-line">` per logical row, holding one `<span>` per
// colour run (scrollback/serializer.ts). So the live region's xterm link
// provider does not reach it, and this is the second surface that has to be
// taught the same grammar — which is exactly why the grammar is a module of
// its own and not a regex in either of them.
//
// The walk is `flattenLine` from word-selection.ts, unchanged and unforked.
// That module was written because a token can STRADDLE colour runs and a
// node-local walk truncates it at the markup — the same defect, one surface
// over. Asking it here rather than re-deriving "where is char N of this row"
// is AD-8 applied to a helper: one owner per behaviour.
//
// ONE ANCHOR PER TEXT NODE, not one per link — and that is the whole of
// nocx-ec18's link half. A single Range over the link and one
// `extractContents` re-parents everything between its ends, so when an end
// falls INSIDE a `.term-cell` the browser splits that box: the original is
// left empty and a clone of it goes inside the `<a>`. A frozen row's boxes
// are what hold it on its columns, so a split box is a row that no longer
// occupies its grid width — the defect this whole change exists to remove,
// re-introduced by the decorator on every row carrying a link.
//
// Wrapping each text node's own slice instead never moves a node out of its
// parent: the box stays a box and gains an `<a>` around its own text. The
// cost is named and deliberate — a link straddling colour runs or boxes is
// now SEVERAL anchors rather than one, so every one of them carries the
// target (surface.ts reads it off whichever element was hit) and assistive
// technology sees N links where a person sees one. That accessibility debt
// is recorded rather than paid here; paying it means a focus order and
// keyboard activation, which is `surface.ts`, not geometry.
//
// Wrapping is destructive to the flattened map, so segments are applied LAST
// FIRST, spans and segments both. `Range.surroundContents` extracts and
// re-inserts within one text node, and the extract keeps everything before
// the cut in the ORIGINAL node — so every offset below the one just consumed
// stays valid, and no offset above it is ever asked for again.
// ═══════════════════════════════════════════════════════════════════════════

import { flattenLine, type FlattenedLine } from '../word-selection'
import { detectLinks, type LinkTarget } from './detect'

/** Identity class for a decorated link. Styling hangs off it; so does the
 *  delegated click handler, which is why it is exported rather than spelled
 *  again at each call site. */
export const LINK_CLASS = 'term-link'

/**
 * Decorate every row under `root` (and `root` itself, when it is a row).
 *
 * Idempotent by inspection rather than by a marker attribute: a row that
 * already holds a link has been decorated, and adding a `data-` flag to say
 * so would change the serialized HTML of every row in the scrollback for the
 * benefit of a check that costs one selector.
 */
export function decorateLinks(root: Element): void {
  if (root.matches(`.term-line`)) decorateRow(root)
  for (const row of root.querySelectorAll('.term-line')) decorateRow(row)
}

function decorateRow(row: Element): void {
  if (row.querySelector(`.${LINK_CLASS}`) !== null) return
  const flat = flattenLine(row)
  if (flat === null) return
  const spans = detectLinks(flat.text)
  const doc = row.ownerDocument
  for (let i = spans.length - 1; i >= 0; i--) {
    const span = spans[i]
    const segments = segmentsOf(flat, span.from, span.to)
    for (let j = segments.length - 1; j >= 0; j--) {
      const seg = segments[j]
      const range = doc.createRange()
      range.setStart(seg.node, seg.start)
      range.setEnd(seg.node, seg.end)
      // An anchor with NO href: semantically a link for assistive technology,
      // and inert to the webview's navigation — a real href pointing at a
      // path would let a click replace the app's own document.
      const a = doc.createElement('a')
      a.className = LINK_CLASS
      // On EVERY segment, not only the first. attachLinkClicks reads the
      // target off whatever element the click landed on (surface.ts), so a
      // segment without one is half a link that opens nothing.
      writeTarget(a, span.target)
      // Surrounds within ONE text node, so the node's parent — a colour run,
      // or a `.term-cell` — is never opened up and never re-parented.
      range.surroundContents(a)
    }
  }
}

/** The link's characters grouped by the text node holding them, in document
 *  order. `flat.chars` already names the node of every character, so the
 *  grouping is a walk of the same array the spans were detected over — no
 *  second answer to "where is char N of this row" (AD-8). Runs are broken on
 *  a node change OR on a gap in the offsets, so a node that appears twice in
 *  the row cannot merge two disjoint slices into one range. */
function segmentsOf(
  flat: FlattenedLine,
  from: number,
  to: number,
): Array<{ node: Text; start: number; end: number }> {
  const segments: Array<{ node: Text; start: number; end: number }> = []
  const last = Math.min(to, flat.chars.length)
  for (let i = Math.max(0, from); i < last; i++) {
    const { node, offset } = flat.chars[i]
    const open = segments[segments.length - 1]
    if (open !== undefined && open.node === node && open.end === offset) {
      open.end = offset + 1
    } else {
      segments.push({ node, start: offset, end: offset + 1 })
    }
  }
  return segments
}

/** Put the target on the element, so a click reads it back instead of
 *  re-running the grammar over text the DOM has since rearranged. */
function writeTarget(el: HTMLElement, target: LinkTarget): void {
  el.dataset.linkKind = target.kind
  if (target.kind === 'url') {
    el.dataset.linkUrl = target.url
    return
  }
  el.dataset.linkPath = target.path
  if (target.line !== undefined) el.dataset.linkLine = String(target.line)
  if (target.col !== undefined) el.dataset.linkCol = String(target.col)
}

/** The target a decorated element carries, or null when it carries none —
 *  what a delegated click handler asks of whatever the user hit. */
export function linkTargetOf(el: Element | null): LinkTarget | null {
  if (el === null || !(el instanceof HTMLElement)) return null
  const kind = el.dataset.linkKind
  if (kind === 'url') {
    const url = el.dataset.linkUrl
    return url === undefined ? null : { kind: 'url', url }
  }
  if (kind !== 'path') return null
  const path = el.dataset.linkPath
  if (path === undefined) return null
  const target: LinkTarget = { kind: 'path', path }
  const line = el.dataset.linkLine
  const col = el.dataset.linkCol
  if (line !== undefined) target.line = Number(line)
  if (col !== undefined) target.col = Number(col)
  return target
}
