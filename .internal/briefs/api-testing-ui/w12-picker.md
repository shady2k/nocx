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

# Task: a directory picker, and an honest absence

**Task id for your sentinel: `picker-7a3d`**

**You own:** `internal/transport/ws_dialog.go` and its tests, `internal/app/app.go` (the
adapter wiring only), `contracts/dialog.openDirectory.schema.json`,
`frontend/src/generated/dialog.openDirectory.ts` (regenerate, never hand-edit),
`frontend/src/dialog-client.ts`.
**Another worker owns, do not touch:** `frontend/src/api/**`, `frontend/src/ui/**`,
`frontend/src/sidebar.tsx`, `frontend/src/main.tsx`.

Bead: `nocx-39jek`.

## What exists, and what to extend

`internal/transport/ws_dialog.go:28` declares `DialogService` with **`OpenFile` alone**.
**Extend that interface; do not add a second service** — one owner for "open a native
dialog".

Read the comment above it before you write anything. It carries a cancellation contract
that is not obvious and was clearly bought by something: an adapter MAY observe `ctx.Done`
where the native API allows dismissal, and where it does not the adapter MUST return
normally while the transport keeps the capability busy, refusing every call from every
connection until it does. **`OpenDirectory` inherits that contract verbatim.** Say in your
report that you kept it.

```go
// OpenDirectory opens the platform directory picker and returns the chosen
// ABSOLUTE path, or "" when the user cancelled.
OpenDirectory(ctx context.Context) (string, error)
```

Add `dialog.openDirectory` as a JSON-RPC method with its contract schema
(`additionalProperties: false`, explicit `required`) and **both** conformance tests,
including the one off the real socket.

## The absence is the part that matters

The dev-web harness has no Wails at all, and **that is the configuration this was found
in**. So the method answering `-32601` is the ordinary case, not an edge one:

- Absence is reported as `-32601`, exactly as `dialog.openFile` does. A test asserts it.
- A test asserts the busy behaviour: while an adapter has not returned, a second
  `dialog.openDirectory` from **another connection** is refused rather than queued.
- A cancelled picker returns `""` with no error, and is distinguishable from a failure.

## Verify

```bash
gofumpt -w internal/transport internal/app
go vet ./internal/transport/... ./internal/app/...
golangci-lint run ./internal/transport/... ./internal/app/...
go test ./internal/transport/ -race -count=1
cd frontend && npm run contracts && npm run contracts:check && npm run typecheck
```

`internal/app` has four tests that fail on this machine and pass in CI (`nocx-58gq`,
`nocx-65v6`) — measure the baseline before blaming your change.

## When done

`REPORT-picker-7a3d.md`: the interface change, confirmation the cancellation contract is
unchanged, exact commands and results.

Then print exactly, on its own line:

    WORKER_DONE::picker-7a3d
