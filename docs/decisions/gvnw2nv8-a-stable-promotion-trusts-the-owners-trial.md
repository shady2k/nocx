# ADR-gvnw2nv8 — A stable promotion trusts the owner's trial, not a re-audit of merged CI

- **Status:** Accepted (owner decision, 2026-11-13, «И зачем такие сложности? Я же сам
  вручную делаю промоушн. Ответственность на мне.»; `nocx-vh2hr.1.30`)
- **Date:** 2026-11-13
- **Supersedes:** only [ADR-0097](0097-trunk-based-releases-with-a-nightly-channel.md)'s
  "it checks via the API that CI was green on that sha" (its shape's point 4 and the same
  clause in its decision); only the promotion clause of
  [ADR-xf4p9fzd](xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md) and of
  [ADR-0bawnmfg](0bawnmfg-a-pull-requests-ci-honours-the-test-quarantine.md);
  [ADR-q0bqxcev](q0bqxcev-a-past-runs-quarantine-is-judged-at-that-run.md) whole. The
  test quarantine itself, its key form ([ADR-c7fbhtkt](c7fbhtkt-a-known-defect-of-a-check-is-quarantined-for-one-pr-only.md)),
  the PR CI gate that honours it, the nightly channel and the promotion's byte checks
  all stand.

## Context

ADR-0097 made a stable release a promotion of an already-built nightly, and had
`release.yml` verify "via the API that CI was green on that sha" instead of re-running
`ci.yml`. That verification grew into a re-audit of every merged pull request in the range
since the last stable: each PR head's run, each attempt, each failed job's log — about 220
PRs × 13 jobs for v0.8.1 — judged against the quarantine file and the tracker as they are
**at promotion time**.

It refused v0.8.1 three times, and every refusal was about that history rather than about
the bytes being promoted:

1. run 43292929736 — a PR's job failed a test quarantined under a bead that was open when
   the job ran and was closed, its flake fixed, a week later. Fixing a flake made a
   release impossible.
2. run 43307875505 attempt 1 — GitHub answered **HTTP 503** to one of the hundreds of job
   log reads; the log read fine minutes later.
3. the same run's attempt 2 — got past the first bead, then refused an entry removed from
   `docs/testing/quarantine.json` by `ab30e76d3` after PR #597's run. The audit read the
   file at the candidate, so an entry removed after its run was invisible.

Two records were written to keep that audit honest — ADR-xf4p9fzd's quarantine, and
ADR-q0bqxcev's rule that a past run is judged at that run — and each was correct on its
own terms. The error is beneath both: a promotion runs because the owner has already
installed a nightly and tried it, and the range's CI is not evidence about the bytes in
front of us. Nobody re-reads a year of code review to sign an artefact they built and ran;
the trial is the evidence, and the signature and digests prove the bytes are the ones that
were tried.

## Decision

**The promotion checks the candidate, not the history.** It keeps the owner's
authorization (actor, triggering actor, `main`, the confirmable `tried`), the nightly's
signed provenance and that its commit is on `main`, the README status line and
`docs/release-notes/v<version>.md` **at the candidate commit**, the exact payload inventory
the signature covers, the byte-identical copy and the re-signed stable manifest, the
upload → download → digest-compare → publish sequence, and the Homebrew cask. It removes
the per-PR CI audit: no pull request list, no workflow runs, no attempts, no job logs, no
quarantine and no tracker read. `scripts/quarantine.mjs` keeps one caller — the pull
request's own CI — and the code the audit alone used is gone, not left dark.

A pull request's red CI keeps its own meaning: it blocks the merge, in the run that is
happening, under ADR-0bawnmfg. What it no longer does is block a later release of a
nightly the owner tried.

## Consequences

- Fixing a flake cannot make a release impossible, and neither can a transient GitHub 5xx
  on a log nobody needs. The promotion reads no job log at all.
- A defect merged red — the case ADR-0097's clause existed for — is caught where it can be
  seen: the nightly build and its own CI, and the owner's trial before the promotion. It is
  no longer caught retrospectively by a refusal that arrives after the trial has happened.
- `promotion/evidence.json` no longer carries a `ci` array; it carries the release, the
  candidate's workflow run, the README, the notes and the previous version. An evidence
  file from before this change has no reader now and none is needed: `collect` is
  re-run in every release run.
- ADR-q0bqxcev's instant (`readQuarantineState(..., at)`, the per-job quarantine record)
  is deleted with the audit it served. A future promotion-side question about a past run
  needs a new record, not that machinery restored by memory.
