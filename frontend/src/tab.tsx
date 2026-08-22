import { Show } from 'solid-js'
import { IconButton } from './ui/icon-button'
import { TAB_DRAG_TYPE } from './layout/strip-drag'
import { PinIcon, ShieldIcon } from './ui/icons'
import type { AgentStatus } from './agent-status'

/**
 * Tab — a feature component for a terminal tab button.
 *
 * KEEPS THE WORD, deliberately (nocx-ehkvy). The rename moved "tab" to "pane"
 * everywhere the symbol holds the durable thing — the pipe, the cwd, the
 * blocks. This is not that thing. It is the STRIP ENTRY: a `role="tab"` button
 * consumed only by TabStrip, which is exactly what the design reserves the word
 * for. The file was briefly renamed to pane.tsx and moved back, so if you are
 * about to rename it again, this paragraph is the reason not to.
 *
 * Renders a `<div role="tab">` with `class="nocx-tab"` and `data-*` / `aria-*`
 * for variance. Tab carries drag/reorder, middle-click close, an activity
 * indicator, an agent-status indicator, and `aria-controls`.
 *
 * This is a feature component (declared in feature-components.json), not a kit
 * primitive — it is a behavioural unit consumed only by TabStrip.
 *
 * Roving tabindex stays with the group (TabStripBase.onTablistKeydown); this
 * component only accepts `tabIndex` and `active`.
 */
export interface TabProps {
  /** Element id, used for aria-labelledby from the pane. */
  id: string
  /** The tab's numeric id, used in callsite identity and data transfer. */
  paneId: number
  /** The pane this tab controls (aria-controls). */
  controlledPaneId: string
  /** 1-based index display. */
  index: number
  /** Whether this tab is active/selected. */
  active: boolean
  /** Agent status for the status indicator. */
  agentStatus: AgentStatus | null
  /** Display title from the reactive store. */
  title: string
  /** Tooltip text from the reactive store — rendered as subtitle in vertical mode. */
  tooltip: string
  /** Whether there is unread activity visible on an inactive tab. */
  hasActivity: boolean
  /** Tabindex for roving tabindex participation. */
  tabIndex: number
  /** Orientation of the tab strip — controls subtitle rendering. Defaults to 'horizontal'. */
  orientation?: 'horizontal' | 'vertical'
  /** When true, the tab row is hidden via CSS (filtering). Defaults to false. */
  hidden?: boolean
  /** The row's second line in vertical placement: the tab's location. Empty when the
   *  title already carries it, in which case no second line is drawn. */
  subtitle?: string
  /** What is happening in the pane, in one line (`Pane.preview`). The
   *  vertical row's second line prefers it over the location — see the
   *  render. */
  preview?: string
  /** When true, the tab offers a save action (alias adoption). */
  adoptable?: boolean
  /** Triggered when the user clicks the save action. */
  onAdopt?: () => void
  /** The environment degraded or became uncertain (nocx-4t37.2): renders
   *  the small warning mark in the status line. */
  warning?: boolean
  /** What the mark means, for its accessible name and its tooltip. The
   *  session's integration status supplies it (nocx-5uu5) — a mark that
   *  cannot say what it is about is a mark people learn to ignore. Falls
   *  back to the generic wording when nothing more specific is known. */
  warningLabel?: string
  /** Native sandbox readiness was confirmed for this pane. */
  sandboxed?: boolean
  /** The tab's colour, as the backend stores it (nocx-isoph.4): one of the
   *  closed set in layout/workspace-colours.ts, or undefined for an undecorated
   *  tab, which is the normal state. It renders as a swatch on the row and
   *  never as a repaint of the tab — the colour is a mark the user put on it,
   *  not a theme of its own. */
  colour?: string
  /**
   * THE COLOUR OF THE RUN THIS TAB SITS IN, not of the tab (workspaces UX
   * rework). Set on every tab of an unfolded workspace in the horizontal
   * strip, so the run reads as one segment with the workspace's pill in
   * front of it — the thing that makes a workspace a place in the row rather
   * than a mode of the window.
   *
   * DELIBERATELY A SECOND ATTRIBUTE AND NOT A REUSE OF `colour`. That one is
   * a mark the USER put on this tab; this one is derived from which container
   * the tab is in, and the user chose neither the mapping nor the palette
   * entry. Folding them into one attribute would make "why is this tab
   * green?" a question with two answers, and clearing a tab's own colour
   * would then have to know whether it was clearing a decoration or a group.
   * They draw differently in tab.css for the same reason: a swatch on the row
   * against a rule along its edge.
   */
  groupColour?: string
  /** Whether the tab is kept at the head of the strip. The strip does the
   *  keeping (layout/strip-order.ts); this only draws the mark that says why
   *  a tab is where it is. */
  pinned?: boolean
  /** How far in the row is drawn: 0 for a top-level row, +1 per LINEAGE
   *  generation (nocx-isoph.5, layout/strip-tree.ts). Indentation is driven
   *  by the number and never by nested DOM — the same technique the kit's
   *  TreeRow uses — so a row is one row at any depth: it keeps its drag, its
   *  keyboard place and its close. The vertical strip is where the tree is
   *  drawn (§4.3); the horizontal one passes 0 and the attribute is absent.
   *
   *  It is provenance and nothing else. A child is drawn under its parent and
   *  no authority follows from that (ADR-0020 §5). */
  depth?: number
  /** Called when the tab is right-clicked, with the viewport coordinates the
   *  menu should open at. The strip owns the menu; a tab knows only that it
   *  was asked for one. */
  onMenu?: (paneId: number, x: number, y: number) => void
  /** Called when the tab is clicked. */
  onActivate: () => void
  /** Called with the tab id when the tab is closed (middle-click or close button). */
  onClose: (paneId: number) => void
  /**
   * Called when a tab is dropped onto this one.
   *
   * `before` is which SIDE of this row the drop lands on — the half of the
   * row the pointer was over. Without it there is no way to put a tab last:
   * every drop meant "in front of the row I hit", so the final slot of a run
   * was unreachable and the bottom row could not move at all, since every
   * target below it was itself.
   */
  onReorder: (fromId: number, toId: number, before: boolean) => void
  /** Tell the strip a drag has started or finished, so it can say which rows
   *  will accept it. */
  onDragBegin?: (paneId: number) => void
  onDragFinish?: () => void
  /**
   * Whether the drag in flight may land on this row.
   *
   * A reorder is one workspace's business — the wire takes a permutation of
   * ONE workspace's tabs (`tabs.reorder`) and refuses anything else — so a
   * row in another group cannot accept the drop. It shows no indicator
   * either: an insertion line at a place that will not take the tab is worse
   * than none, because the person believes it.
   */
  dropAllowed?: boolean
}

export function Tab(props: TabProps) {
  return (
    <div
      id={props.id}
      class="nocx-tab"
      role="tab"
      aria-controls={props.controlledPaneId}
      aria-selected={props.active}
      data-pane-id={String(props.paneId)}
      data-agent-status={props.agentStatus ?? undefined}
      data-colour={props.colour || undefined}
      data-group-colour={props.groupColour || undefined}
      data-pinned={props.pinned === true ? 'true' : undefined}
      data-sandboxed={props.sandboxed === true ? 'true' : undefined}
      data-hidden={props.hidden === true ? 'true' : undefined}
      data-depth={(props.depth ?? 0) > 0 ? String(props.depth) : undefined}
      // Kept in BOTH orientations. The vertical row shows the same text as a
      // subtitle, but that line ellipses — so dropping the native tooltip there
      // took away the only way to read a long path in full.
      title={props.tooltip}
      draggable={true}
      tabIndex={props.tabIndex}
      onClick={() => props.onActivate()}
      onContextMenu={(e: MouseEvent) => {
        if (!props.onMenu) return
        // The browser's own menu here offers nothing about a tab, and the
        // strip's actions (rename, colour, pin) have no other home in the
        // horizontal strip — there is no room for a control per action on a
        // row this narrow.
        e.preventDefault()
        e.stopPropagation()
        props.onMenu(props.paneId, e.clientX, e.clientY)
      }}
      onMouseDown={(e: MouseEvent) => {
        if (e.button === 1) {
          e.preventDefault()
          props.onClose(props.paneId)
        }
      }}
      onDragStart={(e: DragEvent) => {
        // The kind first, so a target can ask what is coming while the drag is
        // in flight; the id is unreadable until the drop (strip-drag.ts).
        e.dataTransfer?.setData(TAB_DRAG_TYPE, String(props.paneId))
        e.dataTransfer?.setData('text/plain', String(props.paneId))
        props.onDragBegin?.(props.paneId)
        if (e.currentTarget instanceof HTMLElement) {
          e.currentTarget.classList.add('dragging')
        }
      }}
      onDragEnd={(e: DragEvent) => {
        props.onDragFinish?.()
        if (e.currentTarget instanceof HTMLElement) {
          e.currentTarget.classList.remove('dragging')
          delete e.currentTarget.dataset.dropEdge
        }
      }}
      onDragOver={(e: DragEvent) => {
        // A WORKSPACE IS NOT A TAB. Its heading is dragged around the same
        // rail, and a row that took every drag lit its insertion line for one
        // — offering a place between two tabs where a workspace cannot go,
        // and doing nothing when it landed there.
        if (!e.dataTransfer?.types.includes(TAB_DRAG_TYPE)) return
        if (props.dropAllowed === false) return
        // Preventing the default is what makes this a drop target at all.
        e.preventDefault()
        const row = e.currentTarget
        if (!(row instanceof HTMLElement)) return
        // WHICH SIDE, from the pointer's half of the row — the axis being the
        // strip's own: a column is divided top from bottom, a row left from
        // right. It is measured per move rather than remembered because a
        // drag crosses the midline without leaving the element, and a stale
        // answer would draw the line on the wrong edge.
        const rect = row.getBoundingClientRect()
        const before =
          props.orientation === 'vertical'
            ? e.clientY < rect.top + rect.height / 2
            : e.clientX < rect.left + rect.width / 2
        row.dataset.dropEdge = before ? 'before' : 'after'
      }}
      onDragLeave={(e: DragEvent) => {
        if (e.currentTarget instanceof HTMLElement) delete e.currentTarget.dataset.dropEdge
      }}
      onDrop={(e: DragEvent) => {
        e.preventDefault()
        const row = e.currentTarget
        const edge = row instanceof HTMLElement ? row.dataset.dropEdge : undefined
        if (row instanceof HTMLElement) delete row.dataset.dropEdge
        props.onDragFinish?.()
        if (props.dropAllowed === false) return
        const draggedId = Number(e.dataTransfer?.getData(TAB_DRAG_TYPE))
        if (!Number.isNaN(draggedId) && draggedId !== props.paneId) {
          props.onReorder(draggedId, props.paneId, edge !== 'after')
        }
      }}
    >
      <span class="nocx-tab-index">{props.index + 1}</span>
      <span class="nocx-tab-label">
        {/* The status dot belongs ON the title's line, not above it. In the
            vertical row the label is a column, so a status span sitting beside
            the title became a third row of its own — 10px tall even when the dot
            is not showing — and pushed the two visible lines below the row's
            centre. Wrapping the pair keeps the column at exactly two children. */}
        <span class="nocx-tab-line">
          <Show when={props.sandboxed === true}>
            <span
              class="nocx-tab-sandboxed-marker"
              aria-label="Sandboxed"
              title="Filesystem-isolated"
            >
              <ShieldIcon />
            </span>
          </Show>
          <span class="nocx-tab-status" />
          {/* Why this tab is at the head of the strip. Without the mark the
              pinning is invisible until the strip is long enough for the
              order to be surprising, which is the moment it is least
              welcome. */}
          <Show when={props.pinned === true}>
            <span class="nocx-tab-pin" aria-label="Pinned" title="Pinned">
              <PinIcon />
            </span>
          </Show>
          <Show when={props.warning === true}>
            <span
              class="nocx-tab-warning"
              aria-label={props.warningLabel ?? 'Environment degraded'}
              title={props.warningLabel ?? 'Shell integration degraded or uncertain'}
            />
          </Show>
          <span class="nocx-tab-title">{props.title}</span>
        </span>
        {/* ONE SECOND LINE, AND WHAT IT SAYS. The row has room for exactly
            one line under the title, and two facts want it: WHERE the pane is
            (the location, when the title is a program's name rather than a
            path) and WHAT IT IS DOING — the same sentence the overview's card
            prints for the same pane.

            What it is doing wins when there is one. A rail of tabs is read to
            find the one that needs you, and `go test ./… · exit 1` answers
            that where a host and a directory do not; the location falls back
            in for a pane with nothing to report, which is when it is the only
            thing there is to say. */}
        <Show
          when={
            props.orientation === 'vertical' &&
            ((props.preview ?? '') !== '' || (props.subtitle ?? '') !== '')
          }
        >
          <span class="nocx-tab-subtitle">
            {(props.preview ?? '') !== '' ? props.preview : props.subtitle}
          </span>
        </Show>
      </span>
      <Show when={props.adoptable === true}>
        <IconButton
          size="sm"
          ariaLabel="Save as connection"
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            props.onAdopt?.()
          }}
          square
        >
          {'+'}
        </IconButton>
      </Show>
      <IconButton
        size="sm"
        ariaLabel="Close tab"
        onClick={(e: MouseEvent) => {
          e.stopPropagation()
          props.onClose(props.paneId)
        }}
      >
        {'\u00d7'}
      </IconButton>
      <div
        class="nocx-tab-indicator"
        data-activity={props.hasActivity && !props.active ? 'true' : undefined}
      />
    </div>
  )
}
