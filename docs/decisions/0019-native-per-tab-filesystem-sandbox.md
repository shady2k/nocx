# ADR-0019 — Native per-tab filesystem sandbox (opt-in, experimental)

- **Status:** Accepted
- **Date:** 2026-08-02
- **Related:** ADR-0002 (native tabs, no embedded multiplexer), AD-7 (one PTY per tab),
  AD-8 (interface-first, single composition root), ADR-0011 (storage roles),
  `.internal/reports/2026-08-02-native-filesystem-sandbox-research.md`,
  `.internal/specs/2026-08-02-native-filesystem-sandbox-design.md`,
  `.internal/plans/2026-08-02-native-filesystem-sandbox-v1.md`
- **Supersedes:** nothing

## Context

nocx spawns local shells via `pty.NewLocal` → `exec.Command(shell, "-i")` with no
containment. Users who want to experiment with a shell that cannot touch the host filesystem
outside a chosen workspace have no way to get one. The reference implementation,
`simion/termic` at commit `a4d2d29d13ea5df5017f9f310babf42fe3278431`, ships a macOS-only
`sandbox-exec`/Seatbelt cage plus an in-process HTTP CONNECT proxy, scoped to a per-task agent
PTY, with per-task and project-level settings, a pure allow-list filesystem model, and
**fail-open** degradation when provisioning fails (verified: `lib.rs` `pty_spawn` ~L1839-1854
spawns unsandboxed on provision error). termic's Docker sandbox design
(`docs/plans/docker-sandbox/*`) is explicitly "proposed, not started", and termic ships no
Linux sandbox at all. termic is AGPL-3.0; nocx reuses behavior, never code.

nocx is a different product: interactive user shells, typed settings (`internal/settings`),
a JSON-RPC control plane with `data.reason` errors, and a strict no-fallback culture. Its
threat model for this experiment is a mistake or malicious filesystem operation made by the
sandboxed shell and its descendants after launch — nothing more.

## Decision

Ship an **opt-in, experimental, filesystem-only, native per-tab sandbox**:

- **Native mechanisms only**: Linux Landlock (strict, unprivileged LSM) and macOS Seatbelt
  (`sandbox-exec -p`, deprecated but runtime-probed). One platform-neutral
  `internal/sandbox.Service` behind the existing composition-root pattern (AD-8).
- **Per-tab, explicitly opted in**: exactly one setting, `sandbox.enabled`
  (Experimental, default `false`, `PublicConfig`), gates a `Sandboxed shell…` Quick Connect
  action that opens a **new** local tab after a native workspace picker. Ordinary tabs, SSH
  tabs, the initial tab, `+`, and `Cmd/Ctrl+T` are untouched. An existing PTY is never
  retrofitted.
- **Common filesystem contract**: both backends enforce the same writable/read-only root
  model — the canonical workspace, an optional Git common directory (from the worktree's
  `.git` file), and one ephemeral mode-`0700` runtime tree under the app cache dir with
  isolated `HOME`/`TMPDIR`; read-only system/runtime roots plus the canonical shell and
  inherited `PATH` dirs. Network, environment, credentials, IPC, devices, and processes are
  explicitly outside the guarantee.
- **Fail closed**: a sandbox request that cannot be enforced fails (`-32010`/`-32011`/`-32012`
  with stable `data.reason` values) and never launches unsandboxed — a deliberate divergence
  from termic's fail-open provisioning. A session is registered only after enforcement is
  confirmed (Linux readiness handshake; macOS probe + `-p` launch success).
- **Linux floor**: Landlock ABI >= 3 (below 3 the kernel cannot deny truncation at all),
  with a semantic cap at ABI 8 — the strict config is `min(detectedABI, 8)`, no `BestEffort`,
  no `RestrictNet`, no `RestrictScoped`; `github.com/landlock-lsm/go-landlock v0.9.0` pinned
  in `go.mod`.
- **macOS mechanism is deprecated and gated**: `sandbox-exec(1)` and `sandbox_init(3)` are
  marked DEPRECATED; App Sandbox is entitlement-based and whole-app and cannot supply
  per-child/per-tab policy; there is no supported Seatbelt/SBPL replacement API. V1 therefore
  treats macOS as experimental, probes at runtime, and ships only while the real packaged
  `.app` passes the platform smoke suite.

### Rejected alternatives

- **App Sandbox (macOS)**: entitlement-based, baked into the app signature, whole-app; cannot
  express per-tab policy (research report §5).
- **Docker/OCI runtime**: heavyweight, not selected for this experiment; termic's own Docker
  design is proposal-only and nocx has no container packaging seam for per-tab shells.
- **Network proxy / firewall**: network isolation is out of scope; the common contract leaves
  network unrestricted (termic's CONNECT proxy is rejected for copying and for scope).
- **Global sandbox default**: contradicts per-tab opt-in; a default-on sandbox would change
  existing behavior, violating the product's no-surprise rule.
- **Best-effort degradation**: `BestEffort` in go-landlock can succeed with zero enforcement,
  and termic's fail-open path proves a silently-unsandboxed tab is indistinguishable from a
  caged one. Fail closed instead.

## Consequences

- New module `internal/sandbox` with platform constructors (`sandbox_linux.go`,
  `sandbox_darwin.go`, unsupported stub), injected only at `internal/app/app.go`.
- New wire surface: `open` gains optional `sandbox: {workspace}`; `sandbox.status` and
  `dialog.openDirectory` are added; `-32010`/`-32011`/`-32012` are reserved with stable
  `data.reason` values. Ordinary and SSH `open` results are unchanged.
- New UI surface: `Sandboxed shell…` action (visible only while the flag is on), unavailable
  row with typed reason, and a lock/shield marker with backend + writable-roots tooltip on
  every sandboxed tab. Sandboxed tabs have `restoreDescriptor: null` in V1.
- `pty.NewLocal` is refactored to build the ordinary command first, then call
  `Service.Prepare` only when a request is present; ordinary behavior is preserved
  byte-for-byte.
- One setting, `sandbox.enabled` (Experimental, `PublicConfig`, default `false`); no allowed
  paths, network, mode, or global-default settings in V1.
- Enabling/disabling the flag never changes or kills running tabs; the backend rejects a
  sandbox request while the flag is off.
- macOS enforcement is `[ASSUMPTION]` until the real release artefact passes the platform
  smoke suite; Landlock requires ABI 3+; Windows is unsupported in V1 (unsupported stub).
- No AGPL code is copied; only behavior-level lessons (per-session policy generation,
  allow-list model, availability reporting, security-visible state) are borrowed, and
  fail-closed launch is recorded as an intentional divergence from termic.

## Not decided here

- Whether network/process isolation is ever added (V1 explicitly excludes it; adding it later
  is a new contract and a new ADR).
- Whether sandboxed tabs get restore support (V1: `restoreDescriptor` is null by design).
- Whether the ephemeral home/temp policy is replaced by user-configurable roots.
- Windows strategy if a future sandbox need appears there.
