# ADR-xf4p9fzd — A flaky test is quarantined by its open bead

- **Status:** Accepted (owner decision, 2026-10-29)
- **Date:** 2026-10-29
- **Related:** [ADR-0097](0097-trunk-based-releases-with-a-nightly-channel.md) (the
  promotion verifies each merged PR's own CI instead of re-running it),
  AGENTS.md "There is no 'not ours'" (a flake is a defect, never weather).

## Context

The promotion to stable refuses any merged PR whose head CI run is not green. On
2026-10-29, while v0.8 was being promoted, the product suites failed one or two
unrelated tests in almost every run: 17 flakes were fixed that day (9 product races,
8 tests that read an asynchronous result immediately), and each new run found another.
With roughly 340 e2e tests across two browsers plus the Go packages, a per-test failure
rate of half a percent already makes a fully green run about one in five, which is what
the day measured. A PR merged red by the owner (#557) could then never become green:
its head is fixed and a re-run of a merged PR cannot pass `ci-backlog` (its range is
empty, nocx-1ebrs.66.1.32). Every later stable release would have been refused.

Large projects reach the same place and answer it the same way: detect the flake,
isolate it under an owner, and keep it visible until it is fixed — Google's flakiness
tracking, Chromium's retry-without-patch and TestExpectations, Mozilla's intermittent
bugs, Playwright's `flaky` status, CI "early flake detection" and quarantine. None of
them treat a pass on retry as green; all of them stop one known flake from blocking
every unrelated change.

## Decision

A test whose failure is a known flake is **quarantined by its bead**, and only while
that bead is open.

- `docs/testing/quarantine.json` lists `{test, bead}`. A key is `go:<TestName>` or
  `e2e:<spec path> › <title path>`, the names the suites print on failure, without line
  numbers. The bead is the flake's own task; the file is not a place to park tests
  nobody owns.
- **A closed bead protects nothing.** Closing the bead ends the quarantine without
  editing the file; a bead that does not exist refuses the file.
- **The promotion** (`scripts/release/promotion*.mjs`) reads every attempt of a PR's head
  CI run and each failed job's log. A required job that is not green still passes when
  every test it failed, in any attempt, is quarantined — or when its failures named no
  test and the same job passed in another attempt of that run (a retry that classified
  an infrastructure flake). A cancelled job, an unquarantined test, or a failure that
  names no test and never passed stays red.
- **A retry is for classification, never acceptance.** A test that passes on a second
  attempt is a flake to file and quarantine, not a green result.

## Consequences

- One known flake no longer blocks every release; an unknown one still does, and so does
  any failure outside a test.
- The quarantine is evidence, not silence: the promotion's `evidence.json` carries the
  attempts and failures it accepted, and every entry names a bead that must still be
  worked. The AGENTS.md rule that a flake is fixed, not retried, is unchanged.
- CI itself still fails a PR on a quarantined test. Making the PR check non-blocking for
  quarantined failures, and running changed tests repeatedly on the PR so a new flake
  cannot land, are separate tasks.
