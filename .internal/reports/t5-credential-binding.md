# PR11-T5 — bind a stored credential to its resolved target host

Worker: nocx-mon wave. Branch `pr-11-boundary`. No commits, per the ground rules.

## The threat

`Credential.Host` was optional and empty meant "works for any host". An
authenticated renderer can both create profiles and call `open`, so it could
point a victim credential at a host it controls and have the backend submit
the password there. T4 stopped the password being _returned_; this is the
other direction. Strict `known_hosts` is host authentication, not credential
authorization — it does not stop a credential being aimed where it was never
meant to go.

## What I built

A binding check at resolve time, straddling two layers (neither has both
facts alone):

- **`internal/connection` (where the binding is known):** the resolver copies
  `profile.Credential.Host`/`Port` onto `ssh.ConnectConfig.BoundHost`/`BoundPort`
  for the target, and `JumpBoundHost`/`JumpBoundPort` for the jump host. The
  resolver does **not** decide the refusal — it carries the binding through.
- **`internal/ssh` (where the effective target is known):** `checkBinding`
  runs **after `resolveConfig`**, before any dial, comparing the credential's
  bound host (case-insensitive, `strings.EqualFold`) and effective port
  against the _resolved_ hostname and effective port — never the alias.

Enforcement points: `RealClient.Connect` (target) and `dialer.connectToJumpHost`
(jump), each gated on `Credentials != nil` so inline auth is exempt (no stored
secret to redirect).

## Decisions (recorded in the code)

- **"Belongs to" = host + port, not user.** Identity for binding is the
  network endpoint the password is submitted _to_. User is an auth parameter,
  not part of the target. Binding on user would let an attacker rename the
  target account and redirect; binding on host+port pins the actual
  destination. (`credential.Identity` for _lookup_ still includes user; that
  is a distinct concern from _binding_.)
- **Empty `Credential.Host` = refused.** The hole _is_ empty-host-means-any.
  Existing unbound credentials are refused on next connect with a distinct
  `ErrCredentialNotBound` naming the credential, so the user sees _why_ their
  old credential no longer works and can re-bind it. No auto-migration: the
  approval-record alternative was rejected (it would live in the renderer —
  the actor this task constrains).
- **Unset bound port (`0`) = "this host, any port".** Host is the
  load-bearing identity. Making port mandatory would break every existing
  host-only credential harder than the hole it would close. **Stated
  exception, not a silent gap.**
- **Port compared: the effective port after `~/.ssh/config` merge + explicit
  override**, never the profile's `Options.Port` alone — an alias can
  override `Port`. `TestBinding_PortFromAlias` pins this.

## Where the check lives and why the alias is unsound

`internal/ssh` after `resolveConfig`. The coordinator confirmed this scope.
A profile alias is not a target: `~/.ssh/config` can map `Host myserver` to
`HostName evil.example.com`, so binding on the alias lets anyone who can
write a profile or an ssh-config entry redirect a bound credential while the
check keeps passing. A binding satisfiable by a name the attacker chooses is
not a binding.

## Tests (`./internal/ssh/...`, `./internal/connection/...`)

`internal/ssh/ssh_binding_test.go` — the attack, against the real in-process
SSH server:

- `TestBinding_RefusesMismatchedHost` — credential bound to A, aimed at B
  (unreachable) → `ErrCredentialBindingMismatch`, not a dial error. The
  unreachable target is the proof the check fires **before any dial**: a
  mismatch returns a binding error, not a connection-refused/timeout.
- `TestBinding_AliasResolutionNotAlias` — alias `victim` → `HostName
127.0.0.1`. Bound to the alias string `victim` → **refused** (resolved host
  is `127.0.0.1`, ≠ `victim`). Bound to the resolved `127.0.0.1` →
  **connects**. This is the load-bearing proof: matching uses the resolved
  value, not the alias, and the check is not "deny everything".
- `TestBinding_PortFromAlias` — alias whose `Port` overrides the default;
  credential bound to port 22 → refused because the effective port is the
  alias's ephemeral port, not 22.
- `TestBinding_UnboundRefused` — empty `BoundHost` → `ErrCredentialNotBound`,
  error names the credential ID.
- `TestBinding_HostAnyPortWhenPortUnset` — bound host with port `0` →
  accepted on the server's ephemeral port (the stated exception).
- `TestBinding_InlineAuthNotChecked` — no `WithCredentials` → check skipped,
  inline profile connects. Guards against an over-broad check.
- `TestBinding_JumpHostRefused` — jump credential bound to
  `other-bastion.example.com`, jump alias resolves to `127.0.0.1` →
  `ErrCredentialBindingMismatch` with `Jump=true`, `ResolvedHost` = the jump
  alias's resolved `HostName`. Target binding matches, isolating the
  jump-host enforcement.

`internal/connection/resolver_test.go` — the wiring (refusal is ssh's job,
tested above):

- `TestResolver_CarriesTargetBinding` — credential bound to A, profile
  pointing at B → `cfg.BoundHost` = A (the credential's, not the profile's).
- `TestResolver_UnboundCredentialSurfacesEmpty` — empty `Host` travels
  through as empty `BoundHost` (not defaulted/masked), so ssh refuses it.
- `TestResolver_CarriesJumpBinding` — jump binding carried separately from
  target binding; both present and independent.

`internal/profile/profile.go` — only the stale `Host` doc comment was
rewritten ("empty = works for any host" → the new semantics). No behaviour
change in the profile package.

## Verification

```
go test -race ./internal/connection/... ./internal/profile/... ./internal/ssh/...
ok  github.com/shady2k/nocx/internal/connection  1.011s
ok  github.com/shady2k/nocx/internal/profile      1.015s
ok  github.com/shady2k/nocx/internal/ssh          1.108s

gofumpt -l .          # clean (repo-wide — the brief's gate)
golangci-lint run ./internal/ssh/... ./internal/connection/... ./internal/profile/...   # exit 0
go vet   ./internal/ssh/... ./internal/connection/... ./internal/profile/...            # clean
```

`git diff HEAD -- internal | grep '^-'`: the only removed lines in my files
are two comments I replaced with fuller explanations (resolver inline-mode
comment; the stale "empty = works for any host" credential comment). No code
removed, no control weakened. (A third `-` line in `internal/transport/ws.go`
belongs to the other worker's uncommitted edit — not mine.)

## Files modified (mine)

- `internal/ssh/ssh.go` — `ConnectConfig` gains `BoundHost`/`BoundPort`,
  `JumpBoundHost`/`JumpBoundPort`.
- `internal/ssh/ssh_config.go` — `checkBinding`, after `resolveConfig`.
- `internal/ssh/ssh_real.go` — enforce target binding in `Connect`.
- `internal/ssh/ssh_dial.go` — enforce jump binding in `connectToJumpHost`.
- `internal/ssh/errors.go` — `ErrCredentialNotBound`, `ErrCredentialBindingMismatch`.
- `internal/ssh/ssh_binding_test.go` — new, the attack tests.
- `internal/connection/resolver.go` — carry binding (target + jump) from
  `profile.Credential`.
- `internal/connection/resolver_test.go` — wiring tests.
- `internal/profile/profile.go` — doc comment only.

## Out of scope / not touched

- `internal/transport/ws.go`, `internal/credential/**` — the other worker
  owns them; not modified. (`internal/credential/credential.go` was briefly
  uncompilable mid-run due to the other worker's in-progress edit; it builds
  again now and my packages were never affected.)
- Frontend, migration tooling for existing unbound credentials. The refusal
  is surfaced as a distinct error; a future task can wire a re-bind prompt.

## Numbers

- 7 new ssh binding tests, 3 new resolver wiring tests; 10 total, all pass
  with `-race`.
- 0 dial attempts on refused bindings (proven by unreachable-target +
  binding-error-returning-binding-not-dial).
- 1 stated exception: unset bound port = any port.
