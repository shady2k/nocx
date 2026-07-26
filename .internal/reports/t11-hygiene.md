# T11 hygiene — verdict and evidence

Bead: `nocx-1q2` (PR11-T11). Worker: w10-t11.
Scope owned: `frontend/src/connections.ts`, `internal/profile/profile.go`, plus
whatever `gofumpt -l .` and `git diff --check` name outside another worker's scope.

## Baseline checks (run before any change)

- `gofumpt -l .` — **clean for my scope.** The only files it flags are
  `internal/credential/secret.go` and `internal/credential/secret_test.go`,
  which belong to the concurrent worker on `nocx-l7o` (`internal/credential/**`).
  `secret.go` currently has a parse error (`expected 'package', found 'import'`
  at 4:1) — it is mid-edit by that worker, not a formatting defect I can fix.
  Per the brief, reported and left untouched. `internal/profile/profile.go` is
  **not** flagged; `gofumpt -d internal/profile/profile.go` is empty.
- `git diff --check` — **clean.** No output against the working tree, against
  the merge base `f308874` (origin/main's tip), or against `origin/main...HEAD`.
  `grep -n ' $' frontend/src/connections.ts` finds no trailing whitespace. The
  trailing whitespace the bead describes at `connections.ts:71` is no longer
  present — later commits on the branch (`fc37a6a`, `fe6e614`) rewrote the file.
- `.orig` artifact — already gone (`e5ad9f2`, per the brief). Confirmed:
  `git ls-files '*.orig'` is empty.

## Files modified

**None.** `git diff --stat HEAD` is empty; `git diff HEAD | grep '^-'` returns
zero removals. The baseline was already clean on the whitespace/gofumpt axes, so
no formatting or whitespace edits were warranted, and the dead-UI investigation
concluded "retain and report" rather than "remove" (see below).

## Dead UI fields — verdict: retain all four (outcome #3, missing feature)

The form at `connections.ts:440-499` exposes four SSH options whose values the
user can set: `keepaliveInterval`, `keepaliveCountMax`, `readyTimeout`, and
`agentForward`. The bead asked which of three outcomes each is. The answer is
the same for all four: **collected, sent, and ignored by the backend connect
path** — outcome #3, a missing feature, not dead UI. None were deleted.

### Evidence chain (same for all four fields)

1. **Collected.** Each control writes back to `profile.options.<field>`:
   `connections.ts:441` (keepaliveInterval), `:446` (keepaliveCountMax),
   `:451` (readyTimeout), `:496` (agentForward).
2. **Sent.** `saveProfile` → `ProfileClient.createProfile`/`updateProfile`
   ships the whole `SSHProfile` (options block included) over the JSON-RPC
   control plane. The backend `profile.SSHProfileOptions` struct declares all
   four (`profile.go:60-64`), `mergeInto` copies them on defaults merge
   (`profile.go:166-179`), and the Tabby importer maps them
   (`internal/importer/tabby.go:39-43,139-143`). So the values are accepted,
   persisted, and survive a defaults merge — they are not silently dropped at
   the store boundary.
3. **Ignored at the connect seam.** `internal/connection/resolver.go`
   `buildConfig` (`:71-125`) is the single point that turns a profile into an
   `ssh.ConnectConfig`. It copies `Port`, `User`, `AuthMode`, `KeyFile`,
   `JumpHost`, `JumpPort`, `JumpUser`, `JumpKeyFile`, `JumpAuthMode`, and the
   credential/jump-credential wiring. It does **not** copy `KeepaliveInterval`,
   `KeepaliveCountMax`, `ReadyTimeout`, or `AgentForward`. `ssh.ConnectConfig`
   (`internal/ssh/ssh.go:36-83`) has **no field** for any of the four, so there
   is nowhere for the resolver to put them even if it tried.
4. **Ignored by the SSH layer.** `internal/ssh/ssh_dial.go:37` hardcodes
   `Timeout: 30 * time.Second` — the profile's `ReadyTimeout` is never used.
   A repo-wide grep for `KeepAlive|Keepalive|RequestAgentForward|AgentForward|
SetKeepAlive|time.NewTicker|ReadyTimeout` across `internal/ssh/` returns
   **zero matches** (verified on `ssh.go`, `ssh_dial.go`, `ssh_config.go`,
   `ssh_real.go`, `ssh_channel.go`, `pool.go`, `auth_chain_test.go`). There is
   no keepalive ticker and no agent-forwarding request anywhere in the SSH
   package.

### Per-field verdict

| Field               | UI line | Stored?               | Sent?      | Backend acts?                                                   | Verdict                          |
| ------------------- | ------- | --------------------- | ---------- | --------------------------------------------------------------- | -------------------------------- |
| `keepaliveInterval` | 441     | yes (`profile.go:60`) | yes (CRUD) | **no** — no `ConnectConfig` field, no ticker in `internal/ssh/` | missing feature — retain, report |
| `keepaliveCountMax` | 446     | yes (`profile.go:61`) | yes        | **no**                                                          | missing feature — retain, report |
| `readyTimeout`      | 451     | yes (`profile.go:62`) | yes        | **no** — `ssh_dial.go:37` hardcodes `30s`                       | missing feature — retain, report |
| `agentForward`      | 496     | yes (`profile.go:64`) | yes        | **no** — no `RequestAgentForward` call anywhere                 | missing feature — retain, report |

### Why this is outcome #3 and not #2

The distinguishing question the brief poses is "does the value reach the
backend." It does: the profile (with these options) is persisted via the
profile CRUD methods, and the backend `SSHProfileOptions` struct + `mergeInto` +
importer all carry the fields. The break is one step later — the resolver does
not propagate them into `ConnectConfig`, and the SSH layer has no knobs to
receive them. That is "the backend ignores it," not "silently dropped before
the backend." Per the brief, a missing feature is **not** deleted; it is
reported so it can be filed. No controls were removed.

### What would close the gap (for filing)

- Add `KeepaliveInterval`, `KeepaliveCountMax`, `ReadyTimeout`, `AgentForward`
  to `ssh.ConnectConfig` (`internal/ssh/ssh.go`).
- Copy them in `resolver.buildConfig` (`internal/connection/resolver.go`).
- In `ssh_dial.go`: use `ReadyTimeout` (falling back to 30s) for
  `gossh.ClientConfig.Timeout`; start a keepalive ticker using
  `KeepaliveInterval`/`KeepaliveCountMax` via `gossh.ClientConn.SendRequest(
"keepalive@openssh.com", true, nil)`.
- In the channel/shell path: call `gossh.Session.RequestAgentForwarding` when
  `AgentForward` is set.

These all touch `internal/ssh/**`, `internal/connection/**` — the concurrent
worker's scope. Not attempted here.

## Verification (run after investigation; no edits to verify)

- `gofumpt -l .` — clean for `internal/profile/profile.go` (the flagged files
  are in `internal/credential/`, another worker's scope).
- `git diff --check` — clean (working tree, merge base, and `origin/main...HEAD`).
- `go build ./internal/profile/...` — exit 0.
- `cd frontend && npx tsc --noEmit` — exit 0.
- `cd frontend && npm run lint` — exit 0.
- `cd frontend && npm run format:check` — "All matched files use Prettier code style!"
- `cd frontend && npx vitest run` — 20 files, 381 tests, all pass.
- `git diff HEAD | grep '^-'` — zero removals (no accidental deletions; no
  edits made).

## What I could not verify

- `go test ./...` was **not** run, per the brief: `internal/credential`,
  `internal/ssh`, `internal/connection`, `internal/transport` contain another
  worker's half-written packages and would surface phantom failures. `go build
./internal/profile/...` is the build check for my owned package and it passes.
- The `internal/credential/secret.go` parse error is the other worker's
  in-flight edit; I did not inspect or touch it beyond confirming it is outside
  my scope.
