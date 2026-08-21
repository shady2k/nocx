## Ground rules — read before anything

1. `pwd` first. Your worktree path is stated in your task section; every path you create or
   edit is under it. The spec and plan quote paths from the coordinator's checkout — they are
   repo-relative, resolve them against YOUR root.
2. **Do not commit, push or branch.** The coordinator integrates. Leave your work
   uncommitted in the worktree.
3. **Do not touch beads / `bd`.** The coordinator owns the tracker.
4. **No repo-wide gates.** Other workers are mid-write in neighbouring packages, so
   `go build ./...` or the full test suite will show you THEIR half-written files and you
   will escalate on a phantom. Verify only your own packages, with the exact commands in
   your section.
5. **No formatting runs** beyond `gofmt -w` on files you wrote. Formatting is a final wave.
6. **Do not edit files another worker owns** — the list is in your section. Escalate instead.
7. Read `AGENTS.md` in your worktree before the first edit. Its testing rules are binding,
   especially: a test asserts what a user can do; every external call has a test where it
   fails, paired with one where it succeeds; invariants are stated with BOTH ends.
8. TDD: the failing test comes first, and you run it and see it fail before implementing.
9. Numbers, not adjectives, in your report: counts, exact commands run, every problem you
   saw and deliberately left.

---

# Task: the collection folder — model, format, handle, path safety

**Task id for your sentinel: `apicoll-7f31`**

**You own:** `internal/apicoll/` and nothing else.
**Others own, do not touch:** `internal/apisend/`, `internal/httppolicy/`, `internal/assistant/`,
`internal/apiimport/`, `internal/apibind/`, `internal/transport/`, `internal/app/`, `frontend/`.

`internal/apicoll/collection.go` already exists — the coordinator landed the type skeleton.
**Extend it; do not restructure it.** Two other workers are compiling against those types
right now, so a rename there breaks them silently.

Read `.internal/specs/2026-08-21-api-testing-design.md` §6 and §13.1 in your worktree.

## What to build

A `Service` with this exact surface — two other tasks will be written against these names:

```go
type Service interface {
    Open(root string) (HandleID, Collection, error)
    List(h HandleID) (Collection, error)
    ReadRequest(h HandleID, relPath string) (Request, error)
    WriteRequest(h HandleID, relPath string, r Request) error
}
var Module = storage.Module{Name: "apicoll", Current: 1}   // internal/storage
```

A collection is a folder the user chose — **not** a document under the profile directory.
The manifest lives at the root; one JSON file per request; nested folders are real
directories; `environments/` sits beside them.

## Acceptance criteria — these are assertions, write them as tests

- A folder with a manifest and two request files opens; both list with method and name.
- A folder with no manifest is refused by a named error. Not a panic, not an empty collection.
- **One malformed request file is refused by name and the others still list.** One bad file
  must not hide a collection.
- **A manifest version newer than ours is refused before any decoding**, and the message
  names the version. This is `internal/storage/document.go`'s protocol — read it and follow
  it; an RPC contract does not provide this and is a different boundary.
- `Read(Write(r)) == r` for every request: empty headers, nil body, a `{{var}}` in each
  field, a header whose value contains a colon, a body with a newline.
- **Path safety, one test each, and refused means refused — never silently clamped:**
  - a relative path containing `..`
  - an absolute path
  - a request file that is a symlink pointing outside the root — **not followed**
  - a write through a symlink (`internal/storage/document.go:159` already refuses exactly
    this; read it and reuse the approach rather than inventing a second one)
  - the root replaced between `Open` and `ReadRequest` — reported, not papered over
- `Open` is the only entry point that accepts a `root`. Every other method takes a
  `HandleID`. A caller cannot name a path twice.
- **Decide the default location for a new collection**, implement it, and state the decision
  and its reason in your report. Leaving it open in the code is not an option.

## Verify — only these

```bash
gofmt -w internal/apicoll
go vet ./internal/apicoll/...          # this type-checks _test.go too; go build does not
go test ./internal/apicoll/ -race -count=1
```

## When done

Write your report to `REPORT-apicoll-7f31.md` at the root of your worktree: what you built,
the exact commands you ran with their result, the default-location decision and why, and
anything you could not verify. Silence is not the same as nothing to report.

Then print exactly, on its own line:

    WORKER_DONE::apicoll-7f31

If you cannot finish, print exactly:

    WORKER_BLOCKED::apicoll-7f31 <one line why>
