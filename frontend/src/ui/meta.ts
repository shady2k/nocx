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
  /** `sm` reads in the mono face at `--font-size-sm` instead of the UI face
   *  at `--font-size-2xs` — the command block's status group (spec
   *  2026-09-15 §4), which sits beside the mono command line and reads
   *  wrong in the UI font at that size. */
  size?: 'sm'
}

const SEPARATOR = ' · '

/** The separator between two Metas that stand as SEPARATE elements — a
 *  settled block's status word and its duration (spec 2026-09-15 §4), the
 *  same shape a running block's word and duration share. Identical to the
 *  one `fill` places between two parts of ONE Meta, so a surface reaching
 *  for "the muted dot between two facts" never types a raw `·` of its own.
 *
 *  `size: 'sm'` is for exactly this standalone case: nested inside a Meta,
 *  the separator already inherits that Meta's face through the cascade
 *  (the reason `fill` below never sets it); standing alone between two
 *  Metas, there is no such ancestor to inherit from. */
export function createMetaSeparator(opts: { size?: 'sm' } = {}): HTMLSpanElement {
  const sep = document.createElement('span')
  sep.className = 'ui-meta__sep'
  sep.setAttribute('aria-hidden', 'true')
  sep.textContent = SEPARATOR
  if (opts.size !== undefined) sep.dataset.size = opts.size
  return sep
}

function fill(el: HTMLSpanElement, parts: readonly MetaPart[], opts: MetaOptions): void {
  el.dataset.tone = opts.tone ?? 'muted'
  if (opts.column === undefined) el.removeAttribute('data-column')
  else el.dataset.column = opts.column
  if (opts.title === undefined) el.removeAttribute('title')
  else el.title = opts.title
  if (opts.size === undefined) el.removeAttribute('data-size')
  else el.dataset.size = opts.size

  const children: HTMLSpanElement[] = []
  parts.forEach((part, i) => {
    if (i > 0) children.push(createMetaSeparator())
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
