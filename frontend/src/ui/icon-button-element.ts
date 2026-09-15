// createIconButton — IconButton (icon-button.tsx) emitted without Solid, for
// imperative code that builds one per block (ADR-0012; spec 2026-09-14 §6.2).
// The SAME element IconButton renders, styled by the same icon-button.css;
// icon-button-element.test.tsx holds the two to one DOM contract.
//
// The glyph is a callable: a ui/icons component called outside a root returns
// a detached SVGElement (terminal-content.ts, workspace-menu.ts do the same).
// `attrs` is the data-* passthrough IconButton gets from its rest props — a
// placement or test hook, never appearance.

import type { IconButtonSize } from './icon-button'

export interface IconButtonElementOptions {
  /** Required — an icon-only control with no accessible name is a defect. */
  ariaLabel: string
  icon: () => Element
  size?: IconButtonSize
  selected?: boolean
  square?: boolean
  /** See icon-button.tsx — the accent-filled submit register. */
  appearance?: 'default' | 'primary' | 'submit'
  railIndicator?: boolean
  disabled?: boolean
  title?: string
  tabIndex?: number
  type?: 'button' | 'submit' | 'reset'
  onClick?: (e: MouseEvent) => void
  attrs?: Readonly<Record<`data-${string}`, string>>
}

export function createIconButton(opts: IconButtonElementOptions): HTMLButtonElement {
  const btn = document.createElement('button')
  btn.className = 'ui-icon-button'
  btn.dataset.size = opts.size ?? 'md'
  if (opts.selected === true) btn.setAttribute('aria-selected', 'true')
  if (opts.square === true) btn.dataset.square = 'true'
  if (opts.appearance && opts.appearance !== 'default') btn.dataset.appearance = opts.appearance
  if (opts.railIndicator === true) btn.dataset.railIndicator = 'true'
  btn.setAttribute('aria-label', opts.ariaLabel)
  btn.disabled = opts.disabled === true
  // IconButton writes `title={local.title ?? ''}` — an empty attribute is part
  // of the contract, not an absence.
  btn.setAttribute('title', opts.title ?? '')
  if (opts.tabIndex !== undefined) btn.tabIndex = opts.tabIndex
  btn.type = opts.type ?? 'button'
  for (const [name, value] of Object.entries(opts.attrs ?? {})) btn.setAttribute(name, value)
  const onClick = opts.onClick
  if (onClick) btn.addEventListener('click', (e) => onClick(e))
  btn.append(opts.icon())
  return btn
}
