# ADR-0011 — Persistence: storage capabilities, and secrets as opaque references

- **Status:** Accepted
- **Date:** 2026-07-25
- **Related:** AD-8 (interface-first + DI at a single composition root), AD-1 (control
  plane), `docs/architecture.md` §Operational envelope ("plain files in the OS config
  dir", and the open JSON-vs-TOML assumption), `docs/vision.md` §10 (no cloud sync, no
  telemetry — ever), beads `nocx-1ei` (settings registry), `nocx-jap` (config is a
  stub), `nocx-p7g` (PR #11 persistence), `nocx-ea6` (PR #11 rework).
- **Revises:** the architecture's "plain files in the OS config dir" line, by naming
  three storage classes instead of one, and by admitting a database for one of them.

## Context

Persistence has been decided ad hoc, once per feature, and the results have started to
disagree.

`internal/config` declares the app's only persistence interface and it is an untyped
key-value bag — `Get(key string) (any, error)` / `Set(key string, value any) error` —
implemented solely by `Stub`, whose `Load`/`Save` log a line and return nil. Nothing a
user chooses survives a restart (`nocx-jap`). That interface also cannot support the
settings screen we want: a screen **generated** from declarations (`nocx-1ei`) needs to
enumerate settings with their types, defaults, sections and validation, and a bag of
`any` keyed by strings can enumerate none of it.

Meanwhile PR #11 (SSH connection manager) shipped a real, working persistence layer
beside it: a typed `ProfileStore` interface with a `JSONStore` implementation doing
atomic temp-plus-rename writes into a `0700` directory, wired at the composition root.
Its shape is better than `config`'s. The problem is not that it exists — it is that the
app now has two unrelated answers to "where does state live", and the next three
features (command history, tab restore, AI conversations) would each invent a third.

The same PR also demonstrated, concretely, why an informal secret boundary fails. SSH
passwords travelled from the backend to the renderer and back:
`frontend/src/connections.ts:280` fetches the password, `frontend/src/ipc.ts:570` puts
it into a JSON-RPC frame, and `frontend/src/tabs.ts:560` logs the whole object —
`Log('nocx: opening session, sshOpts: ' + JSON.stringify(this.sshOpts))`. The secret was
an ordinary string in an ordinary struct, so nothing stopped it. Supporting evidence
that the boundary was never expressed in types: `internal/credential/credential.go:59-64`
defines `VaultSecret{ Value string \`json:"value"\` }`— a secret **designed** to
serialize — and`internal/transport/ws.go:998-1010` deletes credential metadata without
deleting the corresponding keychain entry, orphaning it permanently.

Three different kinds of data are being conflated:

- **Settings** — few, declared ahead of time, enumerable, each with a type and default.
- **Domain records** — SSH profiles and groups: a CRUD collection with identifiers and
  relationships. Not settings; forcing them through a settings API would be worse.
- **Authenticators** — passwords and key passphrases. Must never reach a plain file, a
  log, or the renderer.

And a fourth is coming: **private content** — AI agent conversations and command
history. Append-heavy, unbounded, wanting search and ranking.

## Decision

### 1. Three storage capabilities, not one persistence layer

```
  Settings   Profiles   Credential meta   Conversations   History
      │          │             │                │           │
   ┌──────────────────────┐  ┌──────────┐  ┌──────────────────┐
   │  DocumentStore       │  │ Secret   │  │ ContentDB        │
   │  atomic JSON files   │  │ Store    │  │ SQLite           │
   └──────────────────────┘  └──────────┘  └──────────────────┘
                    chosen at the composition root
```

- **DocumentStore** — bounded, human-recoverable configuration as atomic JSON documents.
  Settings, profiles, groups, credential _metadata_, tab restore. A user can open these
  in an editor and repair them; that is a feature, not an accident.
- **SecretStore** — authenticators only, in the OS keychain. Never a file we write.
- **ContentDB** — one SQLite database for unbounded, query-oriented private content.

Each entity declares its own typed repository interface. Backends are implementations.
The pairing is chosen at the single composition root — AD-8 applied to persistence. No
generic `Repository[T]`: query and consistency needs diverge fast, and a shared generic
would force the widest contract on the narrowest store.

### 2. Secrets are opaque references, enforced by types

The load-bearing rule:

> A secret value exists only inside secret-specific types and APIs. A persisted domain
> record carries an opaque secret **reference**, never secret material.

`CredentialMetadata` holds a `SecretID`. The secret behind it is reachable only through
`SecretStore`, backend-side. The wire request to open a session carries a `profileID`
and nothing else; the backend joins profile to credential to secret at connection time.

This is a **type boundary, not a redaction convention.** Redaction — struct tags,
`json:"-"`, a `String()` that prints `[REDACTED]` — protects one encoder at a time and
would not have prevented the PR #11 leak, because by the time the password reached
`tabs.ts` it was already an ordinary JavaScript string. Defense in depth (a `Secret`
wrapper that refuses to marshal and redacts in `slog`) is worth having _inside_ the
credential package, but it is not the boundary. The boundary is that the renderer has
no API that returns a secret.

Consequently `credentials.lookupPassword` and every sibling RPC that returns plaintext
is removed, not fixed. A secret-class setting generates an editor whose operations are
`set`, `delete` and `exists` — never `get`.

### 3. Data classification governs policy, not routing

A small vocabulary — public config, private metadata, private content, secret
authenticator — drives the generated settings UI, export eligibility, log handling,
retention and backup warnings. It does **not** dynamically decide where each struct
field is written. Routing by reflection over arbitrary entities would turn persistence
into a framework, and one entity's fields are split by _ownership_ (profile metadata vs.
credential secret) far more cleanly than by a per-field router.

Note that the classification is not binary. Command lines, hostnames and conversation
text are not credentials, but they are private content and need retention and export
policy of their own.

### 4. Cross-store writes are explicit workflows, not hidden transactions

No transaction spans JSON and a keychain, or SQLite and a keychain. Rather than let a
repository pretend otherwise, the domain model keeps them separate:

```
SSHProfile ──references──> CredentialMetadata ──references──> Secret
```

Saving a profile touches profile metadata only. Changing a secret is a distinct
credential-service operation, performed as a sequence with stable IDs: write the new
secret under a new ID, then atomically repoint metadata, then best-effort delete the
old. A small journal of **identifiers only** — never secret bytes — makes a crash
recoverable. Deletion goes metadata-first with a retriable secret deletion after: a
brief unreachable orphan is safer than metadata pointing at a secret that is gone.

The keychain must never be called while holding a document lock or an open SQLite
transaction — it can block, prompt, or fail on its own schedule.

### 5. SQLite is adopted as a seam now and a dependency later

The `ContentDB` capability is declared, and the repository interfaces for conversations
and history are written against it. **The SQLite implementation is not built until the
first feature needs it** — the agent-mode epic (`nocx-dw3`) or command history
(`nocx-4ff.6`), whichever lands first. Until then the seam carries a stub, exactly as
`config.Stub` does today, and no SQLite dependency enters `go.mod`.

When it does arrive: one database (`content.db`), not one per entity; WAL mode, because
surviving a force-quit is the whole reason a desktop app takes a database at all;
`foreign_keys=ON`; short transactions through one controlled write path. Configuration
documents do **not** migrate into it for the sake of consistency.

One honesty constraint: ordinary `DELETE` leaves data in WAL pages, freelists and FTS
shadow tables. The UI says "removed from nocx", not "securely erased", unless and until
we implement checkpointing and vacuum behaviour deliberately.

### 6. What is shared is small, and schema versions are not

Shared: path resolution, the migration _protocol_, the classification vocabulary, and
export/backup policy. Paths distinguish OS roles — configuration documents in the config
dir, `content.db` in the application-data dir, disposable indexes in the cache dir,
secrets in the keychain. On macOS these may resolve under the same Application Support
tree; on Linux the XDG distinction is real.

Not shared: the schema version. Each module owns a monotonic version and its own
migrations. A single app-wide version would couple unrelated releases of settings,
profiles, conversations and history, and JSON-document migrations have different
transactional properties from SQLite ones anyway.

### 7. Export and backup are several products, not one button

With a keychain in the middle there is no honest "back up everything" unless the app
reads secrets back out. So:

- **Configuration export** — profiles, groups, credential metadata, settings; secret
  references present but unresolved.
- **Portable encrypted export** — explicit, user-authenticated, encrypted under a new
  passphrase. Not part of normal persistence.
- **Same-machine backup** — documents and `content.db`, plus a plain statement that
  secrets stay in the OS keychain.
- **Import** — metadata first, then the user maps existing credentials or supplies
  missing secrets.

Private content (conversations, history) is never silently included in a portable
export. It is frequently more sensitive than the host metadata beside it.

## Rationale

The alternative shapes were considered and rejected:

- **Everything through the settings registry.** Profiles are a collection, not a set of
  declared knobs; the model does not fit and the fit would get worse with grouping and
  credential links.
- **One shared persistence primitive.** An atomic-file-write primitive serves neither a
  keychain nor a database. Sharing the _mechanism_ only works when the mechanism is the
  same, and here it is three different mechanisms with three different failure modes.
- **A per-field sensitivity router.** The intent is right; the implementation would be a
  reflection engine that every serializer, logger and DTO must honour. Separate types
  achieve the same guarantee by construction.
- **Leave each feature its own store** (today's de facto state). Cheapest now; it is
  already producing divergence at two stores and would be entrenched at four.

`vision.md` §10 forbids cloud sync and telemetry permanently, which removes the usual
argument for a uniform serialization layer — nothing is ever shipped off the machine, so
each store is free to use the representation that suits it.

## Consequences

- `internal/config`'s `Get(key) any` interface is replaced by a typed settings registry;
  it does not survive as the general persistence API.
- PR #11's `ProfileStore` is split by aggregate (profiles, groups, credential metadata
  are three repositories, not one interface), and its private path handling is replaced
  by shared path resolution — removing the `os.TempDir()` fallback at
  `internal/app/app.go:66`.
- `credential.VaultSecret`'s JSON-serializable `Value` is removed; the renderer loses
  every RPC that returns a secret.
- Credential deletion gains secret-store cleanup.
- A new dependency (SQLite) is authorized but deliberately deferred; adding it needs no
  further decision, only a feature that requires it.
- Export/backup becomes a designed feature with several modes rather than an assumed
  file copy.
- Migration from any pre-existing plaintext secret can prevent future plaintext writes
  but cannot erase old backups, snapshots or exports. Migration messaging must say so.
- **Process memory is out of scope, and this is a limitation rather than a gap.** A
  secret in use is a Go value; it can appear in a core dump or in memory-bearing OS
  diagnostics, and Go offers no reliable way to erase every copy once a `[]byte` has
  become a `string`. Third-party libraries — `x/crypto/ssh` among them — retain their own
  copies. The `Secret` type narrows accidental disclosure through serializers, formatters
  and logs; it does not and cannot make the process memory-safe. Anything stronger would
  need a crash-dump policy and an OS-level diagnostics decision, which this ADR does not
  make.

## Revisit when

- A second machine enters the picture in any form — the no-sync premise is what allows
  per-store representations to diverge freely.
- Configuration documents outgrow hand-editability, which would undermine the reason
  they are JSON rather than rows.
- An OS keychain proves unavailable or unusable on a supported platform, forcing an
  app-managed encrypted secret store as a second `SecretStore` implementation.
