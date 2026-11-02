/**
 * The Russian catalogue — deliberately PARTIAL in this stage.
 *
 * A key missing here is not an error: `t()` falls back to the English entry,
 * and the parity lint fixture (lint-fixtures/check-i18n-catalogues.mjs)
 * warns about the gap so it is visible in review without blocking a commit
 * that adds an untranslated key. Coverage grows surface by surface in stage 3
 * of the localisation epic (nocx-dej6).
 *
 * Everything committed in this repo is English — comments included — but
 * catalogue VALUES are the product's Russian, the one place the repository
 * language rule does not reach (AGENTS.md "Language": what is committed is
 * English; what the app renders is the person's language).
 */
import type { CatalogueEntry } from '../locale-model'

/**
 * Flat key → string (or plural object) map. Same key set as `en` except
 * where a translation has not landed yet.
 */
export const ru: Readonly<Record<string, CatalogueEntry>> = {
  // ── Settings → Interface → Language chrome ──────────────────────────────
  'settings.ui.language.label': 'Язык',
  'settings.ui.language.description':
    'Язык, на котором nocx говорит на своих экранах — настройки, приглашения и уведомления. Вывод терминала не переводится.',

  // ── Mechanism samples ───────────────────────────────────────────────────
  // A plain string.
  'i18n.sample.greeting': 'С возвращением',
  // A plural object: Russian needs one/few/many (1 команда / 2 команды /
  // 5 команд), resolved with Intl.PluralRules('ru').
  'i18n.sample.commandCount': {
    one: '{count} команда',
    few: '{count} команды',
    many: '{count} команд',
    other: '{count} команды',
  },
  // 'i18n.sample.untranslated' is deliberately absent — see the header.
}
