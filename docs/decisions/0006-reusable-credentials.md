# ADR-0006 — Reusable Credentials (УЗ) for SSH Connections

- **Status:** Superseded by ADR-0017 (Accepted 2026-07-31). The credential
  aggregate is deleted, not renamed — see ADR-0017 for what happens to each
  of its jobs. Kept for the record; no part of it describes the current model.
- **Date:** 2026-07-24
- **Related:** Connection Manager UI, SSH authentication, `nocx-ec2u`, `nocx-j685`, `nocx-0w2f`

## Amendment (2026-07-29)

Three waves have shipped since this ADR was written, and the model the document
describes stopped existing in wave 2. The amendments below record what the code
actually does.

**Host binding was removed (`nocx-j685`, wave 2).** The original design let a
credential carry an optional `Host` as a connect-time guard: if the credential
was host-bound, verify the connection target matched. `nocx-mon` (wave 0) made
a _non-empty_ host required — an empty host became a refusal at connect time,
because "any host" is what lets a compromised renderer aim a credential at a
host it controls. That defence was correct but limited (the same constrained
actor can rewrite the host), and it collapsed the user experience: one
credential served exactly one host, and a fleet of forty needed forty edits.

Wave 2 (`nocx-j685`) deleted `Credential.Host` and `Credential.Port`
outright. Authorization is now **computed** at connect time from the profile
that references the credential (§3.1 of the connection-manager design spec) —
the saved profile's effective (credential, endpoint) pair proves authorisation
locally. The old host-binding paragraph in the original text is replaced
below; the earlier rule is recorded here so its reasoning is not lost.

**Secret versions were added (`nocx-ec2u`, wave 1).** The original credential
held one `SecretID`. The shipped `Credential` struct carries
`Versions []CredentialVersion`, `CurrentVersionID`, and
`CandidateVersionID`, because rotation is a rollout, not an edit, and a
credential must be able to hold multiple generations of secret material. The
Data Model block below reflects the current struct.

**`CredentialPatch` replaced raw write-back.** Because `SecretID` and
`PassphraseSecretID` are backend-owned opaque references (ADR-0011 §2) —
stripped from every list response and rejected on every request — the
renderer cannot round-trip a whole credential. Updates go through a sparse
`CredentialPatch` that the renderer _can_ author. The original ADR's API
section did not account for this; the corrected API section is below.

**UI is migrating from a dedicated button to the settings rail.** The
original ADR described a "Saved Credentials (УЗ) button" inside the
connection-manager panel. Wave 5 (`nocx-0w2f`) replaces that with a
Credentials section in the settings rail, hosting a shared credential form
that both the settings section and the wave-6 connection dialog use. The
original button section is replaced below.

**Vault-versus-keychain (`nocx-25k9`) is deliberately unresolved by this
ADR.** The original Storage section's "OS keychain / encrypted vault" language
is preserved without change — this ADR does not decide where secrets
physically live.

The original decision text below is preserved for context. The sections that
no longer match the shipped code are replaced by the text below; read this
amendment and the corrected sections as the current state.

---

## Context

The initial connection manager UI stored authentication settings (passwords,
private keys) inline within each SSH profile. This led to duplication: if a
user had the same credentials for 10 servers, they had to enter the password
10 times. Other terminal emulators (Tabby, SecureCRT, MobaXterm) solve this
with **reusable credentials** (УЗ — учетные записи): a credential is a named
authentication identity (username + auth method + secret) that can be shared
across multiple connections.

The existing backend `CredentialStore` interface (internal/credential) is
Identity-based: passwords are keyed by `{user, host, port}`. This is the
correct model for **secret storage** (the OS keychain needs a unique key per
secret), but it does not support **reusable credential objects** that the UI
needs.

## Decision

**Introduce a Credential abstraction layer above the Identity-based secret
store.**

### Data Model

```go
// Credential is a reusable authentication identity. It holds identity only —
// never a host. Which endpoints a credential may be spent on is computed from
// the saved profiles that reference it.
//
// Secrets live in the credential.SecretStore behind opaque references
// (ADR-0011 §2). SecretID and PassphraseSecretID are BACKEND-OWNED: they are
// stripped from every response and rejected on every request, so the renderer
// can neither read nor write them. That is why updates take a CredentialPatch
// rather than a whole record.
type Credential struct {
    ID       string   // Unique ID (e.g. "cred:work-github:024e5c1a")
    Name     string   // Display name (e.g. "work-github", "prod-server")
    Username string   // SSH username
    Auth     AuthMode // Auth method: password, publicKey, agent, keyboardInteractive
    KeyPath  string   // Private key path (only for publicKey auth)

    // SecretID is the opaque reference to the stored password.
    SecretID string
    // PassphraseSecretID is the opaque reference to the stored key passphrase.
    PassphraseSecretID string

    // Versions holds the history of secret material for this credential.
    // A record written before versions existed has no Versions list and a
    // bare SecretID; Current() synthesises one current version from those
    // fields, so existing stores load with no migration step.
    Versions []CredentialVersion

    // CurrentVersionID names the version a normal connection uses.
    CurrentVersionID string
    // CandidateVersionID names a version being staged for rollout.
    // Unused until wave 8 — carried here so the model exists, not built on.
    CandidateVersionID string
}

// CredentialVersion is one generation of a credential's secret material,
// typed per auth method so password-shaped fields do not leak into a key
// rotation.
type CredentialVersion struct {
    ID string

    Auth AuthMode // Auth mode at version creation time; for validation.

    PasswordSecretID      string // Keychain reference for the password.
    PassphraseSecretID    string // Keychain reference for the key passphrase.
    KeyFingerprint        string // SHA256 of the public key, recorded on save.

    Created time.Time // When this version was created.
}
```

A legacy credential (written before versions existed) reads as one current
version from its bare `SecretID` — no migration is required.

Updates arrive as a `CredentialPatch` (sparse — nil means "don't change") so
the renderer can never blank backend-owned secret references.

```go
type CredentialPatch struct {
    Name     *string
    Username *string
    Auth     *AuthMode
    KeyPath  *string
}
```

There is deliberately no `SecretID` in the patch: secret references move only
through `credentials.savePassword` and its siblings, which mint their own IDs.

### Authorization

A credential carries identity only. Host binding was removed in wave 2
(`nocx-j685`): tying a credential to a host forced one credential per host,
defeated reuse, and the connect-time guard was duplicable by the same
constrained actor it was meant to constrain.

At connect time the backend proves authorisation **locally**, from the
selected saved profile:

```
selected saved profile
  -> resolve its group inheritance chain
  -> obtain its effective credentialId and secret version
  -> resolve its canonical endpoint once
  -> authorize that (credential version, endpoint) pair
```

Repeated per credential-bearing hop, where each hop is justified by the
effective profile that supplies **that hop's** credential — not by the fact
that some unrelated saved profile happens to use the same credential at the
same endpoint.

"Which connections use this credential?" is a separate query for the UI,
answered by effective resolution, not by grepping for direct `credentialId`
references.

### Storage

Credentials are stored in the profile store (JSON file) alongside SSH
profiles. The actual secrets (passwords, key passphrases) remain in the OS
keychain / encrypted vault, keyed by `Credential.ID` (not by Identity).

Secrets move through dedicated RPC methods (`credentials.savePassword`, etc.)
that mint backend-owned opaque references (ADR-0011 §2). The credential
aggregate carries the reference; the renderer never sees or transmits the
secret value over the wire.

When connecting, the SSH module resolves the credential:

1. Load `Credential` by ID from the profile store.
2. Resolve the effective profile (profile + group inheritance chain) and
   obtain the credential version.
3. Authorise the (credential version, endpoint) pair against the saved
   profile.
4. Load the secret from the keychain by the version's `SecretID`.
5. Dial with the resolved secret.

### UI Changes

**Settings-rail Credentials section:**

The credential list and form live in a dedicated Credentials section in the
settings rail, alongside the generated sections (Clipboard, Interface) and the
component pages Export / Backup / Import and Connections. A single shared form
component is used both here and in the wave-6 connection dialog.

- Shows a list of saved credentials with edit/delete actions. Like the other
  component pages, it is not among the pages the rail offers while a search is
  active — the rail's search box filters generated settings rows, not pages.
- Opens a form to create/edit a Credential: name, username, auth method,
  secret (password or key path). Secrets are entered through the secret
  field and stored in OS keychain, never in the profile store.
- Credential selection in the connection form is a dropdown that references
  the credential by ID; username and auth are inherited from the credential.
- The credential list in the settings rail and the credential selector in
  the connection dialog use the same form component.

### Backend API

JSON-RPC methods:

- `credentials.list` → `[]Credential` (secret references stripped)
- `credentials.create` → `Credential`
- `credentials.update` (takes `CredentialPatch`) → `Credential`
- `credentials.delete` → `bool`
- `credentials.savePassword` / `credentials.savePassphrase` — mint secret
  references, keyed by `Credential.ID`.

Existing methods (`credentials.savePassword`, etc.) are adapted to key by
`Credential.ID` instead of `Identity`.

`credentials.list` strips backend-owned `SecretID` and `PassphraseSecretID`
from every response (ADR-0011 §2). `credentials.update` accepts a sparse
patch and rejects any payload that attempts to set secret references.

### Consequences

- **Positive:** Users can create a credential once and reuse it across
  multiple connections. Changing a password in one place updates all
  connections using that credential.
- **Positive:** Clear separation between connection settings (host/port) and
  authentication (username/secret).
- **Positive:** Removing the host binding (wave 2) means a credential is no
  longer locked to one host — authorisation is derived from the saved
  profile, not from a duplicate field on the credential.
- **Positive:** Secret versions (wave 1) enable rotation as a rollout: stage
  a candidate version, probe it, promote — without a window in which the
  password is unreachable.
- **Negative:** Migration required for existing profiles with inline
  credentials. Existing profiles will continue to work (legacy inline auth
  is still supported).
- **Negative:** Slightly more complex mental model (credentials plus connections plus versions, versus just connections).
- **Negative:** The sparse patch model means the renderer cannot accidentally
  blank stored secrets, but it also means whole-record round-trips are
  impossible — the renderer must get version metadata through dedicated
  queries.

## Migration Path

Existing profiles with inline `user`/`auth`/`password`/`privateKeys`
continue to work. The UI will show a warning: "This connection uses inline
auth. Consider creating a reusable credential."

Credentials written before the version model existed (a bare `SecretID` with
no `Versions` list) load as a single current version — no migration step is
required.

## Revisit When

- **Multi-protocol support:** If we add RDP/VNC, the Credential model may
  need protocol-specific fields.
- **Cloud key management:** If we integrate with AWS Secrets Manager /
  HashiCorp Vault, the Credential model may need to reference external
  secrets.
- **Credential rollout orchestration:** When wave 8 activates
  `CandidateVersionID`, the credential form needs a version-switching UI and
  probe-aware straggler management.
