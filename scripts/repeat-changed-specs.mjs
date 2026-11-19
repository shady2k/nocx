#!/usr/bin/env node
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { appendFileSync, readFileSync } from 'node:fs'
import { collectReport } from './refresh-e2e-shard-durations.mjs'

export const REPEAT_BUDGET_SECONDS = 40 * 60
export const MAX_REPEAT_EACH = 10

export function planChangedSpecRepeats(files, report) {
  const durations = new Map()
  const withoutTiming = new Map()
  for (const { file, measuredDuration, hasTiming } of collectReport(report, 'chromium').values()) {
    durations.set(file, (durations.get(file) ?? 0) + measuredDuration)
    if (!hasTiming) withoutTiming.set(file, (withoutTiming.get(file) ?? 0) + 1)
  }
  const selected = [...new Set(files)].filter((file) => durations.has(file))
  const seconds = selected.reduce((sum, file) => sum + durations.get(file), 0)
  const repeatEach = !selected.length
    ? 0
    : seconds === 0
      ? MAX_REPEAT_EACH
      : Math.min(MAX_REPEAT_EACH, Math.floor(REPEAT_BUDGET_SECONDS / seconds))
  return {
    files: selected,
    notCollected: [...new Set(files)].filter((file) => !durations.has(file)),
    durations,
    seconds,
    testsWithoutTiming: selected.reduce((sum, file) => sum + (withoutTiming.get(file) ?? 0), 0),
    repeatEach,
  }
}

export function runChangedSpecRepeats(
  files,
  report,
  { shardNumber, planOnly = false, summaryPath, log = console.log, spawn = spawnSync } = {},
) {
  const plan = planChangedSpecRepeats(files, report)
  const { repeatEach, seconds } = plan
  const probability = Math.round((1 - 0.9 ** repeatEach) * 100)
  const lines = [
    `Repeat plan chromium-${shardNumber}: ${plan.files.length} specs x ${repeatEach} repeats; measured ${seconds}s; flake 1 in 10 detection probability 1−0.9^${repeatEach} = ${probability}%`,
    ...plan.files.map(
      (file) => `${file}: measured ${plan.durations.get(file)}s; x ${repeatEach} repeats`,
    ),
    ...plan.notCollected.map(
      (file) => `${file}: not collected on chromium-${shardNumber}; not repeated on this leg`,
    ),
  ]
  if (plan.testsWithoutTiming) lines.push(`${plan.testsWithoutTiming} tests without timing`)
  if (plan.files.length && repeatEach === 0) {
    lines.push(
      `Repeat skipped: measured ${seconds}s exceeds the ${REPEAT_BUDGET_SECONDS}s repeat budget; not repeated: ${plan.files.join(', ')}`,
    )
  }
  for (const line of lines) log(line)
  if (plan.files.length && repeatEach < MAX_REPEAT_EACH) {
    log(`::warning::${lines.join('; ')}`)
  }
  if (summaryPath) appendFileSync(summaryPath, `${lines.join('  \n')}\n`)
  if (!plan.files.length || repeatEach === 0 || planOnly) return 0
  const result = spawn(
    'node',
    [
      'scripts/e2e-disk-watch.mjs',
      '--state',
      `${process.env.RUNNER_TEMP ?? tmpdir()}/nocx-disk-watch-repeat.json`,
      '--',
      'e2e/run-in-container.sh',
      ...plan.files,
      `--repeat-each=${repeatEach}`,
      '--pass-with-no-tests',
      '--output=test-results/repeat-changed',
    ],
    { stdio: 'inherit' },
  )
  if (result.error) throw result.error
  return result.status ?? 1
}

function run() {
  const files = (process.env.REPEAT_SPECS ?? '').trim().split(/\s+/).filter(Boolean)
  const shardNumber = Number(process.env.SHARD_NUMBER)
  if (![1, 2].includes(shardNumber)) {
    throw new Error(`SHARD_NUMBER must be 1 or 2, received ${shardNumber}`)
  }
  const reportPath = `test-results/playwright-chromium-${shardNumber}.json`
  try {
    const report = JSON.parse(readFileSync(reportPath, 'utf8'))
    process.exitCode = runChangedSpecRepeats(files, report, {
      shardNumber,
      planOnly: process.argv.includes('--plan'),
      summaryPath: process.env.GITHUB_STEP_SUMMARY,
    })
  } catch (error) {
    throw new Error(`${reportPath}: ${error.message}`)
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
