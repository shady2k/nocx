// @vitest-environment jsdom
// Catalogue parity — the stage 2 (nocx-dej6.2) key-set alignment check. The
// Go declarations mint their keys through LabelKeyFor/OptionLabelKeyFor, so
// the catalogues must carry exactly those spellings: a hardcoded list proves
// en does, and a ru lookup proves the Russian value resolves through t()
// rather than falling back to English.
import { describe, it, expect, afterEach } from 'vitest'
import { t, setActiveLocale } from './locale-model'
import { en } from './catalogues/en'

// The 13 themes UITheme declares in internal/settings/settings.go, in
// declaration order. Hardcoded on purpose: the list here is the independent
// witness that the catalogue's option keys match the Go option values.
const THEMES = [
  'graphite',
  'tokyo-night',
  'light',
  'ayu-dark',
  'catppuccin-latte',
  'catppuccin-mocha',
  'dracula',
  'gruvbox-dark',
  'nord',
  'one-dark',
  'rose-pine',
  'solarized-dark',
  'solarized-light',
] as const

afterEach(() => {
  // The locale signal is module state; leave it English for other suites.
  setActiveLocale('en')
})

describe('catalogue parity with the Go settings declarations', () => {
  it('en carries a label entry for every theme option value', () => {
    for (const theme of THEMES) {
      expect(en[`settings.ui.theme.option.${theme}`], theme).toBeDefined()
    }
  })

  it('en carries no theme option key outside the declared values', () => {
    const extra = Object.keys(en).filter(
      (key) =>
        key.startsWith('settings.ui.theme.option.') &&
        !THEMES.includes(key.slice('settings.ui.theme.option.'.length) as (typeof THEMES)[number]),
    )
    expect(extra).toEqual([])
  })

  it('t() resolves the Russian theme label under ru, not the fallback', () => {
    setActiveLocale('ru')
    expect(t('settings.ui.theme.label', 'Theme')).toBe('Тема')
    expect(t('settings.ui.theme.option.graphite', 'Graphite')).toBe('Графит')
  })
})
