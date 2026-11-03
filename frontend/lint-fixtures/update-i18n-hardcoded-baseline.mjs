#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/i18n-hardcoded-baseline.json` from the current
 * violation set (nocx-dej6 stage 3's ratchet).
 *
 * Usage: npm run baseline:i18n-hardcoded-update
 * Run from `frontend/`.
 *
 * Deliberate regeneration, like every baseline here: growth is refused —
 * a stage that translated its strings must SHRINK the baseline, and a new
 * hardcoded literal is a violation to fix, not a baseline entry to mint.
 * Copy already inside t()/tPlural() is never seen.
 *
 * The violation objects come straight from the checker's AST scan
 * (update-glyph-icons-baseline.mjs is the template): importing scanTree
 * keeps the baseline's keys bit-identical to what the checker computes —
 * a printed, truncated mirror of them would drift.
 *
 * The allowlist (i18n-hardcoded-allowed.json) is NOT touched: it is the
 * reviewed register of permanently-English strings (example values,
 * technical terms) and is maintained by hand.
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { scanTree, violationKey } from './check-i18n-hardcoded.mjs'

const SCRIPT_DIR = import.meta.dirname
const FRONTEND_DIR = resolve(SCRIPT_DIR, '..')
const BASELINE_PATH = resolve(SCRIPT_DIR, 'i18n-hardcoded-baseline.json')
const ALLOWED_PATH = resolve(SCRIPT_DIR, 'i18n-hardcoded-allowed.json')

// ─── Collect current violations from the AST, not the printer ───────────
const violations = scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)

const PARSE = violations.filter((v) => v.context === 'PARSE')
if (PARSE.length > 0) {
  console.error('PARSE ERRORS present — fix the files before regenerating:')
  for (const v of PARSE) console.error(`  ${v.file}: ${v.snippet}`)
  process.exit(1)
}

const allowedKeys = new Set(
  (() => {
    if (!existsSync(ALLOWED_PATH)) return []
    try {
      return JSON.parse(readFileSync(ALLOWED_PATH, 'utf8')).violations ?? []
    } catch {
      return []
    }
  })().map(violationKey),
)
const enumerated = violations.filter((v) => !allowedKeys.has(violationKey(v)))

// ─── Growth guard: only pure shrink or no-change vs the old baseline ─────
const oldMap = new Map()
if (existsSync(BASELINE_PATH)) {
  try {
    const old = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
    for (const v of old.violations ?? []) oldMap.set(violationKey(v), v)
  } catch {
    console.warn('Warning: existing baseline is invalid, rebuilding from scratch.')
  }
}

const newKeys = new Set(enumerated.map(violationKey))
const growth = [...newKeys].filter((k) => !oldMap.has(k))
if (oldMap.size > 0 && growth.length > 0) {
  console.error(`GROWTH DETECTED: ${growth.length} violation(s) not in the existing baseline.`)
  for (const k of growth) console.error(`  ${k}`)
  console.error(
    'Only pure shrink is allowed. Wrap the literal in t(), or — for the reviewed',
    'permanently-English register — add it to i18n-hardcoded-allowed.json by hand',
    'with a reason, then rerun.',
  )
  process.exit(1)
}

// ─── Write ────────────────────────────────────────────────────────────────
enumerated.sort((a, b) =>
  a.file !== b.file ? a.file.localeCompare(b.file) : a.line - b.line || a.context.localeCompare(b.context),
)

const content = JSON.stringify(
  {
    '//': [
      'DO NOT EDIT MANUALLY. Regenerate with `npm run baseline:i18n-hardcoded-update`.',
      '',
      'This baseline enumerates every current hardcoded user-facing literal that',
      'bypasses t() in application surfaces. It may only shrink. A new violation',
      'in any file is an error even if the file already has baselined ones.',
      '',
      'This file must be empty before epic nocx-dej6 stage 3 closes. The reviewed',
      'permanently-English residue lives in i18n-hardcoded-allowed.json, never here.',
    ],
    violations: enumerated,
  },
  null,
  2,
)

writeFileSync(BASELINE_PATH, content + '\n')

const shrunk = oldMap.size - enumerated.length
const change = shrunk > 0 ? ` (shrunk by ${shrunk})` : ''
console.log(
  `Baseline written: ${enumerated.length} violations across ${
    new Set(enumerated.map((v) => v.file)).size
  } files.${change}`,
)
