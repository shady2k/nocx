// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { readFileSync } from 'node:fs'
import { ToastHost, showToast, dismissToast, clearToasts, toasts } from './toast'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  clearToasts()
  cleanup()
  vi.useRealTimers()
})

describe('showToast', () => {
  it('queues a toast at the requested level', () => {
    showToast({ level: 'success', message: 'Exported.' })
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].level).toBe('success')
    expect(toasts()[0].message).toBe('Exported.')
  })

  it('defaults to info', () => {
    showToast({ message: 'Something happened.' })
    expect(toasts()[0].level).toBe('info')
  })

  it('auto-dismisses info and success after their default duration', () => {
    showToast({ level: 'success', message: 'Exported.' })
    vi.advanceTimersByTime(3999)
    expect(toasts()).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(toasts()).toHaveLength(0)
  })

  // The rule the whole level table exists for: an error the user was not looking
  // at is an error they never saw.
  it('keeps a danger toast up indefinitely', () => {
    showToast({ level: 'danger', message: 'Export failed.' })
    vi.advanceTimersByTime(10 * 60 * 1000)
    expect(toasts()).toHaveLength(1)
  })

  it('lets an explicit duration override the level default', () => {
    showToast({ level: 'danger', message: 'Export failed.', duration: 1000 })
    vi.advanceTimersByTime(1000)
    expect(toasts()).toHaveLength(0)
  })

  it('lets duration 0 pin a success toast', () => {
    showToast({ level: 'success', message: 'Imported, 3 credentials unmapped.', duration: 0 })
    vi.advanceTimersByTime(60_000)
    expect(toasts()).toHaveLength(1)
  })

  it('stacks several toasts in the order raised', () => {
    showToast({ message: 'first' })
    showToast({ message: 'second' })
    expect(toasts().map((t) => t.message)).toEqual(['first', 'second'])
  })

  it('dismisses one without touching the others', () => {
    const first = showToast({ level: 'danger', message: 'first' })
    showToast({ level: 'danger', message: 'second' })
    dismissToast(first)
    expect(toasts().map((t) => t.message)).toEqual(['second'])
  })
})

describe('ToastHost', () => {
  it('renders each toast with its level on the element', () => {
    const { container } = render(() => <ToastHost />)
    showToast({ level: 'warning', message: 'Passphrase is required.' })
    const toast = container.querySelector('.ui-toast')
    expect(toast?.getAttribute('data-level')).toBe('warning')
    expect(toast?.querySelector('.ui-toast__message')?.textContent).toBe('Passphrase is required.')
  })

  it('removes a toast from the DOM when its close button is clicked', () => {
    const { container } = render(() => <ToastHost />)
    showToast({ level: 'danger', message: 'Export failed.' })
    const close = container.querySelector('.ui-toast button') as HTMLButtonElement
    close.click()
    expect(container.querySelector('.ui-toast')).toBeNull()
  })

  it('announces politely rather than interrupting', () => {
    const { container } = render(() => <ToastHost />)
    const host = container.querySelector('.ui-toast-host')
    expect(host?.getAttribute('aria-live')).toBe('polite')
  })
})

// ── The notification area clears the activity bar (nocx-u5zoc) ────────────
//
// The host is fixed to the viewport's bottom-right corner. Once the activity
// bar is on the trailing edge, that corner is the rail's bottom zone — the
// API workbench and Settings buttons — and a `danger` toast is sticky, so it
// would sit on two global actions until dismissed.
//
// Read off the stylesheet SOURCE because jsdom loads no CSS: a
// getComputedStyle assertion here would pass against a stylesheet that says
// anything at all. Same reason sidebar.test.tsx reads its file. What a user
// can see is asserted in a real browser by
// e2e/toast-clears-activity-bar.spec.ts — this pair is the ratchet against
// somebody re-hardcoding the number, not the proof that the layout is right.
describe('the notification area clears the activity bar', () => {
  const TOAST_CSS = readFileSync('src/styles/components/toast.css', 'utf8')
  const TOKENS_CSS = readFileSync('src/styles/tokens.css', 'utf8')

  it('offsets its right edge by the rail width rather than hugging the corner', () => {
    expect(TOAST_CSS).toMatch(
      /\.ui-toast-host\s*\{[^}]*right:\s*calc\(var\(--space-4\)\s*\+\s*var\(--activity-bar-width\)\)/,
    )
  })

  it('the rail width is a token, so the two rules cannot drift apart', () => {
    expect(TOKENS_CSS).toMatch(/--activity-bar-width:\s*48px/)
  })
})
