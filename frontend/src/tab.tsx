import { Show } from 'solid-js'
import { IconButton } from './ui/icon-button'
import type { AgentStatus } from './agent-status'

/**
 * Tab — a feature component for a terminal tab button.
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
  tabId: number
  /** The pane this tab controls (aria-controls). */
  paneId: string
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
  /** When true, the tab offers a save action (alias adoption). */
  adoptable?: boolean
  /** True once the session confirmed a sandboxed local tab (lock/shield
   *  marker renders; ADR-0019 §3.3). */
  sandboxed?: boolean
  /** Triggered when the user clicks the save action. */
  onAdopt?: () => void
  /** Called when the tab is clicked. */
  onActivate: () => void
  /** Called with the tab id when the tab is closed (middle-click or close button). */
  onClose: (tabId: number) => void
  /** Called when a tab is dropped onto this one: (fromId, toId). */
  onReorder: (fromId: number, toId: number) => void
}

export function Tab(props: TabProps) {
  return (
    <div
      id={props.id}
      class="nocx-tab"
      role="tab"
      aria-controls={props.paneId}
      aria-selected={props.active}
      data-tab-id={String(props.tabId)}
      data-agent-status={props.agentStatus ?? undefined}
      data-sandboxed={props.sandboxed === true ? 'true' : undefined}
      data-hidden={props.hidden === true ? 'true' : undefined}
      // Kept in BOTH orientations. The vertical row shows the same text as a
      // subtitle, but that line ellipses — so dropping the native tooltip there
      // took away the only way to read a long path in full.
      title={props.tooltip}
      draggable={true}
      tabIndex={props.tabIndex}
      onClick={() => props.onActivate()}
      onMouseDown={(e: MouseEvent) => {
        if (e.button === 1) {
          e.preventDefault()
          props.onClose(props.tabId)
        }
      }}
      onDragStart={(e: DragEvent) => {
        e.dataTransfer?.setData('text/plain', String(props.tabId))
        if (e.currentTarget instanceof HTMLElement) {
          e.currentTarget.classList.add('dragging')
        }
      }}
      onDragEnd={(e: DragEvent) => {
        if (e.currentTarget instanceof HTMLElement) {
          e.currentTarget.classList.remove('dragging')
        }
      }}
      onDragOver={(e: DragEvent) => {
        e.preventDefault()
      }}
      onDrop={(e: DragEvent) => {
        e.preventDefault()
        const draggedId = Number(e.dataTransfer?.getData('text/plain'))
        if (!Number.isNaN(draggedId) && draggedId !== props.tabId) {
          props.onReorder(draggedId, props.tabId)
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
          <span class="nocx-tab-status" />
          <Show when={props.sandboxed === true}>
            <span class="nocx-tab-sandboxed-marker" aria-label="Sandboxed">
              {'\u26e8'}
            </span>
          </Show>
          <span class="nocx-tab-title">{props.title}</span>
        </span>
        <Show when={props.orientation === 'vertical' && (props.subtitle ?? '') !== ''}>
          <span class="nocx-tab-subtitle">{props.subtitle}</span>
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
          props.onClose(props.tabId)
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
