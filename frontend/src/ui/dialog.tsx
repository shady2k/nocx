/**
 * Dialog — built on native `<dialog>` + `showModal()`.
 *
 * A native modal `<dialog>` renders in the browser top layer, above every
 * stacking context. It is exempt from the portal-root and z-index clauses.
 * It is NOT exempt from the drag-region, focus-return, and xterm-textarea
 * clauses. That distinction is explicit below.
 *
 * Available at both declared floors (WebKitGTK 2.40, Safari 16.2), which is
 * why ADR-0014 chose it over a library. See ADR-0014 for the full rationale.
 *
 * ## What the platform gives us for free
 * - Top-layer rendering
 * - Background inertness (cannot interact with the rest of the page)
 * - Escape / cancel
 * - Native focus treatment (first focusable element receives focus)
 *
 * ## What we write ourselves
 * - Initial-focus policy (autofocus override)
 * - Nesting policy (overlay stack — only topmost is interactive)
 * - `::backdrop` theming via token variables (in overlay.css)
 * - Wails drag-region guard (--wails-draggable: no-drag on the dialog)
 * - Focus return to invoker on close (incl. xterm hidden textarea)
 *
 * @example
 * ```tsx
 * <Dialog open={show()} onClose={() => setShow(false)} title="Confirm">
 *   <p>Are you sure?</p>
 * </Dialog>
 * ```
 *
 * @example Confirm convenience
 * ```tsx
 * const ok = await showConfirm('Are you sure?')
 * ```
 */

import { createEffect, createSignal, onCleanup, type Component, type JSX, Show } from 'solid-js'
import { pushOverlay, popOverlay, restoreFocus } from './overlay/stack'

export interface DialogProps {
  /** Whether the dialog is open. */
  open: boolean
  /** Called when the dialog should close (Escape, cancel event, close button). */
  onClose: () => void
  /** Dialog title (optional). Rendered in `.nocx-dialog__title`. */
  title?: string
  /** Dialog body content. */
  children: JSX.Element
  /** Optional footer / action area. Rendered after children. */
  footer?: JSX.Element
}

export const Dialog: Component<DialogProps> = (props) => {
  let ref: HTMLDialogElement | undefined
  const [entry, setEntry] = createSignal<ReturnType<typeof pushOverlay> | null>(null)

  createEffect(() => {
    const d = ref
    if (!d) return

    if (props.open && !d.open) {
      d.showModal()
      // The callback is stored and invoked later, when Escape or an
      // outside interaction reaches the top of the overlay stack — i.e. as an
      // event response, which is where solid/reactivity permits a prop read.
      // The rule cannot see through the indirection.
      // eslint-disable-next-line solid/reactivity
      const e = pushOverlay(() => {
        props.onClose()
        return true
      })
      setEntry(e)
    } else if (!props.open && d.open) {
      const e = entry()
      if (e) popOverlay(e)
      d.close()
      if (e) restoreFocus(e)
    }
  })

  // Handle native cancel event (Escape key).
  const onCancel = () => {
    props.onClose()
  }

  onCleanup(() => {
    const e = entry()
    if (e) {
      popOverlay(e)
      if (e.prevFocus) restoreFocus(e)
    }
    if (ref?.open) ref.close()
  })

  return (
    <dialog ref={ref} class="nocx-dialog" onCancel={onCancel}>
      <div class="nocx-dialog__panel">
        <Show when={props.title}>
          <h2 class="nocx-dialog__title">{props.title}</h2>
        </Show>
        {props.children}
        <Show when={props.footer}>
          <div class="nocx-dialog__actions">{props.footer}</div>
        </Show>
      </div>
    </dialog>
  )
}

/**
 * Imperative confirm dialog — mounts a native `<dialog>` on the body,
 * returns a promise that resolves to true (OK) or false (Cancel).
 *
 * This is the **terminal-owned code path**. Solid must not render into the
 * terminal's subtree (ADR-0012 §1), so this helper uses only vanilla DOM
 * APIs — no Solid components, no JSX. The dialog mounts directly on
 * `document.body`, not inside a portal root, because the native `<dialog>` is
 * in the browser top layer regardless of where its DOM node sits in the tree.
 *
 * The dialog carries `--wails-draggable: no-drag` so platform drag regions
 * cannot interfere with it (ADR-0014 "Wails drag-region guard").
 *
 * @param message — The text to show (supports newlines, shown as pre-wrap).
 * @param okLabel — Label for the confirm button (default "OK").
 * @param cancelLabel — Label for the cancel button (default "Cancel").
 * @returns Promise<boolean> — true if the user confirmed, false if cancelled.
 */
export function showConfirm(
  message: string,
  okLabel = 'OK',
  cancelLabel = 'Cancel',
): Promise<boolean> {
  return new Promise<boolean>((resolve) => {
    const dialog = document.createElement('dialog')
    dialog.className = 'nocx-dialog'
    dialog.style.setProperty('--wails-draggable', 'no-drag')

    const panel = document.createElement('div')
    panel.className = 'nocx-dialog__panel'

    const msg = document.createElement('p')
    msg.className = 'nocx-dialog__message'
    msg.textContent = message
    panel.appendChild(msg)

    const actions = document.createElement('div')
    actions.className = 'nocx-dialog__actions'

    const cancelBtn = document.createElement('button')
    cancelBtn.textContent = cancelLabel
    cancelBtn.className = 'kit-scope'

    const okBtn = document.createElement('button')
    okBtn.textContent = okLabel
    okBtn.className = 'ui-btn-primary kit-scope'

    actions.appendChild(cancelBtn)
    actions.appendChild(okBtn)
    panel.appendChild(actions)
    dialog.appendChild(panel)
    document.body.appendChild(dialog)

    // Focus return
    const prevFocus = document.activeElement

    function cleanup(result: boolean) {
      dialog.close()
      if (document.body.contains(dialog)) {
        document.body.removeChild(dialog)
      }
      // Restore focus. Use requestAnimationFrame so the browser's native
      // focus-return on dialog close has settled first.
      if (prevFocus instanceof HTMLElement) {
        requestAnimationFrame(() => {
          prevFocus.focus({ preventScroll: true })
        })
      }
      resolve(result)
    }

    dialog.addEventListener('cancel', () => {
      cleanup(false)
    })

    cancelBtn.addEventListener('click', () => {
      cleanup(false)
    })

    okBtn.addEventListener('click', () => {
      cleanup(true)
    })

    dialog.addEventListener('keydown', (e) => {
      // Prevent native Escape from closing without our cleanup.
      if (e.key === 'Escape') {
        e.preventDefault()
        cleanup(false)
      }
    })

    dialog.showModal()
    okBtn.focus()
  })
}
