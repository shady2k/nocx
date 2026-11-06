#!/usr/bin/env node
/**
 * i18n catalogue parity checker.
 *
 * Compares the key sets of src/i18n/catalogues/en.ts and ru.ts — the parity
 * rule the localisation design fixes (nocx-dej6, design §"Frontend catalogue
 * shape"), TIGHTENED at stage 3's close:
 *
 *   - a key missing from `en` is an ERROR: the English catalogue is the
 *     completeness check, and a key that exists nowhere in it is dead;
 *   - a key missing from `ru` is an ERROR too: the Russian catalogue is
 *     complete by policy since the epic's ru-l10n completion. A wording no
 *     approval round has ruled on carries its English AS the ru value — the
 *     visible fallback lives in the data, not in a tolerated hole.
 *   - a literal t()/tPlural() call-site key absent from `en` is an ERROR:
 *     a call whose key exists in neither catalogue renders its fallback
 *     forever and can never gain a ru entry (measured as nocx-dej6.3.8).
 *
 * The catalogues are parsed as text rather than imported: they are .ts
 * modules and this fixture runs under plain node, with no loader.
 *
 * Invocation: node lint-fixtures/check-i18n-catalogues.mjs   (from frontend/)
 */
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')

const EN_PATH = resolve(FRONTEND_DIR, 'src/i18n/catalogues/en.ts')
const RU_PATH = resolve(FRONTEND_DIR, 'src/i18n/catalogues/ru.ts')

/**
 * Extract the quoted keys of a catalogue's object literal. A key is the first
 * single- or double-quoted string on a line that ends with `:` at object-
 * member depth — the shape every entry in these flat catalogues has:
 *
 *   'surface.key.name': 'value',
 *   'surface.key.name': {
 *
 * Values are never read, so quoted strings inside a value (none today, and a
 * plural object's members are unquoted) cannot be mistaken for keys: they are
 * never followed by `:` on the same line.
 */
function extractKeys(source) {
  const keys = new Set()
  const re = /^\s*'([^']+)'\s*:|^\s*"([^"]+)"\s*:/gm
  let match
  while ((match = re.exec(source)) !== null) {
    keys.add(match[1] ?? match[2])
  }
  return keys
}

function loadCatalogue(path, name) {
  let source
  try {
    source = readFileSync(path, 'utf8')
  } catch (err) {
    console.error(`I18N CATALOGUES: cannot read ${name} catalogue (${path}): ${err.message}`)
    process.exit(1)
  }
  const keys = extractKeys(source)
  if (keys.size === 0) {
    // An empty catalogue is a parse failure until proven otherwise: neither
    // catalogue is ever legitimately empty.
    console.error(`I18N CATALOGUES: no keys found in ${name} — parser out of date?`)
    process.exit(1)
  }
  return keys
}

// ─── Call-site extraction ────────────────────────────────────────────────
//
// The catalogues' own parity is not enough: a t('x.y', 'Fallback') call whose
// key exists in NEITHER catalogue renders its fallback forever and can never
// gain a ru entry (measured as nocx-dej6.3.8: three such keys shipped). This
// direction is an error: en.ts's header promises every user-visible key is
// minted in en first, so a call-site key absent from en is a bug.
//
// Dynamic first arguments (a variable, a `?? ''` expression) are not
// extracted — they are Go-minted keys arriving as data (labelKey,
// descriptionKey, group keys) and are pinned by catalogue-parity.test.ts
// witnesses instead. The extracted set is exactly the literal call sites.
//
// Exclusions mirror check-glyph-icons.mjs's tree walk: tests, generated code
// and the i18n module itself (whose catalogues legitimately contain keys no
// call site uses).

const CALLSITE_EXCLUDED_DIRS = new Set(['node_modules', 'dist', 'generated', 'i18n'])

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

/** The key of a `t(...)`/`tPlural(...)` call: the first argument when it is a
 * plain string literal, otherwise null (dynamic — not extracted). */
function tCallKey(callNode) {
  const callee = callNode.callee
  const name =
    callee?.type === 'Identifier' ? callee.name : callee?.property?.name ?? null
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
    ast = parse(source, {
      ecmaVersion: 'latest',
      sourceType: 'module',
      ecmaFeatures: { jsx: file.endsWith('.tsx') },
      loc: true,
      range: true,
    })
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

// ─── CLI entry point ──────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const enKeys = loadCatalogue(EN_PATH, 'en')
  const ruKeys = loadCatalogue(RU_PATH, 'ru')

  // Error direction: en is the completeness check.
  const missingFromEn = [...ruKeys].filter((k) => !enKeys.has(k)).sort()
  // Warning direction: ru may be partial, runtime falls back to en.
  const missingFromRu = [...enKeys].filter((k) => !ruKeys.has(k)).sort()

  // Error direction: every literal t()/tPlural() call-site key must be minted.
  const callSiteHits = scanCallSites(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)
  const unminted = callSiteHits.filter((h) => h.key !== 'PARSE' && !enKeys.has(h.key))

  let failed = false

  if (missingFromEn.length > 0) {
    failed = true
    console.error(
      `I18N CATALOGUES: ${missingFromEn.length} key(s) exist in ru but not in en — en is the completeness check, add them there first:`,
    )
    for (const key of missingFromEn) console.error(`  MISSING FROM EN: ${key}`)
  }

  if (missingFromRu.length > 0) {
    failed = true
    console.error(
      `I18N CATALOGUES: ${missingFromRu.length} key(s) have no ru translation — the Russian catalogue is complete by policy since stage 3's close; add the ru entry (or mint the key's English as its ru value when a round has not ruled yet):`,
    )
    for (const key of missingFromRu) console.error(`  untranslated in ru: ${key}`)
  }

  if (callSiteHits.some((h) => h.key === 'PARSE')) {
    failed = true
    console.error('I18N CATALOGUES: a source file failed to parse while extracting t() call sites:')
    for (const h of callSiteHits.filter((h) => h.key === 'PARSE'))
      console.error(`  PARSE ERROR: ${h.file}`)
  }

  if (unminted.length > 0) {
    failed = true
    console.error(
      `I18N CATALOGUES: ${unminted.length} t() call-site key(s) exist in neither catalogue — mint them in en first:`,
    )
    for (const h of unminted) console.error(`  UNMINTED: ${h.key} (${h.file}:${h.line})`)
  }

  if (failed) process.exit(1)

  const ruNote = missingFromRu.length ? ` (${missingFromRu.length} MISSING in ru)` : ''
  console.error(
    `I18N CATALOGUES: ${enKeys.size} keys, ${callSiteHits.length} call-site key(s), en↔ru parity enforced${ruNote}.`,
  )
}
