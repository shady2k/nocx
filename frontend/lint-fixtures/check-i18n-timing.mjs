#!/usr/bin/env node
/**
 * i18n TIMING checker — a `t()` that runs once cannot follow the language.
 *
 * `t()` reads the locale signal. Called while a module is evaluated, in a
 * component body, or in a descriptor a factory builds, it answers with the
 * language the window started in and the surface keeps that answer for the
 * life of the process. The defect is invisible in English, invisible to a test
 * that mounts after the switch, and invisible to the hardcoded-literal
 * checker — which sees the *strings*, not the moment they were resolved
 * (nocx-dej6.3.8's review, defects 1 and 2: the approval prompt's title map,
 * the activity rail's view names, the vault's auto-lock list, the notification
 * filters, the connections group's field labels).
 *
 * What is refused:
 *
 *   - `t()`/`tPlural()` at MODULE scope — no enclosing function at all, so the
 *     call runs when the module is imported;
 *   - the same call in a COMPONENT body, outside JSX, outside a nested
 *     function and outside the kit's lazy builders (`createMemo`,
 *     `createEffect`, `onMount`, `createResource`): the setup-time snapshot;
 *   - the same call in a function the module INVOKES at its own top level
 *     (`main()`): that function runs once, when the module is imported, which
 *     is the module-scope case wearing a name;
 *   - the same call in a CLASS FIELD initialiser: the field is written when
 *     the instance is constructed, which for the quick-connect provider is
 *     once, at the composition root.
 *
 * What is not refused, deliberately:
 *
 *   - a call inside JSX (`<b title={t('a', 'A')}>`, `{t('a', 'A')}`) — a getter
 *     the renderer tracks;
 *   - a call inside a nested function (an event handler, a helper a render
 *     calls) — the moment it runs is the caller's, and every caller in this
 *     codebase calls those from a tracked position;
 *   - a call inside the kit's lazy builders, for the same reason;
 *   - a call inside a `create*` factory (or any other plainly-named
 *     function): the moment it runs is its caller's — a per-pane painter and
 *     a per-event frame card are called from where the thing is made, and the
 *     checker cannot tell that from the composition root without a call
 *     graph. `painter.ts`'s grid label is the known residue this leaves: it is
 *     resolved when a pane is created, so a pane that outlives a switch keeps
 *     the language it was born in;
 *   - a call inside a module-level function that is not a component
 *     (`function sentence()`, `export const label = (n) => …`): those run when
 *     their result is used, which is where the surfaces call them.
 *
 * The rule is a ratchet like its siblings (check-i18n-hardcoded.mjs,
 * check-dead-exports.mjs): existing violations are baselined, one the baseline
 * does not list fails lint, and the baseline may only shrink —
 * `npm run baseline:i18n-timing-update` refuses growth.
 *
 * Invocation (from frontend/):
 *   node lint-fixtures/check-i18n-timing.mjs            # scan src/, baseline applied
 *   node lint-fixtures/check-i18n-timing.mjs <file...>  # exactly these files, NO baseline
 */
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'i18n-timing-baseline.json')

const EXCLUDED_DIRS = new Set(['node_modules', 'dist', 'generated', 'catalogues'])

/** Calls that DEFER their argument: a `t()` under one of these runs when the
 *  computation runs, which is the render. */
const LAZY_CALLS = new Set(['createMemo', 'createEffect', 'onMount', 'createResource', 'untrack'])

function walk(node, visit, parent = null) {
  if (!node || typeof node !== 'object') return
  visit(node, parent)
  for (const key of Object.keys(node)) {
    if (key === 'parent' || key === 'tokens' || key === 'comments') continue
    const child = node[key]
    if (Array.isArray(child)) {
      for (const c of child) if (c && typeof c.type === 'string') walk(c, visit, node)
    } else if (child && typeof child.type === 'string') {
      walk(child, visit, node)
    }
  }
}

const isFunction = (n) =>
  n !== null &&
  n !== undefined &&
  (n.type === 'ArrowFunctionExpression' ||
    n.type === 'FunctionExpression' ||
    n.type === 'FunctionDeclaration')

/** The name a function is known by: its declaration's, the const it is
 *  assigned to, or the object key it sits under. */
function functionName(fn) {
  if (fn.id?.name) return fn.id.name
  const p = fn.parent
  if (p?.type === 'VariableDeclarator' && p.id.type === 'Identifier') return p.id.name
  if (p?.type === 'Property' && p.key.type === 'Identifier') return p.key.name
  return null
}

/** A component: a capitalized function. Its body runs once per mount, and a
 *  mounted component does not run again when the locale changes — which is
 *  exactly the snapshot defect 2 named (the vault's auto-lock list, the
 *  notification filter axes, the connections group's field labels). */
const looksLikeComponent = (name) => name !== null && /^[A-Z]/.test(name)

/** The functions a module INVOKES at its own top level (`main()`) — called
 *  once, when the module is imported, exactly like the module-scope calls
 *  above. `main.tsx`'s composition root is this shape, and the rail's view
 *  descriptors are built inside it. */
function bootstrapFunctions(ast) {
  const names = new Set()
  for (const statement of ast.body) {
    if (statement.type !== 'ExpressionStatement') continue
    const expr = statement.expression
    if (expr.type === 'CallExpression' && expr.callee.type === 'Identifier') {
      names.add(expr.callee.name)
    }
  }
  return names
}

/** Whether a call sits inside one of the kit's lazy builders, up to `stop`.
 *  The path is what matters: the call has to be one of the builder's own
 *  arguments, not merely somewhere under an unrelated call expression. */
function underLazyCall(callNode, stop) {
  let child = callNode
  let n = callNode.parent
  while (n && n !== stop) {
    if (
      n.type === 'CallExpression' &&
      n.callee.type === 'Identifier' &&
      LAZY_CALLS.has(n.callee.name) &&
      n.arguments.includes(child)
    ) {
      return true
    }
    child = n
    n = n.parent
  }
  return false
}

/**
 * Where a `t()` call sits, in the terms this checker refuses on.
 * @returns {{verdict: 'module'|'setup'|null, detail: string}}
 */
export function classify(callNode, bootstraps = new Set()) {
  // The nearest enclosing function, and whether anything nested it.
  let n = callNode.parent
  let nearest = null
  let nested = false
  while (n && n.type !== 'Program') {
    if (isFunction(n)) {
      if (nearest === null) nearest = n
      else {
        nested = true
        break
      }
    }
    n = n.parent
  }

  // JSX: a getter, tracked by the renderer.
  let m = callNode.parent
  while (m && m !== nearest) {
    if (m.type === 'JSXExpressionContainer' || m.type === 'JSXAttribute') {
      return { verdict: null, detail: '' }
    }
    m = m.parent
  }

  if (nearest === null) {
    return { verdict: 'module', detail: 'module scope — runs when the module is imported' }
  }
  if (nested) return { verdict: null, detail: '' }
  if (underLazyCall(callNode, nearest)) return { verdict: null, detail: '' }

  const name = functionName(nearest)
  if (bootstraps.has(name)) {
    return { verdict: 'setup', detail: `${name}() is invoked once, when the module is imported` }
  }
  if (!looksLikeComponent(name)) return { verdict: null, detail: '' }
  return { verdict: 'setup', detail: `${name}() runs once, at setup` }
}

/**
 * Class fields: a PropertyDefinition whose value holds a `t()` runs when the
 * instance is constructed. Its own shape, because neither of the rules above
 * describes it.
 * @returns {{verdict: 'field'|null, detail: string}}
 */
export function classifyField(callNode) {
  let n = callNode.parent
  while (n && n.type !== 'Program') {
    if (isFunction(n)) return { verdict: null, detail: '' }
    if (n.type === 'PropertyDefinition' && n.value !== null) {
      const key = n.key.type === 'Identifier' ? n.key.name : 'a field'
      return { verdict: 'field', detail: `${key} is written once, when the instance is built` }
    }
    n = n.parent
  }
  return { verdict: null, detail: '' }
}

/**
 * @returns {Array<{file:string,line:number,context:string,key:string,detail:string}>}
 *   A file that fails to parse yields one `context: 'PARSE'` entry — fail closed.
 */
export function scanSource(file, source) {
  let ast
  try {
    ast = parse(source, {
      ecmaVersion: 'latest',
      sourceType: 'module',
      ecmaFeatures: { jsx: file.endsWith('.tsx') },
      loc: true,
      range: true,
    })
  } catch (err) {
    return [
      { file, line: 0, context: 'PARSE', key: '', detail: String(err.message).split('\n')[0] },
    ]
  }
  walk(ast, (node, parent) => {
    if (parent) node.parent = parent
  })
  const bootstraps = bootstrapFunctions(ast)
  const hits = []
  walk(ast, (node) => {
    if (node.type !== 'CallExpression') return
    const callee = node.callee
    const name = callee?.type === 'Identifier' ? callee.name : (callee?.property?.name ?? null)
    if (name !== 't' && name !== 'tPlural') return
    const keyArg = node.arguments[0]
    const key = keyArg?.type === 'Literal' && typeof keyArg.value === 'string' ? keyArg.value : ''
    // The field rule first: a class field at module scope is both shapes, and
    // "written once, when the instance is built" is the sentence that tells a
    // reader where to look.
    const field = classifyField(node)
    const found = field.verdict === null ? classify(node, bootstraps) : field
    if (found.verdict === null) return
    hits.push({
      file,
      line: node.loc.start.line,
      context: found.verdict,
      key,
      detail: found.detail,
    })
  })
  return hits
}

export function scanTree(dir, base) {
  const hits = []
  const walkDir = (d) => {
    for (const entry of readdirSync(d, { withFileTypes: true })) {
      const full = join(d, entry.name)
      if (entry.isDirectory()) {
        if (EXCLUDED_DIRS.has(entry.name)) continue
        walkDir(full)
      } else if (
        (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) &&
        !entry.name.endsWith('.test.ts') &&
        !entry.name.endsWith('.test.tsx') &&
        !entry.name.endsWith('.d.ts')
      ) {
        hits.push(...scanSource(relative(base, full), readFileSync(full, 'utf8')))
      }
    }
  }
  walkDir(dir)
  return hits
}

/** Stable across line moves and prettier re-wraps: file, shape, key. */
export function violationKey(v) {
  return `${v.file}:${v.context}:${v.key}`
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const fileArgs = process.argv.slice(2).filter((a) => a.endsWith('.ts') || a.endsWith('.tsx'))
  const hits =
    fileArgs.length > 0
      ? fileArgs.flatMap((f) => scanSource(f, readFileSync(f, 'utf8')))
      : scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)

  const parseErrors = hits.filter((h) => h.context === 'PARSE')
  let baseline = new Map()
  if (fileArgs.length === 0 && existsSync(BASELINE_PATH)) {
    const recorded = JSON.parse(readFileSync(BASELINE_PATH, 'utf8')).violations ?? []
    baseline = new Map(recorded.map((v) => [violationKey(v), v]))
  }
  const unhandled = hits.filter((h) => h.context !== 'PARSE' && !baseline.has(violationKey(h)))

  for (const h of parseErrors) console.error(`  PARSE ERROR: ${h.file}: ${h.detail}`)
  if (unhandled.length > 0) {
    console.error(
      `I18N TIMING: ${unhandled.length} t() call(s) resolve before render, so they keep the language the window started in:`,
    )
    for (const h of unhandled) {
      console.error(
        `  ${h.file}:${h.line} [${h.context}] ${h.key || '(dynamic key)'} — ${h.detail}`,
      )
    }
    console.error(
      'Hold a key or an accessor and resolve it where it is read, or call t() inside JSX.',
      'See docs/decisions/nswp57x0-catalogues-keys-and-the-translation-contract.md.',
    )
    process.exitCode = 1
  } else if (hits.length > 0 && parseErrors.length === 0) {
    console.error(`I18N TIMING: ${hits.length} call(s) resolved at setup (all baselined).`)
  }
}
