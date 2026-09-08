# Brief — task 4: the approval window names the whole route

You are implementing `nocx-295uk.4` in the nocx repository. `nocx-295uk.3` put
the route and the set of skills on the wire and left the window rendering the
first skill only. This task is that window. **Frontend only** — the contract and
the Go side are finished and you do not touch either.

**Run `pwd` before anything else.** Your worktree is the only tree you write to.

## Read first, in this order

1. `AGENTS.md` — the testing rules and the commit format bind you. Rule 1, "a
   test asserts what a user can do", decides how the tests below are written.
2. `frontend/src/ui/README.md` and a listing of `frontend/src/ui/`. AGENTS.md
   requires this before any UI element and it is not a formality: the kit
   already has `FactList` (named facts as a `<dl>`, each row `{name, value,
note?}`), `MarkerList` (`data-tone` included | excluded | note), `Section`,
   and `FileReadout`. **A surface may place a kit component and may never
   repaint it.** If you find yourself wanting a bespoke `<div>` with its own
   colours, the component is missing a variant and the variant goes in `ui/`.
3. `.internal/specs/2026-09-06-the-agent-finds-the-source-nocx-resolves-it-design.md`
   §3 — the six lines the window owes, and the transcript they come from.
4. `frontend/src/agent-approval-prompt.tsx` — the whole file, not just the
   install block. Note `facts()`, `statedInTheWindow()`, `installManifest()`
   and `installFileOutcome()`; note that `install()` today returns
   `ask().install?.skills[0]`.
5. `frontend/src/agent-approval-prompt.test.tsx`, the describe block
   "the skill an install resolved to (nocx-ojfuc.2)" — 9 tests you must keep
   passing in MEANING, and the fixture shape you extend.

## What the window owes

Spec §3, in the spec's own order: where this started, where it ended up, **that
those are different origins**, the ref asked for, the commit it resolved to, the
path inside the repository, then for each skill its name and description with
the description prominent, its manifest, and every file with its bytes and its
scan findings.

## Decided here, so you do not decide it

**One skill and nine skills are the SAME component.** There is no "single" mode
and no "set" mode. The window iterates `install.skills` always; a set of one
renders exactly what it renders today, with no counter, no "1 of 1", and no
wrapper that appears only when the list is longer. If your diff contains a
branch on `skills.length`, you have built the two-mode version the bead forbids.

**The route facts are rows of the EXISTING fact list, not a second list.** The
window already states `name`, `source` and `digest` as rows; a separate panel
for the route would be a second vocabulary for the same job, which is the defect
`nocx-pp3y` and `nocx-v0ai` spent themselves unwinding. Rows are `{name, value,
note?}` and the list renders every fact given, in the order given.

**`originsDiffer` is a NOTE, never a row.** A row reading `originsDiffer: true`
makes the person do the comparison the field exists to spare them. It is one
line on the row that states where this ended up, in the product's words — that
the person started somewhere else and this is where it went. The backend already
decided it; the window must not re-derive it by comparing two hostnames itself,
because two answers to one question is exactly what AGENTS.md forbids.

**Absent route, absent rows.** `source`, `destination`, `originsDiffer`, `ref`
and `commit` are optional on the type: a plain URL install has none of them, and
then the window says nothing new at all. A resolution that DID happen but never
left its origin has the facts and no transition note. Those are two different
states and both must be tested.

**`path` belongs to the skill, not to the route** — it is per-candidate on the
wire and it is per-skill on screen.

**Do not restate an argument the window already states.** `statedInTheWindow()`
exists for that; extend it rather than working around it.

## Assertions the bead names, as tests

Write them in `frontend/src/agent-approval-prompt.test.tsx`, through the
rendered window, never by reading the component's internals:

- a resolution that never left its origin shows **no** transition wording, while
  one that changed origin states it;
- one skill and nine skills go through the same component: nine render nine, and
  one renders what it renders today;
- the window still says what it always said — the digest is change detection and
  **never** provenance, and that sentence is still on screen.

Add a fourth for the plain URL install, which carries no route at all: it must
be byte-for-byte the window it is today.

## What you must NOT do

- No `contracts/`, no `internal/`, no Go. The wire is finished.
- No new top-level component outside `frontend/src/ui/` if what you need is a
  kit variant; if the kit genuinely lacks it, add it in `ui/` with its CSS in
  `styles/components/`, a stable identity class, a test and a README row.
- No `eslint-disable`, no `any`, no `@ts-expect-error`.
- No commit, no push, no branch. No tracker commands. Leave the tree dirty.
- No repo-wide gates and no repo-wide formatting run.

## Verification, scoped, with the exact binaries

```
cd frontend && npx eslint .
cd frontend && npx tsc --noEmit -p tsconfig.json
cd frontend && npx prettier --check src/
cd frontend && npx vitest run src/agent-approval-prompt
cd frontend && npm run contracts:check
```

`npx eslint .` must be 0 errors and `tsc` exit 0 — vitest transpiles and does
not type-check, so a green test run is not evidence your files compile.

**Baseline before blame.** If a gate fails in a file you did not touch, prove
whether it failed before your edits and say which.

## Report

Numbers, not adjectives: tests before and after, eslint before and after. Name
every kit component you used and every variant you added. Disclose every edit
this brief did not ask for. Say what you could not verify — silence is not the
same as nothing to report.

## When you are done

Print exactly, on its own line:

    WORKER_DONE::approval-route-6d3f92

If you cannot finish:

    WORKER_BLOCKED::approval-route-6d3f92 <one line why>
