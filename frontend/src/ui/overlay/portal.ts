/**
 * Portal root — a single `document.body`-level element owned by the kit.
 *
 * A native modal `<dialog>` is exempt from portal-root use (it renders in the
 * browser top layer). Future custom overlays (Popover, Menu, Combobox) portal
 * into this root. It carries `--wails-draggable: no-drag` so overlays inside
 * it cannot accidentally become Wails drag targets.
 *
 * ADR-0014: "Portal root — a single document.body-level element owned by the kit."
 *
 * @example
 * ```ts
 * import { getPortalRoot } from './overlay/portal'
 * const root = getPortalRoot()
 * render(() => <MyOverlay />, root)
 * ```
 */

const ROOT_ID = 'nocx-portal-root'
let root: HTMLDivElement | null = null

/**
 * Returns the portal root element, creating it if necessary.
 * Call once at kit init; subsequent calls return the same element.
 */
export function getPortalRoot(): HTMLDivElement {
  if (root && document.body.contains(root)) return root

  const existing = document.getElementById(ROOT_ID) as HTMLDivElement | null
  if (existing) {
    root = existing
    return root
  }

  const el = document.createElement('div')
  el.id = ROOT_ID
  // Drag-region guard: the browser top layer (for native <dialog>) is outside
  // our root, but custom overlays inside the root inherit --wails-draggable
  // from their ancestors. Setting no-drag here ensures a portaled overlay
  // cannot accidentally land on a drag region.
  el.style.setProperty('--wails-draggable', 'no-drag')
  document.body.appendChild(el)
  root = el
  return root
}

/** Remove the portal root from the DOM (for testing / teardown). */
export function removePortalRoot(): void {
  if (root && document.body.contains(root)) {
    document.body.removeChild(root)
  }
  root = null
}
