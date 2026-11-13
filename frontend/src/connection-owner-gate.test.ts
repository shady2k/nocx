// Connection-owner fixture gate (ADR-ba6xet1b) — proves the renderer's
// connection-owner rules still bite, in both directions, and that they decide
// identity rather than spelling.
//
// A rule can be weakened, renamed or deleted while CI stays green because
// nothing tests that it fires; this file is that test. eslint.config.js exports
// `connectionOwnerBlocks` — the exact block production wires in — and each
// fixture under eslint-fixtures/ is read and linted through THAT object at a
// virtual path inside src/, which is what makes the production globs and the
// rule's own scope (tests, generated, test-support, *-fixtures.ts) apply.
//
// The rules ask the TypeScript checker (nocx-oalo1.3.5), so the fixtures are
// linted against a program this file builds from the fixture TEXT at the
// virtual path. A fixture exists nowhere on disk; without that program the
// identity question has no answer, and the rules say so instead of silently
// falling back to names. The program is built once, with every fixture at
// every path the tests use — outside src/connection/, inside it, and as a
// test file — because a new path means a new program otherwise.
//
// The fixtures are deliberately broken code: eslint-fixtures/ is ignored by the
// normal lint, typecheck and prettier runs, and vitest's include pattern never
// matches it.
//
// Two directions, and the second is why the first means anything:
//
//   - the error-text panel, the bound/destructured client member, the
//     subscription through a const and the loss through a const FAIL;
//   - the SAME sources inside src/connection/ PASS (the owner is allowed), an
//     unrelated type's `reconnect` PASSES (identity, not spelling), and a file
//     of ordinary code reports nothing.
//
// The last blocks check the collector the CLI and the updater run
// (lint-fixtures/connection-owners.mjs) sees the same sites through the same
// rules, and that the baseline exemption is a count rather than a sentence.
import { describe, expect, it } from 'vitest'
import { Linter } from 'eslint'
import ts from 'typescript'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, resolve } from 'node:path'

const FRONTEND_ROOT = resolve(fileURLToPath(new URL('.', import.meta.url)), '..')
const FIXTURE_DIR = join(FRONTEND_ROOT, 'eslint-fixtures')

interface FlatBlock {
  files?: string[]
  ignores?: string[]
  rules?: Record<string, unknown>
  languageOptions?: Record<string, unknown>
}

const eslintModule = (await import(resolve(FRONTEND_ROOT, 'eslint.config.js'))) as {
  connectionOwnerBlocks: FlatBlock[]
  default: FlatBlock[]
}
const { connectionOwnerBlocks } = eslintModule
const productionConfig = eslintModule.default

interface Violation {
  rule: string
  file: string
  symbol: string
  target: string
  text: string
  line?: number
  count?: number
  reason?: string
}

const owners = (await import(resolve(FRONTEND_ROOT, 'lint-fixtures/connection-owners.mjs'))) as {
  violationsInText: (
    file: string,
    text: string,
    parserOptions?: Record<string, unknown>,
  ) => Violation[]
  loadBaseline: () => Violation[]
  unreasonedEntries: (baseline: Violation[]) => Violation[]
  unknownRuleEntries: (baseline: Violation[]) => Violation[]
  compareToBaseline: (
    current: Violation[],
    baseline: Violation[],
  ) => {
    fresh: Array<{ key: string; count: number }>
    grew: Array<{ entry: Violation; count: number }>
    shrank: Array<{ entry: Violation; count: number }>
    stale: Violation[]
  }
  connectionOwnerRules: Record<string, unknown>
}

const updater = (await import(
  resolve(FRONTEND_ROOT, 'lint-fixtures/update-connection-owners-baseline.mjs')
)) as {
  planBaselineUpdate: (input: {
    violations: Violation[]
    old: Violation[]
    allowGrowth?: boolean
  }) => {
    entries: Violation[]
    growth: Violation[]
    dropped: Violation[]
    shrunk: Array<{ entry: Violation; was: number }>
  }
}

const linter = new Linter({ configType: 'flat' })

// ─── The type program the fixtures are checked against ───────────────────────
const FIXTURES = readdirSync(FIXTURE_DIR)
  .filter((file) => /^connection-.*\.(ts|tsx)$/.test(file))
  .sort()

type Place = 'outside' | 'inside' | 'test'

function virtualPathFor(fixture: string, place: Place): string {
  const ext = fixture.endsWith('.tsx') ? '.tsx' : '.ts'
  const base = fixture.slice(0, -ext.length)
  if (place === 'inside') return `src/connection/${fixture}`
  if (place === 'test') return `src/${base}.test${ext}`
  return `src/${fixture}`
}

/** The fixture's source at a virtual path. `./ipc` is the client type from
 *  src/ and `../ipc` from src/connection/; one file is linted at both depths,
 *  and nothing else about the code changes. */
function sourceFor(fixture: string, virtualPath: string): string {
  const source = readFileSync(join(FIXTURE_DIR, fixture), 'utf8')
  if (!virtualPath.startsWith('src/connection/')) return source
  // One fixture is linted at both depths; only the relative import changes.
  return source
    .replaceAll("from './ipc'", "from '../ipc'")
    .replaceAll("from './dispatcher'", "from '../dispatcher'")
}

const FIXTURE_PROGRAM = (() => {
  const configPath = join(FRONTEND_ROOT, 'tsconfig.json')
  const read = ts.readConfigFile(configPath, (file) => ts.sys.readFile(file))
  if (read.error) throw new Error(ts.flattenDiagnosticMessageText(read.error.messageText, '\n'))
  const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, FRONTEND_ROOT)
  const sources = new Map<string, string>()
  for (const fixture of FIXTURES) {
    for (const place of ['outside', 'inside', 'test'] as const) {
      const virtualPath = virtualPathFor(fixture, place)
      sources.set(resolve(FRONTEND_ROOT, virtualPath), sourceFor(fixture, virtualPath))
    }
  }
  const host = ts.createCompilerHost(parsed.options, true)
  const original = host.getSourceFile.bind(host)
  host.getSourceFile = (fileName, languageVersion, onError, shouldCreateNewSourceFile) => {
    const text = sources.get(resolve(fileName))
    return text === undefined
      ? original(fileName, languageVersion, onError, shouldCreateNewSourceFile)
      : ts.createSourceFile(fileName, text, languageVersion, true)
  }
  host.fileExists = (fileName) => sources.has(resolve(fileName)) || ts.sys.fileExists(fileName)
  host.readFile = (fileName) => sources.get(resolve(fileName)) ?? ts.sys.readFile(fileName)
  return ts.createProgram([...parsed.fileNames, ...sources.keys()], parsed.options, host)
})()

/** The production block's rules, over a program holding the fixture text. */
function fixtureBlocks(): FlatBlock[] {
  return connectionOwnerBlocks.map((block) => ({
    ...block,
    languageOptions: { ...block.languageOptions, parserOptions: { programs: [FIXTURE_PROGRAM] } },
  }))
}

/** Lint a fixture's source through the production block, standing in for a
 *  real file at `virtualPath`. */
function lintFixture(fixture: string, virtualPath: string): Linter.LintMessage[] {
  if (!FIXTURE_PROGRAM.getSourceFile(resolve(FRONTEND_ROOT, virtualPath))) {
    throw new Error(`${virtualPath} is not in the fixture type program`)
  }
  return linter.verify(sourceFor(fixture, virtualPath), fixtureBlocks() as Linter.FlatConfig[], {
    filename: resolve(FRONTEND_ROOT, virtualPath),
  })
}

function reported(messages: Linter.LintMessage[]): Linter.LintMessage[] {
  // ruleId null is ESLint's "no matching configuration" advisory, not a report
  return messages.filter((m) => m.ruleId !== null)
}

function ruleIds(fixture: string, virtualPath: string): Array<string | null> {
  return reported(lintFixture(fixture, virtualPath)).map((m) => m.ruleId)
}

describe('ADR-ba6xet1b — the renderer decides no connection fact', () => {
  it('a panel matching error text is reported, once per decision', () => {
    const messages = reported(
      lintFixture('connection-error-text-match.tsx', 'src/connection-error-text-match.tsx'),
    )
    expect(messages.length).toBeGreaterThanOrEqual(5)
    for (const m of messages) {
      expect(m.ruleId).toBe('nocx/no-error-text-match')
      expect(m.message).toContain('matching error text')
    }
    // Every decision the panel makes must be named in the report: the message
    // text IS the site a person reads.
    const sites = messages.map((m) => m.message).join('\n')
    expect(sites).toContain("message.includes('disconnected')")
    expect(sites).toContain('/not connected|ws closed/.test(message)')
    expect(sites).toContain("r.startsWith('not connected')")
    expect(sites).toContain("err.data?.reason === 'unknown-session'")
    // A computed property read names the same member (bypass 3).
    expect(sites).toContain("e['message'].includes('disconnected')")
  })

  it('a client member bound off the client is reported (bypass 1)', () => {
    const messages = reported(lintFixture('connection-open-call.ts', 'src/connection-open-call.ts'))
    expect(messages.map((m) => m.ruleId)).toEqual([
      'nocx/no-connection-open',
      'nocx/no-connection-open',
    ])
    const sites = messages.map((m) => m.message).join('\n')
    expect(sites).toContain('"openSSHSession"')
    expect(sites).toContain('"openSSHSessionByHost"')
  })

  it('the same references inside src/connection/ pass', () => {
    expect(ruleIds('connection-open-call.ts', 'src/connection/connection-open-call.ts')).toEqual([])
  })

  it("an unrelated type's reconnect is not an open (identity, not spelling)", () => {
    expect(ruleIds('connection-open-unrelated.ts', 'src/connection-open-unrelated.ts')).toEqual([])
  })

  it('a subscription through a const is reported (bypass 2)', () => {
    const messages = reported(
      lintFixture('connection-subscription-const.ts', 'src/connection-subscription-const.ts'),
    )
    expect(messages.map((m) => m.ruleId)).toEqual([
      'nocx/no-connection-subscription',
      'nocx/no-connection-subscription',
    ])
    const sites = messages.map((m) => m.message).join('\n')
    expect(sites).toContain("'exit'")
    expect(sites).toContain("'session.liveness'")
  })

  it('the same subscriptions inside src/connection/ pass', () => {
    expect(
      ruleIds(
        'connection-subscription-const.ts',
        'src/connection/connection-subscription-const.ts',
      ),
    ).toEqual([])
  })

  it('a loss minted through a const is reported (bypass 4)', () => {
    const messages = reported(
      lintFixture('connection-loss-const.ts', 'src/connection-loss-const.ts'),
    )
    expect(messages.map((m) => m.ruleId)).toEqual([
      'nocx/no-connection-loss',
      'nocx/no-connection-loss',
    ])
    expect(messages.map((m) => m.message).join('\n')).toContain('mints a lost session')
  })

  it('the same minted losses inside src/connection/ pass', () => {
    expect(ruleIds('connection-loss-const.ts', 'src/connection/connection-loss-const.ts')).toEqual(
      [],
    )
  })

  it('a computed client key is decided by its type, and an unknown key is refused', () => {
    const messages = reported(
      lintFixture('connection-open-element.ts', 'src/connection-open-element.ts'),
    )
    expect(messages.map((m) => m.ruleId)).toEqual([
      'nocx/no-connection-open',
      'nocx/no-connection-open',
      'nocx/no-connection-open',
    ])
    const text = messages.map((m) => m.message).join('\n')
    expect((text.match(/"openSSHSession"/g) ?? []).length).toBe(2)
    expect(text).toContain('cannot resolve to a literal')
  })

  it('the same computed client accesses inside src/connection/ pass', () => {
    expect(
      ruleIds('connection-open-element.ts', 'src/connection/connection-open-element.ts'),
    ).toEqual([])
  })

  it('a destructured dispatcher subscribe is the same call, and an unknown event name is refused', () => {
    const messages = reported(
      lintFixture('connection-subscription-reached.ts', 'src/connection-subscription-reached.ts'),
    )
    expect(messages.map((m) => m.ruleId)).toEqual([
      'nocx/no-connection-subscription',
      'nocx/no-connection-subscription',
      'nocx/no-connection-subscription',
      'nocx/no-connection-subscription',
    ])
    const text = messages.map((m) => m.message).join('\n')
    expect(text).toContain("'exit'")
    expect(text).toContain("'session.liveness'")
    expect((text.match(/cannot resolve to a literal/g) ?? []).length).toBe(2)
  })

  it('the same reached subscriptions inside src/connection/ pass', () => {
    expect(
      ruleIds(
        'connection-subscription-reached.ts',
        'src/connection/connection-subscription-reached.ts',
      ),
    ).toEqual([])
  })

  it("another type's subscribe with a dynamic name is not the dispatcher's", () => {
    expect(
      ruleIds('connection-subscription-unrelated.ts', 'src/connection-subscription-unrelated.ts'),
    ).toEqual([])
  })

  it('a computed error key resolves by type, and an unknown key on a failure is reported', () => {
    const messages = reported(
      lintFixture('connection-error-computed.ts', 'src/connection-error-computed.ts'),
    )
    expect(messages.map((m) => m.ruleId)).toEqual([
      'nocx/no-error-text-match',
      'nocx/no-error-text-match',
    ])
    const text = messages.map((m) => m.message).join('\n')
    expect(text).toContain('matching error text')
    expect(text).toContain('cannot resolve to a literal')
    expect(text).toContain("e[FIELD].includes('disconnected')")
    expect(text).toContain('e[dynamicField]')
  })

  it('a computed cause key and an as-const value mint the same loss', () => {
    const messages = reported(
      lintFixture('connection-loss-computed.ts', 'src/connection-loss-computed.ts'),
    )
    expect(messages.map((m) => m.ruleId)).toEqual(['nocx/no-connection-loss'])
  })

  it('the same computed loss inside src/connection/ passes', () => {
    expect(
      ruleIds('connection-loss-computed.ts', 'src/connection/connection-loss-computed.ts'),
    ).toEqual([])
  })

  it('a file with no connection code reports nothing', () => {
    expect(ruleIds('connection-clean.ts', 'src/connection-clean.ts')).toEqual([])
  })

  it('a test file is not read at all', () => {
    expect(
      ruleIds('connection-error-text-match.tsx', 'src/connection-error-text-match.test.tsx'),
    ).toEqual([])
  })
})

describe('ADR-ba6xet1b — the production config carries the tested block', () => {
  const files = ['src/**/*.{ts,tsx}']

  it('wires the block into the exported config', () => {
    const wired = productionConfig.filter((b) => JSON.stringify(b.files) === JSON.stringify(files))
    expect(wired).toHaveLength(1)
  })

  it('keeps the wired rules identical to the block under test (no drift, no weakening)', () => {
    const wired = productionConfig.find((b) => JSON.stringify(b.files) === JSON.stringify(files))
    const fragment = connectionOwnerBlocks.find(
      (b) => JSON.stringify(b.files) === JSON.stringify(files),
    )
    expect(wired?.rules).toEqual(fragment?.rules)
    for (const rule of [
      'nocx/no-error-text-match',
      'nocx/no-connection-subscription',
      'nocx/no-connection-open',
      'nocx/no-connection-loss',
    ]) {
      expect(wired?.rules?.[rule]).toBe('error')
    }
  })

  it('gives the tested block the type program its identity rules ask for', () => {
    const fragment = connectionOwnerBlocks[0]
    const parserOptions = fragment.languageOptions?.parserOptions as {
      project?: string[]
      tsconfigRootDir?: string
    }
    expect(parserOptions.project).toContain('./tsconfig.json')
    expect(parserOptions.tsconfigRootDir).toBe(FRONTEND_ROOT)
  })

  it('excludes the fixture directory from the normal lint run', () => {
    const ignoreBlock = productionConfig.find((b) => Array.isArray(b.ignores))
    expect(ignoreBlock?.ignores).toContain('eslint-fixtures/**')
  })
})

describe('ADR-ba6xet1b — the collector the CLI runs sees the same sites', () => {
  it('reports the same decisions as the eslint block, through the same rules', () => {
    for (const [fixture, virtualPath] of [
      ['connection-error-text-match.tsx', 'src/connection-error-text-match.tsx'],
      ['connection-open-call.ts', 'src/connection-open-call.ts'],
      ['connection-subscription-const.ts', 'src/connection-subscription-const.ts'],
      ['connection-subscription-reached.ts', 'src/connection-subscription-reached.ts'],
      ['connection-loss-const.ts', 'src/connection-loss-const.ts'],
      ['connection-loss-computed.ts', 'src/connection-loss-computed.ts'],
      ['connection-open-element.ts', 'src/connection-open-element.ts'],
      ['connection-error-computed.ts', 'src/connection-error-computed.ts'],
    ] as const) {
      const fromEslint = reported(lintFixture(fixture, virtualPath))
      const fromCollector = owners.violationsInText(
        `frontend/${virtualPath}`,
        sourceFor(fixture, virtualPath),
        { programs: [FIXTURE_PROGRAM] },
      )
      expect(fromCollector.map((v) => v.rule)).toEqual(fromEslint.map((m) => m.ruleId))
      expect(fromCollector.map((v) => v.line)).toEqual(fromEslint.map((m) => m.line))
      for (const v of fromCollector) {
        expect(v.text.length).toBeGreaterThan(0)
        expect(v.symbol.length).toBeGreaterThan(0)
        expect(v.target.length).toBeGreaterThan(0)
      }
    }
  })

  it('the committed baseline explains every entry, names a known rule and counts occurrences', () => {
    const baseline = owners.loadBaseline()
    expect(baseline.length).toBeGreaterThan(0)
    expect(owners.unreasonedEntries(baseline)).toEqual([])
    expect(owners.unknownRuleEntries(baseline)).toEqual([])
    for (const entry of baseline) {
      expect(entry.symbol).toBeTruthy()
      expect(entry.target).toBeTruthy()
      expect(Number.isInteger(entry.count)).toBe(true)
      expect(entry.count).toBeGreaterThan(0)
    }
    expect(new Set(baseline.map((e) => `${e.rule}:${e.file}:${e.symbol}:${e.target}`)).size).toBe(
      baseline.length,
    )
  })
})

describe('ADR-ba6xet1b — the baseline exempts occurrences, not sentences', () => {
  const key = {
    rule: 'nocx/no-connection-subscription',
    file: 'frontend/src/ipc.ts',
    symbol: 'WSClient.constructor',
    target: 'exit',
    text: "'exit'",
  }
  const known = { ...key, count: 1, reason: 'the client folds a session exit itself' }
  const fresh = {
    rule: 'nocx/no-error-text-match',
    file: 'frontend/src/ports.tsx',
    symbol: 'listingFailureMessage',
    target: 'includes:"disconnected"',
    text: "r.includes('disconnected')",
  }

  it('refuses a key the existing list does not have', () => {
    expect(() => updater.planBaselineUpdate({ violations: [known, fresh], old: [known] })).toThrow(
      /REFUSING to update baseline/,
    )
  })

  it('refuses a second occurrence of an existing key (bypass 5)', () => {
    expect(() =>
      updater.planBaselineUpdate({ violations: [known, { ...known }], old: [known] }),
    ).toThrow(/REFUSING to update baseline/)
  })

  it('adds it only with --allow-growth, and with no reason yet', () => {
    const plan = updater.planBaselineUpdate({
      violations: [known, fresh],
      old: [known],
      allowGrowth: true,
    })
    expect(plan.growth).toHaveLength(1)
    const added = plan.entries.find((e) => e.target === fresh.target)
    expect(added?.reason).toBe('')
  })

  it('raises a count only with --allow-growth', () => {
    const plan = updater.planBaselineUpdate({
      violations: [known, { ...known }],
      old: [known],
      allowGrowth: true,
    })
    expect(plan.growth).toHaveLength(1)
    expect(plan.entries[0].count).toBe(2)
    expect(plan.entries[0].reason).toBe(known.reason)
  })

  it('drops a key that is gone and keeps every other reason', () => {
    const plan = updater.planBaselineUpdate({ violations: [known], old: [known] })
    expect(plan.growth).toEqual([])
    expect(plan.entries).toEqual([known])
    const shrunk = updater.planBaselineUpdate({ violations: [], old: [known] })
    expect(shrunk.dropped).toEqual([known])
    expect(shrunk.entries).toEqual([])
  })

  it('fails the check on growth, on a new key and on a shrink without an update', () => {
    const grew = owners.compareToBaseline([known, { ...known }, fresh], [known])
    expect(grew.grew).toHaveLength(1)
    expect(grew.fresh).toHaveLength(1)

    const shrank = owners.compareToBaseline([known], [{ ...known, count: 2 }])
    expect(shrank.shrank).toHaveLength(1)

    const stale = owners.compareToBaseline(
      [known],
      [known, { ...known, target: 'session.liveness' }],
    )
    expect(stale.stale).toHaveLength(1)
  })
})
