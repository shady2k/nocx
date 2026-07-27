#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/raw-controls-baseline.json` from the current
 * violation set.
 *
 * Usage: npm run baseline:update
 * Run from `frontend/`.
 *
 * This is a deliberate regeneration: the baseline must be explicitly
 * updated (run this command) rather than being a side effect of lint.
 * The file comment states the epic-close gate.
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
const BASELINE_PATH = resolve(SCRIPT_DIR, 'raw-controls-baseline.json')

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
  const lines = readFileSync(filePath, 'utf-8').split('\n')
  if (line === endLine) {
    return lines[line - 1].slice(col - 1, endCol - 1)
  }
  let text = lines[line - 1].slice(col - 1) + '\n'
  for (let i = line; i < endLine - 1; i++) {
    text += lines[i] + '\n'
  }
  text += lines[endLine - 1].slice(0, endCol - 1)
  return text
}

/** Classify a violation message into a short control name. */
function classifyControl(message) {
  if (message.includes('<button>')) return 'button'
  if (message.includes('<select>')) return 'select'
  if (message.includes('<textarea>')) return 'textarea'
  const inputMatch = message.match(/<input type="(\w+)">/)
  if (inputMatch) return `input/${inputMatch[1]}`
  if (message.includes('innerHTML')) return 'innerHTML'
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
    // If the file is broken, treat as empty (will rebuild from scratch)
    console.warn('Warning: existing baseline is invalid, rebuilding from scratch.')
  }
}

// ─── Collect current violations ──────────────────────────────────────────────────────
console.log('Running eslint to collect violations...')
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
    if (msg.ruleId !== 'nocx/no-raw-controls') continue
    if (!msg.endLine) continue

    const rel = fileResult.filePath.replace(PROJECT_ROOT, '')
    const text = extractSource(
      fileResult.filePath,
      msg.line,
      msg.column,
      msg.endLine,
      msg.endColumn,
    )
    const id = hashText(text)
    const control = classifyControl(msg.message)

    newViolations.push({
      file: rel.startsWith('/') ? rel.slice(1) : rel,
      id,
      control,
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

if (growth.length > 0) {
  console.error(`GROWTH DETECTED: ${growth.length} new violation(s) not in the existing baseline.`)
  console.error('Only pure shrink or no-change is allowed.')
  console.error('')
  console.error('New violations:')
  for (const v of growth) {
    console.error(`  ${v.file}:${v.line} — ${v.control}`)
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
      'DO NOT EDIT MANUALLY. Regenerate with `npm run baseline:update`.',
      '',
      'This baseline enumerates every current raw-control violation in application',
      'surfaces. It may only shrink. A new violation in any file is an error even if',
      'the file already has baselined violations. CI asserts this.',
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
