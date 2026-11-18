import test from 'node:test'
import assert from 'node:assert/strict'
import { assertCoverage, planShards } from '../e2e/shard-files.mjs'
import { buildDurationManifest, collectReport } from './refresh-e2e-shard-durations.mjs'

test('duration-balanced shard plan assigns each measured file exactly once', () => {
  const files = ['e2e/a.spec.ts', 'e2e/b.spec.ts', 'e2e/c.spec.ts', 'e2e/d.spec.ts']
  const measured = {
    seconds: {
      'e2e/a.spec.ts': 100,
      'e2e/b.spec.ts': 80,
      'e2e/c.spec.ts': 20,
      'e2e/d.spec.ts': 10,
    },
    case_counts: {
      'e2e/a.spec.ts': 10,
      'e2e/b.spec.ts': 8,
      'e2e/c.spec.ts': 2,
      'e2e/d.spec.ts': 1,
    },
  }
  const caseCounts = {
    'e2e/a.spec.ts': 10,
    'e2e/b.spec.ts': 8,
    'e2e/c.spec.ts': 2,
    'e2e/d.spec.ts': 1,
  }
  const plan = planShards('chromium', files, measured, caseCounts)

  assertCoverage(files, 21, plan.shards)
  assert.equal(new Set(plan.shards.flatMap((shard) => shard.files)).size, files.length)
  assert.ok(Math.abs(plan.shards[0].seconds - plan.shards[1].seconds) <= 20)
})

test('unmeasured and changed specs use estimates while removed manifest specs are ignored', () => {
  const files = ['e2e/new.spec.ts', 'e2e/changed.spec.ts', 'e2e/kept.spec.ts']
  const measured = {
    seconds: {
      'e2e/changed.spec.ts': 20,
      'e2e/kept.spec.ts': 10,
      'e2e/removed.spec.ts': 50,
    },
    case_counts: {
      'e2e/changed.spec.ts': 4,
      'e2e/kept.spec.ts': 2,
      'e2e/removed.spec.ts': 10,
    },
  }
  const caseCounts = { 'e2e/new.spec.ts': 3, 'e2e/changed.spec.ts': 6, 'e2e/kept.spec.ts': 2 }
  const plan = planShards('chromium', files, measured, caseCounts)
  assert.equal(plan.durations['e2e/new.spec.ts'], 15)
  assert.equal(plan.durations['e2e/changed.spec.ts'], 30)
  assert.deepEqual(plan.estimates.map(({ file }) => file).sort(), [
    'e2e/changed.spec.ts',
    'e2e/new.spec.ts',
  ])
  assertCoverage(files, 11, plan.shards)
})

test('the exact-coverage assertion rejects a duplicate assignment', () => {
  const files = ['e2e/a.spec.ts', 'e2e/b.spec.ts']
  const shards = [
    { files: ['e2e/a.spec.ts'], cases: 1 },
    { files: ['e2e/a.spec.ts', 'e2e/b.spec.ts'], cases: 2 },
  ]
  assert.throws(() => assertCoverage(files, 3, shards), /spec file exactly once/)
})

test('duration-balanced shard plan is deterministic', () => {
  const files = ['e2e/a.spec.ts', 'e2e/b.spec.ts', 'e2e/c.spec.ts']
  const measured = {
    seconds: { 'e2e/a.spec.ts': 15, 'e2e/b.spec.ts': 10, 'e2e/c.spec.ts': 5 },
    case_counts: { 'e2e/a.spec.ts': 3, 'e2e/b.spec.ts': 2, 'e2e/c.spec.ts': 1 },
  }
  assert.deepEqual(planShards('chromium', files, measured), planShards('chromium', files, measured))
})

test('refresh script rebuilds per-browser durations from Playwright JSON reports', () => {
  const makeReport = (project, base, duration) => ({
    suites: [
      {
        file: `${base}.spec.ts`,
        specs: [
          {
            id: `${project}-${base}`,
            file: `${base}.spec.ts`,
            tests: [{ projectName: project, results: [{ duration }] }],
          },
        ],
      },
    ],
  })
  const manifest = buildDurationManifest(
    [
      { project: 'chromium', report: makeReport('chromium', 'a', 12500) },
      { project: 'chromium', report: makeReport('chromium', 'b', 500) },
      { project: 'webkit', report: makeReport('webkit', 'a', 22000) },
      { project: 'webkit', report: makeReport('webkit', 'b', 4000) },
    ],
    'run-123',
  )
  assert.equal(manifest.projects.chromium.seconds['e2e/a.spec.ts'], 12.5)
  assert.equal(manifest.projects.chromium.case_counts['e2e/a.spec.ts'], 1)
  assert.equal(manifest.projects.chromium.seconds['e2e/b.spec.ts'], 0.5)
  assert.equal(manifest.projects.webkit.seconds['e2e/a.spec.ts'], 22)
  assert.equal(manifest.projects.webkit.seconds['e2e/b.spec.ts'], 4)
  assert.equal(manifest.source_runs[0].run_id, 'run-123')
})

test('refresh script tolerates JSON report files whose result has not run yet', () => {
  const report = {
    suites: [
      {
        file: 'not-run.spec.ts',
        specs: [
          {
            id: 'not-run',
            file: 'not-run.spec.ts',
            tests: [{ projectName: 'chromium', results: [] }],
          },
        ],
      },
    ],
  }
  const collected = collectReport(report, 'chromium')
  assert.equal(collected.get('not-run').duration, 0)
})
