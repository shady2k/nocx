#!/usr/bin/env node
/**
 * i18n catalogue checker.
 *
 * Three questions about the catalogues, each with its OWN severity, because
 * the three failures are different in kind (nocx-dej6.3.8's review, the
 * owner's rules of 2026-11-06):
 *
 *   - a key missing from `en` is an ERROR. The English catalogue is the
 *     completeness check: a key that exists nowhere in it is dead, and a key
 *     only `ru` knows can never be resolved through en.
 *   - a key missing from `ru` is REPORTED, never an error. The resolution
 *     order (locale-model.t) falls back to the English catalogue and then to
 *     the call-site literal, so an untranslated key renders English — which
 *     is the product's stated behaviour, not a defect. The report is the
 *     worklist: it says what would read English under `ru`.
 *   - a literal t()/tPlural() call-site key absent from `en` is an ERROR: the
 *     call renders its fallback forever and can never gain a ru entry.
 *
 * And one cross-catalogue invariant: a key present in both must carry the
 * SAME PLACEHOLDERS in both. `{error}` dropped from a Russian form prints a
 * sentence with a hole where the reason was; a `{name}` added on the ru side
 * prints the placeholder literally, because nothing supplies that value. The
 * comparison is per form, so a plural whose `few` form lost its `{count}`
 * fails too.
 *
 * The catalogues are parsed with the TypeScript parser the call-site scan
 * already uses, so the VALUES are read, not guessed from the text: a
 * per-line regex cannot tell a value from a key, and could not see a plural
 * object's forms at all.
 *
 * Invocation: node lint-fixtures/check-i18n-catalogues.mjs   (from frontend/)
 * Exit 1 on an error; exit 0 when the only findings are reported misses.
 */
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')

const EN_PATH = resolve(FRONTEND_DIR, 'src/i18n/catalogues/en.ts')
const RU_PATH = resolve(FRONTEND_DIR, 'src/i18n/catalogues/ru.ts')

// ─── Catalogue parsing ───────────────────────────────────────────────────

function parseModule(file, source) {
  return parse(source, {
    ecmaVersion: 'latest',
    sourceType: 'module',
    ecmaFeatures: { jsx: file.endsWith('.tsx') },
    loc: true,
    range: true,
  })
}

function walkAst(node, visit) {
  if (!node || typeof node !== 'object') return
  visit(node)
  for (const key of Object.keys(node)) {
    if (key === 'parent' || key === 'tokens' || key === 'comments') continue
    const child = node[key]
    if (Array.isArray(child)) {
      for (const c of child) if (c && typeof c.type === 'string') walkAst(c, visit)
    } else if (child && typeof child.type === 'string') {
      walkAst(child, visit)
    }
  }
}

/** One entry's forms: a plain string is one form, a plural object is one form
 *  per category. An entry that is neither yields null — a shape the checker
 *  does not understand fails closed rather than passing silently. */
function entryForms(node) {
  if (!node) return null
  if (node.type === 'Literal' && typeof node.value === 'string') return [node.value]
  if (node.type === 'TemplateLiteral' && node.expressions.length === 0) {
    return [node.quasis.map((q) => q.value.cooked ?? '').join('')]
  }
  if (node.type === 'ObjectExpression') {
    const forms = []
    for (const prop of node.properties) {
      if (prop.type !== 'Property' || prop.computed) return null
      const values = entryForms(prop.value)
      if (values === null || values.length !== 1) return null
      forms.push(values[0])
    }
    return forms
  }
  return null
}

/**
 * Every entry of the catalogue an exported object literal declares.
 * @returns {{forms: Map<string, string[]>, malformed: string[]}}
 */
export function extractEntries(name, source) {
  const forms = new Map()
  const malformed = []
  let ast
  try {
    ast = parseModule(`${name}.ts`, source)
  } catch (err) {
    return { forms, malformed: [`${name}: ${String(err.message).split('\n')[0]}`] }
  }
  let object = null
  walkAst(ast, (node) => {
    if (object !== null) return
    if (
      node.type === 'VariableDeclarator' &&
      node.id.type === 'Identifier' &&
      node.id.name === name &&
      node.init?.type === 'ObjectExpression'
    ) {
      object = node.init
    }
  })
  if (object === null) return { forms, malformed: [`${name}: no exported object literal found`] }
  for (const prop of object.properties) {
    if (prop.type !== 'Property') {
      malformed.push(`${name}: a spread or method where an entry belongs`)
      continue
    }
    const key =
      prop.key.type === 'Literal' && typeof prop.key.value === 'string'
        ? prop.key.value
        : prop.key.type === 'Identifier' && !prop.computed
          ? prop.key.name
          : null
    if (key === null) {
      malformed.push(`${name}: an entry whose key is not a string literal`)
      continue
    }
    const values = entryForms(prop.value)
    if (values === null) malformed.push(`${name}: ${key} has a shape the checker cannot read`)
    else forms.set(key, values)
  }
  return { forms, malformed }
}

/** The `{name}` placeholders of one form, as a sorted set. */
export function placeholdersOf(form) {
  return [...new Set([...form.matchAll(/\{(\w+)\}/g)].map((m) => m[1]))].sort()
}

/** The placeholders every form of an entry carries, and the ones any form is
 *  missing — a phrase whose forms disagree with each other is reported too. */
function entryPlaceholders(forms) {
  const perForm = forms.map(placeholdersOf)
  const all = [...new Set(perForm.flat())].sort()
  const missing = forms
    .map((form, i) => ({ form, lacks: all.filter((p) => !perForm[i].includes(p)) }))
    .filter((f) => f.lacks.length > 0)
  return { all, missing }
}

// ─── Call-site extraction ────────────────────────────────────────────────
//
// The catalogues' own parity is not enough: a t('x.y', 'Fallback') call whose
// key exists in NEITHER catalogue renders its fallback forever and can never
// gain a ru entry. This direction is an error: en.ts's header promises every
// user-visible key is minted in en first.
//
// Dynamic first arguments (a variable, a `?? ''` expression) are not
// extracted — they are Go-minted keys arriving as data (labelKey,
// descriptionKey, group keys) and are pinned by catalogue-parity.test.ts
// witnesses instead. The extracted set is exactly the literal call sites.

/** Directories whose files are not application surfaces. `i18n` is NOT among
 *  them (nocx-dej6.3.8's review): kit-labels.ts lives there and its call sites
 *  are the kit's copy, so excluding the whole module left its keys unchecked.
 *  Only the catalogues themselves are skipped — they declare keys, they never
 *  look one up. */
const CALLSITE_EXCLUDED_DIRS = new Set(['node_modules', 'dist', 'generated', 'catalogues'])

/** The key of a `t(...)`/`tPlural(...)` call: the first argument when it is a
 * plain string literal, otherwise null (dynamic — not extracted). */
function tCallKey(callNode) {
  const callee = callNode.callee
  const name = callee?.type === 'Identifier' ? callee.name : (callee?.property?.name ?? null)
  if (name !== 't' && name !== 'tPlural') return null
  const first = callNode.arguments[0]
  if (first?.type === 'Literal' && typeof first.value === 'string') return first.value
  return null
}

/**
 * Extract the literal keys of t()/tPlural() calls in one source file.
 * @returns {Array<{file:string,line:number,key:string}>}
 *   A file that fails to parse yields one `key: 'PARSE'` entry — fail closed.
 */
export function extractCallSiteKeys(file, source) {
  let ast
  try {
    ast = parseModule(file, source)
  } catch (err) {
    return [{ file, line: 0, key: 'PARSE' }]
  }
  const hits = []
  walkAst(ast, (node) => {
    if (node.type !== 'CallExpression') return
    const key = tCallKey(node)
    if (key !== null) hits.push({ file, line: node.loc.start.line, key })
  })
  return hits
}

export function scanCallSites(dir, base) {
  const hits = []
  const walkDir = (d) => {
    for (const entry of readdirSync(d, { withFileTypes: true })) {
      const full = join(d, entry.name)
      if (entry.isDirectory()) {
        if (CALLSITE_EXCLUDED_DIRS.has(entry.name)) continue
        walkDir(full)
      } else if (
        (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) &&
        !entry.name.endsWith('.test.ts') &&
        !entry.name.endsWith('.test.tsx') &&
        !entry.name.endsWith('.d.ts')
      ) {
        hits.push(...extractCallSiteKeys(relative(base, full), readFileSync(full, 'utf8')))
      }
    }
  }
  walkDir(dir)
  return hits
}

// ─── The comparison, as one testable function ─────────────────────────────
//
// The severities live here rather than in the CLI so the checker's own test
// asserts the RULES (an en miss is an error, a ru miss is only reported, a
// placeholder that does not survive is an error) instead of parsing stderr.

/**
 * @param {ReturnType<typeof extractEntries>} en
 * @param {ReturnType<typeof extractEntries>} ru
 * @param {Array<{file:string,line:number,key:string}>} callSiteHits
 * @returns {{errors: string[], reported: string[], missingFromRu: string[]}}
 */
export function compareCatalogues(en, ru, callSiteHits = []) {
  const errors = []
  const reported = []

  for (const [name, cat] of [
    ['en', en],
    ['ru', ru],
  ]) {
    for (const bad of cat.malformed) errors.push(`${name}: ${bad}`)
  }
  if (en.forms.size === 0) errors.push('en: parsed to no keys at all — parser out of date?')
  if (ru.forms.size === 0) errors.push('ru: parsed to no keys at all — parser out of date?')

  // ERROR: en is the completeness check.
  for (const key of [...ru.forms.keys()].filter((k) => !en.forms.has(k)).sort()) {
    errors.push(`MISSING FROM EN: ${key}`)
  }
  // REPORTED: ru may be behind, and the runtime falls back to English.
  const missingFromRu = [...en.forms.keys()].filter((k) => !ru.forms.has(k)).sort()

  // ERROR: placeholders must survive translation, form by form.
  for (const key of [...en.forms.keys()].filter((k) => ru.forms.has(k)).sort()) {
    const enPh = entryPlaceholders(en.forms.get(key))
    const ruPh = entryPlaceholders(ru.forms.get(key))
    const lost = enPh.all.filter((p) => !ruPh.all.includes(p))
    const added = ruPh.all.filter((p) => !enPh.all.includes(p))
    if (lost.length > 0 || added.length > 0) {
      errors.push(
        `PLACEHOLDER MISMATCH: ${key} — en {${enPh.all.join('} {')}}, ru {${ruPh.all.join('} {')}}`,
      )
    }
    for (const gap of ruPh.missing) {
      errors.push(
        `PLACEHOLDER MISMATCH: ${key} — a ru form drops {${gap.lacks.join('} {')}}: "${gap.form}"`,
      )
    }
  }

  // ERROR: every literal t()/tPlural() call-site key must be minted in en.
  for (const h of callSiteHits.filter((h) => h.key === 'PARSE')) {
    errors.push(`PARSE ERROR (call-site scan): ${h.file}`)
  }
  for (const h of callSiteHits.filter((h) => h.key !== 'PARSE' && !en.forms.has(h.key))) {
    errors.push(`UNMINTED: ${h.key} (${h.file}:${h.line})`)
  }

  for (const key of missingFromRu) reported.push(`untranslated in ru: ${key}`)
  return { errors, reported, missingFromRu }
}

// ─── CLI entry point ──────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const read = (path, name) => {
    let source
    try {
      source = readFileSync(path, 'utf8')
    } catch (err) {
      console.error(`I18N CATALOGUES: cannot read ${name} catalogue (${path}): ${err.message}`)
      process.exit(1)
    }
    return extractEntries(name, source)
  }
  const en = read(EN_PATH, 'en')
  const ru = read(RU_PATH, 'ru')
  const callSiteHits = scanCallSites(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)
  const { errors, reported, missingFromRu } = compareCatalogues(en, ru, callSiteHits)

  if (errors.length > 0) {
    console.error(`I18N CATALOGUES: ${errors.length} error(s):`)
    for (const e of errors) console.error(`  ${e}`)
  }
  if (reported.length > 0) {
    console.error(
      `I18N CATALOGUES: ${reported.length} key(s) have no Russian translation. NOT an error — a missing translation renders the English catalogue value (locale-model.ts's resolution order). Worklist:`,
    )
    for (const r of reported) console.error(`  ${r}`)
  }
  if (errors.length > 0) process.exit(1)

  console.error(
    `I18N CATALOGUES: ${en.forms.size} keys, ${callSiteHits.length} call-site key(s), en↔ru parity and placeholders enforced, ${missingFromRu.length} untranslated in ru.`,
  )
}
