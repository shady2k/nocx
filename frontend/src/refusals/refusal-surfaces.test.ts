// The refusal-surface table, held to the source (ADR-8eej3e66 stage 1).
//
// The table in refusal-surfaces.ts is the enumeration the record's stage 1
// requires: every surface a person's call can be refused on, with its own
// answer, and every background call with the decision that it stays quiet.
// A table nobody checks is prose. These three checks make it a gate:
//
// 1. EVERY CALL IN THE SOURCE IS IN THE TABLE. A new `dispatcher.call('x.y')`
//    anywhere in `src/` fails this test until it is given a row, which forces
//    the author to decide the origin and what a refusal looks like.
// 2. NO ROW IS DECLARED TWICE, and the table names no method the source does
//    not call — a row left behind by a deleted method would keep pretending
//    to answer for something.
// 3. A PERSON-FACING ROW SHOWS SOMETHING, AND IT IS NOT THE REMOVED
//    SENTENCE. Only a background row may say nothing is shown, and no row
//    may describe the failure with "busy" or "try again in a moment".
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'
import { REFUSAL_SURFACES } from './refusal-surfaces'

const SRC = new URL('..', import.meta.url).pathname

/** Every `.ts`/`.tsx` under src/, except the generated contracts and the
 *  tests themselves (a test's own `call('some.method')` is not a surface). */
function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) {
      if (name === 'generated') continue
      out.push(...sourceFiles(full))
      continue
    }
    if (!name.endsWith('.ts') && !name.endsWith('.tsx')) continue
    if (name.includes('.test.')) continue
    out.push(full)
  }
  return out
}

/** `dispatcher.call('method'`, `this.call<T>('method'` and the plain
 *  `.call('method'` form the data-plane client uses. */
const CALL = /\.call(?:<[^>]*>)?\(\s*'([a-zA-Z][\w.]*)'/g

function calledMethods(): Map<string, string[]> {
  const found = new Map<string, string[]>()
  for (const file of sourceFiles(SRC)) {
    const text = readFileSync(file, 'utf8')
    for (const match of text.matchAll(CALL)) {
      const method = match[1]
      const files = found.get(method) ?? []
      files.push(relative(SRC, file))
      found.set(method, files)
    }
  }
  return found
}

describe('the refusal-surface enumeration', () => {
  it('names every method the source calls, and nothing it does not', () => {
    const called = calledMethods()
    const declared = new Map<string, number>()
    for (const row of REFUSAL_SURFACES) {
      for (const method of row.methods) {
        declared.set(method, (declared.get(method) ?? 0) + 1)
      }
    }

    const missing = [...called.keys()].filter((m) => !declared.has(m)).sort()
    expect(
      missing,
      'a dispatcher.call() with no row in refusal-surfaces.ts — decide its origin and what a refusal looks like',
    ).toEqual([])

    const stale = [...declared.keys()].filter((m) => !called.has(m)).sort()
    expect(stale, 'a row for a method the source no longer calls').toEqual([])

    const twice = [...declared.entries()]
      .filter(([, n]) => n > 1)
      .map(([m]) => m)
      .sort()
    expect(twice, 'a method answered by two rows').toEqual([])
  })

  it('makes a person-facing row say what is shown and how to ask again', () => {
    for (const row of REFUSAL_SURFACES) {
      if (row.origin === 'background') continue
      expect(
        row.report.kind,
        `${row.methods[0]}: a person can reach this call, so its refusal cannot be silent`,
      ).not.toBe('background')
    }
  })

  it('never describes a refusal with the sentence this stage removes', () => {
    const banned = /\bbusy\b|try again in a moment/i
    for (const row of REFUSAL_SURFACES) {
      const prose = [
        row.report.where,
        row.note ?? '',
        row.report.kind === 'background' ? '' : row.report.retry,
      ].join(' ')
      expect(prose, `${row.methods[0]}: the refusal's words`).not.toMatch(banned)
    }
  })
})
