/**
 * Sidebar — Solid component for the VS Code-style activity bar + collapsible
 * panel.  Fixes nocx-rp2j: views and actions are separate types, and the
 * panel only opens when a view is active.
 *
 * The activity bar has two zones:
 *   Top zone   — views from the registry.  Clicking toggles the panel and
 *                switches the active view.
 *   Bottom zone — global actions (e.g. Settings gear).  An action opens a tab
 *                and never touches the panel.
 *
 * The panel is rendered via a separate Solid root (`PanelRoot`) that shares
 * the same store as the activity bar, so both zones and panel stay in sync.
 *
 * Keyboard model (roving tabindex toolbar):
 *   role="toolbar" with aria-label="Activity bar"
 *   Up/Down   — move focus between buttons
 *   Home/End  — jump to start / end
 *   Enter/Space — activate the focused button
 *
 * Mounted via `mountSidebar()` into the bar element; also opens a second root
 * inside the panel element.
 */

import { render, Dynamic } from 'solid-js/web'
import { createEffect, createMemo, For, on, onCleanup, Show } from 'solid-js'
import type { Component } from 'solid-js'
import { SidebarView } from './ui/sidebar-view'
import { createAppStore, type AppActions, type AppState } from './state'
import { Button } from './ui/button'

const STORAGE_KEY = 'nocx.sidebar.collapsed'

// ── Types ──────────────────────────────────────────────────────────────────

/** A view whose content is rendered inside the sidebar panel. */
export interface SidebarViewDescriptor {
  readonly id: string
  readonly title: string // panel header, e.g. "EXPLORER"
  readonly icon: Component // activity-bar icon — a component, never markup
  readonly view: Component // panel body
  readonly actions?: Component // per-view header actions (…, refresh, collapse-all)
  readonly order: number
}

/** An action button in the bottom zone (global actions, never opens panel). */
export interface SidebarAction {
  readonly id: string
  readonly title: string
  readonly icon: Component
  readonly onActivate: () => void
}

/** Minimal storage surface — injectable so tests avoid localStorage quirks. */
export interface SidebarStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

/** Handle returned by mountSidebar. */
export interface SidebarHandle {
  destroy(): void
}

function safeLocalStorage(): SidebarStorage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

// ── Panel content (rendered as a separate Solid root inside #sidebar) ──────

interface PanelRootProps {
  state: AppState
  views: readonly SidebarViewDescriptor[]
}

function PanelRoot(props: PanelRootProps) {
  const activeDesc = createMemo(
    () => props.views.find((v) => v.id === props.state.sidebar.activeViewId) ?? null,
  )

  return (
    <Show when={activeDesc()}>
      <ActiveView desc={activeDesc()!} />
    </Show>
  )
}

/**
 * Renders the active view's content inside a SidebarView shell.
 *
 * The components are read through `Dynamic` rather than hoisted into locals.
 * Hoisting (`const ViewComp = props.desc.view`) reads the prop once, outside any
 * tracked scope, so switching views would keep rendering the first view's
 * component — the silent-reactivity failure the Solid lint gate exists to catch.
 */
function ActiveView(props: { desc: SidebarViewDescriptor }) {
  return (
    <SidebarView
      title={props.desc.title}
      actions={
        <Show when={props.desc.actions}>{(Actions) => <Dynamic component={Actions()} />}</Show>
      }
    >
      <Dynamic component={props.desc.view} />
    </SidebarView>
  )
}

// ── Activity bar component ─────────────────────────────────────────────────

interface SidebarSolidProps {
  bar: HTMLElement
  panel: HTMLElement
  views: readonly SidebarViewDescriptor[]
  actions: readonly SidebarAction[]
  storage: SidebarStorage | null
  state: AppState
  storeActions: AppActions
}

function SidebarSolid(props: SidebarSolidProps) {
  // ── Derive active view descriptor ──────────────────────────────────────
  const activeDesc = createMemo(
    () => props.views.find((v) => v.id === props.state.sidebar.activeViewId) ?? null,
  )

  // Panel is effectively collapsed when the store says collapsed or there is
  // no matching view descriptor (e.g. zero views registered or an orphan id).
  const effectivelyCollapsed = createMemo(() => props.state.sidebar.collapsed || !activeDesc())

  // ── Which button gets tabindex="0"? ──────────────────────────────────
  // Active view's button when one is active; otherwise the first button in
  // the toolbar (view or action) so the toolbar is always keyboard-reachable.
  const tabbableId = createMemo(() => {
    if (props.state.sidebar.activeViewId) {
      const found = props.views.find((v) => v.id === props.state.sidebar.activeViewId)
      if (found) return found.id
    }
    // Fall back to the first item in toolbar order (views before actions).
    if (props.views.length > 0) return props.views[0].id
    if (props.actions.length > 0) return props.actions[0].id
    return null
  })

  // ── Keyboard shortcut: Ctrl/Cmd+B toggles sidebar ──────────────────────
  createEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key === 'b') {
        e.preventDefault()
        props.storeActions.toggleSidebar()
      }
    }
    document.addEventListener('keydown', handler)
    onCleanup(() => document.removeEventListener('keydown', handler))
  })

  // ── Persist collapsed state ───────────────────────────────────────────
  createEffect(() => {
    props.storage?.setItem(STORAGE_KEY, props.state.sidebar.collapsed ? '1' : '0')
  })

  // ── Collapsed class on the panel host (CSS in style.css) ──────────────
  createEffect(() => {
    props.panel.classList.toggle('collapsed', effectivelyCollapsed())
  })

  // ── Focus return on collapse ─────────────────────────────────────────
  let focusAnchor: string | null = null
  createEffect(
    on(
      () => props.state.sidebar.collapsed,
      (collapsed, prev) => {
        if (prev === false && collapsed && focusAnchor) {
          const btn = props.bar.querySelector<HTMLElement>(
            `[role="button"][data-view="${focusAnchor}"]`,
          )
          btn?.focus()
          focusAnchor = null
        }
      },
    ),
  )

  // ── Keyboard handler: roving tabindex on the toolbar ──────────────────
  const handleKeyDown = (e: KeyboardEvent) => {
    const toolbar = props.bar.querySelector('[role="toolbar"]')
    if (!toolbar) return
    const buttons = [...toolbar.querySelectorAll<HTMLElement>('[role="button"]')]
    if (buttons.length === 0) return

    const currentIdx = buttons.findIndex((b) => b.getAttribute('tabindex') === '0')
    const idx = currentIdx >= 0 ? currentIdx : 0

    const moveTo = (next: number) => {
      e.preventDefault()
      buttons[idx]?.setAttribute('tabindex', '-1')
      buttons[next]?.setAttribute('tabindex', '0')
      buttons[next]?.focus()
    }

    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowRight':
        moveTo((idx + 1) % buttons.length)
        break
      case 'ArrowUp':
      case 'ArrowLeft':
        moveTo((idx - 1 + buttons.length) % buttons.length)
        break
      case 'Home':
        moveTo(0)
        break
      case 'End':
        moveTo(buttons.length - 1)
        break
      case 'Enter':
      case ' ':
        e.preventDefault()
        buttons[idx]?.click()
        break
      default:
        break
    }
  }

  // ── View click handler ─────────────────────────────────────────────────
  const handleViewClick = (view: SidebarViewDescriptor) => {
    focusAnchor = view.id
    const { collapsed } = props.state.sidebar
    if (collapsed) {
      props.storeActions.setActiveView(view.id)
      props.storeActions.toggleSidebar()
    } else {
      props.storeActions.setActiveView(view.id)
    }
  }

  // ── Action click handler (bottom zone, never touches panel) ───────────
  const handleActionClick = (action: SidebarAction) => {
    action.onActivate()
  }

  // ── Render ─────────────────────────────────────────────────────────────
  return (
    <div role="toolbar" aria-label="Activity bar" class="activity-bar" onKeyDown={handleKeyDown}>
      {/* Top zone: views */}
      <div class="activity-bar-zone activity-bar-top" role="group" aria-label="Views">
        <For each={props.views}>
          {(view) => (
            <Button
              class={`activity-bar-btn${view.id === props.state.sidebar.activeViewId && !props.state.sidebar.collapsed ? ' active' : ''}`}
              role="button"
              data-view={view.id}
              title={view.title}
              aria-label={view.title}
              tabIndex={view.id === tabbableId() ? 0 : -1}
              onClick={() => handleViewClick(view)}
            >
              <view.icon />
            </Button>
          )}
        </For>
      </div>

      {/* Spacer pushes bottom zone to the bottom */}
      <div class="activity-bar-spacer" />

      {/* Bottom zone: actions */}
      <div class="activity-bar-zone activity-bar-bottom" role="group" aria-label="Actions">
        <For each={props.actions}>
          {(action) => (
            <Button
              class="activity-bar-btn"
              role="button"
              data-action={action.id}
              title={action.title}
              aria-label={action.title}
              tabIndex={action.id === tabbableId() ? 0 : -1}
              onClick={() => handleActionClick(action)}
            >
              <action.icon />
            </Button>
          )}
        </For>
      </div>
    </div>
  )
}

// ── Mount function ────────────────────────────────────────────────────────

/**
 * Mount the sidebar Solid component and return a handle to dispose it.
 *
 * Opens two Solid roots sharing one store:
 *   1) Activity bar (toolbar with zones) inside `bar`.
 *   2) Panel content inside `panel` (#sidebar host).
 *
 * @param bar     #activitybar element — Solid mounts the activity bar here
 * @param panel   #sidebar element — Solid mounts the panel content here
 * @param views   view descriptors (top zone)
 * @param actions action descriptors (bottom zone)
 * @param storage injectable storage (defaults to localStorage)
 */
export function mountSidebar(
  bar: HTMLElement,
  panel: HTMLElement,
  views: readonly SidebarViewDescriptor[],
  actions: readonly SidebarAction[],
  storage?: SidebarStorage | null,
): SidebarHandle {
  const safeStorage = storage ?? safeLocalStorage()

  const [state, storeActions] = createAppStore()

  // ── Fix nocx-rp2j: correct initial state ─────────────────────────────
  const firstViewId = views.length > 0 ? views[0].id : ''
  const persistedCollapsed = safeStorage?.getItem(STORAGE_KEY) === '1'

  // Set active view to the first registered view
  if (firstViewId !== state.sidebar.activeViewId) {
    storeActions.setActiveView(firstViewId)
  }

  // Restore persisted collapsed state, or collapse if no views exist
  if (persistedCollapsed || firstViewId === '') {
    if (!state.sidebar.collapsed) {
      storeActions.collapseSidebar()
    }
  }

  // ── Render the activity bar (toolbar) into `bar` ─────────────────────
  const destroyBar = render(
    () => (
      <SidebarSolid
        bar={bar}
        panel={panel}
        views={views}
        actions={actions}
        storage={safeStorage}
        state={state}
        storeActions={storeActions}
      />
    ),
    bar,
  )

  // ── Render the panel content into `panel` (#sidebar host) ────────────
  const destroyPanel = render(() => <PanelRoot state={state} views={views} />, panel)

  return {
    destroy() {
      destroyBar()
      destroyPanel()
    },
  }
}
