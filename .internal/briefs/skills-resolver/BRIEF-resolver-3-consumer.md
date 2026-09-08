# Round 4 — the consumer the contract change broke

Your work is accepted. Findings 1–4 are done, the scoped Go gates and
`contracts:check` are green, and I re-ran them myself on the merged tree.

Then the pre-commit hook refused the commit, and it is right. `npx eslint .` in
`frontend/` reports 22 errors, all in one file:

    frontend/src/agent-approval-prompt.tsx

That surface reads `install().url`, `.name`, `.description`, `.digest` and
`.files`. Those fields moved into `install().skills[]` when the approval became
a set, so every read is now against a type that cannot be resolved.

**This is my mistake, not yours.** BRIEF-resolver-3.md told you to stay out of
`frontend/src` and deferred the window to `nocx-295uk.4`. That was wrong: a
contract change lands together with the consumer it breaks, or the commit cannot
pass the gate at all. So `frontend/src/agent-approval-prompt.tsx` is now IN
scope, and only that file.

## What to do, and what NOT to do

**Make the existing window compile and behave exactly as it does today**, reading
the new shape. Nothing more.

- The window today shows ONE skill. Keep it that way: read the first element of
  `install().skills` and render precisely what it renders now.
- **Do not build the set UI, and do not surface the route facts** — `source`,
  `destination`, `originsDiffer`, `ref`, `commit`. Those are `nocx-295uk.4` and
  somebody is going to design what a person reads there. A half-designed version
  landing now is worse than none, because .4 would have to undo it first.
- Do not silence the rule. `eslint-disable`, `any`, a cast to the old shape, or
  a `@ts-expect-error` all turn a real report into silence, which is the failure
  mode AGENTS.md's testing rules exist to prevent. The type is correct; the reads
  are what must change.
- Keep the file's prose comments accurate. Several of them describe the block as
  ONE resolved skill (lines around 103–130 and 606–612). Where a sentence is now
  false, correct that sentence — do not delete the paragraph, and do not write
  the .4 reasoning into it.

If a read genuinely cannot be expressed against the new type without deciding
something that belongs to .4, stop and say which read and what the decision is.
That is a report, not a failure.

## Verification

```
cd frontend && npx eslint .
cd frontend && npm run contracts:check
cd frontend && npm run typecheck        # if the script exists; say so if not
cd frontend && npx vitest run src/agent-approval-prompt        # and any spec that names this file
```

Then, because the Go side is already green and must stay so:

```
go test ./internal/agenttools/ ./internal/assistant/ ./internal/transport/
```

`npx eslint .` must report **0 errors**. Anything you cannot make zero without
crossing into .4, name it.

**Baseline before blame.** If eslint reports something in a file you did not
touch, prove whether it was failing before your edits.

## Rules, unchanged

No commit, no push, no branch, no tracker commands. Nothing in `internal/skill`.
`frontend/src/agent-approval-prompt.tsx` is the ONLY file in `frontend/src` you
may edit, plus whatever `contracts:check` regenerates. No repo-wide formatting
run.

## Report

Numbers, not adjectives: eslint errors before and after. Every read you changed
and what it now reads. Anything you left for .4, in one line each.

## When you are done

Print exactly, on its own line:

    WORKER_DONE::resolver-consumer-4b71ce

If you cannot finish:

    WORKER_BLOCKED::resolver-consumer-4b71ce <one line why>
