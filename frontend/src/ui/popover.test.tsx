// @vitest-environment jsdom
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Popover } from './popover'

// The panel PORTALS into document.body, so the queries target the document
// and never render()'s container.

afterEach(cleanup)

function mount(over: { onClose?: () => void } = {}) {
  const [open, setOpen] = createSignal(true)
  const onClose = over.onClose ?? (() => setOpen(false))
  render(() => (
    <Popover open={open()} x={100} y={100} ariaLabel="Background operations" onClose={onClose}>
      <button type="button" data-testid="inside">
        Cancel
      </button>
    </Popover>
  ))
  return { open, setOpen }
}

const panel = () => document.querySelector<HTMLElement>('.ui-popover')

describe('Popover', () => {
  it('renders nothing until it is open', () => {
    const [open, setOpen] = createSignal(false)
    render(() => (
      <Popover open={open()} x={0} y={0} ariaLabel="Ops" onClose={() => {}}>
        <span>body</span>
      </Popover>
    ))
    expect(panel()).toBeNull()
    setOpen(true)
    expect(panel()).not.toBeNull()
  })

  it('carries the caller’s accessible name and holds the keyboard while it is up', () => {
    // A panel a screen reader can enter and cannot describe is a region
    // with no name; and a panel that never took focus cannot be escaped
    // from, because the keystroke would go to whatever is underneath.
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    mount()
    expect(panel()?.getAttribute('aria-label')).toBe('Background operations')
    expect(document.activeElement).toBe(panel())
    opener.remove()
  })

  it('renders the caller’s content and knows nothing about it', () => {
    mount()
    expect(document.querySelector('[data-testid="inside"]')).not.toBeNull()
    // No menu vocabulary is invented for content that is not a menu.
    expect(document.querySelector('[role="menuitem"]')).toBeNull()
  })

  it('closes on a pointerdown outside and stays open for one inside', () => {
    const onClose = vi.fn()
    mount({ onClose })
    document
      .querySelector<HTMLElement>('[data-testid="inside"]')!
      .dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(onClose).not.toHaveBeenCalled()

    fireEvent.pointerDown(document.body)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes on Escape, hands the keyboard back, and does not let it through', () => {
    // The surface underneath acts on Escape itself — the terminal does —
    // so the keystroke that closed the panel must stop here.
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const seen = vi.fn()
    document.body.addEventListener('keydown', seen)

    const m = mount()
    fireEvent.keyDown(document, { key: 'Escape' })

    expect(m.open()).toBe(false)
    expect(document.activeElement).toBe(opener)
    expect(seen).not.toHaveBeenCalled()

    document.body.removeEventListener('keydown', seen)
    opener.remove()
  })

  it('stops listening once it is closed', () => {
    const onClose = vi.fn()
    const [open, setOpen] = createSignal(true)
    render(() => (
      <Popover open={open()} x={0} y={0} ariaLabel="Ops" onClose={onClose}>
        <span>body</span>
      </Popover>
    ))
    setOpen(false)
    fireEvent.pointerDown(document.body)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
  })

  it('clamps itself onto the screen from the point the caller asked for', () => {
    // jsdom reports a zero rect for everything, so this asserts the
    // component POSITIONS from the caller's point and defers the geometry
    // to overlay/anchored.ts, which is tested as arithmetic. What is worth
    // guarding here is that a value is written at all: a panel with no
    // left/top sits at the document origin.
    render(() => (
      <Popover open x={640} y={480} ariaLabel="Ops" onClose={() => {}}>
        <span>body</span>
      </Popover>
    ))
    expect(panel()?.style.left).toBe('640px')
    expect(panel()?.style.top).toBe('480px')
  })
})
