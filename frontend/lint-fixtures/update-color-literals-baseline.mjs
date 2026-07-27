#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/color-literals-baseline.json` from the current
 * state of the tree.
 *
 * Runs both the CSS scanner (for .css files outside themes/) and the ESLint
 * rule (for JSX style={...} / SVG attributes in .tsx files).
 *
 * Invocation:
 *   node lint-fixtures/update-color-literals-baseline.mjs
 *
 * Uses NOCX_BASELINE_UPDATE=1 to bypass baseline matching in the ESLint rule
 * and in the CSS scanner, so every violation is emitted regardless of what
 * the current baseline contains.
 */

import { execSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { resolve, dirname, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url)) // frontend/lint-fixtures/
const FRONTEND_DIR = resolve(SCRIPT_DIR, '..') // frontend/
const PROJECT_ROOT = resolve(FRONTEND_DIR, '..')
const BASELINE_PATH = resolve(SCRIPT_DIR, 'color-literals-baseline.json')

/** Compute the same hash the rule uses. */
function hashText(text) {
  return createHash('sha256').update(text).digest('hex').slice(0, 12)
}

/** Classify a literal type into a short label. */
function classifyLiteral(type, literal) {
  if (type === 'hex') return 'hex'
  if (type === 'rgba()' || type === 'rgb()') return 'rgb'
  if (type === 'hsla()' || type === 'hsl()') return 'hsl'
  if (type === 'oklch') return 'oklch'
  if (type === 'lab') return 'lab'
  if (type === 'color()') return 'color'
  if (type === 'named') return literal
  return type
}
/**
 * Extract the source text of a node at the given line/column range.
 * ESLint JSON uses 1-based lines, 1-based columns where 1=first character.
 * endColumn is exclusive (one past the last character).
 */
function extractSource(filePath, line, col, endLine, endCol) {
  const text = readFileSync(filePath, 'utf8')
  const lines = text.split('\n')
  if (endLine === line) {
    // Single-line node: slice between col-1 and endCol-1
    return lines[line - 1].slice(col - 1, endCol - 1)
  }
  // Multi-line — join from start of first line to end of last
  let result = lines[line - 1].slice(col - 1) + '\n'
  for (let i = line; i < endLine - 1; i++) {
    result += lines[i] + '\n'
  }
  result += lines[endLine - 1].slice(0, endCol - 1)
  return result
}

// ─── 1. CSS scanner ────────────────────────────────────────────────────────

console.log('Running CSS colour scanner...')
const cssOutput = execSync(
  `NOCX_BASELINE_UPDATE=1 node ${resolve(SCRIPT_DIR, 'check-css-colors.mjs')} 2>/dev/null || true`,
  { cwd: FRONTEND_DIR, encoding: 'utf8', maxBuffer: 10 * 1024 * 1024 },
)

const cssViolations = cssOutput
  .trim()
  .split('\n')
  .filter(Boolean)
  .map((line) => {
    try {
      return JSON.parse(line)
    } catch {
      return null
    }
  })
  .filter(Boolean)

// ─── 2. ESLint rule violations ────────────────────────────────────────────

console.log('Running ESLint to collect JSX colour violations...')
const eslintOutput = execSync(
  'NOCX_BASELINE_UPDATE=1 npx eslint . --format json 2>/dev/null || true',
  { cwd: FRONTEND_DIR, encoding: 'utf8', maxBuffer: 10 * 1024 * 1024 },
)

let eslintViolations = []
try {
  const results = JSON.parse(eslintOutput)
  for (const fileResult of results) {
    if (!fileResult.messages) continue
    for (const msg of fileResult.messages) {
      if (msg.ruleId !== 'nocx/no-color-literals') continue
      if (!msg.endLine) continue

      const rel = fileResult.filePath.replace(PROJECT_ROOT, '').replace(/^\//, '')
      const text = extractSource(
        fileResult.filePath,
        msg.line,
        msg.column,
        msg.endLine,
        msg.endColumn,
      )
      const id = hashText(text)
      const type = classifyLiteral(msg.message.match(/"([^"]+)"/)?.[1] || 'unknown', '')

      eslintViolations.push({
        file: rel,
        id,
        type,
        line: msg.line,
      })
    }
  }
} catch {
  // No ESLint output or parse error
}

// ─── 3. Merge ──────────────────────────────────────────────────────────────
const newViolations = []

for (const v of cssViolations) {
  newViolations.push({
    file: v.file,
    id: v.id,
    type: classifyLiteral(v.type, v.literal),
    line: v.line,
  })
}

for (const v of eslintViolations) {
  newViolations.push({
    file: v.file,
    id: v.id,
    type: v.type,
    line: v.line,
  })
}

// ─── 4. Load existing baseline for growth guard ───────────────────────────
const oldBaselineMap = new Map()
if (existsSync(BASELINE_PATH)) {
  const old = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
  for (const v of old.violations) {
    oldBaselineMap.set(`${v.file}:${v.id}`, v)
  }
}

// ─── 5. Growth guard ─────────────────────────────────────────────────────
const oldCount = oldBaselineMap.size
const newKeys = new Set(newViolations.map((v) => `${v.file}:${v.id}`))
const growth = []

for (const v of newViolations) {
  if (!oldBaselineMap.has(`${v.file}:${v.id}`)) {
    growth.push(`${v.file}:${v.id} (${v.type}, line ${v.line})`)
  }
}

if (growth.length > 0 && oldCount > 0) {
  console.error(`GROWTH DETECTED — ${growth.length} new violation(s) not in existing baseline:`)
  for (const g of growth) {
    console.error(`  ${g}`)
  }
  console.error('')
  console.error('If this is intentional, delete the old baseline and re-run.')
  // Still write the baseline but warn
}

// ─── 6. Write baseline ──────────────────────────────────────────────────
newViolations.sort((a, b) => {
  if (a.file !== b.file) return a.file.localeCompare(b.file)
  return a.line - b.line
})

const content = JSON.stringify(
  {
    '//': [
      'DO NOT EDIT MANUALLY. Regenerate with `npm run baseline:color-update`.',
      '',
      'This baseline enumerates every current colour-literal violation outside',
      'frontend/src/styles/themes/. It may only shrink. A new violation in any',
      'file is an error even if the file already has baselined violations.',
      '',
      'This file must be empty before the colour grammar epic (nocx-xrrl) closes.',
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
