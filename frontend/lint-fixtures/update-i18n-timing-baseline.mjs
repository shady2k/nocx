#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/i18n-timing-baseline.json` from the current
 * violations (the ratchet of check-i18n-timing.mjs).
 *
 * Usage: npm run baseline:i18n-timing-update
 * Run from `frontend/`.
 *
 * Deliberate regeneration, like every baseline here: growth is REFUSED — a
 * branch that resolved its strings at render must SHRINK the baseline, and a
 * new `t()` at module scope (or in a component body, or in a class field) is a
 * defect to fix, not an entry to mint.
 *
 * The violation objects come straight from the checker's AST scan, so the
 * baseline's keys are bit-identical to what the checker computes.
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { resolve } from 'node:path'
import { scanTree, violationKey } from './check-i18n-timing.mjs'

const SCRIPT_DIR = import.meta.dirname
const FRONTEND_DIR = resolve(SCRIPT_DIR, '..')
const BASELINE_PATH = resolve(SCRIPT_DIR, 'i18n-timing-baseline.json')

const violations = scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)

const PARSE = violations.filter((v) => v.context === 'PARSE')
if (PARSE.length > 0) {
  console.error('PARSE ERRORS present — fix the files before regenerating:')
  for (const v of PARSE) console.error(`  ${v.file}: ${v.detail}`)
  process.exit(1)
}

const oldMap = new Map()
if (existsSync(BASELINE_PATH)) {
  try {
    const old = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
    for (const v of old.violations ?? []) oldMap.set(violationKey(v), v)
  } catch {
    console.warn('Warning: existing baseline is invalid, rebuilding from scratch.')
  }
}

const newKeys = new Set(violations.map(violationKey))
const growth = [...newKeys].filter((k) => !oldMap.has(k))
if (oldMap.size > 0 && growth.length > 0) {
  console.error(`GROWTH DETECTED: ${growth.length} t() call(s) not in the existing baseline.`)
  for (const k of growth) console.error(`  ${k}`)
  console.error('Only pure shrink is allowed. Resolve the string where it is read.')
  process.exit(1)
}

violations.sort((a, b) =>
  a.file !== b.file
    ? a.file.localeCompare(b.file)
    : a.line - b.line || a.context.localeCompare(b.context),
)

const content = JSON.stringify(
  {
    '//': [
      'DO NOT EDIT MANUALLY. Regenerate with `npm run baseline:i18n-timing-update`.',
      '',
      'Every t() call that resolves before render: at module scope, in a component',
      'body, in a class field, or in a function the module invokes at its own top',
      'level. Such a call keeps the language the window started in. The baseline may',
      'only shrink; new code must resolve its strings where they are read.',
    ],
    violations,
  },
  null,
  2,
)

writeFileSync(BASELINE_PATH, content + '\n')

const shrunk = oldMap.size - violations.length
console.log(
  `Baseline written: ${violations.length} violation(s).${shrunk > 0 ? ` (shrunk by ${shrunk})` : ''}`,
)
