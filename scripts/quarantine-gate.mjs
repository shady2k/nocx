#!/usr/bin/env node
/**
 * A pull request's own CI honours the test quarantine.
 *
 * ADR-xf4p9fzd made a red job pass when every test it failed is held by an open
 * bead, and ADR-0bawnmfg is this side of it: the same policy, asked of the job
 * that is running rather than of a merged pull request's stored logs. A job that
 * failed only on quarantined tests passes and PRINTS every one of them with its
 * bead; a test no open bead holds, a bead that has been closed, and a failure
 * that names no test all keep the job red.
 *
 * Every expected step is declared by the workflow, never discovered from
 * surviving files. The helper seals a nonempty log and integer command status
 * only after tee and all writes succeeded. Missing or damaged evidence throws.
 * A nonzero command needs complete Go JSON events or a Playwright JSON report
 * before named test quarantines apply. Printed FAIL text cannot authorize one.
 *
 * The last step emits a compact machine-readable record into the job log even
 * on success. Promotion reads successful job logs too and rejudges this record
 * against its own quarantine state. No expiring workflow artifact is needed.
 *
 * Every invocation fetches current main's tracker export. A bead must exist
 * in both the checkout and current main and be nonterminal in both. Failure to
 * obtain main refuses; new branch-local beads protect nothing until exported.
 *
 *   NOCX_EXPECTED_STEPS   required space-separated positive step manifest
 *   NOCX_TEST_LOG_DIR     the directory the gated steps wrote (required)
 *   NOCX_QUARANTINE_JOB   this job's name as GitHub prints it, e.g.
 *                         `ci-backend (no-secret-service)` — the same name the
 *                         promotion reads from the API, so one entry serves both
 *   NOCX_QUARANTINE_PULL  the pull request number; empty off a pull request,
 *                         where a `job:<name>#<PR>` entry cannot match
 *   NOCX_QUARANTINE_FILE  default docs/testing/quarantine.json
 *   NOCX_ISSUES_FILE      default .beads/issues.jsonl
 */
import { readFileSync, readdirSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'

import {
  goTestEvidence,
  stepManifestDigest,
  playwrightEvidence,
  judgeFailures,
  readQuarantineState,
} from './quarantine.mjs'

const prefix = 'quarantine:'

/** The evidence one gated step left: its name, its command's status, its output. */
export function readStepLogs(dir, expectedSteps) {
  if (
    !Array.isArray(expectedSteps) ||
    expectedSteps.length === 0 ||
    new Set(expectedSteps).size !== expectedSteps.length ||
    expectedSteps.some((name) => !/^[a-zA-Z0-9_-]+$/.test(name))
  )
    throw new Error('invalid or empty expected-step evidence manifest')
  const names = readdirSync(dir)
  if (
    names.some(
      (file) =>
        /\.(log|status|complete)$/.test(file) &&
        !expectedSteps.some(
          (name) =>
            file === `${name}.log` || file === `${name}.status` || file === `${name}.complete`,
        ),
    )
  )
    throw new Error('unexpected step evidence outside manifest')
  const actual = names.filter((name) => name.endsWith('.status')).map((name) => name.slice(0, -7))
  if (
    actual.length !== expectedSteps.length ||
    actual.some((name) => !expectedSteps.includes(name))
  )
    throw new Error('step evidence does not match the expected-step manifest')
  return expectedSteps.map((name) => {
    const raw = readFileSync(join(dir, `${name}.status`), 'utf8').trim()
    if (!/^(0|[1-9]\d*)$/.test(raw) || Number(raw) > 255)
      throw new Error(`invalid status evidence for ${name}`)
    const body = readFileSync(join(dir, `${name}.log`))
    const seal = JSON.parse(readFileSync(join(dir, `${name}.complete`), 'utf8'))
    if (
      !body.length ||
      seal.status !== Number(raw) ||
      seal.bytes !== body.length ||
      seal.sha256 !== createHash('sha256').update(body).digest('hex')
    )
      throw new Error(`incomplete log evidence for ${name}`)
    const status = Number(raw)
    const runner =
      status === 0
        ? { failures: [], complete: true }
        : Object.hasOwn(seal, 'report')
          ? playwrightEvidence(seal.report)
          : goTestEvidence(body.toString('utf8'))
    return {
      name,
      status,
      ...runner,
      complete:
        runner.complete &&
        (status === 0 || status === 1) &&
        (!seal.wrapper?.required || seal.wrapper.status === status),
    }
  })
}

/**
 * The decision, in one place and without a process: 0 when this job may pass, 1
 * when it may not. Everything it says goes through `write`, so a test reads the
 * report a job's log would carry.
 */
export function gate({
  logs,
  expectedSteps,
  job = 'unknown job',
  pull,
  runId,
  attempt,
  quarantineJson,
  issuesJsonl,
  mainIssuesJsonl,
  write = (line) => process.stdout.write(`${line}\n`),
}) {
  if (typeof mainIssuesJsonl !== 'string' || !mainIssuesJsonl.trim())
    throw new Error('current main tracker evidence is missing')
  const checkout = readQuarantineState(quarantineJson, issuesJsonl)
  const current = readQuarantineState(quarantineJson, mainIssuesJsonl)
  const closed = { ...checkout.closed, ...current.closed }
  const open = Object.fromEntries(
    Object.entries(checkout.open).filter(([key]) => current.open[key] && !closed[key]),
  )
  if (
    typeof runId !== 'string' ||
    !/^[1-9]\d*$/.test(runId) ||
    !Number.isSafeInteger(Number(runId)) ||
    !Number.isSafeInteger(attempt) ||
    attempt < 1
  )
    throw new Error('invalid quarantine run/attempt identity')
  const steps = readStepLogs(logs, expectedSteps)
  const manifestDigest = stepManifestDigest(expectedSteps)
  write(
    `NOCX_QUARANTINE_EVIDENCE_V1 ${JSON.stringify({ schema: 1, job, pull: pull || '', runId, attempt, manifestDigest, steps })}`,
  )
  const jobKey = pull ? `job:${job}#${pull}` : undefined
  const failed = []
  for (const step of steps) {
    // ONLY A STEP THAT FAILED IS JUDGED, and the log only names what it failed
    // on. That is not a weakening: e2e/visual-board.spec.ts:170 runs a shell
    // fixture that prints `--- FAIL: TestSessionReconnect (0.08s)` into a pane
    // during a PASSING spec, so a log that names a failure is not evidence the
    // step failed — the status is.
    if (step.status === 0) continue
    failed.push({
      step,
      verdict: judgeFailures({
        failures: step.failures,
        complete: step.complete,
        quarantine: open,
        closed,
        jobKey,
      }),
    })
  }
  if (failed.length === 0) {
    write(`${prefix} ${job}: none of its ${steps.length} recorded test step(s) failed`)
    return 0
  }
  for (const { step, verdict } of failed)
    for (const { test, bead } of verdict.quarantined)
      write(`${prefix} ${job}: ${step.name} failed ${test} — quarantined by ${bead} (open)`)
  const refused = failed.filter(({ verdict }) => !verdict.accepted)
  for (const { step, verdict } of refused) write(`${prefix} ${job}: ${step.name} ${verdict.reason}`)
  const quarantined = failed.reduce((count, { verdict }) => count + verdict.quarantined.length, 0)
  if (refused.length > 0) {
    const unquarantined = refused.reduce(
      (count, { verdict }) => count + verdict.unquarantined.length,
      0,
    )
    const unnamed = refused.filter(({ verdict }) => verdict.unquarantined.length === 0).length
    write(
      `${prefix} FAILED: ${refused.length} test step(s) of ${job} reported ${unquarantined} named failure(s) no open bead holds and ${unnamed} failure(s) that named none, beside ${quarantined} quarantined one(s). Fix them, or file each one's bead and add it to docs/testing/quarantine.json.`,
    )
    write(
      `${prefix} a failure that names no test — a build error, a setup step — is never quarantined by a test name: only a job:<name>#<PR> entry holds one, for one pull request.`,
    )
    return 1
  }
  write(
    `${prefix} passed with ${quarantined} quarantined failure(s): every failure ${job} reported is held by an open bead, listed above.`,
  )
  return 0
}

// Fetch the remote's current main on EVERY invocation, including reruns of an
// old merge ref. No cached origin/main or checkout fallback can renew a closed
// bead. New branch-local beads must be exported on main before they protect CI.
function currentMainIssues() {
  execFileSync('git', ['fetch', '--no-tags', 'origin', 'refs/heads/main'], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  return execFileSync('git', ['show', 'FETCH_HEAD:.beads/issues.jsonl'], {
    encoding: 'utf8',
    maxBuffer: 256 * 1024 * 1024,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
}

export function main(env = process.env) {
  const logs = env.NOCX_TEST_LOG_DIR
  const pull = env.NOCX_QUARANTINE_PULL?.trim()
  try {
    if (!logs) throw new Error('NOCX_TEST_LOG_DIR is not set: nothing to judge')
    return gate({
      logs,
      expectedSteps: env.NOCX_EXPECTED_STEPS?.trim().split(/\s+/),
      job: env.NOCX_QUARANTINE_JOB?.trim() || 'unknown job',
      pull: pull || undefined,
      runId: env.GITHUB_RUN_ID,
      attempt: /^[1-9]\d*$/.test(env.GITHUB_RUN_ATTEMPT || '')
        ? Number(env.GITHUB_RUN_ATTEMPT)
        : undefined,
      quarantineJson: readFileSync(
        env.NOCX_QUARANTINE_FILE || 'docs/testing/quarantine.json',
        'utf8',
      ),
      issuesJsonl: readFileSync(env.NOCX_ISSUES_FILE || '.beads/issues.jsonl', 'utf8'),
      mainIssuesJsonl: currentMainIssues(),
    })
  } catch (error) {
    // Fail closed: a check that cannot read its own evidence must never report
    // a pass.
    process.stderr.write(`${prefix} FAILED: ${error.message}\n`)
    return 1
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exit(main())
}
