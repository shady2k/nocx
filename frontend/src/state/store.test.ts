// store — Solid store integration tests.
// These test that the store wrapper correctly bridges framework‑neutral
// transitions into Solid's reactive store.
import { describe, it, expect } from 'vitest'
import { createRoot } from 'solid-js'
import { createAppStore, type AppState, type AppActions } from './store'

// ── Helper ─────────────────────────────────────────────────────────────────

/** Run a callback inside Solid's root and destroy it afterward. */
function withStore<T>(fn: (state: [AppState, AppActions]) => T): T {
  return createRoot(() => {
    const store = createAppStore()
    return fn(store)
  })
}

// ── Initial state ─────────────────────────────────────────────────────────

describe('createAppStore initial state', () => {
  it('starts empty', () => {
    withStore(([state]) => {
      expect(state.sidebar.collapsed).toBe(false)
      expect(state.settings.values).toEqual({})
      expect(state.profiles.profiles).toHaveLength(0)
      expect(state.banner.shown).toBe(false)
    })
  })
})

// ── Sidebar actions ────────────────────────────────────────────────────────

describe('sidebar actions', () => {
  it('toggleSidebar toggles collapsed', () => {
    withStore(([state, actions]) => {
      expect(state.sidebar.collapsed).toBe(false)
      actions.toggleSidebar()
      expect(state.sidebar.collapsed).toBe(true)
    })
  })

  it('setActiveView switches views', () => {
    withStore(([state, actions]) => {
      actions.setActiveView('files')
      expect(state.sidebar.activeViewId).toBe('files')
    })
  })

  it('collapseSidebar collapses', () => {
    withStore(([state, actions]) => {
      actions.collapseSidebar()
      expect(state.sidebar.collapsed).toBe(true)
    })
  })
})

// ── Settings actions ───────────────────────────────────────────────────────

describe('settings actions', () => {
  it('setSettingsValues replaces values', () => {
    withStore(([state, actions]) => {
      actions.setSettingsValues({ theme: 'dark' })
      expect(state.settings.values).toEqual({ theme: 'dark' })
    })
  })

  it('applySettingsSnapshot sets mirror', () => {
    withStore(([state, actions]) => {
      actions.applySettingsSnapshot({
        values: { theme: 'dark' },
        overridden: ['theme'],
        revision: 5,
      })
      expect(state.settings.values.theme).toBe('dark')
      expect(state.settings.overridden.has('theme')).toBe(true)
      expect(state.settings.revision).toBe(5)
    })
  })
})

// ── Profile actions ────────────────────────────────────────────────────────

describe('profile actions', () => {
  it('setProfiles replaces lists', () => {
    withStore(([state, actions]) => {
      actions.setProfiles([{ id: 'p1', name: 'Server', options: { host: 'x.com' } } as never], [])
      expect(state.profiles.profiles).toHaveLength(1)
      expect(state.profiles.profiles[0].name).toBe('Server')
    })
  })
})

// ── Banner actions ─────────────────────────────────────────────────────────

describe('banner actions', () => {
  it('showBanner marks shown', () => {
    withStore(([state, actions]) => {
      expect(state.banner.shown).toBe(false)
      actions.showBanner()
      expect(state.banner.shown).toBe(true)
    })
  })

  it('dismissBanner clears shown', () => {
    withStore(([state, actions]) => {
      actions.showBanner()
      actions.dismissBanner()
      expect(state.banner.shown).toBe(false)
    })
  })
})

// ── Reset ──────────────────────────────────────────────────────────────────

describe('reset', () => {
  it('resets all state to initial values', () => {
    withStore(([state, actions]) => {
      actions.setActiveView('files')
      actions.showBanner()
      actions.setSettingsValues({ theme: 'dark' })
      expect(state.banner.shown).toBe(true)

      actions.reset()
      expect(state.sidebar.collapsed).toBe(false)
      expect(state.sidebar.activeViewId).toBe('')
      expect(state.banner.shown).toBe(false)
      expect(state.settings.values).toEqual({})
    })
  })
})
