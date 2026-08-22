#!/usr/bin/env node
/**
 * Menu-icons checker — a context-menu item is built with its mark, or not at
 * all.
 *
 * The kit's ContextMenu (`src/ui/context-menu.tsx`) takes `icon?: Component`
 * and RESERVES the icon column whether or not one is passed, deliberately, so
 * a menu may mix marked and unmarked rows without the labels stepping in and
 * out. That is the right mechanism and this rule does not touch it: the kit
 * cannot know that "Copy Absolute Path" wants a copy glyph, so there is no
 * global fix — only a global gate.
 *
 * The gate is this file. Three of the four call sites shipped with no marks
 * at all and nothing said so, which is the only reason it reached the owner's
 * screen (nocx-inbw1). A rule that fails a bare item literal is what stops the
 * NEXT call site omitting one silently.
 *
 * The shape it looks for: an object literal carrying `id`, `label` and
 * `onSelect` — the ContextMenuItem signature, and the WorkspaceMenuRow one,
 * which is the same row seen from the module that builds it. All three
 * together, because any two of them describe half the option objects in the
 * codebase; a rule that reported those would be turned off, which is the same
 * outcome as not having it.
 *
 * Scope, deliberately:
 *
 *   - `icon: undefined` and `icon: null` FAIL. The key alone is not the mark:
 *     both reserve the column and draw nothing, so a rule satisfied by the
 *     key's presence would be satisfied by exactly the state it exists to
 *     forbid.
 *   - A row assembled from a spread (`{ ...base, label }`) is not decided
 *     here. The checker cannot see what the spread carries, so it reports
 *     nothing rather than guessing; documented, not chased.
 *   - `*.test.ts` / `*.test.tsx` are not scanned. The kit's own tests build
 *     deliberately bare items to assert the reserved column, and that is what
 *     the column is for.
 *
 * Policy: existing omissions are baselined; an omission the baseline does not
 * list is new and fails the pre-commit hook. The baseline may only shrink, and
 * every entry carries the one line that says why the omission is deliberate —
 * an unexplained bare row is the defect, whether it is old or new.
 * Regenerate with `node lint-fixtures/update-menu-icons-baseline.mjs`, which
 * refuses to write a baseline that grows and copies each entry's reason
 * forward. `NOCX_BASELINE_UPDATE=1` prints every violation without failing.
 *
 * Invocation (from frontend/):
 *
 *   node lint-fixtures/check-menu-icons.mjs                # scan src/, baseline applied
 *   node lint-fixtures/check-menu-icons.mjs <file...>      # scan exactly these files, NO baseline
 *
 * The single-file form is the fixture / evidence mode: run it against
 * lint-fixtures/menu-icons-fixture/menu.tsx, or against a historical file, to
 * prove the rule fires on real code rather than on a sample.
 */
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'menu-icons-baseline.json')

/** The three properties that together identify a menu row. */
const ROW_SHAPE = ['id', 'label', 'onSelect']

/** The property name of a non-computed object member, or null. */
function propertyName(node) {
  if (node.type !== 'Property' || node.computed) return null
  if (node.key.type === 'Identifier') return node.key.name
  if (node.key.type === 'Literal' && typeof node.key.value === 'string') return node.key.value
  return null
}

/** Is this value an absence written out — `undefined` or `null`? Both reserve
 *  the icon column and draw nothing, so neither is a mark. */
function isAbsence(node) {
  if (!node) return true
  if (node.type === 'Identifier' && node.name === 'undefined') return true
  return node.type === 'Literal' && node.value === null
}

/** The string value of a property whose value is a plain string literal —
 *  used for `id`/`label`, which is what makes a violation readable and its
 *  baseline key stable across line moves. */
function stringValue(node) {
  if (node && node.type === 'Literal' && typeof node.value === 'string') return node.value
  return ''
}

/** Depth-first walk of an ESTree/TS-ESTree node, visiting every node. */
function walk(node, visit) {
  if (!node || typeof node !== 'object') return
  visit(node)
  for (const key of Object.keys(node)) {
    if (key === 'parent') continue
    const child = node[key]
    if (Array.isArray(child)) {
      for (const c of child) {
        if (c && typeof c === 'object' && typeof c.type === 'string') walk(c, visit)
      }
    } else if (child && typeof child === 'object' && typeof child.type === 'string') {
      walk(child, visit)
    }
  }
}

/**
 * Scan one source text for unmarked menu rows.
 *
 * @param {string} file — path to report on the violation (baseline key prefix)
 * @param {string} source — file contents
 * @returns {Array<{file:string,line:number,column:number,id:string,label:string,reason:string}>}
 *   A file that fails to parse yields a single `reason: 'PARSE'` violation —
 *   the checker fails closed, never silently skips.
 */
export function scanSource(file, source) {
  let ast
  try {
    ast = parse(source, {
      ecmaVersion: 'latest',
      sourceType: 'module',
      // JSX only for .tsx. A `.ts` file's `<T,>(...)` type parameters parse
      // as an unclosed JSX element when the flag is on, and the checker fails
      // closed on a parse error — so leaving it on would have turned one
      // test-support module into a permanent PARSE violation while the rule
      // itself was working perfectly. The extension is what TypeScript itself
      // uses to decide, and this file decides the same way.
      ecmaFeatures: { jsx: file.endsWith('.tsx') },
      loc: true,
      range: true,
    })
  } catch (err) {
    const first = String(err.message).split('\n')[0]
    return [{ file, line: 0, column: 0, id: 'PARSE', label: first, reason: 'PARSE' }]
  }

  const violations = []
  walk(ast, (node) => {
    if (node.type !== 'ObjectExpression') return
    const props = new Map()
    for (const p of node.properties) {
      const name = propertyName(p)
      if (name !== null) props.set(name, p.value)
    }
    if (!ROW_SHAPE.every((name) => props.has(name))) return
    if (props.has('icon') && !isAbsence(props.get('icon'))) return
    violations.push({
      file,
      line: node.loc.start.line,
      column: node.loc.start.column + 1,
      id: stringValue(props.get('id')),
      label: stringValue(props.get('label')),
      reason: props.has('icon') ? 'icon is undefined' : 'no icon',
    })
  })
  return violations
}

/**
 * Scan every application .ts/.tsx file under `dir` (skipping node_modules,
 * dist, generated and *.test.* — the kit's own tests build bare rows on
 * purpose, to assert the column stays reserved). Paths are reported relative
 * to `base` so baseline keys are stable.
 */
export function scanTree(dir, base) {
  const violations = []
  const walkDir = (d) => {
    for (const entry of readdirSync(d, { withFileTypes: true })) {
      const full = join(d, entry.name)
      if (entry.isDirectory()) {
        if (entry.name === 'node_modules' || entry.name === 'dist' || entry.name === 'generated') {
          continue
        }
        walkDir(full)
      } else if (
        (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) &&
        !entry.name.endsWith('.test.ts') &&
        !entry.name.endsWith('.test.tsx') &&
        !entry.name.endsWith('.d.ts')
      ) {
        const file = relative(base, full)
        violations.push(...scanSource(file, readFileSync(full, 'utf8')))
      }
    }
  }
  walkDir(dir)
  return violations
}

/** Stable key that lets a baseline entry survive line moves. */
export function violationKey(v) {
  return `${v.file}:${v.id}`
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
  const fileArgs = process.argv.slice(2).filter((a) => a.endsWith('.tsx') || a.endsWith('.ts'))

  let violations
  let baseline
  if (fileArgs.length > 0) {
    // Fixture / evidence mode: scan exactly the named files, never the tree,
    // and never the baseline — the point is to see what the rule finds.
    violations = fileArgs.flatMap((file) => scanSource(file, readFileSync(file, 'utf8')))
    baseline = new Map()
  } else {
    violations = scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)
    baseline = updateMode ? new Map() : loadBaseline()
  }

  const unbaselined = violations.filter((v) => !baseline.has(violationKey(v)))

  for (const v of violations) {
    if (v.reason === 'PARSE') {
      console.error(`  PARSE ERROR: ${v.file}: ${v.label}`)
    } else {
      console.log(`${v.file}:${v.line}: ${v.id} ("${v.label}") — ${v.reason}`)
    }
  }

  if (unbaselined.length > 0) {
    console.error(`Menu-icon violations: ${violations.length} total, ${unbaselined.length} new.`)
    for (const v of unbaselined) {
      if (v.reason === 'PARSE') continue
      console.error(`  NEW: ${v.file}:${v.line} ${v.id} ("${v.label}") — ${v.reason}`)
    }
    console.error(
      'A context-menu row with no mark leaves the reserved icon column empty, and a menu of',
      'empty columns is read word by word every time. Pass an icon from src/ui/icons — or, if',
      'the omission is genuinely deliberate, record why in the baseline:',
      '`npm run baseline:menu-icons-update` (it refuses to grow; every entry keeps a reason).',
    )
    process.exitCode = 1
  } else if (violations.length > 0) {
    console.error(`Menu-icon violations: ${violations.length} (all baselined).`)
  }
}
