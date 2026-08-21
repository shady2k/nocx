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

# Task: make it reachable — capability, transport, contracts, composition root

**Task id for your sentinel: `wiring-8c14`**

**You own:** `internal/capability/api.go` (new), `internal/transport/ws_api_handlers.go` (new),
`internal/transport/ws_config_handlers.go` (edit: registrations only),
`internal/transport/ws_contract_test.go` (edit: add cases), `internal/app/app.go` (edit),
`contracts/api.*.schema.json` (new), `contracts/files/*.schema.json` (new).

**Another worker owns, do not touch:** `internal/apicoll/environment.go`,
`internal/apicoll/substitute.go`, `internal/apisend/ssh_dialer.go`, `internal/apisend/auth.go`,
`internal/apibind/*.go` (except reading the interface). Also do not touch `frontend/`.

**Already committed and yours to CALL, never to edit:** `internal/apicoll` (Service),
`internal/apisend` (Sender), `internal/apibind` (Store — interface only, no implementation
yet), `internal/apiimport`.

## Why this task exists at all

Right now none of those four packages is reachable from `main()`. AGENTS.md is blunt about
what that means: `deadcode` can tell you a symbol is dead but can never tell you a feature
is wired, and an epic once shipped an encrypted store, a key lifecycle and five Settings
controls whose write path had **no caller outside its own tests**. This task is the wiring
that makes the difference, and it is the reason the packages were allowed to land first.

## The method surface — this list is the contract, do not invent beyond it

| Method                  | Params                       | Result                                                         |
| ----------------------- | ---------------------------- | -------------------------------------------------------------- |
| `api.collections.list`  | none                         | the opened-folder list                                         |
| `api.collections.open`  | `{path}`                     | `{handle, collection}` — **the only method that takes a path** |
| `api.collections.close` | `{handle}`                   | `{}`                                                           |
| `api.request.read`      | `{handle, relPath}`          | the request                                                    |
| `api.request.write`     | `{handle, relPath, request}` | `{}`                                                           |
| `api.request.send`      | `{handle, relPath}`          | the response                                                   |
| `api.import.postman`    | `{path, dest}`               | `{unsupported[]}`                                              |
| `api.import.curl`       | `{line}`                     | `{request, unsupported[]}`                                     |

**A test must assert that no method other than `open` and `import.postman` accepts a path.**
That is design §13.1 made enforceable rather than remembered.

## The gate choice, and why the obvious template is wrong here

Follow `internal/transport/ws_snippet_handlers.go` for the SHAPE — a constructed handler
type holding the operation and the `Responder`, never the `*WSServer`, never the service
directly. Follow `internal/capability/snippet.go` for the operation.

**Do not copy its gate.** Snippets hold the config gate because the snippet library is a
document in the profile directory that backup/restore also writes (`ws_snippet_handlers.go:9`
says so). A collection is an arbitrary folder the user chose; backup/restore does not touch
it, so that reasoning does not transfer. **Collections get their own conflict domain.**

And `api.request.send` performs network I/O. **It must not hold a gate across the dial.**
Snapshot what it needs under a short-lived gate, release, then send. Holding a global gate
behind arbitrary remote latency blocks unrelated settings, vault and backup operations.
State in your report which gate each method holds and for how long.

## Contracts

Two different boundaries, and they are not one schema doing both jobs:

- `contracts/api.*.schema.json` — **RPC results**. `additionalProperties: false` plus an
  explicit `required` on every object; a schema without both is theatre.
- `contracts/files/*.schema.json` — the **persisted** manifest, request and environment
  formats. These need strict validation; migrations and newer-version refusal live in
  `internal/apicoll`'s `storage.Module`, already written.

For each RPC result, both tests in `internal/transport/ws_contract_test.go`: the DTO test,
and — the one that matters — `…_OverTheWireConformsToContract`, validating the real result
off the real socket. Its absence is what let `vault.status` omit a field for months while
both suites stayed green.

`npm run contracts:check` runs from `frontend/`. This worktree has no `node_modules`; run
`npm ci` in `frontend/` once, first, or the check cannot run.

## Prove it is wired — this is the acceptance criterion that matters

```bash
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apicoll.<yourServiceType>.Open' ./...
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apisend.<yourSenderType>.Send' ./...
```

Both must print a path from `main`. Put the actual output in your report. Also run it on a
symbol you know is NOT wired and paste that too — the contrast is what makes it evidence
rather than an empty result that would have been empty anyway.

`-tags gtk3` matters: without it cgo fails on Linux before deadcode reaches our code.

## Verify

```bash
gofumpt -w internal/capability internal/transport internal/app
go vet ./internal/capability/... ./internal/transport/... ./internal/app/...
golangci-lint run ./internal/capability/... ./internal/transport/... ./internal/app/...
go test ./internal/transport/ ./internal/app/ -race -count=1
cd frontend && npm run contracts:check
```

## When done

Write `REPORT-wiring-8c14.md` at your worktree root: the gate each method holds, the two
`-whylive` outputs plus the contrast, exact commands and results, and anything unverified.

Then print exactly, on its own line:

    WORKER_DONE::wiring-8c14

If blocked:

    WORKER_BLOCKED::wiring-8c14 <one line why>
