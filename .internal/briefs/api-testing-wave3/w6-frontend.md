## Ground rules — read before anything

1. `pwd` first. Every path you create or edit is under this worktree. The spec and plan
   quote repo-relative paths; resolve them against YOUR root.
2. **Do not commit, push or branch.** The coordinator integrates. Leave work uncommitted.
3. **Do not touch beads / `bd`.** The coordinator owns the tracker.
4. **No repo-wide gates.** Another worker is mid-write in neighbouring files, so
   `go build ./...` or a full suite shows you THEIR half-finished work and you will
   escalate on a phantom. Verify only what your section names.
5. **No formatting runs** beyond the commands your section names.
6. **Do not edit files another worker owns** — listed in your section. Escalate instead.
7. Read `AGENTS.md` first. Binding, especially: a test asserts what a user can do; every
   external call has a test where it fails, paired with one where it succeeds; invariants
   are stated with BOTH ends; and `deadcode` can tell you a symbol is dead but never that a
   feature is wired.
8. TDD: the failing test first, run it, see it fail, then implement.
9. Numbers, not adjectives, in your report. Every suppression with its reason. Every
   problem you saw and deliberately left.

## The gates, in full — this list is complete, nothing else is expected of you

The previous wave was sent back because the brief omitted the linter. It is here now:

```bash
gofumpt -w <your packages>
go vet ./internal/<yours>/...          # type-checks _test.go; `go build` does not
golangci-lint run ./internal/<yours>/...
go test ./internal/<yours>/ -race -count=1
```

All four clean before you print your sentinel. `golangci-lint` runs `gosec` and `govet`
with `shadow`: a shadowed `err` in non-test code may be a real defect, so read both
declarations before renaming anything, and say in your report whether any was real. A
suppression is `//nolint:<linter> // <reason>` with the reason written out. **Never weaken
a test to satisfy a linter** — if a check and an assertion genuinely conflict, stop and say
so.

## Your worktree is SEEDED with uncommitted work

The transport wiring, the `contracts/` schemas and the generated renderer types are present
as **uncommitted changes** in your worktree, put there by the coordinator. They are not on
any branch yet, because they cannot be committed until the renderer imports the generated
types — the dead-exports ratchet refuses them, which is the gate correctly saying "this is
not wired". Treat them as read-only context unless your section says otherwise, and do not
`git checkout`, `git stash` or `git restore` anything: that would delete work that exists
nowhere else.

---

# Task: the API workbench, as a pane

**Task id for your sentinel: `frontend-5a62`**

**You own:** `frontend/src/api/**` (new), `frontend/src/surface-registry.ts` (edit),
`frontend/src/main.tsx` (edit), `frontend/src/styles/components/*` if you add one.
**Another worker owns, do not touch:** everything under `internal/`.

Read `.internal/specs/2026-08-21-api-testing-design.md` §9 in full, and
`frontend/src/ui/README.md` **before you build any control**.

## Read these first — they are the pattern, not an example

- `frontend/src/settings-content.ts` — `SettingsContent extends SolidPaneContent`. The
  registry factory returns a `PaneContent`; a bare Solid component **does not type-check**.
- `frontend/src/solid-pane-content.ts:18` — the base class and the lifecycle you inherit:
  `mount`, `viewportChanged`, `focus`, `dispose`, `setVisible`, `setTarget`.
- `frontend/src/surface-registry.ts` and `frontend/src/main.tsx:348` — how a surface is
  registered, singleton-keyed, and given its client.
- `frontend/src/sidebar.tsx` header comment — the **bottom zone** pattern: an action opens
  a tab and never touches the panel. That is what the API entry does (§9.2), the way the
  Settings gear does.
- `frontend/src/generated/api.*.ts` — the result types, generated from the schemas. **Import
  them.** Do not hand-write a duplicate type; the whole point of the generated file is that
  the renderer's types come from the same schema the Go side is validated against.

## What to build

**One singleton pane** holding, left to right and top to bottom: the collection tree; the
request form (method, URL, headers, body, auth, environment); the list of runs with a
pretty/raw toggle per run.

- `api-client.ts` — framework-neutral JSON-RPC calls, one per method in
  `contracts/api.*.schema.json`. Follow `frontend/src/ports-client.ts` or
  `frontend/src/files/files-client.ts` for the house shape.
- `api-store.ts` — the one list every part of the surface reads. Follow
  `frontend/src/files/files-store.ts`.
- `api-content.ts` — extends `SolidPaneContent`.
- `api-pane.tsx`, `request-form.tsx`, `run-list.tsx` — the views.

**Components come from `frontend/src/ui/`.** A toggle is `Checkbox variant="switch"`; a
titled group is `Section`; a status message is `showToast`. At 90% fit, add the missing
variance to the kit component as a typed `data-*` rather than forking it. A surface may
**place** a kit component (`flex`, `margin`, `width`, `order`) and may never **repaint** it
(`background`, `border`, `color`, `font-*`, `padding`, `box-shadow`). Two epics were spent
unwinding hand-rolled controls inside surfaces; do not add a third.

**A secret value is never a text input.** A bound secret variable renders as a chip
(ADR-0021, see `frontend/src/secret-chip.ts` and `frontend/src/ui/secret-chip.ts`).

## Acceptance criteria — write them as tests a user would recognise

AGENTS.md testing rule 1 is the bar here, and it was bought by a connection manager that
shipped with **no way to create a group** while 1041 frontend tests were green, every one
of them mounting a component and asserting what it rendered. So:

- The activity-bar entry **exists, is enabled from the state a user starts in, and
  activating it opens or focuses the pane** — asserted through the seam a person reaches,
  not by calling the factory.
- Opening a collection from the tree **puts a request in the form**; the assertion is that
  the form shows the method and URL, not that a function was called.
- Pressing Send **reaches the client method** and the run appears in the list afterwards.
- A run shows status, elapsed time and size; a second Send **adds a second run** rather than
  replacing the first.
- The raw toggle shows the request and response text.
- **A binary body shows "binary body, N bytes" and no base64.** A truncated body says it was
  truncated. An empty body and a truncated body do not render alike.
- Singleton: opening the surface twice yields one pane.
- Lifecycle: mount aborted before it completes, `setVisible` before the first measurement,
  `focus`, and `dispose` — one test each. These are what `SolidPaneContent` exists to make
  correct and what a bare component would have got wrong silently.

## Verify

```bash
cd frontend
npm ci                       # this worktree has no node_modules
npm run typecheck
npm run lint                 # includes the dead-exports ratchet; the 8 generated
                             # api.*.ts files are currently NEW violations and your
                             # imports are what clears them. It must end clean.
npm run test -- api
npm run contracts:check
```

**`npm run lint` ending clean is the acceptance criterion that proves the wiring**, in the
same way `-whylive` proved it on the Go side: those 8 generated files are dead right now,
and they stop being dead exactly when the renderer imports them.

## When done

Write `REPORT-frontend-5a62.md` at your worktree root: what you built, which kit components
you used and any variance you added to the kit, the exact commands and results, and
anything unverified.

Then print exactly, on its own line:

    WORKER_DONE::frontend-5a62

If blocked:

    WORKER_BLOCKED::frontend-5a62 <one line why>
