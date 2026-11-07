// @vitest-environment jsdom
// Catalogue parity — the stage 2 (nocx-dej6.2) key-set alignment check. The
// Go declarations mint their keys through LabelKeyFor/OptionLabelKeyFor, so
// the catalogues must carry exactly those spellings: a hardcoded list proves
// en does, and a ru lookup proves the Russian value resolves through t()
// rather than falling back to English.
import { describe, it, expect, afterEach } from 'vitest'
import { t, setActiveLocale } from './locale-model'
import { en } from './catalogues/en'
import { ru } from './catalogues/ru'

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

// The Go-minted families with NO call site (nocx-dej6 stage 3, settings
// backend completeness): the routing matrix, the centre toggles, the debounce
// window, the section titles and the four sentinel-zero sentences. Their keys
// arrive as data (decl.labelKey etc.), so the call-site extraction in the
// lint fixture cannot see them — these hardcoded witnesses are the
// independent check that en and ru carry every one, exactly what THEMES is
// for the options above.

// Every (kind, channel) pair internal/notify's DefaultCatalogue offers, plus
// the centre visibility toggle per kind and the debounce window
// (internal/app registers them as dynamic declarations).
const ROUTE_KINDS = [
  'blockFinished',
  'sessionEnded',
  'transferFinished',
  'programNotify',
  'bell',
  'coordinatorStalled',
  'paneWorkFinished',
] as const
const ROUTE_CHANNELS = ['banner', 'toast'] as const

// The sections settings.go declares (Section: sites, deduplicated) plus the
// notifications section internal/notify declares (RouteSettingSection).
const SECTIONS = [
  'answers',
  'clipboard',
  'history',
  'instructions',
  'interface',
  'pets',
  'skills',
  'terminal',
  'updates',
  'worktrees',
  'notifications',
] as const

// The four sentinel-zero sentences, with their settings keys.
const ZERO_LABEL_KEYS = [
  'settings.history.retentionDays.zeroLabel',
  'settings.worktrees.idleDays.zeroLabel',
  'settings.history.diskCeilingMiB.zeroLabel',
  'settings.terminal.scrollbackLines.zeroLabel',
] as const

describe('catalogue parity with the Go-minted dynamic families', () => {
  it('en and ru carry every routing pair, centre toggle and the debounce', () => {
    for (const kind of ROUTE_KINDS) {
      for (const channel of ROUTE_CHANNELS) {
        expect(en[`notifications.route.${kind}.${channel}`]).toBeDefined()
        expect(ru[`notifications.route.${kind}.${channel}`]).toBeDefined()
        expect(en[`notifications.route.${kind}.${channel}.description`]).toBeDefined()
        expect(ru[`notifications.route.${kind}.${channel}.description`]).toBeDefined()
      }
      expect(en[`notifications.centre.${kind}`]).toBeDefined()
      expect(ru[`notifications.centre.${kind}`]).toBeDefined()
      expect(en[`notifications.centre.${kind}.description`]).toBeDefined()
      expect(ru[`notifications.centre.${kind}.description`]).toBeDefined()
    }
    expect(en['notifications.debounceSeconds.label']).toBeDefined()
    expect(ru['notifications.debounceSeconds.label']).toBeDefined()
    expect(en['notifications.debounceSeconds.description']).toBeDefined()
    expect(ru['notifications.debounceSeconds.description']).toBeDefined()
  })

  it('ru carries every section title and zero sentence, en too', () => {
    for (const s of SECTIONS) {
      expect(en[`settings.section.${s}`]).toBeDefined()
      expect(ru[`settings.section.${s}`]).toBeDefined()
    }
    for (const key of ZERO_LABEL_KEYS) {
      expect(en[key]).toBeDefined()
      expect(ru[key]).toBeDefined()
    }
  })

  it('the matrix column headers resolve from keys, in both catalogues', () => {
    // The headers used to be the two halves of the composed cell label, cut
    // on " → " — so a Russian header read the English half Go's prose
    // carried. They are keys now (settings-domain's rowLabelKey and
    // columnLabelKey), and the columns are the two delivery channels plus
    // the centre-visibility pseudo-column, whose id is "centre".
    const COLUMNS = [...ROUTE_CHANNELS, 'centre'] as const
    for (const column of COLUMNS) {
      expect(en[`notify.column.${column}.label`], column).toBeDefined()
      expect(ru[`notify.column.${column}.label`], column).toBeDefined()
    }
    // And the kinds' own labels, which the row headers resolve through, are
    // the ones the Go catalogue mints for the same ids.
    for (const kind of ROUTE_KINDS) {
      expect(en[`notify.kind.${kind}.label`], kind).toBeDefined()
      expect(ru[`notify.kind.${kind}.label`], kind).toBeDefined()
    }
  })

  it('t() resolves the routing label and section under ru', () => {
    setActiveLocale('ru')
    expect(t('notifications.route.blockFinished.toast', 'Command finished → In-app toast')).toBe(
      'Команда завершена → Тост в приложении',
    )
    expect(t('settings.section.history', 'History')).toBe('История')
    expect(t('settings.history.retentionDays.zeroLabel', 'Never switched off automatically')).toBe(
      'Не отключать автоматически',
    )
  })
})
