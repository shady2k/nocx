# ADR-q0bqxcev — A past run's quarantine is judged at that run

- **Status:** Accepted (owner decision, 2026-11-12, «Да»; `nocx-vh2hr.1.27`)
- **Date:** 2026-11-12
- **Supersedes:** only [ADR-xf4p9fzd](xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md)'s
  "a closed bead protects nothing" **as the promotion applies it to a merged pull request's
  past CI run**. The rule itself, [ADR-c7fbhtkt](c7fbhtkt-a-known-defect-of-a-check-is-quarantined-for-one-pr-only.md)'s
  widened key, [ADR-0bawnmfg](0bawnmfg-a-pull-requests-ci-honours-the-test-quarantine.md)'s PR
  gate and [ADR-0097](0097-trunk-based-releases-with-a-nightly-channel.md)'s promotion are
  unchanged. A new run still asks today's question.

## Context

The promotion does not re-run a merged pull request's CI; it re-reads the run
(ADR-0097). `scripts/release/promotion-github.mjs collect` builds one quarantine from
`docs/testing/quarantine.json` and the tracker as it is **at promotion time**, and judges
every merged PR's head run since the previous stable against it.

On 2026-11-12 the owner dispatched nightly `v0.8.1-nightly.202611121902` (release run
43292929736). It was refused, nothing published:

```
promotion refused: CI for PR #597 head cc04beb0eb75a511350ebc3574581b4f127e19a9: ci-e2e
(chromium, 2) step e2e-chromium-2 failed e2e:e2e/enhanced-input.spec.ts › enhanced input raw
routing › keys typed while the keyboard is unclaimed still reach the program, which no open
bead quarantines
```

That test is in the file under `nocx-1ebrs.66.1.27`. The bead was created
2026-10-29T12:26Z and closed, its flake fixed, on 2026-11-07T20:56Z. PR #597's CI ran
2026-10-30T03:57Z — the bead was open then, and its own gate accepted the failure under
ADR-0bawnmfg. The promotion refused it because the bead is closed **now**.

Measured on 2026-11-12 against the published tracker store and `docs/testing/quarantine.json`
at `main`: read as of today, one of the file's 17 entries names a bead that is still open
(one filed after that run); read at PR #597's run, 8 were open and the failing test is among
them. So every entry that was fixed and closed since its run becomes a refusal in turn, and
a range with enough history cannot be promoted at all. Fixing a flake — what ADR-xf4p9fzd
exists to encourage — made releasing impossible.

The mistake is a question asked at the wrong moment. "Is this test quarantined" is about a
run that happened once, and the answer that run's own CI gave was true when it gave it.

## Decision

**A merged pull request's failed test is accepted by the promotion when the quarantine
listed it under a bead that was open at the moment that failing job ran** — closed since or
not. The bead's own timestamps answer it: `created_at` no later than that moment, and either
no `closed_at` with a status that is not terminal, or a `closed_at` later than it. A bead
that did not exist then, or was already closed then, protects nothing.

The moment is the failing job's own, from the API object the collector already fetches:
`started_at`, else `completed_at`, else the run's `run_started_at`, else its `created_at`.
A job with no time at all refuses rather than defaulting to now.

**One owner, as ADR-0bawnmfg requires.** `readQuarantineState(json, issues, at)` in
`scripts/quarantine.mjs` answers "open at that instant"; `readQuarantine` forwards to it and
a new run — a pull request's CI, and the nightly's — still calls it through
`scripts/quarantine-gate.mjs` without an instant, and gets today's status-only answer. The promotion resolves the
read per judged job in `collectCI` and stores it on that job (`quarantine: { open, closed,
at }`), because the saved `evidence.json` is rejudged by `verifyCI` during `prepare` with no
tracker in hand; a judged job that does not carry its read is refused rather than guessed
at. `judgeFailures` still decides, on the maps it is given; it names the instant in a
refusal only so the reader can see which moment said no.

Unchanged: an unquarantined failure, a failure that names no test and never passed,
incomplete runner evidence, and a cancelled job all still refuse.

## Consequences

- Fixing a flake no longer makes a release impossible. A quarantine entry may stay in the
  file after its bead closes: it protects nothing on a new run (ADR-xf4p9fzd's rule, applied
  where it belongs), and it explains a past run the promotion re-reads.
- The refusal names the moment: "that bead was not open when this job ran
  (2026-10-30T03:57:12Z)", rather than the older "a closed bead protects nothing", which a
  new run still prints.
- `evidence.json` grows by one read per judged job, and is no longer interchangeable with a
  file collected before this change: an entry without those reads is refused, and `collect`
  is the command that produces them.
- The promotion still refuses a run whose own gate accepted nothing — this record changes
  whose clock the question is asked on, not what is accepted.
