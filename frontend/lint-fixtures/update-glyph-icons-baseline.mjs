#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/glyph-icons-baseline.json` from the current tree.
 *
 * Usage: npm run baseline:glyph-icons-update   (from frontend/)
 *
 * Refuses to write a baseline that grows — a key with more occurrences than the old
 * file allowed, or a key it did not list. Shrink and no-change are the only
 * directions; reasons are copied forward by key.
 */
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanTree, violationKey } from './check-glyph-icons.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'glyph-icons-baseline.json')

const hits = scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR).filter(
  (h) => h.context !== 'PARSE',
)
const old = new Map()
if (existsSync(BASELINE_PATH)) {
  for (const v of JSON.parse(readFileSync(BASELINE_PATH, 'utf8')).violations)
    old.set(violationKey(v), v)
}

const counts = new Map()
for (const h of hits) {
  const key = violationKey(h)
  const entry = counts.get(key) ?? { file: h.file, glyph: h.glyph, context: h.context, count: 0 }
  entry.count += 1
  counts.set(key, entry)
}

const growth = [...counts.entries()].filter(([k, v]) => v.count > (old.get(k)?.count ?? 0))
if (growth.length > 0) {
  console.error('Refusing to write: the tree has glyph icons the baseline does not allow:')
  for (const [, v] of growth)
    console.error(`  NEW: ${v.file} "${v.glyph}" as ${v.context} ×${v.count}`)
  process.exit(1)
}

const violations = [...counts.entries()]
  .map(([k, v]) => ({ ...v, reason: old.get(k)?.reason ?? '' }))
  .sort((a, b) => (a.file === b.file ? (a.glyph < b.glyph ? -1 : 1) : a.file < b.file ? -1 : 1))

writeFileSync(
  BASELINE_PATH,
  JSON.stringify(
    {
      '//': [
        'DO NOT EDIT MANUALLY except to write a reason. Regenerate with `npm run baseline:glyph-icons-update`.',
        '',
        'Every entry is a character standing in for an icon as element text (nocx-9bpeq.5).',
        'The baseline may only shrink. Its normal state is empty.',
      ],
      violations,
    },
    null,
    2,
  ) + '\n',
)
console.log(`Baseline written: ${violations.length} entries.`)
