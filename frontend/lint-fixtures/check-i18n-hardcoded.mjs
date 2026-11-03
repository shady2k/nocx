#!/usr/bin/env node
/**
 * Hardcoded-UI-literal checker — every user-facing string goes through the
 * catalogue (nocx-dej6 stage 3; the ratchet for "no user-facing English
 * literal remains at its point of use", the stage's DONE WHEN).
 *
 * A string literal rendered as element text or handed to a text-bearing prop
 * is UI copy. Copy that bypasses t() cannot switch language: it renders
 * English under every locale. This checker enumerates them as a ratchet:
 * violations are baselined; one the baseline does not list is new and fails
 * lint. The baseline may only shrink — regenerate with
 * `npm run baseline:i18n-hardcoded-update`, which refuses to grow. Stage
 * leaves 3c–3g each empty their files' entries; empty is the stage's close
 * gate.
 *
 * What counts, deliberately:
 *   - JSX text with a non-whitespace string (a letter of any script, or any
 *     non-whitespace symbol run that is not pure punctuation);
 *   - a string literal / template's static part in a JSX attribute or object
 *     property named for user-facing text: title, aria-label, ariaLabel,
 *     label, placeholder, description, hint, message;
 *   - a string literal argument to showToast / showConfirm as its message or
 *     title fields (they arrive as object literals, caught by the property
 *     rule above when literal).
 *
 * What does not count: strings already inside t()/tPlural() (their fallback
 * is the sanctioned English), dynamic expressions, CSS/data-* values,
 * developer-log strings. A permanently-English residue — example values
 * ("gpt-4o"), single technical terms ("tls") — lives in
 * i18n-hardcoded-allowed.json with a reason; it is a register, every entry
 * reviewable, never grown silently.
 *
 * Invocation (from frontend/):
 *   node lint-fixtures/check-i18n-hardcoded.mjs              # scan src/, baseline applied
 *   node lint-fixtures/check-i18n-hardcoded.mjs <file...>    # exactly these files, NO baseline
 */
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'i18n-hardcoded-baseline.json')
const ALLOWED_PATH = resolve(__dirname, 'i18n-hardcoded-allowed.json')

const EXCLUDED_DIRS = new Set(['node_modules', 'dist', 'generated', 'i18n'])

/** Text-bearing prop/property names. `label` also carries option lists
 * ({ value, label }), which is intended: an option label is UI copy. */
const TEXT_PROPS = new Set([
  'title',
  'aria-label',
  'ariaLabel',
  'label',
  'placeholder',
  'description',
  'hint',
  'message',
])

// `tokens` and `comments` are parser metadata arrays, not tree structure —
// walking them visits JSXText twice (see check-glyph-icons.mjs's note).
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

/** True when the node is the first argument of a t()/tPlural() call, or any
 * argument of one (a fallback may be passed by reference). */
function insideTranslateCall(node, parent) {
  let n = node
  let p = parent
  while (p) {
    if (
      p.type === 'CallExpression' &&
      p.arguments.includes(n) &&
      (() => {
        const c = p.callee
        const name = c?.type === 'Identifier' ? c.name : c?.property?.name
        return name === 't' || name === 'tPlural'
      })()
    ) {
      return true
    }
    n = p
    p = p.parent
  }
  return false
}

/** The static text of a string literal or a template's static parts, or
 * null. A template with substitutions yields its cooked static text — the
 * copy around the interpolation is still copy. */
function literalText(node) {
  if (!node) return null
  if (node.type === 'Literal' && typeof node.value === 'string') return node.value
  if (node.type === 'TemplateLiteral') return node.quasis.map((q) => q.value.cooked ?? '').join('')
  return null
}

/** JSX text worth reporting: something beyond whitespace. Pure punctuation
 * runs (—, …, ·) and JSX entities are layout, not copy. */
function reportableJsxText(value) {
  const text = value.replace(/&[a-z]+;|&#\d+;/g, '')
  return /\S/.test(text) && /[0-9A-Za-zА-Яа-яЁё]/u.test(text)
}

/**
 * @returns {Array<{file:string,line:number,context:string,snippet:string}>}
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
      { file, line: 0, context: 'PARSE', snippet: String(err.message).split('\n')[0] },
    ]
  }
  const hits = []
  const report = (node, context, snippet) => {
    // Collapse whitespace at the source: a multi-line JSX text must not
    // carry a newline into its key — the violation key is the collapsed
    // form, stable across prettier re-wraps. The FULL text is kept: the
    // baseline updater imports these objects, and a key cut at 60 chars
    // would collide two long strings that share their head.
    hits.push({
      file,
      line: node.loc.start.line,
      context,
      snippet: snippet.replace(/\s+/g, ' ').trim(),
    })
  }
  // Parent links: insideTranslateCall walks upward, the generic walk skips
  // the parent key, so hang them ourselves.
  walk(ast, (node, parent) => {
    if (parent) node.parent = parent
  })
  walk(ast, (node, parent) => {
    if (node.type === 'JSXText' && reportableJsxText(node.value)) {
      report(node, 'jsx-text', node.value.trim().slice(0, 60))
      return
    }
    if (node.type === 'JSXAttribute' && node.name.type === 'JSXIdentifier') {
      const name = node.name.name
      if (TEXT_PROPS.has(name)) {
        const value = node.value
        // A JSXExpressionContainer wrapping a literal/template is copy;
        // other expressions are dynamic.
        const inner = value?.type === 'JSXExpressionContainer' ? value.expression : value
        const text = literalText(inner)
        if (text !== null && text.trim() !== '' && !insideTranslateCall(inner, node)) {
          report(node, `attr:${name}`, text.trim().slice(0, 60))
        }
      }
      return
    }
    if (
      node.type === 'Property' &&
      !node.computed &&
      ((node.key.type === 'Identifier' && TEXT_PROPS.has(node.key.name)) ||
        (node.key.type === 'Literal' && typeof node.key.value === 'string' && TEXT_PROPS.has(node.key.value)))
    ) {
      const text = literalText(node.value)
      if (text !== null && text.trim() !== '' && !insideTranslateCall(node.value, node)) {
        report(node, `prop:${node.key.type === 'Identifier' ? node.key.name : node.key.value}`, text.trim().slice(0, 60))
      }
    }
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

/** Stable across line moves AND prettier re-wraps: file, where the copy
 * sits, and its first words with all whitespace runs collapsed. A
 * multi-line JSX text re-indented by the formatter must not read as a new
 * violation — collapse before the key, and store the collapsed form. */
export function violationKey(v) {
  const snippet = v.snippet.replace(/\s+/g, ' ').trim()
  return `${v.file}:${v.context}:${snippet}`
}

function loadJson(path) {
  if (!existsSync(path)) return []
  const data = JSON.parse(readFileSync(path, 'utf8'))
  return data.violations ?? []
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const updateMode = process.env.NOCX_BASELINE_UPDATE === '1'
  const fileArgs = process.argv.slice(2).filter((a) => a.endsWith('.ts') || a.endsWith('.tsx'))
  const hits =
    fileArgs.length > 0
      ? fileArgs.flatMap((f) => scanSource(f, readFileSync(f, 'utf8')))
      : scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)

  const allowed = new Set(loadJson(ALLOWED_PATH).map(violationKey))
  const baseline = fileArgs.length > 0 || updateMode ? new Map() : new Map(loadJson(BASELINE_PATH).map((v) => [violationKey(v), v]))

  const unhandled = []
  for (const h of hits) {
    if (h.context === 'PARSE') {
      unhandled.push(h)
      continue
    }
    const key = violationKey(h)
    if (allowed.has(key) || baseline.has(key)) continue
    unhandled.push(h)
  }

  for (const h of hits) {
    if (h.context === 'PARSE') console.error(`  PARSE ERROR: ${h.file}: ${h.snippet}`)
    else console.log(`${h.file}:${h.line}: [${h.context}] "${h.snippet.slice(0, 60)}"`)
  }
  if (unhandled.length > 0) {
    console.error(
      `Hardcoded-UI-literal violations: ${hits.length} total, ${unhandled.length} unhandled.`,
    )
    for (const h of unhandled)
      console.error(`  NEW: ${h.file}:${h.line} [${h.context}] "${h.snippet}"`)
    console.error(
      'User-facing copy must go through t(key, englishLiteral) so it can switch',
      'language. Wrap the literal, mint the key in catalogues/en.ts and ru.ts.',
      'A permanently-English string (an example value, a technical term) belongs',
      'in lint-fixtures/i18n-hardcoded-allowed.json with a reason.',
    )
    process.exitCode = 1
  } else if (hits.length > 0) {
    console.error(`Hardcoded-UI-literal violations: ${hits.length} (all baselined/allowed).`)
  }
}
