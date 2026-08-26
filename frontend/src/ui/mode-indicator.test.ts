// @vitest-environment jsdom
//
// ModeIndicator (ui/README table) — the kit's badge wearing the operable
// target-switch variance: what a submit reaches, and the person's one
// explicit switch (ADR-0004 §3, nocx-4ff.7). The component is
// target-agnostic: word and tone are inputs, never a lookup here.
import { describe, it, expect, vi } from 'vitest'
import { createModeIndicator } from './mode-indicator'

describe('createModeIndicator', () => {
  it('is the kit badge with a stable identity and the typed tone variance', () => {
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      onClick: () => {},
    })
    expect(el.tagName).toBe('BUTTON')
    expect(el.type).toBe('button')
    expect(el.classList.contains('ui-badge')).toBe(true)
    expect(el.classList.contains('ui-mode-indicator')).toBe(true)
    expect(el.dataset.tone).toBe('neutral')
    expect(el.textContent).toBe('Run')
  })

  it('carries the registry’s target id as the data-target hook, never a derivation', () => {
    const el = createModeIndicator({
      word: 'Ask',
      tone: 'info',
      targetId: 'agent',
      onClick: () => {},
    })
    expect(el.dataset.target).toBe('agent')
  })

  it('names what the control does in the aria-label — the word alone does not', () => {
    const el = createModeIndicator({
      word: 'Run',
      tone: 'neutral',
      targetId: 'shell',
      onClick: () => {},
    })
    expect(el.getAttribute('aria-label')).toBe('Enter goes to Run. Click to switch.')
  })

  it('click fires the explicit switch; the press never moves the caret or steals focus', () => {
    const onClick = vi.fn()
    const el = createModeIndicator({ word: 'Ask', tone: 'info', targetId: 'agent', onClick })
    const press = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    const prevented = !el.dispatchEvent(press)
    expect(prevented).toBe(true)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('renders any word and tone it is given — the presentation is the host’s vocabulary', () => {
    const el = createModeIndicator({
      word: 'Recall',
      tone: 'warning',
      targetId: 'recall',
      onClick: () => {},
    })
    expect(el.textContent).toBe('Recall')
    expect(el.dataset.tone).toBe('warning')
    expect(el.dataset.target).toBe('recall')
  })
})
