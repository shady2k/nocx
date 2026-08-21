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

# Task: environments, the route, and secret variables

**Task id for your sentinel: `route-3e77`**

**You own (create these exact files, nothing else):**
`internal/apicoll/environment.go`, `internal/apicoll/substitute.go` and their tests;
`internal/apisend/ssh_dialer.go`, `internal/apisend/auth.go` and their tests;
`internal/apibind/store_impl.go` (or several files under `internal/apibind/`) and their tests.

**Another worker owns, do not touch:** `internal/capability/`, `internal/transport/`,
`internal/app/`, `contracts/`, `frontend/`. Also do not edit existing files in
`internal/apicoll/` or `internal/apisend/` other than the ones you create — if you need a
seam in `sender.go`, **escalate rather than editing it**.

Read `.internal/specs/2026-08-21-api-testing-design.md` §6.5, §7.1, §8, §12.2.

## Part 1 — environments and substitution

`apicoll.Environment` and `apicoll.Route` already exist in `collection.go`. Write the reader
for `environments/*.json` and the substitution.

- `{{var}}` resolves in **URL, headers, query and body** — a test for each. One that works
  in three places out of four is the shape that ships.
- **An unresolved variable blocks the send and names itself.** Not the literal braces sent
  to the server, not an empty string quietly substituted. The empty-string version is worse
  than the failure it hides: `Authorization: Bearer ` is a plausible request that teaches
  the wrong lesson about why it was rejected.
- The route lives on the environment, never on a request. **Write a test asserting
  `apicoll.Request` has no route field** — reflect over the struct if that is what it takes.
  The model, not only the UI, must make a per-request route inexpressible.

## Part 2 — the SSH dialer, and its limit stated honestly

```go
func NewSSHDialer(lease ssh.TunnelConn, dialTimeout time.Duration) apisend.Dialer
```

**Read `internal/ssh/ssh_tunnel.go:116` before you start.** `Dial` is `Dial(addr string)` —
it returns a `net.Conn`, which is what matters, but it is **not** `DialContext`'s signature
and it takes **no context**. So:

- The adapter supplies the network parameter and drops the context. Say so in a comment.
- **Cancellation cannot interrupt a blocked remote dial.** What you guarantee instead is a
  bounded `dialTimeout`, and a connection that arrives after cancellation is **closed and
  never produces a run**. Both directions get a test.
- **A spent lease refuses rather than dialling locally.** A silent fallback to a local
  dialer would send a production request around its bastion, which the whole design of §6.5
  exists to make impossible. Test it.
- Taking a lease must not open a second SSH connection when the pool key matches (AD-7:
  `session` references a pooled connection, never owns it). Assert the connection count
  across a send.

## Part 3 — the binding store, and auth

`internal/apibind/store.go` declares `Store`. Implement it over `storage.DocumentStore`
(see `internal/snippet/store.go` for the house pattern: a versioned document with its own
`storage.Module`) plus the vault for values.

- `Bind` writes the **value first, the binding second**. An interruption then leaves an
  unreachable value rather than a binding pointing at nothing. Test both orders of failure.
- `UnbindCollection` is §12.2's **closing event**: deleting a collection removes its
  bindings and the values only those bindings referenced. Test that a value shared with
  another binding survives.
- **Do NOT reuse the first draft's reasoning about `Reconcile`.** It was wrong:
  `internal/vault/journal.go:119` clears the journal entry and **keeps** the secret whenever
  a catalogue record exists, and `CreateNamed` writes value and record in the same save
  (`vault.go:1122`). So a crashed create is treated as complete, and any cleanup here is
  yours, not reconciliation's. Read those two sites before you write the ordering.

Auth in `internal/apisend/auth.go`: bearer, basic, api-key, each built from a **variable
name** resolved through `apibind.Store`, never from an identifier in a file.

**The test that matters most:** a request whose `Auth.Var` is a raw vault identifier
belonging to an SSH profile must resolve **nothing** — it is an unknown variable name, and
the send is blocked as unresolved. The point is that the file's content is irrelevant, not
that a check caught it. Write it as an assertion about the absence of a path, not about a
guard firing.

## Verify

```bash
gofumpt -w internal/apicoll internal/apisend internal/apibind
go vet ./internal/apicoll/... ./internal/apisend/... ./internal/apibind/...
golangci-lint run ./internal/apicoll/... ./internal/apisend/... ./internal/apibind/...
go test ./internal/apicoll/ ./internal/apisend/ ./internal/apibind/ -race -count=1
```

## When done

Write `REPORT-route-3e77.md` at your worktree root: what you built, the exact cancellation
guarantee you can and cannot make and how each is tested, the vault ordering you chose and
why, exact commands and results, and anything unverified.

Then print exactly, on its own line:

    WORKER_DONE::route-3e77

If blocked:

    WORKER_BLOCKED::route-3e77 <one line why>
