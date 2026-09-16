// createButton — the kit's Button, emitted without Solid (spec 2026-09-14 §6.2).
//
// For imperative surfaces whose elements are rewritten by imperative owners:
// the composer's controls, whose text, title, accessible name and disabled
// state are written by setModelChip, setRecoveryAction and GrantController on
// every state change. A Solid island there would have its DOM written under
// it. The element is Button's — same identity, same data-* variance, same
// stylesheet — and button-element.test.tsx holds the two emitters to one shape.
//
// Named button-element.ts, not button.ts: `.ts` resolves before `.tsx`, and a
// button.ts would capture every `from './button'` import in the tree.

import type { ButtonSize, ButtonVariant } from './button'

export interface CreateButtonOptions {
  label: string
  variant?: ButtonVariant
  size?: ButtonSize
  truncate?: boolean
  mono?: boolean
  title?: string
  ariaLabel?: string
  disabled?: boolean
  onClick: (e: MouseEvent) => void
}

export function createButton(opts: CreateButtonOptions): HTMLButtonElement {
  const el = document.createElement('button')
  el.className = 'ui-button'
  el.dataset.variant = opts.variant ?? 'default'
  if (opts.size && opts.size !== 'md') el.dataset.size = opts.size
  if (opts.truncate === true) el.dataset.truncate = 'true'
  if (opts.mono === true) el.dataset.mono = 'true'
  el.type = 'button'
  el.disabled = opts.disabled === true
  el.title = opts.title ?? ''
  if (opts.ariaLabel !== undefined) el.setAttribute('aria-label', opts.ariaLabel)
  el.textContent = opts.label
  el.addEventListener('click', (e) => opts.onClick(e))
  return el
}
