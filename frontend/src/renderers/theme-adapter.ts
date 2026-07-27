/**
 * Theme adapter — resolves `--terminal-*` CSS custom properties into xterm's
 * `ITheme`, with atomic fallback to the built-in default when a required token
 * is missing. Also provides a module-level theme-change pub/sub so terminal
 * controllers can be notified without touching the TabManager or the Solid tree.
 *
 * ADR-0013 §2.6, §8.1; design spec §5.4.
 *
 * # The one place that knows terminal colours
 *
 * Every hex value lives in `DEFAULT_TERMINAL_THEME` below, which IS the
 * built-in default. When `--terminal-*` declarations land in the theme CSS
 * (tokyo-night.css is the target), the adapter reads from `getComputedStyle`
 * and this fallback becomes dead code that can be deleted.
 *
 * Until then every required property resolves to an empty string → the adapter
 * always falls back atomically. Every absent token is a finding.
 */
import type { ITheme } from '@xterm/xterm'

// ── Required CSS custom property names (ADR-0013 §2.6 — exhaustive) ──────

export const REQUIRED_TERMINAL_TOKENS = [
  '--terminal-background',
  '--terminal-foreground',
  '--terminal-cursor',
  '--terminal-cursor-accent',
  '--terminal-selection',
  '--terminal-ansi-0',
  '--terminal-ansi-1',
  '--terminal-ansi-2',
  '--terminal-ansi-3',
  '--terminal-ansi-4',
  '--terminal-ansi-5',
  '--terminal-ansi-6',
  '--terminal-ansi-7',
  '--terminal-ansi-8',
  '--terminal-ansi-9',
  '--terminal-ansi-10',
  '--terminal-ansi-11',
  '--terminal-ansi-12',
  '--terminal-ansi-13',
  '--terminal-ansi-14',
  '--terminal-ansi-15',
] as const

export type TerminalToken = (typeof REQUIRED_TERMINAL_TOKENS)[number]

// ── Built-in default ITheme (Tokyo Night palette) ───────────────────────
// This is the atomic fallback when CSS token resolution fails. It is also
// THE ONE PLACE that knows terminal colours — when `--terminal-*` tokens
// are properly declared in a theme stylesheet this entire object becomes
// semantically dead and can be removed.

export const DEFAULT_TERMINAL_THEME: ITheme = {
  background: '#1a1b26',
  foreground: '#c0caf5',
  cursor: '#c0caf5',
  cursorAccent: '#1a1b26',
  selectionBackground: '#364A82',
  black: '#1a1b26',
  red: '#f7768e',
  green: '#9ece6a',
  yellow: '#e0af68',
  blue: '#7aa2f7',
  magenta: '#bb9af7',
  cyan: '#7dcfff',
  white: '#a9b1d6',
  brightBlack: '#414868',
  brightRed: '#f7768e',
  brightGreen: '#9ece6a',
  brightYellow: '#e0af68',
  brightBlue: '#7aa2f7',
  brightMagenta: '#bb9af7',
  brightCyan: '#7dcfff',
  brightWhite: '#c0caf5',
}

// ── Adapter: CSS custom property → ITheme ───────────────────────────────

/**
 * Resolve a single custom property from the root element.
 * Returns the raw value (which may be empty if the property is not set).
 */
export function resolveCssToken(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/**
 * Maps the 21 required terminal tokens to xterm's ITheme shape.
 * Every value may be empty — the caller should validate.
 */
function cssTokensToITheme(): ITheme {
  return {
    background: resolveCssToken('--terminal-background'),
    foreground: resolveCssToken('--terminal-foreground'),
    cursor: resolveCssToken('--terminal-cursor'),
    cursorAccent: resolveCssToken('--terminal-cursor-accent'),
    selectionBackground: resolveCssToken('--terminal-selection'),
    black: resolveCssToken('--terminal-ansi-0'),
    red: resolveCssToken('--terminal-ansi-1'),
    green: resolveCssToken('--terminal-ansi-2'),
    yellow: resolveCssToken('--terminal-ansi-3'),
    blue: resolveCssToken('--terminal-ansi-4'),
    magenta: resolveCssToken('--terminal-ansi-5'),
    cyan: resolveCssToken('--terminal-ansi-6'),
    white: resolveCssToken('--terminal-ansi-7'),
    brightBlack: resolveCssToken('--terminal-ansi-8'),
    brightRed: resolveCssToken('--terminal-ansi-9'),
    brightGreen: resolveCssToken('--terminal-ansi-10'),
    brightYellow: resolveCssToken('--terminal-ansi-11'),
    brightBlue: resolveCssToken('--terminal-ansi-12'),
    brightMagenta: resolveCssToken('--terminal-ansi-13'),
    brightCyan: resolveCssToken('--terminal-ansi-14'),
    brightWhite: resolveCssToken('--terminal-ansi-15'),
  }
}

/**
 * Resolve the theme from CSS. Returns undefined if ANY required token is
 * missing (empty string) — atomic fallback, never a half-applied palette.
 *
 * The check is on the canonical list, not on the ITheme keys, so a token
 * that is not part of ITheme (e.g. a future extended token) is not required
 * for the palette to be considered valid.
 */
export function resolveValidatedITheme(): ITheme | undefined {
  // Fast path: are all required tokens non-empty?
  for (const token of REQUIRED_TERMINAL_TOKENS) {
    if (!resolveCssToken(token)) return undefined
  }
  return cssTokensToITheme()
}

/**
 * Resolve the theme from CSS, falling back to the built-in default if any
 * required token is absent. Never returns undefined.
 */
export function resolveIThemeOrFallback(): ITheme {
  return resolveValidatedITheme() ?? DEFAULT_TERMINAL_THEME
}

// ── Theme-change pub/sub (module-level) ─────────────────────────────────

type ThemeSubscriber = (theme: ITheme) => void

const _subscribers = new Set<ThemeSubscriber>()
let _currentTheme: ITheme = DEFAULT_TERMINAL_THEME

/** Returns the most recently resolved / accepted theme. */
export function getCurrentTheme(): ITheme {
  return _currentTheme
}

/**
 * Replace the current theme and notify all subscribers. Call this from the
 * bootstrap resolver (before any terminal exists) and later when the Go
 * `ui.theme` setting arrives and differs from the bootstrap value.
 */
export function setCurrentTheme(theme: ITheme): void {
  _currentTheme = theme
  for (const fn of _subscribers) fn(theme)
}

/**
 * Subscribe to future theme changes. The callback fires on every
 * `setCurrentTheme` call. Returns an unsubscribe function.
 *
 * Use inside mount() to register the subscription before construction
 * completes, then immediately re-apply the current theme to close the
 * fetch/subscribe race.
 */
export function subscribeThemeChanges(fn: ThemeSubscriber): () => void {
  _subscribers.add(fn)
  return () => _subscribers.delete(fn)
}

// ── Cleanup (testing / teardown) ────────────────────────────────────────

/** Reset module state — for tests only. */
export function _resetThemeState(): void {
  _subscribers.clear()
  _currentTheme = DEFAULT_TERMINAL_THEME
}
