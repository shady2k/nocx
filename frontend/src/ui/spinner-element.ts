// Spinner, emitted without Solid (spec 2026-09-14 §6.2) — for a header the
// scrollback builds per command and discards by replacement, where a render
// island would have no owner to dispose it. Same identity, same stylesheet
// (styles/components/spinner.css), held to <Spinner> by spinner-element.test.tsx.
import type { SpinnerSize } from './spinner'

export interface SpinnerElementOptions {
  label: string
  size?: SpinnerSize
}

export function createSpinner(opts: SpinnerElementOptions): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-spinner'
  el.setAttribute('role', 'status')
  el.setAttribute('aria-label', opts.label)
  el.dataset.size = opts.size ?? 'md'
  return el
}
