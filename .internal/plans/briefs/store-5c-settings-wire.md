# Worker brief — STORE-5c / bead `nocx-9m5`: wire the settings registry to the wire

Repo `/home/dev/repos/nocx`, branch `feat/persistence-storage-capabilities`. **One other worker
is live**, in `internal/export/**` only. Read before writing code:

1. `.internal/plans/settings-rpc-contract.md` — **frozen**, and it is exactly what you implement.
   Both halves of this feature were already built against it, so it is not negotiable: escalate
   rather than change it.
2. `docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md` — §2.
3. `AGENTS.md`, and AD-1 in `docs/architecture.md` (control plane is JSON-RPC 2.0).

## What already exists — read it first, do not rebuild it

- `internal/settings` — the typed registry, landed and tested. `Registry` exposes
  `Declarations()`, `GetAll()`, typed `GetBool/SetBool/GetString/SetString/GetNumber/SetNumber/
GetSelect/SetSelect`, `Reset(Descriptor)`, and for secrets only `SecretSet/SecretDelete/
SecretExists`. There is deliberately **no** `GetSecret`; do not add one, and do not route
  around its absence.
- `frontend/src/settings.ts` + the 7 client methods in `frontend/src/profiles.ts` — the screen
  is built and unit-tested against the contract with mocks. **Do not modify the frontend.** If
  the backend cannot satisfy the contract exactly as written, that is an escalation.
- `internal/storage` (`Paths`, `DocumentStore`) and `credential.SecretStore` — landed; the
  registry already depends on them.

## Your deliverable

1. **The settings RPC methods in `internal/transport/ws.go`**, exactly as the contract defines:
   `settings.describe`, `settings.getAll`, `settings.set`, `settings.reset`,
   `settings.secretSet`, `settings.secretDelete`, `settings.secretExists`.
   - A validation failure must surface as a **JSON-RPC error**, not `{ok: false}`. The registry
     returns a typed `*settings.ValidationError` (it unwraps to `settings.ErrValidation`) —
     map it to a JSON-RPC error rather than a generic 500-style failure.
   - `settings.set` must **reject a secret-class key**; secrets go through `settings.secretSet`.
   - Nothing in any response may carry a secret value. There is no method that returns one.
2. **Wire the registry into `internal/app/app.go`** at the composition root, pairing it with the
   `DocumentStore` and the `SecretStore` (AD-8: the pairing is chosen here and nowhere else).
3. **Delete `internal/config` entirely.** It is the untyped `Get(key) (any, error)` bag the
   registry replaces (bead `nocx-jap`); `app.go` still references it, and removing that
   reference is part of this task. Greenfield — no adapter, no alias, no deprecation shim.
4. **Make the OSC 52 "Don't show again" decision actually survive a restart** (bead `nocx-3cc`).
   The registry already declares `clipboard.osc52Suppressed` for exactly this. Find where the
   clipboard gate currently drops that decision and route it through the setting. If this turns
   out to need a frontend change, **stop and escalate** — the frontend belongs to nobody right
   now and I would rather decide that explicitly than have it edited silently.

## Files you own

- `internal/transport/ws.go` and the transport tests
- `internal/app/app.go` and `internal/app/app_test.go`
- `internal/config/**` (to delete)

## Files owned by the OTHER worker — do not touch, escalate instead

- `internal/export/**` — a new package appearing under you while you work. Ignore it.
- `frontend/**` — frozen for this task, see above.
- `internal/settings/**`, `internal/storage/**`, `internal/credential/**` — landed; use, do not modify.

## Ground rules

- **Greenfield.** No migrations, no back-compat shims.
- **TDD**: failing test first. Cover at minimum: `settings.describe` returns declarations,
  a validation failure becomes a JSON-RPC error, `settings.set` refuses a secret key, and
  `settings.getAll` contains no secret key.
- **No commit, no push, no branch, no `git stash`.** The coordinator commits.
- **No repo-wide gates.** Scope runs to
  `go test -race ./internal/transport/... ./internal/app/... ./internal/settings/...`
- **No formatting runs.** No `gofumpt -w`, no `prettier`. The coordinator sweeps at the end.
- **No `bd` commands.**

## Report in `worker_done`

- Each of the 7 methods and the handler that serves it.
- How a validation error becomes a JSON-RPC error, quoting the mapping.
- Confirmation that `internal/config` is **deleted**, not merely unreferenced.
- What you did about the OSC 52 decision, and whether it needed a frontend change.
- Test counts and exact commands.
- **Anything you could not verify, stated explicitly**, and anything deliberately left alone.
