// ModeIndicator — the kit's badge wearing the operable target-switch
// variance (ui/README table, ADR-0004 §3, nocx-4ff.7): the small persistent
// state label that says where the next Enter goes, and the person's one
// explicit switch (click, or the ⌘/Ctrl+Enter chord). The badge shape and
// tone are the kit's (ui-badge, data-tone); this module is the indicator's
// DOM. A surface may place it (the CM6 gutter places it as the prompt's
// sigil — ask-entry.ts) and never repaint it.
//
// The component is target-agnostic by construction: it renders the WORD and
// TONE it is given plus the registry's target id as the data-target hook. A
// target's presentation (word, tone) is the host's vocabulary, decided once
// beside the registry lookup — never an id branch inside this module.

import type { BadgeTone } from './badge'

export interface ModeIndicatorOptions {
  /** What the person reads — the active target's word ('Run', 'Ask'). */
  word: string
  /** The badge tone: the active state's register (neutral for the shell,
   *  info for the assistant — the same register the running block wears). */
  tone: BadgeTone
  /** The registry's target id (data-target) — the id, never a derivation. */
  targetId: string
  /** The explicit switch (ADR-0004 §3): fired on click. */
  onClick: () => void
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
  btn.setAttribute('aria-label', `Enter goes to ${opts.word}. Click to switch.`)
  btn.textContent = opts.word
  btn.addEventListener('mousedown', (e) => {
    // The chip is a control, not a caret placement: never let the press
    // also move the caret or steal the editor's focus.
    e.preventDefault()
    e.stopPropagation()
    opts.onClick()
  })
  return btn
}
