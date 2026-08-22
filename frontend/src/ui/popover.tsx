/**
 * Popover — the kit's panel primitive: a small, non-modal surface of
 * arbitrary content anchored at a point, dismissed by clicking away or by
 * Escape. The README's "Popover/Menu/Combobox" row had the menu half built
 * (`context-menu.tsx`) and this half not; a list of operations with
 * progress and a cancel is not a list of actions, so ContextMenu could not
 * carry it and this is the missing half rather than a second menu.
 *
 * What it owns, all in popover.css:
 * - the shell: fixed at a clamped (x, y), the same raised-surface
 *   vocabulary as the menu, so a panel and a menu floating over the same
 *   surface read as one app;
 * - dismissal: outside pointerdown, Escape, or the caller closing it;
 * - the keyboard's whereabouts: the panel takes focus on open and hands it
 *   back to whoever had it when it closes.
 *
 * The mechanics it shares with ContextMenu — clamping a point onto the
 * screen, and the two dismissal listeners — are `overlay/anchored.ts`'s.
 * Two copies of them would agree until one grew a different edge margin.
 *
 * WHAT IS INSIDE IS THE CALLER'S, and this component never looks at it: no
 * roving, no item vocabulary, no assumption that the content is a list. A
 * caller that wants a menu wants ContextMenu.
 *
 * Non-modal and transient on purpose: there is no focus trap and no
 * backdrop. A panel that blocks the app it floated over is a dialog wearing
 * a popover's clothes, and the kit has a Dialog for that.
 */
import { Show, createEffect, onCleanup, type JSX } from 'solid-js'
import { Portal } from 'solid-js/web'
import { attachTransientDismiss, clampToViewport } from './overlay/anchored'

export interface PopoverProps {
  open: boolean
  /** The panel's preferred TOP-LEFT corner, in viewport coordinates. It is
   *  clamped onto the screen, which is also how a panel opened from the
   *  bottom of the window opens upward — see overlay/anchored.ts. */
  x: number
  y: number
  /** Required. A panel with no accessible name is a region a screen reader
   *  can enter and cannot describe. */
  ariaLabel: string
  /** Called when the popover dismisses itself. The caller owns the open
   *  state, exactly as it does for ContextMenu. */
  onClose: () => void
  children: JSX.Element
  'data-testid'?: string
}

export function Popover(props: PopoverProps) {
  let element: HTMLDivElement | undefined
  /** Whoever held the keyboard when the panel took it. */
  let opener: HTMLElement | null = null

  /**
   * Hand the keyboard back to the opener.
   *
   * The panel takes focus on open — it must, or Escape would go to whatever
   * is underneath and the panel's own controls would be unreachable by
   * keyboard — so it owes it back. Only while the panel still HOLDS it: an
   * outside pointerdown is on its way to a new owner, and pulling focus
   * back over the top of that would make a click somewhere else land
   * somewhere else again. (`context-menu.tsx` records what that cost.)
   */
  function releaseFocus(): void {
    const el = opener
    opener = null
    if (el === null || !el.isConnected) return
    const active = document.activeElement
    if (element && !element.contains(active)) return
    el.focus({ preventScroll: true })
  }

  // Position and focus on open. The anchor does not move while the panel is
  // up, so both are measured once per open.
  createEffect(() => {
    if (!props.open) return
    const el = element
    if (!el) return
    const rect = el.getBoundingClientRect()
    const at = clampToViewport(
      { width: rect.width, height: rect.height },
      { x: props.x, y: props.y },
      { width: window.innerWidth, height: window.innerHeight },
    )
    el.style.left = `${at.x}px`
    el.style.top = `${at.y}px`
    opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    el.focus({ preventScroll: true })
  })

  createEffect(() => {
    if (!props.open) return
    onCleanup(
      attachTransientDismiss({
        element: () => element,
        onOutside: () => props.onClose(),
        onEscape: (e) => {
          // Swallowed, like the menu's: the panel floats over surfaces that
          // act on Escape themselves, and the keystroke that closed it must
          // not also reach them.
          e.stopPropagation()
          releaseFocus()
          props.onClose()
        },
      }),
    )
  })

  return (
    <Show when={props.open}>
      <Portal>
        {/* tabIndex -1 and not 0: the panel is focusABLE so it can hold the
            keyboard while it is up, and not a tab stop of the page behind
            it, which it is not part of. */}
        <div
          class="ui-popover"
          role="dialog"
          aria-label={props.ariaLabel}
          tabIndex={-1}
          data-testid={props['data-testid']}
          ref={(el) => {
            element = el
          }}
        >
          {props.children}
        </div>
      </Portal>
    </Show>
  )
}
