// @vitest-environment jsdom
// The two mechanics every anchored transient overlay shares. The clamp is
// tested as arithmetic — it takes measurements and returns a point, so it
// needs no DOM and no layout, which is the whole reason it is a function
// and not a method on a component.
import { describe, expect, it, vi } from 'vitest'

import { OVERLAY_EDGE_MARGIN_PX, attachTransientDismiss, clampToViewport } from './anchored'

const VIEW = { width: 1000, height: 800 }
const M = OVERLAY_EDGE_MARGIN_PX

describe('clampToViewport', () => {
  it('leaves a point that already fits exactly where the caller asked', () => {
    expect(clampToViewport({ width: 200, height: 100 }, { x: 300, y: 400 }, VIEW)).toEqual({
      x: 300,
      y: 400,
    })
  })

  it('pulls an overlay back from the right and bottom edges', () => {
    expect(clampToViewport({ width: 200, height: 100 }, { x: 950, y: 780 }, VIEW)).toEqual({
      x: 1000 - 200 - M,
      y: 800 - 100 - M,
    })
  })

  it('keeps the margin at the left and top', () => {
    expect(clampToViewport({ width: 200, height: 100 }, { x: -50, y: 0 }, VIEW)).toEqual({
      x: M,
      y: M,
    })
  })

  it('opens a bottom-anchored panel upward, which is what the activity bar needs', () => {
    // The button is 40px from the bottom and the panel is 400 tall. Asking
    // for its top-left at the button's own top would run 360px off screen;
    // the clamp puts the panel's BOTTOM on the margin instead.
    const at = clampToViewport({ width: 320, height: 400 }, { x: 48, y: 760 }, VIEW)
    expect(at.y).toBe(800 - 400 - M)
    expect(at.y + 400 + M).toBe(800)
  })

  it('keeps an overlay taller than the viewport reachable at the top', () => {
    // The two bounds cross here. Without the inner Math.max the clamp would
    // place the panel ABOVE the screen, where nothing can scroll to it.
    expect(clampToViewport({ width: 100, height: 2000 }, { x: 10, y: 10 }, VIEW).y).toBe(M)
  })
})

describe('attachTransientDismiss', () => {
  function fixture() {
    const el = document.createElement('div')
    const inside = document.createElement('button')
    el.append(inside)
    document.body.append(el)
    const onOutside = vi.fn()
    const onEscape = vi.fn()
    const detach = attachTransientDismiss({ element: () => el, onOutside, onEscape })
    return { el, inside, onOutside, onEscape, detach }
  }

  it('closes on a pointerdown outside and not on one inside', () => {
    const f = fixture()
    f.inside.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(f.onOutside).not.toHaveBeenCalled()

    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(f.onOutside).toHaveBeenCalledTimes(1)
    f.detach()
    f.el.remove()
  })

  it('reads the element at event time, so a re-rendered panel still dismisses', () => {
    // The element is read through an accessor rather than captured: a
    // component that swaps its panel node would otherwise go on testing
    // containment against a node nothing can click.
    const el = document.createElement('div')
    document.body.append(el)
    let current: HTMLElement | undefined = document.createElement('div')
    const onOutside = vi.fn()
    const detach = attachTransientDismiss({
      element: () => current,
      onOutside,
      onEscape: () => {},
    })
    current = el
    el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(onOutside).not.toHaveBeenCalled()
    detach()
    el.remove()
  })

  it('hands Escape to the caller with the event, and stops when detached', () => {
    const f = fixture()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(f.onEscape).toHaveBeenCalledTimes(1)
    expect(f.onEscape.mock.calls[0][0]).toBeInstanceOf(KeyboardEvent)

    // Another key is not this module's business.
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown' }))
    expect(f.onEscape).toHaveBeenCalledTimes(1)

    f.detach()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(f.onEscape).toHaveBeenCalledTimes(1)
    expect(f.onOutside).not.toHaveBeenCalled()
    f.el.remove()
  })
})
