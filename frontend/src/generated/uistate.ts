/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/uistate.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The result of BOTH uistate.get and uistate.set: the RENDERER'S HALF of the UI-state document (ADR-0048) — what the app must remember without being asked, as opposed to what a user deliberately chose, which is the settings registry. One file rather than two because it is one shape, and the rule is that a result shape is declared once; uistate.set answers with the state as the backend now holds it, which is not always what was sent (the sidebar width is clamped on the way in), and echoing the stored value back is what lets the renderer notice. Window geometry is deliberately absent in both directions: the renderer can neither know it nor act on it, and a copy on the wire would be a second owner of a fact the Wails side already holds. Every field is present and every field has a working default, because an absent document is an ordinary state and never an error a user sees.
 */
export interface UIState {
  sidebar: Sidebar
  /**
   * The durable pane id of the tab that was in front, or "" when none was recorded. A pane id and never an index: an index is meaningless against a different tab set, and the tab set is not restored by this document.
   */
  activeTab: string
}
export interface Sidebar {
  /**
   * Whether the panel is collapsed to the activity bar.
   */
  collapsed: boolean
  /**
   * The sidebar view that was on screen, or "" when none was. A view id the build no longer registers falls back to the first view.
   */
  activeViewId: string
  /**
   * The panel's width as a WHOLE number of CSS pixels, clamped by the backend to the declared bounds. Whole pixels because nothing about a panel edge is meaningful to seven decimal places — a fractional one is what put '206.3828125 px' on a Settings page.
   */
  width: number
}
