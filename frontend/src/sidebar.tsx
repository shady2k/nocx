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
 * The bottom zone deliberately has two typed entry kinds: global actions
 * and one optional indicator. The previous operations-list indicator was
 * removed (nocx-hbdw4.1); the updater now needs a persistent status beside
 * Settings without taking tab space (nocx-1ebrs.66.1.5). The bar draws the
 * icon and level; activation opens the one kit offer owned outside this
 * connection-scoped sidebar. It never selects a view or changes the panel.
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
import { StatusDot, type StatusDotTone } from './ui/status-dot'
import type { ActiveOrigin } from './pane-content'
import {
  SIDEBAR_WIDTH_MAX,
  SIDEBAR_WIDTH_MIN,
  SIDEBAR_WIDTH_STEP,
  type SidebarWidthController,
} from './sidebar-width'
import { createAppVisibility, type AppVisibility } from './app-visible'
import { t, tPlural } from './i18n/locale-model'

// ── Types ──────────────────────────────────────────────────────────────────

/** Props every sidebar view receives from the shell. Views that need
 *  profile scope or visibility gating read these inside effects — the
 *  accessors are reactive, never snapshots. */
export interface SidebarViewProps {
  /** True while THIS view is on screen, the panel is expanded, and the
   *  application window is visible. A panel that renders background work
   *  (polling, sampling) must gate it on this complete visibility fact. */
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
export type SidebarCountKind = 'running' | 'unread'

/** The word the bar says for each kind of count. The map lives HERE, beside
 *  the bar that draws it, because the invariant this file opens with is that
 *  the BAR owns the wording and a view owns only the numbers — two views must
 *  not describe the same number in two vocabularies.
 *
 *  What changed when Notifications arrived is that "running" stopped being
 *  the only true sentence. Background operations run; unread notifications do
 *  not, and a bell whose accessible name read "Notifications — 3 running"
 *  told a screen-reader user something false. The fix is NOT to let the view
 *  pass a string — that is exactly the second vocabulary the invariant
 *  forbids. The view names a KIND from this closed set and the bar keeps the
 *  wording. */
const COUNT_WORD: Record<SidebarCountKind, { key: string; english: string }> = {
  running: { key: 'sidebar.count.running', english: '{count} running' },
  unread: { key: 'sidebar.count.unread', english: '{count} unread' },
}

export interface SidebarViewStatus {
  /** How many things are live. The badge is ABSENT at zero — a badge that
   *  said "0" would be a permanent mark meaning nothing. */
  count: number
  /** WHAT is being counted, which decides the word the bar says. Required
   *  rather than defaulted to 'running': a default is how the wrong word gets
   *  read as the right one, and there are few enough callers that saying it
   *  costs nothing. */
  kind: SidebarCountKind
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
  /** The panel header and the rail's accessible name, READ at render rather
   *  than captured — an accessor for the same reason `status` is one. A
   *  string built from `t()` when the descriptor is constructed is the
   *  language the app started in, and the sidebar outlives a locale switch
   *  (it is remounted per connection, not per language). The accessor reads
   *  the locale signal inside the bar's own scope, so the header, the count
   *  suffix and the aria label all switch together. */
  readonly title: () => string
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

/** A reactive indicator in the bottom zone, owned by the composition root. */
export interface SidebarIndicator {
  /** Resolved at render — see SidebarViewDescriptor.title. */
  readonly title: () => string
  readonly icon: Component
  readonly available: () => boolean
  readonly level: () => StatusDotTone
  readonly onActivate: () => void
}

/** An action button in the bottom zone (global actions, never opens panel). */
export interface SidebarAction {
  readonly id: string
  /** Resolved at render — see SidebarViewDescriptor.title. */
  readonly title: () => string
  readonly icon: Component
  readonly onActivate: () => void
}

/** The panel a tab remembers (ADR-x7cvh8e1, `nocx-ouru5.1.18`): which
 *  sidebar view it shows, or "" for a panel showing nothing, and whether its
 *  rail is collapsed.
 *
 *  It is the TAB's state, not the window's — switching tabs restores the
 *  panel each one left behind — so it is stored on the tab's layout row
 *  beside its colour and pinning, never in a store of its own. */
interface SidebarPanel {
  readonly view: string
  readonly collapsed: boolean
}

/** The panel the front tab remembers, and the seam that records a change.
 *
 *  `panel` is null when no tab holds one: a Settings pane is in no chain, and
 *  a window with no tab has nothing to remember. Null is therefore "no panel"
 *  and renders as the rail collapsed, never as "the last panel" — a second
 *  answer to which panel is open is exactly the defect this seam removes
 *  (ADR-x7cvh8e1).
 *
 *  Injectable and null-able, so a test needs no transport and a shell
 *  without a backend simply starts expanded on the first view. */
export interface SidebarPersistence {
  /** The front tab's panel at mount, or null when no tab holds one. An id
   *  this build no longer registers is repaired to the first view — the
   *  ordinary path for a view that has since been renamed or removed. */
  readonly panel: SidebarPanel | null
  /** Record a change against the front tab. Fire-and-forget: the write is a
   *  layout call, and a failed one never reverts what is on screen. */
  save(panel: SidebarPanel): void
}

/** Handle returned by mountSidebar. */
export interface SidebarHandle {
  destroy(): void
  /** Reveal-or-focus a view from outside the sidebar (keybinding): expands
   *  the panel on the view when it is hidden, focuses the view's
   *  activity-bar button when it is already on screen. */
  revealView(viewId: string): void
  /** Show the panel a tab remembers (nocx-ouru5.1.18), or null for a tab
   *  with no panel at all — a Settings pane, which is in no chain.
   *
   *  This is the tab's state ARRIVING, not a person's change, so it applies
   *  without writing: the persistence seam is for what the person does. */
  showPanel(panel: SidebarPanel | null): void
}

/** The state the panel starts in, from the front tab's remembered panel.
 *
 *  A shell with no persistence starts expanded on the first view. A tab with
 *  no panel (or none recorded at all) starts with the rail collapsed: there
 *  is nothing to show, and "the last panel" is not a fact anybody holds. */
function initialPanel(
  views: readonly SidebarViewDescriptor[],
  firstViewId: string,
  persistence?: SidebarPersistence | null,
): SidebarPanel {
  if (!persistence) return { view: firstViewId, collapsed: false }
  const remembered = persistence.panel
  if (remembered === null || remembered.view === '') return { view: '', collapsed: true }
  if (views.some((v) => v.id === remembered.view)) {
    return { view: remembered.view, collapsed: remembered.collapsed }
  }
  // A renamed or removed view id is repaired to the first view, never obeyed.
  return { view: firstViewId, collapsed: remembered.collapsed }
}

/** The panel to APPLY from a tab, repaired the same way. */
function applyablePanel(
  panel: SidebarPanel | null,
  views: readonly SidebarViewDescriptor[],
  firstViewId: string,
): SidebarPanel {
  if (panel === null || panel.view === '') return { view: '', collapsed: true }
  if (!views.some((v) => v.id === panel.view)) {
    return { view: firstViewId, collapsed: panel.collapsed }
  }
  return panel
}

interface PanelRootProps {
  state: AppState
  views: readonly SidebarViewDescriptor[]
  getActiveProfileId: () => string | null
  getActiveOrigin: () => ActiveOrigin | null
  appVisibility: AppVisibility
  /** The width controller (nocx-qmcu) — when present the panel renders the
   *  kit ResizeHandle at its LEADING edge, facing the panes, and the drag
   *  resizes #sidebar (nocx-crjft). */
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
      {/* The handle is the flex row's LEADING slot (see #sidebar in
          style.css): a real flex item, never an overlay, so it can neither
          cover the view's scrollbar nor be covered by it. First in the DOM
          because it is first on screen — the panel sits at the window's
          trailing edge, so the edge a user drags is its left one, and
          `pane="after"` is what tells the kit component that the width it
          reports belongs to the pane on the far side of it. */}
      <Show when={props.resize}>
        <ResizeHandle
          ariaLabel={t('sidebar.resize', 'Resize sidebar')}
          pane="after"
          value={width()}
          min={SIDEBAR_WIDTH_MIN}
          max={SIDEBAR_WIDTH_MAX}
          step={SIDEBAR_WIDTH_STEP}
          onChange={(w) => props.resize!.apply(w)}
          onCommit={(w) => props.resize!.apply(w, { persist: true })}
          onDragStateChange={(dragging) => props.resize!.setDragging(dragging)}
        />
      </Show>
      <ActiveView
        desc={activeDesc()!}
        appVisible={props.appVisibility.visible}
        collapsed={() => props.state.sidebar.collapsed}
        getActiveProfileId={props.getActiveProfileId}
        getActiveOrigin={props.getActiveOrigin}
      />
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
  appVisible: () => boolean
  getActiveProfileId: () => string | null
  getActiveOrigin: () => ActiveOrigin | null
}) {
  // Only the active view renders. The sidebar is visible only when expanded
  // AND the application window is visible, so every background-work panel
  // shares one shell-owned window-visibility signal.
  const visible = () => !props.collapsed() && props.appVisible()

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
      title={props.desc.title()}
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
  indicator?: SidebarIndicator
  persistence: SidebarPersistence | null
  state: AppState
  storeActions: AppActions
  /** The panel last APPLIED from a tab, so the persist effect can tell a
   *  person's change from the tab's own state arriving. Shared with
   *  `showPanel` because both live outside this component. */
  panelState: { applied: SidebarPanel | null }
  /** Where the keyboard goes back to when an ICON CLICK collapses the panel
   *  (the view's own button). Shared with `showPanel`, which CLEARS it: a
   *  collapse caused by a tab switch is not a click, and pulling the
   *  keyboard out of the terminal the switch just focused is the defect
   *  nocx-ouru5.1.18's first gate found. */
  focusReturn: { anchor: string | null }
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
  //
  // EXPANDING A TAB WITH NO REMEMBERED VIEW PICKS THE DEFAULT ONE. A tab that
  // has never opened a panel is `{view: '', collapsed: true}`; toggling the
  // collapse alone would leave it showing nothing, so the panel would never
  // open (ADR-x7cvh8e1; codex P2). The registry is the component's, which is
  // why this rule lives here and not in the store's transition.
  const togglePanel = (): void => {
    if (!props.state.sidebar.collapsed) {
      props.storeActions.toggleSidebar()
      return
    }
    const remembered = props.state.sidebar.activeViewId ?? ''
    const view = props.views.some((v) => v.id === remembered)
      ? remembered
      : (props.views[0]?.id ?? '')
    props.storeActions.setPanel(view, false)
  }
  createEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key === 'b') {
        e.preventDefault()
        togglePanel()
      }
    }
    document.addEventListener('keydown', handler)
    onCleanup(() => document.removeEventListener('keydown', handler))
  })

  // ── The panel follows the TAB that is in front (nocx-ouru5.1.18) ─────
  // The panel is the tab's, not the window's: switching tabs restores the
  // panel each one left behind, and a tab that has never opened one shows
  // the rail collapsed. The composition root applies a tab's panel through
  // `showPanel` on every activation — including a Settings tab, whose null
  // panel collapses the rail for as long as the person is there. That
  // replaces the old transient-collapse machinery: there is no longer a
  // window-wide preference to snapshot and restore around Settings, because
  // there is no window-wide panel at all.
  //
  // What is written back is the PERSON's change: a view click, Ctrl/Cmd+B,
  // a reveal. `panelState.applied` is what arrived from the tab, so the
  // effect can tell the two apart and does not echo an apply back as an
  // edit.
  createEffect(() => {
    const current: SidebarPanel = {
      view: props.state.sidebar.activeViewId ?? '',
      collapsed: props.state.sidebar.collapsed,
    }
    const applied = props.panelState.applied
    if (
      applied !== null &&
      applied.view === current.view &&
      applied.collapsed === current.collapsed
    ) {
      return
    }
    props.panelState.applied = current
    props.persistence?.save(current)
  })

  // ── Collapsed class on the panel host (CSS in style.css) ──────────────
  createEffect(() => {
    props.panel.classList.toggle('collapsed', effectivelyCollapsed())
  })

  // ── Focus return on collapse ─────────────────────────────────────────
  createEffect(
    on(
      () => props.state.sidebar.collapsed,
      (collapsed, prev) => {
        const anchor = props.focusReturn.anchor
        if (prev === false && collapsed && anchor) {
          const btn = props.bar.querySelector<HTMLElement>(`button[data-view="${anchor}"]`)
          btn?.focus()
          props.focusReturn.anchor = null
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
    props.focusReturn.anchor = view.id
    const { collapsed } = props.state.sidebar
    if (collapsed) {
      // ONE CHANGE, ONE WRITE. Showing a view and expanding the rail is one
      // choice, but it used to be two store updates — two panel writes to the
      // same tab, issued back to back — and their answers can come back in
      // either order: measured on a 50× repeat, the superseded write landed
      // last and the tab's own row ended collapsed on Ports (nocx-ouru5.1.18).
      props.storeActions.setPanel(view.id, false)
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
    <div
      role="toolbar"
      aria-label={t('sidebar.activityBar', 'Activity bar')}
      class="activity-bar"
      onKeyDown={handleKeyDown}
    >
      {/* Top zone: views */}
      <div
        class="activity-bar-zone activity-bar-top"
        role="group"
        aria-label={t('sidebar.views', 'Views')}
      >
        <For each={props.views}>
          {(view) => {
            const status = () => view.status?.() ?? null
            const count = () => status()?.count ?? 0
            const progress = () => status()?.progress ?? null
            /* The name carries the count, because somebody who reaches this
               button with the keyboard cannot see the badge. */
            const kind = () => status()?.kind ?? 'running'
            const label = () =>
              count() === 0
                ? view.title()
                : `${view.title()} — ${tPlural(
                    COUNT_WORD[kind()].key,
                    count(),
                    COUNT_WORD[kind()].english,
                  )}`
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
                    <Badge tone="info" variant="solid">
                      {count() > 99 ? '99+' : String(count())}
                    </Badge>
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
                      ariaLabel={view.title() + ' ' + t('ops.progress.aria', 'progress')}
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
      <div
        class="activity-bar-zone activity-bar-bottom"
        role="group"
        aria-label={t('sidebar.actions', 'Actions')}
      >
        <For each={props.actions}>
          {(action) => (
            <IconButton
              size="lg"
              data-action={action.id}
              title={action.title()}
              ariaLabel={action.title()}
              tabIndex={action.id === tabbableId() ? 0 : -1}
              onClick={() => handleActionClick(action)}
            >
              <action.icon />
            </IconButton>
          )}
        </For>
        <Show when={props.indicator?.available()}>
          <IconButton
            size="lg"
            data-update-indicator
            tabIndex={tabbableId() === null ? 0 : -1}
            title={props.indicator?.title() ?? t('app.update.available', 'Update available')}
            ariaLabel={props.indicator?.title() ?? t('app.update.available', 'Update available')}
            onClick={() => props.indicator?.onActivate()}
          >
            <StatusDot tone={props.indicator?.level() ?? 'neutral'} accessibleName="Update status">
              <Dynamic component={props.indicator?.icon} />
            </StatusDot>
          </IconButton>
        </Show>
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
 * @param persistence        the panel the front tab remembers and the seam
 *                           that records a change (nocx-ouru5.1.18), stored
 *                           on the tab's layout row. Null (no persistence)
 *                           starts expanded on the first view and remembers
 *                           nothing.
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
 * @param indicator          the one optional status beside Settings.
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
  indicator?: SidebarIndicator,
): SidebarHandle {
  const activeProfileId = getActiveProfileId ?? (() => null)
  const activeOrigin = getActiveOrigin ?? (() => null)

  const [state, storeActions] = createAppStore()

  // ── The state the panel starts in ────────────────────────────────────
  // A shell with no persistence (dev-web, a test) starts expanded on the
  // first view and remembers nothing. With persistence, the panel is the
  // front tab's: its view and its collapse, or nothing at all when no tab
  // holds one — which renders as the rail collapsed, never as "the last
  // panel" (there is no last panel any more).
  //
  // A view id this build no longer registers is REPAIRED to the first view
  // rather than leaving the panel open on nothing — a value the app no
  // longer understands is repaired, never treated as an error (ADR-0048 §4).
  const firstViewId = views.length > 0 ? views[0].id : ''
  const startPanel = initialPanel(views, firstViewId, persistence)
  storeActions.setPanel(startPanel.view, startPanel.collapsed)

  // The panel last APPLIED from a tab, shared with the persist effect below
  // so the tab's state arriving is not written back as a person's edit.
  const panelState: { applied: SidebarPanel | null } = { applied: startPanel }
  // Where the keyboard returns to when an icon click collapses the panel. See
  // SidebarSolidProps.focusReturn.
  const focusReturn: { anchor: string | null } = { anchor: null }

  const appVisibility = createAppVisibility()

  // ── Render the activity bar (toolbar) into `bar` ─────────────────────
  const destroyBar = render(
    () => (
      <SidebarSolid
        bar={bar}
        panel={panel}
        views={views}
        actions={actions}
        indicator={indicator}
        persistence={persistence ?? null}
        state={state}
        storeActions={storeActions}
        panelState={panelState}
        focusReturn={focusReturn}
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
        appVisibility={appVisibility}
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

  /** Show the panel a tab remembers (nocx-ouru5.1.18), or null for a tab
   *  with no panel at all. Applied, never written: this is the tab's state
   *  arriving, and the persist effect above is for what the person does. */
  const showPanel = (next: SidebarPanel | null): void => {
    const panel = applyablePanel(next, views, firstViewId)
    // A tab switch is not a click: whatever a previous icon click asked the
    // keyboard to return to, this collapse must not take the keyboard away
    // from the terminal the switch just focused.
    focusReturn.anchor = null
    panelState.applied = panel
    storeActions.setPanel(panel.view, panel.collapsed)
  }

  return {
    destroy() {
      appVisibility.destroy()
      destroyBar()
      destroyPanel()
    },
    revealView,
    showPanel,
  }
}
