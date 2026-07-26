# Worker brief — STORE-3 / bead `nocx-r60`: SecretStore capability + `SecretID` as the boundary

You are one of three workers running in parallel in the **same checkout**
(`/home/dev/repos/nocx`, branch `feat/persistence-storage-capabilities`). Yours is the
security-critical task of the wave. Read these before writing code:

1. `docs/decisions/0011-persistence-storage-capabilities-and-secret-references.md` — binding.
   **§2 is the load-bearing rule and §4 is the cross-store write order.**
2. `.internal/plans/2026-07-26-persistence-storage-capabilities.md` — **§Task 3** is your spec,
   verbatim. Read §"What already landed" carefully: it lists what is already done so you do not
   redo it, and it is based on reading the current code, not on the ADR's prose.
3. `AGENTS.md` — engineering rules.

## What is already done — do not redo or rewrite

- `credential.Secret` (`internal/credential/secret.go`) is finished and thorough: unexported
  `value`, `Use(fn)` as the only accessor, `MarshalJSON`/`MarshalText` **return an error**,
  and `String`/`GoString`/`Format`/`LogValue` all render `[REDACTED]`. **Leave this type alone.**
  Your task builds the _capability_ around it; it does not re-litigate the type.
- `VaultSecret.Value` is already a `Secret`, not a serializable string.
- `credentials.lookupPassword` is already gone from the wire. The surviving credential RPCs are
  already exactly `set`/`delete`/`exists`.
- `session.open` already carries `profileId` only; the profile→credential→secret join already
  happens backend-side in `internal/connection/resolver.go`.

## Your deliverable

The exact interface (`SecretID`, `NewSecretID`, `SecretStore`), the design rules, the acceptance
criteria and the TDD steps are in plan §Task 3. Do not invent a different interface shape. In
summary, three things:

1. **`SecretStore` + stable `SecretID`.** One keychain service for all nocx secrets, account =
   `string(id)`. Nothing is ever re-derived from a file path or from a key's contents again.
2. **`profile.Credential` carries `SecretID` and `PassphraseSecretID`** — opaque strings in
   JSON. `KeyPath` stays; it is not a secret. Thread the IDs through
   `internal/connection/resolver.go` and the `ws.go` credential handlers.
3. **Rewrite the delete cascade against `SecretID`** — this is what finally closes the interim
   implementation. Order per ADR-0011 §4: load metadata → read both `SecretID`s → delete
   metadata → delete secrets best-effort with `errors.Join`. **No re-reading the private key
   file**, which is the whole point: today's cascade cannot delete a passphrase when the user
   has already deleted the key file.

Delete `HashKey`, `KeyHash`, and the `Identity{User: credentialID}` convention. They exist only
because there was no stable reference; a `SecretID` replaces them. **Greenfield: no migration of
existing keychain entries, no dual-read, no compatibility shim.** Old entries become unreachable
garbage and that is the accepted outcome — do not write code to rescue them.

**The wire API does not change shape.** `credentials.savePassword` and its siblings keep taking
`credentialId`; the backend mints and looks up the `SecretID`. The renderer must never see a
`SecretID`, and no RPC returns one. This should mean **zero frontend changes** — if you conclude
otherwise, escalate before touching `frontend/`.

The invariant your work must leave provable: **no API outside `internal/credential` and
`internal/ssh` can obtain plaintext.** `internal/transport/ws_logging_test.go` and
`internal/transport/ws_cascade_test.go` must still pass.

## Files you own (nobody else touches them this wave)

- `internal/credential/**` (create `secretstore.go` + `secretstore_test.go`; modify
  `credential.go`, `keychain.go`, `vault.go`)
- `internal/connection/resolver.go` and its test
- `internal/transport/ws.go` — the credential handlers around `:1029-1160` and the delete
  cascade at `:1240-1270` — and the transport tests
- `internal/ssh/**` where `ConnectConfig.CredIdentity` / `ConnectConfig.Credentials` are consumed
- `internal/profile/profile.go` — **metadata fields only** (adding the two `SecretID` fields).
  Do not restructure `internal/profile/store.go`; that is STORE-4 in a later wave.

## Files owned by OTHER workers — do not touch, escalate instead

- `internal/storage/**`, `internal/app/app.go` → owned by the STORE-1 worker. If your change
  needs composition-root wiring, **escalate**; do not edit `app.go`.
- `internal/content/**` → owned by the STORE-6 worker.

## Ground rules

- **Greenfield.** No migrations, no back-compat shims, no compatibility fallbacks. Delete the old
  path rather than bridging it.
- **TDD**: failing test first, run it, watch it fail, then the minimal implementation. One test in
  particular must be written to fail on today's code: credential deletion removing **both**
  secrets when the private key file no longer exists.
- **Never weaken a security control.** If a step seems to require exposing plaintext, widening a
  return type to `string`, or relaxing the host-binding enforcement in `internal/ssh`
  (`BoundHost`/`BoundPort`, nocx-mon/PR11-T5), that is an escalation, not a decision you make.
- **Never call the keychain while holding a document lock** (ADR-0011 §4).
- **No commit, no push, no branch, no `git stash`.** The coordinator commits.
- **No repo-wide gates.** Do **not** run `go build ./...`, `go test ./...`, `golangci-lint run` —
  another worker's half-written file will make you report a phantom blocker. Scope your runs:
  `go test ./internal/credential/... ./internal/connection/... ./internal/transport/... ./internal/ssh/... ./internal/profile/...`
- **No formatting runs.** No `gofumpt -w`, no `prettier`. Separate final wave.
- **Do not touch the issue tracker.** No `bd` commands — the coordinator owns beads.

## Report in `worker_done`

Numbers, not adjectives:

- Test counts before and after, per package, and the exact commands you ran.
- The failing-first test for the deleted-key-file cascade: paste the failure message you saw
  **before** implementing, proving it fails on the old code.
- Every call site where a `Secret` is converted to a `string`, with its justification — the
  coordinator will check each one against the ADR §2 boundary.
- Confirmation that `HashKey`, `KeyHash` and `Identity{User: credentialID}` are **deleted**, not
  merely unused.
- Whether `frontend/` needed any change (expected: no).
- **Anything you could not verify, stated explicitly.** Silence here is treated as a failure to
  report, not as "nothing to report".
- Any problem you spotted and deliberately left alone.
