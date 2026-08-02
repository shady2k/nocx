# Vault: storage backend, key hierarchy and seal lifecycle — design

- **Date:** 2026-07-30
- **Beads:** nocx-nfvd (this brainstorm), nocx-25k9 (epic), nocx-25k9.1 (the wiring bug this answers)
- **Touches:** ADR-0011 (§1 amended — see §3; §2 upheld — see §3.1), ADR-0006, AD-8
- **Status:** approved section by section by the owner, then adversarially reviewed against a peer
  agent (codex) on 2026-07-30. The review overturned four of my positions; §4.1, §4.2, §5.4 and §7.2
  are the result, not the original draft.

This spec covers the whole architecture (V1–V4, §9). Only **V1** is planned and built now.

---

## 1. What is actually true today

Verified against the tree on 2026-07-30, not recalled:

- `internal/app/app.go:90` wires `credential.NewKeychain()`. That instance reaches the settings
  registry, the transport (`:116`) and the SSH profile resolver. **The vault has no caller.**
- `internal/credential/vault.go` is ~370 lines of AES-256-GCM + PBKDF2 reachable only from its own
  tests. `NewVault()` and `NewCredentialStore()` have no non-test callers.
- Two compatibility aliases exist whose own comments explain the bug: `credential.Keychain`
  ("retained for compatibility with the composition root", `keychain.go:3-6`) and
  `credential.CredentialStore` ("exists only to avoid editing app.go in this wave",
  `credential.go:14-21`). The wave ended; `app.go` was never edited.
- `credential.SecretStore` (`secretstore.go:26-31`) is the interface every consumer already uses,
  addressed by an opaque `SecretID` (`:11-14`) that is 16 bytes of `crypto/rand` (`:17-21`).
  Plaintext escapes only through `Secret.Use`.
- **Secrets are minted in four places, in two different domains**, always mint-then-write and always
  with a fresh id: `ws.go:1638`, `ws.go:1727`, `ws_candidate.go:37` and `settings.go:867`
  (`Registry.SecretSet`). §4.2 turns on this.
- `internal/storage` provides the DocumentStore capability — `document.go:16-30` exposes only
  per-document `Read`/`Write`. **There is no multi-document transaction.** `AtomicImport`
  (`internal/profile/service.go:177`) is atomic only because profiles, groups and credentials live
  in one store loaded through `LoadAll()`.
- `internal/importer/tabby.go:57-66` parses Tabby's `vault` section but never decrypts it.
- `frontend/src/connections.tsx:764,1576` already lets a profile and a group default select a
  `credentialId`; `credentials.tsx` and `credential-form.tsx` already provide credential CRUD.
- `internal/transport/ws.go:1371-1375` already rejects a renderer-supplied `secretId` on
  `credentials.create`; `ws.go:2204-2218` already broadcasts `settings.changed` to every client.
- `zalando/go-keyring` v0.2.8 exposes exactly `Set`, `Get`, `Delete`, `DeleteAll`. **No
  enumeration.** §8 and §9 depend on this.
- **Greenfield:** no vault file and no keychain entry from a previous release exists in the field.
  There is nothing to migrate and no format to stay compatible with.

### 1.1 The keychain is not always available

This is the fact the whole design turns on, and it was measured rather than assumed:

- **macOS** — the platform facility is always installed. `go-keyring` v0.2.8 shells out to
  `/usr/bin/security` (`keyring_darwin.go:29,44,86`). "Installed" is not "answers": a locked login
  keychain, an ACL denial or a damaged keychain all fail, which is why availability is a probe
  (§5.7) and not a build tag.
- **Linux** — not guaranteed at all. `keyring_unix.go` needs `org.freedesktop.secrets` on the
  session bus; with no daemon every `Set`/`Get` fails, and `keyring_fallback.go` only returns
  `ErrUnsupportedPlatform`.
- **We ship Linux.** `.github/workflows/release.yml:179-288` builds a `linux/amd64` AppImage
  alongside `darwin/universal` (`:124`).
- **Measured on the primary dev machine, 2026-07-30:** no `org.freedesktop.secrets` on the session
  bus, no keyring daemon. nocx cannot store a single password there today.

### 1.2 "OS authentication" is not authentication

`security find-generic-password` reads an item whose ACL trusts `/usr/bin/security` — the utility
that created it — so the read is silent. Secret Service likewise returns from an unlocked collection
without prompting. Unsealing "through the OS" is therefore _not asking the user anything_; it is a
keychain read. A real prompt (LocalAuthentication/Touch ID on macOS, polkit on Linux) is net-new
cgo/platform work and is deferred (§9).

**Consequence for wording, in the ADR and in the UI:** the OS-held key means "do not ask for a
passphrase", never "the system verified who you are".

---

## 2. Threat model

Everything below is justified against this list. A control that protects nothing here does not ship.

**In scope:**

- **T1 — brief access to an unlocked machine.** A colleague, a conference, a borrowed desk. The
  dominant real-world case for a terminal. Countered by: idle seal, and the fact that a stored secret
  cannot be displayed or copied at all (§3.1) — there is nothing on screen to read.
- **T2 — data at rest.** Stolen or lost disk, a backup, a config directory that ends up in a
  dotfiles repo or a cloud folder. Countered by: AEAD-encrypted blob whose key is not stored beside
  it; the keychain encrypts on its own.
- **T3 — leakage through our own product.** A secret in a log, in a JSON-RPC response, in a config
  export, in scrollback. Historically the only class that has actually fired here — `nocx-jb20.1` is
  open right now. Countered by: the `Secret` type, opaque references, no plaintext in logs, and the
  rule that a reference is never renderer-supplied.

**Explicitly out of scope:**

- **T4 — a malicious process running as the same user while the app runs.** It reads process memory,
  invokes `/usr/bin/security`, or replaces the binary. Neither store stops this.
- **T5 — a compromised OS, root, or cold boot.**
- **T6 — a hostile owner of the machine.** That is the owner of the secrets.

**Four consequences that must be written down, or they will be forgotten:**

1. The encrypted-file provider is **not stronger than the keychain against T4**. Its purpose is T2 on
   platforms with no keychain, plus the choice of a user who does not trust the system store.
2. `sealed` on the keychain provider closes **T1 only** — it is application policy, not a
   cryptographic boundary. On the file provider it closes T1 and T2, because the data key is wiped.
3. While the OS-held key exists it **caps the achievable strength**: the root key is retrievable from
   the keychain, so the master passphrase adds nothing against T2. The passphrase only matters in the
   mode where the OS-held key is absent. That toggle is therefore the single place a user picks a
   security level, and it must be labelled as such.
4. **The only plaintext that ever reaches the renderer is material the user is entering or being
   given once** — a passphrase being typed, a recovery code being generated. Never a stored secret
   read back (§3.1). Where such material offers Copy, the clipboard is cleared on a timer and that
   clearing is **best-effort**: a clipboard manager, a shell integration or another application may
   already have taken a copy. It narrows the window; it does not close it.

**Deliberately not defended:** the user copying a secret into another app; an already-established SSH
session (the secret was spent at authentication and seal cannot recall it); any form of sync
(vision §10, §11).

---

## 3. Decision, and its effect on ADR-0011

nocx gets a **Vault**: one domain entity that owns the routing of secret references to providers, the
seal lifecycle, and the key material that unlocks them. Providers only store and fetch values. Two
providers ship, compiled on every platform:

- **`system`** — the OS store, via `zalando/go-keyring`. Default wherever the probe (§5.7) succeeds.
- **`file`** — the current `credential.Vault`, renamed and moved behind a provider interface. Default
  where the probe fails; selectable by a user who does not want a system store.

**This amends ADR-0011.** Its §1 says, verbatim and with status Accepted
(`docs/decisions/0011-…md:72`):

> **SecretStore** — authenticators only, in the OS keychain. **Never a file we write.**

That clause is superseded: a file we write becomes a legitimate SecretStore backend, because a
shipped platform has no keychain and the alternative is a build on which no secret can be saved at
all. The rest of ADR-0011 — three storage capabilities, secrets as opaque references, backend-only
resolution, cross-store writes as explicit journalled workflows — stands unchanged and is reinforced.

### 3.1 A second ADR-0011 clause is at stake, and it survives: there is no Reveal

The peer review surfaced this and the owner settled it on 2026-07-30. ADR-0011 §2 does not merely
discourage returning plaintext to the renderer:

> The boundary is that the renderer has no API that returns a secret.
>
> Consequently `credentials.lookupPassword` and every sibling RPC that returns plaintext is
> **removed, not fixed**. A secret-class setting generates an editor whose operations are `set`,
> `delete` and `exists` — **never `get`**.

That was written after an observed leak in PR #11. A "Reveal this secret" affordance would reinstate
the exact RPC shape that clause deleted, and once plaintext reaches JavaScript it lives in immutable
strings that cannot be wiped.

**Decision: no Reveal, and no Copy of a stored secret.** A user who has forgotten a stored password
_replaces_ it; they do not read it back. ADR-0011 §2 stands unamended, and ADR-0016 amends ADR-0011
in exactly one place — §1's "never a file we write".

This is a real capability the product does not have, and it is a deliberate trade, not an oversight:
nocx is not a password manager, and the one class of leak that has actually occurred in this repo is
the one this clause prevents.

One further nuance: DocumentStore is justified in ADR-0011 as "human-recoverable configuration… a
user can open these in an editor and repair them". The `file` provider's blob is the one document
that is deliberately **not** human-recoverable. It still uses DocumentStore as the mechanism (atomic
JSON write); the exception is recorded rather than papered over with a new capability.

---

## 4. Domain model and module boundaries

### 4.1 Routing lives in the identifier; there is no catalogue

The first draft of this spec proposed a vault catalogue document mapping
`SecretID → (ProviderID, Locator, name, kind, timestamps)`. Peer review killed it, correctly, on
three counts: name and kind would then have two persisted owners (credential metadata already holds
them); a second document breaks the all-or-nothing metadata guarantee `nocx-y910.1` shipped, because
DocumentStore has no multi-document transaction (§1); and it introduces a third crash gap in every
write.

Instead the provider is encoded in the reference itself:

```
sec:v1:<provider>:<32 hex chars>      e.g. sec:v1:system:9f0c…   sec:v1:file:41ab…
```

- **Parsing is private to `internal/vault`.** No consumer branches on the prefix; a
  `strings.HasPrefix(id, "sec:v1:file:")` anywhere outside the vault turns the identifier into a mode
  flag distributed across the codebase, which is exactly what AD-8 forbids. The parser is strict:
  exact grammar, bounded and canonical-lowercase provider tag, exactly 32 hex characters, no empty
  component, and **no defaulting on failure**.
- **Provider tags are persisted protocol, not implementation names.** They appear inside the profile
  store and cannot be renamed casually. They are declared in the ADR, validated for uniqueness when
  the registry is constructed, and dispatched through the injected registry map — never a `switch`.
  The tag names the _store_; the blob names its own _format_ (`StoredVault.Version`), so a future
  format change adds a reader to the `file` provider rather than stranding every existing reference.
- **`Locator` disappears as a concept.** A provider derives its own storage key from the `SecretID`
  it is handed. Nothing extra is stored, nothing extra can leak, and the earlier worry about a
  plaintext locator is moot: the identifier was already 16 random bytes and already lived in the
  profile store.
- **The binding is immutable; availability is not.** Moving a secret between providers is not a new
  mechanism — it _is_ rotation, on the path ADR-0011 §4 already prescribes and §7.2 already
  journals.

`credential.SecretID` therefore stays exactly what ADR-0011 calls it: a reference to a material
instance, with rotation lifecycle. It is **not** promoted into a durable user-facing entry identity.

### 4.2 The consumer contract: `Create`, not `Set`

All four minting sites (§1) do `newID := credential.NewSecretID()` and then `Set(newID, …)`, always
with a fresh id. If the provider is encoded in the id, the caller can no longer mint it without
knowing the routing policy — and leaking provider selection out to transport and settings would
destroy the boundary this whole design exists to create. So the contract changes:

```go
type SecretStore interface {
    Create(ctx context.Context, value Secret) (SecretID, error)
    Get(ctx context.Context, id SecretID) (Secret, error)
    Delete(ctx context.Context, id SecretID) error
    Exists(ctx context.Context, id SecretID) (bool, error)
}
```

`Vault` implements it. The composition root swaps one constructor; the read side (resolver, ssh) is
untouched; the four writers change from mint-then-write to write-and-receive. `Set` was semantically
misleading anyway, since every existing caller already discards the old id.

A semantic `Resolve(ctx, query, use func(Secret) error)` is **not** introduced. Its `SecretQuery`
(host/port/user/fingerprint) existed only to serve a runtime Tabby provider, which is out (§7).

**On `ctx`, honestly.** It is added for deadline and shutdown propagation, and it is documented as
bounding how long a caller waits — **not** as cancelling the effect. `go-keyring` takes no context;
racing the call in a goroutine returns early but leaks the blocked operation, so a `Put` or `Delete`
may still land after the caller has been told it failed. That is precisely why the journal is
written before delegation (§7.2): a late-completing write must remain discoverable.

### 4.3 Packages

| Package                 | Owns                                                                                                                        |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `internal/vault`        | routing and the `SecretID` parser, provider registry, lifecycle, key material, journal; implements `credential.SecretStore` |
| `internal/vault/system` | provider over `zalando/go-keyring` (moved out of `credential/secretstore.go`)                                               |
| `internal/vault/file`   | provider from today's `credential.Vault`, blob format v1 (§5.3)                                                             |
| `internal/credential`   | reduced to what it should be: `Secret`, `SecretID`, `SecretStore`                                                           |

### 4.4 Capability is an interface, never an enum

AD-8 is explicit: "variation is expressed by the interface, never by a fork inside an implementation…
the test is whether a new implementation can be added without editing a `switch`".

```go
type Provider interface {
    ID() ProviderID
    Status(ctx context.Context) Status
    Get(ctx context.Context, id credential.SecretID) (credential.Secret, error)
}

type WritableProvider interface {
    Provider
    Put(ctx context.Context, id credential.SecretID, s credential.Secret) error
    Delete(ctx context.Context, id credential.SecretID) error
}
```

The backend branches only on a type assertion to `WritableProvider`. The RPC layer projects
capabilities into DTO flags for the UI — that is a **view**, not a dispatch mechanism.

`Status` keeps two values, `ready` and `unavailable`, because a third would be a state nothing in CI
can exercise. Actionability comes from a machine-readable **reason code** carried alongside:
`no-service`, `locked`, `denied`, `timeout`, `unsupported-platform`, `unknown-provider`. The same
discriminator rides on `ErrProviderUnavailable`, so "start a Secret Service", "unlock the login
keychain" and "this reference names a provider this build does not have" are distinguishable actions
without inventing states.

### 4.5 Seal lives in the Vault, and its limit is stated

A provider knows nothing about sealing; `Vault` refuses before delegating. That is why "policy for
the system provider, cryptography for the file provider" is an observable property of the code: on
seal the file provider has a data key to wipe and the system provider has nothing.

Concurrency rules, because "refuses before delegating" is not sufficient on its own:

- **One owner.** A single mutex/actor inside `Vault` serialises lifecycle transitions and provider
  mutations. Without it two concurrent `Create`s against the file provider both read blob version N,
  mutate independently and atomically write N+1 — DocumentStore's atomic replace prevents a torn
  file, not a lost update.
- **Generations.** Sealing increments a generation. A provider result produced by an operation that
  had not completed before the transition is rejected.
- **The honest limit.** Seal prevents new operations and rejects results from operations that had not
  completed before the transition. It **cannot revoke a `Secret` already handed to an in-flight
  consumer** — once bytes are out, the Vault does not own their lifetime. Anything stronger would
  need a lease-bearing `Secret` whose `Use` consults the generation, which is not justified.
- **No keychain call while holding a document lock.** ADR-0011 §4 already mandates this: the keychain
  "can block, prompt, or fail on its own schedule".

### 4.6 Deleted outright

No compatibility shims, no dead code (AGENTS.md). Pleasingly, these are the very shims that caused
`nocx-25k9.1`:

- `credential.Keychain` + `NewKeychain` — its comment names the composition root we are now editing;
- `credential.CredentialStore` — "only to avoid editing app.go in this wave";
- `credential.vaultSecretStore` + `NewCredentialStore` — replaced by the provider;
- `credential.NewSecretID` as an exported mint — id minting moves inside the Vault;
- `StoredVault.IV` ("unused in v2, kept for backward JSON compat") and the legacy-version branch
  (`vault.go:190-198`);
- the false doc comment at `vault.go:88-92` claiming the passphrase lives in a package-level
  variable — it is a struct field (`:95`).

---

## 5. Keys, storage format, lifecycle

### 5.1 One root key, two envelopes and an OS-held copy

A random 32-byte **Vault Root Key** is unlocked by up to three means. Two are envelopes — ciphertext
wrapping the root key, stored in the vault document. The third is not an envelope and calling it one
was imprecise: it is a **copy of the root key held in the OS store**, whose confidentiality is the
keychain's, not ours.

| Means         | Mechanism                                     | Stored in                            |
| ------------- | --------------------------------------------- | ------------------------------------ |
| passphrase    | argon2id(passphrase, salt) → KEK, AES-256-GCM | vault document                       |
| OS-held copy  | a keychain read, no KEK of ours               | the OS store, under a stable account |
| recovery code | argon2id(code, salt), same construction       | vault document                       |

The OS-held copy needs its service/account naming, overwrite behaviour and the rule for
distinguishing a stale entry from another installation specified at implementation time; the vault
document records only _that_ one exists.

Each means exists only once created. On a machine initialized silently (§5.2) the OS-held copy is the
only one present.

**Exactly one recovery code**, not ten. Any code in a set of ten revokes the whole set on use, so ten
is one code printed ten times with ten more places to leak from. It is shown once with Copy/Download;
using it revokes and reissues.

**Only the `file` provider has a data key** — random, wrapped by the root key. The `system` provider
stores the value itself and has none. The benefits of the root-key indirection (change the passphrase
without re-encrypting; several ways to open one key) therefore apply to the file path only, and the
ADR says so.

**KDF: argon2id**, m=64 MiB, t=3, p=4, 16-byte salt, 32-byte output. `golang.org/x/crypto/argon2` is
in the already-direct dependency `x/crypto v0.54.0`. Parameters are stored beside the envelope and
bound as AAD, so they can be raised later without invalidating existing envelopes.

This retires PBKDF2-SHA512 / 100k / 8-byte salt, inherited from Tabby "for portability"
(`vault.go:16`) — portability we no longer need. `TestPBKDF2VaultParamsSHA512` was written under
`nocx-dcd` so that "a future change cannot silently break vault compatibility"; with nothing in the
field there is no compatibility to protect, so it is **deleted deliberately with the reason recorded
on the bead**, not quietly worked around.

### 5.2 Initialization is silent where a system store answers, and it is journalled

- **Probe succeeds** → on the first `Create` the root key is minted silently and the OS-held copy
  written. No passphrase, no recovery code: there is nothing to recover.
- **Probe fails, or the user removes the OS-held copy, or picks `file` as default** → explicit setup:
  master passphrase, recovery code shown once, Copy/Download. Confirmation is an ordinary
  acknowledgement; the user is not asked to type a code back.

A master passphrase exists exactly when it protects something.

Initialization is multi-step and therefore gets the same treatment as any other cross-store write
(§7.2): journal first, then act. The failure states that must each have a defined behaviour are the
OS-held copy written but the vault document not created; the vault document claiming initialized
while the OS write failed; and the root key existing only in memory when the process dies. None may
resolve by silently minting a second root key.

What unseal then looks like on a system-store machine, spelled out because it is counter-intuitive:
the vault is `sealed` after every restart, and unsealing is a single click on **Unlock** with no
prompt of any kind. The seal there buys the idle timer and the deliberate click, nothing more (§2,
consequence 2).

### 5.3 `file` provider blob, format version 1

Fresh, with no inherited fields — nothing is deployed:

```json
{
  "version": 1,
  "vaultInstance": "<random id, also bound as AAD>",
  "wrappedDataKey": "hex(nonce||ct||tag)",
  "contents": "hex(nonce||ct||tag)"
}
```

`wrappedDataKey` is sealed by the root key; `contents` by the data key. **AAD binds the format
version _and_ the vault-instance id.** Binding the version alone is not enough: an attacker could
transplant a `wrappedDataKey`/`contents` pair between two vault documents, or pair one installation's
metadata with another's blob, and every AEAD check would still pass. AEAD gives integrity, not
document identity.

The existing GCM helpers and the five tamper tests from `nocx-1vr` (ciphertext byte, tag byte,
version field, old-format refusal, wrong-passphrase indistinguishability) carry over, plus a sixth
for a transplanted blob. An unknown version is refused; there is no version negotiation.

### 5.4 Vault document (DocumentStore, unencrypted)

The passphrase and recovery envelopes, whether an OS-held copy exists, the default provider, the
auto-seal timeout, the preferred unseal method, and the operation journal (§7.2). **No catalogue, no
entry names, no kinds, no locators** — those either live in the domain that owns them or do not exist
(§4.1).

### 5.5 Three states

- **uninitialized** — no key material. The app is fully usable: local terminal, SSH agent, creating
  profiles, connecting with a manually entered password. Only `Create` is unavailable, and it is what
  triggers initialization.
- **sealed** — the root key is not in memory. Provider status is visible. `Create`/`Get`/`Delete`/
  `Exists` return `ErrVaultSealed`.
- **unsealed** — operations permitted; the idle timer running.

Established SSH sessions are unaffected by seal. A new connect or a reconnect that needs a stored
secret gets a typed error and the UI offers **Unlock** — there is no silent auto-unseal on connect.

### 5.6 Auto-seal

Idle timer only. Default 15 minutes; settings Off / 5 / 15 / 30 / 60. Activity is keyboard, mouse and
UI actions. **Not** activity: terminal output, background jobs, network events, incoming WebSocket
messages. Consequence, accepted: reading logs for twenty minutes without touching the keyboard means
a reconnect asks to unlock.

Sealing on OS lock and sleep is deferred (§9): there is no OS-event infrastructure in the tree today
— no `login1`, `PrepareForSleep` or `NSWorkspace` anywhere in Go — and it would take a cgo path on
macOS, a D-Bus path plus per-DE screensaver handling on Linux, and it is close to untestable in CI.

### 5.7 The availability probe

"Default wherever the system store answers" needs a defined test, because a `Get` of a nonexistent
item proves only that the service responds — not that nocx can write. The probe writes a value under
a **random** id (never a fixed one, which would collide across processes), reads it back and compares
the bytes, then deletes best-effort. A failed delete leaves one probe artifact, which is preferable to
a false positive. The probe never runs while a document lock is held (§4.5), and its outcome is
cached for the process lifetime with an explicit re-probe action.

---

## 6. Control plane

Both foundations already exist and are copied rather than invented: the broadcast set and
`broadcastSettingsChanged` (`ws.go:2204-2218`), and the forged-identifier refusal (`ws.go:1371-1375`,
verbatim `"secretId/passphraseSecretId are backend-owned"`).

**V1 methods:**

| Method          | Returns / does                                                                                     |
| --------------- | -------------------------------------------------------------------------------------------------- |
| `vault.status`  | state, providers with capability flags plus status and reason code, whether an OS-held copy exists |
| `vault.setup`   | initialize: silent when the probe succeeds; passphrase + one-time recovery code otherwise          |
| `vault.unseal`  | means (`os` \| `passphrase` \| `recovery`) plus the secret input when one is needed                |
| `vault.seal`    | seal now                                                                                           |
| `vault.changed` | broadcast notification on state change, modelled on `settings.changed`                             |

**Invariants, not aspirations:**

1. The renderer never supplies a secret reference; the backend mints it inside `Create`. This is the
   rule `credentials.create` already enforces, extended to everything new.
2. No ordinary response carries plaintext, and no response carries anything from which a storage
   location can be reconstructed.
3. An unresolvable reference is preserved, never re-routed. A `sec:v1:<unknown>:…` returns
   `ErrProviderUnavailable{reason: unknown-provider}` — the record stays valid, because a provider can
   be absent from a build, removed, or newer than this binary. **Silent fallback to the default
   provider would be the security hole**; an unresolved reference is not. Malformed syntax is a
   different thing: an invalid record.

**Blocking dependency, named explicitly:** `nocx-jb20.1` (P0, open) — `export.configExport` still
emits `SecretID` and neither import path strips it. Until that closes, invariant 1 is false in the
product no matter how correct the new vault code is.

**Typed errors — five, not ten:** `ErrVaultUninitialized`, `ErrVaultSealed`, `ErrProviderUnavailable`
(carrying a reason code), `ErrSecretNotFound`, `ErrUnsealFailed` (carrying a reason: wrong
passphrase, wrong recovery code, or a corrupt OS-held copy — deliberately indistinguishable from
tampering within each, as `nocx-1vr` established). The earlier draft's `ErrProviderLocked`,
`ErrProviderAuthenticationRequired`, `ErrExternalSourceChanged`, `ErrReadOnlyProvider`,
`ErrImportConflict` and `ErrRecoveryRequired` are gone: three described a runtime external provider
that no longer exists, one is V4, and one was a way to unseal rather than an error.

**Logging** through the existing `log/slog` interface. Permitted: operation name, provider tag,
`SecretID`, lifecycle state, duration, error category and reason code. Forbidden: plaintext,
passphrase, recovery code, encrypted blob, any request payload carrying a secret field.

---

## 7. Tabby: import only

There is no runtime Tabby provider. Lazy discovery, in-memory snapshots, fingerprint invalidation,
dynamic external credentials and per-connect selection of a foreign credential are all out. The one
capability lost is connecting with a Tabby password without importing it — and that flow made the
user pick a credential by hand on _every_ connect, which is worse than importing once.

- Decryption lives **only** in `internal/importer` as an adapter and never becomes an nocx format.
  `tabby.go:57-66` already parses the `vault` section; decryption is what V4 adds.
- The Tabby format is unauthenticated (PBKDF2-SHA512, 100k, 8-byte salt, AES-256-CBC, base64), so a
  decrypt result gets strict structural validation and bounded sizes: a doctored config must neither
  panic the parser nor push junk into the store.
- The Tabby passphrase is asked once per import operation and stored nowhere.
- **The keytar mode is out of scope entirely.** It would mean reading another application's keychain
  entries; on macOS those are ACL-bound to Tabby, and `nocx-dm0` already records the principle that a
  shared keychain makes touching unrecognised entries unacceptable. With Tabby's vault disabled we
  import profiles without secrets and the password is entered on first connect.

### 7.1 Collision policy is inherited, not reinvented

`nocx-y910.1` already decided and tested it: profiles and groups overwrite, credentials refuse, and an
imported profile naming an existing local credential is marked `needs-review` and will not resolve
until cleared. V4 adds the one case that bead could not cover — an import that **carries** a secret.
Same spirit: a foreign secret never silently overwrites an existing one; the offer is "import as a new
version" (the version machinery exists after `nocx-383c`) or skip.

### 7.2 The journal is required by ADR-0011, and it comes back

An earlier draft of this spec dropped the journal and cited ADR-0011 §4 as accepting that. That was a
misreading. §4 says, verbatim: "A small journal of **identifiers only** — never secret bytes — makes a
crash recoverable." It also settles the transaction question: its heading is "Cross-store writes are
explicit workflows, not hidden transactions", and "No transaction spans JSON and a keychain."

So the sequence for creating or rotating a secret, owned by the Vault:

1. mint the id;
2. write journal `{op: create, newID, phase: prepared}` — **before** touching the provider, because a
   call that times out may still succeed (§4.2) and an unjournalled late write would be permanently
   undiscoverable;
3. provider `Put`;
4. journal `phase: secret-written`;
5. return the id to the caller;
6. caller attaches the metadata target to the journal entry;
7. repoint metadata atomically within its own store;
8. journal `phase: metadata-repointed`;
9. best-effort delete the old secret, then clear the entry.

The journal carries operation, old and new id, the metadata target, and the phase — identifiers and
routing only, never secret bytes. Reconciliation on start is idempotent: `prepared` or
`secret-written` with no metadata target means nothing downstream can have happened, so delete the new
orphan; `metadata-repointed` means verify the new secret resolves, then retry deleting the old. An
entry naming an unknown provider is **retained and reported as blocked**, never cleared as successful.

Deletion keeps ADR-0011's opposite order: metadata first, retriable secret deletion after, because a
brief unreachable orphan is safer than metadata pointing at a secret that is gone.

**Honest limit:** the journal recovers operations it recorded. It cannot discover an entry whose
journal was lost — see §8.

---

## 8. Testability

Three requirements, the first architectural rather than a testing detail:

1. **The provider registry is injected at the composition root** (AD-8 gives this for free) so a test
   can substitute a fake provider, and a **contract suite both providers pass** pins their behaviour
   on absence, empty values, overwrite and delete-missing.
2. **A real Secret Service runs in Linux CI** — `dbus-run-session` plus `gnome-keyring-daemon` — so
   the `system` provider, the availability probe, provider selection and restart behaviour are
   exercised for real on the platform we can automate. "Verified by hand on macOS" is not a gate and
   is not treated as one.
3. **V1 acceptance is user-shaped, not unit-shaped** (AGENTS.md: a test asserts what a user can do),
   run headless through `cmd/devharness` + the `NOCX_WS_PORT` shim (`e2e/harness.ts:26-37`) with no
   wails, GTK or display: open the app with no vault configured, save an SSH password, restart,
   unseal, connect. Run twice — once with the Secret Service present, once with it absent.

Plus three tests that come straight from prior failures in this repo:

- every new RPC is called **the way the renderer calls it**, including the fields the renderer leaves
  empty (the `groups.create` defect);
- a negative test sending a renderer-supplied reference, mirroring what `nocx-jb20.1` demands;
- a crash-injection test per journal phase, asserting reconciliation is idempotent and leaves no
  metadata pointing at a missing secret.

**A known and accepted gap:** `go-keyring` exposes no enumeration (§1), so orphaned entries in the
system store whose journal entry was lost are **undiscoverable**, not merely uncollected. This is not
V1's problem, but it does bound what `nocx-dm0` can ever promise: a universal janitor requires either
a maintained index — which §4.1 deliberately removed — or platform-specific enumeration. That bead
must be amended to say so rather than carrying an unachievable goal.

---

## 9. Decomposition

**V1 — the app actually reaches a vault.** Routing in the identifier, the strict parser, the provider
registry, both providers, root key and the means that V1 itself exercises, the availability probe,
lifecycle with generations, the journal and its reconciliation, replacing `NewKeychain()` at
`app.go:90`, `Create` replacing `Set` at the four minting sites, typed errors with reason codes, the
four RPC methods plus `vault.changed`, and the minimum UI: setup, unseal prompt, and a legible error
on connect.
**Done when:** on a machine with no Secret Service a user can set up a vault, save an SSH password,
close the app, reopen it, unseal, and connect — and the same passes with a Secret Service present.

**V1 ships no dormant machinery.** Recovery-code management, preferred-unseal-method switching and
passphrase rotation are V2; V1 creates only the key material its own acceptance test exercises.
Shipping them dark would repeat exactly the failure that produced `nocx-25k9.1`.

**V2 — the Vault surface.** Lifecycle, providers, default provider, auto-seal setting, passphrase and
recovery management. It is **not** an entry list, and it has no Reveal and no Copy of a stored secret
(§3.1) — a forgotten password is replaced through the credential UI, never read back. Secret
references are minted by at least two
independent domains — credentials (`ws.go:1638`) and settings (`settings.go:867`) — so a generic
"vault entries" page would have to aggregate every metadata repository that owns references, which
recreates the central switch over domain types that §4.1 just removed. Credential secrets stay in the
credential UI, secret settings in the settings UI, and the Vault surface owns lifecycle and providers
only.

**V3 — idle auto-seal.** Activity signal from the frontend, timer, Off/5/15/30/60.

**V4 — Tabby secret import.** Decryption, preview, write through `Create`, reconciled with
`nocx-y910.1`.

**Deferred to their own beads, outside this epic:** sealing on OS lock/sleep; real OS authentication
(Touch ID / polkit); a HashiCorp provider; a provider per credential _version_ (today's
`CredentialVersion` has no provider field, and adding one reworks a model that landed a week ago under
`nocx-383c`); the orphan janitor (`nocx-dm0`, with the enumeration caveat from §8).

---

## 10. Invariants for ADR-0016

1. The Vault is the only secret boundary. Credentials, connections and settings reach secrets only
   through it; no consumer imports a provider package.
2. Capability is expressed by interface satisfaction, never by a mode string or a `switch`.
3. Routing lives in the `SecretID` and is parsed only inside the Vault. No consumer branches on it.
4. The Vault mints every reference. The renderer never supplies one.
5. An unresolvable reference is preserved and reported, never re-routed to another provider.
6. **No response returns a stored secret — there is no `get` and no Reveal** (§3.1). The only
   plaintext crossing to the renderer is material being entered or generated once; none of it is read
   back from a provider. Nothing plaintext reaches any log.
7. Global seal is a **policy** boundary for the `system` provider and a **cryptographic** one for the
   `file` provider. Seal cannot revoke bytes already handed out.
8. Only the default provider is used implicitly; the Vault never silently tries another.
9. Every cross-store write is journalled by identifier before the external call, and reconciliation
   is idempotent. Creation is secret-first; deletion is metadata-first.
10. The Vault and its providers never touch the filesystem directly, and never call the OS store while
    holding a document lock.
11. Secrets are never serializable: the `Secret` type stands, and plaintext materializes only inside
    `Secret.Use`.

---

## 11. Open — needs real hardware or a measurement

Both remaining items are measurements, not decisions. Every design question is settled.

- **macOS keychain ACL.** The claim that items created through `/usr/bin/security` are readable by any
  same-user process invoking the same utility is consistent with how `security` sets an item's trusted
  application list, but neither CI nor the dev machine can verify it. Confirm on a real Mac before
  §1.2 and threat T4 are stated as fact in the ADR. If a prompt does appear, the `system` provider is
  stronger than described and the wording softens; no structural change follows.
- **Argon2id parameters on the slowest supported machine.** m=64 MiB / t=3 / p=4 should land near
  100–200 ms; measure before fixing it in the format.
