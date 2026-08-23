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
 * THE BOTTOM ZONE TAKES ONE KIND OF ENTRY, and that is the whole contract.
 * It was briefly widened to a second — an INDICATOR that opened its own
 * popover and touched neither the panel nor the tabs — for the operations
 * list (nocx-hbdw4).  The widening went back with the thing it was for
 * (nocx-hbdw4.1): "I cannot see that something is running" is answered by an
 * ICON, which a view already has, while a LIST is a place somebody goes to
 * look on purpose, and in this shell such places open the panel.  A widened
 * contract nobody uses is worse than one that was never widened.
 *
 * WHAT A VIEW'S ICON MAY CARRY is the half of that which stayed, because it
 * is what lets the TOP zone answer the complaint at all: a count and an
 * aggregate progress, drawn on the activity-bar button, which is on screen
 * whatever the panel is doing (`SidebarViewDescriptor.status`).  The bar owns
 * the drawing, so two views cannot say "3 running" in two different
 * vocabularies; the view owns only the numbers.
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
import { createEffect, createMemo, createSignal, For, on, onCleanup, Show, untrack } from 'solid-js'
import type { Component } from 'solid-js'
import { SidebarView } from './ui/sidebar-view'
import { ResizeHandle } from './ui/resize-handle'
import { createAppStore, type AppActions, type AppState } from './state'
import { IconButton } from './ui/icon-button'
import { Badge } from './ui/badge'
import { ProgressBar } from './ui/progress-bar'
import type { ActiveOrigin } from './pane-content'
import {
  SIDEBAR_WIDTH_MAX,
  SIDEBAR_WIDTH_MIN,
  SIDEBAR_WIDTH_STEP,
  type SidebarWidthController,
} from './sidebar-width'

// ── Types ──────────────────────────────────────────────────────────────────

/** Props every sidebar view receives from the shell. Views that need
 *  profile scope or visibility gating read these inside effects — the
 *  accessors are reactive, never snapshots. */
export interface SidebarViewProps {
  /** True while THIS view is on screen and the panel is expanded.
   *  Collapsing the sidebar counts as not visible: a view that renders
   *  background work (polling, sampling) must gate it on this. */
  visible: () => boolean
  /** Reactive accessor for the active tab's ports scope — its saved-profile
   *  id, or the reserved "local" for a local shell (nocx-wzc4.8). Null when
   *  the active tab has no ports scope (alias tab, Settings). */
  activeProfileId: () => string | null
  /** Reactive accessor for the ACTIVE tab's origin — the machine a
   *  filesystem-scoped view speaks for (design §5.4): the backend session
   *  and kind of the tab in front, or null when the tab has no origin
   *  (Settings, a nested environment the content cannot speak for). Added
   *  for the Files view; Ports reads only activeProfileId. */
  activeOrigin: () => ActiveOrigin | null
}

/**
 * What a view's activity-bar ICON says about work going on behind it: how
 * many things are running, and how far along they are between them.
 *
 * It exists because the activity bar is the one part of the sidebar that
 * stays on screen whatever the panel is doing, and that is the whole of what
 * "visible from anywhere" needs (nocx-hbdw4.1). The LIST behind the icon is
 * an ordinary view and vanishes with the panel like every other; the count
 * and the bar do not.
 *
 * The bar draws both, from these numbers, so every view that grows a count
 * says it in one vocabulary. A descriptor that carried markup instead would
 * be the second kind of entry all over again.
 */
export interface SidebarViewStatus {
  /** How many things are live. The badge is ABSENT at zero — a badge that
   *  said "0" would be a permanent mark meaning nothing. */
  count: number
  /** Aggregate progress over those things, 0..1, or null when nothing is
   *  running — which is how the bar knows not to be there.
   *
   *  Null and not "0 when idle": zero is a real fraction, and a bar drawn at
   *  zero for a view with nothing to do is motion-free chrome a person has
   *  to learn to ignore. Determinate, never a spinner: a 20-minute upload
   *  must not put permanent motion in somebody's peripheral vision. */
  progress: number | null
}

/** A view whose content is rendered inside the sidebar panel. */
export interface SidebarViewDescriptor {
  readonly id: string
  readonly title: string // panel header, e.g. "EXPLORER"
  readonly icon: Component // activity-bar icon — a component, never markup
  readonly view: Component<SidebarViewProps> // panel body, receives view props
  readonly actions?: Component // per-view header actions (…, refresh, collapse-all)
  /** The view's filter control, declared HERE rather than rendered in the
   *  body — which is the whole point. A filter inside the body scrolls away
   *  with the content it filters, and every panel that had one put it there,
   *  so every panel's filter scrolled away (owner, 2026-08-22). The shell
   *  pins it between the header and the scrolling body, so the behaviour is
   *  the kit's and a panel only has to say WHICH of its children is the
   *  filter — the one thing the kit cannot know for itself. Same shape as
   *  `actions`, in the same place, for the same reason. */
  readonly filter?: Component
  readonly order: number
  /** What the icon says while the panel is elsewhere — a REACTIVE accessor,
   *  read inside the bar's own JSX so the badge and the bar move without the
   *  view being mounted. Absent for a view with nothing to report, which is
   *  most of them. */
  readonly status?: () => SidebarViewStatus | null
}

/** An action button in the bottom zone (global actions, never opens panel). */
export interface SidebarAction {
  readonly id: string
  readonly title: string
  readonly icon: Component
  readonly onActivate: () => void
}

/** The sidebar's remembered state, and the seam that records a change.
 *
 *  It used to be `localStorage` under `nocx.sidebar.collapsed`, which is
 *  precisely the ad-hoc pattern ADR-0033 ends: localStorage may not carry
 *  facts. The panel's collapse and its active view are UI state — what the
 *  app must remember without being asked — so they live in the UI-state
 *  document on the Go side and are reached over the control plane.
 *
 *  Injectable and null-able, so a test needs no transport and a shell
 *  without a backend simply starts expanded on the first view. */
export interface SidebarPersistence {
  /** Collapsed at boot, as the document had it. */
  readonly collapsed: boolean
  /** The view that was on screen, or "" for none. An id this build no
   *  longer registers falls back to the first view — the ordinary path for
   *  a view that has since been renamed or removed. */
  readonly activeViewId: string
  /** Record a change. Fire-and-forget: the write is coalesced on the Go
   *  side, and a failed one never reverts what is on screen. */
  save(state: { collapsed: boolean; activeViewId: string }): void
}

/** Handle returned by mountSidebar. */
export interface SidebarHandle {
  destroy(): void
  /** Reveal-or-focus a view from outside the sidebar (keybinding): expands
   *  the panel on the view when it is hidden, focuses the view's
   *  activity-bar button when it is already on screen. */
  revealView(viewId: string): void
}

interface PanelRootProps {
  state: AppState
  views: readonly SidebarViewDescriptor[]
  getActiveProfileId: () => string | null
  getActiveOrigin: () => ActiveOrigin | null
  /** The width controller (nocx-qmcu) — when present the panel renders the
   *  kit ResizeHandle at its trailing edge and the drag resizes #sidebar. */
  resize?: SidebarWidthController
}

function PanelRoot(props: PanelRootProps) {
  const activeDesc = createMemo(
    () => props.views.find((v) => v.id === props.state.sidebar.activeViewId) ?? null,
  )

  // The handle's aria-valuenow is a projection of the controller's width —
  // the controller is the single owner, the signal is pure display state.
  // The initializer is a one-shot read (the controller is stable per mount),
  // so it is untracked — the subscription effect below is the tracked path.
  const [width, setWidth] = createSignal(untrack(() => props.resize?.width ?? 0))
  createEffect(() => {
    if (!props.resize) return
    const unsub = props.resize.subscribe(setWidth)
    onCleanup(unsub)
  })

  return (
    <Show when={activeDesc()}>
      <ActiveView
        desc={activeDesc()!}
        collapsed={() => props.state.sidebar.collapsed}
        getActiveProfileId={props.getActiveProfileId}
        getActiveOrigin={props.getActiveOrigin}
      />
      {/* The handle is the flex row's trailing slot (see #sidebar in
          style.css): a real flex item, never an overlay, so it can neither
          cover the view's scrollbar nor be covered by it. */}
      <Show when={props.resize}>
        <ResizeHandle
          ariaLabel="Resize sidebar"
          value={width()}
          min={SIDEBAR_WIDTH_MIN}
          max={SIDEBAR_WIDTH_MAX}
          step={SIDEBAR_WIDTH_STEP}
          onChange={(w) => props.resize!.apply(w)}
          onCommit={(w) => props.resize!.apply(w, { persist: true })}
          onDragStateChange={(dragging) => props.resize!.setDragging(dragging)}
        />
      </Show>
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
function ActiveView(props: {
  desc: SidebarViewDescriptor
  collapsed: () => boolean
  getActiveProfileId: () => string | null
  getActiveOrigin: () => ActiveOrigin | null
}) {
  // Only the active view renders, so "visible" is exactly the panel's
  // expanded state — a collapsed sidebar is a hidden view (nocx-wzc4.7).
  const visible = () => !props.collapsed()

  // A TERNARY, never `<Show>`. `<Show when={props.desc.filter}>` looks like
  // the guard and is not one: the JSX evaluates to Show's memo whether or
  // not its condition holds, and a memo is truthy — so the shell's own
  // `<Show when={props.filter}>` was always taken and EVERY panel carried an
  // empty filter row and an empty actions row. It cost nothing while those
  // rows had no box of their own; it costs a strip of dead panel the moment
  // the filter row carries the shell's inset (nocx-708q.3). The read stays
  // reactive because a JSX attribute expression compiles to a getter, so
  // switching views re-evaluates it — which is the property the `Dynamic`
  // below exists for in the first place.
  return (
    <SidebarView
      title={props.desc.title}
      actions={props.desc.actions ? <Dynamic component={props.desc.actions} /> : undefined}
      filter={props.desc.filter ? <Dynamic component={props.desc.filter} /> : undefined}
    >
      <Dynamic
        component={props.desc.view}
        visible={visible}
        activeProfileId={props.getActiveProfileId}
        activeOrigin={props.getActiveOrigin}
      />
    </SidebarView>
  )
}

// ── Activity bar component ─────────────────────────────────────────────────

interface SidebarSolidProps {
  bar: HTMLElement
  panel: HTMLElement
  views: readonly SidebarViewDescriptor[]
  actions: readonly SidebarAction[]
  persistence: SidebarPersistence | null
  state: AppState
  storeActions: AppActions
  /** Reactive: true while the ACTIVE tab is a Settings tab (nocx-3e3b).
   *  Wired by the composition root to the active tab's surface type. */
  getActivePaneIsSettings: () => boolean
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
    // Fall back to the first item in toolbar order (views, then the
    // bottom zone in the order it renders).
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

  // ── Settings-tab transient collapse (nocx-3e3b) ─────────────────────
  // Every sidebar view speaks for the machine a terminal tab is on; a
  // Settings tab is not a place, so arriving on one collapses the panel and
  // the width goes to the settings content. The collapse is a consequence of
  // where the user is, NEVER an edit to their preference: the pre-Settings
  // collapsed state is snapshotted on arrival and restored on departure, and
  // the collapsed-preference write below stands down while the collapse is
  // in force so the transient state cannot leak into the next boot.
  //
  // The rule fires only on the settings-mode EDGES (false→true, true→false):
  // once the user is on Settings, opening or closing the panel is theirs
  // until they leave — a user who deliberately opens the sidebar there gets
  // it, and it stays until they close it.
  let preSettingsCollapsed: boolean | null = null
  createEffect(
    on(
      () => props.getActivePaneIsSettings(),
      (isSettings, wasSettings) => {
        const arriving = isSettings && !wasSettings
        const leaving = !isSettings && wasSettings
        if (arriving) {
          // Snapshot the user's state; collapse only when the panel was
          // open. `untrack` is deliberate — the snapshot is a point-in-time
          // read, never a tracked dependency.
          const preCollapsed = untrack(() => props.state.sidebar.collapsed)
          preSettingsCollapsed = preCollapsed
          if (!preCollapsed) props.storeActions.collapseSidebar()
        } else if (leaving && preSettingsCollapsed !== null) {
          const wanted = preSettingsCollapsed
          preSettingsCollapsed = null
          if (untrack(() => props.state.sidebar.collapsed) !== wanted) {
            props.storeActions.toggleSidebar()
          }
        }
      },
    ),
  )

  // ── Persist the collapse and the active view ──────────────────────────
  // Stands down while the Settings collapse is in force (nocx-3e3b): the
  // transient collapse is a consequence of where the user is, and persisting
  // it would rewrite the state the next boot restores from.
  createEffect(() => {
    if (props.getActivePaneIsSettings()) return
    props.persistence?.save({
      collapsed: props.state.sidebar.collapsed,
      // "" rather than null on the wire: the document's activeViewId is a
      // string whose empty value means "no view", so the absence has one
      // spelling instead of two.
      activeViewId: props.state.sidebar.activeViewId ?? '',
    })
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
          const btn = props.bar.querySelector<HTMLElement>(`button[data-view="${focusAnchor}"]`)
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
    const buttons = [...toolbar.querySelectorAll<HTMLElement>('button')]
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
          {(view) => {
            const status = () => view.status?.() ?? null
            const count = () => status()?.count ?? 0
            const progress = () => status()?.progress ?? null
            /* The name carries the count, because somebody who reaches this
               button with the keyboard cannot see the badge. */
            const label = () => (count() === 0 ? view.title : `${view.title} — ${count()} running`)
            return (
              <IconButton
                size="lg"
                selected={
                  view.id === props.state.sidebar.activeViewId && !props.state.sidebar.collapsed
                }
                data-view={view.id}
                title={label()}
                ariaLabel={label()}
                tabIndex={view.id === tabbableId() ? 0 : -1}
                railIndicator={true}
                onClick={() => handleViewClick(view)}
              >
                <view.icon />
                {/* Inside the button, not beside it: the button is the
                    positioning context and the only toolbar stop, so a mark
                    drawn on it cannot become a second thing to tab to. Both
                    are pointer-events:none in CSS — the whole button is one
                    target. */}
                <Show when={count() > 0}>
                  <span class="activity-bar-badge" data-view-badge={view.id}>
                    <Badge tone="info">{String(count())}</Badge>
                  </span>
                </Show>
                {/* The guard is on PRESENCE and not on truthiness: zero is a
                    real fraction, and `when={progress()}` would hide the bar
                    for a transfer that has not moved a byte yet. */}
                <Show when={progress() !== null}>
                  <span class="activity-bar-progress" data-view-progress={view.id}>
                    {/* Non-null inside this branch by the guard above, which
                        is what the cast rests on. Deliberately not
                        `progress() ?? 0`: a default painted at the render
                        site is a fraction the view never produced and cannot
                        see. */}
                    <ProgressBar
                      value={progress() as number}
                      ariaLabel={`${view.title} progress`}
                    />
                  </span>
                </Show>
              </IconButton>
            )
          }}
        </For>
      </div>

      {/* Spacer pushes bottom zone to the bottom */}
      <div class="activity-bar-spacer" />

      {/* Bottom zone: actions. */}
      <div class="activity-bar-zone activity-bar-bottom" role="group" aria-label="Actions">
        <For each={props.actions}>
          {(action) => (
            <IconButton
              size="lg"
              data-action={action.id}
              title={action.title}
              ariaLabel={action.title}
              tabIndex={action.id === tabbableId() ? 0 : -1}
              onClick={() => handleActionClick(action)}
            >
              <action.icon />
            </IconButton>
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
 * @param bar                #activitybar element — Solid mounts the activity bar here
 * @param panel              #sidebar element — Solid mounts the panel content here
 * @param views              view descriptors (top zone)
 * @param actions            action descriptors (bottom zone)
 * @param persistence        the sidebar's remembered state and the seam
 *                           that records it (ADR-0033), from the UI-state
 *                           document. Null starts expanded on the first
 *                           view and remembers nothing.
 * @param getActiveProfileId reactive accessor for the active tab's
 *                           saved-profile id, forwarded to every view
 *                           (see SidebarViewProps). Defaults to null —
 *                           views that need real scope provide one.
 * @param getActiveOrigin    reactive accessor for the active tab's origin
 *                           (see SidebarViewProps.activeOrigin). Defaults
 *                           to null — views that need real scope provide
 *                           one (the Files panel, design §5.4).
 * @param resize             the width controller (nocx-qmcu), created by
 *                           the composition root from the UI-state
 *                           document's width. When present the panel
 *                           renders the kit ResizeHandle and drags resize
 *                           #sidebar.
 * @param getActivePaneIsSettings reactive accessor for "the active tab is a
 *                           Settings tab" (nocx-3e3b): while true, the
 *                           panel collapses transiently on arrival — the
 *                           user's pre-Settings state is restored on
 *                           departure, and neither the collapsed preference
 *                           nor the width is written. Defaults to false —
 *                           the shell without a Settings surface never
 *                           collapses for it.
 */
export function mountSidebar(
  bar: HTMLElement,
  panel: HTMLElement,
  views: readonly SidebarViewDescriptor[],
  actions: readonly SidebarAction[],
  persistence?: SidebarPersistence | null,
  getActiveProfileId?: () => string | null,
  getActiveOrigin?: () => ActiveOrigin | null,
  resize?: SidebarWidthController,
  getActivePaneIsSettings?: () => boolean,
): SidebarHandle {
  const activeProfileId = getActiveProfileId ?? (() => null)
  const activeOrigin = getActiveOrigin ?? (() => null)
  const activePaneIsSettings = getActivePaneIsSettings ?? (() => false)

  const [state, storeActions] = createAppStore()

  // ── Fix nocx-rp2j: correct initial state ─────────────────────────────
  const firstViewId = views.length > 0 ? views[0].id : ''
  const persistedCollapsed = persistence?.collapsed === true
  // The remembered view, when this build still registers it. A renamed or
  // removed id falls back to the first view rather than leaving the panel
  // open on nothing — a value the app no longer understands is repaired,
  // never treated as an error (ADR-0033 §4).
  const rememberedViewId =
    persistence && views.some((v) => v.id === persistence.activeViewId)
      ? persistence.activeViewId
      : firstViewId

  // Set active view to the remembered one, else the first registered view
  if (rememberedViewId !== state.sidebar.activeViewId) {
    storeActions.setActiveView(rememberedViewId)
  }

  // Restore persisted collapsed state, or collapse if no views exist
  if (persistedCollapsed || rememberedViewId === '') {
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
        persistence={persistence ?? null}
        state={state}
        storeActions={storeActions}
        getActivePaneIsSettings={activePaneIsSettings}
      />
    ),
    bar,
  )
  const destroyPanel = render(
    () => (
      <PanelRoot
        state={state}
        views={views}
        getActiveProfileId={activeProfileId}
        getActiveOrigin={activeOrigin}
        resize={resize}
      />
    ),
    panel,
  )

  /** Reveal-or-focus a view (keybinding entry, nocx-wzc4.7): expand the
   *  panel on the view when it is hidden; focus its activity-bar button
   *  when it is already on screen. Unknown ids are a no-op — activating a
   *  view that is not registered would orphan the panel. */
  const revealView = (viewId: string): void => {
    if (!views.some((v) => v.id === viewId)) return
    const { collapsed, activeViewId } = state.sidebar
    if (activeViewId === viewId && !collapsed) {
      const btn = bar.querySelector<HTMLElement>(`button[data-view="${viewId}"]`)
      btn?.focus()
      return
    }
    // setActiveView switches the active view but never expands a collapsed
    // panel by itself — the icon-click path compensates the same way.
    storeActions.setActiveView(viewId)
    if (collapsed) storeActions.toggleSidebar()
  }

  return {
    destroy() {
      destroyBar()
      destroyPanel()
    },
    revealView,
  }
}
