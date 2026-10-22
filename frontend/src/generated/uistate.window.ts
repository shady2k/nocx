/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/uistate.window.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The result of uistate.window.get: the WINDOW half of the UI-state document (ADR-0098). The shell owns the native window and the coordinator owns the document, and no process holds both, so the recorded geometry crosses the control plane on its own method and the renderer relays it — carrying the saved geometry to the shell's restore binding at bootstrap, deciding nothing on either leg. Width/Height/X/Y always describe the NORMAL window, never the maximised or full-screen one: those are states, restored by asking the platform to enter them. Every field has a meaningful zero, so an absent document is an ordinary state: Width or Height of 0 means no size was ever recorded and the window keeps the size it opened at; displays of "" means no position can be trusted and the window is centred rather than placed.
 */
export interface UIStateWindow {
  /**
   * The normal window's width in logical pixels, or 0 when no size was ever recorded.
   */
  width: number
  /**
   * The normal window's height in logical pixels, or 0 when no size was ever recorded.
   */
  height: number
  /**
   * The normal window's position. May be negative: a display left of or above the primary is an ordinary arrangement.
   */
  x: number
  /**
   * The normal window's position. May be negative: a display left of or above the primary is an ordinary arrangement.
   */
  y: number
  /**
   * Whether the window was maximised when the session ended. Entering the state is what makes leaving it land on the normal geometry beside it.
   */
  maximised: boolean
  /**
   * Whether the window was full-screen when the session ended.
   */
  fullScreen: boolean
  /**
   * A human-readable fingerprint of the attached displays at the moment the position was recorded. Identity, not containment: a mismatch keeps the size and drops only the position, so plugging the monitor back in restores the old arrangement.
   */
  displays: string
}
