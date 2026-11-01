import { createHash } from 'node:crypto'

// One owner for the test quarantine: the file, the log parse, and the verdict.
//
// Two callers ask the same question of the same data — the promotion
// (scripts/release/promotion.mjs), which reads a merged pull request's job logs
// through the GitHub API, and a pull request's own CI
// (scripts/quarantine-gate.mjs), which reads the job's own output. A second
// parser or a second verdict would be a regression with a delay fuse: the two
// agree everywhere anyone looks and disagree somewhere nobody does.
//
// ADR-xf4p9fzd is the decision (a flake is quarantined by its open bead);
// ADR-c7fbhtkt widened a key to a whole job; ADR-0bawnmfg is CI honouring it.

const requireThat = (condition, message) => {
  if (!condition) throw new Error(message)
}

// Canonical step manifest fingerprint. The workflow declares the expected
// names; producer and promotion hash the same sorted, unique list.
export function stepManifestDigest(names) {
  requireThat(
    Array.isArray(names) &&
      names.length > 0 &&
      names.every((name) => typeof name === 'string' && /^[a-zA-Z0-9_-]+$/.test(name)) &&
      new Set(names).size === names.length,
    'invalid expected-step manifest',
  )
  return createHash('sha256')
    .update(JSON.stringify([...names].sort()))
    .digest('hex')
}

// A key is the name the suite prints on failure, without line numbers, which
// move: `go:<TestName>`, `e2e:<spec path> › <title path>`, or — for a known
// defect of a check rather than of a test — `job:<job name>#<pull number>`.
const keyPattern = /^(go:Test\w+|e2e:e2e\/\S+ › .+|job:[^#\n]+#[1-9]\d*)$/

// A quarantined test is one a flake bead owns (ADR "a flaky test is quarantined
// by its bead"). Its failure stops blocking only while that bead is open:
// closing the bead ends the protection without anyone remembering to edit this
// file.
export function readQuarantine(json, issuesJsonl) {
  return readQuarantineState(json, issuesJsonl).open
}

// The same read, keeping the entries whose bead is closed. A closed bead
// protects nothing, and the caller that reports a red job needs the difference:
// "no bead quarantines this" sends the reader to file one, while "the file
// holds it under a closed bead" sends them to reopen the bead or fix the test.
export function readQuarantineState(json, issuesJsonl) {
  if (json === undefined) return { open: {}, closed: {} }
  const doc = JSON.parse(json)
  requireThat(doc?.schema === 1 && Array.isArray(doc.tests), 'invalid test quarantine')
  const status = new Map()
  for (const line of (issuesJsonl ?? '').split('\n')) {
    if (!line.trim()) continue
    const row = JSON.parse(line)
    status.set(row.id, row.status)
  }
  const open = {}
  const closed = {}
  for (const entry of doc.tests) {
    requireThat(
      typeof entry?.test === 'string' &&
        keyPattern.test(entry.test) &&
        typeof entry.bead === 'string',
      `invalid test quarantine entry ${JSON.stringify(entry)}`,
    )
    const state = status.get(entry.bead)
    requireThat(state !== undefined, `quarantine ${entry.test}: bead ${entry.bead} does not exist`)
    if (['closed', 'tombstone'].includes(state)) closed[entry.test] = entry.bead
    else open[entry.test] = entry.bead
  }
  return { open, closed }
}

// The tests a failed job's log names, in the quarantine's key form. A failure
// the log does not name (a build error, a timeout panic, the job's own setup)
// yields nothing, and nothing cannot be quarantined.
//
// Legacy promotion logs still need a diagnostic name reader. Its text result
// is never complete evidence for accepting a named quarantine. New job gates
// use runner events below, and promotion reuses their persisted verdict input.
export function failedTests(log) {
  const keys = new Set()
  for (const raw of log.split(/\r?\n/)) {
    const line = raw.replace(/^\S+Z /, '')
    const go = /^\s*--- FAIL: (Test\w+)/.exec(line)
    if (go) keys.add(`go:${go[1]}`)
    const e2e = /^\s*\d+\) \[[^\]]+\] › (e2e\/[^:\s]+):\d+:\d+ › (.+?)[\s─]*$/.exec(line)
    if (e2e) keys.add(`e2e:${e2e[1]} › ${e2e[2]}`)
  }
  return [...keys].sort()
}

// Positive runner evidence, not failure-looking text printed by a passing test.
// Each started Go package must terminate. Each red package must be explained
// by named terminal test events, with no panic, build/vet or runner failure.
export function goTestEvidence(log) {
  const packages = new Map()
  const failures = new Set()
  let valid = true
  for (const line of log.split(/\r?\n/)) {
    if (!line.trim() || line === '=== go test (no -race): the allocation budgets ===') continue
    let event
    try {
      event = JSON.parse(line)
    } catch {
      valid = false
      continue
    }
    if (!event || typeof event.Package !== 'string' || typeof event.Action !== 'string') {
      valid = false
      continue
    }
    let pkg = packages.get(event.Package)
    if (!pkg) {
      pkg = {
        started: false,
        ended: false,
        failed: false,
        named: false,
        normalFailureEnd: false,
        binaryExit: undefined,
        tests: new Set(),
      }
      packages.set(event.Package, pkg)
    }
    if (
      pkg.ended ||
      (pkg.binaryExit !== undefined &&
        event.Action !== 'output' &&
        !(['pass', 'fail', 'skip'].includes(event.Action) && !event.Test))
    )
      valid = false
    if (event.Action === 'start') {
      if (pkg.started) valid = false
      pkg.started = true
    }
    if (event.Action === 'run' && event.Test) pkg.tests.add(event.Test)
    if (['pass', 'fail', 'skip'].includes(event.Action)) {
      if (event.Test) {
        if (!pkg.tests.delete(event.Test)) valid = false
        if (event.Action === 'fail') {
          if (!/^Test\w+(?:\/.*)?$/.test(event.Test)) valid = false
          else failures.add(`go:${event.Test.split('/')[0]}`)
          pkg.named = true
        }
      } else {
        pkg.ended = true
        pkg.failed = event.Action === 'fail'
      }
    }
    if (event.Action === 'output') {
      const output = event.Output ?? ''
      // A package fail action is also emitted after a killed binary or an
      // abnormal TestMain exit. Require testing.M's normal terminal FAIL,
      // and reject runner diagnostics even after all tests have terminated.
      if (
        /(?:^|\n)(?:panic:|fatal error:|signal:|exit status |FAIL\s+.*\[build failed\])/.test(
          output,
        )
      )
        valid = false
      const receipt = /^NOCX_GO_TEST_EXIT (0|[1-9]\d*)\n$/.exec(output)
      if (receipt) {
        if (pkg.binaryExit !== undefined || pkg.tests.size) valid = false
        pkg.binaryExit = Number(receipt[1])
        if (![0, 1].includes(pkg.binaryExit)) valid = false
      } else if (!event.Test) {
        if (output === 'FAIL\n') {
          if (pkg.normalFailureEnd || pkg.tests.size) valid = false
          pkg.normalFailureEnd = true
        } else if (pkg.normalFailureEnd && !output.startsWith(`FAIL\t${event.Package}\t`)) {
          valid = false
        }
      }
    }
    if (
      !['start', 'run', 'pause', 'cont', 'output', 'pass', 'fail', 'skip', 'bench'].includes(
        event.Action,
      )
    )
      valid = false
  }
  for (const pkg of packages.values())
    if (
      !pkg.started ||
      !pkg.ended ||
      pkg.tests.size ||
      pkg.failed !== pkg.named ||
      (pkg.failed && (!pkg.normalFailureEnd || pkg.binaryExit !== 1)) ||
      (!pkg.failed && pkg.binaryExit !== undefined && pkg.binaryExit !== 0)
    )
      valid = false
  return { failures: [...failures].sort(), complete: valid && packages.size > 0 }
}

// Playwright's JSON reporter includes every selected spec and every terminal
// result, plus global errors and totals. A half report or a runner error is not
// converted into a named flake. Retries/flaky results are deliberately refused.
export function playwrightEvidence(report) {
  const failures = new Set()
  let valid = report?.errors?.length === 0 && Array.isArray(report?.suites)
  let count = 0
  let passed = 0
  let skipped = 0
  let failed = 0
  const walk = (suites, parents = [], parentFile) => {
    for (const suite of suites ?? []) {
      const file = suite.file || parentFile
      // The file suite is not part of the user-visible title path.
      const titles =
        suite.title && suite.title !== file && !suite.title.endsWith('.spec.ts')
          ? [...parents, suite.title]
          : parents
      for (const spec of suite.specs ?? []) {
        if (!Array.isArray(spec.tests) || spec.tests.length === 0) valid = false
        for (const test of spec.tests ?? []) {
          count++
          const results = test.results
          if (!Array.isArray(results) || results.length !== 1) {
            valid = false
            continue
          }
          const status = results[0].status
          if (status === 'skipped' && test.status === 'skipped') skipped++
          else if (status === 'passed' && test.status === 'expected') passed++
          else if (
            ['failed', 'timedOut'].includes(status) &&
            test.status === 'unexpected' &&
            test.expectedStatus === 'passed'
          ) {
            failed++
            const path = spec.file || file
            if (typeof path !== 'string' || !path.endsWith('.spec.ts')) valid = false
            else
              failures.add(
                `e2e:${path.startsWith('e2e/') ? path : `e2e/${path}`} › ${[...titles, spec.title].join(' › ')}`,
              )
          } else valid = false
        }
      }
      walk(suite.suites, titles, file)
    }
  }
  if (valid) walk(report.suites)
  const stats = report?.stats
  valid &&=
    count > 0 &&
    Number.isFinite(stats?.duration) &&
    stats.expected === passed &&
    stats.unexpected === failed &&
    stats.skipped === skipped &&
    stats.flaky === 0 &&
    count === passed + failed + skipped
  return { failures: [...failures].sort(), complete: Boolean(valid) }
}

// The verdict on one job's failures, in one place for both callers.
//
// Every failure must be held by an open bead. A failure that names no test is
// not a flake anyone can quarantine by test name: it passes only when the same
// job passed in another attempt (the promotion's reading of a retry that
// classified an infrastructure flake) or when a bead quarantines exactly this
// job of exactly this pull request (`job:<name>#<PR>`) — a known defect of the
// check itself, never a class. A pull request's own CI has no other attempt, so
// only the job entry can answer there.
//
// `reason` is phrased for the caller's line prefix, so the promotion's
// `requireThat` and the CI gate's report refuse in the same words.
export function judgeFailures({
  failures,
  complete = false,
  quarantine,
  closed = {},
  jobKey,
  passedInAnotherAttempt = false,
}) {
  const quarantined = []
  const unquarantined = []
  for (const test of failures) {
    const bead = quarantine[test]
    if (bead === undefined) unquarantined.push(test)
    else quarantined.push({ test, bead })
  }
  if (failures.length === 0) {
    const held =
      passedInAnotherAttempt || (jobKey !== undefined && quarantine[jobKey] !== undefined)
    if (!passedInAnotherAttempt && held)
      quarantined.push({ test: jobKey, bead: quarantine[jobKey] })
    return held
      ? { accepted: true, quarantined, unquarantined, reason: '' }
      : {
          accepted: false,
          quarantined,
          unquarantined,
          reason: 'failed without naming a test and never passed',
        }
  }
  if (complete !== true)
    return {
      accepted: false,
      quarantined: [],
      unquarantined: failures,
      reason: 'failed with incomplete runner evidence or an unexplained failure',
    }
  if (unquarantined.length === 0) return { accepted: true, quarantined, unquarantined, reason: '' }
  const test = unquarantined[0]
  const bead = closed[test]
  return {
    accepted: false,
    quarantined,
    unquarantined,
    reason: bead
      ? `failed ${test}, which docs/testing/quarantine.json holds under ${bead} — a closed bead protects nothing`
      : `failed ${test}, which no open bead quarantines`,
  }
}
