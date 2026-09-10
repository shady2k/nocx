/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/workers.tabCreated.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the workers.tabCreated JSON-RPC notification (nocx-ui8q6.3): a worker participant's tab, minted by workers.spawn, has appeared in the layout chain. workers.spawn writes the tab and pane rows directly (internal/app/workers.go), the same content-domain write tabs.create makes, but through no capability.LayoutOperation and therefore through no request a renderer sent — a person cannot learn of it by waiting on a response the way tabs.create's own caller does. This notification is the only way a connected renderer learns the tab exists before it next reads the whole chain. tab and firstPane are the SAME shapes tabs.create already returns (contracts/tabs.create.schema.json, contracts/panes.create.schema.json) — extended, not reinvented, because a renderer that already knows how to fold a tabs.create response into its layout cache folds this in exactly the same way. It is broadcast to every connected client rather than addressed to one session's subscriber: unlike lifecycle.changed or files.changed, a tab is not the property of the session running inside it (that session, opened by the backend rather than by an attach, has no subscriber of its own to resolve at emit time — a worker pane is never 'the session a renderer is looking through'), it is a row in the shared layout chain every window reads with layout.read, so every window is the audience. sessionId, instanceId and sessionEpoch are the participant's own session identity (session_open.go's OpenedSession), carried for two reasons: first, a renderer that wants to bring this tab up must ATTACH to the session already running inside it rather than OPEN a second one over the same pane (attach.schema.json takes exactly this triple) — sessions.live exists for the same handoff at startup, and this is its live-event twin, pushed once at the moment of creation rather than asked for with a second round trip. Second, it is what a STALE notification means for a tab fact that carries no lane and no epoch of its own the way lifecycle.changed does: one WebSocket speaks to exactly one backend instance for its whole life (AD-7), so an instanceId this connection has never seen from any session it already holds names a notification that could only have been queued before a reconnect this renderer has since completed, and it must be dropped rather than folded into a layout cache that belongs to the CURRENT instance. replayFrom and attached mirror sessions.live's own fields for the identical reason sessions.live carries them: a fresh attach has to be told where the replayable stream starts and whether somebody already holds it, and inventing a second vocabulary for the same two facts is exactly what AD-8 forbids.
 */
export interface WorkersTabCreated {
  tab: Tab
  firstPane: Pane
  /**
   * The participant's session id, server-minted and server-authoritative (AD-7). Not an addressing field — this notification is broadcast — but what a renderer that wants to bring the tab up passes to `attach` instead of `open`, so it takes back the session already running rather than starting a second one over the same pane.
   */
  sessionId: string
  /**
   * The backend instance that minted the session (AD-7, nocx-3oupk) — the same field lifecycle.changed and sessions.live carry. A connection speaks to exactly one instance for its whole life, so the renderer compares this against the instanceId of any session it already holds from this connection and drops the notification on a mismatch: that mismatch names a fact queued before a reconnect this renderer has since completed, and folding it into the current layout cache would be applying a fact from a backend that is no longer the one behind this socket.
   */
  instanceId: string
  /**
   * The session's epoch within its backend instance, as minted at open (same vocabulary as attach's and sessions.live's sessionEpoch): distinguishes a later session that reuses this sessionId from the incarnation this notification is about.
   */
  sessionEpoch: number
  /**
   * The oldest byte offset the session's replay ring still holds, exactly as sessions.live reports it. A renderer attaching to this session for the first time attaches here.
   */
  replayFrom: number
  /**
   * Whether a client already holds this session, exactly as sessions.live reports it. False at the moment of creation in the ordinary case — nobody has attached to a tab that has just appeared — but stated rather than assumed, on the same terms sessions.live states it.
   */
  attached: boolean
}
/**
 * The tab workers.spawn minted, as stored — the same shape tabs.create returns.
 */
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
/**
 * The participant's one pane, as stored — the same shape panes.create returns.
 */
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
