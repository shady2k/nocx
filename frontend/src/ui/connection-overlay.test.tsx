// @vitest-environment jsdom
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountConnectionOverlay, type ConnectionOverlayState } from './connection-overlay'

const activeDestroyers: (() => void)[] = []

afterEach(() => {
  while (activeDestroyers.length > 0) activeDestroyers.pop()!()
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

function subject(initial: ConnectionOverlayState, options: { minimumVisibleMs?: number } = {}) {
  const [state, setState] = createSignal<ConnectionOverlayState>(initial)
  const retry = vi.fn()
  const host = document.createElement('div')
  document.body.append(host)
  const mounted = mountConnectionOverlay(host, {
    state,
    onRetry: retry,
    ...options,
  })
  const destroy = () => mounted.destroy()
  activeDestroyers.push(destroy)
  return { host, state: setState, retry, destroy }
}

function overlay(host: HTMLElement): HTMLElement {
  const element = host.querySelector<HTMLElement>('.ui-connection-overlay')
  if (!element) throw new Error('connection overlay is not mounted')
  return element
}

function dialog(host: HTMLElement): HTMLDialogElement {
  const element = host.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
  if (!element) throw new Error('connection dialog is not mounted')
  return element
}

describe('ConnectionOverlay', () => {
  it('uses one identity class and puts the state in data-state', () => {
    const { host } = subject({ kind: 'waiting', nextAttemptInMs: 1500 }, { minimumVisibleMs: 0 })
    const root = overlay(host)

    expect(root.className).toBe('ui-connection-overlay')
    expect(root.dataset.state).toBe('waiting')
    expect(root.className).not.toContain('waiting')
  })

  it('states the waiting countdown in human-readable seconds', () => {
    const { host } = subject({ kind: 'waiting', nextAttemptInMs: 1500 }, { minimumVisibleMs: 0 })

    const message = host.querySelector('.ui-connection-overlay__message')?.textContent
    expect(message).toBe('Retrying in 2 seconds')
    expect(message).not.toContain('ms')
  })

  it('uses a native modal dialog and calls showModal', () => {
    const showModal = vi.spyOn(HTMLDialogElement.prototype, 'showModal')
    const { host } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    expect(dialog(host)).toBeTruthy()
    expect(showModal).toHaveBeenCalledOnce()
  })

  it('composes the logo, Spinner, and Button kit primitives without inline repainting', () => {
    const { host } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    expect(host.querySelector('.ui-connection-overlay__logo')).not.toBeNull()
    expect(host.querySelector('.ui-spinner')).not.toBeNull()
    const retry = host.querySelector<HTMLButtonElement>('.ui-button')
    expect(retry).toBeNull()
    expect(host.querySelector('.ui-connection-overlay')?.getAttribute('style')).toBeNull()
  })
  it.each([
    ['connecting', { kind: 'connecting' } as const, true],
    ['waiting', { kind: 'waiting', nextAttemptInMs: 1000 } as const, false],
    ['blocked', { kind: 'blocked', message: 'm', remedy: 'r' } as const, false],
  ])('keys logo pulsing from data-state in %s', (_name, initial, pulses) => {
    const { host } = subject(initial, { minimumVisibleMs: 0 })
    const root = overlay(host)
    const logo = root.querySelector('.ui-connection-overlay__logo')
    const stateSelector = host.querySelector(
      '[data-state="connecting"] .ui-connection-overlay__logo',
    )

    expect(root.dataset.state).toBe(initial.kind)
    expect(stateSelector === logo).toBe(pulses)
  })

  it.each([
    ['connecting', { kind: 'connecting' } as const, false],
    ['waiting', { kind: 'waiting', nextAttemptInMs: 1250 } as const, true],
    ['blocked', { kind: 'blocked', message: 'm', remedy: 'r' } as const, true],
  ])('offers Retry in %s only when it can act', (_name, initial, present) => {
    const { host, retry } = subject(initial, { minimumVisibleMs: 0 })
    const button = host.querySelector<HTMLButtonElement>('button.ui-button')
    expect(button !== null).toBe(present)
    if (button) {
      fireEvent.click(button)
      expect(retry).toHaveBeenCalledOnce()
    }
  })

  it('renders blocked message and remedy verbatim without adding a sentence', () => {
    const message = 'The backend refused this attempt: 503'
    const remedy = 'Start the backend, then choose Retry.'
    const { host } = subject({ kind: 'blocked', message, remedy }, { minimumVisibleMs: 0 })

    expect(host.querySelector('.ui-connection-overlay__message')?.textContent).toBe(message)
    expect(host.querySelector('.ui-connection-overlay__remedy')?.textContent).toBe(remedy)
    expect(overlay(host).textContent).toBe(`${message}${remedy}Retry`)
  })

  it('holds startup visibility for the default minimum, then hides online', () => {
    vi.useFakeTimers()
    const { host } = subject({ kind: 'online' })
    const modal = dialog(host)

    expect(modal.open).toBe(true)
    vi.advanceTimersByTime(999)
    expect(modal.open).toBe(true)
    vi.advanceTimersByTime(1)
    expect(modal.open).toBe(false)
  })

  it('shows a later connecting state immediately without applying startup minimum again', () => {
    vi.useFakeTimers()
    const { host, state } = subject({ kind: 'online' }, { minimumVisibleMs: 100 })
    vi.advanceTimersByTime(100)
    expect(dialog(host).open).toBe(false)

    state({ kind: 'connecting' })
    expect(dialog(host).open).toBe(true)
    state({ kind: 'online' })
    expect(dialog(host).open).toBe(false)
  })

  it('hides online immediately after startup minimum has elapsed', () => {
    vi.useFakeTimers()
    const { host, state } = subject({ kind: 'connecting' }, { minimumVisibleMs: 100 })
    vi.advanceTimersByTime(100)

    state({ kind: 'online' })
    expect(dialog(host).open).toBe(false)
  })
})
