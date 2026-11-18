#!/usr/bin/env node
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { readFileSync } from 'node:fs'
import { planShards } from '../e2e/shard-files.mjs'

export const REPEAT_BUDGET_SECONDS = 40 * 60
export const MIN_REPEAT_EACH = 3
// Preserve the former ten-repeat ceiling: tiny recorded runtimes do not include
// Playwright's test expansion, reporting and per-repeat setup costs.
export const MAX_REPEAT_EACH = 10
// No listing is available on this path. Reserve one minute per unknown spec for
// balancing, and run only the minimum on any leg containing one. This is an
// explicit scheduling allowance, not a claim to have measured the spec.
export const UNKNOWN_SPEC_SECONDS = 60

export function planChangedSpecRepeats(
  shards,
  budgetSeconds = REPEAT_BUDGET_SECONDS,
  estimates = [],
) {
  return shards.map((shard, index) => {
    if (shard.seconds * MIN_REPEAT_EACH > budgetSeconds) {
      throw new Error(
        `chromium-${index + 1}: minimum ${MIN_REPEAT_EACH} repeats need ${((shard.seconds * MIN_REPEAT_EACH) / 60).toFixed(1)} min, exceeding the ${budgetSeconds / 60} min budget; reduce the changed-spec selection or change the shard layout`,
      )
    }
    const unknown = estimates.filter(({ file }) => shard.files.includes(file))
    const repeatEach =
      unknown.length || shard.seconds === 0
        ? MIN_REPEAT_EACH
        : Math.min(MAX_REPEAT_EACH, Math.floor(budgetSeconds / shard.seconds))
    const uncertainty = unknown.length
      ? `; ${unknown.length} unknown timings at ${UNKNOWN_SPEC_SECONDS}s/spec allowance; limited to ${MIN_REPEAT_EACH} repeats (not measured)`
      : ''
    return {
      shard: index + 1,
      files: shard.files,
      repeatEach,
      summary: `${shard.files.length} specs x ${repeatEach} repeats (estimated ${((shard.seconds * repeatEach) / 60).toFixed(1)} min)${uncertainty}`,
    }
  })
}

function run() {
  const files = (process.env.REPEAT_SPECS ?? '').trim().split(/\s+/).filter(Boolean)
  const shardNumber = Number(process.env.SHARD_NUMBER)
  const manifest = JSON.parse(
    readFileSync(new URL('../e2e/shard-duration-seconds.json', import.meta.url), 'utf8'),
  )
  const timing = planShards('chromium', files, manifest.projects.chromium, {}, UNKNOWN_SPEC_SECONDS)
  const repeats = planChangedSpecRepeats(timing.shards, REPEAT_BUDGET_SECONDS, timing.estimates)
  for (const { file, seconds, reason } of timing.estimates) {
    console.log(`Timing allowance: ${file}: ${seconds}s (${reason}; unknown current case count)`)
  }
  for (const leg of repeats) {
    console.log(`Repeat plan chromium-${leg.shard}: ${leg.summary}; ${leg.files.join(' ')}`)
  }
  const selected = repeats[shardNumber - 1]
  if (!selected) throw new Error(`SHARD_NUMBER must be 1 or 2, received ${shardNumber}`)
  if (selected.files.length && !process.argv.includes('--plan')) {
    const result = spawnSync(
      'node',
      [
        'scripts/e2e-disk-watch.mjs',
        '--state',
        `${process.env.RUNNER_TEMP ?? tmpdir()}/nocx-disk-watch-repeat.json`,
        '--',
        'e2e/run-in-container.sh',
        ...selected.files,
        `--repeat-each=${selected.repeatEach}`,
        '--pass-with-no-tests',
        '--output=test-results/repeat-changed',
      ],
      { stdio: 'inherit' },
    )
    process.exitCode = result.status ?? 1
  }
}
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  try {
    run()
  } catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
