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
    expect(hits).toEqual([
      { file: 's.tsx', line: 0, context: 'PARSE', snippet: expect.any(String) },
    ])
  })
})

describe('must stay silent', () => {
  it('a string inside t()', () => {
    expect(
      scanSource(
        's.tsx',
        `import { t } from './i18n/locale-model'\nexport const A = (v: string) => <b title={t('k', 'Close')}>{v}</b>`,
      ),
    ).toEqual([])
  })

  it('the fallback literal of a tPlural call', () => {
    expect(
      scanSource(
        's.ts',
        `import { tPlural } from './i18n/locale-model'\nexport const f = (n: number) => tPlural('k', n, '{count} rows')`,
      ),
    ).toEqual([])
  })

  it('a dynamic attribute expression', () => {
    expect(
      scanSource('s.tsx', 'export const A = (s: string, v: string) => <b title={s}>{v}</b>'),
    ).toEqual([])
  })

  it('pure punctuation and entity JSX text', () => {
    expect(scanSource('s.tsx', 'export const A = () => <p>—</p>')).toEqual([])
    expect(scanSource('s.tsx', 'export const A = () => <p>&mdash;</p>')).toEqual([])
  })

  it('a non-text attribute name', () => {
    expect(
      scanSource('s.tsx', 'export const A = () => <b data-kind="row" class="x">{v}</b>'),
    ).toEqual([])
  })

  it('an empty-string attribute', () => {
    expect(scanSource('s.tsx', `export const A = (v: string) => <b title="">{v}</b>`)).toEqual([])
  })

  it('a property whose value is dynamic', () => {
    expect(scanSource('s.ts', 'export const o = (m: string) => ({ message: m })')).toEqual([])
  })
})

// ── The three shapes the first version could not see ─────────────────────
//
// Each is the same defect one expression away from a literal the checker
// already caught: a conditional in a text position, a variable or return that
// lands in one, imperative DOM text. Closed at nocx-dej6.3.8's review.

describe('conditional expressions in a text position', () => {
  it('reports both branches of a conditional in a title', () => {
    const hits = scanSource(
      's.tsx',
      'export const A = (ok: boolean) => <b title={ok ? "Saved" : "Not saved"}>{v}</b>',
    )
    expect(hits.map((h) => [h.context, h.snippet])).toEqual([
      ['attr:title', 'Saved'],
      ['attr:title', 'Not saved'],
    ])
  })

  it('stays silent when the branches are translated', () => {
    expect(
      scanSource(
        's.tsx',
        `export const A = (ok: boolean) => <b title={ok ? t('a.yes','Saved') : t('a.no','Not saved')}>{v}</b>`,
      ),
    ).toEqual([])
  })
})

describe('variables and returns that land in a text position', () => {
  it('reports the literal a variable holds, at the variable', () => {
    const hits = scanSource(
      's.ts',
      "const msg = 'Copied'\nexport const o = () => ({ message: msg })",
    )
    expect(hits).toEqual([{ file: 's.ts', line: 1, context: 'prop:message', snippet: 'Copied' }])
  })

  it('reports every literal a local function returns into a text position', () => {
    const hits = scanSource(
      's.ts',
      "function sentence(n: number) { if (n === 1) return 'One row'; return 'Many rows' }\nexport const o = () => ({ title: sentence(2) })",
    )
    expect(hits.map((h) => h.snippet)).toEqual(['One row', 'Many rows'])
  })

  it('reports a concise arrow body used as a title', () => {
    const hits = scanSource(
      's.tsx',
      'const label = (n: number) => `${n} rows`\nexport const A = (n: number) => <b title={label(n)}>{v}</b>',
    )
    expect(hits.map((h) => h.context)).toEqual(['attr:title'])
  })

  it('stays silent for a binding it cannot see — the value is dynamic', () => {
    expect(scanSource('s.ts', 'export const o = (m: string) => ({ message: m })')).toEqual([])
  })

  it('stays silent for a call into another module', () => {
    expect(
      scanSource(
        's.ts',
        'import { sentence } from "./x"\nexport const o = () => ({ title: sentence(2) })',
      ),
    ).toEqual([])
  })
})

describe('imperative DOM text', () => {
  it('reports a literal assigned to textContent', () => {
    const hits = scanSource(
      's.ts',
      "export const f = (el: HTMLElement) => { el.textContent = 'Queued' }",
    )
    expect(hits).toEqual([{ file: 's.ts', line: 1, context: 'dom:textContent', snippet: 'Queued' }])
  })

  it('reports a literal set as an accessible attribute', () => {
    const hits = scanSource(
      's.ts',
      "export const f = (el: HTMLElement) => { el.setAttribute('aria-label', 'Close') }",
    )
    expect(hits.map((h) => h.context)).toEqual(['dom:setAttribute:aria-label'])
  })

  it('stays silent for dataset, which is data and never copy', () => {
    expect(
      scanSource('s.ts', "export const f = (el: HTMLElement) => { el.dataset.hint = 'switch' }"),
    ).toEqual([])
  })

  it('stays silent for a glyph or a separator', () => {
    expect(
      scanSource('s.ts', "export const f = (el: HTMLElement) => { el.textContent = '·' }"),
    ).toEqual([])
  })
})
