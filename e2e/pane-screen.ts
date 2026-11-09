// The pane's own screen reading, declared ONCE for the whole e2e project
// (nocx-zg3k3.2.8; the second declaration that collided with it was
// nocx-15b13).
//
// window.__nocxPaneScreen is the renderer's test seam over the ACTIVE pane
// (frontend/src/terminal-content.ts, installPaneScreenSeam): the cell model's
// installed revision and rows, the frame's committed geometry, and what this
// client last reported. Several specs read it.
//
// WHY IT LIVES HERE, AND WHY A SECOND ONE DOES NOT SIMPLY SHADOW IT.
// TypeScript merges an interface's properties across the whole program and
// refuses two DIFFERENT types for one name (TS2717). A spec that declared the
// seam again with only the fields it happened to read — `revision` and `rows` —
// did not get a narrower local view: it collided with this one, the program
// then resolved the name to the narrower shape, and every OTHER spec lost
// `geometry` and `reported` (`Property 'geometry' does not exist on type
// PaneScreenReading`). The root `npm run typecheck`, which checks this
// directory, is what caught it. So the reading is declared here and specs that
// name it import it; a spec that needs a subset reads the subset off this type.

/** The frame's committed geometry — the per-cell metric the client's own
 *  pixel-to-cell mapping reads. */
export interface PaneScreenGeometry {
  cols: number
  rows: number
  cellWidthPx: number
  cellHeightPx: number
}

/** What THIS client last reported for the session, as the seam offers it —
 *  structurally the renderer's `SessionSize`, which this program cannot import
 *  (the renderer is a different project with its own tsconfig). */
export interface PaneScreenReport {
  cols: number
  rows: number
  xpixel: number
  ypixel: number
}

/** What the seam answers for the active pane: the model's installed revision
 *  and each row's text (cells joined), or nulls when the pane has no frame
 *  yet. A model with no installed revision reads as `revision: null` and no
 *  rows. */
export interface PaneScreenReading {
  revision: number | null
  rows: string[]
  geometry: PaneScreenGeometry | null
  reported: PaneScreenReport | null
}

declare global {
  interface Window {
    __nocxPaneScreen?: () => PaneScreenReading | null
  }
}
