#!/usr/bin/env node
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { planFilesAcrossShards } from '../e2e/shard-files.mjs'

export const REPEAT_BUDGET_SECONDS = 40 * 60
export const MIN_REPEAT_EACH = 3

export function planChangedSpecRepeats(shards, budgetSeconds = REPEAT_BUDGET_SECONDS) {
  return shards.map((shard, index) => {
    const repeatEach = Math.max(
      MIN_REPEAT_EACH,
      shard.seconds > 0 ? Math.floor(budgetSeconds / shard.seconds) : MIN_REPEAT_EACH,
    )
    return {
      shard: index + 1,
      files: shard.files,
      repeatEach,
      summary: `${shard.files.length} specs x ${repeatEach} repeats (estimated ${(shard.seconds * repeatEach / 60).toFixed(1)} min)`,
    }
  })
}

function run() {
  const files = (process.env.REPEAT_SPECS ?? '').trim().split(/\s+/).filter(Boolean)
  const shardNumber = Number(process.env.SHARD_NUMBER)
  const manifest = JSON.parse(readFileSync(new URL('../e2e/shard-duration-seconds.json', import.meta.url), 'utf8'))
  const shards = planFilesAcrossShards('chromium', files, manifest.projects.chromium)
  const repeats = planChangedSpecRepeats(shards)
  for (const leg of repeats) {
    console.log(`Repeat plan chromium-${leg.shard}: ${leg.summary}; ${leg.files.join(' ')}`)
  }
  const selected = repeats[shardNumber - 1]
  if (!selected) throw new Error(`SHARD_NUMBER must be 1 or 2, received ${shardNumber}`)
  if (selected.files.length && process.env.REPEAT_PLAN_ONLY !== '1') {
    const result = spawnSync(
      'node',
      [
        'scripts/e2e-disk-watch.mjs',
        '--state',
        `${process.env.RUNNER_TEMP}/nocx-disk-watch-repeat.json`,
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
if (process.argv[1] && new URL(import.meta.url).pathname === process.argv[1]) {
  try {
    run()
  } catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
