# T4 — Move the wire to profile IDs

## What was done

Replaced the plaintext-carrying SSH connection wire with profile-ID-based resolution. The backend now resolves profiles through a new `internal/connection.Resolver` that maps `profileID -> {host, credential.Identity, non-secret options}`. The frontend sends only a `profileId` in the `open` RPC; passwords are never serialised across the WebSocket. All Go tests (14 packages) and frontend tests (381 across 20 files) pass with `-race`.

## What was found

- **The seam existed and worked.** `ssh.WithCredentials(store, identity)` at `ssh.go:139` was already wired through `session.go:274` and resolved at `ssh_auth.go:117-118`. We only needed to route the open path through it and remove the bypass.
- **Jump hosts required parallel wiring.** `ConnectConfig` had `JumpPassword` but no `JumpCredentials`/`JumpCredIdentity`. Added both, forwarded through `sshOptionsFromConfig`, and plumbed into `connectToJumpHost`.
- **Two RPCs removed:** `credentials.lookupPassword` and `credentials.lookupKeyPassphrase` now return `-32601` (method not found). The dispatch entries and case bodies in `handleCredentialMethod` were deleted.
- **Test deleted:** `TestCredentialsRPC_SaveLookup` in `ws_profiles_test.go` — it asserted the behaviour being removed.
- **Auth chain tests updated:** `TestAuthChainOrderAuto` and `TestAuthChainFilterByAuthMode` previously used `Password: "pw123"` directly. Updated to use `Credentials + CredIdentity` via `keyring.MockInit()`.
- **Cycle detection:** The resolver rejects cyclic jump host references with an error naming the profile ID.
- **No files outside scope were touched.** `internal/credential/vault.go`, `pool.go`, `ssh_real.go`, `ssh_channel.go`, and `ssh_dial.go` (beyond the specific jump-credential forwarding) were not modified.

## What was verified

- `go test -race ./...` — 14 packages pass, 0 failures
- `gofumpt -l .` — clean after formatting
- `golangci-lint run` — clean
- Frontend: `npx tsc --noEmit`, `npx eslint`, `npx prettier --check .`, `npx vitest run` — all pass
- Self-diff (`git diff HEAD -- <files> | grep '^-'`): every deletion is an intentional removal of a password-carrying field, function, or RPC. No accidental removals found.

## What was not verified

- End-to-end SSH connection with real credentials. The devharness path is documented in `.internal/reports/devharness-verify.md`.
- Playwright suite (13 pre-existing failures, not in scope).

## Quick-connect decision

Quick-connect (double-click / "SSH" button in the connection manager) now passes the profile directly without resolving credentials client-side. The backend resolver handles credential lookup and jump host resolution. No ephemeral profile is created — the profile must already exist in the store.

## Files changed

### New

- `internal/connection/resolver.go` — ProfileID-to-ConnectConfig resolver with credential wiring and jump host recursion
- `internal/connection/resolver_test.go` — Tests for credential mode, inline mode, jump host, unknown profile, and cycle detection

### Modified (Go)

- `internal/ssh/ssh.go` — Removed `Password`, `JumpPassword` from `ConnectConfig`; removed `WithPassword`; changed `WithJumpHost` signature; added `JumpCredentials`/`JumpCredIdentity` + `WithJumpCredentials`
- `internal/ssh/ssh_auth.go` — `addPasswordMethods` and `addKeyboardInteractiveMethods` now only use credential store (no plaintext cfg.Password path)
- `internal/ssh/ssh_dial.go` — `connectToJumpHost` forwards `JumpCredentials`/`JumpCredIdentity`
- `internal/ssh/auth_chain_test.go` — Tests updated to use credential store instead of Password field
- `internal/session/session.go` — `sshOptionsFromConfig` forwards `JumpCredentials`; removed Password forwarding; updated JumpHost call
- `internal/transport/ws.go` — `openParams` replaced with `ProfileID`; `handleOpen` uses resolver; removed `lookupPassword`/`lookupKeyPassphrase` dispatch and cases; added `ProfileResolver` interface and `WithProfileResolver` option
- `internal/transport/ws_profiles_test.go` — Deleted `TestCredentialsRPC_SaveLookup`; added `TestNoPlaintextSecretsOnWire` (canary test proving no secret travels on the wire, covering profiles._, credentials._, and open paths including jump-host resolution with distinct target/jump canaries)
- `internal/app/app.go` — Wires `connection.NewResolver(profileStore, credStore)` via `WithProfileResolver`

### Modified (Frontend)

- `frontend/src/ipc.ts` — `openSSHSession` takes `profileId` instead of individual SSH fields
- `frontend/src/tabs.ts` — Removed `SSHProfileConnectOpts`; Tab sshOpts simplified to `{profileId, host, user}`; `newSSHTab`/`createTab`/`onConnect` updated
- `frontend/src/profiles.ts` — Removed `lookupPassword` method
- `frontend/src/connections.ts` — Removed `ResolvedSSHOptions`; simplified `quickConnect` and connect button handler to pass profile directly; removed `cloneProfile`
