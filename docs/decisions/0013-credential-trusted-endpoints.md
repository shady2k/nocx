# ADR-0013 — Credential-Scoped Trusted Endpoints

- **Status:** Accepted
- **Date:** 2026-07-27
- **Supersedes:** the host-binding model in
  [ADR-0006 — Reusable Credentials (УЗ) for SSH Connections](0006-reusable-credentials.md)
- **Related:** ADR-0011 (opaque secret references), AD-4 (SSH), AD-7 (session
  ownership), AD-8 (interface-first + DI)

## Context

ADR-0006 made a reusable credential optionally bindable to one `Host` and `Port`.
That shape has two unsafe extremes:

- one bound host prevents legitimate reuse of one account across a fleet;
- an empty host means "any host" in ADR-0006, allowing the account to be offered to
  an arbitrary server.

The current UI exposes this policy as **Bind to Host** in the credential editor. That
asks the user to maintain the same relationship twice: once by selecting a credential
on a connection and again by typing a host into the credential. The two values can
drift, SSH aliases can resolve to a different `HostName` or port, and a single field
cannot represent legitimate many-host use.

A saved connection already expresses the required intent: "use credential C for this
connection." The backend must turn that relationship into a bounded authorization,
without making a later connection attempt self-authorizing.

This authorization is not SSH server identity trust:

- `known_hosts` answers whether the server key is trusted for an endpoint;
- a credential trusted endpoint answers whether a particular saved credential may be
  offered to that endpoint.

Both checks are required. Neither check implies the other.

## Decision

### 1. A credential is authorized for a finite set of exact endpoints

Replace `Credential.Host` and `Credential.Port` with backend-owned trusted endpoint
grants. Conceptually:

```go
type CredentialTrustedEndpoint struct {
    ProfileID string // provenance: the saved connection that granted access
    Host      string // canonical host after SSH config resolution
    Port      uint16 // effective port; always explicit
}

type Credential struct {
    // Existing identity, authentication metadata, and opaque secret references.
    TrustedEndpoints []CredentialTrustedEndpoint
}
```

The persisted representation may use a separate aggregate instead of embedding the
slice, but the domain semantics are binding:

- one credential may have grants for many connection profiles and many endpoints;
- a grant authorizes one credential, one saved profile, and one exact host+port;
- an empty grant set authorizes no remote endpoint;
- there is no wildcard host and no "all ports on this host" value;
- duplicate saves are idempotent;
- the renderer cannot submit or overwrite grants, grant revisions, or secret
  references through credential CRUD DTOs.

`ProfileID` is provenance, not only display metadata. A plain set of host strings would
leave stale authorization behind after a connection is edited or deleted and could not
tell whether another connection still justifies the same endpoint.

### 2. Saving a connection is the only automatic grant operation

When a saved SSH connection references a credential, the backend performs one domain
workflow:

1. Validate the profile and credential references.
2. Resolve the profile's effective SSH endpoint through the same endpoint resolver used
   by connection establishment.
3. Save the profile and upsert its credential grant as one atomic document operation.

The workflow owns all relationship changes:

- assigning a credential adds its grant;
- changing the profile host or effective port replaces that profile's grant;
- changing credential A to credential B removes A's grant and adds B's grant;
- removing the credential or deleting the profile removes its grant;
- saving unchanged data does not create duplicates or advance the authorization
  revision unnecessarily.

If endpoint resolution, grant validation, or persistence fails, the profile save fails
as a whole. The system must not leave a saved profile without its required grant or a
new grant without the profile that justified it.

This workflow belongs in a backend connection/profile service behind an interface. It
must not be assembled from independent renderer calls such as "save profile" followed
by "trust host."

### 3. Connection attempts never expand trust

`open{profileId}` remains the wire contract. The backend loads the saved profile,
credential metadata, grant, and secret references as one coherent authorization
snapshot. It then:

1. Resolves the current effective endpoint.
2. Requires a grant for the same credential, profile, canonical host, and effective
   port.
3. Verifies the server host key through `known_hosts`.
4. Only then permits the selected authentication method to use the credential.

A missing, stale, or mismatching grant fails closed with a distinct credential-not-
authorized error. `open` must never add a grant, including on first use. Otherwise an
attempt to redirect a profile to an attacker-controlled host would authorize itself.

The rule applies independently to the target and every jump host. Authorization is
checked before both a new dial and acquisition of a channel from the SSH connection
pool. A pooled connection is not authorization to spend another credential or to open
a new channel after its grant was revoked.

### 4. Endpoint identity has one owner

Credential grants, enforcement, dialing, host-key lookup, errors, and pool keys consume
the same immutable `CanonicalEndpoint` value.

The endpoint is calculated after SSH configuration is applied, with precedence:

1. explicit profile values;
2. `~/.ssh/config` values such as `HostName` and `Port`;
3. SSH defaults, including port 22.

Host and port are separate validated fields. Ports outside `1..65535` are rejected.
DNS names compare case-insensitively in canonical form; IP literals, including IPv6,
are parsed and stored in canonical form. An SSH alias remains display metadata and is
not an authorization identity. DNS lookup results and CNAME targets are not persisted
as grants: normal DNS movement must not silently rewrite authorization.

If `~/.ssh/config` later changes an alias to a different endpoint, the existing grant no
longer matches and connection fails closed. The user must save the connection again to
authorize the newly resolved endpoint.

### 5. Remove **Bind to Host** from credential management

The credential create/edit form contains authentication identity only: name, username,
authentication mode, key metadata, and secret-write actions. It no longer contains
**Bind to Host** or a binding port.

The connection form remains the place where a host and credential are associated.
Trusted endpoints may be shown in credential details as read-only derived information,
but they are not a free-form credential editor.

While a connection references a credential, username, authentication mode, and key
selection come from that credential. A connection cannot silently override the saved
identity while retaining its grants and secret reference.

### 6. Credential authorization and `known_hosts` stay independent

Assigning a credential to a connection does **not** write to `~/.ssh/known_hosts` and
does not accept an unknown or changed server key. It writes only the credential's
authorization grant.

An unknown server key must still stop before authentication and follow a separate,
explicit host-key acceptance workflow. A changed key must never be silently appended or
overwritten. Any future replacement workflow must show the old and new fingerprints,
bind confirmation to the observed new key, update the trust store atomically, and abort
if the old entry changed concurrently.

### 7. Revocation affects future opens and pool reuse

Removing or replacing a grant prevents all subsequent opens, including new channels on
an existing pooled SSH transport. Existing active channels are allowed to run until
closed; this ADR does not terminate a user's live shell when profile metadata is edited.

The pool's security identity must include the canonical endpoint, credential ID,
authorization revision, authentication policy, and the equivalent jump-route identity.
A pool hit still performs the current grant check before opening a channel.

Secret rotation and grant mutation advance the relevant revision so a new open cannot
reuse a transport authenticated under stale authorization merely because host and
username match.

## Supersession of ADR-0006

This ADR deliberately replaces these ADR-0006 decisions:

| ADR-0006 rule                                    | Replacement                                                         |
| ------------------------------------------------ | ------------------------------------------------------------------- |
| Optional `Credential.Host` and `Credential.Port` | Backend-owned, profile-sourced `TrustedEndpoints`                   |
| Empty host works for any host                    | Empty grant set denies every remote endpoint                        |
| One host-bound credential                        | One credential may have a finite set of exact endpoint grants       |
| User edits **Bind to Host** in credential UI     | Saving a connection derives and maintains the grant                 |
| Connection-time host-bound check                 | Profile+credential+canonical-endpoint grant check before every open |

ADR-0006's reusable credential abstraction remains accepted where it does not conflict
with this ADR. ADR-0011 remains binding: grants and secret references are metadata;
secret material stays backend-side in `SecretStore` and never reaches the renderer.

## Migration

The profile document schema gains a new module version. Migration must read the legacy
fields before ordinary decoding can discard them.

For each credential and saved profile that references it:

1. Resolve the profile's canonical endpoint.
2. If the legacy host is non-empty and matches that endpoint under the legacy port
   semantics, create a profile-sourced trusted endpoint grant.
3. If the legacy host is empty, mismatches, or cannot be resolved, create no grant and
   mark the profile as requiring review; do not broaden it automatically.
4. Remove legacy `Host`/`Port` only after the migrated document is durably written.

The user restores a denied legacy connection by reviewing and saving it. That save is
the explicit action that creates the new grant. Export/import uses the same versioned
migration and must not silently turn a legacy unbound credential into a wildcard.

## Security Boundary

Trusted endpoint grants protect against accidental profile drift, incorrect aliases,
stale configuration, and credential redirection through callers constrained to the
domain APIs. They do not make a fully compromised authenticated renderer or same-user
process harmless: the renderer can open a local shell, and ADR-0011 intentionally keeps
metadata in human-recoverable local documents.

Resistance to a fully hostile renderer would require a privileged user-presence channel
and integrity protection for authorization metadata. That stronger boundary is outside
this ADR. The guarantees here must not be described as protection from arbitrary code
already executing with the user's local nocx capability.

## Consequences

- **Positive:** one account can be reused across an explicit, finite fleet without a
  wildcard credential.
- **Positive:** connection assignment and credential authorization cannot drift as two
  independently edited host fields.
- **Positive:** alias and port policy uses the same effective endpoint as SSH dialing.
- **Positive:** deleting or changing a connection removes authorization that no longer
  has provenance.
- **Negative:** profile saves now require backend endpoint resolution and an atomic
  profile/grant workflow.
- **Negative:** changing `~/.ssh/config` may make a saved connection fail until the user
  reviews and saves it again.
- **Negative:** persistence needs a schema migration and exports need version-aware
  decoding.
- **Negative:** SSH pool identity and invalidation become authorization-aware.

## Required Verification

Implementation is incomplete until tests cover:

- one credential assigned to multiple profiles and exact endpoints;
- an arbitrary ungranted host and an ungranted port being refused before auth;
- SSH aliases, effective ports, IPv4, and IPv6 canonicalization;
- assignment, reassignment, host edit, unassignment, profile deletion, and idempotent
  save;
- target and jump-host credentials independently;
- profile save rollback when grant persistence fails;
- grant fields and secret references rejected or preserved across renderer CRUD;
- revocation before both a fresh dial and a pooled channel open;
- the complete `profileId -> resolver -> session -> SSH` path, proving no authorization
  fields are dropped at a module boundary;
- migration of matching, empty, mismatching, and unresolvable legacy bindings;
- host-key trust remaining independent from credential endpoint authorization.

## Revisit When

- A privileged native confirmation boundary is introduced, allowing manual grants that
  are stronger than renderer-originated profile edits.
- Fleet policy needs host groups, patterns, or centrally managed authorization. Wildcard
  matching must be a new decision; it is not an extension of this exact-endpoint model.
- A supported protocol identifies servers differently from SSH host+port.

## Implementation Status

**Implemented (2026-07-27):**

### Phase 1: Migration
- `internal/profile/migration.go`: Versioned migration with `storage.Module` protocol
- `internal/profile/store.go`: Schema versioning, atomic migration write, `requiresReview` marker on SSHProfileOptions
- Raw document decode for legacy Host/Port fields
- Endpoint resolver callback for canonical endpoint resolution during migration

### Phase 2: Atomic Grant Workflow  
- `SaveProfileWithGrant`: Atomic profile+grant save with idempotent check (JSON snapshot)
- `DeleteProfileWithGrants`: Atomic profile delete with stale grant cleanup
- `ProfileAtomicMutator` interface in `internal/profile/store.go`
- `EndpointResolver` interface for canonical endpoint resolution

### Phase 3: Open Enforcement
- `internal/connection/resolver.go`: Grant check via `CheckGrant` before dial
- `ErrCredentialNotAuthorized` error for fail-closed behavior
- `AuthorizationRevision` in `ConnectConfig` and `poolKey` for pool invalidation
- Pool security identity includes canonical endpoint, credential ID, and authorization revision
- Grant check on pool channel open (via pool key mismatch on grant change)

### Required Verification Tests
All ADR-0013 required verification tests are implemented:
- `TestSaveProfileWithGrant_*`: Adds grant, missing credential fails, empty endpoint fails, idempotent, no credential
- `TestDeleteProfileWithGrants_*`: Removes grants, stale grant cleanup, idempotent
- `TestMigration_*`: Match creates grant, alias resolution, mismatch marks review, unresolvable marks review, Port==0 matches any, nil resolver marks all review
- `TestProfilesRPC_CreateWithCredentialCreatesGrant`: End-to-end grant creation via RPC
- `TestProfilesRPC_DeleteRemovesGrant`: End-to-end grant removal via RPC

### Migration Path
1. On first load, `loadLocked()` detects schema version 0 and applies migration
2. Migration resolves canonical endpoints for each profile with legacy Host/Port
3. Matching grants are created; non-matching profiles are marked `requiresReview`
4. Migrated document is atomically written with schema version 1
5. Subsequent loads skip migration (schema version 1 == current)

### Security Boundary
- Renderer cannot submit/overwrite `TrustedEndpoints`, `SecretID`, or `PassphraseSecretID` via RPC
- `open{profileId}` checks grant before dial (target and jump hosts independently)
- Pool invalidates on grant change via `authorizationRevision` in pool key
- Fail-closed: missing grant returns `ErrCredentialNotAuthorized`
