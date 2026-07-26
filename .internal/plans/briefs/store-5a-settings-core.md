# Worker brief — STORE-5a / bead `nocx-9m5`: the settings registry (backend half)

Repo `/home/dev/repos/nocx`, branch `feat/persistence-storage-capabilities`. **Two other
workers are live** in `internal/profile|connection|importer|transport|app` and in `frontend/**`.
Read before writing code:

1. `docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md` — binding.
2. `.internal/plans/settings-rpc-contract.md` — **frozen**. Your registry must be able to serve
   every method in it. Two other workers have built against it; escalate rather than change it.
3. `.internal/plans/2026-07-26-persistence-storage-capabilities.md` — **§Task 5**.
4. `AGENTS.md` — engineering rules.

`internal/storage` (`Paths`, `DocumentStore`, the per-module schema version protocol) and
`credential.SecretStore` are landed and committed. **Use them; do not modify them.** Settings
persist as a document through `DocumentStore`, and a secret-class setting stores its material
through `SecretStore` — never in the document.

## Your deliverable

A new package `internal/settings` implementing the typed registry that replaces
`internal/config`'s untyped `Get(key) (any, error)` / `Set(key, value any)` bag. That bag cannot
enumerate what settings exist, their types, defaults or validation, and therefore cannot drive a
generated screen — that is the whole reason this task exists.

The shape ADR-0011 and bead `nocx-9m5` require:

- **Typed declarations** carrying key, default, label, description, section, validation, data
  class and control kind — the `Declaration` fields in the frozen contract.
- Go cannot hold differently-typed declarations in one slice, so expose a **non-generic
  descriptor interface for enumeration** alongside **typed keys for get/set**. That split is
  stated in the bead; do not collapse it back into `any`.
- **Adding a setting must require touching exactly one declaration site.** If adding one means
  editing a declaration _and_ a switch _and_ a serializer, the design has not met its bar.
- A **secret-class** setting exposes `set`, `delete`, `exists` — and **no read operation at
  all**, not even an internal one that returns plaintext to the caller. ADR-0011 §2.
- Validation failures are typed errors the transport layer can turn into JSON-RPC errors.
- The registry owns its **own** monotonic schema version through the shared protocol in
  `internal/storage` — never an app-wide version (ADR-0011 §6).

Declare the settings that already have a home in the code today, so the registry is real rather
than empty. At minimum the OSC 52 clipboard gate's "Don't show again" decision (bead `nocx-3cc`)
— today that decision does not survive a restart, and making it stick is a stated acceptance
criterion. Search the frontend for existing persisted preferences before inventing new ones, and
declare only what already exists — YAGNI.

## Files you own (nobody else touches them this wave)

- `internal/settings/**` (create — the whole package is yours)

## Files owned by OTHER workers — do not touch, escalate instead

- `internal/profile/**`, `internal/connection/**`, `internal/importer/**`,
  `internal/transport/ws.go`, `internal/app/app.go` → the STORE-4 worker
- `frontend/**` → the settings-UI worker
- `internal/storage/**`, `internal/credential/**` → landed and frozen

**Do not add the RPC methods to `internal/transport/ws.go` and do not wire the registry into
`internal/app/app.go`.** Both files belong to another worker right now. A separate, later task
does the wiring — your package must simply be ready for it. **Do not delete `internal/config`**
either; that removal happens in the wiring task, because `app.go` still references it.

## Ground rules

- **Greenfield.** No migrations, no back-compat shims. Do not preserve `config.Config`'s
  interface or provide an adapter to it.
- **TDD**: failing test first, run it, watch it fail, then the minimal implementation.
- **No commit, no push, no branch, no `git stash`.** The coordinator commits.
- **No repo-wide gates.** Do **not** run `go build ./...`, `go test ./...`, `golangci-lint run`.
  Scope every run to `go test ./internal/settings/...`
- **No formatting runs.** No `gofumpt -w`, no `prettier`. The coordinator does a final sweep.
- **Do not touch the issue tracker.** No `bd` commands.

## Report in `worker_done`

Numbers, not adjectives:

- The descriptor interface and the typed-key API, in enough detail to review without opening
  files, plus how the two fit together.
- The exact steps needed to add one new setting — enumerate them, so the "one declaration site"
  criterion can be judged rather than taken on trust.
- Which settings you declared and where you found each one already living in the code.
- How a secret-class setting is prevented from being read, structurally rather than by
  convention.
- Test count and the exact command.
- **Anything you could not verify, stated explicitly**, and anything you deliberately left alone.
