#!/usr/bin/env node
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export function collectReport(report, project) {
  if (!['chromium', 'webkit'].includes(project)) throw new Error(`unknown project: ${project}`)
  if (!Array.isArray(report?.suites)) throw new Error('Playwright JSON report has no suites array')
  const cases = new Map()
  const visit = (suites, parentFile = '') => {
    for (const suite of suites ?? []) {
      const suiteFile = suite.file || parentFile
      for (const spec of suite.specs ?? []) {
        const specFile = spec.file || suiteFile
        if (!specFile) throw new Error(`Playwright report has a test without a file: ${spec.title}`)
        for (const test of spec.tests ?? []) {
          if (test.projectName !== project) continue
          const id = spec.id
          if (!id) throw new Error(`Playwright report has a test without an id: ${spec.title}`)
          if (cases.has(id)) throw new Error(`duplicate Playwright test id in reports: ${id}`)
          let duration = 0
          let measuredDuration = 0
          let hasTiming = false
          for (const result of test.results ?? []) {
            if (!Number.isFinite(result.duration)) continue
            duration += result.duration / 1000
            if (result.status !== 'skipped') {
              measuredDuration += result.duration / 1000
              hasTiming = true
            }
          }
          cases.set(id, {
            file: `e2e/${specFile.replaceAll('\\', '/')}`,
            duration,
            measuredDuration,
            hasTiming,
          })
        }
      }
      visit(suite.suites, suiteFile)
    }
  }
  visit(report.suites)
  return cases
}

export function buildDurationManifest(reports, runId = null) {
  const projects = {}
  for (const project of ['chromium', 'webkit']) {
    const inputs = reports.filter((item) => item.project === project)
    if (!inputs.length) throw new Error(`no Playwright JSON reports provided for ${project}`)
    const cases = new Map()
    for (const input of inputs) {
      for (const [id, entry] of collectReport(input.report, project)) {
        if (cases.has(id)) throw new Error(`test ${id} occurs in more than one ${project} report`)
        cases.set(id, entry)
      }
    }
    const seconds = {}
    const case_counts = {}
    for (const { file, duration } of cases.values()) {
      seconds[file] = (seconds[file] ?? 0) + duration
      case_counts[file] = (case_counts[file] ?? 0) + 1
    }
    for (const file of Object.keys(seconds)) {
      seconds[file] = Math.round(seconds[file] * 10) / 10
    }
    projects[project] = { seconds, case_counts }
  }
  return {
    source:
      'Playwright-reported test durations from the source run; unreported and skipped cases count toward case_counts with zero duration.',
    source_runs: runId ? [{ run_id: runId }] : [],
    projects,
  }
}

function main(args) {
  const reports = []
  let runId = null
  let output = 'e2e/shard-duration-seconds.json'
  for (let i = 0; i < args.length; i += 1) {
    if (args[i] === '--report') {
      const value = args[++i]
      const split = value?.indexOf('=') ?? -1
      if (split < 0) throw new Error('--report expects chromium=<path> or webkit=<path>')
      const project = value.slice(0, split)
      const path = resolve(value.slice(split + 1))
      reports.push({ project, report: JSON.parse(readFileSync(path, 'utf8')) })
    } else if (args[i] === '--run-id') {
      runId = args[++i]
      if (!runId) throw new Error('--run-id requires a value')
    } else if (args[i] === '--output') {
      output = resolve(args[++i] || '')
    } else if (args[i] === '--help') {
      console.log(
        'Usage: node scripts/refresh-e2e-shard-durations.mjs --run-id <id> --report chromium=<json> --report chromium=<json> --report webkit=<json> --report webkit=<json> [--output e2e/shard-duration-seconds.json]',
      )
      return
    } else {
      throw new Error(`unknown argument: ${args[i]}`)
    }
  }
  const manifest = buildDurationManifest(reports, runId)
  writeFileSync(output, `${JSON.stringify(manifest, null, 2)}\n`)
  console.log(
    `Wrote measured shard durations to ${output} (${Object.entries(manifest.projects)
      .map(
        ([project, data]) =>
          `${project}: ${Object.keys(data.seconds).length} files, ${Object.values(data.case_counts).reduce((a, b) => a + b, 0)} cases`,
      )
      .join('; ')})`,
  )
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  try {
    main(process.argv.slice(2))
  } catch (error) {
    console.error(error instanceof Error ? error.message : error)
    process.exitCode = 1
  }
}
