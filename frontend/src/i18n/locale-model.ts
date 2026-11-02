/**
 * locale-model — the active locale and the translation lookup.
 *
 * Localisation design §"Frontend catalogue shape" (nocx-dej6): the TypeScript
 * catalogue owns translations, Go ships keys alongside English prose as the
 * fallback. `t(key, fallback)` resolves: the active catalogue → the English
 * catalogue → the call-site fallback literal → the key itself. A key absent
 * from the Russian catalogue therefore renders the English fallback from the
 * call site — never blank, never a raw key — which is the epic's own
 * criterion.
 *
 * ## Why a module-level signal rather than a context
 *
 * The same shape as the toast queue (ui/toast.tsx:96): the app has several
 * Solid roots — the shell plus one per tab surface — and a locale read inside
 * a per-tab root has to update with the shell's. A context would have to be
 * threaded through every mount() boundary; module state crosses those
 * boundaries for free and Solid's reactivity works across roots. The cost is
 * that it is a singleton, which is what one application-wide locale is.
 *
 * Kit components never import this module (ui/README.md): a kit component
 * that needs a string receives it via props from the surface.
 */
import { createSignal } from 'solid-js'
import { en } from './catalogues/en'
import { ru } from './catalogues/ru'

/** The locales the catalogues answer for. Option labels are endonyms —
 * «English», «Русский» — the same in both languages, so no catalogue entry
 * is needed for the picker itself. */
export type Locale = 'en' | 'ru'

/**
 * The value a catalogue entry may hold: a plain string, or a plural object
 * resolved with `Intl.PluralRules`. Russian is the reason the plural shape
 * exists (`1 команда / 2 команды / 5 команд`); English has only
 * `one`/`other`, so an `en` plural entry never carries `few`/`many`.
 */
interface PluralForms {
  one: string
  few?: string
  many?: string
  other: string
}

export type CatalogueEntry = string | PluralForms

type Catalogue = Readonly<Record<string, CatalogueEntry>>

const CATALOGUES: Record<Locale, Catalogue> = { en, ru }

const [locale, setLocale] = createSignal<Locale>('en')

/** The active locale. Reading this inside a component tracks the signal, so
 * a locale switch re-renders every surface that reads translations. */
export { locale }

/** Switch the active locale. The caller owns persistence (main.tsx's
 * reconcile flow, which also writes the bootstrap cache). */
export function setActiveLocale(next: Locale): void {
  setLocale(next)
}

/**
 * The plural category `Intl.PluralRules('ru')` gives a count, or `en`
 * categories when the active locale is English. Asking both locales' rules
 * keeps a plural entry in `en` (only `one`/`other`) resolvable without a
 * special case: the active locale selects the rule set, and a category the
 * entry does not carry falls back to `other`.
 */
const PLURAL_RULES: Readonly<Record<Locale, Intl.PluralRules>> = {
  en: new Intl.PluralRules('en'),
  ru: new Intl.PluralRules('ru'),
}

/** The category the active locale gives this count (`one`, `few`, `many`,
 * `other`). Exported for the plural-resolution test. */
export function pluralCategory(count: number): Intl.LDMLPluralRule {
  return PLURAL_RULES[locale()].select(count)
}

function resolveEntry(key: string): CatalogueEntry | undefined {
  const active = CATALOGUES[locale()] as Record<string, CatalogueEntry | undefined>
  const found = active[key]
  if (found !== undefined) return found
  // The English catalogue is the completeness check: the active catalogue
  // (ru) may be partial, en may not miss a key the code looks up.
  return (en as Record<string, CatalogueEntry | undefined>)[key]
}

function pickForm(entry: CatalogueEntry, count: number): string {
  if (typeof entry === 'string') return entry
  switch (pluralCategory(count)) {
    case 'one':
      return entry.one
    case 'few':
      return entry.few ?? entry.other
    case 'many':
      return entry.many ?? entry.other
    default:
      return entry.other
  }
}

/** Substitute `{count}`-style placeholders in a resolved form; unknown
 * placeholders are left verbatim so a typo is visible, not swallowed. */
function interpolate(form: string, params: Record<string, string | number>): string {
  return form.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in params ? String(params[name]) : whole,
  )
}

/**
 * Translate `key`. The resolution order is the design's own rule: the active
 * catalogue → the English catalogue → the fallback literal at the call site →
 * the key itself. The call-site literal stays in place as the visible
 * fallback, which is what keeps an untranslated surface readable English
 * rather than blank. Reading this inside a component tracks the locale
 * signal, so a switch re-renders it.
 */
export function t(key: string, fallback?: string): string {
  const entry = resolveEntry(key)
  if (entry === undefined) return fallback ?? key
  return typeof entry === 'string' ? entry : pickForm(entry, 1)
}

/**
 * The plural-capable variant of `t`. A count whose catalogue value is a
 * plain string resolves to that string regardless of the count (English
 * often needs no plural object); a plural object resolves the category with
 * `Intl.PluralRules` in the active locale, with `other` as the last resort.
 */
export function tPlural(key: string, count: number, fallback?: string): string {
  const entry = resolveEntry(key)
  if (entry === undefined) return fallback ?? key
  const resolved = pickForm(entry, count)
  return interpolate(resolved, { count })
}

/**
 * Format a date/time with `Intl.DateTimeFormat` in the active locale — the
 * design's one date path; no date is formatted by hand anywhere in the
 * frontend. The returned object is created fresh per call: formatters are
 * cached by the implementation and cheap, while holding one per options
 * combination would pin a locale the signal can change underneath.
 */
export function formatDateTime(value: number | Date, options?: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat(locale(), options).format(value)
}
