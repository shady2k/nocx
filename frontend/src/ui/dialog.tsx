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

import { createUniqueId, onCleanup, onMount, type Component, type JSX, Show } from 'solid-js'
import { render } from 'solid-js/web'
import { createModalHost } from './overlay/modal-host'
import { Button } from './button'
import { CloseIcon } from './icons'
import { IconButton } from './icon-button'

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
  /**
   * Panel width class. `md` (the default) is the confirm/edit width; `lg` is
   * the palette width, wide enough for a host and its label on one line.
   *
   * A prop rather than a class: the panel's width is the kit's to decide, and a
   * caller that reached past `max-width` with its own fixed width simply
   * overflowed the panel — which is exactly what a class would have let it keep
   * doing, silently.
   */
  size?: 'md' | 'lg' | 'full'
  /**
   * The dialog's affirmative action, triggered by Enter in a single-line field.
   *
   * Opt-in rather than automatic: Enter must not fire a destructive
   * confirmation, and a dialog whose body is a message has nothing to submit.
   * A caller that passes this is saying "this dialog has one obvious yes".
   *
   * Textareas and buttons are exempt — Enter belongs to them.
   */
  onSubmit?: () => void
  /**
   * Escape veto. Called when the user presses Escape (via the overlay stack's
   * handler or the native cancel). Return true to CONSUME the Escape and keep
   * the dialog open — for an overlay state that walks back a step instead of
   * closing (the palette's drill-in, nocx-4t37).
   */
  onEscape?: () => boolean
  /**
   * Whether the dialog offers a way out. Defaults to true; false suppresses
   * the universal close control and makes Escape, cancel, and light dismiss
   * inert for blocking overlays with no underlying surface to return to.
   */
  dismissible?: boolean
}

/**
 * Put the caret where the user is about to type.
 *
 * `showModal()` focuses the first focusable descendant, and "focusable" is
 * wider than "a control": a scrolling region is focusable too, so browsers can
 * let a keyboard user scroll it. The dialog body scrolls, so opening a form
 * landed the focus on the body itself — a focus ring drawn around the entire
 * dialog contents, and a first keystroke that went nowhere. Reported from a Mac
 * with full keyboard access on, where it happens every time.
 *
 * Order of preference, and each step exists for a reason:
 *   1. an explicit `autofocus` — the caller has said which control, and on a
 *      destructive confirmation that is deliberately the SAFE one;
 *   2. the first real field — what a form is for;
 *   3. the first contextual action — a dialog with nothing to fill in; the
 *      universal close control is reached later by keyboard navigation.
 * A scroll container matches none of them, so it is reachable by Tab and never
 * chosen for you.
 */
function focusInitial(d: HTMLDialogElement): void {
  const panel = d.querySelector('.nocx-dialog__panel')
  if (!panel) return
  const enabled = ':not([disabled]):not([tabindex="-1"])'
  const firstContextualButton = Array.from(
    panel.querySelectorAll<HTMLButtonElement>('button' + enabled),
  ).find((button) => !button.closest('.nocx-dialog__close'))
  const target =
    panel.querySelector<HTMLElement>('[autofocus]' + enabled) ??
    panel.querySelector<HTMLElement>(
      `input:not([type="hidden"])${enabled}, select${enabled}, textarea${enabled}`,
    ) ??
    firstContextualButton
  target?.focus()
}

export const Dialog: Component<DialogProps> = (props) => {
  const titleId = createUniqueId()
  let panel: HTMLDivElement | undefined

  /* ── Panel height animation ──────────────────────────────────────────
     The panel's height is `auto` and content-driven: it grows and shrinks
     when the body's content changes (a section switch in Tabs, a control
     revealing fields). Those changes are animated here — the footer moves
     visibly instead of teleporting out from under the pointer reaching for
     it, which is the stability the old fixed-size approach bought.

     Transitioning to and from `height: auto` itself needs
     interpolate-size / calc-size(), which is above both declared browser
     floors (WebKitGTK 2.40, Safari 16.2 — ADR-0013 §3), so this is
     measure-and-transition: pin the settled height, set the target height,
     transition, release back to `auto` on transitionend. The transition
     property lives in dialog.css with the reduced-motion override, next to
     the other components' (toast.css, prompt.css). */
  let ro: ResizeObserver | null = null
  let animating = false
  let lastHeight: number | null = null

  /** True under `prefers-reduced-motion: reduce` — no transition at all. */
  const reducedMotion = () =>
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches

  const releasePanelHeight = () => {
    if (!panel) return
    animating = false
    panel.style.height = ''
    panel.style.transition = ''
    panel.removeAttribute('data-animating')
  }

  /**
   * Measure-and-transition the panel to its new natural height.
   *
   * The ResizeObserver fires after layout with the panel already at the new
   * natural height (at rest the height is `auto`), so the current
   * getBoundingClientRect IS the target. `lastHeight` is the settled height
   * from before the change: we pin to it, force a reflow so the transition
   * starts from it, then set the target. The target was measured with the
   * panel's `max-height` applied and the pin is a settled height, so neither
   * can exceed the max-height mid-animation — a short viewport scrolls
   * rather than overflow.
   *
   * The size changes this animation itself causes (the pin, the transition)
   * re-fire the observer every frame; `animating` suppresses those. A content
   * change mid-transition is caught at transitionend: releasing to `auto`
   * changes the size again and the observer re-runs this from the new
   * settled height.
   */
  const syncPanelHeight = () => {
    const p = panel
    const d = host.element()
    if (!p || !d || !d.open) return
    if (animating) return
    if (reducedMotion()) return
    const to = p.getBoundingClientRect().height
    const from = lastHeight ?? to
    if (Math.abs(from - to) < 0.5) {
      lastHeight = to
      return
    }
    animating = true
    // Marks the window in which the panel is pinned to the OLD height while its
    // content is already the NEW size. The body is a scroll container, so for
    // exactly that window it overflows and flashes a scrollbar — the most
    // visible artefact of the whole transition, and it reads as a glitch rather
    // than as movement. The CSS hides the body's overflow while this is set;
    // releasePanelHeight removes it, so a body that genuinely does not fit gets
    // its scrollbar back the moment the panel settles.
    p.setAttribute('data-animating', 'true')
    p.style.transition = 'none'
    p.style.height = `${from}px`
    void p.offsetHeight // commit the pin so the transition starts from it
    p.style.transition = ''
    p.style.height = `${to}px`
  }

  /** The transition ended (or was cancelled): record where it settled and
      release to `auto`, so the CSS max-height, not a stale inline number,
      governs from here on. */
  const settlePanelHeight = () => {
    if (!panel) return
    lastHeight = panel.getBoundingClientRect().height
    releasePanelHeight()
  }

  const onPanelTransitionEnd = (e: TransitionEvent) => {
    if (e.target !== panel || e.propertyName !== 'height') return
    settlePanelHeight()
  }

  const onPanelTransitionCancel = (e: TransitionEvent) => {
    if (e.target !== panel || e.propertyName !== 'height') return
    settlePanelHeight()
  }

  /**
   * The top layer, the overlay stack, Escape and light dismiss — the mechanism
   * a modal surface needs and Dialog does not own. What Dialog adds to it is
   * everything below: the card, its measured height animation, and the caret
   * policy for the field a user is about to type in.
   */
  const host = createModalHost({
    open: () => props.open,
    dismissible: () => props.dismissible !== false,
    onClose: () => props.onClose(),
    onEscape: () => props.onEscape?.() === true,
    // Fresh open: no animation from a previous session's sizes — the panel
    // appears at its natural height and the first observation settles it.
    // Closing mid-animation cancels the transition without a transitionend, so
    // the pin is dropped there too and the panel cannot reopen at a stale
    // inline height.
    beforeOpen: () => {
      releasePanelHeight()
      lastHeight = null
    },
    beforeClose: () => {
      releasePanelHeight()
      lastHeight = null
    },
    afterOpen: (d) => focusInitial(d),
    lightDismissRegion: (d) => d.querySelector('.nocx-dialog__panel'),
  })

  // Watch the panel's own box: any content-driven size change (a section
  // switch, a revealed field) is a change to animate. jsdom has no layout, so
  // tests drive this observer by hand; in a browser it fires once on observe
  // with the initial size, which syncPanelHeight settles with no animation.
  onMount(() => {
    const p = panel
    if (!p) return
    ro = new ResizeObserver(() => syncPanelHeight())
    ro.observe(p)
  })

  // The host disposes the overlay entry, the focus return and the element
  // itself; what is left here is the panel's own measurement state.
  onCleanup(() => {
    ro?.disconnect()
    ro = null
    releasePanelHeight()
  })

  /**
   * Enter in a single-line field means "the obvious yes", which is what it
   * means in every other form on every other platform. Without it a dialog
   * whose whole body is one text box makes the user reach for the mouse to
   * confirm what they have just finished typing.
   *
   * Guarded three ways: only when the caller declared an action, only from a
   * real input (a textarea owns Enter, a button already has its own), and not
   * mid-composition — an IME uses Enter to accept a candidate, and submitting
   * there would eat the word being typed.
   */
  const onKeyDown = (e: KeyboardEvent) => {
    if (!props.onSubmit) return
    if (e.key !== 'Enter' || e.shiftKey || e.isComposing) return
    const target = e.target as HTMLElement | null
    if (!target || target.tagName !== 'INPUT') return
    if ((target as HTMLInputElement).type === 'button') return
    e.preventDefault()
    props.onSubmit()
  }

  return (
    <dialog
      ref={host.ref}
      class="nocx-dialog"
      data-dismissible={props.dismissible === false ? 'false' : undefined}
      aria-labelledby={props.title ? titleId : undefined}
      onCancel={host.onCancel}
      onMouseDown={host.onMouseDown}
      onKeyDown={onKeyDown}
    >
      <div
        class="nocx-dialog__panel"
        data-size={props.size ?? 'md'}
        ref={panel}
        onTransitionEnd={onPanelTransitionEnd}
        onTransitionCancel={onPanelTransitionCancel}
      >
        <div class="nocx-dialog__header">
          <Show when={props.title}>
            <h2 id={titleId} class="nocx-dialog__title">
              {props.title}
            </h2>
          </Show>
          {/* This is the universal dismiss affordance. A footer Cancel remains
              a caller's explicit, contextual action; Dialog does not infer,
              remove, or replace that action. */}
          <Show when={props.dismissible !== false}>
            <span class="nocx-dialog__close">
              <IconButton
                ariaLabel="Close dialog"
                title="Close"
                size="sm"
                onClick={() => props.onClose()}
              >
                <CloseIcon />
              </IconButton>
            </span>
          </Show>
        </div>
        {/* The body is a slot with rhythm of its own, not a place children are
            dropped. They used to be panel children directly, and the panel is a
            gapless flex column — so a dialog whose body was several Fields had
            its label sitting on the control above it, and every caller that
            looked right did so by remembering to wrap its content in a Section
            or a Stack. "Remember to" is not a contract. It also owns the
            scroll: content taller than the panel used to be clipped by the
            panel's `overflow: hidden` and simply unreachable. */}
        <div class="nocx-dialog__body">{props.children}</div>
        <Show when={props.footer}>
          <div class="nocx-dialog__actions">{props.footer}</div>
        </Show>
      </div>
    </dialog>
  )
}

/** The confirm body, so the imperative helper below owns no markup of its own. */
const ConfirmDialog: Component<{
  message: string
  okLabel: string
  cancelLabel: string
  onResolve: (result: boolean) => void
}> = (props) => (
  <Dialog
    open
    onClose={() => props.onResolve(false)}
    footer={
      <>
        <Button variant="default" onClick={() => props.onResolve(false)}>
          {props.cancelLabel}
        </Button>
        <Button variant="primary" onClick={() => props.onResolve(true)}>
          {props.okLabel}
        </Button>
      </>
    }
  >
    <p class="nocx-dialog__message">{props.message}</p>
  </Dialog>
)

/**
 * Imperative confirm dialog — returns a promise that resolves to true (OK) or
 * false (Cancel).
 *
 * Built on `Dialog`, like every other modal in the app. It used to assemble its
 * own `<dialog>` out of `document.createElement` calls, and the result was a
 * third look: the OK button picked up a kit class, the Cancel button was left
 * with nothing that matched a rule, and neither the panel nor the type came from
 * the same place as the rest. One base, the way `Page` is the one base for
 * surfaces.
 *
 * The vanilla-DOM version carried a comment justifying itself with ADR-0012 §1,
 * "Solid must not render into the terminal's subtree". That rule is about the
 * terminal's DOM. This mounts its own root on `document.body`, which is not the
 * terminal's subtree — the native `<dialog>` is in the browser top layer no
 * matter where its node sits — so the constraint never applied here.
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
    const host = document.createElement('div')
    document.body.appendChild(host)

    let dispose: (() => void) | null = null
    let settled = false

    const finish = (result: boolean) => {
      // Escape fires the cancel path and the disposer can run again on unmount;
      // the promise must resolve exactly once.
      if (settled) return
      settled = true
      // Deferred so Dialog's own cleanup — popOverlay and focus restore — runs
      // against a live root before it is torn down.
      queueMicrotask(() => {
        dispose?.()
        host.remove()
      })
      resolve(result)
    }

    dispose = render(
      () => (
        <ConfirmDialog
          message={message}
          okLabel={okLabel}
          cancelLabel={cancelLabel}
          onResolve={finish}
        />
      ),
      host,
    )
  })
}
