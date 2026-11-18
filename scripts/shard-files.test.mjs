import test from 'node:test'
import assert from 'node:assert/strict'
import { assertCoverage, planShards } from '../e2e/shard-files.mjs'
import { readFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { planChangedSpecRepeats } from './repeat-changed-specs.mjs'
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

test('repeat arithmetic uses a fixed fixture and respects the budget boundaries', () => {
  const shards = [
    { files: ['e2e/a.spec.ts'], seconds: 300 },
    { files: ['e2e/b.spec.ts'], seconds: 800 },
  ]
  const plan = planChangedSpecRepeats(shards)
  assert.deepEqual(
    plan.map((leg) => leg.repeatEach),
    [8, 3],
  )
  assert.match(plan[0].summary, /1 specs x 8 repeats/)
  assert.ok(plan.every((leg, index) => leg.repeatEach * shards[index].seconds <= 2400))
})

test('tiny positive durations cannot create more than the former ten repeats', () => {
  const plan = planChangedSpecRepeats([{ files: ['e2e/fast.spec.ts'], seconds: 0.009 }])
  assert.equal(plan[0].repeatEach, 10)
})

test('minimum repeats exceeding the budget refuse the plan with an actionable error', () => {
  assert.throws(
    () => planChangedSpecRepeats([{ files: ['e2e/slow.spec.ts'], seconds: 800.001 }]),
    /chromium-1.*minimum 3 repeats.*40.*reduce.*spec/i,
  )
})

test('mixed measured and unknown specs have positive estimates and only three repeats', () => {
  const measured = {
    seconds: { 'e2e/known.spec.ts': 20 },
    case_counts: { 'e2e/known.spec.ts': 2 },
  }
  const files = ['e2e/known.spec.ts', 'e2e/new.spec.ts', 'e2e/newer.spec.ts']
  const timing = planShards('chromium', files, measured, {}, 60)
  assert.equal(timing.durations['e2e/new.spec.ts'], 60)
  assert.equal(timing.estimates.length, 2)
  const plan = planChangedSpecRepeats(timing.shards, 2400, timing.estimates)
  assert.ok(plan.every((leg) => leg.repeatEach === 3))
  assert.ok(plan.every((leg) => /unknown.*60.*3 repeats/i.test(leg.summary)))
})

test('the actual PR 835 mixed selection is budgeted and its unknown timings are visible', () => {
  const files = JSON.parse(
    readFileSync(new URL('./fixtures/pr-835-changed-specs.json', import.meta.url), 'utf8'),
  )
  assert.equal(files.length, 32)
  const result = spawnSync(process.execPath, ['scripts/repeat-changed-specs.mjs', '--plan'], {
    encoding: 'utf8',
    env: { ...process.env, REPEAT_SPECS: files.join(' '), SHARD_NUMBER: '1' },
  })
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /unknown.*60/i)
  assert.match(result.stdout, /no measured spec duration/)
  const legs = [
    ...result.stdout.matchAll(/(\d+) specs x (\d+) repeats \(estimated ([\d.]+) min\)/g),
  ]
  assert.equal(legs.length, 2)
  assert.equal(
    legs.reduce((n, leg) => n + Number(leg[1]), 0),
    32,
  )
  assert.ok(legs.every((leg) => Number(leg[2]) === 3 && Number(leg[3]) <= 40))
})

test('the live chromium manifest produces a bounded plan or an explicit budget refusal', () => {
  const measured = JSON.parse(
    readFileSync(new URL('../e2e/shard-duration-seconds.json', import.meta.url), 'utf8'),
  ).projects.chromium
  const timing = planShards('chromium', Object.keys(measured.seconds), measured)
  if (timing.shards.some((shard) => shard.seconds * 3 > 2400)) {
    assert.throws(() => planChangedSpecRepeats(timing.shards), /minimum 3 repeats/)
  } else {
    const plan = planChangedSpecRepeats(timing.shards)
    assert.ok(
      plan.every(
        (leg, index) =>
          leg.repeatEach >= 3 &&
          leg.repeatEach <= 10 &&
          leg.repeatEach * timing.shards[index].seconds <= 2400,
      ),
    )
  }
})
