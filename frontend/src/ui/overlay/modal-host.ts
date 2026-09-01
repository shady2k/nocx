/**
 * Modal host — a native `<dialog>` registered with the overlay stack.
 *
 * This is the mechanism, with none of the vocabulary of any one surface built
 * on it: the `<dialog>` lifecycle, `showModal()`/`close()`, the push and pop
 * that make Escape and focus return belong to the topmost overlay, the Wails
 * drag-region guard, the element the toast host renders into, and light
 * dismiss.
 *
 * It exists because two surfaces need the browser top layer and they are not
 * variants of each other. `Dialog` is a card — a title, a footer, a close
 * control, a measured height animation, a caret policy for the field a user is
 * about to type in. `ConnectionOverlay` is a full-bleed ground with an app mark
 * on it, where every one of those is meaningless. Making one a mode of the
 * other would give it a mode in which none of its own props apply; giving each
 * its own `showModal()` call site would be two implementations of one concept,
 * and they would go out of step at exactly the place nobody looks — focus
 * return, or which overlay owns Escape.
 *
 * The top layer is why a plain positioned element will not do: a modal
 * `<dialog>` renders above every stacking context, so an overlay in the normal
 * layer is painted UNDER any dialog that happens to be open, at any z-index.
 * See `stack.ts` — "on top is not a number, it is a parent" — and ADR-0014.
 */
import { createEffect, createSignal, onCleanup } from 'solid-js'

import { popOverlay, pushOverlay, restoreFocus, topOverlay } from './stack'

export interface ModalHostOptions {
  /** Whether the dialog should be open. */
  open: () => boolean
  /**
   * Whether the surface offers a way out. Defaults to true. When false,
   * Escape, native cancel and light dismiss are all inert — a blocking
   * overlay with no underlying surface to return to.
   */
  dismissible?: () => boolean
  /** Called when the surface should close (Escape, cancel, light dismiss). */
  onClose: () => void
  /**
   * Escape veto. Return true to CONSUME the Escape and stay open — for an
   * overlay state that walks back a step instead of closing.
   */
  onEscape?: () => boolean
  /** Runs before `showModal()`, for a surface to reset its own transient state. */
  beforeOpen?: () => void
  /** Runs after `showModal()`, for a surface's own initial-focus policy. */
  afterOpen?: (dialog: HTMLDialogElement) => void
  /** Runs before `close()`, for a surface to release its own transient state. */
  beforeClose?: () => void
  /**
   * The region a pointer must land OUTSIDE of for light dismiss. Returning
   * null disables light dismiss for this surface — which is what a full-bleed
   * ground wants, since it has no outside.
   */
  lightDismissRegion?: (dialog: HTMLDialogElement) => Element | null
}

export interface ModalHost {
  /** Pass as the `<dialog>`'s `ref`. */
  ref: (element: HTMLDialogElement) => void
  /** Pass as the `<dialog>`'s `onCancel`. */
  onCancel: (event: Event) => void
  /** Pass as the `<dialog>`'s `onMouseDown`. */
  onMouseDown: (event: MouseEvent) => void
  /** The hosted element, once rendered. */
  element: () => HTMLDialogElement | undefined
}

/**
 * A click that landed on something which is INSIDE this dialog's subtree but
 * deliberately outside the surface's own region.
 *
 * Each of these re-homes itself into the topmost overlay, because the top
 * layer is escaped by parentage rather than by a z-index: a toast raised over
 * a dialog, a Prompt opened from one, an anchored picker belonging to a field
 * in one. Dismissing any of them must not dismiss the surface that raised it —
 * a user cancelling "which passphrase?" was losing the connection they had
 * just finished filling in.
 */
const REHOMED = '.ui-toast-host, .ui-prompt-overlay, .ui-floating-panel'

export function createModalHost(options: ModalHostOptions): ModalHost {
  let element: HTMLDialogElement | undefined
  const [entry, setEntry] = createSignal<ReturnType<typeof pushOverlay> | null>(null)
  const dismissible = () => options.dismissible?.() !== false

  createEffect(() => {
    const d = element
    if (!d) return

    if (options.open() && !d.open) {
      options.beforeOpen?.()
      d.showModal()
      // The callback is stored and invoked later, when Escape or an outside
      // interaction reaches the top of the stack — an event response, which is
      // where reactivity permits a prop read. The element travels with the
      // entry so the toast host can render itself inside the topmost overlay.
      const close = () => {
        if (!dismissible()) return false
        if (options.onEscape?.() === true) return false
        options.onClose()
        return true
      }
      setEntry(pushOverlay(close, undefined, d))
      options.afterOpen?.(d)
    } else if (!options.open() && d.open) {
      options.beforeClose?.()
      const e = entry()
      if (e) popOverlay(e)
      d.close()
      if (e) restoreFocus(e)
    }
  })

  /**
   * The native cancel event — Escape.
   *
   * Only this dialog's OWN cancel: `cancel` bubbles, and `input[type=file]`
   * fires one when the user dismisses the OS file picker, so cancelling a file
   * chooser inside a dialog was closing the dialog and discarding the form
   * behind it.
   */
  const onCancel = (event: Event) => {
    const d = element
    if (event.target !== d) return
    // Escape belongs to the topmost overlay, which is what the stack is for.
    // A Prompt opened over this one renders INSIDE it, so this is still the
    // element the browser fires `cancel` at; stand down until topmost again.
    // `cancel` is cancelable, and preventing it is what keeps this open.
    const own = entry()
    if (own && topOverlay() !== own) {
      event.preventDefault()
      return
    }
    if (!dismissible()) {
      event.preventDefault()
      return
    }
    if (options.onEscape?.() === true) {
      event.preventDefault()
      return
    }
    options.onClose()
  }

  /**
   * Light dismiss — a pointer press outside the surface's region closes it.
   *
   * The listener is on the `<dialog>` rather than on the backdrop, because
   * `::backdrop` is a pseudo-element and cannot take one. A native modal
   * `<dialog>` fills the viewport, so a press that lands on the dialog element
   * itself landed outside a centred panel. Comparing against the region's box
   * rather than `event.target === dialog` is what makes it survive a press on
   * padding or on a child that stops bubbling.
   */
  const onMouseDown = (event: MouseEvent) => {
    const d = element
    if (!d || !dismissible()) return
    if ((event.target as Element | null)?.closest(REHOMED)) return
    const region = options.lightDismissRegion?.(d)
    if (!region) return
    const r = region.getBoundingClientRect()
    const inside =
      event.clientX >= r.left &&
      event.clientX <= r.right &&
      event.clientY >= r.top &&
      event.clientY <= r.bottom
    // A press with no coordinates is a keyboard activation (Enter on a button
    // reports 0,0); those are never a dismiss.
    if (event.clientX === 0 && event.clientY === 0) return
    if (!inside) options.onClose()
  }

  onCleanup(() => {
    const e = entry()
    if (e) {
      popOverlay(e)
      if (e.prevFocus) restoreFocus(e)
    }
    if (element?.open) element.close()
  })

  return {
    ref: (el: HTMLDialogElement) => {
      element = el
    },
    onCancel,
    onMouseDown,
    element: () => element,
  }
}
