// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { fireEvent } from '@solidjs/testing-library'
import { createEffect, createSignal } from 'solid-js'
import {
  mountSidebar,
  type SidebarHandle,
  type SidebarViewDescriptor,
  type SidebarAction,
  type SidebarViewProps,
} from './sidebar'
import { createSidebarWidthController } from './sidebar-width'
import type { Component } from 'solid-js'

// VS Code-style shell sidebar: zones for views (top) and actions (bottom).
// Views toggle the panel and switch content; actions never touch the panel.

// ── Test helpers ──────────────────────────────────────────────────────────

/** Minimal icon component for tests. */
const TestIcon: Component = () => <svg data-test-icon="1" />

/** Test view — renders its own id so the panel content is observable. */
function TestView(id: string): Component {
  return () => <div data-view-body={id}>{id} content</div>
}

const TWO_VIEWS: SidebarViewDescriptor[] = [
  { id: 'alpha', title: 'Alpha', icon: TestIcon, view: TestView('alpha'), order: 0 },
  { id: 'beta', title: 'Beta', icon: TestIcon, view: TestView('beta'), order: 1 },
]

const SETTINGS_ACTION: SidebarAction = {
  id: 'settings',
  title: 'Settings',
  icon: TestIcon,
  onActivate: () => {},
}

function mount(): { bar: HTMLElement; panel: HTMLElement } {
  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)
  return { bar, panel }
}

function viewBtn(bar: HTMLElement, viewId: string): HTMLElement {
  const el = bar.querySelector<HTMLElement>(`button[data-view="${viewId}"]`)
  if (!el) throw new Error(`no view button for ${viewId}`)
  return el
}

function actionBtn(bar: HTMLElement, actionId: string): HTMLElement {
  const el = bar.querySelector<HTMLElement>(`button[data-action="${actionId}"]`)
  if (!el) throw new Error(`no action button for ${actionId}`)
  return el
}

function pressToggleKey(): void {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'b', ctrlKey: true, bubbles: true }))
}

function panelTitle(panel: HTMLElement): string | null {
  const h2 = panel.querySelector('.ui-sidebar-view__header h2')
  return h2?.textContent ?? null
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('sidebar', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('renders view buttons in the top zone and action buttons in the bottom zone', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    // View buttons
    expect(viewBtn(bar, 'alpha')).toBeTruthy()
    expect(viewBtn(bar, 'beta')).toBeTruthy()

    // Action button
    expect(actionBtn(bar, 'settings')).toBeTruthy()

    // Total toolbar buttons
    expect(bar.querySelectorAll('button')).toHaveLength(3)
  })

  it('starts expanded on the first view with no empty panel (nocx-rp2j fix)', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(viewBtn(bar, 'alpha').getAttribute('aria-selected')).toBe('true')
    expect(panelTitle(panel)).toBe('Alpha')
  })

  it("collapses when the active view's button is clicked, and re-opens on the next click", () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    viewBtn(bar, 'alpha').click()
    expect(panel.classList.contains('collapsed')).toBe(true)
    // VS Code drops the active highlight when the panel is closed
    expect(viewBtn(bar, 'alpha').getAttribute('aria-selected')).toBeNull()

    viewBtn(bar, 'alpha').click()
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(viewBtn(bar, 'alpha').getAttribute('aria-selected')).toBe('true')
  })

  it('switches views when another button is clicked, keeping the panel open', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    viewBtn(bar, 'beta').click()
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Beta')
    expect(viewBtn(bar, 'beta').getAttribute('aria-selected')).toBe('true')
    expect(viewBtn(bar, 'alpha').getAttribute('aria-selected')).toBeNull()
  })

  it('opens the panel on the clicked view when collapsed', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    viewBtn(bar, 'alpha').click() // collapse
    viewBtn(bar, 'beta').click() // re-open on another view

    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Beta')
  })

  it('toggles the panel with Ctrl/Cmd+B', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    pressToggleKey()
    expect(panel.classList.contains('collapsed')).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'b', metaKey: true, bubbles: true }))
    expect(panel.classList.contains('collapsed')).toBe(false)
  })

  it('ignores bare B without a modifier', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'b', bubbles: true }))
    expect(panel.classList.contains('collapsed')).toBe(false)
  })

  it('persists the collapsed state and restores it on the next mount', () => {
    // The persistence seam is the UI-state document (ADR-0033), not
    // localStorage. The fake stands in for the round trip: whatever the
    // first mount saved is what the second one boots from.
    const saved = { collapsed: false, activeViewId: '' }
    const persistence = {
      get collapsed() {
        return saved.collapsed
      },
      get activeViewId() {
        return saved.activeViewId
      },
      save: (next: { collapsed: boolean; activeViewId: string }) => {
        saved.collapsed = next.collapsed
        saved.activeViewId = next.activeViewId
      },
    }

    const first = mount()
    mountSidebar(first.bar, first.panel, TWO_VIEWS, [SETTINGS_ACTION], persistence)
    viewBtn(first.bar, 'alpha').click() // collapse
    expect(saved.collapsed).toBe(true)

    document.body.replaceChildren()
    const second = mount()
    mountSidebar(second.bar, second.panel, TWO_VIEWS, [SETTINGS_ACTION], persistence)

    expect(second.panel.classList.contains('collapsed')).toBe(true)
    expect(viewBtn(second.bar, 'alpha').getAttribute('aria-selected')).toBeNull()
  })

  it('restores the view that was on screen, and falls back when it is gone', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION], {
      collapsed: false,
      activeViewId: 'beta',
      save: () => {},
    })
    expect(panelTitle(panel)).toBe('Beta')
    expect(panel.classList.contains('collapsed')).toBe(false)

    // A view id this build no longer registers is repaired, not obeyed: the
    // panel opens on the first view rather than on nothing.
    document.body.replaceChildren()
    const next = mount()
    mountSidebar(next.bar, next.panel, TWO_VIEWS, [SETTINGS_ACTION], {
      collapsed: false,
      activeViewId: 'a-view-that-was-renamed',
      save: () => {},
    })
    expect(panelTitle(next.panel)).toBe('Alpha')
  })

  it('remembers nothing, and starts open, when there is no persistence', () => {
    // The shell without a backend (dev-web, a test). Absence is an ordinary
    // state: the panel opens on the first view and no write is attempted.
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION], null)
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Alpha')
  })

  it('triggers onActivate when an action button is clicked, without touching the panel', () => {
    const onActivate = vi.fn()
    const { bar, panel } = mount()
    const actions: SidebarAction[] = [{ id: 'gear', title: 'Gear', icon: TestIcon, onActivate }]

    // Start with panel open on a view
    mountSidebar(bar, panel, TWO_VIEWS, actions)

    const panelCollapsedBefore = panel.classList.contains('collapsed')
    actionBtn(bar, 'gear').click()

    expect(onActivate).toHaveBeenCalledOnce()
    // Panel state is unchanged — actions never touch the panel.
    expect(panel.classList.contains('collapsed')).toBe(panelCollapsedBefore)
  })

  it('collapses the panel on cold start when no views are registered (nocx-rp2j)', () => {
    const { bar, panel } = mount()
    mountSidebar(
      bar,
      panel,
      [],
      [{ id: 'settings', title: 'Settings', icon: TestIcon, onActivate: () => {} }],
    )

    // No panel views — the panel must start collapsed, not empty.
    expect(panel.classList.contains('collapsed')).toBe(true)
  })

  it('starts expanded on the first view when both views and actions exist', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Alpha')
  })

  it('renders all icons as component elements, not text or innerHTML strings', () => {
    const { bar } = mount()
    mountSidebar(bar, document.createElement('div'), TWO_VIEWS, [SETTINGS_ACTION])
    const buttons = bar.querySelectorAll<HTMLElement>('button')

    for (const btn of buttons) {
      // The first child must be an Element node (the icon component rendered as
      // a real DOM node), not a Text node or a string fragment from innerHTML.
      expect(btn.firstChild?.nodeType).toBe(Node.ELEMENT_NODE)
    }
  })

  // ── Resize wiring (nocx-qmcu) ──────────────────────────────────────────

  it('renders no resize handle without a controller, and one with it', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])
    expect(panel.querySelector('[role="separator"]')).toBeNull()

    document.body.replaceChildren()
    const withCtrl = mount()
    const ctrl = createSidebarWidthController(withCtrl.panel, 240)
    mountSidebar(
      withCtrl.bar,
      withCtrl.panel,
      TWO_VIEWS,
      [SETTINGS_ACTION],
      undefined,
      undefined,
      undefined,
      ctrl,
    )
    const sep = withCtrl.panel.querySelector('[role="separator"]')
    expect(sep).not.toBeNull()
    expect(sep?.getAttribute('aria-label')).toBe('Resize sidebar')
    // The controller's initial width is applied to the panel host.
    expect(withCtrl.panel.style.getPropertyValue('--sidebar-width')).toBe('240px')
  })

  it('a drag resizes the panel live and persists once on release', () => {
    const { bar, panel } = mount()
    const persist = vi.fn()
    const ctrl = createSidebarWidthController(panel, 240, persist)
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION], undefined, undefined, undefined, ctrl)

    const sep = panel.querySelector('[role="separator"]') as HTMLElement
    fireEvent.pointerDown(sep, { clientX: 100, pointerId: 1 })
    expect(ctrl.isDragging()).toBe(true)
    fireEvent.pointerMove(sep, { clientX: 200, pointerId: 1 })
    expect(panel.style.getPropertyValue('--sidebar-width')).toBe('340px')
    expect(persist).not.toHaveBeenCalled() // still dragging

    fireEvent.pointerUp(sep, { clientX: 200, pointerId: 1 })
    expect(ctrl.isDragging()).toBe(false)
    expect(persist).toHaveBeenCalledWith(340)
  })

  it('keyboard resizing commits each step and clamps at the bounds', () => {
    const { bar, panel } = mount()
    const persist = vi.fn()
    const ctrl = createSidebarWidthController(panel, 636, persist)
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION], undefined, undefined, undefined, ctrl)

    const sep = panel.querySelector('[role="separator"]') as HTMLElement
    fireEvent.keyDown(sep, { key: 'ArrowRight' })
    expect(panel.style.getPropertyValue('--sidebar-width')).toBe('640px')
    expect(persist).toHaveBeenLastCalledWith(640)
    fireEvent.keyDown(sep, { key: 'ArrowRight' }) // already at the ceiling
    expect(persist).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(sep, { key: 'Home' })
    expect(panel.style.getPropertyValue('--sidebar-width')).toBe('200px')
    expect(persist).toHaveBeenLastCalledWith(200)
  })
})

// ── What a view's icon says about work behind it (nocx-hbdw4.1) ─────────
//
// The activity bar is the one part of the sidebar that stays on screen
// whatever the panel is doing, so a count and an aggregate progress drawn on
// a view's BUTTON are what answer "I cannot see that something is running" —
// the list behind the button is an ordinary view and vanishes with the panel
// like every other. These assert the half that must survive the panel being
// elsewhere, which is the half the epic exists for.
describe('sidebar — a view’s icon carries its status', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  /* eslint-disable solid/reactivity -- `status` is consumed reactively, inside
     the bar's own JSX (see SidebarViewDescriptor.status); the gate cannot see
     across the mountSidebar boundary, exactly as it cannot in main.tsx. */

  /** Two views where the SECOND carries a status, so every assertion below
   *  also says the mark landed on the right button. */
  function withStatus(status: () => { count: number; progress: number | null } | null) {
    return [TWO_VIEWS[0], { ...TWO_VIEWS[1], status }] as SidebarViewDescriptor[]
  }

  const badge = (bar: HTMLElement, id: string) =>
    bar.querySelector<HTMLElement>(`[data-view-badge="${id}"]`)
  const progress = (bar: HTMLElement, id: string) =>
    bar.querySelector<HTMLElement>(`[data-view-progress="${id}"]`)

  it('draws the count on the view’s own button, and nothing at zero', () => {
    const { bar, panel } = mount()
    const [count, setCount] = createSignal(0)
    mountSidebar(
      bar,
      panel,
      withStatus(() => ({ count: count(), progress: null })),
      [SETTINGS_ACTION],
    )

    expect(badge(bar, 'beta')).toBeNull()
    setCount(3)
    expect(badge(bar, 'beta')?.textContent).toBe('3')
    expect(badge(bar, 'alpha')).toBeNull()
    // Inside the button, so it cannot become a second toolbar stop.
    expect(badge(bar, 'beta')?.closest('button')).toBe(viewBtn(bar, 'beta'))
    expect(bar.querySelectorAll('.activity-bar-top button')).toHaveLength(2)
  })

  it('keeps the count in the accessible name, for somebody who cannot see it', () => {
    const { bar, panel } = mount()
    mountSidebar(
      bar,
      panel,
      withStatus(() => ({ count: 2, progress: null })),
      [SETTINGS_ACTION],
    )
    expect(viewBtn(bar, 'beta').getAttribute('aria-label')).toBe('Beta — 2 running')
    expect(viewBtn(bar, 'alpha').getAttribute('aria-label')).toBe('Alpha')
  })

  it('draws the bar only while something is running, and at zero it is still drawn', () => {
    // Zero is a MEASUREMENT and null is its absence: a transfer that has
    // not moved a byte yet is running, and a bar that vanished at 0 would
    // say it was not.
    const { bar, panel } = mount()
    const [fraction, setFraction] = createSignal<number | null>(null)
    mountSidebar(
      bar,
      panel,
      withStatus(() => ({ count: 1, progress: fraction() })),
      [SETTINGS_ACTION],
    )

    expect(progress(bar, 'beta')).toBeNull()
    setFraction(0)
    expect(progress(bar, 'beta')).not.toBeNull()
    expect(
      progress(bar, 'beta')!.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow'),
    ).toBe('0')
    setFraction(0.5)
    expect(
      progress(bar, 'beta')!.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow'),
    ).toBe('50')
    setFraction(null)
    expect(progress(bar, 'beta')).toBeNull()
  })

  it('goes on reporting while ANOTHER view is on screen and while the panel is collapsed', () => {
    // The whole reason the status is on the bar and not in the panel. This
    // is the jsdom half of e2e/ops-indicator.spec.ts.
    const { bar, panel } = mount()
    mountSidebar(
      bar,
      panel,
      withStatus(() => ({ count: 1, progress: 0.25 })),
      [SETTINGS_ACTION],
    )

    // The panel is showing the OTHER view, and beta still reports.
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Alpha')
    expect(badge(bar, 'beta')?.textContent).toBe('1')
    expect(progress(bar, 'beta')).not.toBeNull()

    // Collapse the panel altogether — the case that bought this feature.
    viewBtn(bar, 'alpha').click()
    expect(panel.classList.contains('collapsed')).toBe(true)
    expect(badge(bar, 'beta')?.textContent).toBe('1')
    expect(progress(bar, 'beta')).not.toBeNull()
  })

  it('a view with no status draws neither, and the bottom zone holds only actions', () => {
    // The bottom zone's contract is one kind of entry again (nocx-hbdw4.1):
    // the indicator that briefly widened it is gone with its popover.
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])
    expect(bar.querySelector('[data-view-badge]')).toBeNull()
    expect(bar.querySelector('[data-view-progress]')).toBeNull()
    const bottom = bar.querySelector('.activity-bar-bottom')
    expect(bottom?.querySelectorAll('button')).toHaveLength(1)
    expect(bottom?.querySelector('[data-action="settings"]')).not.toBeNull()
  })
})

/* eslint-enable solid/reactivity */

describe('sidebar — revealView and view props (nocx-wzc4.7)', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('revealView expands a collapsed panel on the same view', () => {
    const { bar, panel } = mount()
    const handle = mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])
    viewBtn(bar, 'alpha').click() // collapse
    expect(panel.classList.contains('collapsed')).toBe(true)

    handle.revealView('alpha')
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Alpha')
  })

  it('revealView switches to a different view, keeping the panel open', () => {
    const { bar, panel } = mount()
    const handle = mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    handle.revealView('beta')
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Beta')
    expect(viewBtn(bar, 'beta').getAttribute('aria-selected')).toBe('true')
  })

  it('revealView focuses the view button when the view is already on screen', () => {
    const { bar, panel } = mount()
    const handle = mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])
    // Move focus somewhere else first — the assertion is that reveal moves it.
    actionBtn(bar, 'settings').focus()

    handle.revealView('alpha')
    expect(document.activeElement).toBe(viewBtn(bar, 'alpha'))
    expect(panel.classList.contains('collapsed')).toBe(false)
  })

  it('revealView on an unknown view id is a no-op', () => {
    const { bar, panel } = mount()
    const handle = mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    handle.revealView('nope')
    expect(panelTitle(panel)).toBe('Alpha')
    expect(panel.classList.contains('collapsed')).toBe(false)
  })

  it('passes reactive visible and activeProfileId accessors into the active view', () => {
    const seen: { visible: boolean; profile: string | null }[] = []
    const ProbeView: Component<SidebarViewProps> = (props) => {
      createEffect(() => {
        seen.push({ visible: props.visible(), profile: props.activeProfileId() })
      })
      return <div />
    }
    const { bar, panel } = mount()
    const handle = mountSidebar(
      bar,
      panel,
      [{ id: 'probe', title: 'Probe', icon: TestIcon, view: ProbeView, order: 0 }],
      [],
      undefined,
      () => 'ssh:p1:1',
    )

    expect(seen[seen.length - 1]).toEqual({ visible: true, profile: 'ssh:p1:1' })
    viewBtn(bar, 'probe').click() // collapse — visible flips false
    expect(seen[seen.length - 1]?.visible).toBe(false)
    expect(seen[seen.length - 1]?.profile).toBe('ssh:p1:1')
    handle.revealView('probe') // expand — visible flips true
    expect(seen[seen.length - 1]?.visible).toBe(true)
  })
})

// ── Settings-tab transient collapse (nocx-3e3b) ──────────────────────────
// Every sidebar view speaks for the machine a terminal tab is on; a Settings
// tab is not a place, so arriving on one collapses the panel and the width
// goes to the settings content. The collapse is a consequence of where the
// user is, never an edit to their preference: returning restores the exact
// pre-Settings state, and neither the width nor the collapsed preference is
// persisted by the visit.

describe('sidebar — Settings tab transient collapse (nocx-3e3b)', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  /** Mount with a settings-mode accessor the test can flip mid-flight.
   *  The accessor must read a SIGNAL: Solid's on() subscribes to reactive
   *  reads only, so a plain closure variable would never re-fire the
   *  settings-mode effect. */
  function mountWithSettings(): {
    bar: HTMLElement
    panel: HTMLElement
    handle: SidebarHandle
    setSettings: (v: boolean) => void
  } {
    const [isSettings, setIsSettings] = createSignal(false)
    const { bar, panel } = mount()
    const handle = mountSidebar(
      bar,
      panel,
      TWO_VIEWS,
      [SETTINGS_ACTION],
      undefined,
      undefined,
      undefined,
      undefined,
      /* eslint-disable-next-line solid/reactivity -- mountSidebar consumes
       this accessor reactively (settings-mode effect, nocx-3e3b); the gate
       cannot see across the function boundary — same justification as the
       main.tsx disable. */
      () => isSettings(),
    )
    return { bar, panel, handle, setSettings: setIsSettings }
  }

  it('collapses on arrival at a Settings tab and restores on return, view retained', async () => {
    const { bar, panel, setSettings } = mountWithSettings()
    expect(panel.classList.contains('collapsed')).toBe(false)

    setSettings(true)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))

    setSettings(false)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))
    // The active view is untouched by the round trip.
    expect(panelTitle(panel)).toBe('Alpha')
    expect(viewBtn(bar, 'alpha').getAttribute('aria-selected')).toBe('true')
  })

  it('a sidebar opened while on a Settings tab stays open until the user closes it', async () => {
    const { bar, panel, setSettings } = mountWithSettings()
    setSettings(true)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))

    viewBtn(bar, 'beta').click() // user opens the panel while on Settings
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))
    expect(panelTitle(panel)).toBe('Beta')

    // The rule fired on arrival only; nothing fights the user afterwards.
    expect(panel.classList.contains('collapsed')).toBe(false)
  })

  it('restores a deliberately closed sidebar after a Settings round trip', async () => {
    const { bar, panel, setSettings } = mountWithSettings()
    viewBtn(bar, 'alpha').click() // deliberately closed — the user's state
    expect(panel.classList.contains('collapsed')).toBe(true)

    setSettings(true)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))

    viewBtn(bar, 'alpha').click() // opened while on Settings…
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))

    setSettings(false)
    // …and the departure restores the user's closed state, not the detour:
    // someone who keeps the rail closed must not find it open.
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))
  })

  it('the chosen width survives a Settings round trip and is never persisted by it', async () => {
    const [isSettings, setIsSettings] = createSignal(false)
    const { bar, panel } = mount()
    const persist = vi.fn()
    const ctrl = createSidebarWidthController(panel, 400, persist) // user's non-default width
    mountSidebar(
      bar,
      panel,
      TWO_VIEWS,
      [SETTINGS_ACTION],
      undefined,
      undefined,
      undefined,
      ctrl,
      /* eslint-disable-next-line solid/reactivity -- mountSidebar consumes
       this accessor reactively (settings-mode effect, nocx-3e3b); the gate
       cannot see across the function boundary — same justification as the
       main.tsx disable. */
      () => isSettings(),
    )
    expect(panel.style.getPropertyValue('--sidebar-width')).toBe('400px')

    setIsSettings(true)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))
    setIsSettings(false)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))

    // The width the user chose is untouched — and no write went to the seam.
    expect(panel.style.getPropertyValue('--sidebar-width')).toBe('400px')
    expect(persist).not.toHaveBeenCalled()
  })

  it('a Settings visit does not persist the transient collapse as a preference', async () => {
    const [isSettings, setIsSettings] = createSignal(false)
    const { bar, panel } = mount()
    const writes: boolean[] = []
    const persistence = {
      collapsed: false,
      activeViewId: '',
      save: (next: { collapsed: boolean; activeViewId: string }) => {
        writes.push(next.collapsed)
      },
    }
    mountSidebar(
      bar,
      panel,
      TWO_VIEWS,
      [SETTINGS_ACTION],
      persistence,
      undefined,
      undefined,
      undefined,
      /* eslint-disable-next-line solid/reactivity -- mountSidebar consumes
       this accessor reactively (settings-mode effect, nocx-3e3b); the gate
       cannot see across the function boundary — same justification as the
       main.tsx disable. */
      () => isSettings(),
    )
    // The open state is what was written.
    await vi.waitFor(() => expect(writes[writes.length - 1]).toBe(false))

    setIsSettings(true)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))
    // The transient collapse must not have rewritten the remembered state.
    expect(writes.every((collapsed) => collapsed === false)).toBe(true)

    setIsSettings(false)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))
    expect(writes.every((collapsed) => collapsed === false)).toBe(true)
  })

  it('revealView (the Ctrl/Cmd+Shift+O path) still expands the panel from a Settings tab', async () => {
    const { panel, handle, setSettings } = mountWithSettings()
    setSettings(true)
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))

    handle.revealView('alpha')
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))
    expect(panelTitle(panel)).toBe('Alpha')
  })
})

// ── The two zones, and the rule that used to sit between them ─────────────
//
// A 24px hairline was drawn under the last view icon by
// `.activity-bar-spacer::before` (0f6671de, nocx-82l9.6 — the shell's own
// commit, years before the API work). The owner asked for it to go
// (nocx-5b3ab). What it was doing — saying "these are two zones" — is done by
// the spacer's own growth instead: it takes every spare pixel, so the actions
// sit on the bar's floor and the views on its ceiling.
//
// This is read off the stylesheet SOURCE rather than off a computed style
// because jsdom loads no CSS: a `getComputedStyle` assertion here would pass
// against a stylesheet that says anything at all. That is the same reason
// button.test.tsx and caption.test.tsx read their files.
describe('the activity bar reads as two zones without a rule between them', () => {
  const CSS = readFileSync('src/styles/components/sidebar.css', 'utf8')

  it('nothing draws a hairline between the zones', () => {
    // The pseudo-element is gone, not merely emptied: a `::before` with a
    // `content` and no paint is still a box that takes vertical space.
    expect(CSS).not.toContain('.activity-bar-spacer::before')
    // And no other rule reintroduces one under the top zone by another name.
    expect(CSS).not.toMatch(/\.activity-bar-(zone|top|bottom|spacer)[^{]*\{[^}]*border-bottom/)
    expect(CSS).not.toMatch(/\.activity-bar-top[^{]*\{[^}]*border/)
  })

  it('the spacer still takes every spare pixel, which is what separates them', () => {
    // `flex: 1 1 auto` on the spacer inside a column flex bar of full height
    // is the whole mechanism. Without it the two zones butt together and the
    // rule WOULD be the only thing telling them apart.
    expect(CSS).toMatch(/\.activity-bar-spacer\s*\{[^}]*flex:\s*1 1 auto/)
    expect(CSS).toMatch(/\.activity-bar\s*\{[^}]*flex-direction:\s*column/)
    expect(CSS).toMatch(/\.activity-bar\s*\{[^}]*height:\s*100%/)
  })

  it('and a screen reader distinguishes views, active-tab actions, and global actions', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    const groups = [...bar.querySelectorAll('[role="group"]')]
    expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual([
      'Views',
      'Active tab actions',
      'Actions',
    ])
    // The spacer still separates the top and bottom visual zones in document
    // order; the nested active-tab group stays semantically distinct from views.
    const children = [...(bar.querySelector('.activity-bar')?.children ?? [])]
    expect(children.map((c) => c.className)).toEqual([
      'activity-bar-top',
      'activity-bar-spacer',
      'activity-bar-zone activity-bar-bottom',
    ])
  })
})

// ── The pinned filter slot (nocx-708q.3) ─────────────────────────────────
//
// `SidebarViewDescriptor.filter` is how a panel says WHICH of its children
// is the filter, and the shell pins it between the header and the scrolling
// body. Two things have to hold for that to be one shape rather than a
// fourth arrangement: a view that declares one gets it OUT of the scroller,
// and a view that declares none gets no row at all.

describe('the filter slot', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('pins a declared filter between the header and the scrolling body', () => {
    const { bar, panel } = mount()
    const withFilter: SidebarViewDescriptor = {
      ...TWO_VIEWS[0],
      filter: () => <input data-testid="probe-filter" />,
    }
    mountSidebar(bar, panel, [withFilter], [])

    const field = panel.querySelector<HTMLElement>('[data-testid="probe-filter"]')
    expect(field).not.toBeNull()
    // Pinned means exactly this: it is inside the filter row and outside the
    // scroller, so it cannot scroll away with the list it filters.
    expect(field!.closest('.ui-sidebar-view__filter')).not.toBeNull()
    expect(field!.closest('.ui-sidebar-view__body')).toBeNull()
  })

  it('draws NO filter row for a view that declares none', () => {
    // The row was drawn for every view: `ActiveView` handed SidebarView a
    // `<Show>` element, and a Show element is truthy whether or not its
    // condition holds — so the shell's own `<Show when={props.filter}>` was
    // always taken. It cost nothing while the row had no box; it costs a
    // strip of dead panel the moment the row carries the shell's inset,
    // which is the same 8px the header and the body carry (nocx-708q.3).
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [])
    expect(panel.querySelector('.ui-sidebar-view__filter')).toBeNull()
  })

  it('draws no actions row for a view that declares none, for the same reason', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [])
    expect(panel.querySelector('.ui-sidebar-view__actions')).toBeNull()
  })
})

describe('sidebar view-zone actions', () => {
  it('renders an action beside the Files view, not in the Files panel header', () => {
    const { bar, panel } = mount()
    mountSidebar(
      bar,
      panel,
      [TWO_VIEWS[0]],
      [],
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      [
        {
          id: 'sandbox-shield',
          title: 'Sandbox',
          icon: TestIcon,
          onActivate: () => {},
          selected: () => true,
        },
      ],
    )

    const topButtons = [...bar.querySelectorAll<HTMLElement>('.activity-bar-top button')]
    expect(
      topButtons.map((button) => button.getAttribute('data-view') ?? button.dataset.action),
    ).toEqual(['alpha', 'sandbox-shield'])
    const shield = topButtons[1]
    expect(shield.getAttribute('aria-selected')).toBe('true')
    expect(shield.dataset.railIndicator).toBe('true')
    expect(panel.querySelector('[data-testid="sandbox-shield"]')).toBeNull()
  })

  it('skips a disabled shield during roving keyboard navigation', () => {
    const { bar, panel } = mount()
    mountSidebar(
      bar,
      panel,
      [TWO_VIEWS[0]],
      [],
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      [
        {
          id: 'sandbox-shield',
          title: 'Sandbox unavailable',
          icon: TestIcon,
          onActivate: () => {},
          disabled: () => true,
        },
      ],
    )

    const view = viewBtn(bar, 'alpha')
    const shield = actionBtn(bar, 'sandbox-shield')
    view.focus()
    fireEvent.keyDown(view, { key: 'ArrowDown' })

    expect(document.activeElement).toBe(view)
    expect(view.tabIndex).toBe(0)
    expect(shield.tabIndex).toBe(-1)
  })
})
