# ADR-0015 — `ssh -G` as the `~/.ssh/config` Oracle

- **Status:** Accepted
- **Date:** 2026-07-29
- **Related:** `nocx-c2ym` (wave 7), `nocx-n7yv` (bug class: silent alias mis-resolution)

## Context

SSH config parsing is done today through `github.com/kevinburke/ssh_config` v1.2.0, called in two places:

- `ResolveHostName` (`internal/ssh/ssh_config.go:124-142`) — resolves a host alias to its `HostName` directive; used by the connection resolver so it knows the canonical endpoint before authorising a credential.
- `resolveConfig` (`internal/ssh/ssh_config.go:28-89`) — reads `HostName`, `User`, `Port` and one `IdentityFile` from the config and overlays explicit `ConnectConfig` values.

Both functions call `ssh_config.Decode` and then iterate over the result with `Config.Get`.

### The library's `Match` problem

The library does not support the OpenSSH `Match` directive. Verified from source (`v1.2.0`):

1. **Parser fails on `Match`.** `parser.go:107-109` calls `raiseErrorf` (which panics) on any line whose keyword is `match`. The panic is recovered in `decodeBytes` (`config.go:310-320`) and converted to an `error`.
2. **`Decode` returns an error.** A `~/.ssh/config` containing a `Match` block anywhere — even a `Match all` — causes `Decode` to fail.
3. **Callers treat decode errors as "no config".** `ResolveHostName` returns the original host unmodified; `resolveConfig` falls through to its defaults. No warning is logged, no error is signalled to the user.
4. **`Config.Get` panics on a `Match` KV.** If by some path a `Match` keyword survived parsing into a parsed `Config`, `Config.Get` (`config.go:354-355`) calls `panic("can't handle Match directives")`. This is unreachable today because parsing fails first, but it is a latent crash in a function nocx does call.

**The user-visible bug:** anyone with a `Match` block in `~/.ssh/config` sees host alias resolution silently stop working for _all_ hosts, not just the ones matched by `Match`. The error is silent because the fallthrough from `Decode` failure passes undetected.

### What the spec called for

The connection manager design spec (`.internal/specs/2026-07-29-connection-manager-design.md`, §9 item 3) requires an implementation plan to declare "the declared `~/.ssh/config` supported subset, and the `ssh -G` conformance fixture set." That task largely dissolves: the subset is implicit, because the evaluator is the oracle.

## Decision

**nocx will not parse `~/.ssh/config` itself.** It asks OpenSSH what configuration it would apply, via `ssh -G <host>`, and treats that answer as authoritative.

### What we remove

- `ssh_config.Decode` and `Config.Get` calls in `internal/ssh/ssh_config.go`. The `kevinburke/ssh_config` dependency is removed from `go.mod`.
- `ResolveHostName` is replaced by a call to `ssh -G <host>` and extracting `hostname` from its output.
- `resolveConfig` reads the resolved values (`user`, `port`, `identityfile`, and the several other fields the plan in §3.5 will add) from `ssh -G` output instead of from the library. Explicit `ConnectConfig` values still win the overlay (precedence: explicit > `ssh -G` output > hard-coded default).
- `resolveAuthzEndpoint` resolves through `ssh -G` the same way.

### What stays

- The `ssh_config.go` file and its `resolvedConfig` type. Only the parser dependency changes.
- The overlay logic that applies explicit `ConnectConfig` values on top of the resolved config — that is nocx's own policy, independent of how the base values are obtained.
- Caching. `ssh -G` per host on every connection attempt is a subprocess per open; we cache the output per host and invalidate on `~/.ssh/config` mtime change (or stat-based expiry).

## Consequences

### Positive

- **Correctness by construction.** The reference implementation is the oracle. There is no supported subset to declare and maintain — `ssh -G` already supports `Match`, `Include`, `ProxyJump`, token expansion, `CanonicalizeHostname`, multiple `IdentityFile`, and every other directive OpenSSH implements. Every future directive ships for free.
- **No silent failure.** Either `ssh -G` succeeds and gives authoritative output, or it fails with an error and we can surface that to the user.
- **Closes `nocx-n7yv`.** The bug class of unknown-unknowns in config parsing — directives the library silently ignores, interprets differently, or panics on — is eliminated.

### Negative

- **External process on the resolution path.** An `ssh` binary must exist and be executable. True on macOS and Linux; open on Windows (see Revisit when).
- **Subprocess per host.** Every alias or host resolution spawns an `ssh -G` child. Without caching this is O(1) process per open, which is fast on modern kernels but not free. Caching with mtime-based invalidation keeps it amortised — a user who edits `~/.ssh/config` once a session pays one extra spawn.
- **`ssh -G` output is a stable-ish text format, not an API.** OpenSSH has not changed the `ssh -G` output format in any breaking way in recent memory, but it is not versioned. A parsing test per directive we consume catches format drift.
- **No easy path for a no-ssh build.** A nocx build that targets users without an `ssh` binary is now impossible without reimplementing an ssh config evaluator. That is not a current requirement.

## Alternatives considered

### Keep the library, fail loudly

Instead of silently returning the original host on `Decode` error, surface the error to the user: log a warning, or mark the affected profile. Cheap and honest.

Rejected because it describes what breaks, not what works. A user with a `Match` block anywhere loses all alias resolution, and nothing short of rewriting that config restores it. The feature (ssh aliases in nocx) simply does not work for a real class of users.

### Write our own parser

Implement nocx's own `~/.ssh/config` parser supporting the directives we need. This avoids both the library's limitations and the subprocess cost.

Rejected because the SSH config grammar — first-obtained-wins ordering, `Include` with globs, `Match` with `exec` conditions and canonicalisation, token expansion (`%h`, `%p`, `%r`, `%n`), multiple `IdentityFile`, `CanonicalizeHostname`, `IdentityAgent`, malformed includes — is a long tail of divergence that users would find before tests do. Binding to `ssh -G` means OpenSSH's own test suite covers our parser.

## Relationship to `nocx-n7yv`

`nocx-n7yv` is the bug bead for silent alias mis-resolution. The root cause is trusting the `kevinburke/ssh_config` library to produce the same answer OpenSSH would. This ADR closes that bug by removing the trust entirely: we ask OpenSSH directly.

## Revisit when

- **Windows support.** Whether `ssh -G` exists on Windows is an open question — OpenSSH for Windows ships it as part of an optional feature, and the output format has not been verified against the OpenSSH Portable reference. When a Windows build is on the roadmap, either:
  - The Windows `ssh -G` output is verified against the fixture set and the subprocess path is kept; or
  - A minimal `~/.ssh/config` evaluator is written for the subset needed on Windows, tested against `ssh -G` on a CI runner that has it.
- **Latency regression.** If `ssh -G` execution time on the target platform exceeds 50 ms P99 for a cold cache (measured under real `~/.ssh/config` loads with `Include` chains and `Match exec` conditions), we may need to pre-warm the cache or run resolution asynchronously.
- **OpenSSH changes the `-G` output format.** If a future OpenSSH version renames, removes, or changes semantics of a kw we extract, the parsing test catches it and we adapt.
