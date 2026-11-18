#!/usr/bin/env node
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..')
const manifest = JSON.parse(readFileSync(join(here, 'shard-duration-seconds.json'), 'utf8'))

export function planShards(project, files, measured, caseCounts = {}) {
  if (!['chromium', 'webkit'].includes(project))
    throw new Error(`unknown browser project: ${project}`)
  if (new Set(files).size !== files.length)
    throw new Error('duplicate spec file in collected tests')
  const priorCases = measured.case_counts ?? {}
  const totalPriorCases = Object.values(priorCases).reduce((total, count) => total + count, 0)
  const meanSecondsPerCase = totalPriorCases
    ? Object.values(measured.seconds).reduce((total, seconds) => total + seconds, 0) /
      totalPriorCases
    : 0
  const estimates = []
  const durations = {}
  for (const file of files) {
    const currentCases = caseCounts[file] ?? 0
    const oldSeconds = measured.seconds[file]
    const oldCases = priorCases[file] ?? 0
    if (oldSeconds > 0 && oldCases > 0 && currentCases === oldCases) {
      durations[file] = oldSeconds
    } else if (oldSeconds === undefined || oldCases === 0) {
      durations[file] = currentCases * meanSecondsPerCase
      estimates.push({ file, seconds: durations[file], reason: 'no measured spec duration' })
    } else if (currentCases !== oldCases) {
      durations[file] = (oldSeconds * currentCases) / oldCases
      estimates.push({
        file,
        seconds: durations[file],
        reason: `case count changed ${oldCases} → ${currentCases}`,
      })
    } else {
      durations[file] = oldSeconds
    }
  }
  const shards = [
    { files: [], seconds: 0, cases: 0 },
    { files: [], seconds: 0, cases: 0 },
  ]
  for (const file of [...files].sort((a, b) => durations[b] - durations[a] || a.localeCompare(b))) {
    const shard = shards[0].seconds <= shards[1].seconds ? shards[0] : shards[1]
    shard.files.push(file)
    shard.seconds += durations[file]
    shard.cases += caseCounts[file] ?? 0
  }
  return { project, shards, durations, estimates }
}

export function assertCoverage(files, expectedCases, shards) {
  const assignedFiles = shards.flatMap((shard) => shard.files)
  if (
    assignedFiles.length !== files.length ||
    new Set(assignedFiles).size !== files.length ||
    files.some((file) => !assignedFiles.includes(file))
  ) {
    throw new Error('shard lists do not cover each collected spec file exactly once')
  }
  if (shards.reduce((total, shard) => total + shard.cases, 0) !== expectedCases) {
    throw new Error('shard lists do not cover each collected test case exactly once')
  }
}

function collectSpecFiles(suites, parent = '') {
  const out = []
  for (const suite of suites ?? []) {
    const file = suite.file || parent
    if (suite.specs?.length) {
      for (const spec of suite.specs) {
        const specFile = spec.file || file
        if (!specFile) throw new Error(`Playwright listed a test without a file: ${spec.title}`)
        out.push({ id: spec.id, file: `e2e/${specFile}`, tests: spec.tests ?? [] })
      }
    }
    out.push(...collectSpecFiles(suite.suites, file))
  }
  return out
}

function playwrightListing(project) {
  const result = spawnSync(
    'npx',
    ['playwright', 'test', '--list', '--project', project, '--reporter=json'],
    {
      cwd: root,
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
    },
  )
  if (result.error) throw result.error
  if (result.status !== 0)
    throw new Error(`Playwright listing exited ${result.status}: ${result.stderr}`)
  // Playwright's JSON reporter writes JSON to stdout, while Node's test runner
  // summaries from test files may also appear there. Keep the complete JSON object.
  const start = result.stdout.indexOf('{')
  const end = result.stdout.lastIndexOf('}')
  if (start < 0 || end < start)
    throw new Error(`Playwright listing emitted no JSON: ${result.stdout}${result.stderr}`)
  const report = JSON.parse(result.stdout.slice(start, end + 1))
  if (report.errors?.length)
    throw new Error(`Playwright listing failed: ${JSON.stringify(report.errors)}`)
  return collectSpecFiles(report.suites)
}

function run(project, shardNumber, planOnly = false) {
  const specs = playwrightListing(project)
  const files = [...new Set(specs.map((spec) => spec.file))].sort()
  const counts = Object.fromEntries(
    files.map((file) => [file, specs.filter((spec) => spec.file === file).length]),
  )
  const projectManifest = manifest.projects[project]
  if (!projectManifest) throw new Error(`no measured timings for ${project}`)
  const plan = planShards(project, files, projectManifest, counts)
  const expectedCases = specs.length
  assertCoverage(files, expectedCases, plan.shards)
  const selected = plan.shards[shardNumber - 1]
  if (!selected) throw new Error(`shard must be 1 or 2, received ${shardNumber}`)
  const sourceRuns = manifest.source_runs.map((run) => run.run_id).join(', ')
  console.log(
    `Duration plan (${sourceRuns}): ${plan.shards.map((shard, index) => `${index + 1}=${shard.cases} tests/${(shard.seconds / 60).toFixed(1)} estimated min`).join(', ')}`,
  )
  if (plan.estimates.length) {
    const names = plan.estimates.map(({ file, reason }) => `${file} (${reason})`).join(', ')
    console.log(`Timing estimates for unmeasured or changed files: ${names}`)
  }
  console.log(
    `Coverage verified: ${plan.shards.reduce((n, shard) => n + shard.cases, 0)} test cases across ${files.length} spec files; each assigned once.`,
  )
  if (planOnly) return
  const reportFile = join(root, 'test-results', `playwright-${project}-${shardNumber}.json`)
  const result = spawnSync(
    'npx',
    [
      'playwright',
      'test',
      ...selected.files.slice().sort(),
      '--project',
      project,
      '--reporter=line,json',
    ],
    {
      cwd: root,
      stdio: 'inherit',
      env: { ...process.env, PLAYWRIGHT_JSON_OUTPUT_NAME: reportFile },
    },
  )
  if (result.error) throw result.error
  process.exitCode = result.status ?? 1
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const args = process.argv.slice(2)
  const planOnly = args.includes('--plan')
  const actualArgs = args.filter((arg) => arg !== '--plan')
  const projectIndex = actualArgs.indexOf('--project')
  const shardIndex = actualArgs.indexOf('--shard')
  if (projectIndex < 0 || shardIndex < 0 || actualArgs.length !== 4) {
    console.error('Usage: node e2e/shard-files.mjs --project chromium|webkit --shard 1|2 [--plan]')
    process.exitCode = 2
  } else {
    try {
      run(actualArgs[projectIndex + 1], Number(actualArgs[shardIndex + 1]), planOnly)
    } catch (error) {
      console.error(error instanceof Error ? error.message : error)
      process.exitCode = 1
    }
  }
}
