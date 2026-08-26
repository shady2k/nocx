// @vitest-environment jsdom
//
// The floating Mark button: a selection offers the exact GrantBlock derived by
// ask-entry, and the button is the only thing that confirms it. Geometry is
// tested here because jsdom does no layout; terminal-content.test.ts owns the
// selection-to-offer integration.
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { GrantBlock } from './ask-entry'
import { createMarkAffordance, markButtonPosition, SELECTION_GAP_PX } from './mark-affordance'
import { EDGE_MARGIN_PX } from './ui/menu-geometry'

const anchorAt = (
  opts: Partial<{ left: number; top: number; right: number; bottom: number }> = {},
) => ({
  left: opts.left ?? 100,
  top: opts.top ?? 200,
  right: opts.right ?? 300,
  bottom: opts.bottom ?? 220,
})

const SIZE = { width: 80, height: 32 }
const VIEWPORT = { width: 800, height: 600 }
const grant = (window?: { start: number; count: number }): GrantBlock => {
  const blockEl = document.createElement('div')
  blockEl.className = 'cmd-block'
  blockEl.dataset.entryId = 'entry-1'
  return {
    itemId: 'entry-1',
    blockEl,
    command: 'npm test',
    state: 'exited',
    ...window,
  }
}

afterEach(() => document.body.replaceChildren())

describe('markButtonPosition', () => {
  it("anchors below the selection's last client rect, right edge at the selection's end", () => {
    const { left, top } = markButtonPosition(anchorAt(), SIZE, VIEWPORT)
    expect(left).toBe(300)
    expect(top).toBe(220 + SELECTION_GAP_PX)
  })

  it('flips above the selection when there is no room below it', () => {
    const anchor = anchorAt({ top: 560, bottom: 595 })
    const { top } = markButtonPosition(anchor, SIZE, VIEWPORT)
    expect(top).toBe(560 - SELECTION_GAP_PX - SIZE.height)
    expect(top + SIZE.height + EDGE_MARGIN_PX).toBeLessThanOrEqual(VIEWPORT.height)
  })

  it('clamps to the viewport instead of running off its edges', () => {
    const right = markButtonPosition(anchorAt({ right: 790 }), SIZE, VIEWPORT)
    expect(right.left).toBe(VIEWPORT.width - SIZE.width - EDGE_MARGIN_PX)
    const top = markButtonPosition(anchorAt({ top: 2, bottom: 590 }), SIZE, VIEWPORT)
    expect(top.top).toBe(EDGE_MARGIN_PX)
  })
})

describe('createMarkAffordance', () => {
  it('renders the kit Button in a fixed body-level wrapper with the grant vocabulary', () => {
    const affordance = createMarkAffordance(() => {})
    try {
      const wrapper = document.body.querySelector<HTMLElement>('.mark-affordance')
      expect(wrapper).not.toBeNull()
      expect(wrapper?.style.position).toBe('fixed')
      const btn = wrapper?.querySelector<HTMLButtonElement>('.ui-button')
      expect(btn).not.toBeNull()
      expect(btn?.getAttribute('data-variant')).toBe('primary')
      expect(btn?.getAttribute('data-size')).toBe('sm')
      expect(btn?.textContent).toBe('Mark block')
      expect(btn?.getAttribute('aria-label')).toBe('Mark block for the question')
    } finally {
      affordance.dispose()
    }
  })

  it('names a row window with its line count', () => {
    const affordance = createMarkAffordance(() => {})
    try {
      affordance.show(anchorAt(), grant({ start: 1, count: 2 }), VIEWPORT)
      expect(affordance.button.textContent).toBe('Mark 2 lines')
      expect(affordance.button.getAttribute('aria-label')).toBe('Mark 2 lines for the question')
    } finally {
      affordance.dispose()
    }
  })

  it('is hidden until shown, and show() places it near the selection end', () => {
    const affordance = createMarkAffordance(() => {})
    try {
      const wrapper = document.body.querySelector<HTMLElement>('.mark-affordance')!
      expect(affordance.visible).toBe(false)
      expect(wrapper.style.display).toBe('none')
      affordance.show(anchorAt(), grant(), VIEWPORT)
      expect(affordance.visible).toBe(true)
      expect(wrapper.style.display).not.toBe('none')
      expect(wrapper.style.left).toBe('300px')
      expect(wrapper.style.top).toBe(`${220 + SELECTION_GAP_PX}px`)
      affordance.hide()
      expect(affordance.visible).toBe(false)
      expect(wrapper.style.display).toBe('none')
    } finally {
      affordance.dispose()
    }
  })

  it('fires the exact offered grant when the button is pressed', () => {
    const onMark = vi.fn()
    const offered = grant({ start: 1, count: 2 })
    const affordance = createMarkAffordance(onMark)
    try {
      affordance.show(anchorAt(), offered, VIEWPORT)
      affordance.button.click()
      expect(onMark).toHaveBeenCalledTimes(1)
      expect(onMark).toHaveBeenCalledWith(offered)
    } finally {
      affordance.dispose()
    }
  })

  it('a press never steals the selection: mousedown is prevented', () => {
    const affordance = createMarkAffordance(() => {})
    try {
      affordance.show(anchorAt(), grant(), VIEWPORT)
      const md = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
      affordance.button.dispatchEvent(md)
      expect(md.defaultPrevented).toBe(true)
    } finally {
      affordance.dispose()
    }
  })

  it('dispose() tears the wrapper out of the document', () => {
    const affordance = createMarkAffordance(() => {})
    expect(document.body.querySelector('.mark-affordance')).not.toBeNull()
    affordance.dispose()
    expect(document.body.querySelector('.mark-affordance')).toBeNull()
  })
})
