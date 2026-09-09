#!/usr/bin/env node
/**
 * Two-owners checker — a value-bearing JSX prop may not invent its own default.
 *
 * The rule: a JSX attribute in the value-bearing set (`value`, `checked`,
 * `selected`, `defaultValue`) whose expression is `lhs || literal` or
 * `lhs ?? literal` is a default invented at the render site. By construction
 * no validator or model can see it, so the surface and the rules disagree in
 * exactly the states nobody tested (nocx-a88r: the port input painted 22
 * while the validator judged an empty draft, so every alias-adopted
 * connection was born unable to be edited).
 *
 * Scope, deliberately:
 *
 *   - Only `||` / `??` whose right side is a literal (string, number, boolean,
 *     or a template literal with no substitutions). A computed right side
 *     (`a || b`, `a || fallback()`) is not a default the checker can prove
 *     invisible. The absence-preserving forms are excluded: `?? undefined`
 *     invents nothing, and `?? null` / `?? ''` narrow absent to absent, so
 *     the render site and a validator reading the raw value agree. `|| ''` is
 *     NOT excluded: `||` is falsy-triggered, so `0 || ''` paints empty where
 *     the raw value is 0 — the same shape as `|| 22`. The two operators are
 *     treated differently on purpose; that distinction is the rule. `|| -1`
 *     is a unary expression, not a literal, and is out of scope for the same
 *     reason.
 *   - Only the top level of the attribute expression. A fallback nested inside
 *     a conditional (`x ? y : (a ?? '')`) is presentation logic of a different
 *     shape; chasing it is how a checker starts flagging things it cannot
 *     defend. Documented, not chased.
 *   - A ternary default (`x != null ? String(x) : '0'`) is not `||`/`??` and
 *     is out of scope.
 *   - A plain string attribute (`value="22"`) is not an expression; nothing is
 *     invented at render time.
 *
 * What the checker deliberately cannot decide: whether a particular fallback
 * is a defect. `value={fvStr('helperConsent') || 'unknown'}` on a Select with no
 * required rule is display-only and fine; `value={fvNum('port') || 22}` next to
 * a required('Port') rule is the defect. That judgement lives in the baseline,
 * one reason line per entry, so a legitimate default is never silently
 * protected and a shrink is always a pass.
 *
 * Policy: existing violations are baselined; a violation the baseline does not
 * list is new and fails the pre-commit hook. The baseline may only shrink.
 * Regenerate with `node lint-fixtures/update-two-owners-baseline.mjs`, which
 * refuses to write a baseline that grows and copies each entry's reason
 * forward. `NOCX_BASELINE_UPDATE=1` prints every violation without failing.
 *
 * Invocation (from frontend/):
 *
 *   node lint-fixtures/check-two-owners.mjs                  # scan src/, baseline applied
 *   node lint-fixtures/check-two-owners.mjs <file...>        # scan exactly these files, NO baseline
 *
 * The single-file form is the evidence mode: run it against a historical file
 * to prove the rule fires on the real pre-fix code, not on a sample.
 */
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, extname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'two-owners-baseline.json')

/** Value-bearing props: the ones a validator or model is expected to judge. */
const VALUE_PROPS = new Set(['value', 'checked', 'selected', 'defaultValue'])

/**
 * Is this expression a literal a checker can prove invisible to validators?
 * Strings, numbers, booleans, and template literals with no substitutions.
 * `null`/`undefined` are absence, not defaults; regex/bigint literals and
 * unary expressions (`-1`) are out of scope.
 */
function isLiteralFallback(node) {
  if (!node) return false
  if (node.type === 'TemplateLiteral') return node.expressions.length === 0
  if (node.type === 'Literal') {
    const v = node.value
    return typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean'
  }
  return false
}

/**
 * Is this expression the empty-string literal? The one empty-string case the
 * rule excludes: with `??` the fallback only paints when the value was already
 * absent, so the surface and a validator reading the raw value both see empty
 * and cannot disagree. `|| ''` is NOT excluded — `||` is falsy-triggered, so
 * `0 || ''` paints empty where the raw value is 0 (see the header).
 */
function isEmptyStringLiteral(node) {
  return node && node.type === 'Literal' && node.value === ''
}

/** Source text of a node with internal whitespace collapsed (stable keys). */
function snippet(source, node) {
  return source.slice(node.range[0], node.range[1]).replace(/\s+/g, ' ').trim()
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
 * Scan one source text for two-owners violations.
 *
 * @param {string} file — path to report on the violation (baseline key prefix)
 * @param {string} source — file contents
 * @returns {Array<{file:string,line:number,column:number,prop:string,
 *   operator:'||'|'??',lhs:string,fallback:string}>}
 *   A file that fails to parse yields a single `prop: 'PARSE'` violation —
 *   the checker fails closed, never silently skips.
 */
export function scanSource(file, source) {
  let ast
  try {
    ast = parse(source, {
      ecmaVersion: 'latest',
      sourceType: 'module',
      ecmaFeatures: { jsx: true },
      loc: true,
      range: true,
    })
  } catch (err) {
    const first = String(err.message).split('\n')[0]
    return [
      { file, line: 0, column: 0, prop: 'PARSE', operator: 'ERROR', lhs: '', fallback: first },
    ]
  }

  const violations = []
  walk(ast, (node) => {
    if (node.type !== 'JSXAttribute') return
    const propName = node.name.type === 'JSXNamespacedName' ? node.name.name.name : node.name.name
    if (!VALUE_PROPS.has(propName)) return
    if (!node.value || node.value.type !== 'JSXExpressionContainer') return
    const expr = node.value.expression
    if (expr.type !== 'LogicalExpression') return
    if (expr.operator !== '||' && expr.operator !== '??') return
    if (!isLiteralFallback(expr.right)) return
    // `?? ''` narrows absent to absent (excluded); `|| ''` stays — see header.
    if (expr.operator === '??' && isEmptyStringLiteral(expr.right)) return
    violations.push({
      file,
      line: node.loc.start.line,
      column: node.loc.start.column + 1,
      prop: propName,
      operator: expr.operator,
      lhs: snippet(source, expr.left),
      fallback: snippet(source, expr.right),
    })
  })
  return violations
}

/**
 * Scan every application .tsx file under `dir` (skipping node_modules, dist
 * and *.test.tsx — test files are the one place synthetic values belong).
 * Paths are reported relative to `base` so baseline keys are stable.
 */
export function scanTree(dir, base) {
  const violations = []
  const walkDir = (d) => {
    for (const entry of readdirSync(d, { withFileTypes: true })) {
      const full = join(d, entry.name)
      if (entry.isDirectory()) {
        if (entry.name === 'node_modules' || entry.name === 'dist') continue
        walkDir(full)
      } else if (entry.name.endsWith('.tsx') && !entry.name.endsWith('.test.tsx')) {
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
  return `${v.file}:${v.prop}:${v.operator}:${v.lhs}:${v.fallback}`
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
    // Evidence / fixture mode: scan exactly the named files, never the tree,
    // and never the baseline — the point is to see what the rule finds.
    violations = fileArgs.flatMap((file) => scanSource(file, readFileSync(file, 'utf8')))
    baseline = new Map()
  } else {
    violations = scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)
    baseline = updateMode ? new Map() : loadBaseline()
  }

  const unbaselined = violations.filter((v) => !baseline.has(violationKey(v)))

  for (const v of violations) {
    if (v.prop === 'PARSE') {
      console.error(`  PARSE ERROR: ${v.file}: ${v.fallback}`)
    } else {
      console.log(`${v.file}:${v.line}: ${v.prop}={${v.lhs} ${v.operator} ${v.fallback}}`)
    }
  }

  if (unbaselined.length > 0) {
    console.error(
      `Two-owners violations: ${violations.length} total, ${unbaselined.length} un-baselined.`,
    )
    for (const v of unbaselined) {
      if (v.prop === 'PARSE') continue
      console.error(`  NEW: ${v.file}:${v.line} ${v.prop}={${v.lhs} ${v.operator} ${v.fallback}}`)
    }
    console.error(
      'A value-bearing prop paints a default invented at the render site, which no validator',
      'or model can see. Remove the fallback so the value flows from the resolver/model — or,',
      'if this is a deliberate display default, record why in the baseline:',
      '`npm run baseline:two-owners-update` (it refuses to grow; every entry keeps a reason).',
    )
    process.exitCode = 1
  } else if (violations.length > 0) {
    console.error(`Two-owners violations: ${violations.length} (all baselined).`)
  }
}
