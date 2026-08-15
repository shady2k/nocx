#!/usr/bin/env node
/**
 * Row-grammar checker — the kit owns "describe a record in a row".
 *
 * RecordRow (nocx-pp3y.3) owns the composite: a title, at most one kind
 * badge, meta text, and a status as the kit's dot + text. Before it
 * existed, CollectionRow's `info` slot was free-form, so two lists
 * described one concept in two grammars — `cm-item-name`/`cm-item-meta`
 * and `ep-item-name`/`ep-item-meta` — and each surface wrote CSS for the
 * grammar it invented. Nothing repainted anything, so neither integrity
 * gate could see it: the divergence was at composition, one level up.
 *
 * This rule is the gate for that level. A CSS class whose name encodes a
 * composite-owned concept (`name`, `meta`) under an `item`/`row` row
 * prefix is a surface declaring its own row grammar — the next dialect —
 * and fails. The composite's own classes (`ui-record-row__meta`) are kit
 * identities derived from `src/ui/`, and the rule lets them alone.
 *
 * The one family that predates the composite — the Git panel's dense
 * `git-log-row__meta`, whose meta line carries several ref badges and is
 * not the composite's single-meta shape — is grandfathered in the
 * baseline. The rule proves no NEW family appears.
 *
 * Invocation:
 *   node lint-fixtures/check-row-grammar.mjs
 *   node lint-fixtures/check-row-grammar.mjs --dir=<fixture dir>
 *
 * `--dir` scans exactly that directory (fixture mode): no baseline, so
 * every intentional violation is reported — the negative-fixture gate
 * asserts them by name.
 */
import { createRequire } from 'node:module'
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanKitIdentities } from './scan-kit-identities.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'row-grammar-baseline.json')
const css = createRequire(import.meta.url)('css-tree')

/** A class segment naming a composite-owned concept (name, meta) under an
 *  `item`/`row` row prefix — the dialect's two families, however they are
 *  joined. The composite's own parts (ui-record-row__meta) match this
 *  pattern too and are let through by the kit-identity set. */
const FAMILY = /-(item|row)(?:__|-|_)(name|meta)(?:[_-]|$)/

/** Scan one CSS source for row-grammar violations. `kitIdentities` is the
 *  derived kit identity set (scanKitIdentities' byClass keys); a class the
 *  kit owns is the composite's grammar, not a surface dialect. */
export function scanCss(file, source, kitIdentities = new Set()) {
  let ast
  try {
    ast = css.parse(source, { positions: true })
  } catch {
    // Fail closed: unparseable CSS is itself a defect, and a scanner that
    // silently skips a file would quietly stop guarding it.
    return [{ file, selector: '<unparseable>', line: 0 }]
  }
  const violations = []
  css.walk(ast, (node) => {
    if (node.type !== 'ClassSelector') return
    if (!FAMILY.test(node.name)) return
    if (kitIdentities.has(node.name)) return
    violations.push({ file, selector: `.${node.name}`, line: node.loc?.start.line ?? 0 })
  })
  return violations
}

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

/** The kit's identity set, derived from the components' own JSX — never
 *  from the `ui-` prefix (the prefix is not the test, nocx-hav2). */
export function kitIdentitySet() {
  const { byClass } = scanKitIdentities(resolve(FRONTEND_DIR, 'src/ui'))
  return new Set(byClass.keys())
}

/** Stable key that lets a baseline entry survive line moves. */
export function violationKey(v) {
  return `${v.file}:${v.selector}`
}

function loadBaseline() {
  const map = new Map()
  try {
    const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
    for (const v of data.violations) {
      map.set(violationKey(v), v)
    }
  } catch {
    // No baseline — every violation is an error
  }
  return map
}

// ─── CLI entry point ──────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const updateMode = process.env.NOCX_BASELINE_UPDATE === '1'
  const dirArg = process.argv.slice(2).find((a) => a.startsWith('--dir='))
  const kitIdentities = kitIdentitySet()

  let violations
  let baseline
  if (dirArg) {
    // Fixture mode: scan exactly the named directory, never the baseline —
    // the point is to see what the rule finds.
    const dir = resolve(dirArg.slice('--dir='.length))
    violations = allCSSUnder(dir).flatMap((f) =>
      scanCss(relative(FRONTEND_DIR, f), readFileSync(f, 'utf8'), kitIdentities),
    )
    baseline = new Map()
  } else {
    violations = allCSSUnder(resolve(FRONTEND_DIR, 'src/styles')).flatMap((f) =>
      scanCss(relative(FRONTEND_DIR, f), readFileSync(f, 'utf8'), kitIdentities),
    )
    baseline = updateMode ? new Map() : loadBaseline()
  }

  const unbaselined = violations.filter((v) => !baseline.has(violationKey(v)))

  for (const v of violations) {
    console.log(`${v.file}:${v.line}: ${v.selector}`)
  }

  if (unbaselined.length > 0) {
    console.error(
      `Row-grammar violations: ${violations.length} total, ${unbaselined.length} un-baselined.`,
    )
    for (const v of unbaselined) {
      console.error(`  NEW: ${v.file}:${v.line} ${v.selector}`)
    }
    console.error(
      'A surface declaring its own name/meta row grammar is the second dialect this',
      'composite exists to make impossible. Render the row through RecordRow instead —',
      "or, if a pre-existing family is genuinely not the composite's shape, record why",
      'in the baseline: `npm run baseline:row-grammar-update` (it refuses to grow).',
    )
    process.exitCode = 1
  } else if (violations.length > 0) {
    console.error(`Row-grammar violations: ${violations.length} (all baselined).`)
  }
}
