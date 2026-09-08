# Resume — task 3, second rework round

A previous worker in THIS worktree did most of the first rework and its pane was
killed mid-run (system memory pressure, not your predecessor's fault). Its work
is in the tree, uncommitted, and it is sound — **do not redo it and do not
re-derive its analysis.** `pwd` first; this worktree is the only tree you write
to.

Read `.internal/briefs/skills-resolver/BRIEF-resolver-3.md` for the task, and
`.internal/briefs/skills-resolver/BRIEF-resolver-3-rework.md` for the four
review findings. Two of the four are already done — verify that for yourself
rather than trusting this sentence, then finish the other two.

**The repository moved under this branch while you were away.** `main` was
merged in and the tracker is now `br`, not `bd`; AGENTS.md and CLAUDE.md were
rewritten. It changes nothing about your task — you do not touch the tracker
either way — but if a command or a path in an older brief no longer exists,
AGENTS.md as it stands now is what is true.

## Already done — confirm, do not repeat

- **Finding 1**, the destroyed generated type: the `oneOf` is gone from
  `contracts/agent.approvalRequested.schema.json` and
  `frontend/src/generated/agent.approvalRequested.ts` names its fields again.
- **Finding 2**, two shapes for one concept: `ApprovalInstall` now carries route
  facts and a list.

Confirm both by reading the files. If either is only half done, finish it.

## Still open

### Finding 3 — the deleted wording in `skills.install`'s schema

`contracts/tools/skills.install.schema.json` still has the descriptions
stripped: the result's `status`, `name`, `provenance` and `enabled` carry no
prose at all, and the file's title description is a short replacement rather
than the original. The deleted sentence about an installed skill arriving
switched OFF is load-bearing for how the model reports an install.

The original is `git show HEAD:contracts/tools/skills.install.schema.json` — the
merge did not touch that file, so HEAD still has it. Restore every description
verbatim and add the new fields beside them.

### Finding 4 — the `url` field must not make the caller judge enumerability

`contracts/tools/skills.resolve.schema.json`'s `url` description is still:

    "A public repository, forge page or skill address; the tool decides whether
     it can enumerate it."

That is the shape of wording that talks a model out of calling. Compare
`skills.install`'s `url`, which tells the caller to pass the address "exactly as
they gave it", that they are "not expected to know" what it answers with, and
that they "must not withhold the call". Yours must do the same job: pass the
address as the person gave it or as a page named it, and do not judge in advance
whether it can be enumerated — refusing is the tool's job, and an unsupported
address is not a refusal but "I cannot enumerate that", after which the caller
falls back to `skills.install`.

`internal/agenttools/skills_resolve_test.go` already has
`TestSkillsResolveWordingPassesThroughUnknownAddress`. Read it: if it passes
against the thin wording above, it is not asserting what it was written for, and
it needs to fail first. Model it on
`internal/agenttools/skills_install_wording_test.go`.

## Verification

The full list from BRIEF-resolver-3.md, re-run from scratch — the earlier green
run predates the merge and predates these changes, so it is evidence about
neither. Include `internal/transport`.

After `npm run contracts:check`, OPEN
`frontend/src/generated/agent.approvalRequested.ts` and confirm by eye that
`install` names its fields. A passing check is not that confirmation.

**Baseline before blame.** The merge brought in 26 commits of main. If a gate
fails on something outside your files, prove whether it failed before your edits
and say which.

## Rules, unchanged

No commit, no push, no branch, no tracker commands. Nothing in `internal/skill`.
Nothing in `frontend/src` except what `contracts:check` regenerates. No
repo-wide gates and no repo-wide formatting run.

## Report

Numbers, not adjectives. Paste the `install` type as it stands. Disclose every
edit this brief did not ask for. Say what you could not verify.

## When you are done

Print exactly, on its own line:

    WORKER_DONE::resolver-round3-8e2f04

If you cannot finish:

    WORKER_BLOCKED::resolver-round3-8e2f04 <one line why>
