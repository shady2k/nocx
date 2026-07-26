# T7 — VaultSecret stops being serializable (Secret type)

Bead: `nocx-l7o` (PR11-T7). Branch work in `/home/dev/orca/workspaces/nocx/pr-11-boundary`.

## What was built

`internal/credential/secret.go` — a `Secret` type that:

- **Refuses serialization.** `MarshalJSON` and `MarshalText` return
  `errSecretNotSerializable` (`"credential.Secret is not serializable; use
Secret.Use to access plaintext"`), naming the type so a caller that tries
  to serialize one finds out at the call site. Verified by
  `TestSecretMarshalJSONErrors` (direct + struct-containing-Secret) and
  `TestSecretMarshalTextErrors`.
- **Renders `[REDACTED]` everywhere non-binding.** `String`, `GoString`,
  `Format` (covers every `fmt` verb), and `LogValue` (slog) all return
  `[REDACTED]`. `TestSecretFormatsRedacted` asserts `%s`/`%v`/`%#v`/`%d`/`%x`
  never leak the plaintext; `TestSecretSlogRedacted` asserts on the emitted
  log bytes (`password=[REDACTED]`), not a struct field.
- **Exposes plaintext only via `Use(func([]byte) error) error`.** The single
  binding accessor. `TestSecretUseHandsPlaintext` confirms the callback
  receives the real value; `TestSecretUsePropagatesError` confirms fn errors
  propagate. `NewSecret`/`NewSecretBytes` copy the input so the caller
  cannot mutate the held bytes after handoff (`TestSecretNewSecretCopies`).
  `IsEmpty` lets callers test for the absent-credential case without
  reading plaintext.

## Migration

`VaultSecret.Value` is now `Secret` (was `string \`json:"value"\``). The
`json:"value"`tag is kept (not`json:"-"`) so `json.Marshal(VaultSecret{})`calls`Secret.MarshalJSON`and **errors loudly** — a silent`json:"-"`would
let marshaling succeed while dropping the secret, which is a data-loss hole,
not a boundary.`TestVaultSecretRefusesMarshal` locks this in.

**Vault persistence** (`internal/credential/vault.go`): the in-memory
`vaultData` holds `[]VaultSecret` (with `Secret` values). At the encryption
boundary only, `vaultData.toDTO()` reads each secret through `Secret.Use`
into a private `vaultSecretDTO` (plain `string` value), which `json.Marshal`
serializes for GCM encryption. On decrypt, `vaultDataDTO.fromDTO` wraps each
plaintext back into a `Secret`. This is the one unavoidable plaintext copy —
it lives only for the duration of `Marshal` and is documented at the call
site. The vault round-trip (`TestVaultEncryptDecryptRoundTrip` and all AEAD
tamper tests) still passes.

**`CredentialStore` interface** (`internal/credential/credential.go`):
`LookupPassword` and `LookupKeyPassphrase` now return `Secret` instead of
`string`. `SavePassword`/`SaveKeyPassphrase` still take `string` (the
boundary is on output, not input). Both backends updated:

- `vaultCredentialAdapter` wraps/unwraps via `NewSecret`/`s.Value`.
- `Keychain` (`keychain.go`) wraps the keyring string in `NewSecret`.

**SSH** (`internal/ssh/ssh_auth.go`): `authChainEntry.password string`
became `secret credential.Secret`. `addPasswordMethods` builds
`gossh.PasswordCallback` via `passwordCallbackFromSecret`, which
materializes the plaintext through `Secret.Use` **only when the SSH server
challenges for it** — so the password lives in memory for the duration of
the callback, not the lifetime of the chain. The `Use` error is propagated
(not discarded). `addKeyboardInteractiveMethods` stores the `Secret` for
type consistency; its `method` is still nil (the keyboard-interactive auth
method is not actually built — that was already dead code before this task
and is out of scope). `lookupKeyPassphrase` returns `Secret`.

**Stubs** (`internal/connection/resolver_test.go`): `stubCredentialStore`
updated to the new interface. `internal/transport` uses
`credential.NewKeychain()` directly, so no stub change was needed there.

## The trap — places plaintext escapes `Use` (per the brief)

The brief warns that `Use(func([]byte) error)` is only a boundary if nothing
copies the bytes out, and asks that any unavoidable escape be named in the
report rather than left for a reviewer to grep. There are two places this
task copies plaintext out of `Use` into a string; both are unavoidable and
both are documented at their call sites:

1. **`internal/credential/vault.go` — `vaultData.toDTO` (the encryption
   boundary).** The plaintext must become a `string` for the JSON DTO that
   GCM encrypts. There is no way to encrypt a secret without materializing
   it. The copy lives only for the duration of `vault.Marshal` (until
   `json.Marshal` + `encryptGCM` consume it) and is never handed out beyond
   that function.

2. **`internal/ssh/ssh_auth.go:124` — `passwordCallbackFromSecret` (the SSH
   auth boundary).** `x/crypto/ssh`'s `PasswordCallback` signature is
   `func() (secret string, err error)` — it accepts a string password, not a
   callback over bytes. So the plaintext is read out of `Use` into `pw` and
   returned to `gossh`. This is the minimal possible escape: the string
   exists only for the duration of the callback invoked at challenge time,
   not for the lifetime of the auth chain. It is strictly better than the
   old code, which held `authChainEntry.password string` for the whole
   chain. The `Use` error is propagated, not discarded.

Every other consumer of `LookupPassword`/`LookupKeyPassphrase` takes the
`Secret` and either passes it along (`authChainEntry.secret`) or compares
via `IsEmpty`/`Use` in tests — no further string copies.

## Verification

Scoped gates (per the brief, no frontend/`npm`):

```
$ go test -count=1 ./internal/credential/... ./internal/ssh/... ./internal/connection/... ./internal/transport/...
ok  github.com/shady2k/nocx/internal/credential   1.251s
ok  github.com/shady2k/nocx/internal/ssh          0.034s
ok  github.com/shady2k/nocx/internal/connection   0.003s
ok  github.com/shady2k/nocx/internal/transport    15.668s

$ go vet ./internal/credential/... ./internal/ssh/... ./internal/connection/... ./internal/transport/...
(clean)

$ go test -race -count=1 ./internal/credential/... ./internal/ssh/... ./internal/connection/... ./internal/transport/...
ok  github.com/shady2k/nocx/internal/credential   9.495s
ok  github.com/shady2k/nocx/internal/ssh          1.094s
ok  github.com/shady2k/nocx/internal/connection   1.011s
ok  github.com/shady2k/nocx/internal/transport    16.728s

$ go test -count=1 -run 'TestNoPlaintextSecretsOnWire' ./internal/transport/
ok  github.com/shady2k/nocx/internal/transport    0.005s
```

Acceptance criteria from the brief, all verified by tests:

- `json.Marshal` of a struct containing a `Secret` returns an error naming
  the type → `TestSecretMarshalJSONErrors`, `TestVaultSecretRefusesMarshal`.
- `fmt.Sprintf("%s"/"%v"/"%#v")` and an `slog` record all render
  `[REDACTED]`, never the value, asserted on emitted bytes →
  `TestSecretFormatsRedacted`, `TestSecretSlogRedacted`.
- `Use` hands the real plaintext to the callback →
  `TestSecretUseHandsPlaintext`.
- `TestNoPlaintextSecretsOnWire` still passes.

`git diff HEAD -- internal | grep '^-'` reviewed: every deletion is an
intentional replacement of the old `string`-based `Value`/`LookupPassword`/
`LookupKeyPassphrase`/`authChainEntry.password` signatures and the
`json.Marshal(v.data)` persistence path. No unrelated code was removed. All
crypto helpers (`encryptGCM`, `decryptGCM`, `buildAAD`, `pbkdf2`,
`deriveKey`, `randomBytes`, `keysEqual`) are intact and unchanged.

gofmt + gofumpt clean on all touched files (`gofumpt -l .` reports nothing).
`internal/profile/profile.go` (the other worker's file) was not touched.

## Out of scope (per the brief)

Not touched, as specified:

- vault KDF (`nocx-dcd`) — PBKDF2 hand-roll unchanged.
- AEAD (`nocx-1vr`) — GCM construction unchanged.
- credential-to-host binding (`nocx-mon`) and delete-cascade (`nocx-7l4`).
- `frontend/**` and `internal/profile/profile.go` (another worker owns
  these; no frontend gates run).

## Files

New: `internal/credential/secret.go`, `internal/credential/secret_test.go`.
Modified: `internal/credential/credential.go`, `vault.go`, `keychain.go`,
`credential_test.go`, `keychain_test.go`, `vault_testhelpers.go`,
`internal/ssh/ssh_auth.go`, `internal/ssh/auth_chain_test.go`,
`internal/connection/resolver_test.go`.

No commits, no pushes, no branches, no `git stash` (per ground rules).
