// sidebar-model — pure functions only, no jsdom, no Solid.
import { describe, it, expect } from 'vitest'
import { createSidebarState, toggleSidebar, setActiveView, collapseSidebar } from './sidebar-model'

// ── createSidebarState ─────────────────────────────────────────────────────

describe('createSidebarState', () => {
  it('creates an open sidebar with no active view', () => {
    const s = createSidebarState()
    expect(s.collapsed).toBe(false)
    expect(s.activeViewId).toBe('')
  })

  it('accepts an initial view id', () => {
    const s = createSidebarState('files')
    expect(s.activeViewId).toBe('files')
  })
})

// ── toggleSidebar ──────────────────────────────────────────────────────────

describe('toggleSidebar', () => {
  it('collapses an open sidebar', () => {
    const s = createSidebarState('files')
    const next = toggleSidebar(s)
    expect(next.collapsed).toBe(true)
    expect(next.activeViewId).toBe('files') // view retained
  })

  it('expands a collapsed sidebar', () => {
    const s = { collapsed: true, activeViewId: 'files' }
    const next = toggleSidebar(s)
    expect(next.collapsed).toBe(false)
  })

  it('does not mutate the input', () => {
    const s = createSidebarState('files')
    toggleSidebar(s)
    expect(s.collapsed).toBe(false)
  })
})

// ── setActiveView ─────────────────────────────────────────────────────────

describe('setActiveView', () => {
  it('switches to a different view', () => {
    const s = createSidebarState('files')
    const next = setActiveView(s, 'settings')
    expect(next.activeViewId).toBe('settings')
    expect(next.collapsed).toBe(false)
  })

  it('collapses the sidebar when clicking the active view while open', () => {
    const s = createSidebarState('files')
    const next = setActiveView(s, 'files')
    expect(next.collapsed).toBe(true)
    expect(next.activeViewId).toBe('files')
  })
  it('expands when clicking a different view while collapsed', () => {
    const s = { collapsed: true, activeViewId: 'files' }
    const next = setActiveView(s, 'settings')
    expect(next.activeViewId).toBe('settings')
    expect(next.collapsed).toBe(false) // expands to show the new view
  })
  it('does not mutate the input', () => {
    const s = createSidebarState('files')
    setActiveView(s, 'settings')
    expect(s.activeViewId).toBe('files')
  })
})

// ── collapseSidebar ────────────────────────────────────────────────────────

describe('collapseSidebar', () => {
  it('collapses an open sidebar', () => {
    const s = createSidebarState('files')
    const next = collapseSidebar(s)
    expect(next.collapsed).toBe(true)
  })

  it('is a no-op when already collapsed', () => {
    const s = { collapsed: true, activeViewId: 'files' }
    const next = collapseSidebar(s)
    expect(next).toBe(s)
  })
})
