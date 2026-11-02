#!/usr/bin/env node
/**
 * i18n catalogue parity checker.
 *
 * Compares the key sets of src/i18n/catalogues/en.ts and ru.ts — the parity
 * rule the localisation design fixes (nocx-dej6, design §"Frontend catalogue
 * shape"):
 *
 *   - a key missing from `en` is an ERROR: the English catalogue is the
 *     completeness check, and a key that exists nowhere in it is dead;
 *   - a key missing from `ru` is a WARNING, printed to stderr but never
 *     exiting nonzero: the Russian catalogue is deliberately partial while
 *     coverage grows, and `t()` falls back to the English value at runtime,
 *     so the gap is review-visible without blocking the commit.
 *
 * Warnings therefore must not break `npm run lint` (`eslint --max-warnings 0`
 * runs first and gates eslint's own warnings only), and this fixture keeps
 * its exit code clean while a ru gap exists — the same informational-not-
 * gating split check-dead-exports.mjs applies to dependency noise.
 *
 * The catalogues are parsed as text rather than imported: they are .ts
 * modules and this fixture runs under plain node, with no loader.
 *
 * Invocation: node lint-fixtures/check-i18n-catalogues.mjs   (from frontend/)
 */
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

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

// ─── CLI entry point ──────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const enKeys = loadCatalogue(EN_PATH, 'en')
  const ruKeys = loadCatalogue(RU_PATH, 'ru')

  // Error direction: en is the completeness check.
  const missingFromEn = [...ruKeys].filter((k) => !enKeys.has(k)).sort()
  // Warning direction: ru may be partial, runtime falls back to en.
  const missingFromRu = [...enKeys].filter((k) => !ruKeys.has(k)).sort()

  let failed = false

  if (missingFromEn.length > 0) {
    failed = true
    console.error(
      `I18N CATALOGUES: ${missingFromEn.length} key(s) exist in ru but not in en — en is the completeness check, add them there first:`,
    )
    for (const key of missingFromEn) console.error(`  MISSING FROM EN: ${key}`)
  }

  if (missingFromRu.length > 0) {
    console.warn(
      `I18N CATALOGUES: ${missingFromRu.length} key(s) have no ru translation (runtime falls back to en):`,
    )
    for (const key of missingFromRu) console.warn(`  untranslated in ru: ${key}`)
  }

  if (failed) process.exit(1)

  const warnNote = missingFromRu.length ? ` (${missingFromRu.length} untranslated in ru)` : ''
  console.error(`I18N CATALOGUES: ${enKeys.size} keys, en complete${warnNote}.`)
}
