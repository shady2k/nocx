# ADR-0015 — Structured Backup & Restore without credentials

- **Status:** Accepted
- **Date:** 2026-07-30
- **Related:** ADR-0006 (reusable credentials), ADR-0011 (persistence: storage capabilities
  and secrets as opaque references)
- **Revises:** ADR-0011 §7

## Context

ADR-0011 §7 defined four export/backup modes: configuration export (profiles, groups,
credential metadata, settings with unresolved secret references), portable encrypted
export (explicit, passphrase-encrypted), same-machine backup (documents + content.db with
a plain statement that secrets stay in the OS keychain), and import (metadata first,
then user maps existing credentials or supplies missing secrets).

The four-mode design was specified before the settings registry replaced `internal/config`,
before `PrivateContent` data classification was added, and before the credential model
stabilised with per-credential host binding and `SecretAuthenticator` classification.
The implementation (`internal/export`, `nocx-cym`) built all four modes but none of them
reached a user — no JSON-RPC methods were wired and no UI entry point existed
(`nocx-6ek.3`).

Three problems make the four-mode design the wrong shape now:

1. **The portable encrypted mode cannot deliver what it promises.** Encrypting credentials
   for transport requires the app to read secrets back from the keychain, which
   contradicts ADR-0011's load-bearing rule that a secret exists only inside
   secret-specific types and APIs. The export package correctly does not import
   `credential.SecretStore`, making the structural invariant hold, but the consequence is
   that no mode — including portable encrypted — can resolve a secret. A portable export
   that cannot carry secrets is a configuration export with a passphrase wrapper.

2. **The same-machine backup path listing is an operational concern, not a product
   feature.** Telling the user where `profiles.json` and `content.db` live on disk is a
   documentation line, not a dedicated backup mode. The app should produce a
   self-contained file the user can store anywhere.

3. **Import without credential resolution is misleading.** Restoring profiles whose
   credential references are empty or unresolvable produces broken connections that look
   complete until the user tries to connect. The UI must explicitly enumerate which
   connections need credential reassignment after restore.

## Decision

### One product: Backup & Restore

The four modes are replaced by a single **Backup & Restore** capability producing one
versioned, structured, plaintext JSON file. The file is self-contained and does not
reference the keychain, secret store, or content database. Credential records, `SecretID`
fields, and `ContentDB` are explicitly absent from the backup format.

This is a plaintext product — not encrypted, not portable in the ADR-0011 §7 sense.
Encryption of the backup file is a transport-layer concern (the user's filesystem, a USB
drive, a Veracrypt volume) and is not implemented by nocx.

### Backup scope

The backup file (`nocx-backup` v1, ≤8 MiB UTF-8 JSON) contains:

- **Saved non-secret settings overrides** — all `Registry` values whose `DataClass` is
  `PublicConfig`, `PrivateMetadata`, or `PrivateContent`. Settings with
  `DataClass=SecretAuthenticator` are excluded. Declared defaults and secret references
  are not included; the backup carries only user-saved overrides.
- **SSH Connections (profiles)** — all fields from `SSHProfile` except `CredentialID`.
  Each profile with a non-empty `CredentialID` (or whose assigned group has a non-empty
  `defaults.ssh.options.credentialId`) carries `requiresCredential: true`.
- **Profile groups (tree)** — group identity fields (`ID`, `ParentGroupID`, `Name`,
  `Icon`, `Color`, `Editable`) plus a typed subset of `defaults.ssh.options` (ten safe
  fields: `Host`, `Port`, `User`, `Auth` mode, `KeepaliveInterval`, `KeepaliveCountMax`,
  `ReadyTimeout`, `JumpHost`, `AgentForward`, `CanBeJumpServer`). Group-level
  `credentialId` defaults are never transferred and are noted as omissions.

The backup explicitly **excludes**:

- `Credential` records (credential metadata) — they are stored in the same
  `profiles.json` but are not exported.
- Any `credentialId` field — the backup has no `credentialId` key on any object.
- `SecretID`, `PassphraseSecretID`, or any secret reference.
- OS keychain / `SecretStore` material — no secret value is read or written.
- `ContentDB` content (conversations, command history) — future ADRs may add this as an
  opt-in backup section; v1 excludes it entirely.
- App state (window layout, open tabs) — not in scope.

### Restore strategies

Two strategies, both presented in the UI with preview and confirmation:

- **Merge** (default): backup overrides win per key; local overrides for keys absent from
  backup are preserved. Profiles and groups with matching IDs receive backup-owned fields.
  Local direct `credentialId` on a matching connection survives only when the trimmed
  host and effective port (0 → 22) are unchanged from the backup; a changed host/port
  clears the binding. Matching groups lose their local group-default credential binding
  because it is not tied to a single host identity. New records from the backup are
  added; local records not in the backup are kept.
- **Replace**: non-secret settings overrides become exactly the backup's set (others
  reset to declared defaults). Profiles and groups become exactly the backup's set, in
  backup order. All connections and group defaults are restored without `credentialId`.

Both strategies preserve `Credential` metadata (credential records) and keychain entries
— the backup does not touch them.

### Preview and confirmation

Before restore executes, the UI shows a preview computed from the backup file, selected
strategy, and current local state:

- Counts: included, changed/added/updated/removed/reset settings, connections, and groups.
- Connections requiring credential reassignment (ID + name).
- Omissions: credential bindings removed from connections, credential bindings removed
  from groups, group default keys omitted from backup.

The preview produces a token (SHA-256 over the file contents, strategy, and canonical
current state). The restore call must present the same token; a mismatch (stale preview)
is rejected before any write, and the UI must re-preview and re-confirm.

### Crash recovery and configuration gate

Backup create, preview, and restore operations hold an exclusive configuration gate lock
shared with all profile/group/credential/settings CRUD methods and `open`. This prevents
concurrent mutation during backup read or restore write.

Restore uses a local crash-safe journal (`prepared → committed → idle`):

1. Durable `prepared` with before-snapshots of connections and settings.
2. One atomic connection write.
3. One atomic settings write (notification deferred).
4. Durable `committed` retaining before-snapshots.
5. Best-effort cleanup to `idle`.
6. Publish deferred settings change notification.

On restart, `Recover()` reads the journal:
- `idle` / absent → no-op.
- `prepared` → roll back both connection and settings to before-snapshots, clean to
  `idle`.
- `committed` → the applied state is correct; clean to `idle`.
- Unknown version/state or corrupt journal → unrecoverable error.

If runtime restore encounters an error after journaling `prepared` but before proven
`committed`, it calls `Recover()` immediately. If recovery succeeds, the original restore
error is returned. If recovery fails, the configuration gate is poisoned until restart.

### Backup format versioning

The backup format has its own version (`version: 1` in the document), separate from the
app-wide schema or any per-module schema version. v1 accepts only v1 files; future format
changes require a migration strategy or a new ADR revising the format. The app-wide
settings/profiles schema versions are not coupled to the backup format.

### Plaintext warning

All settings values, hostnames, connection names, and inline `user`/`auth` fields are
potentially private metadata/content and are stored in plaintext in the backup file. The
UI carries a visible warning. A future ADR may add an opt-in encrypted backup mode; v1
is deliberately plaintext to keep the format simple, debuggable, and human-recoverable.

## Rationale

- **One file, not four modes.** The original four modes were designed for an app that
  could read secrets back from the keychain. Since ADR-0011 §2 forbids that, the
  encrypted portable mode is a configuration export with extra steps, and the same-machine
  backup listing is documentation, not a feature. One structured file is simpler to
  implement, test, and explain to users.
- **Credentials stay local.** By excluding credential records and secret references, the
  backup never creates a situation where a restored connection silently fails because its
  credential is missing. The `requiresCredential` flag makes the gap explicit.
- **Merge is default, replace is explicit.** Most users restoring a backup want to add
  connections from another machine or recover lost ones — merge does that. Replace is a
  destructive "reset to snapshot" for migration or factory-reset scenarios.
- **Crash safety without a database.** The journal is a single JSON document, consistent
  with ADR-0011 §1's DocumentStore decision. It is sufficient because only two documents
  (profiles + settings) are modified.
- **No ContentDB in v1.** Conversations and command history are private content under
  ADR-0011 §3. Exporting them needs its own ADR because of size (potentially large),
  privacy (more sensitive than host metadata), and format (SQLite, not JSON). Adding
  ContentDB to the backup format is a future decision, not a v1 flag.

## Consequences

- `internal/export` and its six `export.*` JSON-RPC methods are removed.
- `internal/backup` is created with `Service.Create/Preview/Restore/Recover`.
- `internal/profile.JSONStore` gains `LoadConnectionSnapshot` and
  `ReplaceConnectionSnapshot`.
- `internal/settings.Registry` gains `NonSecretOverrides`, `ReplaceNonSecretOverrides`,
  and `PendingNotification` / `Publish`.
- The old Export / Backup / Import page is replaced by a Backup & Restore page.
- ADR-0011 §7 is partially superseded; its three other storage-capability sections and
  secret-as-reference rule remain unchanged.
- Credential metadata, `SecretID` fields, `SecretStore`, and `ContentDB` are never
  imported by `internal/backup` — this is a structural invariant, not a convention.
- The backup file format (`nocx-backup` v1) becomes a stable contract; changes require
  an ADR or revisit.

## Revisit when

- Encrypted backup is requested by users — a new ADR must decide whether encryption
  happens at the backup layer (the app encrypts the file) or at the storage layer (the
  user encrypts the directory).
- `ContentDB` (conversations, command history) needs export — a new backup section
  (opt-in) or a separate export product.
- A second backup format version is needed — migration rules and backward-compatibility
  policy must be defined.
- The backup format grows beyond 8 MiB — the size limit was chosen for JSON debuggability;
  a larger limit or binary format needs a separate decision.
