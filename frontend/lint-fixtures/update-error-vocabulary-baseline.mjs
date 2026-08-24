#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/error-vocabulary-baseline.json` from the current
 * tree.
 *
 * The updater shares the checker's scan: same tree, same identity set, so
 * what gets written is exactly what the gate sees. Reasons survive
 * regeneration: each entry's `reason` is copied forward by key, so a
 * hand-written justification is never lost to a re-run. A reason only
 * disappears when its entry shrinks away.
 *
 * Growth guard, mirroring the row-grammar updater: refuses to write a
 * baseline that contains a violation absent from the existing one — the
 * fix for a new error vocabulary is deleting the class, not legitimizing
 * it. Only pure shrink or no-change is allowed.
 */
import { readFileSync, writeFileSync, existsSync, readdirSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanCss, kitIdentitySet, violationKey } from './check-error-vocabulary.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'error-vocabulary-baseline.json')

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
    'Refusing to write: the tree contains error-vocabulary violations the baseline does not list:',
  )
  for (const v of growth) console.error(`  NEW: ${v.file}:${v.line} ${v.selector}`)
  console.error(
    'Render the message through the kit (ui-field-error, Toast, StatusCard) instead.',
    'Regeneration is for reasons and shrink, not for legitimizing a new vocabulary.',
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
      'DO NOT EDIT MANUALLY. Regenerate with `node lint-fixtures/update-error-vocabulary-baseline.mjs`.',
      '',
      'Every entry is a surface CSS class that names an error/refusal concept',
      'AND paints a danger token (--color-danger/--color-error) — the private',
      'error vocabulary nocx-8sudy removed and this gate exists to prevent',
      'from reappearing. The baseline may only shrink; a violation it does not',
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
