// Rule 9 fixture gate (ADR-0024 §1, §7) — proves the lifecycle/renderer
// dependency-direction rules still bite. A rule can be weakened, renamed or
// deleted while CI stays green because nothing tests that it fires; this file
// is that test.
//
// Mechanism: eslint.config.js exports `lifecycleBoundaryBlocks` — the exact
// two no-restricted-imports blocks production wires in. Each negative fixture
// under eslint-fixtures/ violates one direction; this test reads the fixture
// source and runs it through THAT fragment with a virtual filename inside the
// production `files` glob (src/renderers/ for direction 1, src/lifecycle/ for
// direction 2). The fragment is used — not a copy — and the wiring assertions
// below prove the production config carries the same blocks, so weakening the
// production rule fails this test even if nothing else does.
//
// The fixtures are deliberately broken code: eslint-fixtures/ is ignored by
// the normal lint, typecheck and prettier runs, and vitest's include pattern
// never matches it.
import { describe, expect, it } from 'vitest'
import { Linter } from 'eslint'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, resolve } from 'node:path'

const FRONTEND_ROOT = resolve(fileURLToPath(new URL('.', import.meta.url)), '..')
const FIXTURE_DIR = join(FRONTEND_ROOT, 'eslint-fixtures')

interface FlatBlock {
  files?: string[]
  ignores?: string[]
  rules?: Record<string, unknown>
}

const eslintModule = (await import(resolve(FRONTEND_ROOT, 'eslint.config.js'))) as {
  lifecycleBoundaryBlocks: FlatBlock[]
  default: FlatBlock[]
}
const { lifecycleBoundaryBlocks: boundaryBlocks } = eslintModule
const productionConfig = eslintModule.default

const linter = new Linter({ configType: 'flat' })

/** Lint a fixture file's source through the production fragment, standing in
 *  for a real file at `virtualPath` (which is what makes the production
 *  `files` globs apply). */
function lintFixture(file: string, virtualPath: string): Linter.LintMessage[] {
  const source = readFileSync(join(FIXTURE_DIR, file), 'utf8')
  return linter.verify(source, boundaryBlocks as Linter.FlatConfig[], {
    filename: resolve(FRONTEND_ROOT, virtualPath),
  })
}

const MESSAGE = {
  rendererToLifecycle: 'the stream is render-only',
  rendererToForwardDeclared: 'the two-axis state machine lives in src/lifecycle/',
  lifecycleToParser: 'never the parsing surface',
  lifecycleToPassport: 'cannot activate a domain',
} as const

describe('Rule 9 dependency direction — negative fixtures fire', () => {
  it('a renderer importing lifecycle/authority state is reported on every import', () => {
    const messages = lintFixture(
      'renderer-imports-lifecycle.ts',
      'src/renderers/__gate_fixture__.ts',
    )
    expect(messages.length).toBeGreaterThanOrEqual(5)
    for (const m of messages) {
      expect(m.ruleId).toBe('no-restricted-imports')
    }
    // Both pattern groups of direction 1 must fire: today's modules and the
    // forward-declared lifecycle state.
    expect(messages.map((m) => m.message).join('\n')).toContain(MESSAGE.rendererToLifecycle)
    expect(messages.map((m) => m.message).join('\n')).toContain(MESSAGE.rendererToForwardDeclared)
  })

  it('a lifecycle module importing the OSC parsing surface is reported on every import', () => {
    const messages = lintFixture('lifecycle-imports-parser.ts', 'src/lifecycle/__gate_fixture__.ts')
    expect(messages.length).toBeGreaterThanOrEqual(3)
    for (const m of messages) {
      expect(m.ruleId).toBe('no-restricted-imports')
    }
    // Both pattern groups of direction 2 must fire: the renderer/OSC 133
    // surface and the OSC 636 passport parser.
    expect(messages.map((m) => m.message).join('\n')).toContain(MESSAGE.lifecycleToParser)
    expect(messages.map((m) => m.message).join('\n')).toContain(MESSAGE.lifecycleToPassport)
  })

  it('a clean renderer file and a file outside the boundary produce no reports', () => {
    // Negative control: the mechanism is scoped, not blanket. A renderer file
    // importing only renderer-internal modules stays silent…
    const clean = linter.verify(
      "import { XtermRenderer } from './xterm'\nexport const x = XtermRenderer",
      boundaryBlocks as Linter.FlatConfig[],
      { filename: resolve(FRONTEND_ROOT, 'src/renderers/xterm.ts') },
    )
    expect(clean).toEqual([])
    // …and the same banned import outside the globs is none of this rule's
    // business (the orchestrator seam consumes both sides today).
    const outside = linter.verify(
      "import { LifecycleKernel } from './lifecycle/state'",
      boundaryBlocks as Linter.FlatConfig[],
      { filename: resolve(FRONTEND_ROOT, 'src/terminal-content.ts') },
    )
    // (ruleId null = ESLint's "no matching configuration" advisory, not a rule
    // report — the fragment legitimately does not govern terminal-content.ts).
    expect(outside.filter((m) => m.ruleId !== null)).toEqual([])
  })
})

describe('Rule 9 — the production config carries the tested fragment', () => {
  const rendererFiles = ['src/renderers/**/*.{ts,tsx}']
  const lifecycleFiles = [
    'src/command-ledger.ts',
    'src/history-client.ts',
    'src/environment-passport.ts',
    'src/lifecycle/**/*.{ts,tsx}',
  ]

  it('wires both boundary blocks into the exported config', () => {
    const wired = (files: string[]) =>
      productionConfig.filter((b) => JSON.stringify(b.files) === JSON.stringify(files))
    expect(wired(rendererFiles)).toHaveLength(1)
    expect(wired(lifecycleFiles)).toHaveLength(1)
  })

  it('keeps the wired rules identical to the fragment (no drift, no weakening)', () => {
    for (const files of [rendererFiles, lifecycleFiles]) {
      const wired = productionConfig.find((b) => JSON.stringify(b.files) === JSON.stringify(files))
      const fragment = boundaryBlocks.find((b) => JSON.stringify(b.files) === JSON.stringify(files))
      expect(wired?.rules).toEqual(fragment?.rules)
    }
  })

  it('excludes the fixture directory from the normal lint run', () => {
    const ignoreBlock = productionConfig.find((b) => Array.isArray(b.ignores))
    expect(ignoreBlock?.ignores).toContain('eslint-fixtures/**')
  })
})
