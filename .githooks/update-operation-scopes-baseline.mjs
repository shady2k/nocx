#!/usr/bin/env node
/**
 * Regenerate the operation-scope baseline. Every existing entry keeps its
 * reason; a new entry gets an empty reason and the gate refuses it until a
 * human or agent writes one down. A baseline that grew is reviewable in the
 * diff, and an unexplained exception cannot clear the gate silently.
 */
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectSources, collectViolations } from './check-operation-scopes.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const PROJECT_ROOT = resolve(__dirname, '..')
const BASELINE_PATH = join(__dirname, 'operation-scopes-baseline.json')

const old = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
const reasons = new Map(old.map((entry) => [`${entry.file}:${entry.text.trim()}`, entry.reason]))
const entries = collectViolations(collectSources(PROJECT_ROOT)).map((v) => ({
  file: v.file,
  text: v.text,
  kind: v.kind,
  reason: reasons.get(`${v.file}:${v.text.trim()}`) ?? '',
}))
writeFileSync(BASELINE_PATH, `${JSON.stringify(entries, null, 2)}\n`)
console.log(
  `${entries.length} entries; ${entries.filter((e) => e.reason === '').length} need a reason`,
)
