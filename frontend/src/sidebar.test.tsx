// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mountSidebar, type SidebarViewDescriptor, type SidebarAction } from './sidebar'
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
  const el = bar.querySelector<HTMLElement>(`[role="button"][data-view="${viewId}"]`)
  if (!el) throw new Error(`no view button for ${viewId}`)
  return el
}

function actionBtn(bar: HTMLElement, actionId: string): HTMLElement {
  const el = bar.querySelector<HTMLElement>(`[role="button"][data-action="${actionId}"]`)
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
  beforeEach(() => {
    localStorage.clear()
  })

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
    expect(bar.querySelectorAll('[role="button"]')).toHaveLength(3)
  })

  it('starts expanded on the first view with no empty panel (nocx-rp2j fix)', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(viewBtn(bar, 'alpha').classList.contains('active')).toBe(true)
    expect(panelTitle(panel)).toBe('Alpha')
  })

  it("collapses when the active view's button is clicked, and re-opens on the next click", () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    viewBtn(bar, 'alpha').click()
    expect(panel.classList.contains('collapsed')).toBe(true)
    // VS Code drops the active highlight when the panel is closed
    expect(viewBtn(bar, 'alpha').classList.contains('active')).toBe(false)

    viewBtn(bar, 'alpha').click()
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(viewBtn(bar, 'alpha').classList.contains('active')).toBe(true)
  })

  it('switches views when another button is clicked, keeping the panel open', () => {
    const { bar, panel } = mount()
    mountSidebar(bar, panel, TWO_VIEWS, [SETTINGS_ACTION])

    viewBtn(bar, 'beta').click()
    expect(panel.classList.contains('collapsed')).toBe(false)
    expect(panelTitle(panel)).toBe('Beta')
    expect(viewBtn(bar, 'beta').classList.contains('active')).toBe(true)
    expect(viewBtn(bar, 'alpha').classList.contains('active')).toBe(false)
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
    const first = mount()
    mountSidebar(first.bar, first.panel, TWO_VIEWS, [SETTINGS_ACTION])
    viewBtn(first.bar, 'alpha').click() // collapse

    document.body.replaceChildren()
    const second = mount()
    mountSidebar(second.bar, second.panel, TWO_VIEWS, [SETTINGS_ACTION])

    expect(second.panel.classList.contains('collapsed')).toBe(true)
    expect(viewBtn(second.bar, 'alpha').classList.contains('active')).toBe(false)
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
    const buttons = bar.querySelectorAll<HTMLElement>('[role="button"]')

    for (const btn of buttons) {
      // The first child must be an Element node (the icon component rendered as
      // a real DOM node), not a Text node or a string fragment from innerHTML.
      expect(btn.firstChild?.nodeType).toBe(Node.ELEMENT_NODE)
    }
  })
})
