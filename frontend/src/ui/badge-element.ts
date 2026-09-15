// createBadge — Badge (badge.tsx) emitted without Solid, for imperative code
// that builds one per block (ADR-0012; spec 2026-09-14 §6.2). The SAME element
// Badge renders, styled by the same badge.css; badge-element.test.tsx holds the
// two emitters to one DOM contract, variance by variance.
//
// Named `-element` rather than `badge.ts`: beside badge.tsx, a `badge.ts` would
// capture every `from './badge'` import in the kit.

import type { BadgeTone } from './badge'

export interface BadgeElementOptions {
  text: string
  tone?: BadgeTone
  variant?: 'solid'
  truncate?: boolean
  title?: string
  testId?: string
}

export function createBadge(opts: BadgeElementOptions): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-badge'
  el.dataset.tone = opts.tone ?? 'neutral'
  if (opts.variant !== undefined) el.dataset.variant = opts.variant
  if (opts.truncate === true) el.dataset.truncate = 'true'
  if (opts.title !== undefined) el.title = opts.title
  if (opts.testId !== undefined) el.dataset.testid = opts.testId
  el.textContent = opts.text
  return el
}
