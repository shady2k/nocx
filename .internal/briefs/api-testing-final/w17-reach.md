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

---

# Task: make the happy path reachable by a person

**Task id for your sentinel: `reach-9e04`**

**You own:** `frontend/src/api/**`, `contracts/api.request.send.schema.json` and its
generated type, `internal/transport/ws_api_handlers.go`, `internal/capability/api.go`.
**Others own, do not touch:** `internal/apiimport/**`, `internal/apicoll/**`, `e2e/**`,
`frontend/src/ui/**`.

Beads: `nocx-6siis`, `nocx-pnvnn`. **Both P0.**

## Where these came from, because it changes how you should work

The epic's end-to-end check found them. Every unit test on both sides was green; the
scenario a person performs was impossible. AGENTS.md's first testing rule was bought by
exactly this — a connection manager shipped with **no way to create a group** while 1041
frontend tests passed, every one of them mounting a component and asserting what it
rendered.

**So a test you write by reading the implementation cannot close either of these.** Drive
the seam a person reaches: the control exists, it is enabled from the state a user starts
in, activating it reaches the client, and the result appears afterwards.

## Defect 1 — the Import section has no entrance (`nocx-6siis`)

`api-pane.tsx` renders:

```tsx
<Section title="Import" collapsible open={false} onToggle={() => undefined}>
```

A literal `false` and a no-op. Measured: `aria-expanded` stays `"false"` across 14 polls
after a real click, in two container runs.

Note what made it invisible: **all four `importPostman` call sites in `frontend/src` drive
the client or the store.** Nothing drove the entrance. Your test must.

- The section opens and closes from a click and its state is real.
- A test drives import **through the UI** — entrance, fields, confirm — and would have
  failed before this fix. Say so in your report.

## Defect 2 — the environment never reaches the backend (`nocx-pnvnn`)

`envRelPath` appears **nowhere** in `frontend/` or `contracts/`. The send path resolves
variables against an environment the renderer has no way to name, so a collection whose URL
is `{{baseUrl}}/…` — nearly every Postman export — fails from the product while working
perfectly over the control plane.

Measured: `-32603 "{{baseUrl}}/users" is not an absolute URL`; with an absolute URL,
`the auth variable "token" is not bound in this environment`.

- The contract for `api.request.send` carries the chosen environment. Regenerate the type
  with `npm run contracts`; never hand-edit it.
- **The panel gets a way to CHOOSE an environment**, and a test drives that choice through
  the UI. A dropdown that exists but cannot be reached is the same defect one layer up.
- The environment is named the way the binding key names it — the backend keys bindings by
  `(collection, environment, variable)` and the environment is the **name inside the file**,
  not its path. Read `internal/capability/api.go`'s `SendInputs` before choosing what to
  send; a second answer to "which environment is this" is what the previous worker
  deliberately avoided.

## The acceptance that matters

`e2e/api-testing.spec.ts` exists on this branch and is **red**. It is the epic's DONE WHEN.
You may not edit it. Run it:

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/api-testing.spec.ts
```

It also needs `nocx-1qtef` (the manifest mismatch, another worker) to pass fully. So:
**get it past your two steps** — the Import section opening, and a `{{baseUrl}}` request
resolving — and report exactly which step it reaches and the message it stops on. If it
stops on the manifest, that is the other worker's and you are done.

## Verify

```bash
cd frontend && npm run typecheck && npm run lint && npm run test -- api && npm run contracts:check
cd .. && go vet ./internal/transport/... ./internal/capability/... && go test ./internal/transport/ -race -count=1
```

## When done

`REPORT-reach-9e04.md`: the e2e step you reached and its message, confirmation your UI test
fails without the fix, exact commands and results.

Then print exactly, on its own line:

    WORKER_DONE::reach-9e04
