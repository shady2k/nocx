// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  CONNECTION_OVERLAY_EXIT_MS,
  mountConnectionOverlay,
  type ConnectionOverlayState,
} from './connection-overlay'

const EXIT_MS = CONNECTION_OVERLAY_EXIT_MS

const activeDestroyers: (() => void)[] = []

afterEach(() => {
  while (activeDestroyers.length > 0) activeDestroyers.pop()!()
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

function subject(
  initial: ConnectionOverlayState,
  options: { minimumVisibleMs?: number; onHidden?: () => void } = {},
) {
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
  const element = host.querySelector<HTMLDialogElement>('dialog.ui-connection-overlay')
  if (!element) throw new Error('connection dialog is not mounted')
  return element
}

function text(host: HTMLElement, part: string): string {
  return host.querySelector(`.ui-connection-overlay__${part}`)?.textContent ?? ''
}

/** The overlay's stylesheet, comments stripped — the ground and the ring are
    declarations, and a test that only reads the DOM cannot see either. */
function sheet(): string {
  return readFileSync('src/styles/components/connection-overlay.css', 'utf8').replace(
    /\/\*[\s\S]*?\*\//g,
    '',
  )
}

function rule(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return sheet().match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`))?.[1] ?? ''
}

describe('ConnectionOverlay', () => {
  it('uses one identity class and puts the state in data-state', () => {
    const { host } = subject({ kind: 'waiting', nextAttemptInMs: 1500 }, { minimumVisibleMs: 0 })
    const root = overlay(host)

    expect(root.className).toBe('ui-connection-overlay')
    expect(root.dataset.state).toBe('waiting')
    expect(root.className).not.toContain('waiting')
  })

  it('says what is wrong in the headline and puts the countdown under it', () => {
    const { host } = subject({ kind: 'waiting', nextAttemptInMs: 1500 }, { minimumVisibleMs: 0 })

    // The countdown was the headline, set at the largest size on the screen —
    // so the biggest thing a person read was a ticking number while WHAT IS
    // WRONG was said nowhere. The headline states the condition; the number is
    // the detail line under it.
    expect(text(host, 'headline')).toBe('Cannot reach the nocx backend')
    expect(text(host, 'detail')).toBe('Next attempt in 2 seconds')
    expect(text(host, 'detail')).not.toContain('ms')
  })

  it('counts the last second down to now rather than to zero', () => {
    const { host } = subject({ kind: 'waiting', nextAttemptInMs: 0 }, { minimumVisibleMs: 0 })

    expect(text(host, 'detail')).toBe('Retrying now')
  })

  it('uses a native modal dialog and calls showModal', () => {
    const showModal = vi.spyOn(HTMLDialogElement.prototype, 'showModal')
    const { host } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    expect(dialog(host)).toBeTruthy()
    expect(showModal).toHaveBeenCalledOnce()
  })

  it('does not offer a close control while connection is unavailable', () => {
    const { host } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    expect(host.querySelector('.nocx-dialog__close')).toBeNull()
  })

  it('keeps the connection overlay open when Escape is pressed', () => {
    const { host } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    fireEvent.keyDown(document, { key: 'Escape' })

    expect(dialog(host).open).toBe(true)
  })

  it('is a mark and a Button, with no second spinner beside the ring', () => {
    const { host } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    expect(host.querySelector('.ui-connection-overlay__mark')).not.toBeNull()
    expect(host.querySelector('.ui-connection-overlay__logo')).not.toBeNull()
    // The ring around the mark IS the indicator. A Spinner under it would be
    // two things saying one thing.
    expect(host.querySelector('.ui-spinner')).toBeNull()
    expect(host.querySelector<HTMLButtonElement>('.ui-button')).toBeNull()
    expect(host.querySelector('.ui-connection-overlay')?.getAttribute('style')).toBeNull()
  })

  it('is not a card: no panel chrome, and the ground is opaque in every theme', () => {
    const { host } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    // It is the application's loading screen, not a message box floating over
    // a terminal a user cannot reach. Spec §8: "the opaque overlay covers it
    // regardless".
    expect(host.querySelector('.nocx-dialog__panel')).toBeNull()
    expect(host.querySelector('.nocx-dialog__header')).toBeNull()
    const ground = rule('dialog.ui-connection-overlay')
    expect(ground).toMatch(/background\s*:\s*var\(--color-canvas\)/)
    expect(ground).not.toMatch(/--color-scrim/)
    expect(rule('dialog.ui-connection-overlay::backdrop')).toMatch(
      /background\s*:\s*var\(--color-canvas\)/,
    )
  })
  it.each([
    ['connecting', { kind: 'connecting' } as const],
    ['waiting', { kind: 'waiting', nextAttemptInMs: 1000 } as const],
    ['blocked', { kind: 'blocked', message: 'm', remedy: 'r' } as const],
  ])('carries its state on the root so the ring is keyed from it in %s', (_name, initial) => {
    const { host } = subject(initial, { minimumVisibleMs: 0 })
    const root = overlay(host)

    expect(root.dataset.state).toBe(initial.kind)
    expect(root.querySelector('.ui-connection-overlay__mark')).not.toBeNull()
  })

  it('turns the ring only while an attempt is in flight, and never under reduced motion', () => {
    const css = sheet()

    // The arc is the moving half of the mark; the track is always there, so the
    // composition does not change size between states. Motion states what is
    // true: an arc that keeps turning through three minutes of backoff is a
    // progress indicator that is lying.
    const arc =
      css.match(/\.ui-connection-overlay__mark::after\s*\{([^}]*conic-gradient[^}]*)\}/)?.[1] ?? ''
    expect(arc).not.toBe('')
    expect(arc).toMatch(/opacity\s*:\s*0/)
    expect(
      css.match(
        /\[data-state=['"]connecting['"]\]\s+\.ui-connection-overlay__mark::after\s*\{([^}]*)\}/,
      )?.[1] ?? '',
    ).toMatch(/animation\s*:/)
    const reduced =
      css.match(/@media \(prefers-reduced-motion: reduce\)\s*\{([\s\S]*)\n\}/)?.[1] ?? ''
    expect(reduced).toMatch(/animation\s*:\s*none/)
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

    expect(text(host, 'headline')).toBe(message)
    expect(text(host, 'detail')).toBe(remedy)
    expect(overlay(host).textContent).toBe(`${message}${remedy}Retry`)
  })

  it('never shows an empty sentence: a held overlay keeps the state it was in', () => {
    vi.useFakeTimers()
    const { host, state } = subject({ kind: 'connecting' }, { minimumVisibleMs: 1000 })

    // The socket came up inside the startup minimum. `online` has no sentence
    // and no mark activity, so displaying it left a bordered box holding one
    // motionless icon for the rest of the second — which is what the owner
    // photographed. While the overlay is up, the last real state is what it
    // shows.
    state({ kind: 'online' })
    vi.advanceTimersByTime(500)

    expect(dialog(host).open).toBe(true)
    expect(overlay(host).dataset.state).toBe('connecting')
    expect(text(host, 'headline')).toBe('Connecting to nocx…')
  })

  it('holds startup visibility for the default minimum, then fades out', () => {
    vi.useFakeTimers()
    const { host } = subject({ kind: 'online' })
    const modal = dialog(host)

    expect(modal.open).toBe(true)
    vi.advanceTimersByTime(999)
    expect(modal.open).toBe(true)
    vi.advanceTimersByTime(1)
    // The minimum is up: the ground starts fading. It is a full-screen opaque
    // surface, and cutting it away in one frame is the jolt the fade exists to
    // remove — so it stays in the top layer for the length of its own
    // transition and closes at the end of it.
    expect(modal.open).toBe(true)
    expect(overlay(host).dataset.exiting).toBe('true')
    vi.advanceTimersByTime(EXIT_MS)
    expect(modal.open).toBe(false)
  })

  it('cancels a departure that is interrupted by a new outage', () => {
    vi.useFakeTimers()
    const { host, state } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0 })

    state({ kind: 'online' })
    expect(overlay(host).dataset.exiting).toBe('true')
    state({ kind: 'waiting', nextAttemptInMs: 2000 })
    vi.advanceTimersByTime(EXIT_MS)

    // A blip that recovers and drops again inside the fade must not leave the
    // window uncovered: the departure is abandoned, not queued.
    expect(dialog(host).open).toBe(true)
    expect(overlay(host).dataset.exiting).toBeUndefined()
    expect(text(host, 'headline')).toBe('Cannot reach the nocx backend')
  })

  it('shows a later connecting state immediately without applying startup minimum again', () => {
    vi.useFakeTimers()
    const { host, state } = subject({ kind: 'online' }, { minimumVisibleMs: 100 })
    vi.advanceTimersByTime(100 + EXIT_MS)
    expect(dialog(host).open).toBe(false)

    state({ kind: 'connecting' })
    expect(dialog(host).open).toBe(true)
    state({ kind: 'online' })
    vi.advanceTimersByTime(EXIT_MS)
    expect(dialog(host).open).toBe(false)
  })

  it('hides online immediately after startup minimum has elapsed', () => {
    vi.useFakeTimers()
    const { host, state } = subject({ kind: 'connecting' }, { minimumVisibleMs: 100 })
    vi.advanceTimersByTime(100)

    state({ kind: 'online' })
    vi.advanceTimersByTime(EXIT_MS)
    expect(dialog(host).open).toBe(false)
  })

  it('notifies after becoming hidden, including repeated visibility cycles', async () => {
    vi.useFakeTimers()
    const onHidden = vi.fn()
    const { state } = subject({ kind: 'connecting' }, { minimumVisibleMs: 0, onHidden })

    expect(onHidden).not.toHaveBeenCalled()
    state({ kind: 'online' })
    vi.advanceTimersByTime(EXIT_MS)
    await vi.advanceTimersByTimeAsync(0)
    expect(onHidden).toHaveBeenCalledOnce()

    state({ kind: 'connecting' })
    state({ kind: 'online' })
    vi.advanceTimersByTime(EXIT_MS)
    await vi.advanceTimersByTimeAsync(0)
    expect(onHidden).toHaveBeenCalledTimes(2)
  })

  it('waits for a nonzero startup minimum before notifying hidden', async () => {
    vi.useFakeTimers()
    const onHidden = vi.fn()
    const { host, state } = subject({ kind: 'connecting' }, { minimumVisibleMs: 100, onHidden })

    state({ kind: 'online' })
    await Promise.resolve()
    expect(onHidden).not.toHaveBeenCalled()
    vi.advanceTimersByTime(99)
    expect(onHidden).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1 + EXIT_MS)
    await vi.advanceTimersByTimeAsync(0)
    expect(onHidden).toHaveBeenCalledOnce()

    state({ kind: 'connecting' })
    state({ kind: 'online' })
    vi.advanceTimersByTime(EXIT_MS)
    await vi.advanceTimersByTimeAsync(0)
    expect(onHidden).toHaveBeenCalledTimes(2)
    expect(host.querySelector('dialog')?.open).toBe(false)
  })

  it('does not notify for an initially hidden online overlay', async () => {
    const onHidden = vi.fn()
    subject({ kind: 'online' }, { minimumVisibleMs: 0, onHidden })

    await Promise.resolve()
    expect(onHidden).not.toHaveBeenCalled()
  })
})
