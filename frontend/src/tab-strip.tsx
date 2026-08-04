import { For, Show, createSignal } from 'solid-js'
import { Tab } from './tab'
import { IconButton } from './ui/icon-button'
import { SearchField } from './ui/search-field'
import { ChevronDownIcon, PlusIcon } from './ui/icons'
import type { Setter } from 'solid-js'
import { createStore } from 'solid-js/store'
import { render } from 'solid-js/web'
import type { AgentStatus } from './agent-status'

// ═══════════════════════════════════════════════════════════════════════════
// TabStrip — presentation port for tab chrome
// ═══════════════════════════════════════════════════════════════════════════

/** The display state a TabStrip reads from each tab. */
export interface TabView {
  readonly id: number
  readonly title: string
  /** Title shown before content publishes its first dynamic title. */
  readonly displayTitle?: string
  readonly hasActivity: boolean
  readonly agentStatus: AgentStatus | null
  readonly tooltip: string
  /** The tab's location for the strip's second line, or '' when the title already
   *  says it — see Tab.subtitle. */
  readonly subtitle: string
  /** When true, the tab offers a save action (alias adoption). */
  readonly adoptable?: boolean
  readonly onAdopt?: (() => void) | null
  readonly paneId: string
  onDisplayChange: (() => void) | null
  /** True once the session confirmed a sandboxed local tab (lock/shield
   *  marker renders; ADR-0019 §3.3). */
  readonly sandboxed: boolean
}

/**
 * Reactive display-state record for a single tab, keyed by tab id.
 * Stored in a local Solid store so JSX expressions (each compiled into
 * their own reactive computation) are fine-grained reactive.
 * Uses displayTitle when the content has not published a dynamic title yet.
 */
interface TabDisplayRecord {
  title: string
  tooltip: string
  subtitle: string
  adoptable: boolean
  hasActivity: boolean
  agentStatus: AgentStatus | null
  /** True once the session confirmed a sandboxed local tab. */
  sandboxed: boolean
}

/** Presentation port for tab chrome. */
export interface TabStrip {
  readonly orientation: Orientation
  mount(container: HTMLElement): void
  addTab(tab: TabView): void
  removeTab(tabId: number): void
  setActive(tabId: number): void
  reorder(tabs: readonly TabView[]): void
  onActivate: ((tabId: number) => void) | null
  onClose: ((tabId: number) => void) | null
  onNewTab: (() => void) | null
  onReorder: ((fromId: number, toId: number) => void) | null
  onQuickConnect: (() => void) | null
}

// ═══════════════════════════════════════════════════════════════════════════
// Internal types
// ═══════════════════════════════════════════════════════════════════════════

export type Orientation = 'horizontal' | 'vertical'

// ═══════════════════════════════════════════════════════════════════════════
// TabStripBase — Solid renders every tab button via <For>, keyed by tab
// object identity. Display-state reactivity comes from a local store
// (createStore) that mirrors onDisplayChange-driven updates; the JSX reads
// store values inline (never hoisted into local variables), so Solid
// compiles each JSX expression into its own reactive computation.
// No createEffect or DOM patching is needed.
// ═══════════════════════════════════════════════════════════════════════════

abstract class TabStripBase implements TabStrip {
  protected dispose: (() => void) | null = null
  protected container: HTMLElement | null = null
  private mounted = false

  // Solid stores/signals — set during mount(), used by imperative API
  private _setTabViews!: Setter<TabView[]>
  private _getTabViews!: () => TabView[]
  private _setDisplay!: (...args: unknown[]) => void

  public abstract readonly orientation: Orientation

  // Intent callbacks
  onActivate: ((tabId: number) => void) | null = null
  onClose: ((tabId: number) => void) | null = null
  onNewTab: (() => void) | null = null
  onReorder: ((fromId: number, toId: number) => void) | null = null
  onQuickConnect: (() => void) | null = null

  /** Subclasses set up container attributes (class, aria). */
  protected abstract setupContainer(container: HTMLElement): void

  mount(container: HTMLElement): void {
    if (this.mounted) return
    this.mounted = true
    this.container = container

    this.setupContainer(container)
    container.addEventListener('keydown', this.onTablistKeydown)
    this.dispose = render(() => {
      const [tabViews, setTabViews] = createSignal<TabView[]>([])
      const [display, setDisplay] = createStore<{
        records: Record<number, TabDisplayRecord>
        activeId: number
      }>({ records: {}, activeId: -1 })
      const [searchQuery, setSearchQuery] = createSignal('')

      this._getTabViews = tabViews
      this._setTabViews = setTabViews
      this._setDisplay = setDisplay

      return (
        <>
          <Show when={this.orientation === 'vertical'}>
            <div class="tabstrip-header">
              <div class="tabstrip-search">
                <SearchField
                  value={searchQuery()}
                  onInput={(v) => setSearchQuery(v)}
                  placeholder="Filter tabs…"
                  ariaLabel="Filter tabs"
                  onKeyDown={(e) => {
                    if (e.key === 'Escape' && searchQuery() !== '') {
                      e.stopPropagation()
                    }
                  }}
                />
              </div>
              <div class="tabstrip-actions">
                <IconButton ariaLabel="New tab" square onClick={() => this.onNewTab?.()}>
                  <PlusIcon />
                </IconButton>
                <IconButton
                  ariaLabel="Quick connect"
                  onClick={() => this.onQuickConnect?.()}
                  tabIndex={-1}
                >
                  <ChevronDownIcon />
                </IconButton>
              </div>
            </div>
          </Show>
          <div class="tabs-container">
            <For each={tabViews()}>
              {(tab, index) => (
                <Tab
                  id={`tab-btn-${tab.id}`}
                  tabId={tab.id}
                  paneId={tab.paneId}
                  index={index()}
                  active={display.activeId === tab.id}
                  agentStatus={display.records[tab.id]?.agentStatus ?? null}
                  adoptable={display.records[tab.id]?.adoptable === true}
                  sandboxed={display.records[tab.id]?.sandboxed === true}
                  onAdopt={tab.onAdopt ?? undefined}
                  title={display.records[tab.id]?.title ?? ''}
                  tooltip={display.records[tab.id]?.tooltip ?? ''}
                  subtitle={display.records[tab.id]?.subtitle ?? ''}
                  hasActivity={display.records[tab.id]?.hasActivity === true}
                  tabIndex={display.activeId === tab.id ? 0 : -1}
                  orientation={this.orientation}
                  hidden={(() => {
                    const q = searchQuery().toLowerCase().trim()
                    if (!q) return false
                    const r = display.records[tab.id]
                    return (
                      !(r?.title ?? '').toLowerCase().includes(q) &&
                      !(r?.tooltip ?? '').toLowerCase().includes(q)
                    )
                  })()}
                  onActivate={() => this.onActivate?.(tab.id)}
                  onClose={(id) => this.onClose?.(id)}
                  onReorder={(fromId, toId) => this.onReorder?.(fromId, toId)}
                />
              )}
            </For>
          </div>
          <Show when={this.orientation === 'horizontal'}>
            {/* The strip's actions, as one group. They were two loose siblings of
                the tab list, which the vertical strip then spread down the whole
                column — the list is `flex: 1 1 auto`, so it pushed them apart and
                left the caret alone in the bottom corner. As a group they can be
                placed once, per orientation, by the strip's own CSS. */}
            <div class="tabstrip-actions">
              <IconButton ariaLabel="New tab" onClick={() => this.onNewTab?.()}>
                <PlusIcon />
              </IconButton>
              <IconButton
                ariaLabel="Quick connect"
                onClick={() => this.onQuickConnect?.()}
                tabIndex={-1}
              >
                <ChevronDownIcon />
              </IconButton>
            </div>
            <div class="tabbar-spacer" />
          </Show>
        </>
      )
    }, container)
  }

  addTab(tab: TabView): void {
    if (!this.mounted) return

    // Wire display-change notification to write changed fields into the store.
    tab.onDisplayChange = () => {
      this._setDisplay('records', tab.id, {
        title: tab.displayTitle ?? tab.title,
        tooltip: tab.tooltip,
        subtitle: tab.subtitle,
        adoptable: tab.adoptable,
        hasActivity: tab.hasActivity,
        agentStatus: tab.agentStatus,
        sandboxed: tab.sandboxed,
      })
    }

    this._setTabViews((prev) => [...prev, tab])

    // Initialize store entry with current display state.
    this._setDisplay('records', tab.id, {
      title: tab.displayTitle ?? tab.title,
      tooltip: tab.tooltip,
      subtitle: tab.subtitle,
      adoptable: tab.adoptable,
      hasActivity: tab.hasActivity,
      agentStatus: tab.agentStatus,
      sandboxed: tab.sandboxed,
    })

    // Link pane to button (aria-labelledby)
    const pane = document.getElementById(tab.paneId)
    if (pane) pane.setAttribute('aria-labelledby', `tab-btn-${tab.id}`)
  }

  removeTab(tabId: number): void {
    if (!this.mounted) return
    this._setTabViews((prev) => {
      const removed = prev.find((t) => t.id === tabId)
      if (removed) removed.onDisplayChange = null
      return prev.filter((t) => t.id !== tabId)
    })
    // Delete store entry — functional update avoids referencing current state.
    this._setDisplay('records', (prev: Record<number, TabDisplayRecord>) => {
      const next = { ...prev }
      delete next[tabId]
      return next
    })
  }

  setActive(tabId: number): void {
    if (!this.mounted) return
    this._setDisplay('activeId', tabId)
  }

  reorder(tabs: readonly TabView[]): void {
    if (!this.mounted) return
    // Solid's <For> reconciliation clears focus when it moves a node with
    // insertBefore, even though the node itself survives — keyed identity is
    // necessary here and not sufficient (nocx-82l9.8). Signal setters run their
    // dependent effects synchronously outside a batch, so the DOM is settled by
    // the time _setTabViews returns and restoring focus here is enough.
    const active = document.activeElement
    this._setTabViews([...tabs])
    if (active instanceof HTMLElement && this.container?.contains(active)) {
      active.focus({ preventScroll: true })
    }
  }

  // ── Keyboard (roving tabindex) ───────────────────────────────────────

  private readonly onTablistKeydown = (e: KeyboardEvent): void => {
    const keys =
      this.orientation === 'vertical'
        ? ['ArrowUp', 'ArrowDown', 'Home', 'End']
        : ['ArrowLeft', 'ArrowRight', 'Home', 'End']
    if (!keys.includes(e.key)) return

    const button = (e.target as HTMLElement).closest('[role="tab"]')
    if (!button) return

    e.preventDefault()
    e.stopPropagation()

    const tabId = Number(button.getAttribute('data-tab-id'))
    if (Number.isNaN(tabId)) return

    const tabs = this._getTabViews()
    const idx = tabs.findIndex((t) => t.id === tabId)
    if (idx === -1) return

    const len = tabs.length
    let nextIdx: number
    switch (e.key) {
      case 'ArrowUp':
      case 'ArrowLeft':
        nextIdx = idx > 0 ? idx - 1 : len - 1
        break
      case 'ArrowDown':
      case 'ArrowRight':
        nextIdx = idx < len - 1 ? idx + 1 : 0
        break
      case 'Home':
        nextIdx = 0
        break
      case 'End':
        nextIdx = len - 1
        break
      default:
        return
    }

    const nextTab = tabs[nextIdx]
    if (nextTab) {
      const nextBtn = document.getElementById(`tab-btn-${nextTab.id}`)
      nextBtn?.focus()
    }
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// HorizontalTabStrip
// ═══════════════════════════════════════════════════════════════════════════

export class HorizontalTabStrip extends TabStripBase {
  public readonly orientation: Orientation = 'horizontal'

  protected setupContainer(container: HTMLElement): void {
    container.classList.add('tabbar')
    container.setAttribute('role', 'tablist')
    container.setAttribute('aria-label', 'Terminal tabs')
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// VerticalTabStrip
// ═══════════════════════════════════════════════════════════════════════════

export class VerticalTabStrip extends TabStripBase {
  public readonly orientation: Orientation = 'vertical'

  protected setupContainer(container: HTMLElement): void {
    container.classList.add('tabstrip-vertical')
    container.setAttribute('role', 'tablist')
    container.setAttribute('aria-label', 'Terminal tabs')
  }
}
