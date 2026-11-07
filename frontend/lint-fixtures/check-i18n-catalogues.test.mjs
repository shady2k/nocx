import { describe, expect, it } from 'vitest'
import {
  compareCatalogues,
  extractCallSiteKeys,
  extractEntries,
  placeholdersOf,
} from './check-i18n-catalogues.mjs'

/**
 * The i18n catalogue checker's own tests for the call-site direction
 * (nocx-dej6.3.8).
 *
 * Three t() call sites shipped with keys that existed in NEITHER catalogue:
 * they render their fallback forever and can never gain a ru entry, and the
 * catalogue-parity check that existed then could not see them because it only
 * compared en against ru. The rule: every literal first argument of a
 * t()/tPlural() call under src/ must be minted in en. Both halves are
 * asserted: an unminted literal key is extracted, and a dynamic first
 * argument (a Go-minted labelKey arriving as data) is not.
 */

describe('must trip', () => {
  it('extracts a literal t() key', () => {
    const hits = extractCallSiteKeys('s.tsx', "const x = t('surface.key', 'Fallback')")
    expect(hits).toEqual([{ file: 's.tsx', line: 1, key: 'surface.key' }])
  })

  it('extracts a literal tPlural() key', () => {
    const hits = extractCallSiteKeys(
      's.tsx',
      "const x = tPlural('surface.items', n, '{count} items')",
    )
    expect(hits).toEqual([{ file: 's.tsx', line: 1, key: 'surface.items' }])
  })

  it('extracts keys from a t() call inside JSX', () => {
    const hits = extractCallSiteKeys(
      's.tsx',
      "export const A = () => <b title={t('a.b', 'B')}>x</b>",
    )
    expect(hits).toEqual([{ file: 's.tsx', line: 1, key: 'a.b' }])
  })

  it('counts distinct literal keys in one file separately', () => {
    const hits = extractCallSiteKeys(
      's.tsx',
      "const a = t('k.one', 'One')\nconst b = t('k.two', 'Two')",
    )
    expect(hits.map((h) => h.key)).toEqual(['k.one', 'k.two'])
  })

  it('fails closed on a parse error', () => {
    const hits = extractCallSiteKeys('s.tsx', 'export const x = = = broken')
    expect(hits).toEqual([{ file: 's.tsx', line: 0, key: 'PARSE' }])
  })
})

describe('must stay silent', () => {
  it('a dynamic first argument (labelKey data) is not extracted', () => {
    expect(extractCallSiteKeys('s.tsx', 'const x = t(m.labelKey, m.label)')).toEqual([])
  })

  it('a dynamic key behind ?? is not extracted', () => {
    expect(extractCallSiteKeys('s.tsx', "const x = t(decl.labelKey ?? '', decl.label)")).toEqual([])
  })

  it('a call to another function named t on an object is still read by name — documented, not chased', () => {
    // obj.t('k', 'f') resolves to property name t; the checker reads it.
    // No catalogue key collision is known for this shape; if one appears,
    // give the checker scope analysis rather than quietly widening this.
    const hits = extractCallSiteKeys('s.tsx', "obj.t('obj.t.key', 'Fallback')")
    expect(hits.map((h) => h.key)).toEqual(['obj.t.key'])
  })

  it('template-literal keys are not extracted (dynamic shape)', () => {
    expect(extractCallSiteKeys('s.tsx', 'const x = t(`a.${b}`, "F")')).toEqual([])
  })

  it('non-string first argument is not extracted', () => {
    expect(extractCallSiteKeys('s.tsx', 'const x = t(42, "F")')).toEqual([])
  })

  it('tests are excluded by the tree walk, not the source scan', () => {
    // The exclusion is scanCallSites's file filter (documented in the CLI);
    // scanSource itself is content-only.
    expect(extractCallSiteKeys('s.test.tsx', "const x = t('k', 'F')").length).toBe(1)
  })
})

// ── The catalogues themselves, and the severities of the comparison ──────
//
// Stage 3 closed with the rule "a key missing from ru is an error" — so every
// untranslated wording had to be copied into ru as English, and the catalogue
// could not say which of its values a round had actually ruled on. The
// owner's rule of 2026-11-06 puts the fallback back where the runtime already
// has it: a missing translation renders the English catalogue value, so it is
// REPORTED and not an error. What stays an error is the direction that has no
// runtime answer: a key only ru knows, and a call site whose key no catalogue
// mints. A placeholder that does not survive translation is an error too — it
// prints a hole or a literal `{name}` at a person.

const enSource = (entries) => `export const en = {\n${entries}\n}\n`
const ruSource = (entries) => `export const ru = {\n${entries}\n}\n`

const compare = (enEntries, ruEntries, hits = []) =>
  compareCatalogues(
    extractEntries('en', enSource(enEntries)),
    extractEntries('ru', ruSource(ruEntries)),
    hits,
  )

describe('extractEntries', () => {
  it('reads a plain string entry and a plural object', () => {
    const { forms } = extractEntries(
      'en',
      enSource("  'a.one': 'One',\n  'a.many': { one: '{count} thing', other: '{count} things' },"),
    )
    expect(forms.get('a.one')).toEqual(['One'])
    expect(forms.get('a.many')).toEqual(['{count} thing', '{count} things'])
  })

  it('fails closed on an entry shape it cannot read', () => {
    const { malformed } = extractEntries('en', enSource("  'a.one': someConstant,"))
    expect(malformed).toEqual(['en: a.one has a shape the checker cannot read'])
  })
})

describe('placeholders', () => {
  it('lists the names of one form, sorted and deduplicated', () => {
    expect(placeholdersOf('{name} and {error} and {name}')).toEqual(['error', 'name'])
  })
})

describe('compareCatalogues severities', () => {
  it('reports a key missing from ru and does not fail', () => {
    const { errors, missingFromRu } = compare(
      "  'a.one': 'One',\n  'a.two': 'Two',",
      "  'a.two': 'Два',",
    )
    expect(errors).toEqual([])
    expect(missingFromRu).toEqual(['a.one'])
  })

  it('fails on a key that exists only in ru', () => {
    const { errors } = compare("  'a.one': 'One',", "  'a.one': 'Один',\n  'a.two': 'Два',")
    expect(errors).toEqual(['MISSING FROM EN: a.two'])
  })

  it('fails on a literal call-site key neither catalogue mints', () => {
    const { errors } = compare("  'a.one': 'One',", "  'a.one': 'Один',", [
      { file: 's.tsx', line: 3, key: 'a.nowhere' },
    ])
    expect(errors).toEqual(['UNMINTED: a.nowhere (s.tsx:3)'])
  })

  it('fails when a translation drops a placeholder', () => {
    const { errors } = compare(
      "  'a.fail': 'Could not copy: {error}',",
      "  'a.fail': 'Не удалось скопировать',",
    )
    expect(errors).toEqual(['PLACEHOLDER MISMATCH: a.fail — en {error}, ru {}'])
  })

  it('fails when a translation invents a placeholder nothing supplies', () => {
    const { errors } = compare("  'a.one': 'One',", "  'a.one': 'Один {name}',")
    expect(errors).toEqual(['PLACEHOLDER MISMATCH: a.one — en {}, ru {name}'])
  })

  it('fails when one form of a plural drops the count', () => {
    const { errors } = compare(
      "  'a.n': { one: '{count} thing', other: '{count} things' },",
      "  'a.n': { one: '{count} вещь', few: 'вещи', many: '{count} вещей', other: '{count} вещи' },",
    )
    expect(errors).toEqual(['PLACEHOLDER MISMATCH: a.n — a ru form drops {count}: "вещи"'])
  })

  it('passes a translated plural with every placeholder in every form', () => {
    const { errors, reported } = compare(
      "  'a.n': { one: '{count} thing', other: '{count} things' },",
      "  'a.n': { one: '{count} вещь', few: '{count} вещи', many: '{count} вещей', other: '{count} вещи' },",
    )
    expect(errors).toEqual([])
    expect(reported).toEqual([])
  })
})
