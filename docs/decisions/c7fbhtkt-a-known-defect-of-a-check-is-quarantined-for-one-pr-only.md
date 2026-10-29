# ADR-c7fbhtkt — A known defect of a check is quarantined for one PR, never for the check

- **Status:** Accepted (coordinator decision under ADR-xf4p9fzd, 2026-10-29; the owner may reverse it)
- **Date:** 2026-10-29
- **Supersedes:** only [ADR-xf4p9fzd](xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md)'s
  sentence that a quarantine key is a test name. The rest of ADR-xf4p9fzd stands.

## Context

ADR-xf4p9fzd quarantines tests by name and lets a red job whose failure names no test
pass only when the same job passed in another attempt. PR #557's head commit
(93503c730) was merged seconds after it was pushed, so `ci-backlog` ran against a main
that already contained it: its range collapsed to `head..head` and
`commit-links.mjs` refused an empty range in both attempts (nocx-1ebrs.66.1.32). Every
other step of that job passed, and the push gate had checked the same commits' links
(0 problems). No test is involved, so no test key can describe it, and a re-run cannot
change it: the head is fixed and the defect is in the check.

## Decision

A quarantine key may also be `job:<job name>#<PR number>`: it accepts a failure that
names no test **in exactly that job of exactly that PR**, while its bead is open. There
is no key for a job across PRs: a defect in a check is fixed in the check, and until it
is, each PR it hits is listed by number, under the defect's bead.

## Consequences

- #557 can be promoted past under nocx-1ebrs.66.1.32, and the next PR the same defect
  hits needs its own entry rather than riding on this one.
- Fixing nocx-1ebrs.66.1.32 and closing it ends every such entry at once.
