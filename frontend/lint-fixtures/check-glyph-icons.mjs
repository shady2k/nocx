#!/usr/bin/env node
/**
 * Glyph-icons checker — no character stands in for an icon (nocx-9bpeq.5).
 *
 * The kit's icons are components in src/ui/icons. A ⋮ × ✕ ⚠ or an emoji written as
 * an element's TEXT is a second icon vocabulary: it renders at the font's whim, has
 * no size the kit controls, and passed every other gate here — the block header's ⋮
 * and its 📁 were built inside files the raw-control lint exempts (ADR-0012's
 * imperative code), which is exactly why this rule exempts no path.
 *
 * What counts as element text, deliberately:
 *   - JSX text, and a string/template literal that is a direct child of a JSX element;
 *   - the right side of an assignment to `.textContent` or `.innerText`;
 *   - the value of a `textContent:` property in an object literal.
 * A glyph anywhere else — a title attribute, a constant compared against a program's
 * screen, a regular expression — is not an icon and is not reported. A table of glyphs
 * assigned later through a lookup is not seen; documented, not chased.
 *
 * Policy: violations are baselined with a reason; one the baseline does not list is
 * new and fails lint. The baseline may only shrink — regenerate with
 * `npm run baseline:glyph-icons-update`, which refuses to grow and copies reasons.
 *
 * Invocation (from frontend/):
 *   node lint-fixtures/check-glyph-icons.mjs              # scan src/, baseline applied
 *   node lint-fixtures/check-glyph-icons.mjs <file...>    # exactly these files, NO baseline
 */
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'glyph-icons-baseline.json')

/** The first glyph in `text` that is standing in for an icon, or null. */
const GLYPH = /[⋮×✕⚠]|\p{Extended_Pictographic}/u
function glyphIn(text) {
  const m = GLYPH.exec(text)
  return m ? m[0] : null
}

function walk(node, visit, parent = null) {
  if (!node || typeof node !== 'object') return
  visit(node, parent)
  for (const key of Object.keys(node)) {
    if (key === 'parent') continue
    const child = node[key]
    if (Array.isArray(child)) {
      for (const c of child) if (c && typeof c.type === 'string') walk(c, visit, node)
    } else if (child && typeof child.type === 'string') {
      walk(child, visit, node)
    }
  }
}

/** The static text of a string literal or a template's quasis, or null. */
function literalText(node) {
  if (!node) return null
  if (node.type === 'Literal' && typeof node.value === 'string') return node.value
  if (node.type === 'TemplateLiteral') return node.quasis.map((q) => q.value.cooked ?? '').join('')
  return null
}

function isTextMember(node) {
  return (
    node?.type === 'MemberExpression' &&
    !node.computed &&
    node.property.type === 'Identifier' &&
    (node.property.name === 'textContent' || node.property.name === 'innerText')
  )
}

/**
 * @returns {Array<{file:string,line:number,glyph:string,context:string,reason:string}>}
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
      { file, line: 0, glyph: '', context: 'PARSE', reason: String(err.message).split('\n')[0] },
    ]
  }
  const hits = []
  const report = (node, text, context) => {
    const glyph = glyphIn(text)
    if (glyph === null) return
    hits.push({ file, line: node.loc.start.line, glyph, context, reason: '' })
  }
  walk(ast, (node, parent) => {
    if (node.type === 'JSXText') {
      report(node, node.value, 'jsx-text')
      return
    }
    if (node.type === 'JSXExpressionContainer' && parent?.type === 'JSXElement') {
      const text = literalText(node.expression)
      if (text !== null) report(node, text, 'jsx-child')
      return
    }
    if (node.type === 'AssignmentExpression' && isTextMember(node.left)) {
      const text = literalText(node.right)
      if (text !== null) report(node, text, 'text-assignment')
      return
    }
    if (
      node.type === 'Property' &&
      !node.computed &&
      ((node.key.type === 'Identifier' && node.key.name === 'textContent') ||
        (node.key.type === 'Literal' && node.key.value === 'textContent'))
    ) {
      const text = literalText(node.value)
      if (text !== null) report(node, text, 'text-property')
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
        if (entry.name === 'node_modules' || entry.name === 'dist' || entry.name === 'generated')
          continue
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

/** Stable across line moves: file, glyph and where it was used. */
export function violationKey(v) {
  return `${v.file}:${v.glyph}:${v.context}`
}

function loadBaseline() {
  const map = new Map()
  try {
    const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
    for (const v of data.violations) map.set(violationKey(v), v)
  } catch {
    // No baseline — every violation is an error.
  }
  return map
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const updateMode = process.env.NOCX_BASELINE_UPDATE === '1'
  const fileArgs = process.argv.slice(2).filter((a) => a.endsWith('.ts') || a.endsWith('.tsx'))
  const hits =
    fileArgs.length > 0
      ? fileArgs.flatMap((f) => scanSource(f, readFileSync(f, 'utf8')))
      : scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)
  const baseline = fileArgs.length > 0 || updateMode ? new Map() : loadBaseline()
  // Count per key, so two identical glyphs in one file and context need two entries.
  const seen = new Map()
  const unbaselined = []
  for (const h of hits) {
    const key = violationKey(h)
    const n = (seen.get(key) ?? 0) + 1
    seen.set(key, n)
    const allowed = baseline.get(key)?.count ?? (baseline.has(key) ? 1 : 0)
    if (h.context === 'PARSE' || n > allowed) unbaselined.push(h)
  }
  for (const h of hits) {
    if (h.context === 'PARSE') console.error(`  PARSE ERROR: ${h.file}: ${h.reason}`)
    else console.log(`${h.file}:${h.line}: "${h.glyph}" as ${h.context}`)
  }
  if (unbaselined.length > 0) {
    console.error(`Glyph-icon violations: ${hits.length} total, ${unbaselined.length} new.`)
    for (const h of unbaselined)
      console.error(`  NEW: ${h.file}:${h.line} "${h.glyph}" as ${h.context}`)
    console.error(
      'A character is standing in for an icon. Use a component from src/ui/icons inside the kit',
      'control that holds it (IconButton, createIconButton). A baseline entry needs a reason:',
      '`npm run baseline:glyph-icons-update` refuses to grow.',
    )
    process.exitCode = 1
  } else if (hits.length > 0) {
    console.error(`Glyph-icon violations: ${hits.length} (all baselined).`)
  }
}
