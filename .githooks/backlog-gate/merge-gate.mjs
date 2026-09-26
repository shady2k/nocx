#!/usr/bin/env node
/**
 * The backlog gate for a merge commit: a violation is new only when it is new
 * against BOTH parents.
 *
 * check.mjs takes one baseline, and the hook gives it HEAD. For an ordinary
 * commit that is the whole truth. For a merge it is half of it: debt that
 * arrives from the other parent is judged new against HEAD, although it was
 * already on that parent and was judged there. Measured 2026-09-26: merging
 * main into the 0.29.0 setup branch was refused for 20 results main already
 * carried as old debt, and every branch merging main after it would have been.
 *
 * Usage: merge-gate.mjs <report against HEAD> <report against MERGE_HEAD>
 * Both are `check.mjs --json` reports of the same staged backlog. Exit 1 when
 * an error is new against both, 2 on misuse.
 */

import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

const key = (rule, v) => `${rule}|${v.id}|${v.ref || ''}`

export function newInBoth(againstHead, againstMergeHead) {
  const other = new Set()
  for (const r of againstMergeHead.results || [])
    for (const v of r.violations) if (v.isNew) other.add(key(r.id, v))
  const out = []
  for (const r of againstHead.results || []) {
    if (r.severity !== 'error') continue
    for (const v of r.violations)
      if (v.isNew && other.has(key(r.id, v))) out.push({ rule: r.id, id: v.id, note: v.note })
  }
  return out
}

function main() {
  const [a, b] = process.argv.slice(2)
  if (!a || !b) {
    console.error('merge-gate.mjs <report against HEAD> <report against MERGE_HEAD>')
    return 2
  }
  let fresh
  try {
    fresh = newInBoth(JSON.parse(readFileSync(a, 'utf8')), JSON.parse(readFileSync(b, 'utf8')))
  } catch (e) {
    console.error(`merge-gate.mjs: ${e.message}`)
    return 2
  }
  for (const f of fresh)
    console.log(`NEW in the merge  ${f.rule}  ${f.id}${f.note ? `: ${f.note}` : ''}`)
  console.log(`merge: new errors against both parents: ${fresh.length}`)
  return fresh.length ? 1 : 0
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  process.exitCode = main()
