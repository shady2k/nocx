# ADR-0016 — A secret owns its name

- **Status:** Accepted
- **Date:** 2026-07-31
- **Related:** ADR-0011 (persistence, storage capabilities and secret references), design
  spec `.internal/specs/2026-07-30-secrets-and-vault-surfaces-design.md` §4 (the registry)
  and §7 (the Secrets surface), `.internal/specs/2026-07-30-vault-storage-backend-design.md`
  §4.1 (the catalogue peer review rejected)
- **Reverses:** the "no display name" clause of the secrets-and-vault design spec §4.1.

## Context

A stored secret has no name of its own. Its identity is `sec:v1:<provider>:<32 hex>` —
opaque and meaningless on purpose, which is what buys sharing between connections and
fresh-id rotation. The label a user sees is therefore **derived from the owner of the
reference**: the credential record knows the login, so the Secrets page reads
"SSH password for deploy" by asking the credential, never the vault. `BuildInventory`
takes credential metadata as an argument precisely because the vault knows nothing
about what its secrets mean.

Two consequences follow, and both have now been met by a user rather than predicted:

- **A shared secret has no name to show.** One password used by twelve connections cannot
  be labelled by any of them, so the surface prints "12 connections" instead. The design
  spec names this as a compromise it accepted.
- **A secret with no owner cannot exist.** Not "is not offered" — cannot be named, so
  cannot be listed, so has nothing to be. Storing a password before the connection that
  will use it is impossible by construction, and the Secrets page has no Add action for
  the same reason.

The design spec forbade a name explicitly (§4.1: "No display name") and defended it in
§4.2 against the peer review that had killed an earlier catalogue: _"name and kind would
have two persisted owners"_.

## Decision

**The secret owns its name.** The vault persists a display name alongside each
`SecretID`, and the surfaces read it from there instead of deriving it from whatever
happens to reference the secret.

The name is filled in two ways, and which one applies is decided by where the secret is
created:

- **Created by saving a password on a connection** — the name is generated from what is
  already known: `root@192.168.0.57`. The user is not asked, because they did not set out
  to create a secret.
- **Created on the Secrets page** — the user is asked, because they did.

## Why the recorded objection does not survive

The objection was **two persisted owners**, not the name itself. Two owners can disagree;
one cannot. The spec resolved the conflict by choosing the credential as owner. This
chooses the secret. Both are single-owner arrangements, and the objection rules out only
the arrangement where both persist a name — which neither proposes.

Choosing the secret is better on three counts:

1. **It names the case the other cannot.** A shared secret has one name under this scheme
   and no name under the other. The compromise the design spec accepted disappears rather
   than being managed.
2. **It makes an unowned secret possible.** That is not a side effect; it is the feature
   the user asked for, and under the old scheme it was not a missing button but an
   unreachable state.
3. **It puts the noun where the user's model already is.** The spec's own goal for the
   credential record was to stop being "a noun the user has to understand". Under this
   decision the credential recedes further — you choose "the prod password", which is a
   thing people already think they own, rather than a "credential", which is not.

## The cost, stated

**A secret and its name are two writes, not one.** This is §4.3's objection and it stands:
a crash between them leaves a secret with no name or a name with no secret. It is not new
machinery, though — `Create` already sequences a pair exactly like it through the journal
(`PhasePrepared` → provider `Put` → commit), and the name joins that sequence. What must
not happen is the name being written by a second, independent path.

**A nameless secret must render.** Whatever the journal guarantees, a secret whose name
did not land is a row the Secrets page still has to draw. It falls back to the derived
label where an owner exists and to the kind otherwise; it never renders blank, and it
never renders the `SecretID`.

**The name is metadata, and metadata is not a secret.** It is written where a reader who
has the file can read it. So it must be treated as user-visible text: no passwords in
names by accident, and the generated form derives from host and user, never from anything
material.

## What this does not change

- The `SecretID` stays opaque and backend-minted. The name is metadata attached to the
  id; it is never part of it, never routes anything, and is never accepted from the
  renderer as an identifier (`nocx-jb20.1`).
- The credential record survives as plumbing for the three reasons ADR-0011 gives. What it
  stops being is something the user creates by hand.
- The vault still stores no secret **values** outside its providers, and still hands none
  back to the renderer (ADR-0011 §2, vault design §3.1).

## Consequences

- The vault gains a persisted `SecretID → {name, kind}` record and becomes the owner of
  both. `BuildInventory` stops taking labels from credential metadata.
- The Secrets page gains Add and Rename, and a secret can exist with no connection using
  it.
- `+ New Credential` leaves the connection editor: selecting an existing credential stays,
  creating one by hand goes.
- The design spec's §4.1 "No display name" line and §7's paragraph on derived labels are
  superseded by this ADR and must be amended to point at it.
