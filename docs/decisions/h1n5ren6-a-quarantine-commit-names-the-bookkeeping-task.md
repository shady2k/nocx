# ADR-h1n5ren6 — A quarantine commit names the bookkeeping task, not the quarantined bead

- **Status:** Accepted (implementation of the criterion in `nocx-atpw5.1.8`, 2026-11-01; adds to
  [ADR-xf4p9fzd](xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md), which it does not alter)
- **Date:** 2026-11-01
- **Related:** [ADR-xf4p9fzd](xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md) (a flaky
  test is quarantined by its open bead), [ADR-0bawnmfg](0bawnmfg-a-pull-requests-ci-honours-the-test-quarantine.md)
  (a pull request's CI honours it), AGENTS.md "A commit that only moves other tasks names the
  bookkeeping, not them".

## Context

A quarantine entry is `{test, bead}` in `docs/testing/quarantine.json`, and the bead is the
flake's own task. Adding the entry is not fixing the flake: the bead must stay open, because
closing it ends the protection.

The backlog gate's `merged-but-open` rule reads a main-line commit as delivering a leaf when
its **header** names it. The gate never reads a diff or a file: the adapter asks git for each
main-line commit's message and hands it to the same header reader the commit check uses. So a
quarantine commit whose subject ends with the flake's id — which is the id the entry itself
carries — reads as the flake's delivery. On 2026-10-30 that made a later push fail with
`merged-but-open (6, 5 new)`, and the only status satisfying both rules was `submitted`, a
lie about work nobody had handed in.

## Decision

A commit whose change set **only adds quarantine entries** names the bookkeeping task that
publishes them in its header, never the bead of an entry it adds. The quarantined ids stay
where they are readable: in the entry itself, and in the commit body if the author wants
them named (body ids are references, not links). A commit that changes anything else — the
flake's test, the product, another file — may name the flake, because that commit might be
the fix.

`commit-links.mjs` refuses the narrow case before the vendored `check-commits.mjs` sees it:
the diff is one path (`docs/testing/quarantine.json`), every existing entry survives
unchanged, at least one entry was added, and the header names one of those entries' beads.
The refusal says to name the bookkeeping task, and the check counts as a link problem.

This is the project's rule, not the plugin's: the gate is vendored verbatim and must not be
edited here. The plugin could in principle ignore a delivery claim when the same change adds
the bead's entry, but that would require it to read file content, which it does not, and the
convention above is already this repository's own.

## Consequences

- A quarantine published by a main-line commit no longer marks the flake as landed; the
  `submitted` workaround is unnecessary, and a later push does not inherit a false delivery.
- The rule is deliberately narrow. A commit that adds an entry _and_ edits something else
  passes even if it names the flake, because only the author knows whether that something
  is the fix; a quarantine-only commit that names the flake is refused with the reason.
- #596's four commits on main are not rewritten — a pushed commit cannot be, per AGENTS.md.
  Their entries and statuses stay as they are; the rule applies to the next quarantine.
