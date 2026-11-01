# The test quarantine

`docs/testing/quarantine.json` lists the tests a known defect makes red, each held by the
open bead that owns it. The decision is
[ADR-xf4p9fzd](../decisions/xf4p9fzd-a-flaky-test-is-quarantined-by-its-open-bead.md); a
pull request's own CI honours the same file
([ADR-0bawnmfg](../decisions/0bawnmfg-a-pull-requests-ci-honours-the-test-quarantine.md)).

## An entry

```json
{ "schema": 1, "tests": [{ "test": "go:TestName", "bead": "nocx-…" }] }
```

- `test` is the key the suite prints on failure, without line numbers:
  `go:<TestName>`, `e2e:<spec path> › <title path>`, or `job:<job name>#<PR number>` for a
  failure that names no test in one job of one pull request.
- `bead` is the flake's own task, and it must stay **open**: closing the bead ends the
  protection without editing this file, and a bead that does not exist refuses it.
- The entry is evidence, not a fix. The bead still has to be worked.

## Committing an entry

Add the entry with the key the suite printed, then commit it under the **bookkeeping task
that publishes it** — not under the flake's bead
([ADR-h1n5ren6](../decisions/h1n5ren6-a-quarantine-commit-names-the-bookkeeping-task.md)).
The flake's id lives in the entry; if the commit body mentions it, that is a reference and
not a link.

The reason is the gate, not taste. `merged-but-open`
(`.githooks/backlog-gate/check.mjs`) reads a leaf as delivered when any main-line commit
**header** names it. The adapter (`landed` in `.githooks/backlog-gate/adapter.mjs`) hands
git's whole message to `linkedIds` (`.githooks/backlog-gate/commit-links.mjs`), which reads
only the header paragraph. It never reads a diff or the contents of this file, so the id in
an entry is invisible to it while the same id in a subject is a delivery claim. Naming the
flake in a quarantine-only commit therefore makes the open bead look landed and makes every
later push inherit `merged-but-open`.

`commit-links.mjs` refuses that case: when the change set only adds entries to this file
and the header names one of their beads, it reports a link problem and says to name the
bookkeeping task. A commit that also changes the flake's test or the product may name the
flake — it might be the fix.
