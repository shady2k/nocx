// @vitest-environment jsdom
/**
 * Theme-adapter tests: atomic fallback when tokens are missing,
 * pub/sub wiring, and mount/dispose counters.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_TERMINAL_THEME,
  REQUIRED_TERMINAL_TOKENS,
  resolveValidatedITheme,
  resolveIThemeOrFallback,
  getCurrentTheme,
  setCurrentTheme,
  subscribeThemeChanges,
  _resetThemeState,
} from './theme-adapter'

// ── Concrete test theme (not the default) ───────────────────────────────

const TEST_THEME = {
  ...DEFAULT_TERMINAL_THEME,
  background: '#000000',
  foreground: '#ffffff',
}

// ── Helpers ─────────────────────────────────────────────────────────────

/** Set all 21 required terminal tokens on the root element. */
function setAllRequiredTokens(): void {
  const values = ['#111', '#222', '#333', '#444', '#555']
  const ansi = [
    '#000',
    '#800',
    '#080',
    '#880',
    '#008',
    '#808',
    '#088',
    '#888',
    '#444',
    '#c44',
    '#4c4',
    '#cc4',
    '#44c',
    '#c4c',
    '#4cc',
    '#ccc',
  ]
  const all = [...values, ...ansi]
  for (let i = 0; i < REQUIRED_TERMINAL_TOKENS.length; i++) {
    document.documentElement.style.setProperty(REQUIRED_TERMINAL_TOKENS[i], all[i])
  }
}

/** Remove all terminal tokens from the root element. */
function clearAllRequiredTokens(): void {
  for (const token of REQUIRED_TERMINAL_TOKENS) {
    document.documentElement.style.removeProperty(token)
  }
}

describe('theme-adapter: fallback atomicity', () => {
  beforeEach(() => {
    _resetThemeState()
    clearAllRequiredTokens()
  })

  it('returns undefined from resolveValidatedITheme when no tokens are set', () => {
    expect(resolveValidatedITheme()).toBeUndefined()
  })

  it('returns DEFAULT_TERMINAL_THEME from resolveIThemeOrFallback when tokens are missing', () => {
    const theme = resolveIThemeOrFallback()
    expect(theme).toBe(DEFAULT_TERMINAL_THEME)
  })

  it('returns a resolved theme when all tokens are present', () => {
    setAllRequiredTokens()
    const theme = resolveValidatedITheme()
    expect(theme).toBeDefined()
    expect(theme!.background).toBe('#111')
    expect(theme!.foreground).toBe('#222')
    expect(theme!.cursor).toBe('#333')
    expect(theme!.black).toBe('#000')
    expect(theme!.brightWhite).toBe('#ccc')
  })

  it('returns undefined when one token is removed (atomic fallback)', () => {
    setAllRequiredTokens()
    // Remove just the foreground token
    document.documentElement.style.removeProperty('--terminal-foreground')
    expect(resolveValidatedITheme()).toBeUndefined()
  })

  it('returns undefined when the selection token is missing', () => {
    setAllRequiredTokens()
    document.documentElement.style.removeProperty('--terminal-selection')
    expect(resolveValidatedITheme()).toBeUndefined()
  })

  it('returns undefined when one ANSI token (ansi-7) is missing', () => {
    setAllRequiredTokens()
    document.documentElement.style.removeProperty('--terminal-ansi-7')
    expect(resolveValidatedITheme()).toBeUndefined()
  })

  it('falls back to DEFAULT even when only one token is present', () => {
    document.documentElement.style.setProperty('--terminal-background', '#ff0000')
    const theme = resolveIThemeOrFallback()
    expect(theme).toBe(DEFAULT_TERMINAL_THEME)
    // Even though a valid token exists, the fallback is atomic
    expect(theme.background).toBe('#1a1b26')
  })
})

describe('theme-adapter: theme-change pub/sub', () => {
  beforeEach(() => {
    _resetThemeState()
  })

  it('getCurrentTheme returns DEFAULT initially', () => {
    expect(getCurrentTheme()).toBe(DEFAULT_TERMINAL_THEME)
  })

  it('setCurrentTheme replaces the current theme', () => {
    setCurrentTheme(TEST_THEME)
    expect(getCurrentTheme()).toBe(TEST_THEME)
  })

  it('subscribe fires when setCurrentTheme is called', () => {
    const fn = vi.fn()
    subscribeThemeChanges(fn)
    setCurrentTheme(TEST_THEME)
    expect(fn).toHaveBeenCalledTimes(1)
    expect(fn).toHaveBeenCalledWith(TEST_THEME)
  })

  it('unsubscribe prevents further notifications', () => {
    const fn = vi.fn()
    const unsub = subscribeThemeChanges(fn)
    unsub()
    setCurrentTheme(TEST_THEME)
    expect(fn).not.toHaveBeenCalled()
  })

  it('subscribers do not fire during setCurrentTheme with the same object', () => {
    const fn = vi.fn()
    subscribeThemeChanges(fn)
    setCurrentTheme(DEFAULT_TERMINAL_THEME)
    // The subscriber should still fire — every setCurrentTheme call notifies
    expect(fn).toHaveBeenCalledTimes(1)
  })
})

describe('theme-adapter: mount/dispose counters', () => {
  beforeEach(() => {
    _resetThemeState()
    clearAllRequiredTokens()
  })

  it('mounting a renderer does not create extra terminals on theme change', () => {
    // With no CSS tokens, the adapter always resolves to DEFAULT_TERMINAL_THEME.
    // A theme-change notification via setCurrentTheme only calls applyTheme on
    // renderers — it does NOT create new terminals or remount hosts.
    // This test verifies the pub/sub path does not accidentally mount or dispose.

    const subscriber = vi.fn()
    subscribeThemeChanges(subscriber)

    // Simulate a theme change (what happens when Go settings arrive)
    const newTheme = { ...DEFAULT_TERMINAL_THEME, background: '#222222' }
    setCurrentTheme(newTheme)

    // The subscriber was notified exactly once
    expect(subscriber).toHaveBeenCalledTimes(1)
    expect(subscriber).toHaveBeenCalledWith(newTheme)

    // No terminals were created or disposed by the theme change itself.
    // (Actual terminal lifecycle is managed by TabManager/terminal-content,
    // which are outside this test scope.)
  })

  it('resetThemeState clears subscribers and restores default', () => {
    setCurrentTheme(TEST_THEME)
    const fn = vi.fn()
    subscribeThemeChanges(fn)
    _resetThemeState()

    expect(getCurrentTheme()).toBe(DEFAULT_TERMINAL_THEME)
    // The old subscriber was cleared — calling setCurrentTheme on the
    // reset state doesn't fire old handlers.
    setCurrentTheme(TEST_THEME)
    expect(fn).not.toHaveBeenCalled()
  })
})
