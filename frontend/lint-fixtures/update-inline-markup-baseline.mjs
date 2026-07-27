#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/inline-markup-baseline.json` from the current
 * violation set.
 *
 * Usage: npm run baseline:inline-update
 * Run from `frontend/`.
 *
 * This is a deliberate regeneration: the baseline must be explicitly
 * updated (run this command) rather than being a side effect of lint.
 *
 * Growth guard: refuses to write a baseline with new violations not present
 * in the existing baseline. Only pure shrink or no-change is allowed. This
 * prevents silently legitimizing new debt through regeneration.
 */
import { execSync } from 'node:child_process'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { resolve } from 'node:path'

const SCRIPT_DIR = import.meta.dirname // frontend/lint-fixtures/
const FRONTEND_DIR = resolve(SCRIPT_DIR, '..') // frontend/
const PROJECT_ROOT = resolve(FRONTEND_DIR, '..') // workspace root
const BASELINE_PATH = resolve(SCRIPT_DIR, 'inline-markup-baseline.json')

/** Compute the same hash the rule uses. */
function hashText(text) {
  return createHash('sha256').update(text).digest('hex').slice(0, 12)
}

/**
 * Extract the source text of a node at the given line/column range.
 * ESLint JSON uses 1-based lines, 1-based columns where 1=first character.
 * endColumn is exclusive (one past the last character).
 */
function extractSource(filePath, line, col, endLine, endCol) {
  try {
    const content = readFileSync(filePath, 'utf-8')
    const lines = content.split('\n')
    if (line === endLine) {
      return lines[line - 1].slice(col - 1, endCol - 1)
    }
    let result = lines[line - 1].slice(col - 1) + '\n'
    for (let i = line; i < endLine - 1; i++) {
      result += lines[i] + '\n'
    }
    result += lines[endLine - 1].slice(0, endCol - 1)
    return result
  } catch {
    return ''
  }
}

/** Classify a violation message into a short type label. */
function classifyType(message) {
  const m = message ?? ''
  if (m.includes('duplicating')) return 'bypassComponent'
  if (m.includes('style')) return 'inlineStyle'
  return 'unknown'
}

// ─── Load existing baseline ──────────────────────────────────────────────────────────
const oldBaselineMap = new Map()
if (existsSync(BASELINE_PATH)) {
  try {
    const raw = JSON.parse(readFileSync(BASELINE_PATH, 'utf-8'))
    for (const v of raw.violations || []) {
      oldBaselineMap.set(`${v.file}:${v.id}`, v)
    }
  } catch {
    // Ignore corrupt baseline
  }
}

// ─── Collect current violations ──────────────────────────────────────────────────────
console.log('Running eslint to collect inline-markup violations...')
const output = execSync('npx eslint . --format json 2>/dev/null || true', {
  cwd: FRONTEND_DIR,
  encoding: 'utf-8',
  env: { ...process.env, NOCX_BASELINE_UPDATE: '1' },
})

const results = JSON.parse(output)
const newViolations = []

for (const fileResult of results) {
  if (!fileResult.messages) continue
  for (const msg of fileResult.messages) {
    if (msg.ruleId !== 'nocx/no-inline-markup') continue
    if (!msg.endLine) continue

    const filePath = fileResult.filePath
    const rel = filePath.replace(PROJECT_ROOT, '')
    const text = extractSource(filePath, msg.line, msg.column, msg.endLine, msg.endColumn)
    const id = hashText(text)
    const type = classifyType(msg.message)

    newViolations.push({
      file: rel.startsWith('/') ? rel.slice(1) : rel,
      id,
      type,
      line: msg.line,
    })
  }
}

// ─── Growth guard: every new violation must match an old baseline entry ──────────────
// Pure shrink (old entries with no new match) is allowed. Growth is not.
const oldCount = oldBaselineMap.size
const newKeys = new Set(newViolations.map((v) => `${v.file}:${v.id}`))
const growth = []

for (const v of newViolations) {
  const key = `${v.file}:${v.id}`
  if (!oldBaselineMap.has(key)) {
    growth.push(v)
  }
}

if (growth.length > 0 && oldCount > 0) {
  console.error(`GROWTH DETECTED: ${growth.length} new violation(s) not in the existing baseline.`)
  console.error('Only pure shrink or no-change is allowed.')
  console.error('')
  console.error('New violations:')
  for (const v of growth) {
    console.error(`  ${v.file}:${v.line} — ${v.type}`)
  }
  console.error('')
  console.error(
    'If these are intentional, first fix the violations or explicitly accept them',
    'by editing the baseline file. Automated regeneration cannot grow the baseline.',
  )
  process.exit(1)
}

// ─── Write baseline ──────────────────────────────────────────────────────────────────
newViolations.sort((a, b) => {
  if (a.file !== b.file) return a.file.localeCompare(b.file)
  return a.line - b.line
})

const content = JSON.stringify(
  {
    '//': [
      'DO NOT EDIT MANUALLY. Regenerate with `npm run baseline:inline-update`.',
      '',
      'This baseline enumerates every current inline-markup violation (bypassed',
      'component classes and inline style props) in application surfaces below',
      'the project root. It may only shrink. A new violation in any file is an',
      'error even if the file already has baselined violations. CI asserts this.',
      '',
      'This file must be empty before epic nocx-vxqj closes.',
    ],
    violations: newViolations,
  },
  null,
  2,
)

writeFileSync(BASELINE_PATH, content + '\n')

const shrunk = oldCount - newViolations.length
const change = shrunk > 0 ? ` (shrunk by ${shrunk})` : ''
console.log(
  `Baseline written: ${newViolations.length} violations across ${
    new Set(newViolations.map((v) => v.file)).size
  } files.${change}`,
)
