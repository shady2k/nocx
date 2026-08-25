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

# Task: close the deadcode ratchet honestly — wire what is real, delete what is not

**Task id for your sentinel: `ratchet-1c05`**

**You own:** `internal/app/app.go`, `internal/transport/ws.go`,
`internal/transport/ws_api_handlers.go`, `internal/apisend/{sender,client,auth}.go`,
`internal/assistant/httpguard.go`, `internal/httppolicy/*.go`, and the tests for each.

**Do not touch:** `frontend/`, `contracts/`, `.internal/`.

## The state you are inheriting

```
$ node .githooks/check-deadcode.mjs
DEADCODE RATCHET: 113 unreachable functions on linux/amd64 (89 baselined, 25 NEW)   exit 1
```

The 25 were dispositioned by the previous worker and **none was baselined**. Your job is to
take them to **zero NEW**, by wiring what is a real feature and deleting what is not. **You
may not baseline anything.** If you believe an entry can only be baselined, stop and say so
rather than doing it — that decision belongs to the owner.

## 1. Wire the binding store — 19 of the 25

`apibind.NewStore(docs, secrets)` exists and is complete. `internal/app/app.go` carries a
stale comment claiming there is no implementation yet. Construct it and pass it through
`transport.WithAPIBindings`.

The previous worker deliberately did not do this, and named the reason: **it turns on
`api.import.postman` in production**, the path that writes a user's Postman tokens into the
vault. That decision has now been taken — it is required by the epic, whose acceptance is a
person importing a collection and the token landing in the vault — and the end-to-end check
that watches it happen is the very next task. So wire it.

This closes the 17 in `apibind`, plus `transport.WithAPIBindings` and
`apiimport.FromPostman`.

## 2. Resolve auth variables on the send path — 2 of the 25

`apisend.Apply` and `resolveAuthVar` have no caller, which means **the send path does not
resolve auth variables at all**: today an auth kind other than `none` is refused by name.
That is honest but it is not the feature. Wire `Apply` into `Send` so a bearer, basic or
api-key request resolves its variable through the binding store.

Three properties, each with a test:

- A bound variable produces the right header, and the value appears in **no** JSON-RPC frame.
- An **unbound** variable blocks the send and names itself — it does not send an empty
  credential. `Authorization: Bearer ` is a plausible request that teaches the wrong lesson
  about why it was rejected.
- A **raw vault identifier** written into `Auth.Var` by a hostile collection file resolves to
  nothing: it is an unknown variable name (design §8). Assert the ordinary bound path works
  in the same test, so the refusal is not the whole world failing to resolve.

## 3. Delete what is genuinely dead — 4 of the 25

Each of these is a leftover with no caller, and the repo's rule is no dead code:

- `assistant.checkDestination` — a one-line forwarder to `httppolicy.CheckDestination` left
  by the extraction. Literally the "second answer to one question" shape.
- `httppolicy.NewClient` — a convenience constructor beside `NewTransport`, which is what
  every caller actually uses.
- `apisend.WithMaxBytes` and `apisend.WithTLSClientConfig` — options nothing in the product
  sets. **Do not invent a caller to keep them.** If you delete `WithMaxBytes`, the 2 MiB
  ceiling stays exactly as it is; the option only ever lowered it and no surface offers to.

If deleting any of these breaks a test, that test was testing the option rather than a
behaviour — say so in your report and delete it with the option.

## The measurement that closes the task

```bash
node .githooks/check-deadcode.mjs
```

must print **0 NEW**. Put the before and after lines in your report. If it does not reach
zero, list what remains with the reason, and do not baseline.

## Verify

```bash
gofumpt -w internal
go vet ./internal/...
golangci-lint run ./internal/...
go test ./internal/apisend/ ./internal/apibind/ ./internal/apicoll/ ./internal/apiimport/ ./internal/httppolicy/ ./internal/assistant/ ./internal/capability/ ./internal/transport/ -race -count=1
node .githooks/check-deadcode.mjs
```

`internal/app` has four tests that fail on this machine and pass in CI (`nocx-58gq`,
`nocx-65v6`). Measure the baseline with `git stash -u` before blaming your change.

## When done

Write `REPORT-ratchet-1c05.md`: the before/after ratchet lines, what you wired, what you
deleted and what broke when you did, exact commands and results.

Then print exactly, on its own line:

    WORKER_DONE::ratchet-1c05

If blocked:

    WORKER_BLOCKED::ratchet-1c05 <one line why>
