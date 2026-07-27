// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import {
  pushOverlay,
  popOverlay,
  topOverlay,
  hasOpenOverlays,
  stackDepth,
  closeTopmost,
  clearStack,
  isWailsDragTarget,
  restoreFocus,
} from './stack'

afterEach(() => {
  clearStack()
})

describe('overlay stack', () => {
  it('starts empty', () => {
    expect(hasOpenOverlays()).toBe(false)
    expect(stackDepth()).toBe(0)
    expect(topOverlay()).toBeUndefined()
  })

  it('push adds an entry with id', () => {
    const close = vi.fn(() => true)
    const entry = pushOverlay(close)
    expect(hasOpenOverlays()).toBe(true)
    expect(stackDepth()).toBe(1)
    expect(topOverlay()).toBe(entry)
    expect(entry.id).toMatch(/^overlay-\d+$/)
  })

  it('pop removes the entry', () => {
    const close = vi.fn(() => true)
    const entry = pushOverlay(close)
    expect(popOverlay(entry)).toBe(true)
    expect(hasOpenOverlays()).toBe(false)
  })

  it('pop fails for unknown entry', () => {
    expect(popOverlay({ id: 'ghost', close: () => true, prevFocus: null })).toBe(false)
  })

  it('pop only the top entry preserves bottom', () => {
    const close1 = vi.fn(() => true)
    const close2 = vi.fn(() => true)
    const e1 = pushOverlay(close1)
    pushOverlay(close2)
    popOverlay(e1)
    // Removing non-top entry is allowed — stack reorders.
    expect(stackDepth()).toBe(1)
  })

  it('closeTopmost calls close on the top and returns true', () => {
    const close1 = vi.fn(() => true)
    const close2 = vi.fn(() => true)
    pushOverlay(close1)
    pushOverlay(close2)
    expect(closeTopmost()).toBe(true)
    expect(close2).toHaveBeenCalledOnce()
    expect(close1).not.toHaveBeenCalled()
  })

  it('closeTopmost on empty stack returns false', () => {
    expect(closeTopmost()).toBe(false)
  })

  it('Escape handler closes only the topmost', () => {
    const close1 = vi.fn(() => true)
    const close2 = vi.fn(() => true)
    pushOverlay(close1)
    pushOverlay(close2)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }))
    expect(close2).toHaveBeenCalledOnce()
    expect(close1).not.toHaveBeenCalled()
  })

  it('multiple Escape presses close one overlay at a time', () => {
    const close1 = vi.fn(() => true)
    const close2 = vi.fn(() => true)
    pushOverlay(close1)
    pushOverlay(close2)

    // Press Escape — closes topmost (close2)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }))
    expect(close2).toHaveBeenCalledOnce()

    // Pop the closed entry
    popOverlay(topOverlay()!)

    // Press Escape — closes next (close1)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }))
    expect(close1).toHaveBeenCalledOnce()
  })

  it('non-Escape key does not trigger close', () => {
    const close = vi.fn(() => true)
    pushOverlay(close)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', cancelable: true }))
    expect(close).not.toHaveBeenCalled()
  })

  it('Escape handler removed when stack empties', () => {
    const close = vi.fn(() => true)
    const entry = pushOverlay(close)
    popOverlay(entry)
    // Should not trigger
    const spy = vi.spyOn(entry, 'close')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }))
    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
  })

  it('push sets prevFocus from document.activeElement', () => {
    const entry = pushOverlay(() => true)
    expect(entry.prevFocus).toBe(document.body)
  })

  it('push accepts explicit prevFocus', () => {
    const btn = document.createElement('button')
    const entry = pushOverlay(() => true, btn)
    expect(entry.prevFocus).toBe(btn)
  })
})

describe('isWailsDragTarget', () => {
  it('false for plain element', () => {
    const el = document.createElement('div')
    document.body.appendChild(el)
    expect(isWailsDragTarget(el)).toBe(false)
    document.body.removeChild(el)
  })

  it('false for no-drag', () => {
    const p = document.createElement('div')
    p.style.setProperty('--wails-draggable', 'no-drag')
    const c = document.createElement('div')
    p.appendChild(c)
    document.body.appendChild(p)
    expect(isWailsDragTarget(c)).toBe(false)
    document.body.removeChild(p)
  })

  it('true for drag', () => {
    const p = document.createElement('div')
    p.style.setProperty('--wails-draggable', 'drag')
    const c = document.createElement('div')
    p.appendChild(c)
    document.body.appendChild(p)
    expect(isWailsDragTarget(c)).toBe(true)
    document.body.removeChild(p)
  })

  it('walks ancestor chain', () => {
    const gp = document.createElement('div')
    gp.style.setProperty('--wails-draggable', 'drag')
    const p = document.createElement('div')
    p.style.setProperty('--wails-draggable', 'no-drag')
    const c = document.createElement('div')
    p.appendChild(c)
    gp.appendChild(p)
    document.body.appendChild(gp)
    // Walks up past no-drag to drag
    expect(isWailsDragTarget(c)).toBe(true)
    document.body.removeChild(gp)
  })
})

describe('restoreFocus', () => {
  it('focuses prevFocus via rAF', () => {
    vi.useFakeTimers()
    const btn = document.createElement('button')
    document.body.appendChild(btn)
    const e = pushOverlay(() => true, btn)
    document.body.focus()
    restoreFocus(e)
    vi.runAllTimers()
    expect(document.activeElement).toBe(btn)
    document.body.removeChild(btn)
    vi.useRealTimers()
  })

  it('skips dialog elements (native focus)', () => {
    const d = document.createElement('dialog')
    const e = pushOverlay(() => true, d)
    expect(() => restoreFocus(e)).not.toThrow()
  })

  it('skips null prevFocus', () => {
    const e = pushOverlay(() => true, null)
    expect(() => restoreFocus(e)).not.toThrow()
  })

  it('restoreFocus on non-HTMLElement does not throw', () => {
    // document is not an Element — the point of the test is that restoreFocus
    // tolerates an invoker that cannot be focused.
    const e = pushOverlay(() => true, document as unknown as Element)
    expect(() => restoreFocus(e)).not.toThrow()
  })
})
