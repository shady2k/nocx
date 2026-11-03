import { describe, expect, it } from 'vitest'
import { scanSource, violationKey } from './check-i18n-hardcoded.mjs'

/**
 * The hardcoded-UI-literal checker's own tests (nocx-dej6 stage 3 ratchet).
 *
 * User-facing copy that bypasses t() cannot switch language. Both halves
 * asserted: a raw literal in a text-bearing position is reported, and the
 * sanctioned shapes — inside t()/tPlural(), dynamic expressions, pure
 * punctuation, technical placeholders — stay silent.
 */

describe('must trip', () => {
  it('JSX text with words', () => {
    const hits = scanSource('s.tsx', 'export const A = () => <p>Saved to history</p>')
    expect(hits).toEqual([
      { file: 's.tsx', line: 1, context: 'jsx-text', snippet: 'Saved to history' },
    ])
  })

  it('a Cyrillic JSX text node', () => {
    const hits = scanSource('s.tsx', 'export const A = () => <p>Сохранено</p>')
    expect(hits.length).toBe(1)
  })

  it('a literal title attribute', () => {
    const hits = scanSource('s.tsx', 'export const A = () => <b title="Close">{v}</b>')
    expect(hits).toEqual([{ file: 's.tsx', line: 1, context: 'attr:title', snippet: 'Close' }])
  })

  it('an expression-container literal in ariaLabel', () => {
    const hits = scanSource('s.tsx', `export const A = () => <b ariaLabel={'Dismiss'}>{v}</b>`)
    expect(hits.map((h) => h.context)).toEqual(['attr:ariaLabel'])
  })

  it('the static head of a template in a label prop', () => {
    const hits = scanSource(
      's.tsx',
      'export const A = (n: number) => <b label={`Rows ${n}`}>{v}</b>',
    )
    expect(hits.map((h) => h.context)).toEqual(['attr:label'])
    expect(hits[0].snippet).toBe('Rows')
  })

  it('message/label/description properties in object literals', () => {
    const hits = scanSource(
      's.ts',
      "showToast({ message: 'Copied' })\nconst opts = [{ value: 1, label: 'Never' }]\nconst d = { description: 'What it does' }",
    )
    expect(hits.map((h) => h.context)).toEqual(['prop:message', 'prop:label', 'prop:description'])
  })

  it('a multi-line JSX text yields one collapsed snippet', () => {
    const hits = scanSource('s.tsx', 'export const A = () => <p>line one\n     line two</p>')
    expect(hits.length).toBe(1)
    expect(hits[0].snippet).toBe('line one line two')
    expect(violationKey(hits[0])).not.toContain('\n')
  })

  it('fails closed on a parse error', () => {
    const hits = scanSource('s.tsx', 'export const x = = = broken')
    expect(hits).toEqual([{ file: 's.tsx', line: 0, context: 'PARSE', snippet: expect.any(String) }])
  })
})

describe('must stay silent', () => {
  it('a string inside t()', () => {
    expect(
      scanSource('s.tsx', `import { t } from './i18n/locale-model'\nexport const A = (v: string) => <b title={t('k', 'Close')}>{v}</b>`),
    ).toEqual([])
  })

  it('the fallback literal of a tPlural call', () => {
    expect(
      scanSource('s.ts', `import { tPlural } from './i18n/locale-model'\nexport const f = (n: number) => tPlural('k', n, '{count} rows')`),
    ).toEqual([])
  })

  it('a dynamic attribute expression', () => {
    expect(scanSource('s.tsx', 'export const A = (s: string, v: string) => <b title={s}>{v}</b>')).toEqual([])
  })

  it('pure punctuation and entity JSX text', () => {
    expect(scanSource('s.tsx', 'export const A = () => <p>—</p>')).toEqual([])
    expect(scanSource('s.tsx', 'export const A = () => <p>&mdash;</p>')).toEqual([])
  })

  it('a non-text attribute name', () => {
    expect(scanSource('s.tsx', 'export const A = () => <b data-kind="row" class="x">{v}</b>')).toEqual([])
  })

  it('an empty-string attribute', () => {
    expect(scanSource('s.tsx', `export const A = (v: string) => <b title="">{v}</b>`)).toEqual([])
  })

  it('a property whose value is dynamic', () => {
    expect(scanSource('s.ts', 'export const o = (m: string) => ({ message: m })')).toEqual([])
  })
})
