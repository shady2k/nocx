import { For, Show, createSignal } from 'solid-js'
import { Button } from './ui/button'
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
  readonly hasActivity: boolean
  readonly agentStatus: AgentStatus | null
  readonly tooltip: string
  readonly paneId: string
  onDisplayChange: (() => void) | null
}

/**
 * Reactive display-state record for a single tab, keyed by tab id.
 * Stored in a local Solid store so JSX expressions (each compiled into
 * their own reactive computation) are fine-grained reactive.
 * Mirrors TabView getters — not Tab.displayTitle (which falls back to
 * 'Terminal' and would break e2e/tab-title.spec.ts).
 */
interface TabDisplayRecord {
  title: string
  tooltip: string
  hasActivity: boolean
  agentStatus: AgentStatus | null
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

      this._getTabViews = tabViews
      this._setTabViews = setTabViews
      this._setDisplay = setDisplay

      return (
        <>
          <div class="tabs-container">
            <For each={tabViews()}>
              {(tab, index) => (
                <div
                  id={`tab-btn-${tab.id}`}
                  classList={{
                    tab: true,
                    active: display.activeId === tab.id,
                    working: display.records[tab.id]?.agentStatus === 'working',
                    waiting: display.records[tab.id]?.agentStatus === 'idle',
                  }}
                  role="tab"
                  aria-controls={tab.paneId}
                  aria-selected={display.activeId === tab.id}
                  data-tab-id={String(tab.id)}
                  title={display.records[tab.id]?.tooltip ?? ''}
                  draggable={true}
                  tabIndex={display.activeId === tab.id ? 0 : -1}
                  onClick={() => this.onActivate?.(tab.id)}
                  onMouseDown={(e: MouseEvent) => {
                    if (e.button === 1) {
                      e.preventDefault()
                      this.onClose?.(tab.id)
                    }
                  }}
                  onDragStart={(e: DragEvent) => {
                    e.dataTransfer?.setData('text/plain', String(tab.id))
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
                    if (!Number.isNaN(draggedId) && draggedId !== tab.id) {
                      this.onReorder?.(draggedId, tab.id)
                    }
                  }}
                >
                  <span class="tab-index">{index() + 1}</span>
                  <span class="tab-label">
                    <span class="tab-status" />
                    <span class="tab-title">{display.records[tab.id]?.title ?? ''}</span>
                  </span>
                  <Button
                    class="tab-close"
                    ariaLabel="Close tab"
                    onClick={(e: MouseEvent) => {
                      e.stopPropagation()
                      this.onClose?.(tab.id)
                    }}
                  >
                    {'\u00d7'}
                  </Button>
                  <div
                    class="tab-indicator"
                    classList={{
                      'tab-activity':
                        display.records[tab.id]?.hasActivity === true &&
                        display.activeId !== tab.id,
                    }}
                  />
                </div>
              )}
            </For>
          </div>
          <Button class="tab-add" ariaLabel="New tab" onClick={() => this.onNewTab?.()}>
            +
          </Button>
          <Button
            class="tab-caret"
            ariaLabel="Quick connect"
            onClick={() => this.onQuickConnect?.()}
            tabIndex={-1}
          >
            ▾
          </Button>
          <Show when={this.orientation === 'horizontal'}>
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
        title: tab.title,
        tooltip: tab.tooltip,
        hasActivity: tab.hasActivity,
        agentStatus: tab.agentStatus,
      })
    }

    this._setTabViews((prev) => [...prev, tab])

    // Initialize store entry with current display state.
    this._setDisplay('records', tab.id, {
      title: tab.title,
      tooltip: tab.tooltip,
      hasActivity: tab.hasActivity,
      agentStatus: tab.agentStatus,
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
