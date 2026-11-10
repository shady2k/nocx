/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/layout.read.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the layout.read JSON-RPC method (nocx-isoph.4, design .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §4.1 and §4.5): the WHOLE durable chain — every workspace, every tab and every pane — as one snapshot. This is the read the twelve write methods of nocx-isoph.2 were missing, and without it 'order, activation and decoration come from the backend' could not be true: a renderer that cannot read the layout back has to remember it, and a fact the renderer remembers is a fact the renderer owns. Reloading the renderer alone, without restarting the backend, is what this answers — the colours, the names, the order and the pinning come back because they were never in the renderer to begin with. ONE call rather than a list method per rung: the three collections are read under the content domain's single operation lane, so they are one consistent picture rather than three that a mutation can interleave with, and the renderer's first draw is one round trip rather than 1 + N + N·M. FLAT LISTS rather than a nested tree, for the same reason there is no third word between workspace and pane: the workspace, tab and pane shapes are each declared exactly once (workspaces.create, tabs.create, panes.create) and are referenced here, so a nested copy would be a second declaration of all three; the edges are already on the rows (a tab names its workspace, a pane names its tab).
 */
export interface LayoutReadResult {
  /**
   * The id of the workspace every tab belongs to until something puts it somewhere else (internal/workspace.Default). It is on the wire rather than compiled into the renderer because the backend is its single owner (AD-8, §7): the default is PERMANENT and NEVER RENDERS — no header, no name, no colour — so the renderer has no name for it and must not acquire one, and a constant copied into the frontend would be a second owner of an id whose whole purpose is having one. A renderer with nowhere else to put a tab puts it here.
   */
  defaultWorkspaceId: string
  /**
   * Every workspace, in the order the switcher shows them. Never null; an empty array is a real answer and the ordinary one on a fresh profile — the default workspace's row is not created until something needs it.
   */
  workspaces: Workspace[]
  /**
   * Every tab in the application, across all workspaces, in (workspace, position) order. Not scoped to one workspace, unlike tabs.reorder: a reorder is an operation on one strip, while this is the picture a renderer draws itself from — and a renderer that had to ask per workspace would be deciding which ones to ask about. Each tab names its own workspace, so the grouping is in the data. What is NOT here is what §4.5 refuses to store: the activity indicator, the attention indicator and the label are computed from a tab's panes, because attention arrives at a PANE and a copy on the tab would give one fact two owners.
   */
  tabs: Tab[]
  /**
   * Every pane in the application, grouped by tab in the order Panes returns them. The pane is the durable identity (§5) and this is where a reloaded renderer finds the set it must draw: a tab is labelled by its panes' titles, so a strip cannot be drawn from the tabs alone.
   */
  panes: Pane[]
}
export interface Workspace {
  /**
   * The workspace's id. Client-minted UUIDv7 and therefore UNTRUSTED INPUT (design .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §7): the shape is validated and never believed, an insert on an id that already means something else FAILS rather than overwriting, and knowing an id confers NO RIGHT to use it — a UUIDv7 embeds a timestamp and is guessable by construction, so nothing anywhere may treat possession of one as evidence.
   */
  id: string
  /**
   * The name the user gave it. A workspace, unlike a tab, is always created deliberately, so it always has one. The DEFAULT workspace is the exception that proves it: it never renders and never acquires a name (workspaces-ux §4.2), and nothing creates it through this method.
   */
  name: string
  /**
   * The colour the user chose for it, or null for a workspace nobody coloured — the default workspace, and any row the backend minted for a session nobody recorded. One of the closed nine in the renderer's layout/workspace-colours.ts. DELIBERATELY NOT THE TAB PALETTE: a tab's colour follows the theme, because a tab decorated under one theme must still read under another; a workspace's colour is the identity of a container the user made and must NOT change when the theme does, any more than its name would. The store keeps a string and judges none of it — what is drawable is the renderer's question, and it already answers it by drawing an unknown value as no colour rather than as a broken swatch.
   */
  colour: string | null
  /**
   * Where it sits in the switcher. Written by the backend from the order workspaces.reorder was given; the renderer never computes it.
   */
  position: number
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
}
export interface Pane {
  /**
   * The pane's id, and the DURABLE IDENTITY of this whole chain (§5): it outlives its shell, its tab and the application, and its blocks are found by it after a restart. Client-minted UUIDv7 and therefore UNTRUSTED INPUT (design .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §7): the shape is validated and never believed, an insert on an id that already means something else FAILS rather than overwriting, and knowing an id confers NO RIGHT to use it — a UUIDv7 embeds a timestamp and is guessable by construction, so nothing anywhere may treat possession of one as evidence.
   */
  id: string
  /**
   * The tab currently holding this pane — the pane's ONLY edge, because panes do not nest (§5). It is a field of an object the renderer asked for, NOT an address: every backend→renderer message is still addressed by sessionId (§4.4), since a tab holds several panes and 'the tab that spoke' is not well defined.
   */
  tabId: string
  /**
   * Where the pane's shell is, and what a restore reopens in.
   */
  cwd: string
  /**
   * Where the pane's pipe goes, and what decides restore behaviour rather than a dialog (§8): a local pane starts a fresh shell in the same cwd, an ssh pane attempts to reconnect. Deliberately two values and not the four an environment has — 'container' and 'unknown' are honest answers about where a recorded command RAN, and a pane is a thing the user opens.
   */
  kind: 'local' | 'ssh'
  /**
   * The canonical user@host:port an ssh pane applies at; null for a local pane. null rather than an empty string, which is a real value meaning the local machine.
   */
  endpoint: string | null
  /**
   * This pane's share of its tab's extent. Size is a property of the MEMBER, direction a property of the set (§5).
   */
  sizeShare: number
}
