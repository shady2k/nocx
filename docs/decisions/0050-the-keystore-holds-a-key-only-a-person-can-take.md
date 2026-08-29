# ADR-0050 — The OS keystore holds a key only a person can take

- **Status:** Accepted
- **Date:** 2026-08-29
- **Related:** ADR-0011 (persistence, storage capabilities and secret
  references), ADR-0018 §3 (the ContentDB key's homes), ADR-0003
  (distribution without a Developer ID), design D9 (the vault seals when the
  last client leaves) and D10 (the keystore stance is declared, not
  discovered), design spec
  `.internal/specs/2026-07-30-vault-storage-backend-design.md` §T4,
  beads `nocx-e7bhr`, `nocx-xsd03`
- **Amends:** the vault design's threat model. T4 — a hostile process running
  as the same user — stays out of scope for what the vault can DEFEND. What
  changes is that we no longer ship a mode whose only protection was T4 being
  out of scope.

## Context

The backend became a per-user daemon that outlives the window
(`cmd/nocx-server`). The vault moved with it, so where a shipped user's
secrets live stopped being a question about a window and became a question
about a process that is still running after the window is gone.

Two facts collided there, and neither was visible from the task that
produced it. D10 made the keystore stance a property of the BUILD: a binary
compiled without `-tags nocx_login_session` has no OS keystore to reach, by
construction rather than by error handling. And the release workflow builds
`cmd/nocx-server` with `-tags release` and nothing else. So the shipped
coordinator takes the file provider, and `nocx-e7bhr` was filed as a P0 to
add the missing tag.

Before adding it, the protection it would buy was measured on macOS, against
an item written exactly the way `zalando/go-keyring` v0.2.8 writes one —
`security -i` fed `add-generic-password -U -s .. -a .. -w ..`, with no `-T`:

- The item's read ACL entry carries **no trusted-application list**.
- A Swift binary calling `SecItemCopyMatching` **under its own signature** is
  prompted for the user's password.
- `security find-generic-password -s .. -a .. -w`, from an unrelated process,
  prints the secret **silently**. So does delete.

The trusted application is therefore `/usr/bin/security` itself — the
general-purpose CLI that anything can invoke, and the same one nocx reaches
the keychain through. A confused deputy: any process running as the user
reads a vault secret with one shell command, without the passphrase, without
the root key, and without the vault being unsealed. Sealing gates our own
API; it does not gate the bytes.

**This confirmed a documented non-goal rather than uncovering a defect.** The
vault design already names T4 — "a malicious process running as the same
user … invokes `/usr/bin/security` … Neither store stops this" — and places
it explicitly out of scope. What the measurement changed is not whether T4 is
defensible. It is whether we are willing to ship a mode that has no other
protection.

That mode is **silent setup**: `Setup` with an empty passphrase puts the ROOT
KEY itself in the keychain (`internal/vault/vault.go`), and mints neither a
passphrase envelope nor a recovery envelope — the design says so plainly,
"on a machine initialized silently the OS-held copy is the only one present".
So the machine that loses that keychain item has lost the vault, with no
second means, and any process under that login can take the key with one
command in the meantime.

Two further facts bound what may be decided here:

- **The keystore is not the vault's alone.** `internal/contentkey` keeps the
  ContentDB key there, under a DIFFERENT and explicitly narrower threat model
  — the detached copy, not a live attacker — and under two invariants: the
  read never passes a vault seal gate, and history never asks for a
  passphrase on any platform. Deleting the keystore would silently decide
  that too.
- **A stable code identity is available without Apple, and was measured.**
  A self-signed code-signing certificate yields
  `designated => identifier X and certificate leaf = H"…"` — anchored to the
  certificate, not to a `cdhash`, so it survives a rebuild. ADR-0003's
  ad-hoc signature does not: its requirement IS the code hash.

## Decision

**The OS keystore may hold vault key material only where a PERSON, not a
program, is what releases it.**

Concretely, and in this order:

1. **Silent setup is removed.** Every vault gets a passphrase envelope and a
   recovery code. There is no mode in which the root key is obtainable
   without either.
2. **The keychain keeps no verification material.** Not a hash of the
   passphrase — it opens nothing, so it buys no convenience and adds an
   offline verification oracle. Not the argon2id output either: that IS the
   key-encryption key, so storing it recreates silent setup with an extra
   layer. And not the recovery code or its envelope: the recovery code exists
   to survive the loss of the machine, and a copy on that machine defeats the
   only case it is for.
3. **nocx stops reaching the keychain through `/usr/bin/security`.** The
   darwin provider calls Security.framework directly, under our own code
   identity, signed by a project certificate whose designated requirement is
   stable across releases.
4. **The item requires user presence.** Touch ID or the login password, via
   the item's access control — because a signature alone does not close the
   hole. Trust anchored to our binary means "any instance of our binary", and
   our binary is on disk, signed, and designed to open the user's vault: an
   attacker runs it against the user's profile and satisfies the requirement.
   Only a prompt aimed at the human survives that.
5. **`internal/contentkey` keeps its own arrangement, decided separately.**
   It is not a vault secret and its threat model is not the vault's.

## Why this rather than the alternatives

**Rather than adding the missing tag** (`nocx-e7bhr` as filed): it would turn
a shipped configuration that is currently safe by accident into the
vulnerable one. The bead has its sign backwards.

**Rather than removing the system provider outright.** It was the first
conclusion and it was an overreaction. It amends a threat model that was
already decided, it deletes a capability `internal/contentkey` still needs,
and it costs a wire contract and a renderer type to buy a property a narrower
change buys as well.

**Rather than keeping silent setup and documenting it honestly.** Honest
documentation would have to say that the master key is obtainable with one
command and that there is no recovery code. The second half is not a threat
model position; it is data loss.

**The strongest argument against this decision, recorded because it is
real.** A mandatory passphrase creates an offline guessing oracle that the
keychain does not: copy the vault document and guess forever. At this repo's
measured argon2id cost (~100–200 ms per derivation), a million-candidate
space falls in about an hour and a half, a hundred million in about six days;
forty bits of real entropy holds for centuries. Sealing on every window
close (D9) pushes people towards the first two. This is why step 4 exists
rather than "passphrase only, forever": user presence is what returns the
convenience without returning the exposure, and until it ships, D9's
aggressiveness is the dial to argue about — not the passphrase.

## What the next person inherits

`nocx-e7bhr` is closed by this ADR as DECIDED, not as fixed, and is P2 rather
than P0. Steps 3 and 4 are not started: the certificate exists as a
measurement, not as a release identity, and the release workflow still
ad-hoc signs (`codesign --force --deep --sign -`) and builds `nocx-server`
with `CGO_ENABLED=0` — which a direct Security.framework provider makes
impossible, the way the desktop binary already builds with `CGO_ENABLED=1`
and explicit `-arch` flags.

Two things must be measured before step 4 is believed rather than assumed:
that `/usr/bin/security` is refused against an item our provider wrote, and
that the designated requirement survives a rebuild and an update. Neither is
established today.

A certificate anchors the requirement by its own hash, so its expiry is a
release concern: renewing it changes the requirement and every existing
install stops recognising nocx. Issue it long, and treat its private key as a
release secret — whoever holds it can sign a binary the keychain accepts as
ours.
