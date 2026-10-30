import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, rmSync, unlinkSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { readQuarantineEvidence } from './release/promotion.mjs'
import { gate } from './quarantine-gate.mjs'
import {
  failedTests,
  goTestEvidence,
  playwrightEvidence,
  judgeFailures,
  readQuarantine,
  readQuarantineState,
  stepManifestDigest,
} from './quarantine.mjs'

// Real log lines (CI runs 40866954718 and 40893969284, 2026-10-29), with the
// timestamp prefix the jobs/logs endpoint adds and Playwright's trailing rule.
const goLog =
  '2026-10-29T08:51:12.1Z === RUN   TestZshAgentWrapperCancelsTheLaunchOnCtrlC\n' +
  '2026-10-29T08:51:23.8Z --- FAIL: TestZshAgentWrapperCancelsTheLaunchOnCtrlC (11.67s)\n' +
  '2026-10-29T08:51:23.8Z     --- FAIL: TestZshAgentWrapperCancelsTheLaunchOnCtrlC/sub (0.1s)\n' +
  '2026-10-29T08:51:24.0Z FAIL\tgithub.com/shady2k/nocx/internal/app\t300.1s\n'
const e2eLog =
  '2026-10-29T13:01:02.0Z   1) [webkit] › e2e/ask-attaches-the-frozen-screen.spec.ts:218:5 › the assistant is told the frozen screen came with the question (nocx-hp8p2.4) › summoning over a full-screen program on the alternate screen attaches its screen to the ask ─────────\n' +
  '2026-10-29T13:01:02.1Z     Error: fake-openai: timed out waiting for 2 request(s)\n'
const askKey =
  'e2e:e2e/ask-attaches-the-frozen-screen.spec.ts › the assistant is told the frozen screen came with the question (nocx-hp8p2.4) › summoning over a full-screen program on the alternate screen attaches its screen to the ask'

test('a failed job log names its failed tests in the quarantine key form, and nothing else', () => {
  assert.deepEqual(failedTests(goLog), ['go:TestZshAgentWrapperCancelsTheLaunchOnCtrlC'])
  assert.deepEqual(failedTests(e2eLog), [askKey])
  assert.deepEqual(failedTests('2026-10-29T00:00:00Z panic: test timed out after 10m0s\n'), [])
})

test('only an open bead quarantines; a closed one protects nothing; an unknown one refuses', () => {
  const issues = [
    { id: 'nocx-a', status: 'open' },
    { id: 'nocx-b', status: 'submitted' },
    { id: 'nocx-c', status: 'closed' },
  ]
    .map((row) => JSON.stringify(row))
    .join('\n')
  const doc = (tests) => JSON.stringify({ schema: 1, tests })
  assert.deepEqual(readQuarantine(undefined, issues), {})
  assert.deepEqual(
    readQuarantine(
      doc([
        { test: 'go:TestA', bead: 'nocx-a' },
        { test: askKey, bead: 'nocx-b' },
        { test: 'go:TestC', bead: 'nocx-c' },
      ]),
      issues,
    ),
    { 'go:TestA': 'nocx-a', [askKey]: 'nocx-b' },
  )
  // The closed entry is kept, not dropped: a red job must be able to say that
  // the file holds the test and its bead is closed, rather than that no bead
  // quarantines it.
  assert.deepEqual(
    readQuarantineState(doc([{ test: 'go:TestC', bead: 'nocx-c' }]), issues).closed,
    { 'go:TestC': 'nocx-c' },
  )
  assert.throws(() => readQuarantine(doc([{ test: 'go:TestA', bead: 'nocx-z' }]), issues), /exist/)
  assert.throws(() => readQuarantine(doc([{ test: 'TestA', bead: 'nocx-a' }]), issues), /entry/)
  assert.deepEqual(readQuarantine(doc([{ test: 'job:ci-backlog#557', bead: 'nocx-a' }]), issues), {
    'job:ci-backlog#557': 'nocx-a',
  })
  assert.throws(
    () => readQuarantine(doc([{ test: 'job:ci-backlog', bead: 'nocx-a' }]), issues),
    /entry/,
  )
  assert.throws(() => readQuarantine('{"schema":2,"tests":[]}', issues), /quarantine/)
})

test('the verdict holds a failure only while an open bead owns it, in both callers', () => {
  const quarantine = { 'go:TestA': 'nocx-a', 'job:ci-backend (no-secret-service)#12': 'nocx-b' }
  const held = judgeFailures({ failures: ['go:TestA'], complete: true, quarantine })
  assert.deepEqual(held.quarantined, [{ test: 'go:TestA', bead: 'nocx-a' }])
  assert.equal(held.accepted, true)
  const other = judgeFailures({ failures: ['go:TestA', 'go:TestB'], complete: true, quarantine })
  assert.equal(other.accepted, false)
  assert.match(other.reason, /failed go:TestB, which no open bead quarantines/)
  // A failure that names no test is never held by a test key, only by a retry
  // that classified it or by an entry for exactly this job of this PR.
  assert.equal(judgeFailures({ failures: [], quarantine }).accepted, false)
  assert.match(judgeFailures({ failures: [], quarantine }).reason, /never passed/)
  assert.equal(
    judgeFailures({ failures: [], quarantine, passedInAnotherAttempt: true }).accepted,
    true,
  )
  const jobKey = 'job:ci-backend (no-secret-service)#12'
  assert.equal(judgeFailures({ failures: [], quarantine, jobKey }).accepted, true)
  assert.equal(
    judgeFailures({ failures: [], quarantine, jobKey: 'job:ci-backend (no-secret-service)#13' })
      .accepted,
    false,
  )
})

// The gate's own fixtures: the files a gated step leaves behind.
function evidence(t, steps, { tests } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'nocx-quarantine-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const logs = join(root, 'logs')
  mkdirSync(logs)
  for (const { name, status, log, report } of steps) {
    writeFileSync(join(logs, `${name}.log`), log)
    writeFileSync(join(logs, `${name}.status`), `${status}\n`)
    writeFileSync(
      join(logs, `${name}.complete`),
      JSON.stringify({
        status,
        bytes: Buffer.byteLength(log),
        sha256: createHash('sha256').update(log).digest('hex'),
        ...(report !== undefined ? { report } : {}),
      }),
    )
  }
  const files = {
    quarantineJson: JSON.stringify({
      schema: 1,
      tests: tests ?? [
        { test: 'go:TestA', bead: 'nocx-a' },
        { test: askKey, bead: 'nocx-b' },
      ],
    }),
    issuesJsonl: [
      { id: 'nocx-a', status: 'open' },
      { id: 'nocx-b', status: 'open' },
      { id: 'nocx-c', status: 'closed' },
    ]
      .map((row) => JSON.stringify(row))
      .join('\n'),
  }
  const run = ({
    job = 'ci-backend (no-secret-service)',
    pull = '12',
    mainIssuesJsonl = files.issuesJsonl,
  } = {}) => {
    const report = []
    const code = gate({
      logs,
      expectedSteps: steps.map((step) => step.name),
      job,
      pull,
      runId: '22',
      attempt: 1,
      ...files,
      mainIssuesJsonl,
      write: (line) => report.push(line),
    })
    return { code, report, text: report.join('\n') }
  }
  return { run, logs }
}

const goEvents = (events) => events.map((event) => JSON.stringify(event)).join('\n') + '\n'
const failure = (test, pkg = `nocx/${test}`) =>
  goEvents([
    { Action: 'start', Package: pkg },
    { Action: 'run', Package: pkg, Test: test },
    { Action: 'fail', Package: pkg, Test: test },
    { Action: 'output', Package: pkg, Output: 'FAIL\n' },
    { Action: 'output', Package: pkg, Output: 'NOCX_GO_TEST_EXIT 1\n' },
    { Action: 'fail', Package: pkg },
  ])
const e2eReport = {
  errors: [],
  stats: { expected: 0, unexpected: 1, skipped: 0, flaky: 0, duration: 10 },
  suites: [
    {
      title: 'ask-attaches-the-frozen-screen.spec.ts',
      file: 'ask-attaches-the-frozen-screen.spec.ts',
      suites: [
        {
          title: 'the assistant is told the frozen screen came with the question (nocx-hp8p2.4)',
          specs: [
            {
              title:
                'summoning over a full-screen program on the alternate screen attaches its screen to the ask',
              tests: [
                { status: 'unexpected', expectedStatus: 'passed', results: [{ status: 'failed' }] },
              ],
            },
          ],
        },
      ],
    },
  ],
}

test('a job that failed only on quarantined tests passes, and every one of them is printed with its bead', (t) => {
  const { run } = evidence(t, [
    { name: 'go-backend', status: 1, log: failure('TestA') },
    { name: 'go-backend-local-ssh', status: 0, log: 'ok  \tgithub.com/shady2k/nocx/x\t1.0s\n' },
    { name: 'e2e-chromium-1', status: 1, log: e2eLog, report: e2eReport },
  ])
  const { code, text } = run()
  assert.equal(code, 0, text)
  assert.match(text, /go-backend failed go:TestA — quarantined by nocx-a \(open\)/)
  assert.ok(text.includes(`${askKey} — quarantined by nocx-b (open)`), text)
  assert.match(text, /passed with 2 quarantined failure\(s\)/)
})

test('one failure no open bead holds keeps the job red, beside the quarantined ones', (t) => {
  const { run } = evidence(t, [
    {
      name: 'go-backend',
      status: 1,
      log: failure('TestA') + failure('TestUnowned'),
    },
  ])
  const { code, text } = run()
  assert.equal(code, 1, text)
  assert.match(text, /go:TestA — quarantined by nocx-a \(open\)/)
  assert.match(text, /go-backend failed go:TestUnowned, which no open bead quarantines/)
  assert.match(text, /FAILED: 1 test step\(s\)/)
})

test('a closed bead protects nothing, and the report says so instead of asking for a new entry', (t) => {
  const { run } = evidence(t, [{ name: 'go-backend', status: 1, log: failure('TestC') }], {
    tests: [{ test: 'go:TestC', bead: 'nocx-c' }],
  })
  const { code, text } = run()
  assert.equal(code, 1, text)
  assert.match(text, /holds under nocx-c — a closed bead protects nothing/)
})

test('a step that failed without naming a test stays red, and a job entry holds exactly one pull request', (t) => {
  const build = [{ name: 'go-backend', status: 2, log: 'x.go:3:2: undefined: thing\n' }]
  const unnamed = evidence(t, build).run()
  assert.equal(unnamed.code, 1, unnamed.text)
  assert.match(unnamed.text, /failed without naming a test and never passed/)
  assert.match(unnamed.text, /only a job:<name>#<PR> entry holds one/)

  const held = evidence(t, build, {
    tests: [{ test: 'job:ci-backend (no-secret-service)#12', bead: 'nocx-a' }],
  })
  assert.equal(held.run().code, 0, held.run().text)
  assert.match(
    held.run().text,
    /failed job:ci-backend \(no-secret-service\)#12 — quarantined by nocx-a \(open\)/,
  )
  assert.match(held.run().text, /passed with 1 quarantined failure/)
  const otherPR = held.run({ pull: '13' })
  assert.equal(otherPR.code, 1, otherPR.text)
  // Off a pull request the workflow passes an empty number, and there is then no
  // key an entry could name.
  assert.equal(held.run({ pull: '' }).code, 1)
})

test('a job whose every test step passed says so, and an unreadable evidence set is refused', (t) => {
  const { run } = evidence(t, [{ name: 'go-backend', status: 0, log: 'ok\n' }])
  const { code, text } = run()
  assert.equal(code, 0, text)
  assert.match(text, /none of its 1 recorded test step\(s\) failed/)
  // A PASSING step that prints a test failure's line is not a failure. This is
  // not hypothetical: e2e/visual-board.spec.ts:170 runs a shell fixture that
  // prints `--- FAIL: TestSessionReconnect (0.08s)` into a pane during a green
  // spec, so text alone would fail the e2e job on a green run.
  const printed = evidence(t, [
    { name: 'e2e-chromium-1', status: 0, log: '--- FAIL: TestSessionReconnect (0.08s)\n' },
  ]).run()
  assert.equal(printed.code, 0, printed.text)
  assert.throws(
    () =>
      gate({
        logs: join(tmpdir(), 'nocx-quarantine-does-not-exist'),
        runId: '22',
        attempt: 1,
        expectedSteps: ['go-backend'],
        mainIssuesJsonl: '{}',
        quarantineJson: '{"schema":1,"tests":[]}',
        issuesJsonl: '',
        write: () => {},
      }),
    /ENOENT/,
  )
})

// Missing evidence is not an empty green run. The manifest is supplied by the
// workflow, never inferred from whichever statuses happened to survive.
test('every expected step has a nonempty completed log and an integer status', (t) => {
  for (const damage of [
    'empty-directory',
    'missing-status',
    'empty-status',
    'fractional-status',
    'missing-log',
    'empty-log',
    'truncated-log',
    'changed-status',
    'orphan-log',
  ]) {
    const { logs, run } = evidence(t, [{ name: 'go-backend', status: 0, log: 'ok\n' }])
    if (damage === 'empty-directory') {
      rmSync(logs, { recursive: true })
      mkdirSync(logs)
    } else if (damage === 'missing-status') unlinkSync(join(logs, 'go-backend.status'))
    else if (damage === 'empty-status') writeFileSync(join(logs, 'go-backend.status'), '')
    else if (damage === 'fractional-status') writeFileSync(join(logs, 'go-backend.status'), '0.5\n')
    else if (damage === 'missing-log') unlinkSync(join(logs, 'go-backend.log'))
    else if (damage === 'empty-log') writeFileSync(join(logs, 'go-backend.log'), '')
    else if (damage === 'changed-status') writeFileSync(join(logs, 'go-backend.status'), '1\n')
    else if (damage === 'orphan-log') writeFileSync(join(logs, 'orphan.log'), 'orphan\n')
    else writeFileSync(join(logs, 'go-backend.log'), 'a partial log\n')
    assert.throws(() => run(), /evidence|status|log|ENOENT/, damage)
  }
})

test('an evidence write failure fails the helper instead of leaving a green step', (t) => {
  const { logs } = evidence(t, [])
  const bin = join(logs, 'bin')
  mkdirSync(bin)
  writeFileSync(join(bin, 'tee'), '#!/bin/sh\ncat >/dev/null\nexit 23\n', { mode: 0o755 })
  const result = spawnSync(
    'bash',
    ['scripts/ci-run-logged.sh', 'broken', 'printf', 'test output\n'],
    {
      env: { ...process.env, NOCX_TEST_LOG_DIR: logs, PATH: `${bin}:${process.env.PATH}` },
      encoding: 'utf8',
    },
  )
  assert.notEqual(result.status, 0, result.stdout + result.stderr)
})

test('a quarantined name never covers an unexplained or incomplete runner failure', () => {
  const verdict = judgeFailures({
    failures: ['go:TestA'],
    complete: false,
    quarantine: { 'go:TestA': 'nocx-a' },
  })
  assert.equal(verdict.accepted, false)
  assert.match(verdict.reason, /incomplete|unexplained/)
})

test('a bead closed on current main revokes a stale checkout quarantine', (t) => {
  const { run } = evidence(t, [{ name: 'go-backend', status: 1, log: failure('TestA') }])
  const result = run({
    mainIssuesJsonl:
      JSON.stringify({ id: 'nocx-a', status: 'closed' }) +
      '\n' +
      JSON.stringify({ id: 'nocx-b', status: 'open' }),
  })
  assert.equal(result.code, 1, result.text)
  assert.match(result.text, /closed bead/)
})

test('structured Go evidence accounts for every package and ignores printed FAIL text', () => {
  assert.deepEqual(goTestEvidence(failure('TestA')), { failures: ['go:TestA'], complete: true })
  for (const tail of [
    goEvents([
      { Action: 'start', Package: 'broken' },
      { Action: 'fail', Package: 'broken' },
    ]),
    goEvents([{ Action: 'start', Package: 'broken' }]),
    'vet error: broken\n',
    goEvents([
      { Action: 'start', Package: 'broken' },
      { Action: 'output', Package: 'broken', Output: 'panic: timeout\n' },
      { Action: 'fail', Package: 'broken' },
    ]),
  ])
    assert.equal(goTestEvidence(failure('TestA') + tail).complete, false, tail)
  const printed = goEvents([
    { Action: 'start', Package: 'a' },
    { Action: 'run', Package: 'a', Test: 'TestPassing' },
    { Action: 'output', Package: 'a', Test: 'TestPassing', Output: '--- FAIL: TestA (0s)\n' },
    { Action: 'pass', Package: 'a', Test: 'TestPassing' },
    { Action: 'pass', Package: 'a' },
    { Action: 'start', Package: 'broken' },
    { Action: 'fail', Package: 'broken' },
  ])
  assert.deepEqual(goTestEvidence(printed), { failures: [], complete: false })
})

test('structured Playwright evidence refuses runner errors and interrupted or partial shards', () => {
  assert.deepEqual(playwrightEvidence(e2eReport), { failures: [askKey], complete: true })
  for (const mutate of [
    (r) => r.errors.push({ message: 'runner crashed' }),
    (r) => {
      r.suites[0].suites[0].specs[0].tests[0].results[0].status = 'interrupted'
    },
    (r) => {
      r.stats.unexpected = 2
    },
    (r) => {
      delete r.stats
    },
  ]) {
    const report = structuredClone(e2eReport)
    mutate(report)
    assert.equal(playwrightEvidence(report).complete, false)
  }
})

test('nonordinary runner exits and mixed named plus unnamed failures stay red at the gate', (t) => {
  for (const step of [
    { name: 'go-backend', status: 137, log: failure('TestA') },
    {
      name: 'go-backend',
      status: 1,
      log:
        failure('TestA') +
        goEvents([
          { Action: 'start', Package: 'b' },
          { Action: 'fail', Package: 'b' },
        ]),
    },
    {
      name: 'e2e',
      status: 1,
      log: e2eLog,
      report: { ...e2eReport, errors: [{ message: 'runner failed' }] },
    },
  ])
    assert.equal(evidence(t, [step]).run().code, 1)
})

test('a missing or unknown authoritative main state never renews checkout protection', (t) => {
  const { run } = evidence(t, [{ name: 'go-backend', status: 1, log: failure('TestA') }])
  assert.throws(() => run({ mainIssuesJsonl: '' }), /current main/)
  assert.throws(
    () => run({ mainIssuesJsonl: JSON.stringify({ id: 'nocx-b', status: 'open' }) }),
    /does not exist/,
  )
})

test('the actual producer seals ordinary test failures and preserves both command and evidence status', (t) => {
  const { logs } = evidence(t, [])
  const result = spawnSync(
    'bash',
    ['scripts/ci-run-logged.sh', 'real-step', 'bash', '-c', 'printf "runner output\\n"; exit 1'],
    {
      env: { ...process.env, NOCX_TEST_LOG_DIR: logs },
      encoding: 'utf8',
    },
  )
  assert.equal(result.status, 0, result.stderr)
  assert.equal(readFileSync(join(logs, 'real-step.status'), 'utf8'), '1\n')
  assert.equal(JSON.parse(readFileSync(join(logs, 'real-step.complete'), 'utf8')).status, 1)
  mkdirSync(join(logs, 'status-fails.status'))
  const bad = spawnSync(
    'bash',
    ['scripts/ci-run-logged.sh', 'status-fails', 'printf', 'output\n'],
    {
      env: { ...process.env, NOCX_TEST_LOG_DIR: logs },
      encoding: 'utf8',
    },
  )
  assert.notEqual(bad.status, 0)
})

test('a gate CLI refuses a failed tracker-store fetch instead of using a cached snapshot', (t) => {
  const { logs } = evidence(t, [{ name: 'go-backend', status: 0, log: 'ok\n' }])
  const bin = join(logs, 'bin')
  mkdirSync(bin)
  writeFileSync(join(bin, 'git'), '#!/bin/sh\nprintf "%s\\n" "$*" >> "$FETCH_TRACE"\nexit 45\n', {
    mode: 0o755,
  })
  const trace = join(logs, 'fetch-trace')
  const result = spawnSync('node', ['scripts/quarantine-gate.mjs'], {
    env: {
      ...process.env,
      NOCX_TEST_LOG_DIR: logs,
      NOCX_EXPECTED_STEPS: 'go-backend',
      FETCH_TRACE: trace,
      PATH: `${bin}:${process.env.PATH}`,
    },
    encoding: 'utf8',
  })
  assert.equal(result.status, 1, result.stderr)
  assert.match(result.stderr, /FAILED/)
  assert.equal(
    readFileSync(trace, 'utf8'),
    'fetch --no-tags origin refs/tracker/state:refs/tracker/state\n',
  )
})

test('the real helper and gate leave rejudgeable evidence even when a quarantine makes the job green', (t) => {
  const { logs } = evidence(t, [])
  const runner = join(logs, 'runner.json')
  writeFileSync(runner, failure('TestA'))
  const produced = spawnSync(
    'bash',
    ['scripts/ci-run-logged.sh', 'go-backend', 'bash', '-c', 'cat "$1"; exit 1', 'runner', runner],
    {
      env: { ...process.env, NOCX_TEST_LOG_DIR: logs },
      encoding: 'utf8',
    },
  )
  assert.equal(produced.status, 0, produced.stderr)
  const lines = []
  const issues = JSON.stringify({ id: 'nocx-a', status: 'open' })
  const code = gate({
    logs,
    expectedSteps: ['go-backend'],
    job: 'ci-backend',
    pull: '12',
    runId: '22',
    attempt: 1,
    quarantineJson: JSON.stringify({ schema: 1, tests: [{ test: 'go:TestA', bead: 'nocx-a' }] }),
    issuesJsonl: issues,
    mainIssuesJsonl: issues,
    write: (line) => lines.push(line),
  })
  assert.equal(code, 0, lines.join('\n'))
  assert.deepEqual(
    readQuarantineEvidence(lines.join('\n'), 'ci-backend', 12, {
      runId: '22',
      attempt: 1,
      expectedSteps: ['go-backend'],
    }).steps,
    [{ name: 'go-backend', status: 1, failures: ['go:TestA'], complete: true }],
  )
})

test('a macOS boundary failure beside a quarantined spec cannot become green', (t) => {
  const { logs, run } = evidence(t, [
    {
      name: 'e2e-macos-first-command',
      status: 1,
      log: e2eLog + 'BOUNDARY LEAK — real home changed\n',
      report: e2eReport,
    },
  ])
  const sealPath = join(logs, 'e2e-macos-first-command.complete')
  const seal = JSON.parse(readFileSync(sealPath, 'utf8'))
  seal.wrapper = { required: true, status: null }
  writeFileSync(sealPath, JSON.stringify(seal))
  assert.equal(run().code, 1)
})

test('the actual macOS wrapper proves its boundary before a named quarantine applies', (t) => {
  for (const leak of [false, true]) {
    const { logs } = evidence(t, [])
    const home = join(logs, 'home')
    mkdirSync(home)
    const bin = join(logs, 'bin')
    mkdirSync(bin)
    const report = join(logs, 'playwright.json')
    const wrapperStatus = join(logs, 'wrapper.receipt')
    writeFileSync(
      join(bin, 'npx'),
      `#!/bin/sh\nif [ "$LEAK" = 1 ]; then touch "$HOME/.bashrc"; fi\ncat "$REPORT_SOURCE" > "$NOCX_QUARANTINE_REPORT"\nprintf 'runner failure\\n'\nexit 1\n`,
      { mode: 0o755 },
    )
    // The wrapper uses macOS stat; this fake only needs deterministic mtimes.
    writeFileSync(join(bin, 'stat'), '#!/bin/sh\nprintf "1\\n"\n', { mode: 0o755 })
    const source = join(logs, 'report-source.json')
    writeFileSync(source, JSON.stringify(e2eReport))
    const result = spawnSync(
      'bash',
      ['scripts/ci-run-logged.sh', 'e2e-macos-first-command', 'bash', 'e2e/run-headless-macos.sh'],
      {
        env: {
          ...process.env,
          HOME: home,
          LEAK: leak ? '1' : '0',
          REPORT_SOURCE: source,
          NOCX_TEST_LOG_DIR: logs,
          NOCX_QUARANTINE_REPORT: report,
          NOCX_QUARANTINE_WRAPPER_STATUS: wrapperStatus,
          PATH: `${bin}:${process.env.PATH}`,
        },
        encoding: 'utf8',
      },
    )
    assert.equal(result.status, 0, result.stderr)
    const lines = []
    const issues = JSON.stringify({ id: 'nocx-b', status: 'open' })
    const code = gate({
      logs,
      expectedSteps: ['e2e-macos-first-command'],
      job: 'ci-mac',
      pull: '12',
      runId: '22',
      attempt: 1,
      quarantineJson: JSON.stringify({ schema: 1, tests: [{ test: askKey, bead: 'nocx-b' }] }),
      issuesJsonl: issues,
      mainIssuesJsonl: issues,
      write: (line) => lines.push(line),
    })
    assert.equal(code, leak ? 1 : 0, lines.join('\n'))
  }
})

test('a completed quarantined test never explains an abnormal Go package termination', (t) => {
  const prefix = [
    { Action: 'start', Package: 'nocx/abnormal' },
    { Action: 'run', Package: 'nocx/abnormal', Test: 'TestA' },
    { Action: 'fail', Package: 'nocx/abnormal', Test: 'TestA' },
  ]
  for (const outputs of [
    ['signal: killed\n'],
    ['exit status 2\n'],
    [],
    ['FAIL\n', 'signal: killed\n'],
    ['FAIL\n', 'exit status 2\n'],
  ]) {
    const log = goEvents([
      ...prefix,
      ...outputs.map((Output) => ({ Action: 'output', Package: 'nocx/abnormal', Output })),
      { Action: 'fail', Package: 'nocx/abnormal' },
    ])
    assert.equal(goTestEvidence(log).complete, false, JSON.stringify(outputs))
    assert.equal(evidence(t, [{ name: 'go-backend', status: 1, log }]).run().code, 1)
  }
})

test('the Go binary wrapper preserves abnormal TestMain and signal exit status after named tests end', (t) => {
  for (const status of [1, 2, 137]) {
    const result = spawnSync(
      'bash',
      ['scripts/ci-go-test-exec.sh', 'bash', '-c', `printf 'FAIL\\n'; exit ${status}`],
      { encoding: 'utf8' },
    )
    assert.equal(result.status, status)
    assert.equal(result.stdout, `FAIL\nNOCX_GO_TEST_EXIT ${status}\n`)
    const log = failure('TestA').replace('NOCX_GO_TEST_EXIT 1', `NOCX_GO_TEST_EXIT ${status}`)
    assert.equal(
      evidence(t, [{ name: 'go-backend', status: 1, log }]).run().code,
      status === 1 ? 0 : 1,
    )
  }
})

test('a successful gate record carries its exact run attempt and declared manifest', (t) => {
  const { run } = evidence(t, [{ name: 'go-backend', status: 0, log: 'ok\n' }])
  const { text } = run()
  const record = JSON.parse(
    text
      .split('\n')
      .find((line) => line.startsWith('NOCX_QUARANTINE_EVIDENCE_V1 '))
      .slice('NOCX_QUARANTINE_EVIDENCE_V1 '.length),
  )
  assert.equal(record.runId, '22')
  assert.equal(record.attempt, 1)
  assert.equal(record.manifestDigest, stepManifestDigest(['go-backend']))
})

test('the binary receipt distinguishes a normal pass and a binary killed after printing output', () => {
  const pass = spawnSync(
    'bash',
    ['scripts/ci-go-test-exec.sh', 'bash', '-c', 'printf "PASS\\n"; exit 0'],
    { encoding: 'utf8' },
  )
  assert.equal(pass.status, 0)
  assert.equal(pass.stdout, 'PASS\nNOCX_GO_TEST_EXIT 0\n')
  const killed = spawnSync(
    'bash',
    ['scripts/ci-go-test-exec.sh', 'bash', '-c', 'printf "FAIL\\n"; kill -KILL $$'],
    { encoding: 'utf8' },
  )
  assert.equal(killed.status, 137)
  assert.equal(killed.stdout, 'FAIL\nNOCX_GO_TEST_EXIT 137\n')
})
