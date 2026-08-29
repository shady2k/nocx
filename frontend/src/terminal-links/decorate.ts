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
// Wrapping is destructive to the flattened map, so spans are applied LAST
// FIRST. `Range.extractContents` may split the text node it starts in, and a
// split keeps the head in the original node — so every offset before the cut
// stays valid, and offsets after it have already been consumed.
// ═══════════════════════════════════════════════════════════════════════════

import { flattenLine, charPos } from '../word-selection'
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
  for (let i = spans.length - 1; i >= 0; i--) {
    const span = spans[i]
    const doc = row.ownerDocument
    const start = charPos(flat, span.from)
    const end = charPos(flat, span.to - 1)
    const range = doc.createRange()
    range.setStart(start.node, start.offset)
    range.setEnd(end.node, end.offset + 1)
    // An anchor with NO href: semantically a link for assistive technology,
    // and inert to the webview's navigation — a real href pointing at a
    // path would let a click replace the app's own document.
    const a = doc.createElement('a')
    a.className = LINK_CLASS
    writeTarget(a, span.target)
    a.appendChild(range.extractContents())
    range.insertNode(a)
  }
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
