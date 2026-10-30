# ADR-0bawnmfg — A pull request's CI honours the test quarantine

- **Status:** Accepted (implementation of the follow-up ADR-xf4p9fzd itself names, 2026-10-29)
- **Date:** 2026-10-29
- **Supersedes:** only [ADR-xf4p9fzd](xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md)'s
  Consequences sentence "CI itself still fails a PR on a quarantined test." Its decision, and
  [ADR-c7fbhtkt](c7fbhtkt-a-known-defect-of-a-check-is-quarantined-for-one-pr-only.md)'s widened
  key, stand.

## Context

ADR-xf4p9fzd let the promotion accept a merged pull request whose red jobs failed only on
tests `docs/testing/quarantine.json` holds under an open bead, and said in as many words that
making the pull request's own CI non-blocking for those was a separate task. Until it existed
the two checks disagreed about the same run: the promotion would take the merge, while the
merge itself waited on a flake nobody could fix in that session. A quarantined flake blocked
every unrelated change on its way to `main`, and the file the flake was recorded in changed
nothing about the run that was red.

The obstacle is not the policy, it is the evidence. The promotion reads a merged pull
request's job logs through `GET /repos/{repo}/actions/jobs/{id}/logs`, with a token and a
repository it is not running inside. A workflow job has neither: `permissions: contents:
read` gives it no log endpoint of its own, and it cannot read its own job log while it is
still running. Whatever the job knows about its failures, it has to keep itself.

## Decision

**The policy is one module, `scripts/quarantine.mjs`**, holding the file's read, the log parse
(`failedTests`) and the verdict (`judgeFailures`). The promotion and a pull request's CI both
import it, so a second parser or a second verdict cannot exist to drift from the first. A
policy change lands in one place and both callers take it.

**The evidence is the job's own output.** A gated step runs its command through
`scripts/ci-run-logged.sh`, which tees the step's stdout and stderr to
`$NOCX_TEST_LOG_DIR/<name>.log` and writes the command's exit status to `<name>.status`. That
file holds what the jobs/logs endpoint would return for the same step, which is why one
`failedTests` reads both, and nothing here downloads a log, holds a token or asks the API.

**The status decides whether a step is judged; the log only names what it failed on.** Both
halves are needed and neither is enough. A failure that names no test — a build error, a setup
step — leaves nothing in the text to find, so the status is the only thing that can say the
step failed. And text alone would fail a green job: `e2e/visual-board.spec.ts:170` runs a shell
fixture that prints `--- FAIL: TestSessionReconnect (0.08s)` into a pane during a PASSING spec,
so a log that names a failure is not evidence that anything failed.

**The verdict is taken once, at the end of the job** — `node scripts/quarantine-gate.mjs`
under `if: always()`. A gated step therefore always exits 0: stopping a step at its first
failure would hide the second suite's failures from the only place that judges them. A step
that is not routed through the helper still fails its job the ordinary way, so a step added
later cannot go unnoticed and green.

**The job name in `job:<name>#<PR>` is the name GitHub prints** — `ci-backend
(with-secret-service)`, built from the matrix — because that is the string the promotion
reads from the API. One entry serves both checks. A pull request's CI has no second attempt,
so the promotion's "the same job passed in another attempt" clause has no equivalent there:
a failure that names no test stays red unless a `job:<name>#<PR>` entry holds it.

**A closed bead protects nothing, and the report says which.** `readQuarantineState` keeps the
entries whose bead is closed, so a refusal can say "the file holds it under `nocx-x` — a
closed bead protects nothing" instead of "no bead quarantines this", which would send the
reader to file a second entry for one already there.

**Every quarantined failure prints itself, with its bead**, and the job states that it passed
with them. A quarantine nobody can see becomes silence, and silence is what the rules against
retries exist to prevent.

## Consequences

- A known flake no longer blocks a pull request's merge; an unknown one still does, and so
  does any failure outside a test.
- The quarantine is read at the pull request's head, so a change that adds an entry is
  protected in the same run — and its bead's row has to be in that change's
  `.beads/issues.jsonl`, or `readQuarantine` refuses the entry as a bead that does not exist.
- The `ci-frontend` job (vitest) is deliberately not gated: nothing has quarantined a vitest
  test, and a check with no entries to honour would be a step that can only print. Adding it
  is the same three lines when the first entry appears.
- The local runners (`make ci-*`, `e2e/run-in-container.sh`) stay red on a quarantined test.
  The gate is the pull request's, not the developer's: a red local run is the moment the flake
  is filed, and a local pass that hid it would be the same silence in a different place.
- A job that times out is still red. A timeout names no test, so nothing can quarantine it.
- One owner means the promotion's reading of a job changed with it: `verifyCI` now asks
  `judgeFailures` the same question the gate asks, and its evidence still names every
  quarantined failure it accepted.
