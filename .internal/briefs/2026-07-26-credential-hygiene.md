# Worker brief — credential hygiene: two beads (`nocx-6ek.2`, `nocx-dcd`)

Both beads already contain the diagnosis, the intended fix and acceptance criteria, and both were
written after reading the code. **Do not re-litigate them** — read them, verify the claims still
hold, and implement. The bodies are reproduced below because you must not run `bd`.

Do them **in this order**: `nocx-dcd` first (self-contained, and its verification is mechanical),
then `nocx-6ek.2`.

---

## Bead 1 — `nocx-dcd`: replace the hand-rolled PBKDF2

`internal/credential/vault.go` inlines its own PBKDF2 (RFC 2898, HMAC-SHA512) with the comment
_"We avoid the x/crypto/pbkdf2 dependency by inlining the core loop."_

**The justification is false:** `golang.org/x/crypto` v0.54.0 is already a direct dependency in
`go.mod` — `internal/ssh` imports `golang.org/x/crypto/ssh`. Nothing is avoided.

Hand-rolling a crypto primitive is a standing risk even when the algorithm is simple: the vetted
implementation gets constant-time review, published test vectors and CVE tracking; this one gets
none. It sits on the vault's key-derivation path, so a subtle error weakens every secret stored on
the non-keychain path.

**Fix:** use `golang.org/x/crypto/pbkdf2` and delete the inlined loop.

**The verification requirement is the hard part and is not optional.** The derived key must be
**byte-identical** before and after, or every existing vault becomes unreadable. So:

1. Add a test with the **RFC 6070** PBKDF2 test vectors and run it against the **current inlined**
   implementation first. Paste that output.
2. Swap to `x/crypto/pbkdf2`.
3. Run the same test again. Paste that output.
4. Additionally assert equality on the vault's own parameters (its real iteration count, salt
   length and HMAC-SHA512 choice) — RFC 6070's vectors use SHA-1, so they prove the algorithm but
   **not** this call site. Derive a key with the old code, derive with the new, compare bytes.

If you cannot make the two byte-identical, **stop and escalate** with the diff. Do not "fix" it by
changing the parameters — that silently destroys existing vaults.

Context: this predates PR11-T10 (`nocx-1vr`), which replaced the AES-CBC layer around it and
deliberately left the KDF alone. This bead came out of reviewing that work.

---

## Bead 2 — `nocx-6ek.2`: encrypted keys never use their stored passphrase

`internal/ssh/ssh_auth.go` `loadKey()` parses the key and, on `gossh.PassphraseMissingError`,
returns `ErrEncryptedKey` — it **never calls `lookupKeyPassphrase`**. So a passphrase stored
through `credentials.saveKeyPassphrase` is written, referenced by `PassphraseSecretID`, threaded all
the way into `ConnectConfig`, and then never read on the connect path. **A passphrase-protected key
cannot be used at all.**

Two defects at once: the feature does not work, and `lookupKeyPassphrase` is dead code whose only
caller is `auth_chain_test.go:226` — which `AGENTS.md` forbids.

**Fix — wire it, do not delete it:** on `ErrEncryptedKey`, resolve `PassphraseSecretID` through the
`SecretStore` and retry with `gossh.ParsePrivateKeyWithPassphrase`. **Read the plaintext inside
`Secret.Use`** so it never becomes a long-lived string. That last point is the security-critical
part of this change — a passphrase that escapes `Secret.Use` into an ordinary `string` defeats the
opaque-reference design in ADR-0011.

**Acceptance criteria, from the bead:**

- a key protected by a stored passphrase connects successfully;
- `lookupKeyPassphrase` has a non-test caller;
- the passphrase is read **only** inside `Secret.Use` and is never returned as a string.

This is **pre-existing, not a regression**: verified against the baseline — `origin/main`'s
`loadKey` has the same shape and its `lookupKeyPassphrase` was equally uncalled. STORE-3
(`nocx-r60`) faithfully ported the dead path onto `SecretID` and flagged it in its report. So do
not go hunting for what broke it; nothing did.

---

## Read first

- `AGENTS.md` — binding. TDD, no dead code, no compatibility shims, structured logging via the
  logging interface (`log/slog`), never `fmt.Println`.
- `docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md` — §2 is the
  secrets-as-opaque-references rule that bead 2 must respect.

## Files you own

`internal/credential/**`, `internal/ssh/**`, `go.mod` / `go.sum` (only if the pbkdf2 import needs
it — `golang.org/x/crypto` is already there, so likely no change), plus all their tests.

You are alone in this worktree, so **whole-project gates are safe and required**. Do not touch
anything under `frontend/` — two other workers are editing the frontend on a different branch, and
nothing in these two beads needs it.

## Verification — required, on the FINAL state of the tree

```bash
gofumpt -l .
golangci-lint run ./...
go test -race -count=1 ./...
```

Frontend is untouched by this work; run its gate once anyway to prove that:

```bash
cd frontend && npm ci && npm run typecheck && npm run test
```

The Playwright e2e suite is **red on `main`** (13 failures, `nocx-bw2`) and is not in the
per-commit gate. Do not run it, do not chase it, do not claim anything about it.

## Ground rules

- **Do not commit, push or branch.** The coordinator owns git.
- **Do not touch the issue tracker.** No `bd` commands.
- **If you finish early, STOP and report.** Do not start adjacent work — and there is plenty of
  adjacent work in this area that is deliberately NOT yours (`nocx-dm0` orphaned-secret janitor,
  `nocx-23v` secret redaction, `nocx-6ek.1` Windows paths). Leave them alone.
- **Paste the real output of every gate, on the final tree.** Three worker reports on this
  programme have already claimed a clean gate that was not clean, so the next false claim only
  costs you a rejected round. For `nocx-dcd` specifically, paste the before-and-after byte-equality
  proof — a claim of "behaviour preserved" without pasted evidence will be rejected outright.
- Report the file list from actual `git status --porcelain` output, pasted, not from memory.
- Report numbers, not adjectives.
- **Never log, print or return a secret value.** If you need to prove a passphrase was used, assert
  on the _outcome_ (the key parsed, the connection authenticated), never on the plaintext.
- **State explicitly anything you could not verify** — in particular, say plainly whether you could
  exercise a real passphrase-protected key end to end or only through a unit-level fake.
