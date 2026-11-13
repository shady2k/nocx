// Connection-owner fixture gate (ADR-ba6xet1b) — proves the renderer's
// connection-owner rules still bite, in both directions.
//
// A rule can be weakened, renamed or deleted while CI stays green because
// nothing tests that it fires; this file is that test. The mechanism is the one
// src/eslint-fixture-gate.test.ts uses for Rule 9: eslint.config.js exports
// `connectionOwnerBlocks` — the exact block production wires in — and each
// fixture under eslint-fixtures/ is read and linted through THAT object at a
// virtual path inside src/, which is what makes the production globs and the
// rule's own scope (tests, generated, test-support, *-fixtures.ts) apply.
//
// The fixtures are deliberately broken code: eslint-fixtures/ is ignored by the
// normal lint, typecheck and prettier runs, and vitest's include pattern never
// matches it.
//
// Two directions, and the second is why the first means anything:
//
//   - the error-text panel and the open/reconnect call FAIL with the rule's id;
//   - the same call inside src/connection/ PASSES (the owner is allowed), and a
//     file of ordinary code — a typed discriminator, an emptiness check, a
//     switch on a declared union — reports nothing.
//
// The last block checks that the collector the CLI and the updater run
// (lint-fixtures/connection-owners.mjs) sees the same sites through the same
// rules. It is a second entry point, not a second implementation, and this is
// what keeps it one.
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
  connectionOwnerBlocks: FlatBlock[]
  default: FlatBlock[]
}
const { connectionOwnerBlocks } = eslintModule
const productionConfig = eslintModule.default

interface Violation {
  rule: string
  file: string
  text: string
  line?: number
}

const owners = (await import(resolve(FRONTEND_ROOT, 'lint-fixtures/connection-owners.mjs'))) as {
  violationsInText: (file: string, text: string) => Violation[]
  loadBaseline: () => Array<Violation & { reason: string }>
  unreasonedEntries: (baseline: Array<Violation & { reason: string }>) => Violation[]
  unknownRuleEntries: (baseline: Array<Violation & { reason: string }>) => Violation[]
  connectionOwnerRules: Record<string, unknown>
}

const updater = (await import(
  resolve(FRONTEND_ROOT, 'lint-fixtures/update-connection-owners-baseline.mjs')
)) as {
  planBaselineUpdate: (input: {
    violations: Violation[]
    old: Array<Violation & { reason: string }>
    allowGrowth?: boolean
  }) => {
    entries: Array<Violation & { reason: string }>
    growth: Violation[]
    dropped: Violation[]
  }
}

const linter = new Linter({ configType: 'flat' })

/** Lint a fixture's source through the production block, standing in for a
 *  real file at `virtualPath`. */
function lintFixture(file: string, virtualPath: string): Linter.LintMessage[] {
  const source = readFileSync(join(FIXTURE_DIR, file), 'utf8')
  return linter.verify(source, connectionOwnerBlocks as Linter.FlatConfig[], {
    filename: resolve(FRONTEND_ROOT, virtualPath),
  })
}

function reported(messages: Linter.LintMessage[]): Linter.LintMessage[] {
  // ruleId null is ESLint's "no matching configuration" advisory, not a report
  return messages.filter((m) => m.ruleId !== null)
}

describe('ADR-ba6xet1b — the renderer decides no connection fact', () => {
  it('a panel matching error text is reported, once per decision', () => {
    const messages = reported(lintFixture('connection-error-text-match.tsx', 'src/ports-extra.tsx'))
    expect(messages.length).toBeGreaterThanOrEqual(4)
    for (const m of messages) {
      expect(m.ruleId).toBe('nocx/no-error-text-match')
      expect(m.message).toContain('matching error text')
    }
    // Every decision the panel makes must be named in the report: the message
    // text IS the site, which is what a person reads and what the baseline key
    // is made of.
    const sites = messages.map((m) => m.message).join('\n')
    expect(sites).toContain("message.includes('disconnected')")
    expect(sites).toContain('/not connected|ws closed/.test(message)')
    expect(sites).toContain("r.startsWith('not connected')")
    expect(sites).toContain("err.data?.reason === 'unknown-session'")
  })

  it('the reconnect entry outside src/connection/ is reported', () => {
    const messages = reported(lintFixture('connection-open-call.ts', 'src/panes-extra.ts'))
    expect(messages.map((m) => m.ruleId)).toEqual([
      'nocx/no-connection-open',
      'nocx/no-connection-open',
    ])
    expect(messages.map((m) => m.message).join('\n')).toContain('"openSSHSession"')
    expect(messages.map((m) => m.message).join('\n')).toContain('"reconnect"')
  })

  it('the same call inside src/connection/ passes', () => {
    expect(reported(lintFixture('connection-open-call.ts', 'src/connection/open.ts'))).toEqual([])
  })

  it('a file with no connection code reports nothing', () => {
    expect(reported(lintFixture('connection-clean.ts', 'src/vault-label.ts'))).toEqual([])
  })

  it('a test file is not read at all', () => {
    expect(
      reported(lintFixture('connection-error-text-match.tsx', 'src/ports-extra.test.tsx')),
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

  it('excludes the fixture directory from the normal lint run', () => {
    const ignoreBlock = productionConfig.find((b) => Array.isArray(b.ignores))
    expect(ignoreBlock?.ignores).toContain('eslint-fixtures/**')
  })
})

describe('ADR-ba6xet1b — the collector the CLI runs sees the same sites', () => {
  it('reports the same decisions as the eslint block, through the same rules', () => {
    for (const [fixture, virtualPath] of [
      ['connection-error-text-match.tsx', 'src/ports-extra.tsx'],
      ['connection-open-call.ts', 'src/panes-extra.ts'],
    ] as const) {
      const source = readFileSync(join(FIXTURE_DIR, fixture), 'utf8')
      const fromEslint = reported(lintFixture(fixture, virtualPath))
      const fromCollector = owners.violationsInText(`frontend/${virtualPath}`, source)
      expect(fromCollector.map((v) => v.rule)).toEqual(fromEslint.map((m) => m.ruleId))
      expect(fromCollector.map((v) => v.line)).toEqual(fromEslint.map((m) => m.line))
      for (const v of fromCollector) expect(v.text.length).toBeGreaterThan(0)
    }
  })

  it('the committed baseline explains every entry and names a known rule', () => {
    const baseline = owners.loadBaseline()
    expect(baseline.length).toBeGreaterThan(0)
    expect(owners.unreasonedEntries(baseline)).toEqual([])
    expect(owners.unknownRuleEntries(baseline)).toEqual([])
  })
})

describe('ADR-ba6xet1b — the baseline may shrink and not grow', () => {
  const known = {
    rule: 'nocx/no-connection-open',
    file: 'frontend/src/terminal-content.ts',
    text: 'reconnect',
    reason: 'the pane reconnects itself',
  }
  const fresh = {
    rule: 'nocx/no-error-text-match',
    file: 'frontend/src/ports-extra.tsx',
    text: "message.includes('disconnected')",
  }

  it('refuses a site the existing list does not have', () => {
    expect(() => updater.planBaselineUpdate({ violations: [known, fresh], old: [known] })).toThrow(
      /REFUSING to update baseline/,
    )
  })

  it('adds it only with --allow-growth, and with no reason yet', () => {
    const plan = updater.planBaselineUpdate({
      violations: [known, fresh],
      old: [known],
      allowGrowth: true,
    })
    expect(plan.growth).toHaveLength(1)
    const added = plan.entries.find((e) => e.text === fresh.text)
    expect(added?.reason).toBe('')
  })

  it('drops a site that is gone and keeps every other reason', () => {
    const plan = updater.planBaselineUpdate({ violations: [known], old: [known] })
    expect(plan.growth).toEqual([])
    expect(plan.entries).toEqual([known])
    const shrunk = updater.planBaselineUpdate({ violations: [], old: [known] })
    expect(shrunk.dropped).toEqual([known])
    expect(shrunk.entries).toEqual([])
  })
})
