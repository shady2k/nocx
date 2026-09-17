// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountToolSurfaceNotice } from './tool-surface-notice'
import type { SessionToolSurfaceChanged } from './generated/session.toolSurfaceChanged'

let dispose: (() => void) | null = null
let pane: HTMLElement | null = null

afterEach(() => {
  dispose?.()
  dispose = null
  pane?.remove()
  pane = null
})

function fact(
  reason = 'tools.catalogue did not arrive before the launch deadline',
): SessionToolSurfaceChanged {
  return {
    sessionId: 'session-1',
    instanceId: 'instance-1',
    sessionEpoch: 1,
    status: 'unavailable',
    reason,
  }
}

describe('worker tool-surface notice', () => {
  it('places one kit status card above the terminal and names the unavailable surface', () => {
    pane = document.createElement('div')
    const terminal = document.createElement('div')
    terminal.className = 'scrollback-layout'
    pane.appendChild(terminal)
    document.body.appendChild(pane)

    dispose = mountToolSurfaceNotice(pane, { fact: fact(), onDismiss: vi.fn() })

    expect(pane.firstElementChild?.className).toBe('nocx-tool-surface-notice')
    expect(pane.querySelectorAll('.ui-status-card')).toHaveLength(1)
    expect(pane.querySelector('.ui-status-card__title')?.textContent).toBe(
      'Worker tools unavailable',
    )
    expect(pane.querySelector('.ui-status-card__desc')?.textContent).toContain(
      'tools.catalogue did not arrive',
    )
    expect(pane.lastElementChild).toBe(terminal)
  })

  it('dismisses the card without writing into the terminal', () => {
    pane = document.createElement('div')
    const terminal = document.createElement('div')
    terminal.textContent = 'agent-owned terminal output'
    pane.appendChild(terminal)
    document.body.appendChild(pane)
    const onDismiss = vi.fn()
    dispose = mountToolSurfaceNotice(pane, {
      fact: fact('peer refused: session already has a worker caller'),
      onDismiss,
    })

    pane.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')?.click()

    expect(onDismiss).toHaveBeenCalledOnce()
    expect(terminal.textContent).toBe('agent-owned terminal output')
  })
})
