// One place that paints shell into an element, and catches up when the
// grammar finishes loading.
import { highlightShellText, onShellHighlightReady } from '../shell-highlight'
import type { CommandSnapshotStore } from '../command-snapshot'

// ── Painting into an element, and catching up when the grammar lands ────────
//
// The Shiki grammar loads asynchronously at module init, so anything painted
// in the few milliseconds before it resolves would stay plain forever. Spans
// painted pre-ready are registered here and repainted once the tokenizer
// exists; after that the registration is a no-op.
//
// THREE callers need it now: a frozen block's header, a row inside a shell
// fence in a streamed answer, and the same row in a RESTORED answer
// (nocx-4em1z). It used to sit in blocks.ts, where the first two are — but
// the third arrives through answer-body.ts, which blocks.ts imports, and one
// registry cannot be reached from both ends of an import edge.
//
// It is a module of its own, and here rather than beside the lexer, because
// this is where the lexer's output is WRITTEN INTO THE DOM: ADR-0014 keeps
// markup-writing inside the terminal-owned files, and shell-highlight.ts is
// not one of them. It returns a string; putting the assignment there would
// have moved an innerHTML write into a surface the ADR deliberately
// excludes, to save one file.

let tokenizerLoaded = false
/** Spans painted before the grammar loaded, keyed by the tab's snapshot
 *  store so the repaint judges against the right tab's command set. */
const pendingHighlightSpans = new Map<HTMLElement, CommandSnapshotStore>()

onShellHighlightReady(() => {
  tokenizerLoaded = true
  for (const [el, store] of pendingHighlightSpans) {
    const text = el.textContent ?? ''
    if (text && text !== '(empty)') el.innerHTML = highlightShellText(text, store)
  }
  pendingHighlightSpans.clear()
})

/**
 * Paint `text` into `el` as shell, and register the element for a repaint if
 * the grammar has not loaded yet. The ONE way to put highlighted shell into
 * an element — a caller that assigns innerHTML itself gets the pre-ready
 * behaviour wrong and has no way to catch up.
 */
export function paintShellInto(el: HTMLElement, text: string, store: CommandSnapshotStore): void {
  el.innerHTML = highlightShellText(text, store)
  if (!tokenizerLoaded) pendingHighlightSpans.set(el, store)
}
