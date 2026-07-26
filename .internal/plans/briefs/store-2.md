# Worker brief — STORE-2 / bead `nocx-kx4`: DocumentStore capability

You are working in a **shared checkout** (`/home/dev/repos/nocx`, branch
`feat/persistence-storage-capabilities`) alongside up to two other live workers. Read these
before writing code:

1. `docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md` — binding;
   **§1 and §6** are yours.
2. `.internal/plans/2026-07-26-persistence-storage-capabilities.md` — **§Task 2** is your spec,
   verbatim. Also read §"File ownership map".
3. `AGENTS.md` — engineering rules.

`internal/storage/paths.go` already exists and is **finished** (STORE-1 landed). Read it; use
`storage.Paths` as-is. Do not modify `paths.go` or `paths_test.go`.

## Your deliverable, in TWO PHASES

The phases exist because another worker is editing `internal/profile/profile.go` **right now**.

### Phase A — the capability (do all of this first)

Create `internal/storage/document.go` + `internal/storage/document_test.go` implementing the
`DocumentStore` interface from plan §Task 2:

```go
type DocumentStore interface {
	Read(name string, into any) (found bool, err error)
	Write(name string, doc any) error
}
```

Extract the mechanism currently private inside `profile.JSONStore.save`
(`internal/profile/store.go:67-98`) — `0700` directory, `0600` file, temp file + rename — and fix
the two defects the bead records, both of which are real:

- **Directory fsync after rename.** Without it "atomic" is not crash-durable. Add it.
- **Symlink target check.** Rename-over-path replaces a symlinked target with a regular file
  (same class as bead `nocx-ab4`). Refuse to write when the target is a symlink.

Also implement the **per-module schema version** protocol: each module declares its own
monotonic version and its own migrations; you share the _protocol_, never a single app-wide
version number. ADR-0011 §6 says why: one app-wide version couples unrelated releases of
settings, profiles, conversations and history, and JSON migrations have different transactional
properties from SQLite ones.

Verification for Phase A — **scoped to your own package only**:

```bash
go test ./internal/storage/...
```

### Phase B — the switchover (only after Phase A is green)

Make `profile.JSONStore` use the capability instead of its own temp-plus-rename, and wire the
`DocumentStore` into `internal/app/app.go`. You own `internal/app/app.go` now; STORE-1's worker
has finished with it.

**`internal/profile/profile.go` is owned by another worker and may be mid-edit.** You own
`internal/profile/store.go` for this task; you do **not** own `profile.go`.

- Do not edit `internal/profile/profile.go`.
- **Do not restructure `store.go`'s interface** — splitting `ProfileStore` into three
  repositories is STORE-4, a later task. Your change is the write mechanism, not the shape.
- If `go test ./internal/profile/...` fails inside `profile.go`, or on a `Credential` field you
  did not touch, that is **the other worker's in-flight edit, not your bug**. Do not fix it, do
  not work around it: report it in your `worker_done` and move on. Escalate if it blocks you.

## Ground rules

- **Greenfield.** No migrations, no back-compat shims, no compatibility fallbacks. No reading of
  an old on-disk format "just in case".
- **TDD**: failing test first, run it, watch it fail, then the minimal implementation. The
  symlink-refusal and the fsync both need their own test.
- **No commit, no push, no branch, no `git stash`.** The coordinator commits.
- **No repo-wide gates.** Do **not** run `go build ./...`, `go test ./...`, `golangci-lint run` —
  a neighbour's half-written file will make you report a phantom blocker. Scope every run to
  `./internal/storage/...` in Phase A, plus `./internal/profile/... ./internal/app/...` in Phase B.
- **No formatting runs.** No `gofumpt -w`, no `prettier`. Separate final wave.
- **Do not touch the issue tracker.** No `bd` commands — the coordinator owns beads.

## Report in `worker_done`

Numbers, not adjectives:

- Test counts and the exact commands you ran, per phase.
- How you tested the directory fsync and the symlink refusal — the assertions, not a claim.
- The schema-version protocol you settled on, in three lines, so the coordinator can check it
  against ADR-0011 §6 without opening the files.
- Whether Phase B completed, and if not, exactly what stopped it.
- **Anything you could not verify, stated explicitly.** Silence here is treated as a failure to
  report, not as "nothing to report".
- Any problem you spotted and deliberately left alone.
