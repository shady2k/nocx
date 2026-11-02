// @vitest-environment jsdom
// locale-model — the translation mechanism of stage 1 (nocx-dej6). The three
// behaviours the design names as its own criteria: the fallback order
// (active → en → call-site → key), the reactivity of the locale signal, and
// Russian plural resolution through Intl.PluralRules.
import { describe, it, expect, afterEach } from 'vitest'
import { createRenderEffect, createRoot } from 'solid-js'
import { t, tPlural, formatDateTime, locale, setActiveLocale, pluralCategory } from './locale-model'
import { bootstrapLocale, reconcileLocaleFromGo, STORAGE_KEY } from './bootstrap'

afterEach(() => {
  // The locale signal is module state; leave it English so one test's switch
  // cannot decide what the next test reads.
  setActiveLocale('en')
  localStorage.clear()
})

// ── t(): the fallback order ──────────────────────────────────────────────

describe('t resolution order', () => {
  it('returns the active catalogue value', () => {
    setActiveLocale('ru')
    expect(t('i18n.sample.greeting')).toBe('С возвращением')
  })

  it('falls back to the English catalogue when the active locale misses the key', () => {
    // 'i18n.sample.untranslated' is deliberately absent from ru.
    setActiveLocale('ru')
    expect(t('i18n.sample.untranslated')).toBe('This key has no Russian translation yet')
  })

  it('falls back to the call-site literal when neither catalogue knows the key', () => {
    expect(t('i18n.sample.nowhere', 'Visible English fallback')).toBe('Visible English fallback')
  })

  it('returns the key itself as the last resort — never blank', () => {
    expect(t('i18n.sample.nowhere')).toBe('i18n.sample.nowhere')
  })
})

// ── Reactivity: reading t() inside a component tracks the signal ─────────

describe('locale signal reactivity', () => {
  it('switching the locale changes the resolved string inside a tracked scope', async () => {
    const resolved: string[] = []
    const dispose = createRoot((dispose) => {
      createRenderEffect(() => {
        resolved.push(t('i18n.sample.greeting'))
      })
      return dispose
    })
    // Each switch gets its own microtask so the effect re-runs once per
    // locale, reading the intermediate value instead of the batched final.
    setActiveLocale('ru')
    await Promise.resolve()
    setActiveLocale('en')
    await Promise.resolve()
    dispose()
    // en → ru → en: the effect re-ran for each switch, proving t() tracked
    // the signal rather than capturing a one-time value.
    expect(resolved).toEqual(['Welcome back', 'С возвращением', 'Welcome back'])
  })
})

// ── Plurals: one/few/many for ru ─────────────────────────────────────────

describe('plural resolution', () => {
  it('picks one/few/many for ru counts 1/2/5', () => {
    setActiveLocale('ru')
    expect(tPlural('i18n.sample.commandCount', 1)).toBe('1 команда')
    expect(tPlural('i18n.sample.commandCount', 2)).toBe('2 команды')
    expect(tPlural('i18n.sample.commandCount', 5)).toBe('5 команд')
  })

  it('uses the same entry for English counts through one/other', () => {
    setActiveLocale('en')
    expect(tPlural('i18n.sample.commandCount', 1)).toBe('1 command')
    expect(tPlural('i18n.sample.commandCount', 2)).toBe('2 commands')
    expect(tPlural('i18n.sample.commandCount', 5)).toBe('5 commands')
  })

  it('exposes the category the rules give a count', () => {
    setActiveLocale('ru')
    expect(pluralCategory(1)).toBe('one')
    expect(pluralCategory(2)).toBe('few')
    expect(pluralCategory(5)).toBe('many')
  })

  it('falls back to the call-site literal when the key is nowhere', () => {
    setActiveLocale('ru')
    expect(tPlural('i18n.sample.nowhere', 3, '3 items')).toBe('3 items')
  })
})

// ── Date formatting follows the active locale ────────────────────────────

describe('formatDateTime', () => {
  it('formats with the active locale', () => {
    const instant = Date.UTC(2026, 9, 27) // 2026-10-27, the design's own date
    setActiveLocale('en')
    const en = formatDateTime(instant, { dateStyle: 'long' })
    setActiveLocale('ru')
    const ru = formatDateTime(instant, { dateStyle: 'long' })
    expect(en).not.toBe(ru)
    expect(en).toContain('October')
    expect(ru).toContain('октября')
  })
})

// ── Bootstrap and Go reconciliation ──────────────────────────────────────

describe('bootstrap and reconcile', () => {
  it('seeds the locale from the localStorage cache before first render', () => {
    localStorage.setItem(STORAGE_KEY, 'ru')
    expect(bootstrapLocale()).toBe('ru')
    expect(locale()).toBe('ru')
  })

  it('falls back to English for an unrecognised cache value', () => {
    localStorage.setItem(STORAGE_KEY, 'fr')
    expect(bootstrapLocale()).toBe('en')
  })

  it('reconciles a recognised Go value and caches it for the next launch', () => {
    reconcileLocaleFromGo('ru')
    expect(locale()).toBe('ru')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('ru')
  })

  it('ignores an absent or unrecognised Go value', () => {
    setActiveLocale('ru')
    reconcileLocaleFromGo(undefined)
    reconcileLocaleFromGo('klingon')
    expect(locale()).toBe('ru')
  })
})
