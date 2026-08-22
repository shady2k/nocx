#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/menu-icons-baseline.json` from the current tree.
 *
 * Usage: npm run baseline:menu-icons-update   (from frontend/)
 *
 * Deliberate regeneration, never a side effect of lint. Growth guard,
 * mirroring update-two-owners-baseline.mjs: refuses to write a baseline that
 * contains a violation absent from the existing one. Only pure shrink or
 * no-change is allowed, so regeneration cannot silently legitimize a menu row
 * that ships with an empty icon column. Removing entries (the fix — pass an
 * icon) is the one direction that never fails.
 *
 * The baseline is EMPTY as of nocx-inbw1, and that is its normal state: every
 * row in the product carries a mark. An entry here is a documented exception,
 * so each one keeps a `reason` and reasons survive regeneration — they are
 * copied forward by key, and only disappear when their entry shrinks away.
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanTree, violationKey } from './check-menu-icons.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'menu-icons-baseline.json')

// The updater shares the checker's scan: same tree, same rule.
const violations = scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)

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
  console.error('Refusing to write: the tree contains menu rows the baseline does not list:')
  for (const v of growth) console.error(`  NEW: ${v.file}:${v.line} ${v.id} — ${v.reason}`)
  console.error(
    'Pass an icon from src/ui/icons instead. Regeneration is for reasons and shrink,',
    'not for legitimizing a row that ships with an empty icon column.',
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
      'DO NOT EDIT MANUALLY. Regenerate with `npm run baseline:menu-icons-update`.',
      '',
      'Every entry is a context-menu row literal (id + label + onSelect) that',
      'ships with no icon, so the column the kit reserves for it stays empty',
      '(nocx-inbw1). The baseline may only shrink; a row it does not list is',
      'new and fails the lint gate.',
      '',
      'It is EMPTY, and that is the normal state: the kit cannot know which',
      'glyph a label wants, so every call site chooses one. An entry here is a',
      'deliberate exception and carries the one line that says why it is',
      'acceptable to keep. When that reason stops holding, delete the entry',
      '(or regenerate — the updater copies reasons forward by key and refuses',
      'to grow).',
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
  `Baseline written: ${byKey.size} unmarked menu rows across ${new Set(entries.map((e) => e.file)).size} files.${change}`,
)
