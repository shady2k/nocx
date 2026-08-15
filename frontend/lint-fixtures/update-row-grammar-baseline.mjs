#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/row-grammar-baseline.json` from the current tree.
 *
 * Usage: npm run baseline:row-grammar-update   (from frontend/)
 *
 * Deliberate regeneration, never a side effect of lint. Growth guard,
 * mirroring update-two-owners-baseline.mjs: refuses to write a baseline
 * that contains a violation absent from the existing one. Only pure shrink
 * or no-change is allowed, so regeneration cannot silently legitimize a new
 * surface row grammar. Removing entries (the fix for a dialect) is the one
 * direction that never fails.
 *
 * Reasons survive regeneration: each entry's `reason` is copied forward by
 * key, so a hand-written justification is never lost to a re-run. A reason
 * only disappears when its entry shrinks away.
 */
import { readFileSync, writeFileSync, existsSync, readdirSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanCss, kitIdentitySet, violationKey } from './check-row-grammar.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'row-grammar-baseline.json')

/** Every .css file under a directory, recursively. */
function allCSSUnder(dir) {
  const out = []
  if (!existsSync(dir)) return out
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...allCSSUnder(full))
    else if (entry.isFile() && entry.name.endsWith('.css')) out.push(full)
  }
  return out
}

// The updater shares the checker's scan: same tree, same identity set.
const identities = kitIdentitySet()
const violations = allCSSUnder(resolve(FRONTEND_DIR, 'src/styles')).flatMap((f) =>
  scanCss(relative(FRONTEND_DIR, f), readFileSync(f, 'utf8'), identities),
)

// ─── Load existing baseline ────────────────────────────────────────────────
const oldBaseline = new Map()
if (existsSync(BASELINE_PATH)) {
  const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
  for (const v of data.violations) {
    oldBaseline.set(violationKey(v), v)
  }
}

// ─── Growth guard: every new violation must match an old baseline entry ────
const growth = violations.filter((v) => !oldBaseline.has(violationKey(v)))
if (growth.length > 0) {
  console.error(
    'Refusing to write: the tree contains row-grammar violations the baseline does not list:',
  )
  for (const v of growth) console.error(`  NEW: ${v.file}:${v.line} ${v.selector}`)
  console.error(
    'Render the row through RecordRow instead. Regeneration is for reasons and shrink,',
    'not for legitimizing a new dialect.',
  )
  process.exit(1)
}

// ─── Write baseline (one entry per key; reasons copied from the old file) ──
const byKey = new Map()
for (const v of violations) {
  byKey.set(violationKey(v), {
    ...v,
    reason: oldBaseline.get(violationKey(v))?.reason ?? '',
  })
}
const entries = [...byKey.values()].sort((a, b) =>
  a.file === b.file ? a.line - b.line : a.file < b.file ? -1 : 1,
)

const content = JSON.stringify(
  {
    '//': [
      'DO NOT EDIT MANUALLY. Regenerate with `npm run baseline:row-grammar-update`.',
      '',
      'Every entry is a surface CSS class naming a composite-owned concept',
      '(name/meta) under an item/row row prefix — the dialect RecordRow owns',
      '(nocx-pp3y.3). The baseline may only shrink; a violation it does not',
      'list is new and fails the lint gate.',
      '',
      'Each entry carries the one-line reason that makes it acceptable to keep.',
      'When the reason stops holding, delete the entry (or regenerate — the',
      'updater copies reasons forward by key and refuses to grow).',
    ],
    violations: entries,
  },
  null,
  2,
)

writeFileSync(BASELINE_PATH, content + '\n')

const shrunk = oldBaseline.size - byKey.size
const change = shrunk > 0 ? ` (shrunk by ${shrunk})` : ''
console.log(
  `Baseline written: ${byKey.size} violations across ${new Set(entries.map((e) => e.file)).size} files.${change}`,
)
