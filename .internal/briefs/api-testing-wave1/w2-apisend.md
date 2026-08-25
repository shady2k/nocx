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

# Task: extract the HTTP policy, then send — bounded and streamed

**Task id for your sentinel: `apisend-4b90`**

**You own:** `internal/httppolicy/` (new), `internal/apisend/` (new), and
`internal/assistant/httpguard.go` (edit).
**Others own, do not touch:** `internal/apicoll/`, `internal/apiimport/`, `internal/apibind/`,
`internal/transport/`, `internal/app/`, `frontend/`.

`internal/apicoll/collection.go` exists and is another worker's file. **Read it, import it,
never edit it.**

Read `.internal/specs/2026-08-21-api-testing-design.md` §7 and §12.3.

## Part 1 — extract the guard, without changing what the assistant does

`internal/assistant/httpguard.go` holds a guard this feature needs. Its header comment gives
four reasons the check cannot be a form validator. **Carry that comment across verbatim** —
a reader who does not know those reasons will "simplify" it back into a form check.

The extraction is **not** "add a policy parameter". Read the file first: at `:140` it
resolves locally, and at `:208` and `:226` it dials with a concrete `net.Dialer`. That is
correct for the assistant and cannot be reused as-is by a caller whose hostname must be
resolved by a remote SSH server. So extract **two things**: a policy engine, and a
route-specific resolve-and-dial capability. The assistant keeps its existing concrete
behaviour and its constructor stays locked to it.

**`go test ./internal/assistant/ -race` must pass with its test files UNEDITED.** If you
find yourself wanting to edit an assistant test, stop and escalate — that is the signal the
extraction changed the assistant's policy, which is not allowed.

## Part 2 — the sender

```go
type Dialer interface {
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}
type Key struct { RouteID, CookieScope string }   // what a cached client instance is keyed by
type Sender interface {
    Send(ctx context.Context, r apicoll.Request, k Key) (Response, error)
}
type Response struct {
    Status     int
    Headers    []apicoll.Header
    Text       string // decoded, valid UTF-8; EMPTY when Binary
    Binary     bool   // never base64
    Lossy      bool
    Truncated  bool
    Size       int64
    Timings    Timings
    TLSVersion string
    RemoteAddr string
}
type Timings struct { DNS, Connect, TLS, TTFB, Total time.Duration }
```

Client instances are **immutable and cached by `Key`**. One shared mutable client cannot
hold a per-environment cookie jar and a per-call dialer at the same time without leaking one
environment's cookies or route into another's request.

## Acceptance criteria — assertions

- Status, headers, decoded body and a non-zero `Total` against an `httptest` server.
- **The body is bounded HERE, not in a later task.** The reader stops at the ceiling **plus
  one byte and never buffers the whole body**. Assert peak allocation against a server that
  streams far past the ceiling — a cap applied after reading is not a cap. This is the
  property `files.read` states; read `contracts/files.read.schema.json` for the wording.
- **The ceiling is 2 MiB, inherited from `files.read`, not chosen by you.** A parameter may
  only lower it.
- `Truncated`, `Binary` and `Lossy` are three distinct states with three distinct meanings.
  **A binary body carries empty text, never base64.**
- `http://` to a public address refused; to loopback allowed; checked on the connection and
  on **every redirect hop**, not on the form.
- `Authorization` is dropped on any origin change.
- A failure test for each of: DNS failure, connection refused, TLS handshake failure, server
  closing mid-body. **Each paired with a test that it succeeds on an ordinary machine** —
  AGENTS.md is explicit that "returns an error when…" without its pair is half a test.

## Verify — only these

```bash
gofmt -w internal/apisend internal/httppolicy internal/assistant
go vet ./internal/apisend/... ./internal/httppolicy/... ./internal/assistant/...
go test ./internal/apisend/ ./internal/httppolicy/ ./internal/assistant/ -race -count=1
```

## When done

Write `REPORT-apisend-4b90.md` at your worktree root: what moved where, the exact commands
and results, confirmation that no assistant test file was edited (`git status` output for
`internal/assistant/` proves it), and anything unverified.

Then print exactly, on its own line:

    WORKER_DONE::apisend-4b90

If blocked:

    WORKER_BLOCKED::apisend-4b90 <one line why>
