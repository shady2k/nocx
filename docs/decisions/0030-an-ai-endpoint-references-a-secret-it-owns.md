# ADR-0030 — An AI endpoint references a secret it owns

- **Status:** Accepted
- **Date:** 2026-08-14
- **Related:** ADR-0011 §2 (secrets as opaque references) and §4 (metadata-first
  deletion), ADR-0016 (a secret owns its name), ADR-0017 (a connection
  references a secret), ADR-0031 (vault reset counts every reference holder),
  design spec `.internal/specs/2026-08-13-ai-assistant-surface-design.md` §4.5,
  bead `nocx-kn9q`
- **Extends:** ADR-0016 and ADR-0017. A connection already references a secret
  by opaque reference; this records how an AI endpoint record uses the same
  answer, and adds the one decision neither ADR settled: who owns the key.

## Context

The assistant's configuration surface needs an endpoint record: a display
name, a base URL, a wire schema, a credential, and one or more models each
with an optional picker alias. Design §4.5 models the record on Warp's
custom-endpoint dialog with three deliberate differences, and two of them are
already decided: the API key is never in the record (an opaque vault
reference instead, ADR-0016/0017), and the wire schema is a field on the
record with no select while one implementation exists.

What neither ADR settled is the **ownership** of an endpoint's key, and the
lifecycle that follows. A connection's password is named and referenced by
the connection, but its material is written by a form field the user may
also replace on the Secrets page. An endpoint's key is the same shape —
except that the endpoint is the only record that mints it. Three questions
had to be answered before any handler could be written:

1. Where does the record live?
2. What happens to the key when the endpoint is deleted or its key field is
   re-filled?
3. What is true on disk and in the vault on each failure, and what recovers?

## Decision

**An endpoint is a config record in the profile store family, and it owns
the secret it mints.**

### 1. Storage

The record lives in the same JSON document as profiles and groups
(`internal/profile`), not in `internal/settings` (scalar declarations) and
not in a second store. Two reasons, and the second is the one that settles
it:

- **It is the same concept.** A list of records that own vault secret
  references is exactly what `internal/profile` already is; a second store
  is a second implementation of one concept, which is the defect
  AGENTS.md's "look for the existing answer" rule exists to prevent.
- **The sweeps must stay one atomic write.** `secretrefs.go` exists
  because clearing every reference on a vault reset "has to be one atomic
  write, or an interruption leaves half the store pointing at a vault that
  has gone". `ClearSecretRefs` — the metadata-first half of deleting a
  secret (ADR-0011 §4) — has the same requirement. A second store would
  split both operations across two documents that could disagree. Extending
  the one document keeps them one write; the bookkeeping consequences are
  ADR-0031.

### 2. The endpoint owns its key

The form's key field is an **input**, never a persisted value. At create,
the backend mints the material into the vault (`vault.CreateNamed`,
auto-named `<endpoint name> API key` per ADR-0016 — the name derives from
what is already known, never from the material) and the record holds only
the reference. At update with a new key, the backend **rotates** the
material behind the endpoint's own secret (`vault.ReplaceSecret`: same id,
same name, new material) — this is exactly the rotation ADR-0017 §2 names
as the only one the storage model can honestly support. Deleting the
endpoint deletes the record, then the material, metadata-first (ADR-0011
§4). A key deleted on the Secrets page clears the endpoint's reference
through the extended `ClearSecretRefs` in the same atomic write that clears
profile bindings; the endpoint survives, credential-less, and says so.

Why rotation rather than mint-new-and-swap on update: mint-and-swap changes
the reference, which makes every other record that shares the secret
ambiguous — either it silently follows the new key or it keeps a reference
to a secret nobody owns. Rotation keeps the id stable, so sharing keeps its
plain meaning (whoever references the secret uses its current material),
and the vault's journal makes the write crash-safe. The endpoint never
orphans a key on update by construction.

### 3. The wire

The credential crosses the wire only as a **row handle** (`secrow:...`),
never the reference (ADR-0017 §1, the `nocx-jb20.1` line). `null` when no
key is set. The key is sent once in the create/update params, minted by
the backend, never echoed, never persisted (`credential.Secret` redacts in
every fmt/slog path, so a logging mistake cannot leak it).

### 4. Failure contracts, verified against the vault's journal

Each failure names what is true afterwards and what recovers:

- **Mint fails (create/update with a key)** — no record is written; the
  RPC errors; the user retries. Nothing to recover.
- **Store write fails after the mint** — the minted secret is a record-less
  orphan. The vault's journal (PhasePrepared/PhaseSecretWritten, empty
  target) survives; `Reconcile` deletes the orphan at the next start
  (journal.go:119-137). Verified.
- **Rotate fails (update with a key)** — the record is untouched; the RPC
  errors; the user retries. The rotation is same-id, so a later retry
  converges.
- **Store write fails after the rotate** — the material is rotated but the
  record kept its old fields; the reference is the same id, so nothing is
  dangling. The user retries.
- **Material delete fails (endpoint delete)** — `vault.Delete` journals
  `Op=delete` and drops the catalogue record in the same document write
  (vault.go:1241-1254) before calling the provider; on provider failure the
  entry survives and `Reconcile` re-attempts the provider delete at the next
  start (journal.go:119-137). That retry succeeds for lock-independent
  providers — the OS keychain, the product's default — and is blocked for
  the file store while the vault is sealed (the file provider's Delete
  needs the data key, file.go:271-276): the entry is retained and logged,
  and a later reset sweeps the residue. Either way the record and the
  catalogue row are gone, so nothing points at the material while it awaits
  cleanup. This is the same machinery `vault.deleteSecret` already relies
  on; the endpoint delete adds no second answer.
- **Vault sealed (endpoint delete)** — `vault.Delete` refuses before
  journaling or dropping the catalogue record (vault.go:1220-1222), so the
  secret stays visible and deletable on the Secrets page. The endpoint is
  gone; the key is the user's to remove there.

## Why the record-first order on delete

ADR-0011 §4 settled the direction: "a brief unreachable orphan is safer
than metadata pointing at a secret that is gone". The endpoint delete
removes the record and clears every remaining binding to its key in the
same write — a shared secret loses its other bindings exactly the way
`ClearSecretRefs` does when the user deletes a shared secret from the
Secrets page — and only then touches the material; every interruption
leaves either a fully-deleted endpoint or an ownerless secret the journal
retires, never a record claiming a key that cannot exist.

## Alternatives considered

**Leave the minted key as an ownerless secret on the Secrets page after
endpoint deletion.** ADR-0016 made ownerless secrets first-class, so this
is coherent — and it is what a key deleted on the Secrets page does. It was
rejected for the delete path because the endpoint is the only natural owner
of its key: an endpoint that is gone leaving a key nobody can attribute is
the exact "orphan" the metadata-first order exists to avoid, and the user
deleted the endpoint expecting its credential to go with it. The ownership
rule makes the delete path total: no orphan on success, a journaled,
retried cleanup on provider failure.

**Mint-new-and-swap on update.** Rejected in §2: it changes the reference
and makes the shared-secret case ambiguous.

## Consequences

- `internal/profile` gains an `Endpoint` record type and endpoint CRUD;
  `SecretReferenceImpact`, `ClearAllSecretReferences` and `ClearSecretRefs`
  extend to endpoints (ADR-0031).
- `internal/capability`'s config service gains the four endpoint methods;
  the config operation gains a narrow `EndpointSecrets` vault seam
  (`CreateNamed`, `ReplaceSecret`, `Delete`).
- The transport gains `endpoints.create|list|update|delete`, each with a
  schema in `contracts/`; the renderer's types are generated from them.
- An endpoint's key appears on the Secrets page as a password-kind secret
  named `<endpoint name> API key`; deleting it there clears the endpoint's
  reference in the same write.
- The secrets inventory's usage projection counts connections only; an
  endpoint-owned key reads `usedBy: 0` until the surface that renders
  endpoint usage lands. Named as a gap in design §4.5.6, not a silent
  under-report.
- The backup format carries profiles and groups; endpoints ride the next
  format version. No endpoint data exists yet, so nothing is lost today
  (design §4.5.6).

## Not decided here

- **Whether an endpoint may reference a secret picked from the Secrets page
  (a picker rather than a key input). This pass has no picker; if one
  arrives, the ownership rule changes and this ADR is the place that says
  so.**

  Decided by nocx-rzjw, 2026-08-15: the picker arrived. The ownership rule
  is now **owning is a case of referencing**: an endpoint REFERENCES a
  secret; when it mints one (a typed key) it also owns it, and when it
  references an existing secret it does not. The consequences:

  - The key field is a source control ("type a new one" / "use an existing
    secret") — the same control the connections editor's password field
    uses (one vocabulary, one component).
  - A reference swap (updating an endpoint to reference a different secret)
    touches no material: nothing is minted, rotated or deleted. An owned
    key the endpoint abandons stops being referenced and stays visible on
    the Secrets page, where ADR-0016 makes ownerless secrets first-class.
  - The custom HTTP headers an endpoint sends (nocx-lyyk) may reference
    vault secrets the same way; `ClearSecretRefs` and the reset impact
    cover header references alongside the credential (a header whose secret
    is deleted is dropped from the record, never left sourceless).

- The address restriction and the Test button — `nocx-edio`, with the HTTP
  client (design §4.5).
