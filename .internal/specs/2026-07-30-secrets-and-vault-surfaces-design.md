# Secrets and Vault: what the user manages, and what the layer remembers

- **Date:** 2026-07-30
- **Status:** Draft for review
- **Brainstorming bead:** nocx-aizr
- **Supersedes nothing.** Amends the surface half of
  `.internal/specs/2026-07-30-vault-storage-backend-design.md`; its storage half stands
  except where §4 below says otherwise.
- **Binding decisions this design does NOT touch:** ADR-0011 §2 and vault design §3.1 —
  no plaintext crosses to the renderer, no Reveal, no Copy of a stored secret.

---

## 1. What is wrong today

A user opened Settings → Vault and said, in these words: _"How do I even see what is in my
vault? The set of settings is very strange. 'Vault has not been set up' — why? What do I do
to make it work? As a user I don't understand the point of this page at all."_ He also said
nothing in the product explains what a **Credential** is or why it exists.

Three findings, each verified in the tree.

### 1.1 The navigation is a projection of the module map

`Connections`, `Credentials` and `Vault` sit in the settings rail as peers. They are
`internal/profile`, `internal/credential` and `internal/vault` — the Go package layout,
rendered as a menu. Module boundaries are chosen to isolate change; they are not user
tasks. The three are not peers: a connection is the thing the user wants, a credential is a
reusable part of it, and the vault is where the bytes live under both.

### 1.2 The layer cannot account for what it holds

`internal/vault/provider.go:16-28` — `Provider` is `ID/Status/Get`, `WritableProvider` adds
`Put/Delete`. There is no `List`, and `zalando/go-keyring` v0.2.8 has none either, so the
`system` provider cannot enumerate at any level. The journal
(`internal/vault/journal.go`) is transient: `prepared → secret-written →
metadata-repointed`, reconciled at startup and then gone. Nothing durable remembers that a
secret was ever allocated.

So the product can answer _metadata → bytes_ (a credential holds a reference; go fetch it)
and cannot answer _bytes → metadata_ (what do we hold?). That is not a missing screen. It is
missing control-plane state, and it is why orphan collection is "best-effort by design and
has no janitor" (nocx-dm0) and why a machine migration cannot produce a preflight list.

### 1.3 The surfaces announce problems and explain themselves only when empty

- `vault.tsx:1000` — the page's dialog signal is `'passphrase' | 'recovery'`. `SetupDialog`
  exists in the same file at line 387 and nothing on this page opens it. Setup happens only
  as a side effect of saving a secret elsewhere. The page says "not set up" three times and
  offers no remedy (nocx-25k9.12).
- `credentials.tsx:281` — the only definition of a credential lives in the `EmptyState`. It
  disappears permanently once the user creates one.
- `connections.tsx:1103-1105` — typing a password into a connection silently creates a
  credential. The user did not ask for an "account"; he typed a password. Then he finds a
  `Credentials` section holding something he never knowingly made. **The noun is unexplained
  because it appears unbidden.**

---

## 2. The model

Three concepts, named for what they are to the user rather than to the compiler.

|                | what it is                                                                                                           | where it lives                                                    |
| -------------- | -------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| **Connection** | where and how to reach a host: address, user, port, which material to use, path to a key file the user keeps himself | profile store, plain JSON, readable always                        |
| **Secret**     | one piece of secret material: a password, a key passphrase, a private key, a public key, an OTP seed                 | the vault                                                         |
| **Card**       | a named bundle of material that travels together, reusable across connections                                        | profile store (its non-secret fields) + references into the vault |

Two rules fall out of this and both matter.

**A path is not a secret.** `credential.go:25` and `profile.go:243` both carry `KeyPath`, and
nothing anywhere holds key bytes. So today we protect the passphrase inside the vault and
leave the key it unlocks as a file we merely know the path of — we guard the key to the door
and not the door. The path is configuration and belongs to the connection; the key material,
when the user chooses to hand it over, belongs to the vault.

**Reading `~/.ssh` continues.** Importing a key into the vault is offered, never required. A
user who wants his key usable by plain `ssh` and `ssh-agent` keeps it as a file and gives us
the path. This is additive: nocx does not stop being a reader of the system's SSH material.
The precedent is already in the tree — `~/.ssh/config` hosts appear in the picker beside
nocx's own connections, with `ssh -G` as the oracle (ADR-0015).

**The unit of storage is a value; the unit of management is a card.** Each value has its own
provider and its own rotation lifecycle, so the catalogue is per-value. The user edits one
card that holds several values — the same shape a password manager uses.

**A card's non-secret fields stay out of the vault.** The username is not a secret and it is
already on disk in plain JSON. Putting it inside would buy nothing and would make the card
unidentifiable while sealed.

---

## 3. Binding, and the one data-model change

ADR-0006 exists so that one login can be reused by many connections; that value is kept.
A card is therefore **not** bound to a host. What binds is the set of connections that
reference it:

- a shared domain password on twelve hosts → one card, twelve references;
- an OTP seed for one host → a card referenced by one connection.

No new concept is needed for host-specific material. **But one thing must change:**
`profile.go:71` gives a connection a single `CredentialID`. If the password is shared and the
OTP seed is per-host, one link is not enough. **A connection references a set of material,
not one card.** A single reference becomes the one-element case, so the migration is
mechanical.

The alternative — duplicating a card per host so it can carry that host's OTP — destroys
exactly the reuse ADR-0006 was written for, and is rejected.

### 3.1 Why the owning record has to exist at all

The obvious simplification is to delete it: put the secret reference straight on the
connection, and let twelve connections carry the same reference string. It was asked, and it
is worth writing down why it does not work, because the record is otherwise easy to mistake
for an extra file somebody chose.

Note first that the simplification is not hypothetical — it is what nocx did originally.
ADR-0006's Context: "The initial connection manager UI stored authentication settings
(passwords, private keys) inline within each SSH profile. This led to duplication: if a user
had the same credentials for 10 servers, they had to enter the password 10 times."

Three things stop it here, and the first is decisive:

1. **The renderer may not name a secret.** ADR-0011 §2 makes references backend-owned; they
   are stripped from every list response (`ws.go:1354-1363`) and rejected on every request
   (`ws.go:1371-1375`), and nocx-jb20.1 is a P0 for the one path that leaks them. So the
   shared thing needs an identifier the renderer is _allowed_ to hold. `sec:v1:…` is not one.
   The record's id is.
2. **Rotation mints a new `SecretID`** rather than overwriting material (ADR-0011). Sharing by
   pointer therefore turns a rotation into an N-place write across a store with **no
   multi-document transaction** — a failure partway leaves some connections on the new secret
   and some on the old, silently. With a record it is one field on one document.
3. **A key and its passphrase have to be known to belong together**, and the vault cannot say
   so: it stores values one at a time and, on the OS keychain, cannot even enumerate them.

What follows from this is not that the record is a user-facing concept. It is the opposite:
the record is the **stable, nameable address of a shared secret**, and the user should meet it
only as the row in Secrets that its material produces (§7). Today it is met as a settings
section called "Credentials" that the user never knowingly populated, which is the defect
§1.3 measures.

The inline path is not deleted either. `profile.go:65-72` already lets a connection carry its
own `user` / `auth` / `keyPath` when it names no record — right for a one-off host, wrong for
a fleet. Both stay.

---

## 4. The vault keeps a registry, because the providers cannot

The vault is a layer over heterogeneous providers. Its purpose is to give one answer over
stores with different capabilities. Of the three providers in view, two can enumerate — the
`file` provider holds its whole blob, and an external HashiCorp Vault has a list API — and
one cannot, ever: the OS keychain. **When a provider cannot enumerate, the layer above must
remember, because there is nobody else.** Otherwise this is not a vault, it is a routing
function wearing the name.

### 4.1 What the registry holds

```
SecretID          sec:v1:system:9f0c…
kind              password | key-passphrase | private-key | public-key | otp-seed
provider          system | file | <external>
state             allocated | superseded | pending-delete
created / verified
owner             an opaque token naming the card that owns it
```

No secret value. No storage locator — the provider derives its own key from
the `SecretID` (vault design §4.1). ~~No display name.~~ **Superseded by
ADR-0016: the secret owns its name.** The registry holds `{name, kind}` per
`SecretID`, written in the same journal sequence as the secret itself — never
by a second, independent path — and the surfaces read it from there. The
derived label survives only as the fallback for a secret whose name did not
land.

### 4.2 This is not the catalogue that peer review killed

Vault design §4.1 rejected a catalogue mapping `SecretID → (provider, locator, name, kind,
timestamps)` on three counts. Taken in turn:

- _"name and kind would have two persisted owners"_ — **superseded by ADR-0016.** The
  objection ruled out the arrangement where both the credential and the vault persist a
  name; choosing the secret as the single owner keeps it a one-owner arrangement. The
  registry holds `name` and `kind`, and the vault is the only owner of both: the kind
  describes the material, and the material is the vault's.
- _"a second document breaks the all-or-nothing metadata guarantee"_ — stands, and is the
  real cost. See §4.3.
- _"it introduces a third crash gap in every write"_ — likely avoidable. See §4.3.

What §4.1 actually settled is that the reference stays opaque and backend-minted, that
parsing is private to `internal/vault`, and that `SecretID` is not promoted into a durable
user-facing identity. **All of that stands unchanged.** §4.1 never asked whether the vault
must remember what it allocated; that question is answered here for the first time.

### 4.3 Cost, and the cheap way to pay it

Today the journal writes `PhasePrepared` **before** the provider call and clears the record
on commit. If commit instead transitioned the record to `allocated` and left it, the registry
would cost no new write in the hot path: the same number of writes, one fewer deletion.
**Whether the journal document can carry this without breaking `Reconcile` must be verified
against `internal/vault/journal.go` before the plan is written** — it is the difference
between a cheap change and a new document with its own crash gap.

### 4.4 What the registry is not

It records what nocx believes it allocated, not what a store actually contains. Since the
keychain cannot be listed, verification is per-entry: walk the registry and `Get` each id.
That catches material deleted behind our back. It can never find a secret we wrote and then
lost the registry record for — so the window of undiscoverable orphans narrows from "any
interrupted write" to "a lost registry write, which happens first", but does not close.

---

## 5. Sealing

Two independent axes. Conflating them is what produced the impossible advice on the current
page.

**Sealing is ours, and it is global.** The registry is sealed or it is not. Sealed, it
answers nothing — not a `Get`, not a write, **and not a list**. How it would have talked to
its providers is not part of the question.

**Reachability is per-provider.** The login keychain is locked, an external token expired,
the network is down. None of that seals or unseals our registry; it means one store is not
answering right now.

This is why `internal/vault/file/file.go:62-69` is a layering defect and not a wording one:
it returns `Reason="locked"` whenever its data key is nil — i.e. whenever _our_ vault is
sealed — and the UI maps `locked` to "Your login keychain is locked", which the file provider
does not have. `provider.go:9-10` states the contract the implementation breaks: a provider
"knows nothing about sealing, routing or policy". Fixing this (nocx-25k9.13) is a
**prerequisite** for a third provider, not tidying.

Consequence, accepted deliberately: **on a sealed vault the Secrets page shows a locked state
and one action, with no counts and no rows.** The alternative — a list derived from profile
metadata — was considered and rejected: it would show through a lock the user just engaged.

What still works while sealed: the Vault settings page (it configures, it does not query the
registry), and the Connections and Cards lists, because _what we reference_ lives in the
profile store as plain JSON while _what we hold_ lives in the registry. That split is worth
stating on its own: **the profile knows what we point at; the registry knows what we keep.**

---

## 6. Providers, and configuring one

References stay exactly as they are: `sec:v1:<provider>:<32 hex>`, minted by the backend,
opaque, with the provider tag naming the store. A third provider adds a tag value and nothing
else to the grammar.

An external store is configured by **address, authentication and a base path**. Below that
path nocx builds its own structure, the same way it does inside the encrypted file. The user
supplies a prefix, not a per-secret path.

This distinction is load-bearing for security. A provider's base path is **configuration**
and legitimately comes from the user. A `SecretID` is **backend-minted** and must never
arrive from the renderer — which is what nocx-jb20.1 (P0, open) exists to enforce on the
import path. The two never meet, and no part of this design weakens that bead.

Our two current providers have nothing to configure, which is why the Vault page reads as
empty bureaucracy today. With an external store there is real work there: address, auth
method, base path, a connection test, and where new secrets go.

---

## 7. The surfaces

### Connections

Structurally unchanged. Each row states its authentication plainly rather than by implication
— `deploy@production · password saved`. The path to a key file the user keeps himself is
configuration and lives here.

### Secrets (today's "Credentials")

The contents of the vault, **one row per stored value**. Not a list of logins, and not a list
of bundles. Renamed because the material is no longer only logins: a private key and an OTP
seed are not credentials.

- **Sealed:** a locked state and one action. Nothing else.
- **Unsealed:** a row per secret — what it is, which store holds it, whether that store is
  answering, and what uses it.

```
🔑  SSH password for deploy · 12 connections     system keychain
🔑  SSH password for shady@vm-dsm01:22           system keychain
🗝   Private key id_ed25519                       encrypted nocx storage
🔑   Passphrase for key SHA256:bdc73f37…          system keychain
```

**The label is derived, never invented.** Tabby does this and it is why its vault page reads
(`tabby-core/src/services/vault.service.ts:27-31`,
`tabby-settings/src/components/vaultSettingsTab.component.ts:87-99`): a secret there is
`{type, key, value}` where `key` carries the meaning — `{user, host, port}` for
`ssh:password`, `{hash}` for `ssh:key-passphrase` — and the label is a format string over it.

**We cannot copy that, and the reason is the identity model.** Tabby's secret is
content-addressed: its identity _is_ its meaning. That buys a free label and costs sharing —
a password for host A and host B are two entries by construction, and `keyMatches` overwrites
on save. Ours is a minted opaque `sec:v1:<provider>:<32 hex>` with no meaning in it
deliberately (vault design §4.1), which buys sharing and fresh-id rotation and costs the free
label.

**Superseded by ADR-0016: the secret owns its name.** The vault persists a display
name per `SecretID` and the surface reads it from there; the owner-derived label is
now only the fallback for a secret whose name did not land, and "12 connections"
is gone — a shared secret has one name like any other. Where a name is still missing
and no owner exists, `kind` carries the row as an explicit registry field, so a new
kind does not degrade into "unknown".

**The credential record does not appear on this page, or anywhere else the user looks.** It
survives as plumbing for the three reasons §3 gives — the renderer may not name a secret, so
the shared thing needs an id the renderer may hold; rotation mints a new `SecretID`, so
sharing by pointer would be an N-place write in a store with no multi-document transaction;
and something must say that a key and its passphrase belong together. What it stops being is
a **noun the user has to understand**. Today it is a surrogate: created silently at
`connections.tsx:1103-1105` because there is nowhere else to put a secret reference, then
displayed as a section called "Credentials" that the user never knowingly populated.

**Say it when it happens.** When typing a password into a connection creates a stored secret,
the surface says what was stored and where — at that moment, not in a settings page the user
may never open.

**Adding material is an action, not a mode.** `SegmentedControl` means _one of these_ — right
for how a key is supplied (path / choose a file / paste) and for a connection's auth method,
wrong for what a secret set contains, which is additive. Add opens a Dialog; the kind is
chosen inside it, where the choice really is exclusive. No new kit component is needed:
`Dialog`, `SegmentedControl` and `FileInput` all exist.

### Vault

Where material is stored and how it is protected. Not a list of contents. It works while
sealed: it configures, it does not query the registry.

**State, and one action for it.** Uninitialized → **Set up protection** (nocx-25k9.12; a page
that announces a problem offers its remedy). Sealed → **Unlock**. Unsealed → **Lock now**.

**Where it is stored.** A **vertical section rail** — the kit's `Tabs` at
`orientation="vertical"`, already used by the connection and group editors
(`connections.tsx:831,1562`) — one row per store, that store's panel beside it. `Tabs`
measures rather than names its size: every section renders into one grid cell, so the box is
the largest panel and switching stores cannot move a control out from under the pointer.

**The rail is only correct if every row carries its store's state.** Without that it has the
defect that ruled out plain tabs: standing on the external store while the login keychain is
the thing not answering tells the user nothing. A stacked list of expanded groups was the
alternative — it shows every state, and grows unusable past three or four stores. The rail
shows the same states in less space.

That needs **one addition to the kit**: `TabItem` is `{id, label, content}` today and has
nowhere to put a marker. Per `ui/README.md` this is a typed variance added to `Tabs`, never a
fork. The marker is semantic rather than decorative — it carries a tone (ok / warning /
error) and an accessible name, or a screen-reader user is told "System keychain" with no hint
that it is broken.

**Add a store** is a button below the rail, not a row inside it: a rail row is a section, and
an action rendered as one makes the rail lie about what it enumerates.

Each store's panel carries:

- name and kind — _System keychain_, _Encrypted nocx storage_, _HashiCorp Vault ·
  `vault.homelab:8200`_;
- **its state, always visible**, in words with a remedy attached — "Not answering: your login
  keychain is locked. Unlock it and try again" — never a reason code;
- **Test** — on every store, not only external ones. The probe already exists and is
  specified to be re-runnable: it writes a random id, reads it back, compares bytes and
  deletes best-effort, and its result is cached for the process lifetime _with an explicit
  re-probe action_ (vault design §5.7). The button is that action;
- for an external store: address, authentication, base path, **Edit**;
- **Store new secrets here** — one marker across all stores;
- **how much it holds** — "6 secrets", from the registry. Disconnecting a store must say what
  it strands, and without the registry that warning cannot be written at all (§4);
- when the last check ran, and what the store actually answered.

What may be collapsed is the _detail_ of a state, never the state itself. The current page
puts `Writable`/`Not ready` badges in the body with no action attached to either, which is
the opposite arrangement.

**Protection.** Lock now; lock automatically after Never / 5 / 15 / 30 / 60 minutes; change
the master passphrase; reissue the recovery code. The last two appear only when a passphrase
actually protects something (vault design §5.2).

**Rotating a factor re-authenticates, and this is already true end to end — keep it.**
`vault.go:742-746` refuses `ChangePassphrase` unless the old passphrase or a recovery code is
supplied, and the OS-held key is explicitly not sufficient: "a factor that only unlocks must
not be able to replace the factor that recovers. Unsealing is temporary; rotating is not."
`RegenerateRequest` requires the passphrase the same way, and `vault.tsx:729,733` already
collects one and refuses to submit without it. A rebuilt surface must keep asking; the
backend refuses regardless, so the failure mode of forgetting is a dead end rather than a
hole, but the prompt is part of the design and is written down here so it is not "simplified"
away.

Unlock stays where the user is blocked. It is already built and is not moved:
`main.tsx:335-364` mounts the setup and unlock dialogs above every page, and
`terminal-content.ts:622-629` turns a `vault-sealed` error into that dialog.

### Vocabulary

The interface stops speaking implementation. `seal` → _lock_; `provider` → _where it is
stored_; `system` → _system keychain_; `file` → _encrypted nocx storage_; `uninitialized` →
_protection is not set up yet_.

---

## 8. Deliberately out of scope

- **Reveal or Copy of a stored value.** Settled, and not reopened.
- **Making the registry authoritative about provider contents.** It cannot be; §4.4.
- **Orphan collection** (nocx-dm0) and **machine migration**. The registry is what makes both
  possible; neither is designed here.
- **Importing existing `~/.ssh` keys as bytes** is designed here as a supported kind, but the
  migration of users who already have key files is its own piece of work.

---

## 9. Open, and to be settled before the plan

1. **Can the journal document carry the registry** without a second document and without
   breaking `Reconcile` (§4.3)? This decides whether the registry is cheap or expensive.
2. **Does an imported private key ever touch disk?** Recommendation: no — it lives in backend
   memory for the duration of a connection, the same contract passwords already have. A user
   who needs the key for external `ssh` keeps it as a file and gives us the path.
3. **How much of the kind vocabulary ships first.** The format must carry `kind` from day
   one; only `password` and `key-passphrase` exist in the tree today
   (`credential.go:57-60`).
4. **Whether "card" is the shipped word.** It is used here to avoid overloading "credential"
   while the rename is undecided.

---

## 10. What this design costs the existing decisions

| decision                                                             | effect                                                                                  |
| -------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| ADR-0011 §2 / vault §3.1 — no plaintext to the renderer              | unchanged                                                                               |
| vault §4.1 — opaque minted references, no user-facing entry identity | unchanged; the registry holds no name and no locator                                    |
| vault §4.1 — no catalogue                                            | **amended**: the vault keeps a registry of its own allocations, for the reason §4 gives |
| ADR-0006 — reusable credentials                                      | unchanged in value; a connection gains a set of references instead of one               |
| vault §5.5 — three vault states                                      | unchanged, and sharpened: sealing is ours and global, reachability is per-provider      |
| nocx-jb20.1 (P0)                                                     | unchanged and unweakened; provider config and secret references are different things    |
| nocx-25k9.13                                                         | promoted from a wording defect to a prerequisite                                        |

An ADR is warranted for §4 (the vault keeps a registry) and for §2's key-material decision,
because both are hard to reverse and both amend a settled document.
