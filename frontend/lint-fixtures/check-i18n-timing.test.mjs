import { describe, expect, it } from 'vitest'
import { scanSource } from './check-i18n-timing.mjs'

/**
 * The i18n TIMING checker's own tests (nocx-dej6.3.8's review, defects 1–2).
 *
 * A `t()` that runs once cannot follow the language: it reads the locale
 * signal at module evaluation, at component setup, in a class field, or in the
 * function a module invokes at its own top level — and the surface keeps that
 * answer for the life of the process. Each shape is asserted here, and so is
 * every position that must stay silent (JSX, nested handlers, lazy builders,
 * plainly-named helpers), because a checker that fires on the sanctioned shapes
 * would be switched off rather than fixed.
 */

describe('must trip', () => {
  it('a call at module scope', () => {
    const hits = scanSource('s.ts', "const TITLE = t('a.title', 'A title')")
    expect(hits).toEqual([
      {
        file: 's.ts',
        line: 1,
        context: 'module',
        key: 'a.title',
        detail: 'module scope — runs when the module is imported',
      },
    ])
  })

  it('a call in a module-level map', () => {
    const hits = scanSource(
      's.ts',
      "const LABELS = { one: t('a.one', 'One'), two: t('a.two', 'Two') }",
    )
    expect(hits.map((h) => h.key)).toEqual(['a.one', 'a.two'])
  })

  it('a call in a component body, outside JSX', () => {
    const hits = scanSource(
      's.tsx',
      `import { t } from './i18n/locale-model'
export function VaultSection() {
  const OPTIONS = [t('a.never', 'Never')]
  return <div />
}`,
    )
    expect(hits.map((h) => [h.context, h.key])).toEqual([['setup', 'a.never']])
  })

  it('a call in the function a module invokes at its own top level', () => {
    const hits = scanSource(
      's.tsx',
      `import { t } from './i18n/locale-model'
function main(): void {
  const VIEW = { title: t('app.page.ports', 'Ports') }
  void VIEW
}
main()`,
    )
    expect(hits.map((h) => [h.context, h.key])).toEqual([['setup', 'app.page.ports']])
  })

  it('a call in a class field initialiser', () => {
    const hits = scanSource(
      's.ts',
      `import { t } from './i18n/locale-model'
export class Provider {
  readonly label = t('a.label', 'A label')
}`,
    )
    expect(hits.map((h) => [h.context, h.key])).toEqual([['field', 'a.label']])
  })

  it('tPlural is the same defect as t', () => {
    const hits = scanSource('s.ts', "const N = tPlural('a.count', n, '{count} things')")
    expect(hits.map((h) => h.context)).toEqual(['module'])
  })

  it('fails closed on a parse error', () => {
    const hits = scanSource('s.ts', 'export const x = = = broken')
    expect(hits).toEqual([
      { file: 's.ts', line: 0, context: 'PARSE', key: '', detail: expect.any(String) },
    ])
  })
})

describe('must stay silent', () => {
  it('a call in JSX — a getter the renderer tracks', () => {
    const hits = scanSource(
      's.tsx',
      `import { t } from './i18n/locale-model'
export function A() {
  return <b title={t('a.title', 'A title')}>{t('a.body', 'Body')}</b>
}`,
    )
    expect(hits).toEqual([])
  })

  it('a call inside an event handler', () => {
    const hits = scanSource(
      's.tsx',
      `import { t } from './i18n/locale-model'
export function A(props: { onClick: () => void }) {
  const fire = () => props.onClick(t('a.sent', 'Sent'))
  return <button onClick={fire}>x</button>
}`,
    )
    expect(hits).toEqual([])
  })

  it('a call inside a lazy builder', () => {
    const hits = scanSource(
      's.tsx',
      `import { createMemo } from 'solid-js'
import { t } from './i18n/locale-model'
export function A() {
  const title = createMemo(() => t('a.title', 'A title'))
  return <b>{title()}</b>
}`,
    )
    expect(hits).toEqual([])
  })

  it('a call inside a plainly-named helper, which runs when its result is used', () => {
    const hits = scanSource(
      's.ts',
      `import { t } from './i18n/locale-model'
function sentence(n: number): string {
  return t('a.row', '{count} rows')
}
export const label = (n: number) => sentence(n)`,
    )
    expect(hits).toEqual([])
  })

  it('a method body — it runs when the method is called', () => {
    const hits = scanSource(
      's.ts',
      `import { t } from './i18n/locale-model'
export class Chip {
  render(): string {
    return t('a.chip', 'Chip')
  }
}`,
    )
    expect(hits).toEqual([])
  })
})
