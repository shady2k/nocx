# ADR-0017 — A connection references a secret, not a credential

- **Status:** Accepted
- **Date:** 2026-07-31
- **Related:** ADR-0006 (reusable credentials), ADR-0011 §2 (secrets as opaque
  references), ADR-0016 (a secret owns its name), beads `nocx-b5bu` (a secret saved on
  the Secrets page cannot be attached to a connection), `nocx-gqzg`, `nocx-cx03`
- **Supersedes:** ADR-0006 in full. The credential aggregate is deleted, not renamed; the
  Decision says what happens to each of its three jobs.

## Context

A user saved a password on the Secrets page, opened the connection editor, and could not
choose it. The Credential select offered one entry: "— None (specify below) —".

That is not a missing option. It is the model working exactly as designed, and the design
has drifted out from under itself.

**What the model is today.** A profile references a `credentialId`. The credential is a
reusable identity — username, auth mode, key path — that owns `Versions []CredentialVersion`,
and each version holds the opaque `SecretID`s. The connection editor therefore enumerates
**credentials**. A secret created on the Secrets page belongs to no credential, so it is
absent from that list by construction, and no amount of UI work will put it there.

**Why it drifted.** ADR-0006 was written when a secret had no independent existence: it was
an opaque reference inside a credential, and the credential was the only object a user could
name or reuse. ADR-0016 changed that six days later — the vault now persists a display name
per secret, precisely so a secret can exist _before_ the connection that will use it. The
Secrets page is that promise made visible: add, rename, rotate, and (soon, `nocx-pf8b`)
delete a secret on its own terms.

So the product now has two answers to "what is the reusable authentication thing the user
manages", and they are not nested cleanly:

|                                    | Credential           | Secret                |
| ---------------------------------- | -------------------- | --------------------- |
| Has a user-visible name            | yes                  | yes, since ADR-0016   |
| Can be shared across connections   | yes                  | yes                   |
| Can exist before any connection    | no                   | yes, since ADR-0016   |
| Is listed on a settings page       | was, wave 5; removed | yes, the Secrets page |
| Is what the user thinks they saved | no                   | **yes**               |

The last row is the finding. The user saved a password; the product stored a secret inside a
credential and then offered them the credential. Two objects, one intent.

**What the credential still uniquely holds.** Being fair to ADR-0006, three things:

1. **Identity** — username, auth mode, key path. But the profile already carries all three
   inline (`options.user`, `options.auth`, `options.keyPath`), and the editor shows the
   profile's, not the credential's. This is duplication, not a job.
2. **Versions and rollout** — `Versions`, `CurrentVersionID`, `CandidateVersionID`, and
   `rollout.run`, which stages a candidate and probes it before promotion. The backend is
   wired (`app.go:165`) and the method answers on the wire. **The user cannot reach any of
   it:** `RolloutPanel` is imported by its own test and by nothing else, because the
   Credentials page that hosted it was removed. So this is real, tested, unique — and
   currently a feature no user can invoke (`nocx-si5z`).
3. **The authorization anchor** — ADR-0006 wave 2 removed host binding and replaced it with
   a computed proof: the saved profile resolves to an effective (credential version,
   endpoint) pair, and that pair is authorised at connect time. The argument there is subtle
   and worth keeping: authorisation is proven from the profile the user selected, never from
   the fact that some _other_ profile uses the same credential at the same endpoint.

Only (2) and (3) are jobs. Neither of them requires the user to see the word "Credential" —
and (2) is deleted outright by this ADR, for reasons the storage model forces rather than
taste. See the Decision, §2.

## Decision

**A connection references a secret.** The connection editor offers the secrets the vault
holds, and picking one is what "this connection authenticates with that" means. `Credential`
stops being a concept the user selects, names, or is offered.

Four parts, in the order they must land:

### 1. The editor picks a secret

The Authentication section shows User, Method, and — for a method that needs one — a
**secret picker** listing what the vault holds, filtered to the kind the method needs
(`password` for Password, `private-key` for Public Key). Creating a secret inline stays
exactly as it is today: typing a password still stores one, still auto-named per ADR-0016.
What changes is that a secret which already exists is reachable, which today it is not.

The word "Credential" leaves the interface.

### 2. Versions and rollout are deleted

The first draft of this ADR planned to move versions onto the secret, and treated that as
the expensive part. The decision is the opposite: **versions and rollout are removed from
the product.** `Versions`, `CurrentVersionID`, `CandidateVersionID`, `rollout.run`, the
runner, `ResolveWithVersion` and `RolloutPanel` all go.

Two reasons, and the second is the one that settles it:

- **Nothing reaches it.** `RolloutPanel` is imported by its own test and by nothing else,
  because the Credentials page that hosted it was removed. No user has ever staged a
  candidate.
- **It cannot survive the storage model.** A version is a second generation of material
  addressable at the same time as the first, and that is not something the stores this
  product is built on will give us. The OS keychain has one entry per key: two generations
  means inventing a naming convention on top of somebody else's namespace and hoping nothing
  else writes there. An external store — HashiCorp Vault and its peers — **owns** versioning
  itself, with its own semantics for what current means and its own retention; a second
  version scheme layered above it is either ignored or actively wrong. Rotation as an
  application-level aggregate only works for a store we fully control, and we deliberately
  do not require one (ADR-0011 §1: the SecretStore is a boundary, not a file we write).

So ADR-0006's aggregate does not need a replacement. The guarantee it carried — that a
rejected candidate is never silently retried with the working secret — is not weakened by
this, because with no staging there is no candidate and no retry; a probe authenticates with
the one secret the connection names, and a rejection is reported.

Rotating a secret remains what the Secrets page already does: `vault.replaceSecret`
overwrites the material behind a secret that keeps its id and its name. That is a rotation in
the sense a user means, and it is the only one the storage model can honestly support.

### 3. The authorization proof is re-anchored, not weakened

The pair becomes (secret, endpoint), proven the same way and from the same place:

```
selected saved profile
  -> resolve its group inheritance chain
  -> obtain its effective secretId
  -> resolve its canonical endpoint once
  -> authorize that (secret, endpoint) pair
```

Per hop, justified by the effective profile that supplies **that hop's** secret — never by
the fact that some other profile happens to use the same secret at the same endpoint. That
is the whole of ADR-0006 wave 2's argument, and nothing in it changes except the noun and
the disappearance of the version.

### 4. Nothing named "credential" is left

The word goes from the interface, from the wire, from the stores and from the type names.
Not renamed and kept — removed. A search of the tree for `credential` finds the
`credential.Secret`/`credential.SecretStore` boundary of ADR-0011 §2 and nothing else; that
boundary is about secret **material** and is untouched by this ADR. The rule is the
repository's own: no compatibility shims, no dead code, delete it.

## Consequences

- **The credential aggregate is deleted**: `profile.Credential`, `CredentialVersion`,
  `CredentialPatch`, the metadata repository, `internal/rollout`, `ws_rollout.go`,
  `ResolveWithVersion` and `RolloutPanel`. The `credential.Secret` / `credential.SecretStore`
  boundary stays exactly as it is — ADR-0011 §2 is untouched, and a secret value still never
  reaches the renderer.
- **`credentials.*` and `rollout.*` RPCs go.** `savePassword`, `saveKeyMaterial`,
  `saveKeyPassphrase` become operations that mint a secret and hand back the row handle the
  editor then names; each gets a contract schema in `contracts/` as it is touched. A method
  that disappears is a wire change like any other.
- **Profiles carry `secretId` where they carried `credentialId`.** Group inheritance is
  unchanged; it inherits a different field.
- **The "N connections" count becomes answerable directly** — it is the number of profiles
  whose effective secret is this one. `nocx-8pct` and `nocx-cx03` are both symptoms of
  counting through an owner that may not exist, and both dissolve here rather than being
  fixed twice.
- **Stored data is not a consideration.** There are no users, no shipped release and no
  connections worth keeping. The document shape changes, documents in the old shape are not
  read, and nothing converts them — no fallback, no field kept "just in case". A conversion
  would also mean keeping the credential aggregate alive long enough to read the old shape,
  which is the thing being deleted.
- **The renderer gets simpler in a way worth naming.** `AuthenticationEditor` currently
  juggles a credential id, a credential draft, a usage count and an inline-creation path
  that mints a credential the user never asked for. Most of that exists to keep an invisible
  object consistent.

## Alternatives considered

**Keep credentials and lazily attach one to an ownerless secret.** The editor lists secrets;
picking one that has no credential mints a credential behind the scenes. This fixes the
reported bug in a day and preserves every line of the rollout machinery.

Rejected. It keeps two objects for one concept, which is precisely the drift this ADR exists
to stop, and every future surface pays the tax of asking which one it means. It was worth
considering as a staging step while §2 looked expensive; once versions are deleted rather
than moved, the expensive part is gone and there is nothing left to stage.

**Do nothing; teach the Secrets page not to create ownerless secrets.** This is the
consistent version of ADR-0006, and it means reverting ADR-0016's central promise: a secret
could not exist before its connection. The user asked for the opposite six days ago and was
right — storing a key before wiring it up is an ordinary thing to want.

## Not decided here

- Whether a future need for staged rotation is met by the store rather than by us. If one
  arrives, the place to look is the store's own versioning — HashiCorp Vault has it — not a
  second scheme above it. That is a reversal of this ADR and needs its own.
- Whether a secret may be restricted to particular endpoints as a user-set property. Today
  authorisation is computed, never declared, and this ADR keeps it that way.
- The Secrets page's own surface (delete, kind badges, usage counts) — `nocx-pf8b`,
  `nocx-mg9r`, `nocx-8pct` are in flight and land against the current model.
