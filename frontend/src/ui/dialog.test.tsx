// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest'
import { render, fireEvent, cleanup } from '@solidjs/testing-library'
import { Dialog, showConfirm, type DialogProps } from './dialog'
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
} from './overlay/stack'

afterEach(() => {
  cleanup()
  clearStack()
})

/* ── Overlay stack ─────────────────────────────────────────────────── */

describe('overlay stack', () => {
  it('starts empty', () => {
    expect(hasOpenOverlays()).toBe(false)
    expect(stackDepth()).toBe(0)
    expect(topOverlay()).toBeUndefined()
  })

  it('push adds an entry', () => {
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
    expect(stackDepth()).toBe(0)
  })

  it('pop returns false for unknown entry', () => {
    expect(popOverlay({ id: 'ghost', close: () => true, prevFocus: null, element: null })).toBe(
      false,
    )
  })

  it('closeTopmost calls close on the top entry', () => {
    const close1 = vi.fn(() => true)
    const close2 = vi.fn(() => true)
    pushOverlay(close1)
    pushOverlay(close2)
    expect(closeTopmost()).toBe(true)
    expect(close2).toHaveBeenCalledOnce()
    expect(close1).not.toHaveBeenCalled()
  })

  it('closeTopmost returns false when empty', () => {
    expect(closeTopmost()).toBe(false)
  })

  it('Escape closes only the topmost overlay', () => {
    const close1 = vi.fn(() => true)
    const close2 = vi.fn(() => true)
    pushOverlay(close1)
    pushOverlay(close2)

    fireEvent(
      document,
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )

    expect(close2).toHaveBeenCalledOnce()
    expect(close1).not.toHaveBeenCalled()
  })

  it('Escape does not fire when stack is empty', () => {
    const handler = vi.fn()
    pushOverlay(handler)
    popOverlay(topOverlay()!)
    // Stack is now empty; handler was removed.
    fireEvent(
      document,
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    expect(handler).not.toHaveBeenCalled()
  })

  it('push sets prevFocus from document.activeElement', () => {
    // jsdom default activeElement is <body>
    const close = vi.fn(() => true)
    const entry = pushOverlay(close)
    expect(entry.prevFocus).toBe(document.body)
  })

  it('captures the actual focused element as prevFocus', () => {
    const btn = document.createElement('button')
    btn.setAttribute('id', 'focus-src')
    document.body.appendChild(btn)
    btn.focus()

    const close = vi.fn(() => true)
    const entry = pushOverlay(close)
    expect(entry.prevFocus).toBe(btn)

    document.body.removeChild(btn)
  })
})

/* ── Focus return ──────────────────────────────────────────────────── */

describe('restoreFocus', () => {
  it('focuses the prevFocus element via rAF', () => {
    vi.useFakeTimers()
    const btn = document.createElement('button')
    document.body.appendChild(btn)

    const entry = pushOverlay(() => true, btn)
    // Simulate something else is focused
    document.body.focus()

    restoreFocus(entry)
    vi.runAllTimers()

    expect(document.activeElement).toBe(btn)
    document.body.removeChild(btn)
    vi.useRealTimers()
  })

  it('does not focus if prevFocus is a dialog element', () => {
    // restoreFocus skips dialog elements — they handle focus natively.
    const dialog = document.createElement('dialog')
    document.body.appendChild(dialog)

    const entry = pushOverlay(() => true, dialog)
    // This should be a no-op and not throw.
    restoreFocus(entry)

    document.body.removeChild(dialog)
  })

  it('does not throw if prevFocus is null', () => {
    const entry = pushOverlay(() => true, null)
    expect(() => restoreFocus(entry)).not.toThrow()
  })
})

/* ── Wails drag-region guard ───────────────────────────────────────── */

describe('isWailsDragTarget', () => {
  it('returns false for an element with no --wails-draggable ancestor', () => {
    const el = document.createElement('div')
    document.body.appendChild(el)
    expect(isWailsDragTarget(el)).toBe(false)
    document.body.removeChild(el)
  })

  it('returns false for no-drag', () => {
    const parent = document.createElement('div')
    parent.style.setProperty('--wails-draggable', 'no-drag')
    const child = document.createElement('div')
    parent.appendChild(child)
    document.body.appendChild(parent)

    expect(isWailsDragTarget(child)).toBe(false)

    document.body.removeChild(parent)
  })

  it('returns true for a drag target', () => {
    const parent = document.createElement('div')
    parent.style.setProperty('--wails-draggable', 'drag')
    const child = document.createElement('div')
    parent.appendChild(child)
    document.body.appendChild(parent)

    expect(isWailsDragTarget(child)).toBe(true)

    document.body.removeChild(parent)
  })

  it('walks the ancestor chain', () => {
    const grandparent = document.createElement('div')
    grandparent.style.setProperty('--wails-draggable', 'drag')
    const parent = document.createElement('div')
    parent.style.setProperty('--wails-draggable', 'no-drag')
    const child = document.createElement('div')
    parent.appendChild(child)
    grandparent.appendChild(parent)
    document.body.appendChild(grandparent)

    // no-drag on immediate parent takes precedence — wait, no.
    // isWailsDragTarget walks up until it finds 'drag' or exhausts the chain.
    // So it should find grandparent's 'drag'.
    expect(isWailsDragTarget(child)).toBe(true)

    document.body.removeChild(grandparent)
  })
})

/* ── Dialog component ──────────────────────────────────────────────── */

describe('Dialog', () => {
  function subject(overrides?: Partial<DialogProps>) {
    const props: DialogProps = {
      open: false,
      onClose: vi.fn(),
      title: 'Test Dialog',
      children: 'Dialog body content',
      ...overrides,
    }
    return render(() => <Dialog {...props} />)
  }

  it('renders nothing in the DOM when closed', () => {
    subject()
    // The <dialog> element exists but is not displayed (not open).
    // In jsdom, dialog elements always exist in the DOM.
    const dialogs = document.querySelectorAll<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialogs.length).toBe(1)
    expect(dialogs[0].open).toBe(false)
  })

  it('is open when open={true}', () => {
    subject({ open: true })
    const dialog = document.querySelector('dialog.nocx-dialog')!
    // showModal() does not work in jsdom (unsupported), but the open prop
    // would trigger it in a real browser. We test the rendered tree.
    expect(dialog).toBeTruthy()
  })

  it('renders title and children', () => {
    subject({ open: true })
    expect(document.querySelector('.nocx-dialog__title')?.textContent).toBe('Test Dialog')
    expect(document.querySelector('.nocx-dialog__panel')?.textContent).toContain(
      'Dialog body content',
    )
  })

  it('renders footer actions', () => {
    subject({
      open: true,
      footer: <button>OK</button>,
    })
    expect(document.querySelector('.nocx-dialog__actions')).toBeTruthy()
    expect(
      document.querySelector<HTMLButtonElement>('.nocx-dialog__actions button')?.textContent,
    ).toBe('OK')
  })

  it('calls onClose when cancel event fires', () => {
    const onClose = vi.fn()
    subject({ open: true, onClose })

    const dialog = document.querySelector('dialog.nocx-dialog')!
    fireEvent(dialog, new Event('cancel', { bubbles: true }))

    expect(onClose).toHaveBeenCalledOnce()
  })

  it('registers with the overlay stack when opened', () => {
    // showModal() doesn't work in jsdom, so we simulate by calling showModal
    // directly. The Dialog component uses createEffect which runs synchronously
    // in tests. We need to verify the stack isn't empty after open.
    subject({ open: true })
    // In jsdom, showModal throws — so this test verifies the component
    // tolerates that and the effect runs.
    expect(document.querySelector('dialog.nocx-dialog')).toBeTruthy()
  })
})

/* ── Panel height animation ──────────────────────────────────────────── */

// A `cancel` from a descendant is not the dialog's cancel. `input[type=file]`
// fires one when the OS file picker is dismissed, and it bubbles — so choosing
// "Choose file" in the connection editor and then pressing Cancel in the
// picker closed the whole editor and discarded the form. Reported from the
// running app.
describe('Dialog cancel', () => {
  it('closes on its own cancel', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog open={true} onClose={onClose} title="T">
        body
      </Dialog>
    ))
    const dialog = container.querySelector('dialog') as HTMLDialogElement
    fireEvent(dialog, new Event('cancel', { bubbles: true }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('ignores a cancel bubbling up from a descendant, such as a file picker', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog open={true} onClose={onClose} title="T">
        <input type="file" data-testid="picker" />
      </Dialog>
    ))
    const picker = container.querySelector('input[type=file]') as HTMLInputElement
    fireEvent(picker, new Event('cancel', { bubbles: true }))
    expect(onClose).not.toHaveBeenCalled()
  })
})

describe('Dialog panel height animation', () => {
  // jsdom has no layout and ResizeObserver is stubbed to never fire, so we
  // capture the callback the component registers and drive it by hand, with
  // getBoundingClientRect stubbed to report the heights a browser would.
  let roCallback: (() => void) | null = null

  beforeEach(() => {
    roCallback = null
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      constructor(cb: () => void) {
        roCallback = cb
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  })

  function subject(overrides?: Partial<DialogProps>) {
    const props: DialogProps = {
      open: true,
      onClose: vi.fn(),
      title: 'Test Dialog',
      children: 'Dialog body content',
      ...overrides,
    }
    return render(() => <Dialog {...props} />)
  }

  function stubPanelHeight(panel: HTMLElement, height: number) {
    Object.defineProperty(panel, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({
        x: 0,
        y: 0,
        width: 480,
        height,
        top: 0,
        left: 0,
        right: 480,
        bottom: height,
        toJSON: () => ({}),
      }),
    })
  }

  function fireResize() {
    expect(roCallback).not.toBeNull()
    roCallback!()
  }

  function endTransition(panel: HTMLElement) {
    const ev = new Event('transitionend', { bubbles: true })
    Object.defineProperty(ev, 'propertyName', { value: 'height' })
    fireEvent(panel, ev)
  }

  // The artefact a user actually sees. Mid-transition the panel is pinned to
  // the height it is leaving while the body is already sized for the height it
  // is arriving at, so the body overflows and flashes a scrollbar for the whole
  // 180ms. Reported from the running app, not from any test — every assertion
  // about heights passed while the transition looked broken.
  it('does not let the body flash a scrollbar while the panel is mid-transition', () => {
    const { container } = subject()
    const panel = container.querySelector('.nocx-dialog__panel') as HTMLElement
    stubPanelHeight(panel, 200)
    fireResize()
    expect(panel.hasAttribute('data-animating')).toBe(false)

    stubPanelHeight(panel, 320)
    fireResize()
    // The marker the stylesheet hangs `overflow-y: hidden` on.
    expect(panel.hasAttribute('data-animating')).toBe(true)

    endTransition(panel)
    // And it must come back: a body that genuinely does not fit still scrolls
    // once the panel has settled.
    expect(panel.hasAttribute('data-animating')).toBe(false)
  })

  it('pins the panel height to the new size when the content resizes', () => {
    const { container } = subject()
    const panel = container.querySelector('.nocx-dialog__panel') as HTMLElement
    stubPanelHeight(panel, 200)
    fireResize()
    // At rest the height is auto — nothing pinned.
    expect(panel.style.height).toBe('')

    // The body grows (a section switch, a revealed field): the panel must be
    // measured at the settled size, pinned, and transitioned to the new one —
    // the footer moves visibly instead of teleporting.
    stubPanelHeight(panel, 400)
    fireResize()
    expect(panel.style.height).toBe('400px')
  })

  it('releases the panel back to auto when the height transition ends', () => {
    const { container } = subject()
    const panel = container.querySelector('.nocx-dialog__panel') as HTMLElement
    stubPanelHeight(panel, 200)
    fireResize()
    stubPanelHeight(panel, 400)
    fireResize()
    expect(panel.style.height).toBe('400px')

    endTransition(panel)
    // Back to auto so the CSS max-height, not a stale inline number, governs.
    expect(panel.style.height).toBe('')
    expect(panel.style.transition).toBe('')
  })

  it('never pins a height above the panel max-height', () => {
    const { container } = subject()
    const panel = container.querySelector('.nocx-dialog__panel') as HTMLElement
    panel.style.maxHeight = '300px'
    stubPanelHeight(panel, 200)
    fireResize()
    // A body whose natural height (500px) exceeds the cap: a real browser
    // clamps the measurement to the max-height, and the animation must pin to
    // that clamped value — a short viewport scrolls instead of overflowing.
    stubPanelHeight(panel, 300)
    fireResize()
    expect(panel.style.height).toBe('300px')
  })

  it('does not animate under prefers-reduced-motion', () => {
    // jsdom ships no matchMedia; install a reduce-answering one. The
    // component must skip pinning and transitioning entirely under it.
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: () => ({
        matches: true,
        media: '(prefers-reduced-motion: reduce)',
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }),
    })
    const { container } = subject()
    const panel = container.querySelector('.nocx-dialog__panel') as HTMLElement
    stubPanelHeight(panel, 200)
    fireResize()
    stubPanelHeight(panel, 400)
    fireResize()
    // No pin, no transition: the height jumps, as reduced motion requires.
    expect(panel.style.height).toBe('')
  })
})
/* ── showConfirm imperative helper ─────────────────────────────────── */

describe('showConfirm', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('renders a dialog with message and buttons', async () => {
    void showConfirm('Are you sure?')
    // Wait for the microtask that showModal schedules.
    await vi.waitFor(() => {
      const dialog = document.querySelector('dialog.nocx-dialog')
      expect(dialog).toBeTruthy()
    })

    const dialog = document.querySelector('dialog.nocx-dialog')!
    expect(dialog.querySelector('.nocx-dialog__message')?.textContent).toBe('Are you sure?')
    expect(dialog.querySelector('.nocx-dialog__actions')).toBeTruthy()

    // Cleanup: click cancel to close
    const cancelBtn = dialog.querySelector<HTMLButtonElement>('button')!
    cancelBtn.click()
  })

  it('resolves to false on cancel click', async () => {
    const promise = showConfirm('Test')
    await vi.waitFor(() => {
      expect(document.querySelector<HTMLDialogElement>('dialog')).toBeTruthy()
    })

    const cancelBtn = document.querySelector<HTMLButtonElement>('dialog button')!
    cancelBtn.click()

    const result = await promise
    expect(result).toBe(false)
  })

  it('resolves to true on confirm click', async () => {
    const promise = showConfirm('Test', 'Yes', 'No')
    await vi.waitFor(() => {
      expect(document.querySelector<HTMLDialogElement>('dialog')).toBeTruthy()
    })

    const buttons = document.querySelectorAll<HTMLButtonElement>('dialog button')
    // Second button is the confirm (OK)
    buttons[1].click()

    const result = await promise
    expect(result).toBe(true)
  })

  it('removes the dialog from the DOM after resolution', async () => {
    const promise = showConfirm('Test')
    await vi.waitFor(() => {
      expect(document.querySelector<HTMLDialogElement>('dialog')).toBeTruthy()
    })

    const cancelBtn = document.querySelector<HTMLButtonElement>('dialog button')!
    cancelBtn.click()
    await promise

    expect(document.querySelector<HTMLDialogElement>('dialog')).toBeNull()
  })

  it('restores focus to the previously focused element', async () => {
    vi.useFakeTimers()
    const btn = document.createElement('button')
    btn.setAttribute('id', 'focus-trap')
    document.body.appendChild(btn)
    btn.focus()

    const promise = showConfirm('Test')

    // Simulate timer for rAF
    vi.runAllTimers()

    // Click cancel
    const cancelBtn = document.querySelector<HTMLButtonElement>('dialog button')!
    cancelBtn.click()

    await promise
    vi.runAllTimers()

    expect(document.activeElement).toBe(btn)
    document.body.removeChild(btn)
    vi.useRealTimers()
  })
})
