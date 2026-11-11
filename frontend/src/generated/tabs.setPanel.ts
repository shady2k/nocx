/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/tabs.setPanel.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the tabs.setPanel JSON-RPC method: the tab as stored after the change, read back rather than echoed. The panel a tab shows is a fact about the tab, stored beside its colour and pinning (nocx-ouru5.1.18), so switching tabs restores the panel each one left behind.
 */
export interface TabsSetPanelResult {
  tab: Tab
}
export interface Tab {
  /**
   * The tab's id. Client-minted UUIDv7 and therefore UNTRUSTED INPUT (design .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §7): the shape is validated and never believed, an insert on an id that already means something else FAILS rather than overwriting, and knowing an id confers NO RIGHT to use it — a UUIDv7 embeds a timestamp and is guessable by construction, so nothing anywhere may treat possession of one as evidence.
   */
  id: string
  /**
   * The workspace this tab is in. NEVER empty and never absent: a tab is always in exactly one workspace and there is no null (workspaces-ux §4.2). The column behind it is nullable, for the CLOSED tab that outlived its workspace, and no closed tab is ever sent here — the wire carries the window set. This is where workspaceId LIVES since §4.5 — it moved off the session, because the backend now owns the whole chain and resolves pane → tab → workspace itself.
   */
  workspaceId: string
  /**
   * The LINEAGE edge and nothing else (§4.2): who spawned whom, provenance, immutable, never set by hand. null for a tab nobody spawned, and null rather than absent so 'no parent' is distinguishable from 'this backend does not say'. It survives the parent being closed: a closed tab keeps its row (nocx-l21ib.4), so the edge still names it and null now means only that nobody spawned this tab. The DISPLAY grouping ('A, B and C are shown together') is the tab's other edge; it is symmetric, has no host and therefore no row (§4.3), and it must never be read off this field.
   */
  parentId: string | null
  /**
   * The name the user typed, or null. null is the NORMAL case and not a defect: a tab created by a drag was never named by anybody (§4.5), so its label is derived from its panes' titles and is COMPUTED, never carried here. A name the user does type is stored and wins.
   */
  name: string | null
  /**
   * The colour the user chose, or null for a tab that was never decorated.
   */
  colour: string | null
  /**
   * Where it sits in the strip. Written by the backend from the order tabs.reorder was given.
   */
  position: number
  /**
   * Whether the tab is kept at the head of the strip.
   */
  pinned: boolean
  /**
   * The direction this tab arranges its panes in. Direction is a property of the SET and size a property of the member (§5), which is why the tab needed a row and the display group did not. Two values, and the cost is stated rather than hidden: panes do not nest, so no asymmetric layout is expressible.
   */
  layout: 'row' | 'column'
  /**
   * When the user last looked at this tab, in Unix milliseconds, or null for a tab never seen. A MARK rather than a verdict: the unseen indicator is computed from it, and storing the verdict would be the duplication §4.5 refuses. The activity and attention indicators are absent for the same reason — attention arrives at a PANE, so a copy on the tab would give one fact two owners.
   */
  seenAt: number | null
  /**
   * The sidebar view this tab shows, or "" for a tab that has never opened one. A view id the build no longer registers is repaired to the first registered view by the renderer: the store keeps a string and judges none of it, exactly as it does for a tab's colour. The panel belongs to the TAB rather than the window (nocx-ouru5.1.18): switching tabs restores the panel each one left behind, and only WHICH view is remembered — never the state inside it, because a Files tree's expansion is the view's own and not a fact about the tab.
   */
  panelView: string
  /**
   * Whether this tab's panel is collapsed to the activity bar. Kept beside the view rather than folded into it, because a collapsed panel remembers the view it reopens on — the behaviour the activity bar's toggle has always had. A tab with no view is collapsed whatever this says: there is nothing to show.
   */
  panelCollapsed: boolean
}
