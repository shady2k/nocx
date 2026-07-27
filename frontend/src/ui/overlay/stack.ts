/**
 * Overlay stack — a singleton registry of open overlays.
 *
 * Without a stack, nested overlays each think they own Escape and closing one
 * closes all. This module ensures Escape and outside-interaction close only
 * the topmost overlay.
 *
 * A native modal `<dialog>` is exempt from the portal-root and z-index
 * clauses (it renders in the browser top layer, above every stacking context).
 * It is NOT exempt from the drag-region, focus-return, and xterm-textarea
 * clauses. That distinction is explicit in the Dialog component.
 *
 * ADR-0014 estimate: 300–500 lines for the overlay core (all modules combined).
 */

export interface OverlayEntry {
  /** Unique id for this overlay instance. */
  readonly id: string
  /** Called to close this overlay. Returns false if the close was prevented. */
  close: () => boolean
  /**
   * Element that had focus before this overlay opened.
   * The overlay stack captures this on push so focus return works even when
   * the opening code is not a Solid effect (e.g. imperative dialog from
   * terminal-owned code).
   */
  readonly prevFocus: Element | null
}

let idCounter = 0
const stack: OverlayEntry[] = []

let escapeHandler: ((e: KeyboardEvent) => void) | null = null

function nextId(): string {
  idCounter++
  return `overlay-${idCounter}`
}

/**
 * Returns true if the target descends from a `--wails-draggable: drag` ancestor.
 * When true, the Wails native mousedown hook should handle the event (i.e. the
 * overlay should NOT intercept it). This is a Wails drag-region guard.
 *
 * In a packaged Wails webview, --wails-draggable is read by a native mousedown
 * hook that runs before any JS event handler. Playwright does not implement
 * this behaviour — see the verification section.
 */
export function isWailsDragTarget(target: Element): boolean {
  let el: Element | null = target
  while (el) {
    const val = getComputedStyle(el).getPropertyValue('--wails-draggable').trim()
    if (val === 'drag') return true
    el = el.parentElement
  }
  return false
}

/** Install a one-at-a-time Escape listener on document. */
function installEscapeHandler(): void {
  if (escapeHandler) return
  escapeHandler = (e: KeyboardEvent) => {
    if (e.key !== 'Escape') return
    const top = stack[stack.length - 1]
    if (!top) return
    e.preventDefault()
    top.close()
  }
  document.addEventListener('keydown', escapeHandler, true)
}

function uninstallEscapeHandler(): void {
  if (stack.length > 0 || !escapeHandler) return
  document.removeEventListener('keydown', escapeHandler, true)
  escapeHandler = null
}

/**
 * Push a new overlay entry onto the stack.
 * Registers the document-level Escape handler (one-at-a-time).
 * Returns the assigned entry (with id populated).
 */
export function pushOverlay(close: () => boolean, prevFocus?: Element | null): OverlayEntry {
  const entry: OverlayEntry = {
    id: nextId(),
    close,
    prevFocus: prevFocus ?? document.activeElement,
  }
  stack.push(entry)
  installEscapeHandler()
  return entry
}

/**
 * Remove the given overlay entry from the stack.
 * If it was the topmost entry, it is closed. Returns true if removed.
 */
export function popOverlay(entry: OverlayEntry): boolean {
  const idx = stack.indexOf(entry)
  if (idx === -1) return false
  stack.splice(idx, 1)
  uninstallEscapeHandler()
  return true
}

/** Returns the topmost overlay entry, or undefined if the stack is empty. */
export function topOverlay(): OverlayEntry | undefined {
  return stack[stack.length - 1]
}

/** Returns true if there are any open overlays. */
export function hasOpenOverlays(): boolean {
  return stack.length > 0
}

/** Returns the current depth of the overlay stack. */
export function stackDepth(): number {
  return stack.length
}

/**
 * Close the topmost overlay. Returns true if an overlay was closed.
 * Useful for Escape cascade handlers in custom overlays.
 */
export function closeTopmost(): boolean {
  const top = stack[stack.length - 1]
  if (!top) return false
  return top.close()
}

/**
 * Whether the given target is inside an element with `--wails-draggable: drag`.
 * Used as a mouse-down guard before an overlay's interaction handler activates.
 *
 * @example
 * ```ts
 * function onPointerDown(e: PointerEvent) {
 *   if (isWailsDragTarget(e.target as Element)) return
 *   // … handle outside-interaction …
 * }
 * ```
 */

/** Restore focus to the element that was active before the given overlay opened. */
export function restoreFocus(entry: OverlayEntry): void {
  const el = entry.prevFocus
  if (!el) return

  // HTMLDialogElement has special focus behaviour when closed — the browser
  // restores focus automatically. Do NOT double-focus in that case.
  if (el instanceof HTMLDialogElement || el.closest?.('dialog[open]')) {
    return
  }

  // xterm stores focus in a hidden textarea that is not a normal focusable
  // element. A standard .focus() call does not work on it in some webview
  // environments — see ADR-0014 "Consequences / Negative".
  if (el instanceof HTMLElement) {
    // requestAnimationFrame ensures the dialog's native focus-return has
    // settled before we try to redirect focus. The double-focus pattern
    // (native dialog close restores focus → our rAF redirects it) is the
    // recommended workaround for environments where the native return is
    // imprecise.
    requestAnimationFrame(() => {
      el.focus({ preventScroll: true })
    })
  }
}

/** Clear the entire stack (for testing / teardown). */
export function clearStack(): void {
  stack.length = 0
  uninstallEscapeHandler()
}
