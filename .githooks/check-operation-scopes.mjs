#!/usr/bin/env node
/**
 * Operation-scope ratchet — a provider call in the handler layer, or a
 * context that lost its invocation, must be a baselined decision.
 *
 * ADR-8eej3e66 §4 says nothing runs outside a scope: the catalogue's handle
 * hands out a scope carrying deadlines and cancellation, and a static check
 * refuses provider calls outside the adapters, `context.Background()` in
 * request work and unchecked context detachment.
 *
 * THIS FILE CHECKS ONLY THE CONTEXTS. A regex cannot answer "is this a
 * provider call": `p := s.filesys; p.Close(...)` and
 * `server.filesys.Close(...)` are the same call under other spellings. That
 * half is `internal/operation/scopes`'s Go test, which resolves every call
 * through go/types and reports the method it reaches, whatever the receiver is
 * named. This ratchet keeps the two context shapes, where text is the whole
 * question: a call that dropped its invocation.
 *
 * Scope, stated rather than implied: this checks internal/transport's
 * non-test Go files. The provider packages themselves are the adapters, and
 * internal/app is the composition root plus the background owner — its
 * provider calls construct and supervise, they do not serve a request. The
 * internal operations this stage wired call BeginInternal where they start;
 * converting internal/app's remaining background call sites belongs to the
 * stages that migrate each domain (4–8).
 *
 * Invocation: node .githooks/check-operation-scopes.mjs
 * Regenerate: node .githooks/update-operation-scopes-baseline.mjs
 */
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const PROJECT_ROOT = resolve(__dirname, '..')
const BASELINE_PATH = join(__dirname, 'operation-scopes-baseline.json')
const SCOPED_DIR = 'internal/transport'

const BACKGROUND_RE = /context\.(?:Background\(\)|WithoutCancel\()/

function lineKey(file, text) {
  return `${file}:${text.trim()}`
}

/**
 * Collect every violation from a {file: source} map. Pure, so the planted
 * violation is a fixture rather than an edit to the tree.
 */
export function collectViolations(sources) {
  const violations = []
  for (const [file, source] of Object.entries(sources)) {
    source.split('\n').forEach((line, i) => {
      const text = line.trim()
      if (text.startsWith('//') || text === '') return
      if (BACKGROUND_RE.test(text)) {
        violations.push({ file, line: i + 1, text, kind: 'unscoped-context' })
      }
    })
  }
  return violations
}

/** Split a violation list into baselined matches and new ones. */
export function partition(violations, baseline) {
  const byKey = new Map(baseline.map((entry) => [lineKey(entry.file, entry.text), entry]))
  const known = []
  const fresh = []
  for (const v of violations) {
    const entry = byKey.get(lineKey(v.file, v.text))
    if (entry) known.push({ ...v, reason: entry.reason })
    else fresh.push(v)
  }
  return { known, fresh }
}

export function collectSources(root = PROJECT_ROOT) {
  const sources = {}
  const dir = join(root, SCOPED_DIR)
  for (const name of readdirSync(dir).sort()) {
    if (!name.endsWith('.go') || name.endsWith('_test.go')) continue
    sources[`${SCOPED_DIR}/${name}`] = readFileSync(join(dir, name), 'utf8')
  }
  return sources
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const baseline = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
  for (const entry of baseline) {
    if (typeof entry.reason !== 'string' || entry.reason.trim() === '') {
      console.error(`baseline entry for ${entry.file} has no reason: ${entry.text}`)
      process.exit(1)
    }
  }
  const { known, fresh } = partition(collectViolations(collectSources()), baseline)
  console.log(`operation scopes: ${known.length} baselined unscoped context(s), 0 new`)
  console.log('provider calls are checked from types by internal/operation/scopes')
  if (fresh.length !== 0) {
    for (const v of fresh) console.error(`NEW ${v.kind}: ${v.file}:${v.line}  ${v.text}`)
    console.error(
      '\nTake the provider call through the operation scope, or add a reasoned baseline entry.',
    )
    process.exit(1)
  }
}
