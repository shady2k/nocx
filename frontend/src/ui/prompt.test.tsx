// @vitest-environment jsdom
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { Show, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Dialog } from './dialog'
import { Prompt } from './prompt'
import { ToastHost, clearToasts, showToast } from './toast'
import { stackDepth } from './overlay/stack'

afterEach(() => {
  clearToasts()
  cleanup()
})

describe('Prompt', () => {
  it('renders a labelled top sheet with its actions', () => {
    const { container } = render(() => (
      <Prompt
        open
        ariaLabel="Password"
        placement="top-sheet"
        onClose={() => undefined}
        actions={<button type="button">OK</button>}
      >
        <input />
      </Prompt>
    ))

    const prompt = container.querySelector('.ui-prompt')
    expect(prompt?.getAttribute('role')).toBe('dialog')
    expect(prompt?.getAttribute('aria-label')).toBe('Password')
    expect(prompt?.getAttribute('data-placement')).toBe('top-sheet')
    expect(prompt?.querySelector('.ui-prompt__actions')?.textContent).toBe('OK')
  })

  it('closes when the scrim is pressed', () => {
    const close = vi.fn()
    const [open, setOpen] = createSignal(true)
    const { container } = render(() => (
      <Prompt
        open={open()}
        ariaLabel="Password"
        onClose={() => {
          close()
          setOpen(false)
        }}
        actions={null}
      >
        Secret
      </Prompt>
    ))

    fireEvent.mouseDown(container.querySelector('.ui-prompt-overlay')!)
    expect(close).toHaveBeenCalledOnce()
    expect(container.querySelector('.ui-prompt')).toBeNull()
  })

  // ── Keyboard: Enter submits, Escape cancels ──────────────────────────
  // A Prompt is not a `<dialog>`: the native cancel and showModal Enter
  // behaviour do not come for free. Escape comes from the overlay stack's
  // document-level handler; Enter is supplied here, with the same guards
  // Dialog uses — only a single-line input, never a textarea or a button,
  // never while an IME is composing, and only when the caller declared one.

  it('submits on Enter in a single-line field when onSubmit is passed', () => {
    const submit = vi.fn()
    render(() => (
      <Prompt open ariaLabel="Password" onSubmit={submit} onClose={() => undefined} actions={null}>
        <input type="text" />
      </Prompt>
    ))

    fireEvent.keyDown(document.querySelector('.ui-prompt input')!, { key: 'Enter' })
    expect(submit).toHaveBeenCalledOnce()
  })

  it('does not submit on Enter when no onSubmit is passed', () => {
    const submit = vi.fn()
    render(() => (
      <Prompt open ariaLabel="Password" onClose={() => undefined} actions={null}>
        <input type="text" />
      </Prompt>
    ))

    fireEvent.keyDown(document.querySelector('.ui-prompt input')!, { key: 'Enter' })
    expect(submit).not.toHaveBeenCalled()
  })

  it('leaves Enter to a textarea and to a button', () => {
    const submit = vi.fn()
    render(() => (
      <Prompt open ariaLabel="Password" onSubmit={submit} onClose={() => undefined} actions={null}>
        <textarea />
        <button type="button">Go</button>
      </Prompt>
    ))

    fireEvent.keyDown(document.querySelector('.ui-prompt textarea')!, { key: 'Enter' })
    fireEvent.keyDown(document.querySelector('.ui-prompt button')!, { key: 'Enter' })
    expect(submit).not.toHaveBeenCalled()
  })

  it('cancels on Escape, supplied by the overlay stack', () => {
    const close = vi.fn()
    const [open, setOpen] = createSignal(true)
    render(() => (
      <Prompt
        open={open()}
        ariaLabel="Password"
        onClose={() => {
          close()
          setOpen(false)
        }}
        actions={null}
      >
        Secret
      </Prompt>
    ))

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(close).toHaveBeenCalledOnce()
    expect(document.querySelector('.ui-prompt')).toBeNull()
  })

  // ── Focus: to the field on open, back to the opener on close ────────
  // Dialog gets both from showModal(); a Prompt is a plain div and must do
  // them itself — the overlay stack records the pre-open focus for return.

  it('focuses the autofocus field when opened', () => {
    render(() => (
      <Prompt open ariaLabel="Password" onClose={() => undefined} actions={null}>
        <input type="text" />
      </Prompt>
    ))

    expect(document.activeElement).toBe(document.querySelector('.ui-prompt input'))
  })

  it('returns focus to the element that had it before opening', () => {
    vi.useFakeTimers()
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()

    const [open, setOpen] = createSignal(true)
    render(() => (
      <Prompt open={open()} ariaLabel="Password" onClose={() => setOpen(false)} actions={null}>
        <input />
      </Prompt>
    ))

    vi.runAllTimers()
    fireEvent.keyDown(document, { key: 'Escape' })
    vi.runAllTimers()

    expect(document.activeElement).toBe(opener)
    document.body.removeChild(opener)
    vi.useRealTimers()
  })

  // ── Toast visibility: the brief's question, answered in a test ───────
  // ToastHost portals into the topmost open overlay; a Prompt registers
  // itself as one (pushOverlay with its element), so a toast raised while
  // the prompt is open must render inside the prompt, not under it.

  it('hosts toasts raised while open inside the prompt overlay', async () => {
    render(() => (
      <>
        <Prompt open ariaLabel="Password" onClose={() => undefined} actions={null}>
          Secret
        </Prompt>
        <ToastHost />
      </>
    ))

    showToast({ level: 'success', message: 'Vault unlocked.' })

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')
      expect(toast).toBeTruthy()
      expect(toast!.closest('.ui-prompt-overlay')).not.toBeNull()
    })
  })

  // ── Layering: the reported bug, in a test ────────────────────────────
  // A Prompt is a plain div in the normal layer, while Dialog uses
  // showModal() and renders in the browser's top layer — which is above
  // every z-index in the normal layer by definition. A prompt opened while
  // a dialog is up therefore painted UNDER the dialog's backdrop: the user
  // saw the dialog, heard the prompt, and could not type. Being above a
  // top-layer element is not a number, it is a parent — so the prompt must
  // render as a child of the topmost open overlay, exactly like the
  // connection editor's own password prompt, which is a DOM child of the
  // dialog and appears correctly.

  it('renders inside the topmost open overlay, so a dialog never covers it', () => {
    const [open, setOpen] = createSignal(false)
    const { container } = render(() => (
      <>
        <Dialog open onClose={() => undefined} title="New Connection">
          Body
        </Dialog>
        <Prompt open={open()} ariaLabel="Password" onClose={() => setOpen(false)} actions={null}>
          Secret
        </Prompt>
      </>
    ))

    // The dialog registered itself while the prompt was closed. Opening the
    // prompt now must place it inside the dialog element.
    setOpen(true)

    const overlay = container.querySelector('.ui-prompt-overlay')
    expect(overlay).toBeTruthy()
    expect(overlay!.closest('dialog.nocx-dialog')).not.toBeNull()
  })

  it('renders in place when no overlay is open', () => {
    const [open, setOpen] = createSignal(false)
    const { container } = render(() => (
      <Prompt open={open()} ariaLabel="Password" onClose={() => setOpen(false)} actions={null}>
        Secret
      </Prompt>
    ))

    setOpen(true)
    expect(container.querySelector('.ui-prompt-overlay')).toBeTruthy()
    expect(container.querySelector('.ui-prompt-overlay')!.closest('dialog')).toBeNull()
  })

  // Moving the prompt inside the dialog puts its scrim on top of the dialog's
  // own, so one mousedown lands on both. Dismissing "which passphrase?" must
  // not take the connection editor that asked for it down with it — the same
  // exemption the toast host already has, for the same reason.
  it('dismissing a hosted prompt leaves the dialog beneath it open', () => {
    const [open, setOpen] = createSignal(false)
    const onDialogClose = vi.fn()
    const { container } = render(() => (
      <>
        <Dialog open onClose={onDialogClose} title="New Connection">
          Body
        </Dialog>
        <Prompt open={open()} ariaLabel="Password" onClose={() => setOpen(false)} actions={null}>
          Secret
        </Prompt>
      </>
    ))
    setOpen(true)

    const overlay = container.querySelector('.ui-prompt-overlay')!
    // Real coordinates: the dialog's light dismiss compares against the
    // panel's box and ignores a 0,0 click as keyboard-activated.
    fireEvent.mouseDown(overlay, { clientX: 5, clientY: 5 })

    expect(container.querySelector('.ui-prompt-overlay')).toBeNull()
    expect(onDialogClose).not.toHaveBeenCalled()
  })

  // The prompt's element is appended into the dialog, so it no longer sits
  // where Solid put it. Closing must still remove it — a detached overlay
  // would sit over the app forever, invisible to the stack that popped it.
  it('removes a hosted prompt from the dialog when it closes', () => {
    const [open, setOpen] = createSignal(false)
    render(() => (
      <>
        <Dialog open onClose={() => undefined} title="New Connection">
          Body
        </Dialog>
        <Prompt open={open()} ariaLabel="Password" onClose={() => setOpen(false)} actions={null}>
          Secret
        </Prompt>
      </>
    ))

    setOpen(true)
    expect(document.querySelector('.ui-prompt-overlay')).toBeTruthy()
    setOpen(false)
    expect(document.querySelector('.ui-prompt-overlay')).toBeNull()
  })

  // Closing the prompt must hand the keyboard back to the dialog it
  // interrupted. It landed on <body> instead: the overlay stack skipped its
  // focus return whenever the previous focus was inside an open dialog, on the
  // assumption that the browser would do it — which is true when a <dialog>
  // closes and false for a plain div.
  it('returns focus into the dialog it was raised over', () => {
    vi.useFakeTimers()
    const [open, setOpen] = createSignal(false)
    render(() => (
      <>
        <Dialog open onClose={() => undefined} title="New Connection">
          <input id="host-field" />
        </Dialog>
        <Prompt open={open()} ariaLabel="Password" onClose={() => setOpen(false)} actions={null}>
          <input />
        </Prompt>
      </>
    ))
    const field = document.querySelector('#host-field') as HTMLInputElement
    field.focus()
    expect(document.activeElement).toBe(field)

    setOpen(true)
    vi.runAllTimers()
    setOpen(false)
    vi.runAllTimers()

    expect(document.activeElement).toBe(field)
    vi.useRealTimers()
  })

  // The way the vault actually closes its prompts: the owner is unmounted
  // (`<Show when={unlockOpen()}>`), not merely told `open={false}`. Moving the
  // element into the dialog by hand meant Solid removed nothing on unmount and
  // the panel stayed on screen with the overlay entry already popped — the
  // first Escape then did nothing and the second took the dialog with it.
  it('leaves nothing behind when its owner unmounts while open', () => {
    const [mounted, setMounted] = createSignal(true)
    render(() => (
      <>
        <Dialog open onClose={() => undefined} title="New Connection">
          Body
        </Dialog>
        <Show when={mounted()}>
          <Prompt open ariaLabel="Password" onClose={() => undefined} actions={null}>
            Secret
          </Prompt>
        </Show>
      </>
    ))
    expect(document.querySelector('.ui-prompt-overlay')).toBeTruthy()
    expect(stackDepth()).toBe(2)

    setMounted(false)
    expect(document.querySelector('.ui-prompt-overlay')).toBeNull()
    expect(stackDepth()).toBe(1)
  })

  // Escape's other route to the same damage. The browser answers a close
  // request by firing `cancel` at the dialog — the dialog is still the target
  // even when the prompt on top of it is a child of it — so the dialog closed
  // underneath a prompt the user was only trying to dismiss. Dispatching
  // `cancel` is what Escape does; jsdom does not synthesise it from a keydown,
  // so the event is raised directly rather than tested through a key that
  // does nothing here.
  it('ignores the native cancel while a prompt is open above it', () => {
    const [open, setOpen] = createSignal(false)
    const onDialogClose = vi.fn()
    render(() => (
      <>
        <Dialog open onClose={onDialogClose} title="New Connection">
          Body
        </Dialog>
        <Prompt open={open()} ariaLabel="Password" onClose={() => setOpen(false)} actions={null}>
          Secret
        </Prompt>
      </>
    ))
    setOpen(true)

    const dialog = document.querySelector('dialog.nocx-dialog')!
    const cancel = new Event('cancel', { bubbles: false, cancelable: true })
    dialog.dispatchEvent(cancel)

    expect(onDialogClose).not.toHaveBeenCalled()
    expect(cancel.defaultPrevented).toBe(true)

    // …and once the prompt is gone the dialog owns Escape again.
    setOpen(false)
    dialog.dispatchEvent(new Event('cancel', { bubbles: false, cancelable: true }))
    expect(onDialogClose).toHaveBeenCalledOnce()
  })

  it('lays its actions out in one row by default', () => {
    const { container } = render(() => (
      <Prompt open ariaLabel="Password" onClose={() => undefined} actions={null}>
        Secret
      </Prompt>
    ))
    expect(container.querySelector('.ui-prompt__actions')!.getAttribute('data-layout')).toBe('row')
  })

  it('stacks its actions when the caller declares more answers than a line holds', () => {
    const { container } = render(() => (
      <Prompt
        open
        ariaLabel="Password"
        onClose={() => undefined}
        actionsLayout="stacked"
        actions={null}
      >
        Secret
      </Prompt>
    ))
    expect(container.querySelector('.ui-prompt__actions')!.getAttribute('data-layout')).toBe(
      'stacked',
    )
  })
})
