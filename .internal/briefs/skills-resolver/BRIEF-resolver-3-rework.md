# Rework — task 3, first review

Your diff is in the tree and I have read it. Four things must change. Nothing
below is a new requirement: each is something BRIEF-resolver-3.md asked for and
the diff does not do. Same rules as before — no commit, no push, no beads, no
repo-wide gates, nothing in `internal/skill`, nothing in `frontend/src` except
what `npm run contracts:check` regenerates.

`pwd` first. Same worktree.

## 1. You destroyed the renderer's type, and did not report it

`git diff frontend/src/generated/agent.approvalRequested.ts`. The `install`
block used to be a fully typed object — url, name, description, digest, and a
`files` tuple with per-file findings. It is now:

```ts
install?: { [k: string]: unknown } & ( { [k: string]: unknown } | (null & { [k: string]: unknown }) )
```

Every field is gone. The cause is the `oneOf` you put at the top level of
`install` in `contracts/agent.approvalRequested.schema.json`:
json-schema-to-typescript cannot express that alternation and silently degrades
the whole subtree to an index signature. `npm run contracts:check` passed
because the generated file does match the schema — it matches it as an untyped
bag, which is the exact failure AGENTS.md rule 5 exists to prevent, and
`nocx-295uk.4` is the approval window that has to read this type.

The brief told you to check what `gen-contracts.mjs` does with a `oneOf` before
committing to one, and to say so in your report if it could not express the
alternation. You committed to one and the report does not mention it. That
omission is the more serious half of this finding.

Note the tool schemas under `contracts/tools/` are NOT TypeScript-generated, so
the `oneOf` in `skills.install.schema.json` and `skills.resolve.schema.json` is
fine and stays. This finding is only about `agent.approvalRequested`.

## 2. One shape, not two — this was decided in the brief, not left open

You kept the five single-skill fields (now `omitempty`) AND added a parallel
`Resolution` object carrying its own candidate tree with its own name,
description, url, digest and files. That is two shapes for one concept, with
the surface forced to branch on which half is populated — the thing AGENTS.md's
"look for the existing answer" section forbids, and the thing the brief
pre-empted in the paragraph beginning "Decided in this brief, so you do not
have to decide it".

What was asked for, restated so there is no ambiguity: `ApprovalInstall` is

- the route facts, shared by every skill in the set: where this started, where
  it ended up, that the origin changed, the ref, the resolved commit;
- and a LIST of skills, each with what a single skill carries today plus its
  path inside the repository.

A plain `url` install is a set of ONE, with no route facts. It is not a second
shape; it is the same shape with a one-element list and the route absent.

Do that, and finding 1 dissolves with it: there is no alternation left at the
`install` level, so the generated type comes back typed.

## 3. You deleted wording the brief told you not to touch

`git diff contracts/tools/skills.install.schema.json`. The title description
was replaced with a shorter one, and the result's `status`, `name`,
`provenance` and `enabled` descriptions were deleted outright — including the
sentence explaining that an installed skill arrives switched OFF and that a
successful install does not mean the skill is in use. That prose is the tool's
own wording, it is load-bearing for how the model reports an install, and the
brief said the `url` shape stays "exactly as today and with today's wording
untouched".

Restore every description in that file verbatim from `git show HEAD:` and add
your new fields beside them.

## 4. The wording test half one owes does not exist

`internal/agenttools/skills_resolve_test.go` contains exactly one test,
`TestSkillsResolveDeclarationMatchesFetchBoundary`. That is the declaration
test. The wording test is a different test and the brief called it not
optional and not decoration.

And the field it is supposed to guard does not say the right thing. Today:

    "A public repository, forge page or skill address; the tool decides whether
     it can enumerate it."

Read `internal/agenttools/skills_install_wording_test.go` and the `url` field it
guards. `skills.install` tells the caller to pass the address "exactly as they
gave it", that they are "not expected to know" what it answers with, and that
they "must not withhold the call". Yours must do the same job: pass the address
as the person gave it, or as a page named it, and do not judge in advance
whether it is enumerable — refusing is the tool's job, and an unsupported
address is not a refusal but "I cannot enumerate that", after which the caller
falls back to `skills.install`.

## Verification

The same list, in full, from BRIEF-resolver-3.md — including
`internal/transport`, which is where the over-the-wire test lives and where
finding 2 will move bytes. Re-run all of it; a green run from before these
changes is not evidence about them.

After `npm run contracts:check`, OPEN
`frontend/src/generated/agent.approvalRequested.ts` and confirm by eye that
`install` names its fields. A passing check is not that confirmation.

## Report

Numbers, not adjectives, and paste the `install` type as it stands after your
change. Disclose every edit this rework did not ask for. If you believe any of
the four findings is wrong, say which and why rather than working around it.

## When you are done

Print exactly, on its own line:

    WORKER_DONE::resolver-rework-51ab7d

If you cannot finish:

    WORKER_BLOCKED::resolver-rework-51ab7d <one line why>
