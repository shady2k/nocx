// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { ScrollbackController } from './controller'
import type { TerminalRenderer } from '../renderers/types'

function makeRenderer(): TerminalRenderer {
  return {
    write: vi.fn(),
    onData: vi.fn(),
    onCommandMarker: vi.fn(),
    onBufferChange: vi.fn(),
    onTitle: vi.fn(),
    mount: vi.fn(() => Promise.resolve({ cols: 80, rows: 24 })),
    dispose: vi.fn(),
    focus: vi.fn(),
    setReadOnly: vi.fn(),
    registerMarker: vi.fn(() => undefined),
    paste: vi.fn(),
    clearViewport: vi.fn(),
    fitViewport: vi.fn(),
    getBufferLine: vi.fn(() => null),
    reset: vi.fn(),
  } as unknown as TerminalRenderer
}

function makeController() {
  const pane = document.createElement('div')
  const controller = new ScrollbackController({ pane, renderer: makeRenderer() })
  // jsdom does no layout: give the scrollback area a real height so the
  // fill-the-pane sizing has something to read.
  Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
    value: 360,
    configurable: true,
  })
  return { pane, controller }
}

describe('ScrollbackController unstructured mode', () => {
  it('fills the pane for a markerless session (plain SSH before any OSC 133)', () => {
    const { controller } = makeController()
    expect(controller.mode).toBe('idle')

    controller.setUnstructured()

    expect(controller.mode).toBe('unstructured')
    expect(controller.xtermLiveContainer.className).toContain('live-unstructured')
    expect(controller.xtermLiveContainer.style.height).toBe('360px')
  })

  it('lets the first OSC-133 marker transition back to the normal layout', () => {
    const { controller } = makeController()
    controller.setUnstructured()

    // The marker arrives: PROMPT_READY collapses the live region to idle.
    controller.setIdle()

    expect(controller.mode).toBe('idle')
    expect(controller.xtermLiveContainer.className).toContain('live-idle')
  })

  it('markerless alt-screen return restores the full pane, not the hidden idle', () => {
    const { controller } = makeController()
    controller.setUnstructured()
    controller.enterFullscreen()
    expect(controller.mode).toBe('fullscreen')

    // Buffer returned to normal without any OSC-133 ever arriving: the
    // terminal must fill the pane again, not collapse.
    controller.exitFullscreen()
    controller.setUnstructured()

    expect(controller.mode).toBe('unstructured')
    expect(controller.xtermLiveContainer.className).toContain('live-unstructured')
  })
})
