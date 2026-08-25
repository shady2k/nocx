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

# Task: close the two gaps the deadcode ratchet named

**Task id for your sentinel: `gaps-6b41`**

**You own:** `internal/apicoll/create.go` (new) and its test, `internal/apisend/routes.go`
(new) and its test, plus the edits needed in `internal/capability/api.go`,
`internal/transport/ws_api_handlers.go`, `internal/transport/ws_config_handlers.go`,
`internal/app/app.go`, and one new `contracts/api.collections.create.schema.json` with its
generated type (regenerate with `npm run contracts`, never hand-edit).

**Another worker owns, do not touch:** `internal/apisend/{sender,client,spans,jar,auth,ssh_dialer}.go`
except where this brief says otherwise, and everything under `frontend/src/` except
`frontend/src/generated/`.

## Why this task exists

`deadcode` reported two functions with no caller, and neither is baseline noise. Each is a
feature that does not exist:

```
apicoll.NewDefaultCollection  → a person cannot create a collection
apisend.WithRoutes            → the environment's route never reaches the sender,
                                so "send from inside an SSH connection" is not wired
```

AGENTS.md is explicit that `deadcode` can never tell you a feature is wired — but here it
is telling you two are **not**, and that is exactly what it is good for.

## Gap 1 — `api.collections.create`

`apicoll.NewDefaultCollection(storage.Paths, name)` already exists and already decided the
default location. Give it a caller: a method that mints an empty collection with its
manifest and an `environments/` directory, and returns the same `{handle, collection}` shape
`api.collections.open` returns, so the renderer has one thing to do afterwards rather than
two.

- A name that is empty, contains a path separator, or is `.`/`..` is refused by name.
- Creating over an existing directory is **refused, not merged**.
- The created collection opens: assert through `Open`, not by reading the directory.

## Gap 2 — the route actually reaches the dialer

`apisend.WithRoutes` exists and nothing passes it. Wire it so that:

- An environment whose `Route.Kind` is `"connection"` sends through the SSH dialer for that
  `ProfileID`; `"direct"` sends through the local dialer.
- **The route is resolved per send, from the environment**, and there is no way for a caller
  to pass a route alongside a request — `apicoll.Request` has no route field and a test
  already asserts that. Do not add a parameter that reintroduces one.
- **A `"connection"` route whose profile has no live lease must FAIL the send**, with a named
  error. It must not fall back to the local dialer: that would send a production request
  around its bastion, which is the whole reason the route lives on the environment (§6.5).
  Test it.
- The lease comes from the existing pool (`tunnel.Connector` / `ssh.TunnelConn`); taking one
  must not open a second SSH connection when the pool key matches (AD-7).

## The measurement that closes the task

```bash
node .githooks/check-deadcode.mjs     # or the command that hook runs; read the hook
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apicoll.NewDefaultCollection' ./...
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apisend.WithRoutes' ./...
```

Both must now print a path from `main`. Put both outputs in your report, and put the
before/after NEW-violation counts next to them. If any of the other seven pre-existing
violations can be honestly closed by wiring rather than by baselining, close it and say so;
if one genuinely cannot, say which and why rather than baselining it quietly.

## Verify

```bash
gofumpt -w internal/apicoll internal/apisend internal/capability internal/transport internal/app
go vet ./internal/apicoll/... ./internal/apisend/... ./internal/capability/... ./internal/transport/... ./internal/app/...
golangci-lint run ./internal/apicoll/... ./internal/apisend/... ./internal/capability/... ./internal/transport/... ./internal/app/...
go test ./internal/apicoll/ ./internal/apisend/ ./internal/capability/ ./internal/transport/ -race -count=1
cd frontend && npm ci && npm run contracts && npm run contracts:check
```

**`internal/app` has four tests that fail on this machine and pass in CI**
(`TestLocalEnhancedSession*`, `TestClosingAnEnhancedSessionEndsItsWatch`,
`TestAPlatformThatCannotObserveStillOpensTheSession`, and `TestLiveSshd_*`). AGENTS.md names
this divergence (`nocx-58gq`, `nocx-65v6`). Measure the baseline with `git stash -u` before
attributing any `internal/app` failure to your change, and say in your report which side of
that line each failure fell.

## When done

Write `REPORT-gaps-6b41.md` at your worktree root: both `-whylive` outputs, the before/after
violation counts, the disposition of every remaining violation, exact commands and results.

Then print exactly, on its own line:

    WORKER_DONE::gaps-6b41

If blocked:

    WORKER_BLOCKED::gaps-6b41 <one line why>
