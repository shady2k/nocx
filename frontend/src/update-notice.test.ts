// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mountUpdateNotice, type UpdateNoticeController } from './update-notice'

vi.mock('../wailsjs/go/main/WailsApp', () => ({
  ApplyUpdate: vi.fn<() => Promise<void>>(),
  CheckForUpdate: vi.fn(),
  ReportHealthy: vi.fn(),
  GetWSPort: vi.fn(),
  GetWSToken: vi.fn(),
}))

import { ApplyUpdate } from '../wailsjs/go/main/WailsApp'

function stateClass(kind: string): string {
  if (kind === 'hidden' || kind === 'available') return 'update-notice'
  return `update-notice ${kind}`
}

const ALL_STATES = ['hidden', 'available', 'downloading', 'pending', 'error'] as const

// Every directed pair (from → to, from ≠ to) = 16 transitions (hidden excluded as target).
const ALL_PAIRS: [string, string][] = ALL_STATES.flatMap((from) =>
  ALL_STATES.filter((to) => to !== from && to !== 'hidden').map((to): [string, string] => [
    from,
    to,
  ]),
)

function setState(kind: string, ctrl: UpdateNoticeController): void {
  switch (kind) {
    case 'hidden':
      break // already hidden from mount
    case 'available':
      ctrl.showAvailable('1.0.0', 'https://example.com/release')
      break
    case 'downloading':
      ctrl.showDownloading()
      break
    case 'pending':
      ctrl.showPendingRestart('1.0.0')
      break
    case 'error':
      ctrl.showError('test error')
      break
  }
}

describe('UpdateNotice', () => {
  let bar: HTMLElement
  let ctrl: UpdateNoticeController

  beforeEach(() => {
    bar = document.createElement('div')
    document.body.append(bar)
    ctrl = mountUpdateNotice(bar)
    vi.mocked(ApplyUpdate).mockReset()
    vi.mocked(ApplyUpdate).mockResolvedValue(undefined)
  })

  afterEach(() => {
    bar.remove()
  })

  // ── Individual state renders ──────────────────────────────────────────

  it('hidden state sets the hidden attribute', () => {
    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el).not.toBeNull()
    expect(el.hasAttribute('hidden')).toBe(true)
    expect(el.className).toBe('update-notice')
  })

  it('showAvailable renders version, link, and button', () => {
    ctrl.showAvailable('1.0.0', 'https://example.com/release')

    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el).not.toBeNull()
    expect(el.className).toBe('update-notice')
    expect(el.hasAttribute('hidden')).toBe(false)
    expect(el.textContent).toContain('1.0.0 available')
    expect(el.querySelector('.update-notes-link')).not.toBeNull()
    expect(el.querySelector('.update-apply-btn')).not.toBeNull()
  })

  it('showDownloading sets downloading class and content', () => {
    ctrl.showDownloading()

    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el).not.toBeNull()
    expect(el.className).toBe('update-notice downloading')
    expect(el.textContent).toBe('Downloading update…')
  })

  it('showPendingRestart sets pending class and content', () => {
    ctrl.showPendingRestart('1.0.0')

    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el).not.toBeNull()
    expect(el.className).toBe('update-notice pending')
    expect(el.textContent).toContain('1.0.0 installed')
    expect(el.textContent).toContain('restart to apply')
  })

  it('showError sets error class and content', () => {
    ctrl.showError('connection lost')

    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el).not.toBeNull()
    expect(el.className).toBe('update-notice error')
    expect(el.textContent).toContain('Update failed')
    expect(el.textContent).toContain('connection lost')
  })

  // ── Every state-to-state transition ───────────────────────────────────

  it.each(ALL_PAIRS)('%s→%s resets className to target state', (from, to) => {
    setState(from, ctrl)
    setState(to, ctrl)

    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el.className).toBe(stateClass(to))
    if (to === 'hidden') {
      expect(el.hasAttribute('hidden')).toBe(true)
    } else {
      expect(el.hasAttribute('hidden')).toBe(false)
    }
  })

  // ── Apply path: successful update ─────────────────────────────────────

  it('click Update → downloading → pending on success', async () => {
    vi.mocked(ApplyUpdate).mockResolvedValue(undefined)

    ctrl.showAvailable('1.0.0', 'https://example.com/release')

    // Click the Update button — starts async apply.
    const btn = bar.querySelector('.update-apply-btn') as HTMLButtonElement
    expect(btn).not.toBeNull()
    btn.click()

    // Immediately after click: downloading state.
    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el.className).toBe('update-notice downloading')

    // Wait for the async ApplyUpdate to resolve.
    await vi.waitFor(() => {
      expect(el.className).toBe('update-notice pending')
    })
  })

  // ── Apply path: failed update ─────────────────────────────────────────

  it('click Update → downloading → error on failure', async () => {
    vi.mocked(ApplyUpdate).mockRejectedValue(new Error('network error'))

    ctrl.showAvailable('1.0.0', 'https://example.com/release')

    const btn = bar.querySelector('.update-apply-btn') as HTMLButtonElement
    expect(btn).not.toBeNull()
    btn.click()

    // Immediately after click: downloading state.
    const el = bar.querySelector('.update-notice') as HTMLElement
    expect(el.className).toBe('update-notice downloading')

    // Wait for the async ApplyUpdate to reject.
    await vi.waitFor(() => {
      expect(el.className).toBe('update-notice error')
    })
    expect(el.textContent).toContain('network error')
  })

  // ── Apply path: non-Error rejection ───────────────────────────────────

  it('click Update → downloading → error with string rejection', async () => {
    vi.mocked(ApplyUpdate).mockRejectedValue('plain string')

    ctrl.showAvailable('1.0.0', 'https://example.com/release')

    const btn = bar.querySelector('.update-apply-btn') as HTMLButtonElement
    btn.click()

    await vi.waitFor(() => {
      const el = bar.querySelector('.update-notice') as HTMLElement
      expect(el.className).toBe('update-notice error')
      expect(el.textContent).toContain('plain string')
    })
  })
})
