#!/usr/bin/env node
/**
 * Regenerate `.githooks/connection-owners-baseline.json` from the current
 * connection-owner ratchet output (nocx-oalo1.3.4).
 *
 * Usage: node .githooks/update-connection-owners-baseline.mjs [--allow-growth]
 *
 * The baseline is today's list of sites that decide an SSH connection fact
 * outside the transport, and every stage of nocx-oalo1 removes one. So the
 * ordinary direction is SHRINK: a site taken through the transport, or
 * deleted with its dead code, drops its entry and this script rewrites the
 * file without it. A site that decides the same fact fewer times (a loop
 * unrolled, a second call deleted) keeps its entry with the new count.
 * Existing reasons are preserved, so a shrinking baseline is never a
 * re-answer of a question already answered.
 *
 * GROWTH IS REFUSED unless --allow-growth is given, the same guard
 * update-log-context-baseline.mjs has. Growth is a new key or a key that now
 * occurs more often — a second identical call in one function is a second
 * decision, and the count is what makes it visible. A new entry is a new
 * second owner, and the flag is the author saying so out loud in a diff
 * somebody reviews; without it, this script exits 1 and prints the sites it
 * would have added. An added entry starts with an empty reason, and
 * check-connection-owners.mjs refuses an empty one, so a growth cannot be
 * committed without writing down why it may exist and what removes it.
 *
 * Entries are keyed by rule + file + enclosing function + object, never by a
 * line, so an unrelated edit above a baselined site does not churn this file
 * and a second identical site cannot hide behind the first.
 */
import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  BASELINE_PATH,
  collectViolations,
  loadBaseline,
  violationKey,
} from './check-connection-owners.mjs'

/**
 * What the next baseline should be. Pure, so the test can ask the question
 * without writing the repository's file.
 *
 * `old` entries keep their reason; a key not in `old`, or one whose count
 * grew, is growth. Growth without `allowGrowth` throws, and the caller writes
 * nothing.
 */
export function planBaselineUpdate({ violations, old, allowGrowth = false }) {
  const previous = new Map(old.map((entry) => [violationKey(entry), entry]))
  const next = []
  const growth = []
  const dropped = []
  const shrunk = []
  for (const row of violations) {
    const key = violationKey(row)
    const before = previous.get(key)
    if (!before) {
      const entry = {
        rule: row.rule,
        file: row.file,
        func: row.func,
        object: row.object,
        count: row.count,
        reason: '',
      }
      growth.push(entry)
      next.push(entry)
      continue
    }
    if (row.count > before.count) {
      const entry = { ...before, count: row.count }
      growth.push(entry)
      next.push(entry)
      continue
    }
    if (row.count < before.count) shrunk.push({ ...before, count: row.count })
    next.push({ ...before, count: row.count })
  }
  for (const key of previous.keys()) {
    if (!violations.some((row) => violationKey(row) === key)) dropped.push(previous.get(key))
  }
  if (growth.length > 0 && !allowGrowth) {
    const named = growth
      .map((e) => `  NEW ${e.rule}: ${e.file} ${e.func} → ${e.object} ×${e.count}`)
      .join('\n')
    throw new Error(
      `REFUSING to update baseline: ${growth.length} site(s) are not in the existing one, ` +
        'or decide their fact more often.\n' +
        `${named}\n` +
        'A new site outside the SSH transport is a new second owner of a connection fact. ' +
        'Take it through the transport, or pass --allow-growth and say why in its reason.',
    )
  }
  next.sort((a, b) => violationKey(a).localeCompare(violationKey(b)))
  return { entries: next, growth, dropped, shrunk }
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
    console.log(`dropped: ${violationKey(entry)}`)
  }
  for (const entry of plan.shrunk) {
    console.log(`shrunk:  ${violationKey(entry)} → ${entry.count} occurrence(s)`)
  }
  const content = JSON.stringify(
    {
      '//': [
        "DO NOT EDIT MANUALLY except for a new entry's reason. Regenerate with",
        '`node .githooks/update-connection-owners-baseline.mjs`.',
        '',
        'Every entry is a site OUTSIDE the SSH transport (internal/ssh,',
        'internal/helper/sshsvc, internal/sshtest) that decides a fact about an SSH',
        'connection: it names an x/crypto/ssh object, dials a network, reads an',
        'ExitError or sends a keepalive. ADR-ba6xet1b says one owner exists — the',
        'backend connection manager behind the Carrier interface — and the stages of',
        'nocx-oalo1 remove these. The list may only SHRINK or stay level; the updater',
        'refuses growth without --allow-growth, and an entry with no reason fails the',
        'check.',
        '',
        'Keyed on rule + file + enclosing function + the object referenced, never on a',
        'line, so an unrelated edit above a baselined site does not churn this file.',
        '',
        'transport-re-export is a SHAPE check: an exported package-level var holding a',
        'dial function, or an exported func whose whole body is one `return <dial call>`.',
        'A dial handed out any other way (through a field, a method, a channel, a',
        'multi-statement func) is not chased. The transport packages are the one owner of',
        'a connection, and what they export is reviewed with them; a shape heuristic here',
        'would grow without bound and still be outrun by the next spelling.',
        '`count` is how many times that function decides the fact: a second identical',
        'call is a second decision, and a count that GROWS fails the check until the',
        'baseline is updated deliberately. A count that SHRINKS is a stale licence and',
        'fails too, so running the updater is the only way this file moves.',
      ],
      violations: plan.entries,
    },
    null,
    2,
  )
  writeFileSync(BASELINE_PATH, content + '\n')
  const unreasoned = plan.entries.filter((e) => e.reason.trim() === '').length
  console.log(
    `Baseline written: ${plan.entries.length} site(s) outside the transport, ` +
      `${plan.growth.length} added or grown, ${plan.dropped.length} dropped, ` +
      `${plan.shrunk.length} shrunk, ${unreasoned} need a reason.`,
  )
  if (unreasoned > 0) {
    console.log(
      "Write each new entry's reason in .githooks/connection-owners-baseline.json: what it " +
        'is and which stage or bead removes it. check-connection-owners.mjs refuses an empty one.',
    )
  }
}
