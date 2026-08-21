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

# Task: the two importers — Postman v2.1 and a curl line

**Task id for your sentinel: `apiimport-2d55`**

**You own:** `internal/apiimport/` and nothing else.
**Others own, do not touch:** `internal/apicoll/`, `internal/apisend/`, `internal/httppolicy/`,
`internal/assistant/`, `internal/apibind/`, `internal/transport/`, `internal/app/`, `frontend/`.

`internal/apicoll/collection.go` and `internal/apibind/binding.go` exist and belong to other
workers. **Read them, import them, never edit them.**

Read `.internal/specs/2026-08-21-api-testing-design.md` §8, §10 and §12.2.
Read `internal/importer/tabby.go` — the existing importer in this repo, and the shape to
follow for treating input as hostile.

## Surface

```go
func FromPostman(r io.Reader) (apicoll.Collection, []apicoll.Request, []apicoll.Environment, []Unsupported, error)
func FromCurl(line string) (apicoll.Request, []Unsupported, error)
func ImportInto(ctx context.Context, fs FS, b BindWriter, dest string, r io.Reader) ([]Unsupported, error)

type Unsupported struct{ What, Why string }
type FS interface {          // injected so "fail at file N" is a test, not a hope
    MkdirTemp(dir, pattern string) (string, error)
    WriteFile(name string, b []byte, perm os.FileMode) error
    Sync(name string) error
    Rename(oldpath, newpath string) error
    RemoveAll(path string) error
}
type BindWriter interface {  // define it here as a narrow consumer contract;
    Bind(ctx context.Context, k apibind.Key, value []byte) error   // apibind implements it later
}
```

## The rule that decides the whole design

**A collection file names a variable, never a secret.** A Postman environment variable of
`"type": "secret"` becomes: its NAME in the environment file, its VALUE handed to
`BindWriter`, and **no identifier anywhere under the collection root**. If you find yourself
writing an id into a file, stop — that is the attack the format exists to make unspellable.

## Acceptance criteria — assertions

**Postman:**

- Folders become directories, requests become files, `{{baseUrl}}` survives as `{{baseUrl}}`.
- A walk of every written file finds **neither the secret value nor any identifier for it**.
- Anything not carried over is returned as `[]Unsupported`, **never only logged**.
- **Atomicity, and each of these is a test using the injected `FS`:** the temp directory is
  created **inside the destination's parent** so the rename stays on one filesystem; an
  existing destination is **refused, not replaced**; files and the staging directory are
  synced before the rename and the parent after it. Inject a failure at: file N, the sync,
  the rename, and after the rename. After each, the destination does not exist.
- **An import never fires a request.**

**curl:**

- One test per flag: `-X`, `-H`, `-d`, `--data-raw`, `--data-binary`, `--data-urlencode`,
  `-F`, `--json`, `-u`, `-b`, `-G`, `-L`, `-k`, `--compressed`.
- **Your own parser for quoting and continuations. No shell, ever.** A line containing
  `$(touch /tmp/pwned)` and one containing backticks parse as **literal text**, and the test
  asserts no shell was invoked — assert on the absence of an exec, not on the absence of
  damage.
- `--proxy`, `--cert`, `-o` are **refused out loud** into `[]Unsupported`. A flag that
  changes the meaning of a request may not be silently dropped.
- A line carrying `-H 'Authorization: Bearer …'` yields a request whose `Auth.Var` is a
  variable name, and the token is offered to `BindWriter` — never written into the request.

## Verify — only these

```bash
gofmt -w internal/apiimport
go vet ./internal/apiimport/...
go test ./internal/apiimport/ -race -count=1
```

## When done

Write `REPORT-apiimport-2d55.md` at your worktree root: what you built, exact commands and
results, the list of Postman features you chose not to carry and why, and anything
unverified.

Then print exactly, on its own line:

    WORKER_DONE::apiimport-2d55

If blocked:

    WORKER_BLOCKED::apiimport-2d55 <one line why>
