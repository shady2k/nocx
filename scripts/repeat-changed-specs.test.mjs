import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { planChangedSpecRepeats, runChangedSpecRepeats } from './repeat-changed-specs.mjs'
import { buildDurationManifest } from './refresh-e2e-shard-durations.mjs'

// Fixed Playwright-shaped fixtures: nested suites, multiple cases and retries.
const report = (seconds) => ({
  suites: [
    {
      file: 'a.spec.ts',
      suites: [
        {
          specs: [
            {
              id: 'a',
              tests: [
                { projectName: 'chromium', results: seconds.map((s) => ({ duration: s * 1000 })) },
              ],
            },
          ],
        },
      ],
    },
  ],
})
const changed = ['e2e/a.spec.ts']

// A serial group stops after its first failure; remaining tests may be skipped
// or have no result at all. A skipped result is not a duration measurement.
const serialReport = () => ({
  suites: [
    {
      file: 'a.spec.ts',
      suites: [
        {
          title: 'serial group',
          specs: [
            {
              id: 'failed',
              tests: [
                { projectName: 'chromium', results: [{ status: 'failed', duration: 30000 }] },
              ],
            },
            {
              id: 'skipped',
              tests: [{ projectName: 'chromium', results: [{ status: 'skipped', duration: 0 }] }],
            },
            { id: 'not-run', tests: [{ projectName: 'chromium', results: [] }] },
          ],
        },
      ],
    },
  ],
})

test('serial-group tests without timing are reported in the plan and summary without a warning', (t) => {
  const fixture = serialReport()
  const plan = planChangedSpecRepeats(changed, fixture)
  assert.equal(plan.seconds, 30)
  assert.equal(plan.testsWithoutTiming, 2)
  assert.equal(plan.repeatEach, 10)
  const result = invoke(t, fixture, changed, true)
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /2 tests without timing/)
  assert.match(result.summary, /2 tests without timing/)
  assert.doesNotMatch(result.stdout, /::warning::/)
})

test('skipped results never add to repeat time and unselected tests do not add missing timing', () => {
  const fixture = serialReport()
  fixture.suites[0].suites[0].specs[1].tests[0].results[0].duration = 900000
  fixture.suites.push({
    file: 'unchanged.spec.ts',
    specs: [{ id: 'unchanged', tests: [{ projectName: 'chromium', results: [] }] }],
  })
  const plan = planChangedSpecRepeats(changed, fixture)
  assert.equal(plan.seconds, 30)
  assert.equal(plan.testsWithoutTiming, 2)
})

test('the manifest still counts all serial-group cases, including skipped and unreported cases', () => {
  const inputs = ['chromium', 'webkit'].map((project) => {
    const fixture = serialReport()
    for (const spec of fixture.suites[0].suites[0].specs) {
      spec.tests[0].projectName = project
    }
    return { project, report: fixture }
  })
  const manifest = buildDurationManifest(inputs)
  for (const project of ['chromium', 'webkit']) {
    assert.deepEqual(manifest.projects[project], {
      seconds: { 'e2e/a.spec.ts': 30 },
      case_counts: { 'e2e/a.spec.ts': 3 },
    })
  }
})

test('only unmeasured selected tests still get ten repeats', () => {
  const fixture = serialReport()
  fixture.suites[0].suites[0].specs.shift()
  const plan = planChangedSpecRepeats(changed, fixture)
  assert.equal(plan.seconds, 0)
  assert.equal(plan.repeatEach, 10)
  assert.equal(plan.testsWithoutTiming, 2)
})

test('measured repeat counts at and around budget boundaries, including the ten-repeat ceiling', () => {
  for (const [seconds, expected] of [
    [0, 10],
    [0.009, 10],
    [30, 10],
    [240, 10],
    [240.001, 9],
    [400, 6],
    [400.001, 5],
    [720, 3],
    [800, 3],
    [800.001, 2],
    [2400, 1],
    [2400.001, 0],
    [2500, 0],
  ]) {
    const plan = planChangedSpecRepeats(changed, report([seconds]))
    assert.equal(plan.repeatEach, expected, `${seconds}s`)
    assert.equal(plan.seconds, seconds)
  }
})

test('the leg budget sums every case across all its changed specs', () => {
  const fixture = report([100, 200])
  fixture.suites[0].suites[0].specs.push({
    id: 'a2',
    tests: [{ projectName: 'chromium', results: [{ duration: 100000 }] }],
  })
  fixture.suites.push({
    file: 'b.spec.ts',
    specs: [{ id: 'b', tests: [{ projectName: 'chromium', results: [{ duration: 400000 }] }] }],
  })
  const plan = planChangedSpecRepeats([...changed, 'e2e/b.spec.ts'], fixture)
  assert.deepEqual(plan.files, ['e2e/a.spec.ts', 'e2e/b.spec.ts'])
  assert.equal(plan.seconds, 800)
  assert.equal(plan.repeatEach, 3)
})

test('only this leg owns its reported changed specs and all retry results count', () => {
  const fixture = report([100, 300])
  fixture.suites.push({
    file: 'unchanged.spec.ts',
    specs: [
      { id: 'unchanged', tests: [{ projectName: 'chromium', results: [{ duration: 9999999 }] }] },
    ],
  })
  const plan = planChangedSpecRepeats(
    [...changed, 'e2e/other-leg.spec.ts', 'e2e/ignored.spec.ts'],
    fixture,
  )
  assert.deepEqual(plan.files, changed)
  assert.deepEqual(plan.notCollected, ['e2e/other-leg.spec.ts', 'e2e/ignored.spec.ts'])
  assert.equal(plan.seconds, 400)
  assert.equal(plan.repeatEach, 6)
})

function invoke(t, fixture, specs = changed, planOnly = false) {
  const cwd = mkdtempSync(join(tmpdir(), 'nocx-repeat-'))
  t.after(() => rmSync(cwd, { recursive: true, force: true }))
  mkdirSync(join(cwd, 'test-results'))
  const path = join(cwd, 'test-results/playwright-chromium-2.json')
  if (fixture !== undefined)
    writeFileSync(path, typeof fixture === 'string' ? fixture : JSON.stringify(fixture))
  const summary = join(cwd, 'summary.md')
  const result = spawnSync(
    process.execPath,
    [resolve('scripts/repeat-changed-specs.mjs'), ...(planOnly ? ['--plan'] : [])],
    {
      cwd,
      encoding: 'utf8',
      env: {
        ...process.env,
        REPEAT_SPECS: specs.join(' '),
        SHARD_NUMBER: '2',
        GITHUB_STEP_SUMMARY: summary,
      },
    },
  )
  return { ...result, path, summary: result.status === 0 ? readFileSync(summary, 'utf8') : '' }
}

test('small PR still gets ten repeats without a warning', (t) => {
  const result = invoke(t, report([30]), changed, true)
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /1 specs x 10 repeats/)
  assert.match(result.stdout, /a.spec.ts.*30.*10/)
  assert.doesNotMatch(result.stdout, /::warning::/)
  assert.match(result.summary, /a.spec.ts/)
})

test('reduced sensitivity is a PR warning and summary with probability and each spec', (t) => {
  const result = invoke(t, report([720]), changed, true)
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /::warning::.*27%/)
  assert.match(result.summary, /1 specs x 3 repeats/)
  assert.match(result.summary, /1.*0[.,]9.*27%/)
  assert.match(result.summary, /a.spec.ts.*720.*3/)
})

test('over-budget repeat is green, explicitly skipped and never launches Playwright', (t) => {
  const result = invoke(t, report([2500]))
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /::warning::/)
  assert.match(result.summary, /a.spec.ts/)
  assert.match(result.summary, /skipped.*2500.*2400/i)
  assert.match(result.summary, /0 repeats/)
})

test('empty intersection is recorded as not collected on this leg without a launch', (t) => {
  const result = invoke(t, report([30]), ['e2e/ignored.spec.ts'])
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /ignored.spec.ts.*not collected/)
  assert.match(result.summary, /0 specs x 0 repeats/)
})

test('missing, malformed and invalid reports fail with the report path', (t) => {
  for (const fixture of [undefined, '{broken', {}]) {
    const result = invoke(t, fixture)
    assert.equal(result.status, 1)
    assert.ok(result.stderr.includes('test-results/playwright-chromium-2.json'), result.stderr)
  }
})

test('the execution seam preserves disk watch and forwards the measured selection and repeat failure', () => {
  let call
  const status = runChangedSpecRepeats(changed, report([30]), {
    shardNumber: 1,
    log: () => {},
    spawn: (...args) => {
      call = args
      return { status: 7 }
    },
  })
  assert.equal(status, 7)
  assert.equal(call[0], 'node')
  assert.ok(call[1].includes('scripts/e2e-disk-watch.mjs'))
  assert.ok(call[1].includes('e2e/run-in-container.sh'))
  assert.ok(call[1].includes('--repeat-each=10'))
  assert.ok(call[1].includes('e2e/a.spec.ts'))
  assert.ok(call[1].includes('--output=test-results/repeat-changed'))
})
