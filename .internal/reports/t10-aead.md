# T10 — AES-256-GCM AEAD for vault encryption

## What changed

Replaced the unauthenticated AES-256-CBC encrypt/decrypt in
`internal/credential/vault.go` with an authenticated AES-256-GCM construction.
The vault now writes format version 2; version 1 (legacy CBC) blobs are
refused with a clear message.

## Format (version 2)

```
StoredVault.Version = 2
StoredVault.Contents = hex(nonce || ciphertext || tag)
StoredVault.KeySalt  = hex(PBKDF2 salt, 8 bytes)
StoredVault.IV       = "" (retained for backward JSON compat, unused)
```

- Nonce: 12 bytes from `crypto/rand`, generated fresh per `Marshal` call.
- Ciphertext+tag: AES-256-GCM output (tag is 16 bytes, appended by Go's `gcm.Seal`).
- Key: 32 bytes via PBKDF2-HMAC-SHA512, 100k iterations (unchanged).

### GCM additional authenticated data (AAD)

17 bytes: `version (4B BE) || salt (8B raw) || iterations (4B BE) || keyLength (1B)`.

This binds the KDF parameters and version number to the ciphertext. An attacker
who swaps the salt, iteration count, key length, or version in a stored blob
will cause GCM authentication failure — indistinguishable from a wrong
passphrase.

## Design rationale

- **AES-256-GCM** over ChaCha20-Poly1305: stdlib (`crypto/cipher`), hardware
  accelerated (AES-NI), no new dependencies. GCM-SIV and XChaCha20-Poly1305
  add nonce-misuse resistance unnecessary here since a fresh random nonce is
  generated per encryption.
- **Nonce reuse prevention**: each `Marshal` call generates both a fresh 8-byte
  salt and a fresh 12-byte nonce from `crypto/rand`. Even identical plaintext
  with the same passphrase produces a different derived key and nonce. The
  birthday bound for random 96-bit nonces is ~2^48 encryptions — far beyond
  vault file lifetime.
- **Version in AAD**: mirrors `StoredVault.Version` so GCM detects version
  tampering even if the JSON `version` field were altered.
- **Salt + KDF in AAD**: prevents an attacker from substituting different KDF
  parameters that could weaken the key derivation.

## Migration of old vaults

Version 1 vaults (legacy AES-CBC) are **refused** with the error:

> vault format version 1 is no longer supported (unauthenticated AES-CBC);
> delete the vault file and create a new one with a fresh passphrase

Silent migration would accept forged old-format data without detection (CBC
has no MAC). Refusing forces the user to re-create the vault, which is the
safe path for a security boundary change.

Unknown version numbers are also refused explicitly: `unknown vault format
version N`.

## Error indistinguishability

Both wrong-passphrase and tampered-data failures produce the identical error:

> decrypt failed: wrong passphrase or corrupted vault

GCM's `Open` returns a generic error on authentication failure, and the
vault's `decrypt()` wraps it without distinguishing cause. The caller cannot
tell whether the passphrase was wrong or the ciphertext was tampered.

## Tests added (5 new, all pass)

| Test                                                | What it proves                                                                |
| --------------------------------------------------- | ----------------------------------------------------------------------------- |
| `TestVaultTamperedCiphertextRejected`               | Flipping a byte in the ciphertext (past the nonce) causes decryption failure  |
| `TestVaultTamperedTagRejected`                      | Flipping a byte in the GCM tag (last 16 bytes) causes decryption failure      |
| `TestVaultTamperedVersionRejected`                  | Changing the version field to 999 causes explicit refusal                     |
| `TestVaultOldFormatRefused`                         | A version=1 `StoredVault` is refused with a message mentioning version/format |
| `TestVaultWrongPasswordIndistinguishableFromTamper` | Wrong-password and tampered-data errors are identical strings                 |

Existing 7 vault tests + 9 keychain tests continue to pass. Total: 21 passing, 0 skipped, 0 failures.

## Verification

```
go test -race ./internal/credential/...   → PASS (21/21)
gofumpt -l internal/credential            → (clean)
golangci-lint run ./internal/credential/... → (clean)
git diff HEAD -- internal/credential | grep '^-'
  → ivLength, encryptAESCBC, decryptAESCBC, IV generation/usage, old error messages
  → no accidental removals
```

## Files modified

- `internal/credential/vault.go` — crypto constants, `StoredVault` doc, `Marshal`, `decrypt`, replaced CBC with GCM + `buildAAD`
- `internal/credential/credential_test.go` — 5 new AEAD tamper-detection tests + `encoding/hex`/`strings` imports
