/**
 * Bootstrap theme resolver — runs synchronously before the Solid render so
 * `data-theme` and colour tokens are applied before the first frame.
 *
 * ADR-0013 §8, §8.1; design spec §5.4.
 *
 * # Resolution order
 *
 * 1. Read the bootstrap cache (localStorage). If present and recognised, it
 *    becomes the applied theme id.
 * 2. Apply `data-theme` to `document.documentElement`.
 * 3. Resolve via `resolveValidatedITheme()`. If any `--terminal-*` token is
 *    missing, fall back atomically to the built-in default.
 * 4. Call `setCurrentTheme()` so mount() reads the correct theme.
 * 5. Return the applied theme id for upstream reconciliation.
 *
 * # Go reconciliation (stubbed)
 *
 * The selected theme is a Go setting (`ui.theme`) that arrives asynchronously
 * over the WebSocket. The bootstrap cache covers the first frame; when Go
 * arrives, call `reconcileThemeFromGo(goThemeId)` (exported below) to resolve
 * the new theme, apply `data-theme`, notify live terminals, and update the
 * cache.
 *
 * The Go setting `ui.theme` does NOT exist yet in internal/settings/ — the
 * reconcile path is wired but tolerant of the setting being absent.
 */
import { resolveValidatedITheme, setCurrentTheme, DEFAULT_TERMINAL_THEME } from './theme-adapter'

// ── Constants ───────────────────────────────────────────────────────────

export const DEFAULT_THEME_ID = 'tokyo-night'

/** Versioned localStorage key for the bootstrap cache. Cache format v1. */
export const STORAGE_KEY = 'nocx:bootstrap:theme:v1'

const KNOWN_THEME_IDS = new Set(['tokyo-night', 'light'])

// ── Bootstrap ───────────────────────────────────────────────────────────

/**
 * Read the bootstrap cache, apply `data-theme`, validate terminal tokens,
 * and set the module-level current theme. Runs synchronously — call before
 * `render(() => <App />, ...)`.
 *
 * Returns the applied theme id (for reconciliation with Go later).
 */
export function bootstrapTheme(): string {
  const saved = readCachedThemeId()
  const themeId = saved !== null && KNOWN_THEME_IDS.has(saved) ? saved : DEFAULT_THEME_ID

  document.documentElement.setAttribute('data-theme', themeId)

  // Resolve terminal tokens from CSS. If the --terminal-* declarations are
  // not yet in the stylesheet (current state), every token resolves to empty
  // and we fall back to the built-in DEFAULT_TERMINAL_THEME.
  const resolved = resolveValidatedITheme()
  setCurrentTheme(resolved ?? DEFAULT_TERMINAL_THEME)

  return themeId
}

// ── Cache read/write ────────────────────────────────────────────────────

function readCachedThemeId(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

/**
 * Write a theme id to the bootstrap cache. Call this when Go accepts a new
 * theme value so the next launch renders correctly from the first frame.
 * Never called by user action — only on Go reconcile.
 */
export function cacheThemeId(id: string): void {
  try {
    localStorage.setItem(STORAGE_KEY, id)
  } catch {
    // Silently ignore quota-exceeded or non-browser environments.
  }
}

// ── Go reconciliation (stubbed for absent ui.theme setting) ─────────────

/**
 * Reconcile the Go `ui.theme` setting against the current bootstrap cache.
 *
 * When the Go value differs from what was bootstrapped, this function:
 * 1. Applies `data-theme` for the new id
 * 2. Re-resolves terminal tokens from CSS
 * 3. Updates the module-level current theme (which notifies all live
 *    terminal controllers via the theme-change pub/sub)
 * 4. Updates the bootstrap cache
 *
 * When the Go value matches or is undefined, this is a no-op.
 *
 * @param goThemeId — the value of `ui.theme` from the Go settings snapshot,
 *   or undefined if the setting does not exist yet.
 * @param currentAppliedId — the id returned by `bootstrapTheme()`. Defaults
 *   to the cache value if not provided.
 */
export function reconcileThemeFromGo(
  goThemeId: string | undefined,
  currentAppliedId?: string,
): void {
  // No Go setting → nothing to do.
  if (goThemeId === undefined || goThemeId === '') return

  // Already matches → nothing to do.
  const applied = currentAppliedId ?? readCachedThemeId() ?? DEFAULT_THEME_ID
  if (goThemeId === applied) return

  // Apply the new id, resolve, notify.
  const normalizedId = KNOWN_THEME_IDS.has(goThemeId) ? goThemeId : DEFAULT_THEME_ID
  document.documentElement.setAttribute('data-theme', normalizedId)

  const resolved = resolveValidatedITheme()
  setCurrentTheme(resolved ?? DEFAULT_TERMINAL_THEME)

  cacheThemeId(normalizedId)
}
