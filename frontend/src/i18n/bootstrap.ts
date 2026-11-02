/**
 * Locale bootstrap — the ui.language half of what theme-bootstrap.ts does for
 * ui.theme (localisation design Q4, nocx-dej6): seeded synchronously from
 * localStorage before the first render so the first frame already speaks the
 * right language, reconciled against the Go settings snapshot when it
 * arrives, and cached on every reconcile so the next launch's first frame is
 * right too.
 *
 * The active locale itself lives in the locale model's signal; this module
 * only decides what it starts as and keeps the cache honest.
 */
import { setActiveLocale, type Locale } from './locale-model'

const DEFAULT_LOCALE: Locale = 'en'

/** Versioned localStorage key for the bootstrap cache. Cache format v1.
 * Exported for the bootstrap test, the one consumer outside this module. */
export const STORAGE_KEY = 'nocx:bootstrap:locale:v1'

/**
 * The locales the frontend can activate. An id outside this set falls back
 * to the default rather than being trusted — a locale with no catalogue
 * would render every key as raw English-or-key text while the setting says
 * otherwise. This is the set the `ui.language` Select declares in
 * internal/settings/settings.go; the two are pinned by nothing yet, the
 * same deliberate gap theme-bootstrap had before its catalogue test.
 */
const KNOWN_LOCALES: ReadonlySet<string> = new Set(['en', 'ru'])

// ── Bootstrap ───────────────────────────────────────────────────────────

/**
 * Read the bootstrap cache, validate it, and set the locale model's signal.
 * Runs synchronously — call before `render(() => <App />, ...)`. Returns the
 * applied locale (for tests and for symmetry with bootstrapTheme).
 */
export function bootstrapLocale(): Locale {
  const saved = readCachedLocale()
  const locale = saved !== null && KNOWN_LOCALES.has(saved) ? (saved as Locale) : DEFAULT_LOCALE
  setActiveLocale(locale)
  return locale
}

// ── Cache read/write ────────────────────────────────────────────────────

function readCachedLocale(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

/**
 * Write a locale to the bootstrap cache. Called only on Go reconcile, never
 * by user action — the accepted Go value is what the next launch should
 * render from its first frame.
 */
function cacheLocale(locale: Locale): void {
  try {
    localStorage.setItem(STORAGE_KEY, locale)
  } catch {
    // Silently ignore quota-exceeded or non-browser environments.
  }
}

// ── Go reconciliation ───────────────────────────────────────────────────

/**
 * Reconcile the Go `ui.language` setting against the active locale: apply a
 * changed, recognised value to the signal (which re-renders every surface
 * reading `t()`) and refresh the bootstrap cache so the next launch's first
 * frame matches. An absent or unrecognised value is a no-op — the seeded
 * locale stays.
 */
export function reconcileLocaleFromGo(goLocale: string | undefined): void {
  if (goLocale === undefined || goLocale === '' || !KNOWN_LOCALES.has(goLocale)) return
  const next = goLocale as Locale
  setActiveLocale(next)
  cacheLocale(next)
}
