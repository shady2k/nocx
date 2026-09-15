// ModeIndicator — the kit's badge wearing the operable target-switch
// variance (ui/README table, ADR-0004 §3, nocx-4ff.7): the small persistent
// state label that says where the next Enter goes, and the person's one
// explicit switch. The badge shape and tone are the kit's (ui-badge,
// data-tone); this module is the indicator's DOM. A surface may place it
// (the CM6 gutter places it as the prompt's sigil — ask-entry.ts) and never
// repaint it.
//
// The component is target-agnostic by construction: it renders the WORD and
// TONE it is given plus the registry's target id as the data-target hook. A
// target's presentation (word, tone) is the host's vocabulary, decided once
// beside the registry lookup — never an id branch inside this module. The
// same rule extends to the menu (spec 2026-09-15 §6): the ROWS are the
// host's `items`, this module only draws them.
//
// Click no longer switches directly — it opens the kit ContextMenu, a
// render island mounted on open and disposed on close (base spec §6.3),
// exactly the precedent scrollback/blocks.ts's block-actions menu set. The
// ⌘/Ctrl+Enter chord bypasses this component entirely (it calls
// TargetIndicator.toggle directly, ask-entry.ts) and is unaffected.

import { createComponent } from 'solid-js'
import { render } from 'solid-js/web'
import type { BadgeTone } from './badge'
import { ChevronDownIcon, CheckCircleIcon, iconElement } from './icons'
import { ContextMenu, type ContextMenuItem } from './context-menu'

export interface ModeIndicatorMenuItem {
  /** The registry's target id — the item's identity and the checked test
   *  (it is checked when it equals the indicator's own targetId), never a
   *  derivation. */
  targetId: string
  /** What the person reads — the target's presentation word ('Run', 'Ask'). */
  word: string
}

export interface ModeIndicatorOptions {
  /** What the person reads — the active target's word ('Run', 'Ask'). */
  word: string
  /** The badge tone: the active state's register (neutral for the shell,
   *  info for the assistant — the same register the running block wears). */
  tone: BadgeTone
  /** The registry's target id (data-target) — the id, never a derivation. */
  targetId: string
  /** `'field'` — the composer's full-height leading segment beside CM6
   *  (spec 2026-09-15 §4: 14px UI type, 12px inline padding, a 64px minimum
   *  inline size, a trailing divider). Omitted keeps the base compact
   *  look this component always had. Own every appearance difference in
   *  mode-indicator.css under `data-variant='field'` — never a second
   *  component. */
  variant?: 'field'
  /** Every row the menu offers, in the order it lists them. One row when
   *  only one target is registered — the menu still opens; it is not
   *  gated on there being a choice. */
  items: ModeIndicatorMenuItem[]
  /** Fired when a row is picked, including the already-active row: the
   *  indicator does not special-case that — a target-agnostic component
   *  cannot know a no-op switch from a real one, so the host's onSelect
   *  decides (ask-entry.ts: a pick that does not change the target is a
   *  no-op, since the underlying switch is a binary toggle). */
  onSelect: (targetId: string) => void
}

/** Create the indicator button. The word is what the person reads; the
 *  aria-label says what the control IS, because the word alone ('Run') does
 *  not. */
export function createModeIndicator(opts: ModeIndicatorOptions): HTMLButtonElement {
  const btn = document.createElement('button')
  btn.type = 'button'
  btn.className = 'ui-badge ui-mode-indicator'
  btn.dataset.tone = opts.tone
  btn.dataset.target = opts.targetId
  if (opts.variant) btn.dataset.variant = opts.variant
  btn.setAttribute('aria-haspopup', 'menu')
  btn.setAttribute('aria-expanded', 'false')
  btn.setAttribute('aria-label', `Enter goes to ${opts.word}. Click to choose.`)

  const label = document.createElement('span')
  label.className = 'ui-mode-indicator__label'
  label.textContent = opts.word
  btn.append(label)

  // `iconElement` (ui/icons/icon-element.ts) resolves a detached icon
  // regardless of ambient Solid state — the same pattern PromptContext's
  // GitBranchIcon and the block header's ChevronRightIcon use, needed
  // because ChevronDownIcon can be called both this bare way and, in
  // principle, uncalled as a menu item's `icon` (nocx-9bpeq.12 round 3).
  const chevron = iconElement(ChevronDownIcon)
  chevron.classList.add('ui-mode-indicator__chevron')
  btn.append(chevron)

  // The divider AFTER the switch (round 18, mockup pass): the mockups read
  // "Run ⌄ │ git diff" — one rule separating the whole switch (word +
  // chevron) from the draft beside it, never a rule splitting the word from
  // its own chevron. The earlier placement (between label and chevron) is
  // what the owner's screenshot showed as "a divider inside the pill" — a
  // seam nobody asked for, inside a control that reads as one unit. The
  // kit's own decoration, never a text glyph, so it never lands in
  // textContent or in an accessible name built from it.
  const divider = document.createElement('span')
  divider.className = 'ui-mode-indicator__divider'
  divider.setAttribute('aria-hidden', 'true')
  btn.append(divider)

  /** Disposes the open menu's Solid root, or null while closed — a render
   *  island, mounted on open and disposed on close (base spec §6.3). */
  let disposeMenu: (() => void) | null = null

  const closeMenu = (): void => {
    const dispose = disposeMenu
    disposeMenu = null
    if (dispose === null) return
    dispose()
    btn.setAttribute('aria-expanded', 'false')
  }

  const openMenu = (): void => {
    if (disposeMenu !== null) {
      closeMenu()
      return
    }
    const rect = btn.getBoundingClientRect()
    const menuItems: ContextMenuItem[] = opts.items.map((item) => ({
      id: item.targetId,
      label: item.word,
      // ContextMenu's vocabulary for a row is icon + label, nothing else —
      // there is no dedicated "checked" affordance to ask for (spec §6:
      // "whatever ContextMenu offers for checked items"), so the active
      // row wears the icon column's mark and every other row's column
      // stays empty rather than reaching for a placeholder that would
      // read as an action.
      icon: item.targetId === opts.targetId ? CheckCircleIcon : undefined,
      onSelect: () => opts.onSelect(item.targetId),
    }))
    // A host div is enough: ContextMenu portals itself to document.body
    // (context-menu.tsx), so this never needs to be attached anywhere —
    // the same shape buildOverflowMenu uses.
    const host = document.createElement('div')
    disposeMenu = render(
      () =>
        createComponent(ContextMenu, {
          open: true,
          align: 'start',
          anchor: btn,
          x: rect.left,
          y: rect.bottom + 2,
          items: menuItems,
          onClose: closeMenu,
          'data-testid': 'mode-indicator-menu',
        }),
      host,
    )
    btn.setAttribute('aria-expanded', 'true')
  }

  btn.addEventListener('mousedown', (e) => {
    // The chip is a control, not a caret placement: never let the press
    // also move the caret or steal the editor's focus.
    e.preventDefault()
    e.stopPropagation()
  })
  // 'click', not 'mousedown', fires the menu: a native <button> dispatches
  // click for BOTH a completed mouse press and a keyboard Enter/Space
  // activation, so this one listener is the keyboard path too — no second,
  // parallel keydown handler to keep in step with it.
  btn.addEventListener('click', (e) => {
    e.stopPropagation()
    openMenu()
  })

  return btn
}
