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

# Task: raw with its secret spans, and a cookie jar scoped per environment

**Task id for your sentinel: `rawjar-9d03`**

**You own:** `internal/apisend/spans.go`, `internal/apisend/jar.go`,
`internal/apisend/sender.go`, `internal/apisend/client.go`, their tests, and
`contracts/api.request.send.schema.json` plus its generated type (regenerate, do not
hand-edit).
**Another worker owns, do not touch:** everything under `frontend/src/` **except**
`frontend/src/generated/api.request.send.ts`, which is generated output you must refresh.

Read `.internal/specs/2026-08-21-api-testing-design.md` §11 in full, and §12.3.

## Part 1 — raw rides on the send result, not on a method of its own

The plan proposed `api.request.raw`. **Do not build it.** The raw text belongs to a
particular run, so a second round trip could only fetch the raw of a _different_ send. Add
it to the send result instead, and update the schema and the generated type together.

```go
type Span struct {
    From, To int    `json:"from"`
    Kind     string `json:"kind"`   // "text" | "secret" | "secret-damaged"
    Name     string `json:"name"`   // the NAME, never the value
    Damage   string `json:"damage"` // "truncated, 24 of 214 bytes"; empty unless damaged
}
type Raw struct { Text string `json:"text"`; Spans []Span `json:"spans"` }

// Placement is what the sender knows BECAUSE IT DID THE SUBSTITUTING. It is
// why the request side is a verification rather than a search.
type Placement struct { From, To int; Name, Want string } // Want never crosses the wire

func MarkRequest(text string, placed []Placement) Raw
func SearchResponse(decoded string, used []NamedSecret) Raw
```

**Three states, never two:**

| State                            | Rendered as                                                                     |
| -------------------------------- | ------------------------------------------------------------------------------- |
| the bytes still equal the secret | `secret`, named                                                                 |
| our span, bytes differ           | `secret-damaged`, naming the SHAPE — **and the surviving bytes appear nowhere** |
| not our span                     | `text`                                                                          |

The middle row is the one that makes this safe. A truncated token is a _prefix of a live
token_, so "show the text when it does not match" would print the beginning of a real
credential in the clear.

**The response is a different mechanism and the design says so.** A placement in the request
says nothing about whether a server echoed the bytes back, or where, so `SearchResponse` is
a **bounded known-plaintext search** over the two or three values this request used — not a
sweep against the vault. State its limits in code and pin each with a test: it runs on the
**decoded** body only (after decompression and de-chunking); it does **not** find
transformed spellings, and a base64-wrapped or URL-escaped token is missed **on purpose**,
so the coverage is never overstated; overlapping matches collapse to the longest.

## Part 2 — the jar

- A login that sets a cookie is followed by a request carrying it, with no configuration.
- **The jar is part of the client `Key`** (`Key{RouteID, CookieScope}` already exists). Write
  a test that sends **concurrently** under two environments and asserts neither the cookie
  nor the dialer crossed. This is the concrete reason instances are immutable and cached
  rather than one client mutated per send.
- A `Secure` cookie is not sent over plain http — a test each way.
- **Decide whether the jar survives a restart, implement it, and say which and why in your
  report.** Leaving it open in the code is not an option.

## Acceptance criteria that are easy to fake — do not

- Assert that **no `Span` ever carries a secret value**, in all three states, by inspecting
  the marshalled JSON rather than the struct.
- For the damaged case, assert the surviving bytes are absent from the **whole payload**,
  not just from the span.
- The response search must have a test proving it finds an echoed token, **and** one proving
  it does not find a base64-wrapped one. The second is what stops the first being read as
  more than it is.

## Verify

```bash
gofumpt -w internal/apisend
go vet ./internal/apisend/...
golangci-lint run ./internal/apisend/...
go test ./internal/apisend/ -race -count=1
cd frontend && npm ci && npm run contracts && npm run contracts:check
```

`npm run contracts` regenerates the type from your changed schema; commit nothing, but leave
the regenerated file in place.

## When done

Write `REPORT-rawjar-9d03.md` at your worktree root: the jar-persistence decision and why,
the exact limits of the response search, exact commands and results, anything unverified.

Then print exactly, on its own line:

    WORKER_DONE::rawjar-9d03

If blocked:

    WORKER_BLOCKED::rawjar-9d03 <one line why>
