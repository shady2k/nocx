#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/connection-owners-baseline.json` from the current
 * renderer connection-owner output (nocx-oalo1.3.5).
 *
 * Usage: node lint-fixtures/update-connection-owners-baseline.mjs [--allow-growth]
 *        (also `npm run baseline:connection-update`, from frontend/)
 *
 * The baseline is today's list of renderer sites that decide a connection fact
 * — or decide one from an error's text — and every stage of nocx-oalo1 removes
 * one. So the ordinary direction is SHRINK: a site taken through
 * frontend/src/connection/, or deleted with its dead code, drops its entry and
 * this script rewrites the file without it. Existing reasons are preserved, so
 * a shrinking baseline is never a re-answer of a question already answered.
 *
 * GROWTH IS REFUSED unless --allow-growth is given, the same guard
 * .githooks/update-connection-owners-baseline.mjs and
 * update-dead-exports-baseline.mjs have. A new entry is a new second owner, and
 * the flag is the author saying so out loud in a diff somebody reviews; without
 * it, this script exits 1 and prints the sites it would have added. An added
 * entry starts with an empty reason, and connection-owners.mjs refuses an empty
 * one, so a growth cannot be committed without writing down why it may exist
 * and what removes it.
 */
import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  BASELINE_PATH,
  collectViolations,
  loadBaseline,
  violationKey,
} from './connection-owners.mjs'

/**
 * What the next baseline should be. Pure, so the test can ask the question
 * without writing the repository's file.
 *
 * `old` entries keep their reason; a violation not in `old` is growth. Growth
 * without `allowGrowth` throws, and the caller writes nothing.
 */
export function planBaselineUpdate({ violations, old, allowGrowth = false }) {
  const previous = new Map(old.map((entry) => [violationKey(entry), entry]))
  const next = new Map()
  const growth = []
  const dropped = []
  for (const v of violations) {
    const key = violationKey(v)
    if (next.has(key)) continue // the same site twice is one entry
    const before = previous.get(key)
    if (before) {
      next.set(key, { ...before, reason: before.reason ?? '' })
      continue
    }
    const entry = { rule: v.rule, file: v.file, text: v.text, reason: '' }
    next.set(key, entry)
    growth.push(entry)
  }
  for (const key of previous.keys()) {
    if (!next.has(key)) dropped.push(previous.get(key))
  }
  if (growth.length > 0 && !allowGrowth) {
    const named = growth.map((e) => `  NEW ${e.rule}: ${e.file}: ${e.text}`).join('\n')
    throw new Error(
      `REFUSING to update baseline: ${growth.length} site(s) are not in the existing one.\n` +
        `${named}\n` +
        'A new site that decides a connection fact is a new second owner (ADR-ba6xet1b). ' +
        'Take it through frontend/src/connection/, or pass --allow-growth and say why in its reason.',
    )
  }
  const entries = [...next.values()].sort((a, b) => violationKey(a).localeCompare(violationKey(b)))
  return { entries, growth, dropped }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const allowGrowth = process.argv.includes('--allow-growth')
  const old = loadBaseline()
  let plan
  try {
    plan = planBaselineUpdate({ violations: collectViolations(), old, allowGrowth })
  } catch (err) {
    console.error(err.message)
    process.exit(1)
  }
  for (const entry of plan.dropped) {
    console.log(`dropped: ${entry.rule}: ${entry.file}: ${entry.text}`)
  }
  const content = JSON.stringify(
    {
      '//': [
        "DO NOT EDIT MANUALLY except for a new entry's reason. Regenerate with",
        '`node lint-fixtures/update-connection-owners-baseline.mjs`.',
        '',
        'Every entry is a renderer site that decides a fact about an SSH connection: it',
        'matches an error message or a refusal reason string, subscribes to a connection',
        'notification, opens or reconnects a session, or mints a lost session. ADR-ba6xet1b',
        'says one owner exists — the backend connection manager behind the Carrier',
        'interface — and the stages of nocx-oalo1 remove these. The list may only SHRINK;',
        'the updater refuses growth without --allow-growth, and an entry with no reason',
        'fails the check.',
        '',
        "Keyed on rule + file + the site's own source text, not a line number, so an",
        'unrelated edit above a baselined site does not make this file churn — the tradeoff',
        '.githooks/check-connection-owners.mjs makes for its own baseline. A site that is',
        'reformatted, or a file moved, is a new key and its reason has to be carried over',
        'by hand.',
      ],
      violations: plan.entries,
    },
    null,
    2,
  )
  writeFileSync(BASELINE_PATH, content + '\n')
  const unreasoned = plan.entries.filter((e) => e.reason.trim() === '').length
  console.log(
    `Baseline written: ${plan.entries.length} site(s), ${plan.growth.length} added, ` +
      `${plan.dropped.length} dropped, ${unreasoned} need a reason.`,
  )
  if (unreasoned > 0) {
    console.log(
      "Write each new entry's reason in lint-fixtures/connection-owners-baseline.json: what it " +
        'is and which stage or bead removes it. connection-owners.mjs refuses an empty one.',
    )
  }
}
