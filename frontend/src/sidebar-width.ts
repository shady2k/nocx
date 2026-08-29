/**
 * sidebar-width — the shell's width policy for the sidebar panel (nocx-qmcu).
 *
 * One clamped WHOLE number of CSS pixels, applied to #sidebar as the
 * `--sidebar-width` variable and persisted in the UI-state document on the Go
 * side (ADR-0048). This module owns the frontend half: the bounds, the
 * rounding, the application, and the controller that is the single owner of
 * the value between the drag and the persistence seam.
 *
 * ## It used to be a setting, and that was the bug
 *
 * The width was registered as `sidebar.width` in the settings registry, which
 * put a "Sidebar width" row on Settings → Interface reading `206.3828125 px`
 * and badged the section "Modified" as soon as anybody dragged the edge. Two
 * symptoms, one cause: a setting is something a user DELIBERATELY CHOOSES, and
 * a width produced by dragging a panel edge is not a decision — it is what the
 * app must remember without being asked. Wrong owner; both symptoms follow.
 * The fractional pixels are gone here, at the clamp, because that is the one
 * place the number is decided (nocx-mqie.3).
 *
 * ## The bounds, measured rather than guessed
 *
 * MIN 200px. The Git panel's dense row (`ui-file-status-row` on
 * `CollectionRow data-density="dense"`) spends its width on, in order: the
 * status letter (1.25em ≈ 15px), the type glyph (1em ≈ 12px), the
 * `+N −N` counts (≈ 58px for a 3-digit count, plus its 8px lead), the
 * stage control (an IconButton, ≈ 28px), and then — only then — the file
 * name. Around those fixed parts sit the row's own gaps and padding
 * (4px gap + 4px dense padding each side), the list's 8px gap, and the
 * panel body's 8px side padding; the sum is ≈ 165px before the name gets
 * a single pixel. 200px leaves the name ≈ 35px — five or six characters
 * of the part that answers "which file is this" — before the ellipsis.
 * Below that, the row is a column of ellipses and the panel has stopped
 * being useful at any width.
 *
 * MAX 640px. The reference product the owner compares against runs its
 * panel at roughly twice our default, so the ceiling must sit above that;
 * 640px is the width at which the sidebar plus the 48px activity bar
 * (688px) would own more than half of a 1280px window. The bound's job is
 * to stop the panel swallowing the panes, and 640 keeps that true on any
 * window the app runs on.
 *
 * The default is 240px — today's width, unchanged for the user who never
 * drags. MIN/MAX/DEFAULT are mirrored in internal/uistate's ClampSidebarWidth,
 * which is what actually validates the persisted value; move the numbers in
 * both places.
 */

export const SIDEBAR_WIDTH_MIN = 200
export const SIDEBAR_WIDTH_MAX = 640
export const SIDEBAR_WIDTH_STEP = 8
export const SIDEBAR_WIDTH_DEFAULT = 240

/** Clamp a width to the declared bounds and round it to a whole pixel.
 *  Non-finite values (a corrupted persisted value) fall back to the default
 *  rather than corrupting layout.
 *
 *  The rounding is here rather than at the persistence seam because this is
 *  the single place the width is decided: a pointer position is a fraction,
 *  and every consumer — the CSS variable, the handle's aria-valuenow, the
 *  document — should see the same whole number. Rounding only on the way out
 *  would leave the screen and the file disagreeing by half a pixel, and put a
 *  value back on screen that nothing else in the app would ever produce. */
export function clampSidebarWidth(width: number): number {
  if (!Number.isFinite(width)) return SIDEBAR_WIDTH_DEFAULT
  return Math.round(Math.min(SIDEBAR_WIDTH_MAX, Math.max(SIDEBAR_WIDTH_MIN, width)))
}

/** Apply a width to the panel. The stylesheet reads `--sidebar-width` with
 *  a 240px fallback (frontend/src/style.css `#sidebar`), so a panel this
 *  was never called on still renders the default. */
export function applySidebarWidth(panel: HTMLElement, width: number): void {
  panel.style.setProperty('--sidebar-width', `${clampSidebarWidth(width)}px`)
}

/** The persistence seam the composition root hands the controller: write
 *  the width into the UI-state document, fire-and-forget, and surface a
 *  failed write as a warning — a soft degrade must be visible in the
 *  product, not only in a log (AGENTS.md). The failure never propagates
 *  into the drag: the width is already applied and stays on screen, and the
 *  next commit retries. `save` is passed as a function because the client
 *  method is `this`-bound; a synchronous throw from it is caught here just
 *  like a rejected promise. */
export function persistSidebarWidth(
  save: (width: number) => Promise<unknown>,
  onFailure: (message: string) => void,
  width: number,
): void {
  const fail = (): void => {
    onFailure('Could not save the sidebar width — it will not survive a restart')
  }
  try {
    void Promise.resolve(save(width)).catch(fail)
  } catch {
    fail()
  }
}

export interface SidebarWidthController {
  /** The current applied width, px, clamped. */
  readonly width: number
  /** True while a pointer drag is in flight — the live pointer position is
   *  the truth until the release commits it, so nothing may clobber it with
   *  a value fetched from elsewhere. */
  isDragging(): boolean
  setDragging(dragging: boolean): void
  /** Apply a new width: clamp, paint, notify. `persist` also pushes the
   *  settled value through the persistence seam. */
  apply(width: number, opts?: { persist?: boolean }): void
  /** Listen for applied widths (the handle's aria-valuenow). Returns an
   *  unsubscribe. */
  subscribe(listener: (width: number) => void): () => void
}

/** The single owner of the sidebar width. Created by the composition root
 *  with the UI-state document's value and the persistence seam; passed to
 *  mountSidebar, which binds the kit ResizeHandle to it. */
export function createSidebarWidthController(
  panel: HTMLElement,
  initialWidth: number,
  persist?: (width: number) => void,
): SidebarWidthController {
  let current = clampSidebarWidth(initialWidth)
  let dragging = false
  const listeners = new Set<(width: number) => void>()

  applySidebarWidth(panel, current)

  return {
    get width() {
      return current
    },
    isDragging() {
      return dragging
    },
    setDragging(next: boolean) {
      dragging = next
    },
    apply(width: number, opts?: { persist?: boolean }) {
      const next = clampSidebarWidth(width)
      // Persist is a COMMAND, not a paint: the caller only sends it at a
      // commit boundary, and the drag's final live apply often painted the
      // same number a moment earlier — deduping here would silently drop
      // the write that makes the width survive a restart.
      if (opts?.persist) {
        try {
          persist?.(next)
        } catch {
          // A failed persist must not lose the already-applied width or
          // wedge the handle — the value stays on screen either way, and
          // the next commit retries. Swallowed on purpose.
        }
      }
      if (next === current) return
      current = next
      applySidebarWidth(panel, next)
      for (const listener of listeners) listener(next)
    },
    subscribe(listener: (width: number) => void) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
  }
}
