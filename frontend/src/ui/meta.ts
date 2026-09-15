// Meta — the kit's inline fact: where a command ran, how long it took, and the
// status word when there is one (spec 2026-09-14 §6.1). Muted text, one line,
// tabular figures, parts joined by a separator that assistive tech skips.
//
// Vanilla-emitted, like SecretChip and ModeIndicator: the terminal screen that
// uses it is imperative DOM (ADR-0012), and it is built once per block. No
// Solid version exists until a Solid surface needs one.
//
// Identity `ui-meta`; variance on data-tone, data-column and a part's
// data-emphasis. A surface places it and never repaints it (ui/README).

type MetaTone = 'muted' | 'dim' | 'danger' | 'accent'
export type MetaPart = string | { text: string; emphasis?: 'strong' }

export interface MetaOptions {
  /** The register: muted by default; dim for a quieter fact; danger for a
   *  failure's word; accent for work in progress. */
  tone?: MetaTone
  /** `duration` gives the element the width floor that keeps a column of
   *  durations aligned. */
  column?: 'duration'
  /** Hover detail — the start time on a duration. */
  title?: string
}

const SEPARATOR = ' · '

function fill(el: HTMLSpanElement, parts: readonly MetaPart[], opts: MetaOptions): void {
  el.dataset.tone = opts.tone ?? 'muted'
  if (opts.column === undefined) el.removeAttribute('data-column')
  else el.dataset.column = opts.column
  if (opts.title === undefined) el.removeAttribute('title')
  else el.title = opts.title

  const children: HTMLSpanElement[] = []
  parts.forEach((part, i) => {
    if (i > 0) {
      const sep = document.createElement('span')
      sep.className = 'ui-meta__sep'
      sep.setAttribute('aria-hidden', 'true')
      sep.textContent = SEPARATOR
      children.push(sep)
    }
    const span = document.createElement('span')
    span.className = 'ui-meta__part'
    if (typeof part === 'string') {
      span.textContent = part
    } else {
      span.textContent = part.text
      if (part.emphasis === 'strong') span.dataset.emphasis = 'strong'
    }
    children.push(span)
  })
  el.replaceChildren(...children)
}

export function createMeta(parts: readonly MetaPart[], opts: MetaOptions = {}): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-meta'
  fill(el, parts, opts)
  return el
}

/** Restate an existing Meta — the running duration ticks through this, so the
 *  element (and anything placed relative to it) stays put. */
export function updateMeta(
  el: HTMLSpanElement,
  parts: readonly MetaPart[],
  opts: MetaOptions = {},
): void {
  fill(el, parts, opts)
}
