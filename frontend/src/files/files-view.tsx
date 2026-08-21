// FilesPanel — the Files sidebar view (design §5.4): the first icon in the
// activity bar, a session-scoped tree of the ACTIVE tab's machine.
//
// The panel follows the active tab through SidebarViewProps.activeOrigin —
// never a silent fall back to local, which would breach §0 in the same
// gesture as the panel's own primary action. Opening a file is the panel's
// primary action but the viewer is another worker's, so the panel takes a
// FileOpener as a dependency (the seam agreed in advance; a no-op default
// keeps the panel testable and runnable before the viewer lands).
//
// The header carries the panel's name, the refresh action and the polling
// badge slot (the badge itself belongs to the watching wave, §5.5, so the
// slot is left here and nothing else invents a different one). It carries no
// path: the root is the filesystem root and never moves, so there is nothing
// there to report. The root the panel is actually showing lives on the panel
// element as data-root, which is what the checks read.

import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import type { Component } from 'solid-js'
import type { SidebarViewDescriptor } from '../sidebar'
import type { ActiveOrigin } from '../pane-content'
import { createClipboardAccess, type ClipboardAccess } from '../clipboard'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { ContextMenu, type ContextMenuItem } from '../ui/context-menu'
import { EmptyState } from '../ui/empty-state'
import { IconButton } from '../ui/icon-button'
import { RefreshIcon } from '../ui/icons'
import { Spinner } from '../ui/spinner'
import { showToast } from '../ui/toast'
import { isExpandable, TreeRow } from '../ui/tree-row'
import { WatchBadge } from '../ui/watch-badge'
import type { FilesPanelServices } from './files-client'
import {
  createFilesTreeStore,
  type FilesFlatRow,
  type FilesNode,
  type FilesTreeStore,
} from './files-store'

// ── The opener seam ────────────────────────────────────────────────────────

/** The panel's primary action, delegated: the viewer tab is another worker's
 *  deliverable, so the panel calls this fixed contract and never builds a
 *  tab itself. The canonical comes from files.read (the file's identity —
 *  what the viewer's singletonKey deduplicates on); displayHost is null for
 *  a local file. `origin` is the scope the panel held at click time (minus
 *  its paneId — a tab detail the viewer does not know): the viewer answers
 *  the PaneContent `activeOrigin` capability with it, so the origin-following
 *  panel keeps showing this machine — and keeps the binding the viewer is
 *  reading through — while the viewer tab is in front (design §5.4). */
interface FileOpener {
  open(target: {
    bindingId: string
    endpointId: string | null
    path: string // lexical, as listed
    canonical: string // from files.read / files.list — the identity
    displayHost: string | null // null for local
    name: string
    origin: Omit<ActiveOrigin, 'paneId'> | null
  }): void
}

/** A no-op default: the panel is testable and runnable before the viewer
 *  lands, and a registration that forgets the opener degrades to nothing. */
const NOOP_OPENER: FileOpener = { open: () => {} }

// ── Activity-bar icon ──────────────────────────────────────────────────────

/** A folder (Lucide `folder` under ISC, like the other kit icons) — the
 *  activity bar's Files glyph. currentColor, same viewBox and stroke
 *  vocabulary as ui/icons so the rail treats it identically. */
const FilesIcon: Component = () => (
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
  </svg>
)

// ── Panel ─────────────────────────────────────────────────────────────────

export const FILES_VIEW_ID = 'files'

/** The activity-bar order. Ports registers 0 (main.tsx); Files registers
 *  BELOW it so it sorts to the top of the view zone — an owner requirement
 *  (the first icon is Files), asserted in files-view.test.tsx. */
export const FILES_VIEW_ORDER = -1

interface FilesPanelProps {
  store: FilesTreeStore
  services: FilesPanelServices
  opener: FileOpener
  /** The clipboard seam (AD-8): the composition root injects its single
   *  instance. Copying a path through the seam is what makes a refused
   *  write a reported failure instead of a silent no-op. */
  clipboard: ClipboardAccess
  /** The ACTIVE tab's origin — a reactive accessor, never a capture: the
   *  panel follows the tab in front. */
  activeOrigin: () => ActiveOrigin | null
}

function FilesPanel(props: FilesPanelProps) {
  // Re-scope on origin change: the panel follows the ACTIVE tab. The store
  // itself decides whether the change re-opens (different session) or is a
  // no-op (same session, rule 1). The accessor is read INSIDE the on()
  // source function so the read is tracked: props.activeOrigin is itself a
  // prop access (reactive), and the accessor it wraps is a signal read.
  createEffect(
    on(
      () => props.activeOrigin(),
      (origin) => props.store.rescope(origin),
    ),
  )
  // The reveal's SCROLL is the view's job — the store only says which
  // path the last completed reveal reached (revealTarget); this effect
  // watches that answer and scrolls the row into view when it lands.
  // `start` rather than `nearest`: under `/` the chain to a home directory
  // is long, and a target that merely came into view sits at the bottom
  // edge with its newly expanded children below the fold — the answer to
  // "where am I" should be at the top with room under it. The row renders
  // in the same flush that sets the target (the walk's last expansion
  // bumps the tree before the target is set), so the DOM is current.
  let treeEl: HTMLDivElement | undefined
  createEffect(
    on(
      () => props.store.revealTarget(),
      (target) => {
        if (target === null || treeEl === undefined) return
        // The target row, found by comparing the row's own data-path —
        // no selector escaping, so a path with quotes or brackets cannot
        // break the lookup (CSS.escape is not even available in jsdom).
        const rows = treeEl.querySelectorAll<HTMLElement>('[data-path]')
        for (const row of rows) {
          if (row.dataset.path === target) {
            // Optional: jsdom does not implement scrollIntoView, and a
            // scroll that cannot happen must not break the reveal.
            row.scrollIntoView?.({ block: 'start' })
            return
          }
        }
      },
    ),
  )
  // The view unmounts when another view takes the panel; its binding closes
  // with it, and the next mount re-opens through the rescope above.
  onCleanup(() => props.store.dispose())
  /** The primary action: resolve the file's canonical (files.read — the
   *  only shape that carries identity for a file, D12) and hand the target
   *  to the opener. A refusal here is an action outcome: a toast, never a
   *  silently dead row. */
  const openFile = async (node: FilesNode): Promise<void> => {
    const b = props.store.binding()
    const o = props.store.origin()
    if (b === null || o === null) return
    try {
      const res = await props.services.read(b.bindingId, node.path, 0)
      props.opener.open({
        bindingId: b.bindingId,
        endpointId: b.endpointId,
        path: node.path,
        canonical: res.canonical,
        // Provenance rides the origin's host label: null for a local file.
        // The viewer titles a remote file "host · name" and a local file by
        // its basename alone — the asymmetry is carried, never invented.
        displayHost: o.host,
        name: node.name,
        // The machine, minus the tab detail: the viewer answers the
        // activeOrigin capability with this, so the panel does not churn
        // its binding when the viewer tab it just opened takes focus.
        origin: {
          sessionId: o.sessionId,
          kind: o.kind,
          cwd: o.cwd,
          cwdVerified: o.cwdVerified,
          host: o.host,
          // The viewer's answer to "where are we" is NO opinion — it
          // carries the frozen origin for the machine, and this flag is
          // what tells origin-following surfaces not to move (design
          // §5.4, brief: a viewer must not cause a reveal).
          cwdFollow: false,
        },
      })
    } catch (e) {
      showToast({ level: 'danger', message: e instanceof Error ? e.message : String(e) })
    }
  }

  /** The open context menu: its anchor and the row it was opened for.
   *  Null when closed. */
  const [menu, setMenu] = createSignal<{ x: number; y: number; node: FilesNode } | null>(null)

  /** Copy an entry's path through the clipboard SEAM — never the browser
   *  API directly (AD-8). The seam rejects a write the platform refused
   *  (and the degraded seam rejects everything), so a copy that did not
   *  land reports it: a toast, exactly like the other refused actions. */
  const copyPath = async (node: FilesNode, kind: 'relative' | 'absolute'): Promise<void> => {
    const text = kind === 'relative' ? props.store.relativePath(node) : node.path
    try {
      await props.clipboard.writeText(text)
      showToast({ level: 'success', message: `Copied ${kind} path` })
    } catch (e) {
      showToast({ level: 'danger', message: e instanceof Error ? e.message : String(e) })
    }
  }

  /** Show in Finder: LOCAL tabs only — on a remote tab the item is not in
   *  the menu at all (§4), and absence, not a greyed-out row, is what tells
   *  the user the capability does not apply to that machine. The backend
   *  method exists; on a local binding the Wails seam is a later wave, so
   *  an honest refusal (-32601) is rendered like every other refused
   *  action — never stubbed, never hidden, never a silent no-op. */
  const revealInFinder = async (node: FilesNode): Promise<void> => {
    const b = props.store.binding()
    if (b === null) return
    try {
      await props.services.reveal(b.bindingId, node.path)
    } catch (e) {
      showToast({ level: 'danger', message: e instanceof Error ? e.message : String(e) })
    }
  }

  /** The menu's items for the row it is open on. The two copy entries are
   *  always there — they are two different answers, and both were asked
   *  for. Show in Finder joins only on a LOCAL origin. */
  const menuItems = (): ContextMenuItem[] => {
    const m = menu()
    if (m === null) return []
    const items: ContextMenuItem[] = [
      {
        id: 'copy-relative',
        label: 'Copy Relative Path',
        onSelect: () => void copyPath(m.node, 'relative'),
      },
      {
        id: 'copy-absolute',
        label: 'Copy Absolute Path',
        onSelect: () => void copyPath(m.node, 'absolute'),
      },
    ]
    const o = props.store.origin()
    if (o !== null && o.kind === 'local') {
      items.push({
        id: 'reveal',
        label: 'Show in Finder',
        onSelect: () => void revealInFinder(m.node),
      })
    }
    return items
  }

  /** What may be opened — the §5.1 table, kept in the renderer's words:
   *  regular opens, symlink→regular opens after canonical resolution,
   *  dir expands, other (FIFO, device) does neither. */
  const openable = (node: FilesNode): boolean =>
    node.kind === 'regular' || (node.kind === 'symlink' && node.linkKind === 'regular')

  const renderRow = (row: FilesFlatRow) => {
    if (row.kind === 'entry') {
      const node = row.node
      return (
        <div
          class="files-row"
          data-testid="files-row"
          data-path={node.path}
          onClick={() => {
            if (openable(node)) {
              void openFile(node)
              return
            }
            // A click anywhere on a directory row expands or collapses it —
            // the disclosure is a 16px target and the row is the whole
            // width, and every file manager the product is measured against
            // behaves this way. Expandability is asked of the kit rather
            // than re-derived here (AD-8): the row that draws the disclosure
            // is the one that decides there is one. A busy or unreadable row
            // is left alone, matching its disabled disclosure.
            if (node.busy === true || node.state === 'error') return
            if (isExpandable(node.kind, node.linkKind, node.cyclic)) {
              props.store.toggle(node)
            }
          }}
          onContextMenu={(e) => {
            e.preventDefault()
            setMenu({ x: e.clientX, y: e.clientY, node })
          }}
        >
          <TreeRow
            name={node.name}
            depth={row.depth}
            kind={node.kind}
            linkKind={node.linkKind}
            cyclic={node.cyclic}
            disabled={node.state === 'error'}
            busy={node.busy}
            expanded={node.expanded}
            // The reveal's selection: the row whose path the last
            // completed reveal reached. The kit row owns the rendering
            // (data-selected); the panel only names the target.
            selected={node.path === props.store.revealTarget()}
            onToggle={() => props.store.toggle(node)}
          />
        </div>
      )
    }
    if (row.kind === 'loading') {
      return (
        <div class="files-row" data-depth={row.depth} data-testid="files-loading-row">
          <Spinner size="sm" label="Loading directory" />
        </div>
      )
    }
    if (row.kind === 'more') {
      // D10: an explicit "show next N", never virtualised rows. N is what
      // remains — the next page will hold min(pageSize, remaining).
      const remaining = Math.max(0, row.dir.total - row.dir.children.length)
      return (
        <div class="files-row" data-depth={row.depth}>
          <Button
            size="sm"
            data-testid="files-show-more"
            disabled={row.dir.busy}
            onClick={() => props.store.showMore(row.dir)}
          >
            Show next {remaining}
          </Button>
        </div>
      )
    }
    // state row — rule 4: tooLarge and timedOut are REAL states, switched on
    // first, and neither is a toast nor an empty directory.
    const dir = row.dir
    if (dir.state === 'tooLarge') {
      const observed = dir.observedCount !== null ? ` (${dir.observedCount} entries)` : ''
      return (
        <div
          class="files-row files-row-state"
          data-depth={row.depth}
          data-testid="files-state-too-large"
        >
          <Badge tone="warning">Directory too large</Badge>
          <span>
            More than {dir.tooLargeLimit} entries{observed} — nocx does not display directories this
            large.
          </span>
        </div>
      )
    }
    if (dir.state === 'timedOut') {
      return (
        <div
          class="files-row files-row-state"
          data-depth={row.depth}
          data-testid="files-state-timed-out"
        >
          <span>This directory took too long to load.</span>
          <Button size="sm" data-testid="files-retry" onClick={() => props.store.retry(dir)}>
            Retry
          </Button>
        </div>
      )
    }
    return (
      <div class="files-row files-row-state" data-depth={row.depth} data-testid="files-state-error">
        <span>{dir.error}</span>
      </div>
    )
  }

  // data-root carries the root the panel is actually showing. The path left
  // the header — a header names the panel — but it is still the panel's
  // central state, and something has to be able to say WHICH machine and
  // directory this tree is: a check that waits on "a row appeared" cannot
  // tell a correct tree from a wrong machine's.
  return (
    <div class="files-panel" data-testid="files-panel" data-root={props.store.root()?.path}>
      <Show when={props.store.phase() === 'no-origin'}>
        <EmptyState
          icon={<FilesIcon />}
          title="No files to show"
          description="Focus a terminal tab to see the files of the machine you are on."
        />
      </Show>
      <Show when={props.store.phase() === 'opening'}>
        <div class="files-loading" data-testid="files-loading">
          <Spinner label="Opening files" />
        </div>
      </Show>
      <Show when={props.store.phase() === 'failed'}>
        <div class="files-error" data-testid="files-error">
          <p>{props.store.openError()}</p>
          <Button size="sm" data-testid="files-retry-open" onClick={() => props.store.refresh()}>
            Retry
          </Button>
        </div>
      </Show>
      <Show when={props.store.phase() === 'ready'}>
        {/* Refresh that has actually stopped (§5.5): a sticky INLINE
            message with Retry — not a toast, because a toast cannot answer
            "why is this stale?" ten minutes later. The Retry is the header
            refresh cycle, which re-sends the watch set and clears the
            failure the instant it recovers. */}
        <Show when={props.store.watchFailed() !== null}>
          <div class="files-watch-error" data-testid="files-watch-error">
            <span>{props.store.watchFailed()}</span>
            <Button size="sm" data-testid="files-watch-retry" onClick={() => props.store.refresh()}>
              Retry
            </Button>
          </div>
        </Show>
        <div class="files-tree" role="tree" aria-label="Files" ref={treeEl}>
          <For each={props.store.rows()}>{(row) => renderRow(row)}</For>
        </div>
      </Show>
      <ContextMenu
        open={menu() !== null}
        x={menu()?.x ?? 0}
        y={menu()?.y ?? 0}
        items={menuItems()}
        data-testid="files-context-menu"
        onClose={() => setMenu(null)}
      />
    </div>
  )
}

export interface FilesViewDeps {
  /** The panel's backend surface (createFilesPanelServices(dispatcher)). */
  services: FilesPanelServices
  /** The viewer-tab opener; a no-op default keeps the panel runnable before
   *  the viewer lands. */
  opener?: FileOpener
  /** The clipboard seam (AD-8): the composition root injects its single
   *  instance. The default exists so the panel runs standalone; main.tsx
   *  owns the real one. */
  clipboard?: ClipboardAccess
  /** Reactive accessor for the ACTIVE tab's origin — the coordinator wires
   *  it to PaneManager.activeOrigin() through onActivePaneChange, exactly like
   *  the ports target id. */
  activeOrigin: () => ActiveOrigin | null
}

/** Build the Files view descriptor. The store is created once, per
 *  descriptor, and shared between the header action (refresh) and the panel
 *  body — one signal, one backend call site (the ports pause pattern). */
export function createFilesView(deps: FilesViewDeps): SidebarViewDescriptor {
  const opener = deps.opener ?? NOOP_OPENER
  const clipboard = deps.clipboard ?? createClipboardAccess()
  const store = createFilesTreeStore(deps.services)
  return {
    id: FILES_VIEW_ID,
    title: 'Files',
    icon: FilesIcon,
    actions: () => (
      <>
        {/* Read store.root() INSIDE the JSX: a component body executes once,
            so capturing `const root = store.root()` would freeze the header
            path at its first render (the Solid gate's silent-reactivity
            failure — the refresh button's disabled binding is reactive, the
            captured const is not). */}
        {/* No root path here, and no badge about it either. A header names
              the panel; a path is neither a name nor an action.

              AD-5 binds CWD, not the panel root, and only reached this
              panel because the old design derived the root from the cwd —
              an unverifiable cwd substituted $HOME into the root, and the
              rule demanded that substitution be surfaced. The root is now
              the constant filesystem root: nothing is derived, nothing is
              substituted, and there is nothing to surface. When the cwd is
              unknown the panel shows / and highlights nothing, full stop. */}
        <IconButton
          data-testid="files-refresh"
          size="sm"
          ariaLabel="Refresh files"
          title="Refresh files"
          disabled={store.origin() === null}
          onClick={() => store.refresh()}
        >
          <RefreshIcon />
        </IconButton>
        {/* Polling badge (§5.5): 'polling' on a LOCAL binding with a
              reason is a real degrade — the persistent badge beside
              Refresh, hover carries the reason, cleared the instant
              watching recovers. A remote binding's designed-mode polling
              has no reason and warns about nothing.

              The judgement and the slot that carries the established mode
              are the KIT's now (`ui/watch-badge.tsx`), because the API
              workbench needed the same answer and a second copy of it would
              have agreed with this one everywhere anybody looked. The
              identities this panel's checks address — the slot, the badge
              and `data-watch-mode` — are unchanged; only their owner is. */}
        <WatchBadge
          testId="files-polling-badge"
          mode={store.watchMode()}
          reason={store.watchDegradedReason()}
          local={store.origin()?.kind === 'local'}
        />
      </>
    ),
    view: (props) => (
      <FilesPanel
        store={store}
        services={deps.services}
        opener={opener}
        clipboard={clipboard}
        activeOrigin={props.activeOrigin}
      />
    ),
    order: FILES_VIEW_ORDER,
  }
}
