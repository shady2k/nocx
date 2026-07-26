# Persistence: storage capabilities + secrets as opaque references — Implementation Plan

> **For agentic workers:** this plan is executed as Orca worker waves. Each Task below is an
> existing bead under epic `nocx-6ek`. Workers implement one task, TDD, and report
> `worker_done`. **Only the coordinator owns beads, commits, and quality gates.**

**Goal:** Implement ADR-0011 — three storage capabilities (DocumentStore, SecretStore,
ContentDB) with typed per-entity repositories paired at the single composition root, and
secrets reachable only through `SecretStore` by opaque `SecretID`.

**Architecture:** A new `internal/storage` package owns path resolution and atomic JSON
documents. `internal/credential` gains a `SecretStore` capability addressed by stable
`SecretID`; persisted domain records carry the ID, never the material. `internal/content`
declares the ContentDB seam with a stub and no SQLite dependency. `internal/app/app.go`
remains the only place backends are paired with repositories (AD-8).

**Tech Stack:** Go (stdlib + `zalando/go-keyring`), TypeScript/xterm.js frontend, Wails v2.

## Global Constraints

- **ADR-0011** (`docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md`)
  is binding. So are `AD-1`, `AD-6`, `AD-7`, `AD-8` in `docs/architecture.md`.
- **Clean-only** (AGENTS.md): no back-compat shims, no dead code, no speculative features.
  Greenfield — breaking existing on-disk/keychain state is allowed and expected.
- **TDD**: failing test first, then minimal implementation. Every language held to the
  same bar.
- **No secret may become an ordinary `string` outside `internal/credential` and
  `internal/ssh`.** A secret in a persisted record is a `SecretID` and nothing else.
- **Schema versions are per module**, never app-wide.
- `go.mod` gains **no database dependency** in this epic.
- Structured logging via `log/slog` behind the logging interface — no `fmt.Println`.

## What already landed (verified 2026-07-26, do not redo)

PR #12 shipped more of ADR-0011 than the ADR text implies. Confirmed by reading the code:

- `credential.Secret` exists (`internal/credential/secret.go`) and is thorough: unexported
  `value`, `Use` callback accessor, `MarshalJSON`/`MarshalText` **return an error**,
  `String`/`GoString`/`Format`/`LogValue` all render `[REDACTED]`. **Do not rewrite it.**
- `VaultSecret.Value` is already a `Secret`, not a JSON string. ADR-0011's "remove the
  json-serializable Value" consequence is already satisfied.
- `credentials.lookupPassword` is already **gone from the wire**. The surviving RPCs are
  exactly `set`/`delete`/`exists`: `credentials.savePassword`, `credentials.deletePassword`,
  `credentials.hasPassword`, `credentials.saveKeyPassphrase`, `credentials.deleteKeyPassphrase`.
- `session.open` already carries `profileId` only (`internal/transport/ws.go:361,548`); the
  profile→credential→secret join happens backend-side in `internal/connection/resolver.go`.
- A delete cascade exists but is **interim** (`nocx-7l4`): it derives the password key from
  the credential ID and the passphrase key by re-reading and hashing the private key file.
  Task 3 replaces that with `SecretID` and that is what finally closes `nocx-7l4`.

The gap this epic closes is therefore: path resolution, the DocumentStore capability,
`SecretID` addressing, the ProfileStore split (and its lost-update race), the settings
registry, the ContentDB seam, and export/backup modes.

## File ownership map

`internal/app/app.go` is the composition root and is contended by several tasks. It is
assigned to exactly one worker per wave; every other worker in that wave is forbidden to
touch it and must escalate instead.

| Path                                          | Owner                                                     |
| --------------------------------------------- | --------------------------------------------------------- |
| `internal/storage/**`                         | Task 1, then Task 2                                       |
| `internal/app/app.go`                         | Task 1 (wave 1), Task 2 (wave 2), coordinator (waves 3–4) |
| `internal/credential/**`                      | Task 3                                                    |
| `internal/connection/resolver.go`             | Task 3 (wave 1), Task 4 (wave 3)                          |
| `internal/transport/ws.go`                    | Task 3 (wave 1), Task 5 (wave 3)                          |
| `internal/ssh/**`                             | Task 3                                                    |
| `internal/content/**`                         | Task 6                                                    |
| `internal/profile/**`                         | Task 3 (metadata fields only), Task 4 (structure)         |
| `internal/config/**` → `internal/settings/**` | Task 5                                                    |
| `frontend/src/settings*`                      | Task 5                                                    |
| `internal/export/**`                          | Task 7                                                    |

## Wave map

```
Wave 1 (parallel, disjoint):  Task 1 (nocx-2j3)   Task 3 (nocx-r60)   Task 6 (nocx-208)
Wave 2 (single):              Task 2 (nocx-kx4)          [needs Task 1]
Wave 3 (parallel, disjoint):  Task 4 (nocx-am4)   Task 5 (nocx-9m5)   [need Task 2]
Wave 4 (single):              Task 7 (nocx-cym)          [needs Tasks 2+3]
Gate:                         coordinator runs the full gate ONCE, here.
```

---

### Task 1 — Shared storage path resolution (`nocx-2j3`)

**Files:**

- Create: `internal/storage/paths.go`, `internal/storage/paths_test.go`
- Modify: `internal/app/app.go:76-85` (the `os.UserConfigDir()` block and the `os.TempDir()`
  fallback), `internal/app/app_test.go` if it asserts on `New()` behaviour

**Interfaces — Produces:**

```go
package storage

// Paths resolves the OS locations nocx persists into, distinguishing roles.
// There is deliberately no SecretsDir: secrets live in the OS keychain, which
// is not a path we own (ADR-0011 §1).
type Paths interface {
	// ConfigDir is where human-recoverable configuration documents live.
	ConfigDir() string
	// DataDir is where content.db lives.
	DataDir() string
	// CacheDir is where disposable indexes live.
	CacheDir() string
}

// NewOSPaths resolves the three roles for appName on the current platform.
// It returns an error when any role cannot be resolved; there is no fallback
// (ADR-0011: failure to resolve is explicit, never a silent /tmp write).
func NewOSPaths(appName string) (Paths, error)
```

**Platform rules:**

- Linux: honour `XDG_CONFIG_HOME` / `XDG_DATA_HOME` / `XDG_CACHE_HOME`; defaults
  `~/.config`, `~/.local/share`, `~/.cache`, each with `/<appName>` appended. `os.UserConfigDir`
  and `os.UserCacheDir` cover config and cache; there is no `os.UserDataDir`, so resolve
  data from `XDG_DATA_HOME` or `~/.local/share` yourself.
- macOS: config and data both under `~/Library/Application Support/<appName>`, cache under
  `~/Library/Caches/<appName>`. The ADR expects config and data to coincide here.
- Any role that cannot resolve (no `HOME`, no XDG override) → `NewOSPaths` returns an error
  naming the role that failed.

**Acceptance Criteria:**

- A single injected component returns the locations per platform. No store computes its own path.
- The `/tmp` fallback is gone; failure to resolve a location is an explicit error, not a
  silent fallback. `app.New()` returns that error rather than warning and continuing with an
  in-memory or `/tmp` store.
- `profile.NewJSONStore` receives `filepath.Join(paths.ConfigDir(), "profiles.json")`.

**Steps:**

- [ ] Write `paths_test.go`: table-driven, `t.Setenv` the XDG vars, assert all three roles on
      Linux; assert `NewOSPaths` errors when `HOME` and every XDG var are unset.
- [ ] Run `go test ./internal/storage/...` — expect FAIL (package does not exist).
- [ ] Implement `paths.go` minimally to pass.
- [ ] Run `go test ./internal/storage/...` — expect PASS.
- [ ] Rewrite `internal/app/app.go:76-85` to construct `storage.NewOSPaths("nocx")` and
      propagate its error out of `New()`; delete the `os.TempDir()` branch and the
      `configDir == ""` branch entirely.
- [ ] Run `go test ./internal/storage/... ./internal/app/...` — expect PASS.

---

### Task 3 — SecretStore capability + `SecretID` as the reference (`nocx-r60`)

Security-critical, and it subsumes the rewrite that closes `nocx-7l4`.

**Files:**

- Create: `internal/credential/secretstore.go`, `internal/credential/secretstore_test.go`
- Modify: `internal/credential/credential.go`, `internal/credential/keychain.go`,
  `internal/credential/vault.go`, `internal/profile/profile.go` (metadata fields only),
  `internal/connection/resolver.go`, `internal/transport/ws.go` (credential handlers around
  `:1029-1160` and the delete cascade at `:1240-1270`), `internal/ssh/**` where
  `ConnectConfig.CredIdentity` / `Credentials` are consumed

**Interfaces — Produces:**

```go
// SecretID is an opaque, stable handle to secret material held by a
// SecretStore. It is the ONLY form in which a secret may appear in a
// persisted domain record or cross a package boundary (ADR-0011 §2).
type SecretID string

// NewSecretID mints a fresh, collision-free ID: "sec:" + uuid.
func NewSecretID() SecretID

// SecretStore holds authenticators in the OS keychain. Its operations are
// deliberately set/delete/exists plus a backend-only Get; there is no API
// that hands plaintext to the renderer (ADR-0011 §2).
type SecretStore interface {
	Get(id SecretID) (Secret, error) // empty Secret, nil error when absent
	Set(id SecretID, value Secret) error
	Delete(id SecretID) error
	Exists(id SecretID) (bool, error)
}
```

**Design rules:**

- Keychain backend: one service name for all nocx secrets, account = `string(id)`. Stable IDs
  mean nothing is ever re-derived from a file path or a key's contents.
- `profile.Credential` carries `SecretID` (password) and `PassphraseSecretID` (key
  passphrase). Both are opaque strings in JSON. `KeyPath` stays — it is not a secret.
- `HashKey` / `KeyHash` content-addressing goes away as the _secret_ key. It exists today only
  because there was no stable reference; a `SecretID` replaces it. Delete the dead code.
- **The wire API does not change shape.** `credentials.savePassword` etc. keep taking
  `credentialId`; the backend mints/looks up the `SecretID`. The renderer must never see a
  `SecretID`, and no RPC returns one.
- The delete cascade becomes: load metadata → read both `SecretID`s → delete metadata →
  delete secrets best-effort with `errors.Join`. **No re-reading the private key file.** This
  is ADR-0011 §4 order and it removes the `nocx-dm0` failure (a key file the user already
  deleted).
- Never call the keychain while holding a document lock (ADR-0011 §4).

**Acceptance Criteria:**

- Secrets are addressed by stable `SecretID`. No API outside `internal/credential` and
  `internal/ssh` can obtain plaintext. A secret cannot be marshalled or logged.
- Deleting a credential deletes both of its secrets, derived from stored IDs, with no
  dependency on the private key file still existing.
- The existing `ws_logging_test.go` password-redaction test and `ws_cascade_test.go` still pass.

**Steps:**

- [ ] Write `secretstore_test.go` covering set → exists → get → delete → exists-false, and
      absent-`Get` returning an empty `Secret` with a nil error. Use the existing keychain test
      helpers (`internal/credential/vault_testhelpers.go`, `keychain_test.go`) as the pattern.
- [ ] Run `go test ./internal/credential/...` — expect FAIL.
- [ ] Implement `secretstore.go` and its keychain backend; make the tests pass.
- [ ] Write a failing test proving credential deletion removes both secrets **when the key
      file no longer exists**. This is the test that fails on today's implementation.
- [ ] Add `SecretID`/`PassphraseSecretID` to `profile.Credential`; thread them through
      `resolver.go` and the `ws.go` credential handlers; delete `HashKey`, `KeyHash` and the
      `Identity{User: credentialID}` convention.
- [ ] Run `go test ./internal/credential/... ./internal/connection/... ./internal/transport/... ./internal/ssh/...`
      — expect PASS.

---

### Task 6 — ContentDB seam + stub (`nocx-208`)

**Files:**

- Create: `internal/content/content.go`, `internal/content/stub.go`, `internal/content/stub_test.go`

**Interfaces — Produces:** a `ContentDB` capability plus `ConversationRepository` and
`CommandHistoryRepository` written against it, and a stub implementation that logs and
returns a sentinel error, exactly as `config.Stub` does today.

**Design rules:**

- **No SQLite implementation and no `go.mod` change.** Verify with `git diff --stat go.mod go.sum`
  showing no output.
- No generic `Repository[T]` (ADR-0011 §1 rejects it explicitly).
- Record the conditions for the real implementation in a package doc comment where the
  implementer will find them: one database (`content.db`) not one per entity; WAL mode,
  because surviving a force-quit is the whole reason a desktop app takes a database;
  `foreign_keys=ON`; short transactions through one controlled write path; and the honesty
  constraint that ordinary `DELETE` leaves data in WAL pages, freelists and FTS shadow
  tables, so the UI says "removed from nocx", not "securely erased".

**Acceptance Criteria:**

- Repository interfaces exist and compile against a stub. `go.mod` gains no database
  dependency. The conditions for the real implementation are recorded where the implementer
  will find them.

**Steps:**

- [ ] Write `stub_test.go` asserting each repository method returns the sentinel
      not-implemented error and that the stub satisfies the interfaces at compile time.
- [ ] Run `go test ./internal/content/...` — expect FAIL.
- [ ] Implement `content.go` (interfaces + doc comment) and `stub.go`.
- [ ] Run `go test ./internal/content/...` — expect PASS; confirm `git diff go.mod go.sum` is empty.

---

### Task 2 — DocumentStore capability (`nocx-kx4`) — wave 2

**Files:**

- Create: `internal/storage/document.go`, `internal/storage/document_test.go`
- Modify: `internal/profile/store.go` (use the capability instead of its private
  temp-plus-rename), `internal/app/app.go` (wiring)

**Interfaces — Consumes:** `storage.Paths` from Task 1.

**Produces:**

```go
// DocumentStore reads and writes bounded, human-recoverable configuration as
// atomic JSON documents. Callers name a document; the store owns the path,
// permissions and atomicity.
type DocumentStore interface {
	Read(name string, into any) (found bool, err error)
	Write(name string, doc any) error
}
```

**Design rules:**

- Extract the mechanism currently private inside `profile.JSONStore.save`
  (`internal/profile/store.go:67-98`): `0700` directory, `0600` file, temp file + rename.
- Fix what that mechanism does **not** do today, both flagged on the bead:
  - **Directory fsync after rename** — without it "atomic" is not crash-durable. Add it.
  - **Symlink target check** — rename-over-path replaces a symlinked target with a regular
    file (same class as `nocx-ab4`). Refuse to write when the target is a symlink.
- **Per-module schema version.** Share the migration _protocol_, not the version: each module
  declares its own monotonic version and its own migrations. A single app-wide version would
  couple unrelated releases of settings, profiles, conversations and history.

**Acceptance Criteria:**

- Documents are written atomically with correct permissions. Each module declares and migrates
  its own schema version through a shared protocol. `profile.JSONStore` uses the capability
  rather than its own implementation.

---

### Task 4 — Split ProfileStore by aggregate (`nocx-am4`) — wave 3

**Files:** `internal/profile/store.go`, `internal/profile/profile_test.go`,
`internal/connection/resolver.go`, `internal/app/app.go` (coordinator wires)

**Design rules:**

- `ProfileStore`'s nine methods over three aggregates become `ProfileRepository`,
  `GroupRepository`, `CredentialMetadataRepository`. No generic `Repository[T]`.
- **Fix the lost-update race:** CRUD loads outside the mutex (`store.go:108-120`) while only
  `save` locks (`:67-69`), so two concurrent WebSocket requests load the same snapshot and the
  later rename silently discards the earlier mutation. The read-modify-write must be atomic.
- Cover it with a race test (`go test -race`) that fails on today's code.

**Acceptance Criteria:**

- Three focused repositories behind their own interfaces. Concurrent updates to different
  records cannot lose one another; covered by a race test.

---

### Task 5 — Settings registry on DocumentStore (`nocx-9m5`) — wave 3

**Files:** create `internal/settings/**`; delete `internal/config/**`; frontend settings screen.

**Design rules:**

- Replace `config.Config`'s `Get(key string) (any, error)` / `Set(key string, value any) error`
  — it cannot enumerate what settings exist, their types, defaults or validation, and so
  cannot drive a generated screen.
- Typed declarations carry key, default, label, description, section, validation, data class
  and control kind. Go cannot hold differently-typed declarations in one slice, so expose a
  **non-generic descriptor interface** for enumeration alongside **typed keys** for get/set.
- A **secret-class** setting generates an editor whose operations are `set`, `delete`,
  `exists` — never `get`.
- Retires the `config.Stub` (`nocx-jap`) and makes "Don't show again" on the OSC 52 gate mean
  what it says (`nocx-3cc`).

**Acceptance Criteria:**

- The settings screen is rendered from declarations, not hand-maintained. A user decision
  survives restart. Adding a setting requires touching one declaration site. No secret-class
  setting exposes a read operation.

---

### Task 7 — Export, import and backup as distinct modes (`nocx-cym`) — wave 4

**Files:** create `internal/export/**`; frontend entry points.

**Design rules:** four separate modes, each stating what it does and does not carry —
configuration export (references present but unresolved), portable encrypted export
(explicit, user-authenticated, new passphrase), same-machine backup (documents + `content.db`,
plus a plain statement that secrets stay in the OS keychain), and import (metadata first, then
the user maps existing credentials or supplies missing secrets). Private content is **never**
silently included in a portable export.

**Acceptance Criteria:**

- Each mode exists separately and states what it does and does not carry. No mode silently
  exports private content or resolved secrets.

---

## Coordinator gate (once, after wave 4)

Per the user's instruction, gates run **once at the end of the epic**, not per task:

```bash
gofumpt -l .                 # must print nothing
golangci-lint run
go test -race ./...
npx prettier --check . && npx eslint . && npx tsc --noEmit && npx vitest run
```

Formatting is a final single-worker pass — never run in parallel with implementation waves.
Baseline first with `git stash -u` before attributing any failure to this epic's changes;
`nocx-bw2` records that 13 e2e tests already fail on `main`, so the Playwright suite is a
known-red baseline and not this epic's regression.
