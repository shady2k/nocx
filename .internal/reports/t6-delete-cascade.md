# T6 — deleting a credential deletes its secrets (nocx-7l4, PR11-T6)

## What changed

The `credentials.delete` RPC used to call `s.profiles.DeleteCredential(id)`
and stop there — metadata gone, the keychain entry orphaned forever. It now
goes through `WSServer.deleteCredentialCascade` (new, `internal/transport/
ws.go`), which removes the metadata **and** every secret the credential
references, in an order that cannot strand one.

A new canonical derivation, `credential.HashKey([]byte) credential.KeyHash`
(`internal/credential/credential.go`), turns raw key bytes into the
`sha512:<hex>` lookup key. This is the contract both the (future) save path
and the delete path must use, so a passphrase saved under `HashKey(data)` is
reachable for deletion by re-reading the same key file.

### Files

- `internal/credential/credential.go` — added `HashKey` (+ imports
  `crypto/sha512`, `encoding/hex`).
- `internal/credential/credential_test.go` — `TestHashKey` (format contract,
  stability, collision).
- `internal/transport/ws.go` — `credentials.delete` case now calls
  `deleteCredentialCascade`; added `deleteCredentialCascade`,
  `findCredentialByID`, `deriveKeyHashFromPath` (+ `os` import).
- `internal/transport/ws_cascade_test.go` — new, four cascade tests.

## The ordering trap and how this resolves it

The two secret kinds are keyed differently:

- a **password** keys on `Identity{User: credentialID}` — derivable from the
  ID alone;
- a **key passphrase** keys on `KeyHash` — derived from the key material at
  `KeyPath`, and the metadata stores only `KeyPath`.

Once the metadata row is gone there is no `KeyPath`, no hash can be derived,
and the passphrase is unreachable forever. The cascade therefore:

1. **Loads the metadata BEFORE deleting it** (`findCredentialByID`).
2. **Derives the key hash BEFORE deleting metadata**
   (`deriveKeyHashFromPath`), so a key-read failure is known while the
   metadata still exists — the "derive everything you need, then remove"
   step from the brief.
3. **Deletes metadata first** (`profiles.DeleteCredential`). Its deletion
   stands regardless of secret-deletion outcome.
4. **Deletes both secrets best-effort afterwards**, aggregating errors with
   `errors.Join` so a single failure does not skip the other.

## Decisions recorded in the code

### Order: metadata-first, then best-effort secrets (ADR-0011 §4)

ADR-0011 §4 is decisive: _"Deletion goes metadata-first with a retriable
secret deletion after: a brief unreachable orphan is safer than metadata
pointing at a secret that is gone."_ So metadata deletion stands; secret
deletion is attempted after and its failure is **returned to the caller**
(`errors.Join`) so the cascade is not silently incomplete, but the metadata
is **not rolled back** — the credential is gone either way. The orphan is
observable and a future janitor can sweep it by hash.

The reverse order (secrets-first, then metadata) strands **metadata pointing
at a deleted secret**: every connect attempt for a surviving reference fails
in a way the user cannot diagnose. That is the worse failure.

### Missing secret is not an error

Both backends already treat "already absent" as success:

- `Keychain.DeletePassword` / `DeleteKeyPassphrase` return `nil` on
  `keyring.ErrNotFound`;
- `Vault.DeleteSecret` returns `nil` when no entry matches.

So deleting a credential that never had a password (or whose passphrase was
never saved) converges with deleting one whose secrets were already removed.
No special-casing was needed. Covered by `TestDeleteCascade_NoSecretsSucceeds`.

### Multi-profile references

The model allows several `SSHProfileOptions.CredentialID` values to point at
one credential (`profile.go:56` — `CredentialID` is a bare string link, no
reference count). **This cascade does NOT refuse when other profiles
reference the credential.** The user asked to delete it; refusing would leave
an orphaned-credential path the UI cannot recover from. Surviving references
become stale: their password/passphrase lookups return empty, which is the
same state a never-had-a-secret credential is already in. Reference
integrity (refusing delete while references exist, or nulling them) is a
separate work item — recorded here, not silently introduced.

### Shared key files

Two credentials pointing at the same `KeyPath` share one passphrase entry
(content-addressed by `HashKey`). Deleting the first credential removes the
passphrase; the second's lookup then returns empty. The model has no secret
reference count, so this is the same compromise as multi-profile references:
documented, not silently "fixed" with a count that does not exist.

### KeyPath unreadable

If the key file at `KeyPath` can no longer be read (moved, deleted,
permission-denied), the passphrase entry cannot be reached for deletion.
Per ADR-0011 §4 the metadata deletion still stands; the unreadable-key error
is aggregated into the returned error so the orphan is observable, and a
future janitor can sweep by hash. The hash is derived **before** metadata
deletion, so this failure is known while the metadata still exists — but we
proceed with metadata deletion anyway, because the orphan is the lesser
failure.

## Verification

TDD: tests written red first, then implementation.

`TestDeleteCascade_RemovesKeyPassphrase` is the trap test: it failed on the
naive delete-metadata-only implementation (confirmed in the red run:
"key passphrase survived credential delete; the secret is orphaned forever")
and passes after the cascade. It writes a real key file, saves a passphrase
under `HashKey(contents)`, deletes the credential, and queries the store
directly (not through the RPC under test) to assert the entry is gone.

All four cascade tests:

- `TestDeleteCascade_RemovesPassword` — password entry gone after delete.
- `TestDeleteCascade_RemovesKeyPassphrase` — passphrase entry gone; fails on
  naive impl.
- `TestDeleteCascade_NoSecretsSucceeds` — credential with no secrets deletes
  cleanly.
- `TestDeleteCascade_Idempotent` — second delete of an already-gone
  credential succeeds; no secret remains.

### Test run

```
$ go test ./internal/transport/... ./internal/credential/...
ok  github.com/shady2k/nocx/internal/transport   15.662s
ok  github.com/shady2k/nocx/internal/credential  1.281s
exit: 0
```

### Gate: `gofumpt -l .`

- **Owned files clean:** `internal/credential/credential.go`,
  `internal/credential/credential_test.go`, `internal/transport/ws.go`,
  `internal/transport/ws_cascade_test.go` — gofumpt reports 0.
- **2 non-owned files flagged:** `internal/ssh/ssh_binding_test.go`,
  `internal/ssh/ssh_config.go` — owned by the nocx-mon/PR11-T5 worker
  (`internal/ssh/**` is outside my scope: `internal/transport/ws.go` and
  `internal/credential/**` only). Not touched.

### Deletion review

`git diff HEAD -- internal | grep '^-'` (excluding `---` headers) yields 3
deletion lines:

1. `internal/connection/resolver.go`: `-		// Inline mode: use profile's own fields.`
   — **other worker's change** (nocx-mon/PR11-T5 binding work).
2. `internal/profile/profile.go`: `-	// Optional: bind to specific host (empty = works for any host)`
   — **other worker's change** (nocx-mon/PR11-T5).
3. `internal/transport/ws.go`: `-		if err := s.profiles.DeleteCredential(params.ID); err != nil {`
   — **mine**, the naive metadata-only delete, replaced by the cascade.

The only deletion in my owned files is the intended replacement of the
metadata-only delete with `deleteCredentialCascade`.

## Diff stat (owned files only)

```
internal/credential/credential.go      |  23 ++++++
internal/credential/credential_test.go |  24 ++++++
internal/transport/ws.go               | 137 ++++++++++++++++++++++++++++++++-
internal/transport/ws_cascade_test.go  | 195 +++++++++++++++++++++++++++++++++ (new)
```

3 files changed (+183 -1), 1 new file (+195).

## What's left

Nothing in scope. Reference integrity (refusing delete while profiles
reference the credential, or nulling their `CredentialID`) is a deliberate
non-goal recorded above. A janitor that sweeps orphaned secrets by hash
(after a crashed cascade) is a future hardening item, not part of T6.
