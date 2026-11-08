// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { readFileSync } from 'node:fs'
import { ToastHost, showToast, updateToast, dismissToast, clearToasts, toasts } from './toast'

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
    // 56px since the mockup-decision-record pass (round 2, nocx-9bpeq.23):
    // was 48px until the activity rail scaled up with the tab strip.
    expect(TOKENS_CSS).toMatch(/--activity-bar-width:\s*56px/)
  })
})

// ── Typed actions and a link (nocx-1ebrs.54) ─────────────────────────────
//
// An update toast must offer what it announces: a Release notes link and
// Install now / Later actions. That capability belongs to the KIT — a caller
// hand-rolling its own button row inside a toast would be a second
// vocabulary for one concept, exactly what this kit exists to prevent. These
// assert the capability the way a caller reaches it: the options are typed
// on showToast, the actions are real kit Buttons, and activating one both
// runs the caller's handler and dismisses the toast — a toast whose Later
// leaves the toast up would make the label a lie.
describe('showToast with actions and a link', () => {
  it.each(['resolve', 'reject', 'throw'])(
    'hands link activation synchronously to the opener: %s',
    async (outcome) => {
      const error = new Error('browser refused /private/path')
      const open = vi.fn<(url: string) => Promise<void>>().mockImplementation(() => {
        if (outcome === 'throw') throw error
        return outcome === 'reject' ? Promise.reject(error) : Promise.resolve()
      })
      const reportError = vi.fn()
      const { container } = render(() => <ToastHost openUrl={open} onOpenUrlError={reportError} />)
      showToast({
        message: 'Update available',
        link: { label: 'Release notes', href: 'https://example.com/notes' },
      })
      const link = container.querySelector('a')!
      const event = new MouseEvent('click', { bubbles: true, cancelable: true })
      link.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(true)
      expect(open).toHaveBeenCalledWith('https://example.com/notes')
      await Promise.resolve()
      if (outcome === 'resolve') expect(toasts()).toHaveLength(1)
      else {
        expect(toasts()).toHaveLength(2)
        expect(toasts()[1].level).toBe('danger')
        expect(toasts()[1].message).toContain('The browser could not be opened.')
        expect(toasts()[1].message).not.toContain('/private/path')
        expect(reportError).toHaveBeenCalledWith(error)
      }
    },
  )

  it('renders the link as an anchor to where it points', () => {
    const { container } = render(() => <ToastHost />)
    showToast({
      message: 'nocx 0.7.0 is available',
      link: { label: 'Release notes', href: 'https://example.com/notes' },
    })
    const link = container.querySelector('.ui-toast__link') as HTMLAnchorElement
    expect(link).not.toBeNull()
    expect(link.getAttribute('href')).toBe('https://example.com/notes')
    expect(link.textContent).toBe('Release notes')
    // Navigation is handed to the injected opener, never the webview.
    expect(link.getAttribute('target')).toBeNull()
  })

  it('renders actions as kit buttons that reach the caller', () => {
    const { container } = render(() => <ToastHost />)
    const install = vi.fn()
    const later = vi.fn()
    showToast({
      message: 'nocx 0.7.0 is available',
      actions: [
        { label: 'Install now', onClick: install, variant: 'primary' },
        { label: 'Later', onClick: later },
      ],
    })
    const buttons = [...container.querySelectorAll('.ui-toast button.ui-button')]
    expect(buttons.map((b) => b.textContent)).toEqual(['Install now', 'Later'])
    ;(buttons[1] as HTMLButtonElement).click()
    expect(later).toHaveBeenCalledTimes(1)
    expect(install).not.toHaveBeenCalled()
  })

  it('marks the action a person is meant to press as the toast primary', () => {
    const { container } = render(() => <ToastHost />)
    showToast({
      message: 'nocx 0.7.0 is available',
      actions: [
        { label: 'Install now', onClick: () => {}, variant: 'primary' },
        { label: 'Later', onClick: () => {} },
      ],
    })
    const primary = container.querySelector('.ui-toast button.ui-button[data-variant="primary"]')
    expect(primary?.textContent).toBe('Install now')
  })

  it('closes the toast when an action runs, so no action is its own dismiss button too', () => {
    const { container } = render(() => <ToastHost />)
    showToast({
      message: 'nocx 0.7.0 is available',
      actions: [{ label: 'Later', onClick: () => {} }],
    })
    ;(container.querySelector('.ui-toast button.ui-button') as HTMLButtonElement).click()
    expect(container.querySelector('.ui-toast')).toBeNull()
    expect(toasts()).toHaveLength(0)
  })
})

// A measure the toast reports while it lasts (nocx-vh2hr.1.25). The bar is the
// kit's ProgressBar — the toast places it and never paints one of its own —
// and the caller's figure is the words beside it, because the kit does not know
// what the numbers count or which language they are read in.
describe('a toast that reports a measure', () => {
  const measure = (value: number, label: string) => ({
    value,
    label,
    ariaLabel: 'Update download',
  })

  it('draws the kit bar with the caller’s figure beside it', () => {
    const { container } = render(() => <ToastHost />)
    showToast({ message: 'Downloading nocx 0.9.0…', progress: measure(0.42, '42% · 63 of 150 MB') })
    const bar = container.querySelector('.ui-progress-bar')
    expect(bar).not.toBeNull()
    expect(bar?.getAttribute('role')).toBe('progressbar')
    expect(bar?.getAttribute('aria-label')).toBe('Update download')
    expect(bar?.getAttribute('aria-valuenow')).toBe('42')
    // The measure is the point in a toast, so it is the kit's taller variant;
    // the requirement is only that it IS the kit's bar.
    expect(bar?.getAttribute('data-size')).toBe('md')
    expect(container.querySelector('.ui-toast__figure')?.textContent).toBe('42% · 63 of 150 MB')
  })

  it('moves the same bar when the report is updated in place', () => {
    const { container } = render(() => <ToastHost />)
    const id = showToast({ message: 'Downloading…', progress: measure(0.1, '10%') })
    const bar = container.querySelector('.ui-progress-bar')
    expect(updateToast(id, { message: 'Downloading…', progress: measure(0.75, '75%') })).toBe(true)
    // Identity, not equality: a replaced element would restart the bar's
    // transition and re-announce the toast on every tick.
    expect(container.querySelector('.ui-progress-bar')).toBe(bar)
    expect(bar?.getAttribute('aria-valuenow')).toBe('75')
    expect(container.querySelector('.ui-toast__figure')?.textContent).toBe('75%')
    expect(container.querySelector('.ui-toast__message')?.firstChild?.textContent).toBe(
      'Downloading…',
    )
  })

  it('reports false for a toast that is gone rather than raising a new one', () => {
    const { container } = render(() => <ToastHost />)
    const id = showToast({ message: 'Downloading…' })
    dismissToast(id)
    expect(updateToast(id, { message: 'Downloading…', progress: measure(0.5, '50%') })).toBe(false)
    expect(container.querySelectorAll('.ui-toast')).toHaveLength(0)
  })

  it('renders no bar for a toast that is only a sentence', () => {
    const { container } = render(() => <ToastHost />)
    showToast({ message: 'Can not reach the release server.' })
    expect(container.querySelector('.ui-progress-bar')).toBeNull()
  })
})
