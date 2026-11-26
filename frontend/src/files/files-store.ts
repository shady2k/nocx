// FilesTreeStore — the Files panel's tree: lazy expansion with pagination
// (D10), origin-scoped staleness (D4) and canonical cycle detection (D9).
//
// The four rules that make it correct, each with the failure it stops:
//
// 1. THE ROOT COMES FROM files.open AND DOES NOT MOVE. The composition
//    layer pins it to the FILESYSTEM ROOT `/` — a verified OSC 7 cwd is
//    never handed to files.open, so a later `cd` (a new OSC 7 cwd on the
//    same session) must not re-root the tree: rescope() is a no-op when
//    the session scope is unchanged and REVEALS instead (walk the chain
//    from the root down to the new cwd, expand, select — never collapse).
//    Only a different tab or a dead binding re-opens.
// 2. STALE RESPONSES ARE DROPPED, AND NOTHING CLIENT-MINTED GOES ON THE
//    WIRE. Every request captures the {generation, bindingId} scope it was
//    issued for; a response applies only if it still belongs to the view's
//    current scope (the binding — a files.list for session A, still in flight
//    when the user activates session B, must never paint A's listing into
//    B's tree) AND its generation is not older than what has already been
//    applied to that node (the ordering guard — a refresh re-list that landed
//    first must not be overwritten by the expand that started before it). The
//    generation counter grows on every re-scope and every refresh cycle, so
//    it orders answers and never decides which context owns them: a paneId
//    belongs to a view, not to a tree (ALX-1031).
// 3. CYCLE DETECTION COSTS NO EXTRA CALL. Every successful list returns the
//    listed directory's `canonical`; the store compares it against the
//    canonicals it already holds for the directory's expanded ancestors
//    (root first) BEFORE committing children, marks the row cyclic on a
//    match, commits nothing and never asks again.
// 4. state IS A DISCRIMINATOR, SWITCHED ON FIRST. tooLarge is a real state
//    ("more than N entries", no pagination offered), timedOut is its own
//    state with a retry; permission-denied arrives as a rejected call and is
//    a rendered node state too. None of them is a toast, none is an empty
//    directory.
//
// The tree is plain mutable objects behind a version signal: Solid re-renders
// on the bump, and the mutation-before-render order is enforced by methods
// never being called during a render.

import { createMemo, createSignal, untrack } from 'solid-js'
import type { FilesListResult, FilesListEntry } from '../generated/files.list'
import type { FilesChanged } from '../generated/files.changed'
import type { FilesRefreshStateChanged } from '../generated/files.refreshStateChanged'
import type { FilesPanelServices } from './files-client'
import type { CallOrigin } from '../dispatcher'
import type { ActiveOrigin } from '../pane-content'
import { isExpandable, type TreeRowKind } from '../ui/tree-row-kind'
import { helperGate, type HelperGate } from '../connection/helper-axis'

/** The watch set for a tree: the successfully listed root plus every
 *  successfully listed EXPANDED directory — exactly the rows the panel can
 *  render (§5.2). A directory whose first list is pending or failed must not
 *  enter the set: files.watch takes its own baseline listing, so publishing
 *  it early races the foreground files.list on the capacity-one filesystem
 *  gate and can make the panel refuse its own startup work.
 *
 *  The backend REPLACES the set on every files.watch, so collapsing a
 *  directory removes it by construction and can never leak a watch. The
 *  notification carries the same provider-syntax paths, so the changed
 *  handler matches against this vocabulary. */
function currentWatchPaths(root: FilesRoot): string[] {
  const paths: string[] = []
  if (root.state !== 'ok') return paths
  paths.push(root.path)
  const collect = (dir: FilesRoot | FilesNode): void => {
    for (const child of dir.children) {
      if (child.expanded && child.state === 'ok') {
        paths.push(child.path)
        collect(child)
      }
    }
  }
  collect(root)
  return paths
}

/** The entry's path relative to the display root, as spelled — lexical,
 *  symlinks unresolved. That is what a person means by "the path in this
 *  tree"; the D9 canonical is the deduplication identity and resolves
 *  symlinks, so copying it would hand the user a path they did not click
 *  on. The root is always the tree's own root, so every visible entry is
 *  under it; the fallback spells the path as-is rather than inventing a
 *  relative form for a node that is not in the tree. */
function relativePathOf(rootPath: string | null, nodePath: string): string {
  // The filesystem root has no prefix: the path as spelled from / IS the
  // path minus its leading slash. (rootPath + '/' would be '//' — a
  // prefix no path has — so the general form cannot serve the root.)
  if (rootPath === '/') return nodePath.startsWith('/') ? nodePath.slice(1) : nodePath
  if (rootPath !== null && nodePath.startsWith(rootPath + '/')) {
    return nodePath.slice(rootPath.length + 1)
  }
  return nodePath
}

/** One page of children per expanded directory (D10). A starting number,
 *  named in code because §9 of the design says so: the backend's ordering is
 *  deterministic before pagination, so the page size is a product choice to
 *  tune once the panel is in daily use — not a constant somebody picked. */
export const FILES_PAGE_SIZE = 50

type FilesPanelPhase = 'no-origin' | 'opening' | 'ready' | 'failed'

type FilesListingState = 'ok' | 'tooLarge' | 'timedOut' | 'error'

/** The state of one expanded-or-attempted directory's enumeration. */
interface DirListing {
  /** A list request is in flight for this directory. */
  busy: boolean
  /** The outcome discriminator of the last enumeration: null before the
   *  first attempt completes. */
  state: FilesListingState | null
  tooLargeLimit: number | null
  observedCount: number | null
  timeout: number | null
  /** Rejected-call message (permission denied, dead channel). */
  error: string | null
  /** The directory's provider-canonical identity, from its last successful
   *  list — the D9 cycle vocabulary. */
  canonical: string | null
  rev: string
  total: number
  hasMore: boolean
  nextOffset: number
  /** The generation whose response was last applied to this directory. */
  appliedGeneration: number
  /** A "Show next" the person pressed while this directory was already
   *  enumerating. It is an INPUT and not a second `busy`: the click is
   *  serviced when the listing in flight settles (resumePendingPage), never
   *  dropped, because a change-driven re-list owns the enumeration but not
   *  the person's intent (nocx-atpw5.1.16). */
  wantsNextPage: boolean
  /** Which request set `busy`, so the request — and only the request — can
   *  clear it (nocx-4y82c.1.6). A listing that answers for a scope the store
   *  has left must not touch the TREE; it must still take its own busy flag
   *  down, or a person watches a row spin for as long as the panel lives. */
  busySeq: number
}

function emptyListing(): DirListing {
  return {
    busy: false,
    state: null,
    tooLargeLimit: null,
    observedCount: null,
    timeout: null,
    error: null,
    canonical: null,
    rev: '',
    total: 0,
    hasMore: false,
    nextOffset: 0,
    appliedGeneration: -1,
    wantsNextPage: false,
    busySeq: 0,
  }
}

/** One row of the tree: the entry as the wire listed it, plus the tree's
 *  own state for it. `children` is only meaningful while `expanded`. */
export interface FilesNode extends DirListing {
  name: string
  path: string
  kind: TreeRowKind
  linkTarget?: string
  linkKind?: TreeRowKind
  size: number
  modTime: string
  mode: number
  expanded: boolean
  /** Symlink whose canonical matched an expanded ancestor (D9): renders as
   *  a leaf and is never requested again. */
  cyclic: boolean
  children: FilesNode[]
}

/** The root from files.open, plus its own listing state — the root is a
 *  directory like any other and can be tooLarge or timedOut too. It is never
 *  a row (no TreeRow); its children are the depth-0 rows. */
interface FilesRoot extends DirListing {
  path: string
  display: string
  inferred: boolean
  inferredReason: string
  children: FilesNode[]
}

export type FilesFlatRow =
  | { kind: 'entry'; node: FilesNode; depth: number; projected?: boolean }
  | { kind: 'loading'; dir: FilesRoot | FilesNode; depth: number }
  | { kind: 'more'; dir: FilesRoot | FilesNode; depth: number }
  | { kind: 'state'; dir: FilesRoot | FilesNode; depth: number }
  | { kind: 'filter-searching'; dir: FilesRoot | FilesNode; depth: number }
  | { kind: 'filter-refusal'; dir: FilesRoot | FilesNode; depth: number; message: string }

interface FilterProjection {
  dir: FilesRoot | FilesNode
  bindingId: string
  generation: number
  filterGeneration: number
  rev: string
  state: 'searching' | 'ready' | 'refusal'
  nodes: FilesNode[]
  error: string | null
}

interface FilesBinding {
  bindingId: string
  endpointId: string | null
  /** Whether this backend has a file-manager revealer wired. A build
   *  fact carried on the open result; the panel offers "Show in Finder"
   *  only when it is true (nocx-ngf3u). */
  revealAvailable: boolean
}

export interface FilesTreeStore {
  phase(): FilesPanelPhase
  /** THE FAR HELPER'S GATE (design §5.10, C25/C26): `starting` until an
   *  observation of this tab's connection arrives (or while it says
   *  `starting`), `blocked` while the axis does not serve, and NULL when the
   *  tab may ask its host — every local tab, and an SSH tab whose helper
   *  serves. It is the ONE fold's answer (connection/helper-axis.ts), read
   *  from the origin this store is scoped to.
   *
   *  While it is not null the store asks NOTHING: no files.open, no
   *  files.list, no files.watch, no read. That is the panel's half of "a
   *  panel is unavailable only with the connection's reason"; its binding is
   *  released and the tree stays in memory, so the folders a person opened
   *  come back with the axis instead of being re-opened by hand. */
  axisGate(): HelperGate
  openError(): string | null
  binding(): FilesBinding | null
  origin(): ActiveOrigin | null
  root(): FilesRoot | null
  /** The visible rows in display order (the flatten of the expanded tree).
   *  The NAME FILTER IS NOT APPLIED HERE — narrowing is the view's, over
   *  `files-filter.ts`, because a filter that reached into the flatten
   *  would be a store deciding what is on screen, and the store's rows are
   *  also what the reveal walk and the watch-set reconciliation reason
   *  about. Both must go on seeing the whole tree. */
  rows(): FilesFlatRow[]
  /** Rows for the active name filter. These may contain disposable
   *  whole-directory projection entries; `rows()` remains the committed tree. */
  filterRows(): FilesFlatRow[]
  /** Whether every expanded-and-truncated directory has answered. */
  filterSettled(): boolean
  /** The panel's name filter (nocx-708q.2), and its residence is the same
   *  choice Git's made: in the store, so it survives the view being
   *  swapped out and back — the panel is unmounted whenever another
   *  sidebar view is in front, and a filter that evaporated on a glance at
   *  Ports would be a control nobody could rely on.
   *
   *  It is NOT cleared on a re-scope. A name is a stable vocabulary across
   *  machines in a way a session id is not: somebody filtering for
   *  "nginx.conf" while comparing two hosts is doing exactly what the
   *  control is for. */
  filter(): string
  setFilter(value: string): void
  /** Walk from the root down to `path`, listing and expanding each level
   *  that is not already expanded, then select the target (revealTarget)
   *  and expand it too — arriving somewhere shows you what is there, so
   *  the target ends OPEN with its first page listed (see finishReveal).
   *  NEVER collapses — a directory the user opened by hand stays open.
   *  Idempotent: revealing the path already revealed (or a reveal to it
   *  in flight) does nothing. Resolves true when the target is an expandable
   *  directory, including a non-cyclic directory symlink, and false when the
   *  target is a regular file or the walk cannot reach it. Stops honestly:
   *  a level that comes back tooLarge/timedOut/unreadable, or a path that
   *  does not exist under the root, ends the walk with what was expanded left
   *  expanded and the level's state row rendered — the reveal did not reach
   *  the target and the tree says so where it stopped. */
  revealPath(path: string): Promise<boolean>
  /** The path the last completed reveal selected, or null when nothing
   *  has been revealed: no verified cwd yet, a viewer origin (no
   *  opinion), or a fresh scope. The view renders the matching row
   *  selected and scrolls it into view — the scroll belongs to the
   *  view, never the store. */
  revealTarget(): string | null
  /** Re-scope to the active tab's origin: opens a fresh binding when the
   *  session changed or the previous binding is gone; a no-op when the same
   *  session is still in front (the root does not move, rule 1). */
  rescope(origin: ActiveOrigin | null): void
  /** Expand or collapse a directory row. */
  toggle(node: FilesNode): void
  /** Fetch the next page of an expanded directory. */
  showMore(dir: FilesRoot | FilesNode): void
  /** Re-issue the failed enumeration of a timedOut (or error) directory. */
  retry(dir: FilesRoot | FilesNode): void
  /** Re-list the root and every expanded directory in one cycle (the
   *  header's refresh action); re-opens when the binding is gone. Also
   *  re-sends the watch set, so it is the Retry for a failed watch. */
  refresh(): void
  /** Whether the panel showing this tree is on screen. Collapsing the
   *  sidebar or switching to another view counts as hidden, and while it is
   *  hidden the BACKEND stops polling entirely — the Files poll is
   *  backend-driven, so unlike Git and Ports the panel cannot stop it by
   *  simply not asking (nocx-8hdia.1). Idempotent per binding: re-asserting
   *  the same value sends nothing, and a new binding re-sends. */
  setVisible(visible: boolean): void
  /** The backend's reported refresh mode for the current watch set: null
   *  until the first files.watch response (§5.5). */
  watchMode(): 'watching' | 'polling' | null
  /** Why refresh is degraded — non-null only for a LOCAL binding whose live
   *  watch could not be established and which fell back to polling. The
   *  persistent badge renders from this; a remote binding's designed-mode
   *  polling has no reason and warns about nothing. */
  watchDegradedReason(): string | null
  /** A files.watch call failed — the change stream may be gone. Sticky
   *  until the next successful watch (the header refresh re-establishes
   *  it); rendered as an inline message with Retry, never a toast. */
  watchFailed(): string | null
  /** The binding-level freshness state: delayed only after the backend's
   *  visible observation window crosses its threshold, and ok on recovery. */
  refreshState(): 'ok' | 'delayed' | null
  /** The entry's path relative to the display root, as spelled — lexical,
   *  symlinks unresolved (the copy-path action's "path in this tree"). */
  relativePath(node: FilesNode): string
  /** Close the binding and reset. Called when the view unmounts; the store
   *  is reusable — the next rescope re-opens. */
  dispose(): void
}

/** The scope a list or watch response was issued for. `paneId` is
 *  deliberately absent (ALX-1031): a pane is a view of one context, and a
 *  viewer tab or a diff tab of the same session is the same tree — the
 *  membership test is the binding. The generation stays because it orders
 *  answers per node (`appliedGeneration`), a different question from which
 *  instance a read belongs to. */
interface ListCtx {
  generation: number
  bindingId: string | null
}

export function createFilesTreeStore(services: FilesPanelServices): FilesTreeStore {
  let generation = 0
  /** The claim counter behind DirListing.busySeq (claimBusy): one number per
   *  request that takes a directory's busy flag, so the flag can only be
   *  cleared by the request that took it (nocx-4y82c.1.6). */
  let busySeq = 0
  /** True between dispose() and the next rescope(): late responses from a
   *  previous life of this store must not touch the reset state. */
  let closed = true
  /** Whether the panel showing this tree is on screen. FALSE until the
   *  sidebar first shows Files, which is the state in which this store does
   *  no work at all (ADR-8eej3e66 §9, setVisible). */
  let panelVisible = false
  /** True when the axis STOPPED SERVING and then served again while the
   *  panel was hidden (design §5.10, C25). Nothing is asked while nothing is
   *  on screen, so the return is recorded here and the re-read happens when
   *  the panel is shown — over the binding the pause kept, with the open
   *  folders still in the tree. */
  let treeNeedsResume = false
  /** True while the resumed tree has not yet read its root: the one state in
   *  which a failed read may mean the HELD binding is gone rather than the
   *  host refusing the path (see reopenDeadScope). */
  let resumeUnread = false
  /** The session the open binding belongs to, so the panel showing on a tab
   *  that moved while it was hidden can tell that the tree is the wrong one
   *  without keeping a second copy of the origin. */
  let openedSession: string | null = null
  // A shell can exit before its replacement supplies an origin. Keep its
  // tree out of the panel until the same pane and connection are identified.
  let interruptedScope: { origin: ActiveOrigin; root: FilesRoot } | null = null

  const [phase, setPhase] = createSignal<FilesPanelPhase>('no-origin')
  const [openError, setOpenError] = createSignal<string | null>(null)
  const [binding, setBinding] = createSignal<FilesBinding | null>(null)
  const [origin, setOrigin] = createSignal<ActiveOrigin | null>(null)
  /** The far helper's gate over the CURRENT origin (design §5.10). Derived,
   *  never stored: the origin is the observation, and a copied state would be
   *  a second answer that could lag the one the head draws. */
  const axisGate = createMemo<HelperGate>(() => {
    const o = origin()
    return o === null ? null : helperGate(o)
  })
  const [root, setRoot] = createSignal<FilesRoot | null>(null)
  const [treeVersion, setTreeVersion] = createSignal(0)
  const bumpTree = (): void => {
    setTreeVersion((v) => v + 1)
  }
  /** The §5.5 watching state: the backend's reported refresh mode for the
   *  current watch set, the degraded-mode reason (local fallback to
   *  polling), a sticky watch failure, and the binding-level freshness edge
   *  (routine pacing stays invisible). */
  const [watchMode, setWatchMode] = createSignal<'watching' | 'polling' | null>(null)
  const [watchDegradedReason, setWatchDegradedReason] = createSignal<string | null>(null)
  const [watchFailed, setWatchFailed] = createSignal<string | null>(null)
  const [refreshState, setRefreshState] = createSignal<'ok' | 'delayed' | null>(null)
  /** The panel's name filter (nocx-708q.2): the one string that narrows the
   *  tree. Typing activates one serialized, whole-directory projection after
   *  a debounce; subsequent keystrokes reuse it, so they cannot churn the
   *  watch set or race a reveal walk. Deliberately NOT reset by `dispose`,
   *  which is what makes it survive a sidebar view switch. */
  const [filter, setFilterSignal] = createSignal('')
  /** Filter projections are presentation-only whole-directory snapshots. */
  const [filterVersion, setFilterVersion] = createSignal(0)
  const filterProjections = new Map<string, FilterProjection>()
  let filterTimer: ReturnType<typeof setTimeout> | null = null
  let filterActivationReady = false
  let filterSearchStarted = false
  let filterQueue: {
    dir: FilesRoot | FilesNode
    ctx: ListCtx
    rev: string
    filterGeneration: number
  }[] = []
  let filterQueueRunning = false
  let refreshing = false
  let filterGeneration = 0
  const FILTER_ACTIVATION_DEBOUNCE = 150
  const bumpFilter = (): void => {
    setFilterVersion((v) => v + 1)
  }
  /** The path the last completed reveal selected (see the interface doc).
   *  Reset on every re-scope and dispose — a selection from a previous
   *  machine must not linger. */
  const [revealTarget, setRevealTarget] = createSignal<string | null>(null)
  let pendingReveal: string | null = null
  let pendingRevealPromise: Promise<boolean> | null = null
  let pendingRevealResolve: ((isDirectory: boolean) => void) | null = null
  const settleReveal = (isDirectory: boolean): void => {
    const resolve = pendingRevealResolve
    pendingRevealResolve = null
    pendingRevealPromise = null
    resolve?.(isDirectory)
  }
  const cancelReveal = (): void => {
    settleReveal(false)
  }
  /** True while a reveal walk is in flight. The change stream must not
   *  refresh a directory the walk is paging: a files.changed issued
   *  against the walk's first page would re-list `offset=0, limit=50`,
   *  and if that response lands AFTER the walk's later pages it replaces
   *  them — the accumulated rows shrink back to page 1 and the revealed
   *  target vanishes. The walk's own pages are the freshest data for the
   *  dirs it touches; the next poll's change (or the walk's re-sent watch
   *  set) re-validates anything missed in the few milliseconds it runs. */
  let revealing = false
  /** The watch set as last handed to files.watch, or null when the backend's
   *  set must be treated as unknown (never sent; dropped by a reconnect; the
   *  refresh cycle deliberately forcing a re-send).
   *
   *  This record, not a flag, is what decides whether to publish. The set
   *  changes when a directory's first listing SUCCEEDS — not when something
   *  decides to expand it, and there is an await between those two moments.
   *  A flag set at the expansion and cleared at a walk's end therefore
   *  published a set its own listings had not joined yet, and a walk that was
   *  superseded never reached its own end at all: reveal /home/alice expands
   *  /home, `cd /` supersedes it and completes with nothing of its own to
   *  publish, and /home stays rendered and unwatched, so nothing under it
   *  ever reports a change again. Comparing what is rendered against what was
   *  published cannot miss that, because it does not depend on which code
   *  path noticed. */
  let publishedWatchPaths: string[] | null = null
  /** The outstanding files.watch, so a caller that must not overlap it (the
   *  open chain before reveal — all three operations claim the same
   *  capacity-one filesystem admission) can await the publish that some
   *  other seam started. */
  let watchInFlight: Promise<void> = Promise.resolve()
  /** The current reveal walk's identity. Every walk captures it; a step
   *  applies only while it still matches — a reveal in flight when the
   *  origin changes (or a newer reveal starts) must drop, never paint. */
  let revealWalkId = 0

  interface RowWalkerOptions {
    children(dir: FilesRoot | FilesNode): readonly FilesNode[]
    resolveNode(dir: FilesRoot | FilesNode, candidate: FilesNode): FilesNode
    isProjected(dir: FilesRoot | FilesNode, candidate: FilesNode): boolean
    structural(dir: FilesRoot | FilesNode, depth: number): FilesFlatRow | null
  }

  function walkRows(r: FilesRoot, options: RowWalkerOptions): FilesFlatRow[] {
    const out: FilesFlatRow[] = []
    const emitDir = (dir: FilesRoot | FilesNode, childDepth: number): void => {
      for (const candidate of options.children(dir)) {
        const node = options.resolveNode(dir, candidate)
        out.push({
          kind: 'entry',
          node,
          depth: childDepth,
          ...(options.isProjected(dir, candidate) ? { projected: true } : {}),
        })
        if (node.expanded) emitDir(node, childDepth + 1)
      }
      const structural = options.structural(dir, childDepth)
      if (structural !== null) out.push(structural)
    }
    emitDir(r, 0)
    return out
  }

  function standardStructural(dir: FilesRoot | FilesNode, depth: number): FilesFlatRow | null {
    if (dir.state === 'ok') return dir.hasMore ? { kind: 'more', dir, depth } : null
    if (dir.state !== null) return { kind: 'state', dir, depth }
    return dir.busy ? { kind: 'loading', dir, depth } : null
  }

  const rows = createMemo<FilesFlatRow[]>(() => {
    treeVersion()
    const r = root()
    if (r === null) return []
    return walkRows(r, {
      children: (dir) => dir.children,
      resolveNode: (_dir, candidate) => candidate,
      isProjected: () => false,
      structural: standardStructural,
    })
  })

  function validFilterProjection(dir: FilesRoot | FilesNode): FilterProjection | null {
    const projection = filterProjections.get(dir.path)
    const currentBinding = untrack(binding)
    if (
      projection === undefined ||
      projection.dir !== dir ||
      projection.bindingId !== currentBinding?.bindingId ||
      projection.generation !== generation ||
      projection.filterGeneration !== filterGeneration ||
      projection.rev !== dir.rev
    ) {
      return null
    }
    return projection
  }

  function buildFilterRows(): FilesFlatRow[] {
    const r = untrack(root)
    if (r === null) return []
    const active = filter().trim() !== ''
    const sources = new Map<
      FilesRoot | FilesNode,
      { candidates: readonly FilesNode[]; actual: Map<string, FilesNode> | null }
    >()
    const sourceFor = (
      dir: FilesRoot | FilesNode,
    ): { candidates: readonly FilesNode[]; actual: Map<string, FilesNode> | null } => {
      const existing = sources.get(dir)
      if (existing !== undefined) return existing
      const eligible = active && dir.state === 'ok' && dir.hasMore
      const projection = eligible ? validFilterProjection(dir) : null
      const actual =
        projection?.state === 'ready'
          ? new Map(dir.children.map((child) => [child.path, child]))
          : null
      const source = {
        candidates: projection?.state === 'ready' ? projection.nodes : dir.children,
        actual,
      }
      sources.set(dir, source)
      return source
    }
    return walkRows(r, {
      children: (dir) => sourceFor(dir).candidates,
      resolveNode: (dir, candidate) => sourceFor(dir).actual?.get(candidate.path) ?? candidate,
      isProjected: (dir, candidate) => {
        const actual = sourceFor(dir).actual
        return actual !== null && !actual.has(candidate.path)
      },
      structural: (dir, depth) => {
        const eligible = active && dir.state === 'ok' && dir.hasMore
        const projection = eligible ? validFilterProjection(dir) : null
        if (!active || !eligible) return standardStructural(dir, depth)
        if (projection?.state === 'refusal') {
          return {
            kind: 'filter-refusal',
            dir,
            depth,
            message: projection.error ?? 'Could not search this directory',
          }
        }
        return projection?.state === 'ready' ? null : { kind: 'filter-searching', dir, depth }
      },
    })
  }

  const filterRows = createMemo<FilesFlatRow[]>(() => {
    treeVersion()
    filterVersion()
    filter()
    return buildFilterRows()
  })

  function eligibleFilterDirs(): (FilesRoot | FilesNode)[] {
    const r = untrack(root)
    if (r === null) return []
    const dirs: (FilesRoot | FilesNode)[] = []
    const collect = (dir: FilesRoot | FilesNode): void => {
      if (dir.state === 'ok' && dir.hasMore) dirs.push(dir)
      for (const child of dir.children) if (child.expanded) collect(child)
    }
    collect(r)
    return dirs
  }
  /** The response's request is still the view's current scope: same tab,
   *  and — once a binding exists — the same binding. The generation part of
   *  the triple is checked per node (rule 2), not here.
   *
   *  The signal reads are deliberately UNTRACKED: a response handler asks
   *  "is this still the scope?" at the moment the response lands, and must
   *  never re-run when the scope changes — rule 2 exists precisely because
   *  a response applies to the state it lands in, not to a state it could
   *  have re-rendered for. untrack says that in code, and keeps the
   *  one-shot guard from looking like a reactive derivation. */

  function maybeQueueFilterDirs(): void {
    if (!filterSearchStarted || !filterActivationReady || refreshing || revealing) return
    const ctx = captureCtx()
    if (ctx === null) return
    const eligible = eligibleFilterDirs()
    const eligiblePaths = new Set(eligible.map((dir) => dir.path))
    for (const path of filterProjections.keys()) {
      if (!eligiblePaths.has(path)) filterProjections.delete(path)
    }
    for (const dir of eligible) {
      const existing = validFilterProjection(dir)
      if (existing !== null) continue
      const projection: FilterProjection = {
        dir,
        bindingId: ctx.bindingId as string,
        generation: ctx.generation,
        filterGeneration,
        rev: dir.rev,
        state: 'searching',
        nodes: [],
        error: null,
      }
      filterProjections.set(dir.path, projection)
      filterQueue.push({ dir, ctx, rev: dir.rev, filterGeneration })
    }
    bumpFilter()
    if (filterQueue.length > 0) void runFilterQueue()
  }

  async function runFilterQueue(): Promise<void> {
    if (filterQueueRunning) return
    filterQueueRunning = true
    await watchInFlight
    bumpFilter()
    try {
      while (filterQueue.length > 0) {
        if (refreshing || revealing) return
        const item = filterQueue.shift()!
        if (item.filterGeneration !== filterGeneration || !scopeCurrent(item.ctx)) continue
        const projection = validFilterProjection(item.dir)
        if (projection === null) continue
        const limit = Math.max(item.dir.total, FILES_PAGE_SIZE + 1)
        try {
          const result = await services.list(item.ctx.bindingId as string, item.dir.path, 0, limit)
          if (
            !scopeCurrent(item.ctx) ||
            item.filterGeneration !== filterGeneration ||
            filterProjections.get(item.dir.path) !== projection
          ) {
            continue
          }
          if (result.state === 'ok' && (result.rev !== item.rev || item.dir.rev !== item.rev)) {
            filterGeneration++
            filterQueue = []
            filterProjections.clear()
            filterSearchStarted = false
            bumpFilter()
            refreshDir(item.dir, item.ctx)
          } else if (result.state === 'ok') {
            const oldProjected = new Map(projection.nodes.map((node) => [node.path, node]))
            const actual = new Map(item.dir.children.map((node) => [node.path, node]))
            projection.nodes = result.entries.map(
              (entry) =>
                actual.get(entry.path) ?? oldProjected.get(entry.path) ?? entryToNode(entry),
            )
            projection.state = 'ready'
            projection.error = null
          } else if (result.state === 'tooLarge') {
            projection.state = 'refusal'
            projection.error = `Directory too large to search (more than ${result.limit} entries)`
          } else if (result.state === 'timedOut') {
            projection.state = 'refusal'
            projection.error = 'Directory search took too long'
          } else {
            filterProjections.delete(item.dir.path)
          }
        } catch (e) {
          if (
            scopeCurrent(item.ctx) &&
            item.filterGeneration === filterGeneration &&
            filterProjections.get(item.dir.path) === projection
          ) {
            projection.state = 'refusal'
            projection.error = messageOf(e)
          }
        }
        bumpFilter()
      }
    } finally {
      filterQueueRunning = false
      bumpFilter()
    }
  }

  function maybeStartFilterSearch(): void {
    if (filter().trim() === '' || !filterActivationReady || refreshing || revealing) return
    if (!filterSearchStarted) {
      filterSearchStarted = true
      filterGeneration++
      filterProjections.clear()
      filterQueue = []
    }
    maybeQueueFilterDirs()
  }

  function discardFilterProjection(): void {
    if (filterTimer !== null) {
      clearTimeout(filterTimer)
      filterTimer = null
    }
    filterGeneration++
    filterQueue = []
    filterProjections.clear()
    filterActivationReady = false
    filterSearchStarted = false
    bumpFilter()
  }

  function setFilter(value: string): void {
    setFilterSignal(value)
    if (filterTimer !== null) {
      clearTimeout(filterTimer)
      filterTimer = null
    }
    if (value.trim() === '') {
      discardFilterProjection()
      return
    }
    if (filterSearchStarted) return
    if (eligibleFilterDirs().length === 0) {
      // No request can be issued for a fully loaded tree, so the answer is
      // settled without waiting for the request-avoidance debounce.
      filterActivationReady = true
      maybeStartFilterSearch()
      return
    }
    filterActivationReady = false
    filterTimer = setTimeout(() => {
      filterTimer = null
      filterActivationReady = true
      maybeStartFilterSearch()
    }, FILTER_ACTIVATION_DEBOUNCE)
  }

  function filterSettled(): boolean {
    filterVersion()
    if (filter().trim() === '' || !filterActivationReady) return false
    if (!filterSearchStarted && eligibleFilterDirs().length > 0) return false
    if (filterQueueRunning || filterQueue.length > 0 || refreshing || revealing) return false
    return eligibleFilterDirs().every((dir) => validFilterProjection(dir) !== null)
  }

  /** The response's request is still the view's scope: a live origin and —
   *  once a binding exists — the same binding. That is the whole membership
   *  test in this store (ALX-1031): the generation orders responses per
   *  directory (rule 2, `appliedGeneration`) and `refresh()` raises it to do
   *  so, so it cannot also say which instance a read belongs to. The two
   *  roles are deliberately kept apart, and a paneId is not consulted at all:
   *  a diff or viewer tab of the same session is the same tree. */
  function scopeCurrent(ctx: ListCtx): boolean {
    if (closed) return false
    if (untrack(origin) === null) return false
    const b = untrack(binding)
    if (ctx.bindingId !== null && (b === null || ctx.bindingId !== b.bindingId)) return false
    return true
  }

  /** An files.open response applies only if it is still the newest scope
   *  request: the open that establishes a binding wins by generation. Same
   *  untracked read as scopeCurrent — a one-shot guard, never a reactive
   *  derivation. */
  function openCurrent(ctx: { generation: number }): boolean {
    if (closed) return false
    return untrack(origin) !== null && ctx.generation === generation
  }

  function messageOf(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  function entryToNode(e: FilesListEntry): FilesNode {
    return {
      ...emptyListing(),
      name: e.name,
      path: e.path,
      kind: e.kind,
      ...(e.linkTarget !== undefined ? { linkTarget: e.linkTarget } : {}),
      ...(e.linkKind !== undefined ? { linkKind: e.linkKind } : {}),
      size: e.size,
      modTime: e.modTime,
      mode: e.mode,
      expanded: false,
      cyclic: false,
      children: [],
    }
  }

  /** Merge a fresh listing page into the existing children, preserving node
   *  identity for rows that persist: a refresh re-list of a parent must not
   *  orphan the in-flight re-list of an expanded child (its response would
   *  apply to a node the tree no longer holds and the tree would collapse).
   *  Wire facts (name, kind, size, mtime, mode, link) are refreshed from the
   *  new listing; tree state (expanded, children, canonical, cyclic) stays
   *  with the surviving node. Rows the new listing no longer contains are
   *  dropped. */
  function mergeChildren(oldChildren: FilesNode[], entries: FilesListEntry[]): FilesNode[] {
    const byPath = new Map(oldChildren.map((c) => [c.path, c]))
    return entries.map((e) => {
      const existing = byPath.get(e.path)
      if (existing === undefined) return entryToNode(e)
      existing.name = e.name
      existing.kind = e.kind
      if (e.linkTarget !== undefined) existing.linkTarget = e.linkTarget
      if (e.linkKind !== undefined) existing.linkKind = e.linkKind
      existing.size = e.size
      existing.modTime = e.modTime
      existing.mode = e.mode
      return existing
    })
  }
  /** The canonicals of the node's expanded ancestors, root first — the D9
   *  chain. Only ancestors can be listed before the node (a row is visible
   *  only under expanded directories), so a canonical match against this
   *  chain is exactly "resolves back into an already-expanded ancestor".
   *  The node itself is never part of the chain: its own canonical must not
   *  trip the check on a refresh re-list. The root read is untracked like
   *  the other response-time snapshots — this runs inside applyListing,
   *  never during a render. */
  function ancestorCanonicals(node: FilesNode): (string | null)[] {
    const r = untrack(root)
    if (r === null) return []
    const chain: (string | null)[] = [r.canonical]
    let dir: FilesRoot | FilesNode = r
    for (;;) {
      if (dir === node) break
      let child: FilesNode | undefined
      for (const candidate of dir.children) {
        if (
          candidate === node ||
          node.path === candidate.path ||
          node.path.startsWith(candidate.path + '/')
        ) {
          child = candidate
          break
        }
      }
      if (child === undefined || child === node) break
      chain.push(child.canonical)
      dir = child
    }
    return chain
  }

  function isCyclic(node: FilesNode, canonical: string): boolean {
    return canonical !== '' && ancestorCanonicals(node).includes(canonical)
  }

  /** Apply one listing, then reconcile the watch set. The wrapper is what
   *  makes the reconciliation unconditional: a directory joins the set the
   *  moment its first listing succeeds, and that moment is one of several
   *  early returns below. syncWatchSet is a no-op unless the set drifted, so
   *  paying it on every exit costs nothing and cannot be forgotten by a
   *  branch added later. */
  function applyListing(dir: FilesRoot | FilesNode, ctx: ListCtx, res: FilesListResult): void {
    applyListingState(dir, ctx, res)
    void syncWatchSet()
    maybeStartFilterSearch()
  }

  function applyListingState(dir: FilesRoot | FilesNode, ctx: ListCtx, res: FilesListResult): void {
    if (!scopeCurrent(ctx)) return
    if (ctx.generation < dir.appliedGeneration) {
      dir.busy = false
      bumpTree()
      return
    }
    // THE HELD BINDING ANSWERED: whatever the answer is, the resume's question
    // ("is this binding still known to the backend?") is settled, and a later
    // failure in some subdirectory is about that subdirectory.
    if (dir === untrack(root)) resumeUnread = false
    if (res.state === 'ok') {
      // Rule 3: the cycle check runs BEFORE the children are committed, so
      // a cyclic expansion never flashes a listing.
      if ('kind' in dir && isCyclic(dir, res.canonical)) {
        dir.cyclic = true
        dir.expanded = false
        dir.busy = false
        dir.state = null
        dir.children = []
        dir.hasMore = false
        bumpTree()
        return
      }
      dir.state = 'ok'
      dir.canonical = res.canonical
      dir.rev = res.rev
      dir.total = res.total
      dir.children = mergeChildren(dir.children, res.entries)
      dir.hasMore = res.hasMore
      dir.nextOffset = res.offset + res.entries.length
      dir.error = null
      dir.appliedGeneration = ctx.generation
      dir.busy = false
      bumpTree()
      return
    }
    if (res.state === 'tooLarge') {
      // D14: a real state — "more than N entries" — with no pagination
      // offered. The header refresh stays the manual retry.
      dir.state = 'tooLarge'
      dir.tooLargeLimit = res.limit
      dir.observedCount = res.observedCount ?? null
      dir.children = []
      dir.hasMore = false
      dir.canonical = null
      dir.appliedGeneration = ctx.generation
      dir.busy = false
      bumpTree()
      return
    }
    dir.state = 'timedOut'
    dir.timeout = res.timeout
    dir.children = []
    dir.hasMore = false
    dir.canonical = null
    dir.appliedGeneration = ctx.generation
    dir.busy = false
    bumpTree()
  }

  function applyListError(dir: FilesRoot | FilesNode, ctx: ListCtx, e: unknown): void {
    applyListErrorState(dir, ctx, e)
    // A directory that leaves 'ok' leaves the set too.
    void syncWatchSet()
    maybeStartFilterSearch()
  }

  function applyListErrorState(dir: FilesRoot | FilesNode, ctx: ListCtx, e: unknown): void {
    if (!scopeCurrent(ctx)) return
    // THE FIRST READ AFTER A HOLD FAILED: try one fresh binding before
    // painting the failure (see reopenDeadScope). The question is asked as a
    // state this store owns — was this the resumed tree's OWN ROOT, read for
    // the first time since the axis came back? — and never by reading the
    // refusal's words: `files.*` carries no typed reason for a dead binding
    // (unlike `git.*`'s `unknown-binding`), and a sentence is not a fact
    // (`nocx/no-error-text-match`). Asked once per resume: the reopened
    // scope's answer is the answer.
    if (resumeUnread && dir === untrack(root)) {
      const o = untrack(origin)
      if (o !== null) {
        resumeUnread = false
        reopenDeadScope(o)
        return
      }
    }
    if (ctx.generation < dir.appliedGeneration) {
      dir.busy = false
      bumpTree()
      return
    }
    // Permission denied is a rendered node state, never a silently empty
    // directory (AGENTS.md): the row renders disabled and this message is
    // the state row beneath it.
    dir.state = 'error'
    dir.error = messageOf(e)
    dir.children = []
    dir.hasMore = false
    dir.appliedGeneration = ctx.generation
    dir.busy = false
    bumpTree()
  }

  /** Snapshot the current scope for a request, untracked like the guards:
   *  the request must carry the scope that ISSUED it, and the caller asks
   *  once, at call time. */
  function captureCtx(): ListCtx | null {
    const o = untrack(origin)
    const b = untrack(binding)
    if (o === null || b === null) return null
    return { generation, bindingId: b.bindingId }
  }

  /** The last (binding, visibility) pair the backend was actually told, so
   *  a re-render cannot re-send what it already knows and a NEW binding
   *  re-sends even when the flag itself did not move.
   *
   *  Cleared on failure rather than left standing: a refused "I am hidden"
   *  would otherwise be remembered as sent, and the poll it was meant to
   *  stop would run until the next transition. */
  let publishedVisible: { bindingId: string; visible: boolean } | null = null

  /** Tell the backend whether the panel showing this tree is on screen. A
   *  refused signal is not worth a message in the panel — the next
   *  transition retries, and the cost of losing one is a poll that keeps
   *  running. */
  function publishVisibility(visible: boolean): void {
    const ctx = captureCtx()
    if (ctx === null) return
    const bindingId = ctx.bindingId as string
    if (
      publishedVisible !== null &&
      publishedVisible.bindingId === bindingId &&
      publishedVisible.visible === visible
    ) {
      return
    }
    publishedVisible = { bindingId, visible }
    void services.visible(bindingId, visible, 'background').catch(() => {
      if (
        publishedVisible !== null &&
        publishedVisible.bindingId === bindingId &&
        publishedVisible.visible === visible
      ) {
        publishedVisible = null
      }
    })
  }

  /**
   * Whether the panel showing this tree is on screen — and the switch that
   * makes the store do any work at all (ADR-8eej3e66 §9).
   *
   * The panel is MOUNTED while the sidebar is collapsed, and it is mounted
   * again whenever another view is not in front, so "mounted" has never
   * meant "looked at". Until this is true the store opens no binding,
   * lists nothing, watches nothing and walks no reveal: a tab switch or a
   * `cd` updates the origin it would open at and stops there. That is what
   * a closed panel costs, which is nothing.
   *
   * On the way in, the work starts: a binding that is gone (or was never
   * made) is opened for the origin recorded while hidden, a failed open is
   * asked again, and a watch that was refused in the background is
   * re-sent. On the way out, a reveal in flight is stopped and the backend
   * is told to stop its poll — the binding survives, because closing and
   * re-opening it on every glance at another view is the churn this flag
   * exists to avoid.
   */
  function setVisible(visible: boolean): void {
    const was = panelVisible
    panelVisible = visible
    if (!visible) {
      if (was) {
        // A walk nobody can see is work for nothing, and its pending
        // promise belongs to whatever asked for it — settle it.
        revealing = false
        revealWalkId++
        cancelReveal()
      }
      if (binding() !== null) publishVisibility(false)
      return
    }
    if (was && binding() !== null) return
    ensureScope()
  }

  /** Start (or resume) the scope's work now that the panel is on screen. */
  function ensureScope(): void {
    if (!panelVisible) return
    const o = untrack(origin)
    if (o === null) return
    // NOTHING IS ASKED OF A TAB THE HELPER DOES NOT SERVE (design §5.10,
    // C25). The card is the panel's whole answer there, and its action is the
    // remedy; opening a binding behind it would spend an RPC on a host the
    // contract has already said cannot be reached.
    if (helperGate(o) !== null) return
    const b = untrack(binding)
    if (b === null) {
      if (phase() === 'opening') return
      // A fresh open, or the re-ask of one refused while hidden. Both are
      // the same call: nothing on screen had a tree, and now something does.
      // The tree is carried: a tab that opened straight into `starting`/`blocked`
      // has none, and a hold that began before the binding existed may have kept
      // whatever the person had before it (design §5.10, C25).
      treeNeedsResume = false
      openScope(o, untrack(root))
      return
    }
    if (openedSession !== o.sessionId) {
      // The tab moved to another machine while nobody was looking. The
      // binding cannot serve the tree now wanted, so it is swapped the same
      // way a visible rescope swaps it.
      treeNeedsResume = false
      restartScope(o)
      return
    }
    // THE AXIS CAME BACK WHILE NOBODY WAS LOOKING (M1). Nothing was asked then
    // — a hidden panel does no work — so the re-read happens NOW, on the
    // binding the pause kept, with the open folders still in the tree.
    if (treeNeedsResume) {
      resumeScope(o)
      return
    }
    publishVisibility(true)
    // Background work refused while hidden is asked again by its natural
    // trigger, and this is it: the panel is being looked at.
    if (watchFailed() !== null || publishedWatchPaths === null) {
      forgetPublishedWatchSet()
      void syncWatchSet()
    }
  }

  /** Claim a directory's busy flag for one request, and answer the settle
   *  that request owes it (nocx-4y82c.1.6).
   *
   *  THE FLAG AND THE REQUEST ARE ONE FACT. It used to be a bare boolean
   *  cleared by whichever apply path happened to own the answer — and every
   *  one of those paths begins by dropping answers that belong to a scope the
   *  store has left. The tree is right to be protected from those answers;
   *  the busy flag is not, because nothing else will ever clear it: the row
   *  spun for as long as the panel lived, which is the one thing ADR-8eej3e66
   *  forbids ("a person never sees busy"). Measured on CI:
   *  [data-testid=files-row][data-path$=/churn/d08] .ui-tree-row kept
   *  data-busy="true" for the whole 30 s bound under the 50,000-entry churn.
   *
   *  So the request carries a sequence number, `settle` clears the flag iff
   *  no later request has taken it over (a stale answer must not un-busy a
   *  row a newer listing is still filling), and every settle path calls it
   *  BEFORE any scope or generation check. */
  function claimBusy(dir: FilesRoot | FilesNode): () => void {
    const seq = ++busySeq
    dir.busy = true
    dir.busySeq = seq
    bumpTree()
    return () => {
      if (dir.busySeq !== seq) return
      dir.busy = false
      bumpTree()
    }
  }

  function issueList(
    dir: FilesRoot | FilesNode,
    offset: number,
    limit: number,
    ctx: ListCtx,
    origin: CallOrigin = 'person',
  ): Promise<void> {
    const settle = claimBusy(dir)
    return services.list(ctx.bindingId as string, dir.path, offset, limit, origin).then(
      (res) =>
        untrack(() => {
          settle()
          applyListing(dir, ctx, res)
          resumePendingPage(dir)
        }),
      (e) =>
        untrack(() => {
          settle()
          applyListError(dir, ctx, e)
          resumePendingPage(dir)
        }),
    )
  }

  /** How wide a re-list of an already-displayed directory asks (nocx-o97xj).
   *  The single owner of that rule: both the change-driven refreshDir and the
   *  header's whole-tree refresh() call it, because two copies of one window
   *  rule drifting is how this defect gets a second life.
   *
   *  A change notification names a directory and carries no diff (§5.5), so
   *  the width of the re-list decides what the panel is able to see at all.
   *  Asking for exactly the count on screen — which is what this did — cannot
   *  see an entry that sorts past the last displayed row, so a file arriving
   *  in a directory the user is looking at stayed invisible behind a
   *  "Show next 1" button, which is the opposite of the promise D5 makes.
   *
   *  The rule turns on whether the user has paged:
   *
   *  - Fully displayed (`hasMore` false) — the window IS the directory, so it
   *    must have room to grow: one page of headroom above what is displayed.
   *    This is a fixed point rather than a ratchet; the window follows the
   *    directory's real size and stops there.
   *  - Paged into (`hasMore` true) — exactly the displayed count. That window
   *    is the user's: a refresh must not shrink it back to page one, and must
   *    not page them deeper into a directory they never asked to see more of.
   *    Arrivals that sort inside the window still appear, and the tail is
   *    already represented honestly by "Show next N", whose count is `total`
   *    and so is right after every re-list.
   *
   *  Why one page of headroom and not one row: a change event is a coalesced
   *  invalidation hint (§5.5), so one notification can stand for a burst — a
   *  checkout, an unpacked archive, an agent writing a batch — and headroom
   *  of one would surface one of them. Why not the whole directory: both
   *  providers enumerate, sort and cap the entire directory before slicing
   *  the page (internal/filesystem/{local,sftp}), so a wider limit buys wire
   *  bytes and merged rows, never round trips — but handing back everything
   *  would override the paging budget the user is already living under.
   *  FILES_PAGE_SIZE is the increment they have already accepted here; a
   *  separate number would be a second rule to keep in step.
   *
   *  A capped directory never reaches this: onFilesChanged requires state
   *  'ok', and the backend does not poll a tooLarge directory at all. */
  function refreshLimit(dir: FilesRoot | FilesNode): number {
    const displayed = dir.children.length
    if (displayed === 0) return FILES_PAGE_SIZE
    return dir.hasMore ? displayed : displayed + FILES_PAGE_SIZE
  }

  /** The §5.1 refresh form: re-list at offset 0 over the displayed window,
   *  replacing every displayed row atomically. With no explicit ctx this
   *  starts a new cycle (it supersedes in-flight page requests). */
  function refreshDir(dir: FilesRoot | FilesNode, ctx?: ListCtx): void {
    const next = ctx ?? captureCtx()
    if (next === null) return
    void issueList(dir, 0, refreshLimit(dir), next, 'background')
  }

  /** Send the watch set the panel currently renders — files.watch REPLACES
   *  the set rather than adding to it. Callers that are about to issue
   *  another filesystem operation await this promise: files.watch takes a
   *  synchronous baseline, so overlapping it with files.list would make the
   *  panel contend with itself for the capacity-one filesystem gate.
   *
   *  Called after a directory's first successful listing, when a loaded
   *  directory expands or collapses, on reconnect, and after refresh() —
   *  the Retry for a failed watch. The response carries the refresh mode
   *  (§5.5); a rejection becomes the sticky inline message. */
  function pushWatchSet(): Promise<void> {
    const ctx = captureCtx()
    const r = untrack(root)
    if (!panelVisible || ctx === null || r === null) return Promise.resolve()
    // Recorded at the point the paths are read, and NOT rolled back on
    // failure: a rejected watch is a sticky failure the user retries through
    // the header refresh (§5.5), so re-sending the identical set on the next
    // listing would erase the message it is supposed to leave up.
    const paths = currentWatchPaths(r)
    publishedWatchPaths = paths
    watchInFlight = services.watch(ctx.bindingId as string, paths, 'background').then(
      (res) => {
        if (!scopeCurrent(ctx)) return
        setWatchFailed(null)
        setWatchMode(res.mode)
        setWatchDegradedReason(res.degradedReason ?? null)
      },
      (e) => {
        if (!scopeCurrent(ctx)) return
        setWatchFailed(messageOf(e))
      },
    )
    return watchInFlight
  }

  /** Publish the watch set iff what the panel renders has drifted from what
   *  the backend was last told. Every seam that can change the set calls
   *  this and nothing calls pushWatchSet directly, so a set cannot be sent
   *  twice for one change and cannot be missed by a path that forgot.
   *
   *  Suppressed while a reveal walk is in flight, which is what keeps a deep
   *  reveal linear: files.watch takes its own baseline listing of every path
   *  in the set, so publishing per level would baseline the growing path N
   *  times over. The walk's end calls this once. */
  function syncWatchSet(): Promise<void> {
    if (!panelVisible || revealing) return watchInFlight
    const r = untrack(root)
    if (r === null) return watchInFlight
    const want = currentWatchPaths(r)
    if (
      publishedWatchPaths !== null &&
      publishedWatchPaths.length === want.length &&
      publishedWatchPaths.every((path, i) => path === want[i])
    ) {
      return watchInFlight
    }
    return pushWatchSet()
  }

  /** Force the next sync to publish: the backend's set is gone (reconnect) or
   *  the user asked for it back (the refresh cycle is the Retry for a failed
   *  watch), and in both cases an unchanged set must still be re-sent. */
  function forgetPublishedWatchSet(): void {
    publishedWatchPaths = null
  }

  /** The server-initiated invalidation (SettingsObserver pattern). The
   *  notification names one dirty path and carries no entries — re-listing
   *  through refreshDir keeps exactly one code path rendering a directory.
   *  Three filters, each for a different defect: a change for a binding
   *  this tree does not follow is not this tree's business (a viewer's
   *  binding, or a previous scope's); a directory whose rows are not
   *  rendered (tooLarge/timedOut/error) keeps its state row until the user
   *  retries; and a rev that already matches what is applied means the
   *  change is already on screen. A busy directory is skipped too — an
   *  enumeration is already on the wire and its response is newer than
   *  this notification's knowledge. */
  function onFilesChanged(p: FilesChanged): void {
    // A reveal walk is paging: a refresh here would re-list at the walk's
    // first-page count and, landing late, replace the rows the walk has
    // since accumulated — the reveal would collapse out from under the
    // user (the files.changed-vs-pagination race, nocx-r3bz). The walk's
    // own pages are the freshest data; the next poll re-validates.
    if (revealing) return
    const b = untrack(binding)
    if (b === null || p.bindingId !== b.bindingId) return
    const r = untrack(root)
    if (r === null) return
    const ctx = captureCtx()
    if (ctx === null) return
    let dir: FilesRoot | FilesNode | null = r.path === p.path ? r : null
    if (dir === null) {
      const find = (d: FilesRoot | FilesNode): void => {
        for (const child of d.children) {
          if (child.path === p.path) {
            dir = child
            return
          }
          if (child.expanded) find(child)
        }
      }
      find(r)
    }
    if (dir === null || dir.state !== 'ok' || dir.busy) return
    if (p.rev !== undefined && p.rev === dir.rev) return
    // A change invalidates the presentation snapshot immediately. The
    // committed refresh below is the only source allowed to rebuild it, so a
    // late projection response cannot briefly show entries from the old rev.
    if (filterProjections.delete(dir.path)) bumpFilter()
    refreshDir(dir, ctx)
  }

  function onFilesRefreshStateChanged(p: FilesRefreshStateChanged): void {
    const b = untrack(binding)
    if (b === null || p.bindingId !== b.bindingId) return
    if (p.state !== 'ok' && p.state !== 'delayed') return
    setRefreshState(p.state)
  }

  /** Open the scope's binding and start its first listing.
   *
   *  `carried` is the TREE THE AXIS PAUSED, and it is not an optimisation:
   *  a person who had four folders open on an SSH host and lost the
   *  connection gets those four folders back, with the same rows, when the
   *  helper returns (design §5.10, C25 — "expanded folders kept"). The open
   *  still re-reads: the carried root keeps its `expanded` nodes, the reveal
   *  target and the selection, and the refresh cycle below replaces their
   *  listings with what the host holds now.
   *
   *  A carried tree whose root moved (a provider that answers a different
   *  root) is discarded rather than merged: the open's own root is the truth,
   *  and hanging one machine's rows under another's root is the defect this
   *  branch must not have. */
  function openScope(o: ActiveOrigin, carried: FilesRoot | null = null): void {
    generation++
    treeNeedsResume = false
    resumeUnread = false
    discardFilterProjection()
    if (filter().trim() !== '') setFilter(filter())
    // A replacement can open with a tree while the phase still says there is
    // no origin. Record the open before another observation can restart it.
    if (carried === null || untrack(phase) === 'no-origin') setPhase('opening')
    setRefreshState(null)
    setOpenError(null)
    // A fresh binding has no watch state yet: the badge and the sticky
    // message wait for this binding's first files.watch response.
    setWatchMode(null)
    setWatchDegradedReason(null)
    setWatchFailed(null)
    // A fresh scope starts with nothing revealed — a selection from a
    // previous machine must not linger — and supersedes any walk still
    // in flight from the previous scope: its responses must not paint,
    // and its pending marker must not suppress this scope's reveal of
    // the same path.
    if (carried === null) {
      setRevealTarget(null)
      pendingReveal = null
      cancelReveal()
      revealing = false
      revealWalkId++
    }
    // The root is the FILESYSTEM ROOT, pinned here and never derived from
    // the cwd: a verified OSC 7 cwd must not re-root the tree (it REVEALS
    // instead), and the provider's fallback machinery is not reachable
    // from the panel — the panel is a file manager from / whether or not
    // the shell reports where it is.
    const ctx = { generation }
    openedSession = o.sessionId
    const opening = services.open(o.sessionId, '/', 'background')
    opening
      .then((res) => {
        if (!openCurrent(ctx)) {
          // Stale: a newer scope superseded this open. A SUCCESSFUL open has
          // already registered a live binding on the backend — dropping the
          // answer without closing it leaks that binding for as long as the
          // session lives (ALX-1031). Nothing else about it is applied.
          void services.close(res.bindingId, 'background').catch(() => {})
          return
        }
        setBinding({
          bindingId: res.bindingId,
          endpointId: res.endpointId,
          revealAvailable: res.revealAvailable,
        })
        // THE PAUSED TREE COMES BACK AS IT WAS. Every open folder keeps its
        // node and its rows, and the refresh cycle below re-reads them from
        // the host that has just answered again.
        if (carried !== null && carried.path === res.root.path) {
          carried.display = res.root.display
          carried.inferred = res.root.inferred
          carried.inferredReason = res.root.inferredReason
          carried.busy = false
          setRoot(carried)
          setPhase('ready')
          bumpTree()
          // The one refresh cycle (root and every expanded directory, then
          // the watch set) — the same code the header's action runs, so a
          // resumed tree and a refreshed one cannot diverge.
          refresh()
          return
        }
        setRoot({
          ...emptyListing(),
          path: res.root.path,
          display: res.root.display,
          inferred: res.root.inferred,
          inferredReason: res.root.inferredReason,
          children: [],
        })
        setPhase('ready')
        // One-shot reads at response time, untracked for the same reason as
        // the guards: the first list is issued against the state that just
        // landed, never re-run for a state that replaced it. The initial
        // watch follows that list and settles before reveal starts: all
        // three consume the same capacity-one filesystem admission, so
        // launching any pair together can make the panel refuse itself.
        const b = untrack(binding)
        const r = untrack(root)
        if (b !== null && r !== null) {
          const listCtx: ListCtx = { generation, bindingId: b.bindingId }
          void issueList(r, 0, FILES_PAGE_SIZE, listCtx, 'background').then(async () => {
            if (!scopeCurrent(listCtx)) {
              cancelReveal()
              return
            }
            if (r.state !== 'ok') {
              cancelReveal()
              return
            }
            await syncWatchSet()
            if (!scopeCurrent(listCtx)) {
              cancelReveal()
              return
            }
            if (pendingReveal !== null) {
              startQueuedReveal()
              return
            }
            // Reveal-on-open: land where the terminal is. Read the LIVE
            // origin — the cwd may have moved while open/list/watch were in
            // flight, and the walk reads the committed root.
            const live = untrack(origin)
            if (
              panelVisible &&
              live !== null &&
              live.cwdVerified &&
              live.cwd !== null &&
              live.cwdFollow
            ) {
              void revealPath(live.cwd)
            }
          })
        }
      })
      .catch((e) => {
        if (!openCurrent(ctx)) return
        pendingReveal = null
        cancelReveal()
        openedSession = null
        setPhase('failed')
        // The panel names what failed and keeps the Retry — the header
        // refresh re-asks (phase 'failed' with no binding). A failure here
        // is a named local failure, never a sentence about the terminal
        // being busy (ADR-8eej3e66 §8).
        setOpenError(messageOf(e))
      })
  }

  /** THE ONE SCOPE PREDICATE (ALX-1043): two origins are the same tree when
   *  they name the same session and kind and this store still holds that
   *  scope. The cwd only reveals and the paneId is a view, so neither is part
   *  of it (ALX-1031). Both `rescope` branches — hidden and visible — decide
   *  the transition by this predicate and by nothing else; a second notion of
   *  "same scope" is how a hidden panel recorded a new origin without
   *  releasing the instance that answered for the old one. */
  function sameScope(prev: ActiveOrigin | null, next: ActiveOrigin | null): boolean {
    // A hidden replacement can already own the carried tree. Its open is
    // deferred until the panel is shown, so the phase still reads 'no-origin'.
    return (
      prev !== null &&
      next !== null &&
      prev.sessionId === next.sessionId &&
      prev.kind === next.kind &&
      (untrack(phase) !== 'no-origin' || untrack(root) !== null)
    )
  }

  // A FROZEN ORIGIN HAS NO OPINION ABOUT THE CONNECTION, AND MUST NOT CLEAR
  // THE ONE THE TAB WAS OPENED ON (design §5.4/§5.10). A viewer or diff tab
  // answers the origin it was opened from with `cwdFollow: false` and no
  // `connection` — and `helperGate` reads a missing observation as
  // `starting`. Taken at face value that turned activating the viewer into
  // "the helper is starting": the held binding was released and the viewer's
  // own liveness died on the spot. The pane manager attaches the LIVE
  // connection of the terminal this tab belongs to; this is the second end of
  // the same rule, for an answer that reached here without one (a caller that
  // built the origin itself). The axis of the tab that HAS a connection is
  // the authority; a frozen origin only carries it.
  function carryConnection(prev: ActiveOrigin | null, next: ActiveOrigin): ActiveOrigin {
    if (next.cwdFollow || next.connection !== undefined) return next
    if (prev === null || prev.sessionId !== next.sessionId) return next
    return { ...next, connection: prev.connection }
  }

  function sameSshPane(prev: ActiveOrigin, next: ActiveOrigin): boolean {
    return (
      prev.kind === 'ssh' &&
      next.kind === 'ssh' &&
      prev.cwdFollow &&
      next.cwdFollow &&
      prev.paneId === next.paneId
    )
  }

  function carryTreeForReplacement(
    prev: ActiveOrigin | null,
    next: ActiveOrigin | null,
  ): FilesRoot | null {
    const tree = untrack(root)
    if (
      prev?.kind === 'ssh' &&
      prev.cwdFollow &&
      prev.connection?.connectionId &&
      tree !== null &&
      (next === null || (sameSshPane(prev, next) && prev.sessionId !== next.sessionId))
    ) {
      interruptedScope = { origin: prev, root: tree }
    }
    const held = interruptedScope
    if (held === null || next === null) return null
    // The replacement can announce its session before it has a connection
    // observation. Its serving observation supplies the identity to compare.
    if (sameSshPane(held.origin, next) && helperGate(next) !== null) return null
    interruptedScope = null
    return sameSshPane(held.origin, next) &&
      held.origin.sessionId !== next.sessionId &&
      held.origin.connection?.connectionId === next.connection?.connectionId
      ? held.root
      : null
  }

  function rescope(raw: ActiveOrigin | null): void {
    const prev = origin()
    const next = raw === null ? null : carryConnection(prev, raw)
    const carried = carryTreeForReplacement(prev, next)
    // THE AXIS IS THE FIRST DISCRIMINATOR (design §5.10, C25/C26), and it is
    // answered before the visibility switch because it is true of a hidden
    // panel too: an SSH tab the helper does not serve has nothing to ask, so
    // the live instance of the scope ends — while the TREE stays, which is
    // what gives the person their open folders back when the axis returns.
    if (next !== null && helperGate(next) !== null) {
      if (sameScope(prev, next) && prev !== null) {
        // A newer observation of a tab already held: enter the hold the first
        // time it stops serving, and do nothing at all afterwards (a
        // heartbeat must neither close a binding the pause keeps nor reset a
        // tree that is waiting).
        if (helperGate(prev) === null) holdScope()
        setOrigin(next)
        return
      }
      dropScope(next)
      return
    }
    // NOTHING ON SCREEN DOES NO WORK. The origin is recorded — it is where
    // the panel would open — and no binding, listing, watch or reveal is
    // started for it. `setVisible(true)` starts the scope from this record
    // (ensureScope), which is why a tab switch with the sidebar collapsed
    // costs nothing at all.
    if (!panelVisible) {
      // Not disposed by this: the store is live and recording where it will
      // open; it simply has no reason to open yet.
      closed = false
      if (next === null) {
        setOrigin(null)
        releaseScope()
        return
      }
      // A DIFFERENT scope is still a transition while nothing is on screen.
      // An answer for the old one — a `files.open` already in flight — must
      // not land under the new origin and list or watch the machine the
      // person has left. Nothing is opened here: the generation is raised
      // and the binding released by `releaseScope`, and `setVisible` starts
      // the recorded scope through `ensureScope`.
      if (sameScope(prev, next)) {
        // THE AXIS CAME BACK WHILE NOBODY WAS LOOKING: record the debt, do
        // not pay it. `setVisible(true)` re-reads the carried tree over the
        // binding the pause kept, because a hidden panel that spent a
        // `files.open` would be exactly the background work this flag exists
        // to forbid.
        if (prev !== null && helperGate(prev) !== null) treeNeedsResume = true
        if (carried !== null) setRoot(carried)
        setOrigin(next)
        return
      }
      releaseScope()
      setOrigin(next)
      if (carried !== null) setRoot(carried)
      return
    }
    // Rule 1: the same scope keeps its binding and its tree, whatever its
    // phase — in flight, ready or failed. A later OSC 7 report must not
    // re-root the tree, a repeated origin after a failed files.open must not
    // retry behind the person's back (the panel's Retry is the retry), and
    // neither may a viewer tab or a diff tab that answers the origin it was
    // opened from (design §5.4): the viewer is the same machine with a
    // different paneId, and re-opening there would close the very binding
    // the viewer is reading through. Only a different session, a different
    // kind, or a dead scope (dispose, or an origin that went null and came
    // back) re-opens — `restartScope` is the one open, and `refresh()` is
    // its explicit caller.
    if (sameScope(prev, next) && prev !== null && next !== null) {
      // The stored origin stays CURRENT even though the scope does not
      // re-open: openFile hands it to the viewer, and the reveal check
      // reads the live cwd. A stale cwd here would reach the viewer.
      setOrigin(next)
      // THE AXIS CAME BACK. Nobody pressed anything: the binding is opened
      // again and the paused tree re-read, its open folders kept (§5.10,
      // C25). A newer serving observation after that is not a new permission
      // to open — the gate above answers that case by leaving the tab alone.
      if (helperGate(prev) !== null) {
        if (carried !== null) setRoot(carried)
        resumeScope(next)
        return
      }
      // Same scope: the tree does not move, but a VERIFIED cwd CHANGE on the
      // origin the panel follows REVEALS — the chain from the root down to
      // the new cwd is expanded and the target selected. This is the product
      // rule: the terminal owns "where am I", and the panel, rooted at /,
      // follows by revealing, never by re-rooting. The (cwd, verified) pair
      // that did NOT move issues nothing: the wake that republished the
      // origin is not a reason to walk the tree again (ALX-1019). An
      // unverified or absent cwd reveals nothing (AD-5: no silent $HOME
      // guess); an origin with no opinion — a viewer tab's frozen origin —
      // reveals nothing either: it is "stay exactly as you are".
      if (
        (next.cwd !== prev.cwd || next.cwdVerified !== prev.cwdVerified) &&
        next.cwdVerified &&
        next.cwd !== null &&
        next.cwdFollow
      ) {
        void revealPath(next.cwd)
      }
      return
    }
    restartScope(next, carried)
  }

  /** End the live INSTANCE of the current scope and KEEP THE TREE AND THE
   *  BINDING: the axis stopped serving, and everything the person opened is
   *  what it returns to.
   *
   *  THE BINDING STAYS, and that is the fix for a real defect (ALX-1793 round
   *  1, M2). The preceding version released it here, on the theory that a
   *  paused connection's binding is a handle onto a host that cannot be
   *  reached. On the backend it is not: a `helperfs` binding is pinned to the
   *  connection, and the connection survives a pause, a cut and a helper
   *  redial — so the binding was never dead, and closing it killed every
   *  viewer tab of that host for good (the close is what turns a binding
   *  `liveness: false`, and a viewer tab cannot get it back).
   *
   *  What the hold does end is the live OUTPUT: the generation bump
   *  supersedes answers already on the wire, the reveal walk is cancelled, and
   *  the transient badges are cleared because their subject — a watch, a
   *  freshness state — belonged to a connection that just went away.
   *  `dropScope` still closes the binding, because there the tree belongs to
   *  another machine. */
  function holdScope(): void {
    closed = true
    generation++
    resumeUnread = false
    // The backend was told this binding is visible; the next visible edge must
    // tell it again rather than trust a statement made across a break.
    publishedVisible = null
    if (phase() === 'opening') setPhase(untrack(root) === null ? 'no-origin' : 'ready')
    setOpenError(null)
    setWatchMode(null)
    setWatchDegradedReason(null)
    setWatchFailed(null)
    setRefreshState(null)
    publishedWatchPaths = null
    pendingReveal = null
    revealing = false
    cancelReveal()
    revealWalkId++
    bumpTree()
  }

  /** Reset the binding and the visible tree while the new axis is blocked. */
  function dropScope(next: ActiveOrigin | null): void {
    holdScope()
    const prevBinding = binding()
    if (prevBinding !== null) {
      void services.close(prevBinding.bindingId, 'background').catch(() => {})
      publishedVisible = null
    }
    openedSession = null
    setBinding(null)
    discardFilterProjection()
    setRoot(null)
    setRevealTarget(null)
    setOrigin(next)
    setPhase(next === null ? 'no-origin' : 'ready')
    bumpTree()
  }

  /** The axis serves again: re-read the held tree, its open folders kept, and
   *  open a binding only where the hold has none (a tab that opened straight
   *  into `starting`/`blocked` never had one).
   *
   *  A REFRESH, NOT AN OPEN, is the answer for a held binding (ALX-1793 round
   *  1, M2): the backend never dropped it, so re-opening would mint a second
   *  handle, orphan the first and hand every viewer tab of this host a
   *  binding nobody uses. The whole tree is re-read through the same refresh
   *  cycle the header's action runs, so a resumed tree and a refreshed one
   *  cannot diverge — and a folder the person opened comes back with the rows
   *  the host holds now (§5.10, C25). */
  function resumeScope(o: ActiveOrigin): void {
    treeNeedsResume = false
    closed = false
    setOrigin(o)
    if (untrack(binding) === null) {
      openScope(o, untrack(root))
      return
    }
    // THE HOST IS TOLD THE PANEL IS ON SCREEN AGAIN (ALX-1793 round 2, M-r2).
    // The hold dropped the last statement deliberately — it was made across a
    // break — and `files.watch` over a watcher that already exists moves no
    // visibility, so a resume that only re-read the tree left this binding's
    // observer at `visible: false`: an hour-long timer, no poll, and a file
    // arriving on the host invisible in a panel that looked healthy
    // (`refreshState: ok`, so not even the delayed badge). The published
    // visible edge is what restarts the poll.
    //
    // Published here and nowhere else: a binding opened by `openScope` starts
    // visible on the backend, and this is the ONE path that returns to a
    // binding the pause kept. The gate is checked once more because the panel
    // may be shown while the axis is still down — the card is the whole
    // answer then, and there is nothing to poll.
    if (panelVisible && helperGate(o) === null) publishVisibility(true)
    resumeUnread = true
    refresh()
  }

  /** Replace a held binding with a fresh one, carrying the tree over. This is
   *  the fallback, never the norm — a pause keeps the binding, because the
   *  backend keeps it too. It exists for the case that binding is genuinely
   *  gone (a newer helper session, a restarted coordinator): the resumed
   *  tree's root then cannot be read, and one `files.open` answers where a
   *  painted failure would leave the person looking at a machine they can
   *  reach. */
  function reopenDeadScope(o: ActiveOrigin): void {
    const prevBinding = untrack(binding)
    if (prevBinding !== null) {
      void services.close(prevBinding.bindingId, 'background').catch(() => {})
      publishedVisible = null
    }
    openedSession = null
    setBinding(null)
    openScope(o, untrack(root))
  }

  /** Drop whatever the store holds for the old scope and open the new one.
   *  Shared by a visible re-scope, a re-scope that happens while the panel
   *  is shown again after the tab moved, and the origin going away. */
  function restartScope(next: ActiveOrigin | null, carried: FilesRoot | null = null): void {
    const prevBinding = binding()
    closed = false
    treeNeedsResume = false
    resumeUnread = false
    generation++
    if (next === null) discardFilterProjection()
    if (prevBinding !== null) {
      // The store's own bookkeeping, not a person's action: the binding is
      // released because the tree it served is gone.
      void services.close(prevBinding.bindingId, 'background').catch(() => {})
      publishedVisible = null
    }
    openedSession = null
    setOrigin(next)
    setBinding(null)
    setRoot(carried)
    setRevealTarget(null)
    if (next === null) {
      setPhase('no-origin')
      bumpTree()
      return
    }
    openScope(next, carried)
  }

  /** The origin went away (no tab in front). A binding nobody can read is
   *  released; nothing else is started. */
  function releaseScope(): void {
    const prevBinding = binding()
    treeNeedsResume = false
    resumeUnread = false
    if (prevBinding !== null) {
      void services.close(prevBinding.bindingId, 'background').catch(() => {})
      publishedVisible = null
    }
    openedSession = null
    closed = false
    generation++
    discardFilterProjection()
    setBinding(null)
    setRoot(null)
    setPhase('no-origin')
    setOpenError(null)
    setWatchMode(null)
    setWatchDegradedReason(null)
    setWatchFailed(null)
    setRefreshState(null)
    setRevealTarget(null)
    pendingReveal = null
    cancelReveal()
    revealing = false
    revealWalkId++
    bumpTree()
  }

  function toggle(node: FilesNode): void {
    if (node.busy || node.cyclic) return
    if (node.expanded) {
      node.expanded = false
      // The collapse is a later input than any owed page: a page fetched for
      // a directory nobody is looking at would be invisible work, and the
      // flag must not fire on some later settle (nocx-atpw5.1.16).
      node.wantsNextPage = false
      bumpTree()
      // The collapsed directory leaves the watch set: the set is what the
      // panel renders, and files.watch replaces it wholesale, so sending
      // the set without the collapsed path is what stops the watch.
      void syncWatchSet()
      maybeQueueFilterDirs()
      return
    }
    node.expanded = true
    // Collapse keeps the children; re-expanding a loaded directory is
    // instant, and the header refresh is what re-validates it.
    if (node.state === 'ok' && node.children.length > 0) {
      bumpTree()
      void syncWatchSet()
      maybeQueueFilterDirs()
      return
    }
    const ctx = captureCtx()
    if (ctx === null) {
      node.expanded = false
      bumpTree()
      return
    }
    void issueList(node, 0, FILES_PAGE_SIZE, ctx)
  }

  // ── Reveal (the product rule: the terminal owns "where am I"; the
  //    panel, rooted at the filesystem root, follows by revealing) ─────

  /** Reveal `targetPath`: walk from the root down to it, listing and
   *  expanding each level that is not already expanded, then select the
   *  target (revealTarget — the view scrolls it into view). NEVER
   *  collapses: a directory the user opened by hand stays open. Stops
   *  honestly: a level that comes back tooLarge/timedOut/unreadable, or a
   *  path that does not exist under the root, ends the walk with what was
   *  expanded left expanded and the level's state row rendered — the reveal
   *  did not reach the target and the tree says so where it stopped.
   *  Resolves true when the reached target is expandable, including a
   *  non-cyclic directory symlink, and false otherwise. Idempotent: the path
   *  already revealed reports true and a matching walk shares its result.
   *  Every step rides the {generation, bindingId} discipline (rules 2 and 3):
   *  a reveal in flight when the scope changes must drop. */
  /** Start a reveal requested while a new binding was opening. The queued
   *  promise stays owned by the caller while revealPath mints the walk after
   *  the root's first listing is ready. */
  function startQueuedReveal(): void {
    const target = pendingReveal
    const resolve = pendingRevealResolve
    if (target === null || resolve === null || revealing || untrack(root) === null) return
    pendingReveal = null
    pendingRevealPromise = null
    pendingRevealResolve = null
    void revealPath(target).then(resolve, () => resolve(false))
  }

  function revealPath(targetPath: string): Promise<boolean> {
    const r = untrack(root)
    if (r === null) {
      if (closed || untrack(origin) === null) return Promise.resolve(false)
      if (targetPath === pendingReveal) return pendingRevealPromise ?? Promise.resolve(false)
      cancelReveal()
      pendingReveal = targetPath
      const promise = new Promise<boolean>((resolve) => {
        pendingRevealResolve = resolve
      })
      pendingRevealPromise = promise
      return promise
    }
    if (targetPath === revealTarget()) return Promise.resolve(true)
    if (targetPath === pendingReveal) return pendingRevealPromise ?? Promise.resolve(false)
    const ctx = captureCtx()
    if (ctx === null) return Promise.resolve(false)
    // The target must sit under the root. The root is the filesystem
    // root, so in practice every absolute cwd does; the guard is the
    // walk's honesty about a path it cannot reach. (The '/' case below
    // is the only one the panel reaches; the general form stays honest
    // if a provider ever answers a different root.)
    const under = r.path === '/' ? targetPath.startsWith('/') : targetPath.startsWith(r.path + '/')
    if (targetPath !== r.path && !under) return Promise.resolve(false)
    cancelReveal()
    const promise = new Promise<boolean>((resolve) => {
      pendingRevealResolve = resolve
    })
    pendingRevealPromise = promise
    const walk = ++revealWalkId
    revealing = true
    // The path segments of the target below the root, in order; empty
    // when the target IS the root (it has no row — selecting the root
    // is selecting nothing, and that is the correct answer to `cd /`).
    const rest = r.path === '/' ? targetPath.slice(1) : targetPath.slice(r.path.length + 1)
    const segments = targetPath === r.path ? [] : rest.split('/').filter((s) => s !== '')
    descend(r, segments, 0, walk)
    return promise
  }

  /** One walk step. `dir` is the parent at level `i`; the child named by
   *  `segments[i]` must be found (listing the parent if necessary,
   *  paging exactly like "show next" when the child sits beyond the
   *  loaded pages) and expanded, then the walk descends into it. When
   *  `i` exhausts the segments, `dir` IS the target: select it. Every
   *  entry re-checks the walk id and the scope — a superseded walk or a
   *  changed origin drops without painting. */
  function descend(dir: FilesRoot | FilesNode, segments: string[], i: number, walk: number): void {
    if (walk !== revealWalkId) return
    const ctx = captureCtx()
    if (ctx === null || !scopeCurrent(ctx)) return
    if (i >= segments.length) {
      // The target: hand it to finishReveal, which selects it AND opens
      // it — arriving somewhere shows you what is there, so the target's
      // first page is listed like any level on the way.
      finishReveal(dir, walk)
      return
    }
    if (dir.state === 'tooLarge' || dir.state === 'timedOut' || dir.state === 'error') {
      // Honest stop: the level's state row is the visible "the reveal
      // did not reach the target". Nothing further is expanded.
      endWalk(walk)
      return
    }
    if (dir.state !== 'ok') {
      // Not enumerated yet (the root's first list is in flight, or the
      // directory was just expanded): list one page, then continue from
      // the committed children.
      revealList(dir, ctx, walk, () => descend(dir, segments, i, walk))
      return
    }
    const child = dir.children.find((c) => c.name === segments[i])
    if (child !== undefined) {
      // A non-directory cannot be descended into: the path ends here.
      // Asked of the kit, not re-derived: the row that draws a disclosure
      // owns what "expandable" means, and a second copy here would agree
      // everywhere anyone looked and disagree on the cyclic symlink.
      if (!isExpandable(child.kind, child.linkKind, child.cyclic === true)) {
        endWalk(walk)
        return
      }
      if (i + 1 >= segments.length) {
        // The child IS the target: finishReveal selects it and opens it,
        // the same landing as the branch above.
        finishReveal(child, walk)
        return
      }
      // A level ON the way: expand it (never collapse — a directory the
      // user opened by hand stays open) and descend. The reveal publishes
      // its whole watch set once its final list settles; doing so at every
      // level would baseline the growing path repeatedly.
      if (!child.expanded) {
        child.expanded = true
        bumpTree()
      }
      descend(child, segments, i + 1, walk)
      return
    }
    if (dir.hasMore) {
      // The child sits beyond the loaded pages: fetch the next page and
      // look again (D10 pagination applies to the walk too — the target
      // can be the 200th entry of a level).
      revealList(dir, ctx, walk, () => descend(dir, segments, i, walk))
      return
    }
    // The level is fully listed and holds no child by that name: the
    // target does not exist under the root.
    endWalk(walk)
  }
  /** One listing step of a reveal walk: issue the page, apply it, and
   *  continue the walk only when it is still the current walk. Pages
   *  APPEND to the loaded children — the walk enumerates the directory
   *  progressively, and a later page must never discard the rows an
   *  earlier page already showed (applyListing's replace semantics are
   *  for refreshes, whose limit covers the whole displayed window; a
   *  page's limit covers only the page). The ordering guard still
   *  applies: a response superseded by a newer cycle drops, and the walk
   *  continues from whatever state the tree is in. */
  function revealList(
    dir: FilesRoot | FilesNode,
    ctx: ListCtx,
    walk: number,
    onDone: () => void,
  ): void {
    const settle = claimBusy(dir)
    const apply = (res: FilesListResult): void => {
      settle()
      // The walk-identity check guards CONTINUING the walk, never applying
      // the answer. A superseded walk has already committed its expansion
      // to the tree — the directory is open and on screen — so dropping its
      // listing left that row spinning on a busy flag nothing would ever
      // clear, and left the directory out of the watch set, which is a row
      // the user is looking at that never reports a change again. Scope is
      // what decides whether the answer is ours at all.
      if (!scopeCurrent(ctx)) return
      if (ctx.generation < dir.appliedGeneration) {
        dir.busy = false
        bumpTree()
        if (walk === revealWalkId) onDone()
        resumePendingPage(dir)
        return
      }
      if (res.state === 'ok') {
        dir.state = 'ok'
        dir.canonical = res.canonical
        dir.rev = res.rev
        dir.total = res.total
        // Dedupe by path: a refresh that slipped in mid-walk (the
        // manual header refresh supersedes by generation, but a change
        // stream notification can race the same cycle) must not
        // duplicate rows the walk already holds.
        const known = new Set(dir.children.map((c) => c.path))
        for (const e of res.entries) {
          if (!known.has(e.path)) {
            dir.children.push(entryToNode(e))
            known.add(e.path)
          }
        }
        dir.hasMore = res.hasMore
        dir.nextOffset = res.offset + res.entries.length
        dir.error = null
        dir.appliedGeneration = ctx.generation
        dir.busy = false
        bumpTree()
        // This apply is the walk's own, so it does not run through
        // applyListing's wrapper: reconcile here for the same reason.
        void syncWatchSet()
        if (walk === revealWalkId) onDone()
        resumePendingPage(dir)
        return
      }
      // tooLarge/timedOut: apply through the same path every other
      // refusal rides, so the state row renders identically.
      applyListing(dir, ctx, res)
      if (walk === revealWalkId) onDone()
      resumePendingPage(dir)
    }
    services
      .list(ctx.bindingId as string, dir.path, dir.nextOffset, FILES_PAGE_SIZE, 'background')
      .then(apply, (e) =>
        untrack(() => {
          settle()
          if (!scopeCurrent(ctx)) return
          applyListError(dir, ctx, e)
          if (walk === revealWalkId) onDone()
          resumePendingPage(dir)
        }),
      )
  }

  /** The walk reached the target: EXPAND it, then select it. Expanding is
   *  the point — `cd` into a directory is a statement about what the user
   *  is now looking at, and answering it with a closed folder they have to
   *  click makes them do the work twice. Only a node expands: the root has
   *  no row and is enumerated already, so `cd /` selects nothing, which is
   *  the right answer. Listing goes through the walk's own path so the
   *  append semantics and the walk-id guard still hold. */
  function finishReveal(dir: FilesRoot | FilesNode, walk: number): void {
    if (walk !== revealWalkId) return
    const node = 'expanded' in dir ? dir : null
    if (
      node !== null &&
      !node.expanded &&
      isExpandable(node.kind, node.linkKind, node.cyclic === true)
    ) {
      node.expanded = true
      // Mirrors toggle(): a directory listed once and collapsed re-opens
      // instantly; anything else needs its first page. The final watch
      // baseline follows revealList, never overlaps it.
      if (node.state !== 'ok' || node.children.length === 0) {
        const ctx = captureCtx()
        if (ctx !== null) {
          revealList(node, ctx, walk, () => completeReveal(dir, walk))
          return
        }
      }
    }
    completeReveal(dir, walk)
  }

  /** Land the reveal: select the target, release the walk, and publish the
   *  levels it opened. The single baseline happens HERE rather than per
   *  level — a depth-N reveal would otherwise baseline the growing path N
   *  times — and it is a sync rather than a push, so a walk that opened
   *  nothing new sends nothing. */
  function completeReveal(dir: FilesRoot | FilesNode, walk: number): void {
    if (walk !== revealWalkId) return
    pendingReveal = null
    revealing = false
    setRevealTarget(dir.path)
    maybeStartFilterSearch()
    bumpTree()
    settleReveal(true)
    void syncWatchSet()
    resumePendingPages()
  }
  /** A stopped walk (a refused level, a missing child) keeps what it did
   *  open and publishes it. A SUPERSEDED walk returns at the guard and
   *  publishes nothing — it does not have to: its expansions are committed
   *  to the tree, and the listing it left in flight reconciles the set when
   *  it lands, which is the only moment the set actually changed. */
  function endWalk(walk: number): void {
    if (walk !== revealWalkId) return
    pendingReveal = null
    revealing = false
    settleReveal(false)
    maybeStartFilterSearch()
    void syncWatchSet()
    resumePendingPages()
  }

  /** Fetch the next page of an expanded directory (D10).
   *
   *  A CLICK THAT LANDS WHILE THE DIRECTORY IS ALREADY ENUMERATING IS
   *  REMEMBERED, NOT DROPPED (nocx-atpw5.1.16). A change-driven re-list
   *  (onFilesChanged → refreshDir) owns the enumeration for as long as its
   *  answer is outstanding, and this used to return on `busy` — the person
   *  pressed "Show next 20" and no files.list left the renderer at all. The
   *  button's own disabled state was a second owner of the same input, so
   *  the browser could swallow the click too; both are gone. The store is
   *  the one owner of the directory's enumeration: the click is serviced
   *  when the listing in flight settles (resumePendingPage), after the
   *  window that listing established. */
  function showMore(dir: FilesRoot | FilesNode): void {
    if (dir.state !== 'ok' || !dir.hasMore) return
    const ctx = captureCtx()
    if (ctx === null) return
    if (dir.busy) {
      dir.wantsNextPage = true
      return
    }
    requestNextPage(dir, ctx)
  }

  /** One page request: append the answer to what is already displayed. */
  function requestNextPage(dir: FilesRoot | FilesNode, ctx: ListCtx): void {
    const offset = dir.nextOffset
    const settle = claimBusy(dir)
    services.list(ctx.bindingId as string, dir.path, offset, FILES_PAGE_SIZE).then(
      (res) => {
        untrack(() => {
          settle()
          applyNextPage(dir, ctx, res)
        })
        untrack(() => resumePendingPage(dir))
      },
      (e) =>
        untrack(() => {
          settle()
          applyListError(dir, ctx, e)
          resumePendingPage(dir)
        }),
    )
  }

  function applyNextPage(dir: FilesRoot | FilesNode, ctx: ListCtx, res: FilesListResult): void {
    if (!scopeCurrent(ctx)) return
    if (ctx.generation < dir.appliedGeneration || dir.state !== 'ok') {
      dir.busy = false
      bumpTree()
      return
    }
    if (res.state === 'ok') {
      // A directory that changed between pages must not have a page
      // from the old snapshot appended to a page from the new one —
      // that is how rows duplicate and skip (§7). Re-list the
      // displayed window in one call instead (the refresh form).
      if (res.rev !== dir.rev) {
        refreshDir(dir)
        return
      }
      // The pagination was reset while this page was in flight (a
      // refresh landed first): appending would corrupt the fresh
      // listing, so drop the page — the refresh already replaced it.
      if (res.offset !== dir.nextOffset) {
        dir.busy = false
        bumpTree()
        return
      }
      dir.children.push(...res.entries.map(entryToNode))
      dir.nextOffset = res.offset + res.entries.length
      dir.hasMore = res.hasMore
      dir.total = res.total
      dir.rev = res.rev
      dir.appliedGeneration = ctx.generation
      dir.busy = false
      bumpTree()
      return
    }
    // A page request refused: the directory is now too large or too
    // slow. Render that state; the partial listing is discarded
    // (D14: an apparently complete prefix is worse than a refusal).
    dir.state = res.state === 'tooLarge' ? 'tooLarge' : 'timedOut'
    if (res.state === 'tooLarge') {
      dir.tooLargeLimit = res.limit
      dir.observedCount = res.observedCount ?? null
    } else {
      dir.timeout = res.timeout
    }
    dir.children = []
    dir.hasMore = false
    dir.appliedGeneration = ctx.generation
    dir.busy = false
    bumpTree()
  }

  /** Service a "Show next" that landed while the directory was already
   *  enumerating (nocx-atpw5.1.16). Called wherever a listing for the
   *  directory settles — the change-driven re-list, a page, a walk step — so
   *  the click follows the enumeration instead of racing it.
   *
   *  The watch is awaited first: the listing that just settled may have
   *  published a new set, and files.watch takes its own baseline listing, so
   *  the panel must not contend with itself for the capacity-one filesystem
   *  gate. A listing that starts in that gap keeps the click owed, and the
   *  next settle services it. */
  function resumePendingPage(dir: FilesRoot | FilesNode): void {
    if (!dir.wantsNextPage) return
    if (dir.busy || revealing) return
    if (dir.state !== 'ok' || !dir.hasMore) {
      dir.wantsNextPage = false
      return
    }
    if (captureCtx() === null) return
    void watchInFlight.then(() =>
      untrack(() => {
        if (!dir.wantsNextPage) return
        if (dir.busy || revealing) return
        if (dir.state !== 'ok' || !dir.hasMore) {
          dir.wantsNextPage = false
          return
        }
        // Re-read after the wait: the click is serviced against the scope
        // that is current when the page is actually asked for.
        const ctx = captureCtx()
        if (ctx === null) return
        dir.wantsNextPage = false
        requestNextPage(dir, ctx)
      }),
    )
  }

  /** Every directory still owed a page, walked in tree order. The reveal
   *  walk suppresses its own change refreshes and publishes its watch set
   *  only when it ends, so its two endings are the moments a click that
   *  landed mid-walk can be serviced (nocx-atpw5.1.16). */
  function resumePendingPages(): void {
    const r = untrack(root)
    if (r === null) return
    const visit = (dir: FilesRoot | FilesNode): void => {
      resumePendingPage(dir)
      for (const child of dir.children) if (child.expanded) visit(child)
    }
    visit(r)
  }

  function retry(dir: FilesRoot | FilesNode): void {
    if (dir.busy || (dir.state !== 'timedOut' && dir.state !== 'error')) return
    const ctx = captureCtx()
    if (ctx === null) return
    void issueList(dir, dir.nextOffset, FILES_PAGE_SIZE, ctx)
  }

  function refresh(): void {
    const o = origin()
    if (o === null) return
    // A paused or starting axis has nothing to re-read: the card is the
    // panel's answer, and its own action is the remedy (§5.10).
    if (helperGate(o) !== null) return
    if (phase() === 'no-origin') return
    if (binding() === null) {
      // The binding is gone (open failed, or disposed): re-establish the
      // scope — the header refresh is the retry for a failed open too, and
      // it is the one caller that opens without the context moving.
      if (phase() === 'failed') restartScope(o)
      return
    }
    // One cycle for the whole tree: every expanded directory is re-listed
    // over its displayed window (refreshLimit owns how wide that is), so a
    // refresh is a single snapshot per directory and in-flight page requests
    // are superseded by the bump.
    // Submit those listings serially: they all claim the same filesystem
    // conflict gate, and parallel refresh work would make sibling
    // directories refuse one another after the bounded gate wait.
    generation++
    refreshing = true
    const ctx = captureCtx()
    const r = root()
    if (ctx === null || r === null) {
      refreshing = false
      maybeStartFilterSearch()
      return
    }
    const dirs: (FilesRoot | FilesNode)[] = [r]
    const collect = (dir: FilesRoot | FilesNode): void => {
      for (const child of dir.children) {
        if (child.expanded) {
          dirs.push(child)
          collect(child)
        }
      }
    }
    collect(r)
    void dirs
      .reduce<Promise<void>>((chain, dir) => {
        return chain.then(() => {
          if (!untrack(() => scopeCurrent(ctx))) return
          return untrack(() => issueList(dir, 0, refreshLimit(dir), ctx))
        })
      }, Promise.resolve())
      // The watch set rides the refresh cycle too: re-establishing it is
      // what recovers a failed watch. It follows every list so retrying the
      // watch cannot contend with the refresh it belongs to.
      .then(() => {
        if (!untrack(() => scopeCurrent(ctx))) return
        forgetPublishedWatchSet()
        return untrack(() => syncWatchSet())
      })
      .finally(() =>
        untrack(() => {
          refreshing = false
          maybeStartFilterSearch()
        }),
      )
  }

  function dispose(): void {
    interruptedScope = null
    closed = true
    treeNeedsResume = false
    resumeUnread = false
    panelVisible = false
    openedSession = null
    publishedVisible = null
    discardFilterProjection()
    const b = binding()
    setOrigin(null)
    setBinding(null)
    setRoot(null)
    setPhase('no-origin')
    setOpenError(null)
    setWatchMode(null)
    setWatchDegradedReason(null)
    setWatchFailed(null)
    setRefreshState(null)
    setRevealTarget(null)
    // Supersede any walk still in flight: closed already drops its
    // responses, and the pending marker must not suppress the next
    // scope's reveal of the same path.
    pendingReveal = null
    revealing = false
    cancelReveal()
    revealWalkId++
    bumpTree()
    if (b !== null) void services.close(b.bindingId, 'background').catch(() => {})
    unsubChanged()
    unsubConnect()
    unsubRefreshState()
  }

  /** The change stream, subscribed once for the store's whole life: the
   *  handler filters by binding, so a notification for another binding —
   *  a viewer's, or a previous scope's — is ignored. On reconnect the
   *  watch set is re-sent: the backend's dirty set is flushed to the
   *  re-attached subscriber, and re-sending the set is idempotent (the
   *  backend diffs), so a dropped socket cannot silently detach the panel
   *  from the change stream. Unsubscribed in dispose() with the binding. */
  const unsubChanged = services.subscribeFilesChanged((p) => onFilesChanged(p))
  const unsubRefreshState = services.subscribeFilesRefreshStateChanged((p) =>
    onFilesRefreshStateChanged(p),
  )
  const unsubConnect = services.onConnect(() => {
    forgetPublishedWatchSet()
    void syncWatchSet()
  })

  return {
    phase,
    axisGate,
    openError,
    binding,
    origin,
    root,
    rows,
    filterRows,
    filterSettled,
    filter,
    setFilter,
    rescope,
    toggle,
    revealPath,
    revealTarget,
    showMore,
    retry,
    refresh,
    refreshState,
    setVisible,
    watchMode,
    watchDegradedReason,
    watchFailed,
    relativePath: (node) => relativePathOf(untrack(root)?.path ?? null, node.path),
    dispose,
  }
}
