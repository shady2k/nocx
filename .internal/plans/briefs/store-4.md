# Worker brief — STORE-4 / bead `nocx-am4`: split ProfileStore by aggregate

Repo `/home/dev/repos/nocx`, branch `feat/persistence-storage-capabilities`. **Two other
workers are live** in `internal/settings/**` and `frontend/**`. Read before writing code:

1. `docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md` — binding.
2. `.internal/plans/2026-07-26-persistence-storage-capabilities.md` — **§Task 4** is your spec.
3. `AGENTS.md` — engineering rules.

`internal/storage` (Paths + DocumentStore) and `credential.SecretStore` are already landed and
committed. Use them as-is; do not modify them.

## Your deliverable — two things, and the second is a real bug

### 1. Split the interface by aggregate

`internal/profile/store.go:14` currently has one `ProfileStore` interface owning three
aggregates across nine methods. Split it into `ProfileRepository`, `GroupRepository` and
`CredentialMetadataRepository`, each behind its own interface.

**No generic `Repository[T]`.** ADR-0011 §1 rejects it explicitly and says why: query and
consistency needs diverge fast, and a shared generic forces the widest contract on the
narrowest store. Do not reintroduce it under another name.

`JSONStore` may still implement all three (they share one document today) — the point is that
consumers depend on the narrow interface they actually use. Update every consumer to take the
narrowest one it needs: `internal/connection/resolver.go` (profiles + credential metadata),
`internal/importer/tabby.go` (profiles + groups), `internal/transport/ws.go`, and the
composition root in `internal/app/app.go`.

### 2. Fix the lost-update race — this is the substantive part

`store.go` loads outside the mutex while only `save` locks it. Two concurrent WebSocket
requests therefore load the same snapshot, mutate different records, and the later rename
silently discards the earlier mutation. Read-modify-write must be atomic as a whole.

Write the failing test **first**: concurrent `SaveProfile` calls for two _different_ profile
IDs, run under `go test -race`, asserting both survive. Watch it fail before you fix it — the
report must quote that failure.

Take care that the fix is a correct lock discipline and not a lock held across a keychain call:
ADR-0011 §4 forbids calling the keychain while holding a document lock, because the keychain can
block, prompt, or fail on its own schedule.

## Files you own (nobody else touches them this wave)

- `internal/profile/**`
- `internal/connection/**`
- `internal/importer/**`
- `internal/transport/ws.go` and the transport tests
- `internal/app/app.go`

## Files owned by OTHER workers — do not touch, escalate instead

- `internal/settings/**` → the settings-core worker (a new package; it does **not** exist in
  your world yet — do not create it, do not wire it, do not delete `internal/config`)
- `frontend/**` → the settings-UI worker

You will see `internal/settings/` and frontend files appear and change under you while you
work. That is expected. Ignore them.

## Ground rules

- **Greenfield.** No migrations, no back-compat shims. Delete the old interface rather than
  keeping it as an alias.
- **TDD**: failing test first, run it, watch it fail, then the minimal implementation.
- **No commit, no push, no branch, no `git stash`.** The coordinator commits.
- **No repo-wide gates.** Do **not** run `go build ./...`, `go test ./...`, `golangci-lint run`
  — a neighbour's half-written file will make you report a phantom blocker. Scope every run:
  `go test -race ./internal/profile/... ./internal/connection/... ./internal/importer/... ./internal/transport/... ./internal/app/...`
- **No formatting runs.** No `gofumpt -w`, no `prettier`. The coordinator does a final sweep.
- **Do not touch the issue tracker.** No `bd` commands.

## Report in `worker_done`

Numbers, not adjectives:

- The three interfaces and their method sets, so they can be checked without opening files.
- Which narrow interface each consumer now depends on.
- **The literal failure output of the race test before the fix.** This is the evidence the fix
  addresses a real defect; a report without it will be sent back.
- Test counts before and after, and the exact commands.
- **Anything you could not verify, stated explicitly**, and anything you deliberately left alone.
