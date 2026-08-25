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

# Task: the importer writes the manifest the reader reads

**Task id for your sentinel: `manifest-4c73`**

**You own:** `internal/apiimport/**`, `internal/apicoll/**` and their tests.
**Others own, do not touch:** `frontend/**`, `contracts/**`, `internal/transport/**`,
`internal/app/**`, `e2e/**`.

Bead: `nocx-1qtef`. **P0.**

## What is wrong, measured

`apiimport` writes `collection.json` containing an `apicoll.Collection`.
`apicoll` reads `nocx-collection.json` containing `{schemaVersion, name}` and decodes with
`DisallowUnknownFields`.

So an imported collection **cannot be opened**. End to end this reads:

```
-32602  folder has no collection manifest
-32603  no migration from version 0        (after renaming the file by hand)
```

## Why it survived every gate we have

Both packages are green. Each tests its own half, and **no test anywhere crosses the seam** —
the importer was never asked to produce something the reader would accept. It was found by
the epic's end-to-end check, which is the only kind of check that could have found it.
AGENTS.md says exactly this: `deadcode` and coverage are floors, never criteria, and neither
can report a feature that is missing.

## What to do

Make the importer write what the reader reads: the same filename, the same shape, the same
version the reader understands, through `apicoll`'s own `storage.Module` protocol rather
than a second serialisation.

**Prefer giving `apicoll` the writing.** The reader owns the format; a second package that
knows how to spell that format is the two-owners defect that caused this. If `apiimport`
ends up formatting a manifest itself, say in your report why `apicoll` could not.

## The test that matters

**One test that imports and then OPENS, in the same test.** Not an import test and an open
test — the seam is what is under test, and two green halves are what shipped this.

```
import a Postman export → open the resulting directory → the requests list
```

It must fail against the current code. Run it before your fix and put that output in your
report; a test that passes before the fix is testing something else.

## Verify

```bash
gofumpt -w internal/apiimport internal/apicoll
go vet ./internal/apiimport/... ./internal/apicoll/...
golangci-lint run ./internal/apiimport/... ./internal/apicoll/...
go test ./internal/apiimport/ ./internal/apicoll/ -race -count=1
```

## When done

`REPORT-manifest-4c73.md`: the failing output before the fix, where the writing now lives
and why, exact commands and results.

Then print exactly, on its own line:

    WORKER_DONE::manifest-4c73
