import { describe, expect, it } from 'vitest'
import { extractCallSiteKeys } from './check-i18n-catalogues.mjs'

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
