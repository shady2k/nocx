/**
 * The English catalogue — the source language and the completeness check.
 *
 * A key that exists nowhere in `en` is dead: a missing `en` entry fails the
 * parity lint fixture (lint-fixtures/check-i18n-catalogues.mjs), while a
 * missing `ru` entry only warns and falls back to the English value at
 * runtime. Every user-visible string the localisation epic translates gets
 * its key minted here first, namespaced by surface (`settings.ui.language.*`,
 * `connections.title`, …) per the design §"Frontend catalogue shape".
 */
import type { CatalogueEntry } from '../locale-model'

/**
 * Flat key → string (or plural object) map. This stage carries only the
 * `ui.language` setting chrome plus one key per mechanism the model must
 * prove (a plain string, a plural object); surface translation is stage 3.
 */
export const en: Readonly<Record<string, CatalogueEntry>> = {
  // ── Settings → Interface → Language chrome ──────────────────────────────
  'settings.ui.language.label': 'Language',
  'settings.ui.language.description':
    'The language nocx speaks in its own screens — settings, prompts and notices. Terminal output is never translated.',

  // ── Mechanism samples ───────────────────────────────────────────────────
  // A plain string: the base case every translated surface will use.
  'i18n.sample.greeting': 'Welcome back',
  // A plural object: `Intl.PluralRules` picks the form for the count. The
  // label names the mechanism itself, so the sample is self-describing in
  // whichever locale is active.
  'i18n.sample.commandCount': {
    one: '{count} command',
    other: '{count} commands',
  },
  // Deliberately absent from ru: proves the visible-English fallback at
  // runtime (design story 2) and emits the parity fixture's warning shape.
  'i18n.sample.untranslated': 'This key has no Russian translation yet',
}
