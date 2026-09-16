#!/usr/bin/env node
/**
 * Regenerate `.githooks/log-context-baseline.json` from the current
 * log-context ratchet output (nocx-n14oo.9).
 *
 * Usage: node .githooks/update-log-context-baseline.mjs   (from the repo root)
 *
 * Growth guard, the same shape update-deadcode-baseline.mjs and
 * lint-fixtures/update-raw-controls-baseline.mjs use: refuses to write a
 * baseline containing a violation absent from the existing one. Only a pure
 * shrink or no-change is allowed, so regenerating cannot silently legitimize
 * a new un-migrated log call. Removing entries — migrating a call site onto
 * log.From(ctx) — is the one direction that never fails.
 */
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { BASELINE_PATH, collectLogContextViolations, violationKey } from './check-log-context.mjs'

const bootstrap = !existsSync(BASELINE_PATH)
const oldBaseline = new Map()
if (!bootstrap) {
  const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
  for (const v of data.violations) oldBaseline.set(violationKey(v), v)
}

const violations = collectLogContextViolations()

const growth = bootstrap ? [] : violations.filter((v) => !oldBaseline.has(violationKey(v)))
if (growth.length > 0) {
  console.error(
    `REFUSING to update baseline: ${growth.length} violation(s) are not in the existing baseline.`,
  )
  for (const v of growth) console.error(`  NEW: ${v.file}:${v.line}: ${v.text}`)
  console.error('Route them through log.From(ctx) first, or add them deliberately by hand.')
  process.exit(1)
}

violations.sort((a, b) => violationKey(a).localeCompare(violationKey(b)))

const content = JSON.stringify(
  {
    '//': [
      'DO NOT EDIT MANUALLY. Regenerate with `node .githooks/update-log-context-baseline.mjs`.',
      '',
      'Every entry is a logging call check-log-context.mjs could not prove goes through',
      'log.From(ctx) (module/trace/span/request travel automatically only on that path,',
      'internal/log/context.go). It may only shrink: migrating a call site onto',
      'log.From(ctx) and re-running this script drops its entry; nothing else does.',
      '',
      'Keyed on file + the trimmed call line, not a line number, so an unrelated edit',
      'above a baselined line does not make this file churn — the same tradeoff',
      'check-control-goroutines.mjs makes for its own baseline.',
    ],
    violations,
  },
  null,
  2,
)

writeFileSync(resolve(BASELINE_PATH), content + '\n')
console.log(`Baseline written: ${violations.length} call site(s) not yet through log.From(ctx).`)
