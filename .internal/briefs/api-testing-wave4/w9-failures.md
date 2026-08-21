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

# Task: the failure paths, and a cancellation boundary stated honestly

**Task id for your sentinel: `failures-4f8e`**

**You own:** `internal/apisend/failure_test.go` (new), `internal/apicoll/failure_paths_test.go`
(new), `internal/apibind/failure_test.go` (new), and — only if a production file genuinely
lacks an observable your test needs — a **named, minimal** addition to
`internal/apisend/sender.go`. If you need more than an observable, escalate.

**Another worker owns, do not touch:** `internal/apisend/routes.go`, `internal/apicoll/create.go`,
`internal/capability/`, `internal/transport/`, `internal/app/`, `contracts/`, `frontend/`.

Read `.internal/specs/2026-08-21-api-testing-design.md` §12.

## The rule this task exists to satisfy

AGENTS.md, testing rule 3: **for every external call your code makes, there is a test where
that call fails** — mechanical, cheap, and the single highest-yield check available. And for
every "returns an error when…", a paired "and on an ordinary machine it succeeds", because a
failure test with no success partner once let `contentkey` have tests for every failure path
and none asserting the key is obtainable on a normal machine, where it never was.

## The list

Each of these gets a failure test **and** its success partner:

| Call that fails                                     | Where                                           |
| --------------------------------------------------- | ----------------------------------------------- |
| DNS resolution                                      | apisend                                         |
| TCP connect refused                                 | apisend                                         |
| TLS handshake                                       | apisend                                         |
| the server closing mid-body                         | apisend                                         |
| the pool lease refused                              | apisend                                         |
| the vault sealed                                    | apibind                                         |
| the collection folder unreadable                    | apicoll                                         |
| the collection folder read-only on write            | apicoll                                         |
| a malformed import                                  | apiimport (test only, do not edit that package) |
| the handle invalidated by a root swapped underneath | apicoll                                         |

## Cancellation — assert on an observable, never on a goroutine count

`runtime.NumGoroutine` is **not** an observation: it is polluted by unrelated runtime
goroutines and it is timing-dependent, which AGENTS.md forbids outright ("a test may not
depend on timing — wait on an observable state change, never on a duration").

So the sender must expose a lifecycle completion signal and the test waits on **that**. If
`sender.go` has no such observable, adding one is the single production edit this task
allows: name it, keep it minimal, and say in your report what you added and why nothing
existing would do.

**The boundary must be stated, not implied away.** `tunnelConn.Dial` takes no context, so a
blocked remote dial cannot be interrupted. What you test instead, both directions:

- the bounded dial deadline fires;
- a connection that arrives **after** cancellation is closed and **never produces a run**.

Cancelling in flight must leave no half-written run.

## When done

Write `REPORT-failures-4f8e.md` at your worktree root: the table above with a test name
against each row and its success partner, what observable you used for cancellation and
whether you had to add it, exact commands and results, and any row you could not test with
the reason.

Then print exactly, on its own line:

    WORKER_DONE::failures-4f8e

If blocked:

    WORKER_BLOCKED::failures-4f8e <one line why>
