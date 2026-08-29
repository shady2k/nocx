# ADR-0051 — An imported credential stays the person's

- **Status:** Accepted
- **Date:** 2026-08-29
- **Related:** ADR-0011 (secrets are backend-owned; what travels is a reference),
  ADR-0042 (a collection file names a secret by handle), ADR-0047 (a program may ask; it
  never chooses), `.internal/specs/2026-08-21-api-testing-design.md` §§6.3, 8,
  `.internal/specs/2026-08-25-a-secret-in-any-field-design.md` §10, `nocx-14exx`,
  `nocx-flidy`, `nocx-jjnch`, `nocx-zn386`
- **Reverses:** the half of `nocx-jjnch` that made the importer DESTROY credential
  material. The half that refuses a `{{secret:…}}` naming this machine's vault stands
  unchanged.

## Context

Importing a real Postman workspace produced 93 notes reading "imported credential
material is not carried; supply the value after import" — one per bearer token, basic
password, apikey value and credential-shaped header the export held. The values were not
moved anywhere. They were dropped, and the person retyped them by hand.

Two rules had been welded into one, and only one of them was ever decided.

The decided one is the owner's line in the secret-in-any-field design's §10: **a Postman
document never yields a secret REFERENCE.** A file that arrived from anywhere may not
address a record in this machine's vault. That is the attack ADR-0042 concedes it
reintroduces the spelling for, and `dropSecretReferences` is what answers it.

The one actually destroying values was §8's older rule — a collection file may never
spell a token — which the product had already stopped believing. `nocx-14exx` decided
that a credential the person pasted stays where they put it; `nocx-flidy` then rewrote
the collections panel's promise to say so, ending its criteria with "Nothing here
rewrites, refuses or sanitises a header." A curl line's `Authorization` header is saved
into the request file in full. The Postman importer went on refusing to write the very
thing the save path writes, so "may a credential be in a request file" had two answers,
decided by which door the person came in through.

The destination §6.3 had promised — the value goes to the vault, the file gets a
reference — was removed by `nocx-jjnch` along with `apibind`, `secretOffer` and the
`BindWriter` seam. What was left was half a rule: taken out of the file, and nowhere to
put.

## Decision

**A credential an import finds is carried, exactly as the same credential is carried
when a curl-imported request is saved.** Auth blocks, credential-shaped headers and
ordinary variables all arrive with the values the export held.

**A variable the export marked `type: secret` is the one case that asks.** The import
offers to store those values in the vault and write `{{secret:secrow:…}}` in their place.
It does not decide: unticked — the state a fresh ask opens in — the values are written
like every other value.

**`{{secret:…}}` arriving inside an imported document is still dropped and still
reported.** Nothing about the owner's rule changes.

**The mint belongs to whoever holds the vault gate, and `internal/apiimport` still has no
path reaching a vault write.** It answers WHICH variables were marked, and takes back the
references; `internal/capability` mints them through the vault seam the environment editor
and the Auth tab already use. What `nocx-jjnch` deleted does not come back where it was.

**The record and the collection arrive together or not at all.** A record exists from
before the collection is written until either the write arrives or the record is deleted:
a mint for an import that then fails is rolled back, because a vault entry for a
collection that is not on disk is one nobody would know to look for.

## Rationale

### "Removing the value removes the risk"

It removes the value, which is not the same thing. The person's export still holds every
token; what the import achieved was to make them type all 93 in again, one request at a
time, into a product that already had them. Meanwhile the same credential entering
through the curl door was written to disk untouched — so the risk was not removed, it was
made to depend on which door was used.

### "A collection folder must be safe to commit"

It is safe to commit by construction only for the values a variable BINDS, and that is
what the panel now says (`nocx-flidy`). A person may put a token in a header; the product
tells them the folder then holds one. Making the import the single surface that still
enforced the abandoned rule did not restore the guarantee — it only made one door
lossy.

### "Then offer the vault for everything, not only `type: secret`"

The owner's call, and it is the smaller ask that survives contact with a real export.
Postman marks few variables secret — nought of six in the workspace that bought this —
so the offer draws nothing at all for an ordinary import, and a person who wants a header
in the vault reaches the same picker every other field has. An offer over 93 items would
be a second, bulk door to an act that already has one.

### "Default the offer to ON, since Postman said it is a secret"

ADR-0047: a program may ask, never choose. A preselected box that mints vault records the
moment somebody presses Import is choosing, and the choice is not free — it creates
records the person did not ask for and must now manage. The offer is drawn only where
there is something to offer, and it starts unticked.

## Consequences

- The import operation now holds the vault gate as well as the api gate. It is a
  concurrency gate, so an import that stores nothing simply serialises against other
  vault work; a gate that depended on a parameter would be a second answer to "what does
  this operation touch".
- `api.import.postman` gains `storeSecrets`, and its preview answers for every entrance
  that can be read without a network call. A URL is not previewed: reading one means
  fetching it, and a surface that calls somebody's server because a field lost focus is
  doing something nobody asked for.
- The preview carries the NAMES of the marked variables and never their values. The
  renderer can make the offer without ever holding a credential.
- The design documents that describe the old rule are corrected in the same change:
  §6.3's binding document no longer exists, and §10's three bullets are no longer read as
  one owner decision.
