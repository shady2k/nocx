# ADR-0031 — Vault reset counts and clears every secret-reference holder

- **Status:** Accepted
- **Date:** 2026-08-14
- **Related:** ADR-0011 §4 (metadata-first deletion), ADR-0030 (an AI
  endpoint references a secret it owns), design spec
  `.internal/specs/2026-08-13-ai-assistant-surface-design.md` §4.5.5,
  bead `nocx-kn9q`
- **Extends:** the `internal/vaultreset` bookkeeping. Endpoints
  (ADR-0030) are a second record kind that holds secret references; this
  records how the reset sees them, and fixes the naming that was about to
  lie.

## Context

`vaultreset` takes exactly one `SecretReferenceStore` — its own comment
calls it "the profile store seen from here" — and its `Impact` carries a
`ProfileCount`. The two bulk operations (`CountSecretReferences`,
`ClearAllSecretReferences`) and the per-secret `ClearSecretRefs` are
implemented on the profile store by scanning profiles and group defaults.

ADR-0030 makes AI endpoints a record kind in the same store, holding a
secret reference of their own. With the reset left as it was, two defects
follow:

- The **preview under-reports** what the user is about to lose: endpoint
  references are not counted, so the confirmation dialog says "N secrets, M
  connections" while an endpoint key is about to be destroyed with them.
- **`ClearAllSecretReferences` leaves endpoint records pointing at a vault
  that has gone** — the exact state the reset exists to prevent, and the
  state `secretrefs.go`'s atomicity invariant was written to make
  impossible.

The naming is a third, latent defect: the moment the same `Impact` counts
anything besides profiles, `ProfileCount` lies about what it holds.

## Decision

**Endpoints live in the same store and document as profiles (ADR-0030),
and the reset's impact is counted per record kind.**

### 1. One store, one atomic write

The reset's clear is one document write over profiles and endpoints (group
defaults: see the named gap in Consequences). A second store for endpoints
was rejected: `secretrefs.go`
exists precisely because the sweep "has to be one atomic write, or an
interruption leaves half the store pointing at a vault that has gone", and
`ClearSecretRefs` (the metadata-first half of deleting a secret, ADR-0011
§4) has the same requirement — a secret being deleted must have every
reference cleared in one write, or a crash between two documents leaves one
of them naming a secret that no longer exists. One document keeps both
operations one write. The choice of store is therefore not a matter of
taste but of the atomicity the existing code already demands; it is made in
ADR-0030 and enforced here.

### 2. Per-kind counts; the naming stops lying

`profile.SecretReferenceImpact` gains `EndpointCount` beside `ProfileCount`,
and the reset's own `Impact` mirrors it. The wire's `vault.resetPreview`
gains a required `endpointCount`; the renderer's generated type follows the
schema.

The counts stay separate on purpose — the original design counted secrets
and profiles separately because "collapsing them into one number would make
the sentence shorter and wrong": "12 secrets" is what is destroyed, "9
connections" is what behaves differently afterwards. "2 endpoints" is a
third sentence because it is a third question. A single `RecordCount`
would repeat the original mistake with the second record kind.

`ProfileCount` keeps its name because it still counts only profiles; the
lie it was about to tell — counting more than profiles — is now impossible,
because endpoints have their own field. The interface comment stops calling
the store "the profile store": it is the config store, holding profiles,
groups and endpoints.

## Consequences

- `impactOf` and `ClearAllSecretReferences` in
  `internal/profile/secretrefs.go` scan endpoints alongside profiles; an
  endpoint reference counts once per distinct secret, exactly like a
  profile's. `ClearSecretRefs` — the per-secret metadata-first path — now
  also clears endpoint references (it already cleared group defaults).
- **Group defaults are a known pre-existing gap, deliberately left as
  found.** A secret reference stored in a group default is invisible to
  `impactOf` and survives `ClearAllSecretReferences` today; fixing it
  means redefining `ProfileCount` as the effective computation (a profile
  inheriting a group's password loses it too), which changes the wire
  meaning of `profileCount`. That is a reset-semantics decision of its
  own, not an endpoint one; it is named in design §4.5.6 and this ADR
  records that it was considered and deferred, so it is a gap, not a
  surprise.
- `vaultreset.Impact` gains `EndpointCount`; `Preview` and `Execute`
  report it; the `SecretReferenceStore` comment names the store honestly.
- `contracts/vault.resetPreview.schema.json` gains `endpointCount`
  (required, `minimum: 0`); the Go wire struct and the generated renderer
  type follow.
- The reset dialog's sentence gains the endpoint clause when the surface
  that renders the preview is touched (the form pass); until then the wire
  carries the number and the existing text is not wrong about connections.

## Alternatives considered

**Generalise the interface over several stores** — `vaultreset` takes a
slice of `SecretReferenceStore`s, one per record kind. Rejected: it
changes the reset's atomicity model for the worse. Two stores mean two
files and two writes for what is one logical clear; an interruption between
them leaves one kind cleared and the other pointing at a vault that is
about to be destroyed, which is the exact state the one-write invariant
exists to prevent. Aggregating behind one store keeps the interface
one-valued and the write atomic.

**Collapse the counts into one "records" number.** Rejected in §2: it
repeats the mistake the original comment names.

## Not decided here

- Whether the reset preview's rendering uses the new `endpointCount` — the
  form pass owns the surface; this ADR guarantees the number exists and is
  true.
- Whether future record kinds (a second model catalogue, a provider
  catalogue) join the same document or force the generalisation rejected
  above. If one arrives, this ADR is the place that says whether the
  one-write argument still holds.
