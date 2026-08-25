// @vitest-environment jsdom
//
// The frontend half of `assistant.expandReasoning` (nocx-y9e88): the
// declared default, the decision painted on the document root, and the ONE
// rule that a setting the surface contradicts is the defect — notes already
// on screen follow a change.
import { describe, it, expect, beforeEach } from 'vitest'
import {
  REASONING_EXPANDED_DEFAULT,
  applyReasoningExpanded,
  reasoningStartsExpanded,
} from './reasoning-expanded'
import { createReasoningNote } from './ui/reasoning-note'

function freshRoot(): HTMLElement {
  const root = document.createElement('div')
  document.body.appendChild(root)
  return root
}

describe('assistant.expandReasoning — the frontend half', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('paints the decision on the root and answers from it', () => {
    const root = freshRoot()
    expect(reasoningStartsExpanded(root)).toBe(REASONING_EXPANDED_DEFAULT)
    applyReasoningExpanded(true, root)
    expect(root.dataset.reasoningExpanded).toBe('on')
    expect(reasoningStartsExpanded(root)).toBe(true)
    applyReasoningExpanded(false, root)
    expect(root.dataset.reasoningExpanded).toBe('off')
    expect(reasoningStartsExpanded(root)).toBe(false)
  })

  it('keeps the declared default when the snapshot does not carry the key', () => {
    const root = freshRoot()
    applyReasoningExpanded(undefined, root)
    expect(reasoningStartsExpanded(root)).toBe(REASONING_EXPANDED_DEFAULT)
    applyReasoningExpanded('yes', root)
    expect(reasoningStartsExpanded(root)).toBe(REASONING_EXPANDED_DEFAULT)
  })

  it('opens the notes ALREADY on screen, and closes them again', () => {
    const root = freshRoot()
    const a = createReasoningNote()
    const b = createReasoningNote()
    root.append(a.el, b.el)
    expect(a.el.open).toBe(false)

    applyReasoningExpanded(true, root)
    expect(a.el.open).toBe(true)
    expect(b.el.open).toBe(true)

    applyReasoningExpanded(false, root)
    expect(a.el.open).toBe(false)
    expect(b.el.open).toBe(false)
  })

  it('leaves a note a person closed by hand alone until the setting itself changes', () => {
    const root = freshRoot()
    applyReasoningExpanded(true, root)
    const note = createReasoningNote({ expanded: true })
    root.appendChild(note.el)
    // The person closes this one. The setting is the DEFAULT, not a lock.
    note.el.open = false
    // Any other setting changing refetches the whole snapshot and applies
    // this key again with the SAME value — which must not reopen the note.
    applyReasoningExpanded(true, root)
    expect(note.el.open).toBe(false)
  })
})
