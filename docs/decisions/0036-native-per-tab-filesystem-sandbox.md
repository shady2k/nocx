# ADR-0036 — Native per-tab filesystem sandbox (opt-in, experimental)

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
sandboxed process and its descendants after launch — nothing more.

## Decision

Ship an **opt-in, experimental, filesystem-only, native per-tab sandbox**:

- **Native mechanisms only**: Linux Landlock (strict, unprivileged LSM) and macOS Seatbelt
  (`sandbox-exec -p`, deprecated but runtime-probed). One platform-neutral
  `internal/sandbox.Service` behind the existing composition-root pattern (AD-8).
- **Per-tab, explicitly opted in**: the Experimental, default-off `sandbox.enabled`
  setting gates a `Sandboxed opencode…` Quick Connect action that opens a **new** local agent
  tab after a native workspace picker and permission confirmation. Ordinary tabs, SSH tabs,
  the initial tab, `+`, and `Cmd/Ctrl+T` are untouched. An existing PTY is never retrofitted.
- **Common filesystem contract**: both backends enforce the same writable/read-only root
  model — the canonical workspace, an optional Git common directory from a validated linked
  worktree, and one ephemeral mode-`0700` runtime tree under the app cache dir with isolated
  `HOME`/`TMPDIR`; read-only system/runtime roots plus the canonical shell, fixed agent
  executable, their runtime dependencies, and inherited `PATH` dirs. The worktree exception requires the canonical
  `<common>/.git/worktrees/<name>` shape and its regular `gitdir` backlink to resolve exactly
  to the selected workspace's regular `.git` file; the workspace pointer alone is never
  authority to widen the cage. Network, environment, credentials, IPC, devices, and
  processes are explicitly outside the guarantee.
- **Fail closed**: a sandbox request that cannot be enforced fails (`-32005`/`-32006`/`-32007`
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
  `dialog.openDirectory` are added; `-32005`/`-32006`/`-32007` are reserved with stable
  `data.reason` values. Ordinary and SSH `open` results are unchanged.
- New UI surface: `Sandboxed opencode…` action (visible only while the flag is on),
  unavailable row with typed backend/intent reason, and a lock/shield marker with backend +
  writable-roots tooltip on every sandboxed tab. Sandboxed tabs have
  `restoreDescriptor: null` in V1.
- `pty.NewLocal` is refactored to build the ordinary command first, then call
  `Service.Prepare` only when a request is present; ordinary behavior is preserved
  byte-for-byte.
- Sandbox wrapping preserves every descriptor the ordinary command inherits (including the
  shell-integration lifecycle channel); Linux appends policy/readiness descriptors and passes
  their computed child FD numbers to the helper rather than assuming fd 3/4.
- Git compatibility never trusts the writable workspace's `.git` pointer alone: metadata
  reads are bounded, symlink-contained with `os.Root`, and the linked-worktree target must
  reciprocally point back before its parent common `.git` directory is granted read-write.
- One setting, `sandbox.enabled` (Experimental, `PublicConfig`, default `false`); no allowed
  paths, network, mode, or global-default settings in V1.
- Enabling/disabling the flag never changes or kills running tabs; the backend rejects a
  sandbox request while the flag is off.
- Pull-request CI runs the real source-level enforcement smokes on Linux and macOS 15. The
  release workflow additionally invokes the release-tagged Linux binary and the executable
  inside the packaged `.app`; both enter the same `Service.Prepare` path as a tab.
- macOS enforcement remains `[ASSUMPTION]` until that packaged-artifact gate passes on the
  hosted macOS 15 runner; Landlock requires ABI 3+; Windows is unsupported in V1.
- No AGPL code is copied; only behavior-level lessons (per-session policy generation,
  allow-list model, availability reporting, security-visible state) are borrowed, and
  fail-closed launch is recorded as an intentional divergence from termic.

ADR-0037 amends the one-workspace policy with bounded writable grants and launch-time
overrides. ADR-0038 amends the launch target to fixed backend-resolved `opencode`, moves
private bootstrap artifacts into the sandbox runtime tree, and requires lifecycle
authentication before the bootstrap shell replaces itself with the agent.

## Not decided here

- Whether network/process isolation is ever added (V1 explicitly excludes it; adding it later
  is a new contract and a new ADR).
- Whether sandboxed tabs get restore support (V1: `restoreDescriptor` is null by design).
- Whether the ephemeral home/temp policy is replaced by user-configurable roots.
- Windows strategy if a future sandbox need appears there.

## Amendment — the package store is a system read-only root (2026-08-20, nocx-263da)

The documented read-only set was FHS: `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`,
`/etc`, `/dev`, `/proc`, `/sys`. On a content-addressed distribution that list
authorizes nothing. `/etc/hosts` on NixOS resolves to a store path, and both
Landlock and Seatbelt authorize the resolved target, so a read-only grant on
`/etc` covers the symlink and not the file behind it. The measured effect was
one failing check out of thirty-five in the Landlock enforcement smoke, and a
policy that never got that far anyway: the loader roots derived from `PATH`
came to 339 on that host against a 256-root bound, so every launch was refused
with `-32007` and a message naming an internal constant.

The store is added to the system read-only set on Linux when it exists. It is
the same kind of object `/usr` is — system-owned, world-readable, immutable
(mounted read-only), holding packages rather than user documents, which is why
storing a secret in it is a known anti-pattern rather than a threat this
boundary was ever meant to carry. Naming it collapses the derived roots at
their source: on the host that found this, 347 roots become 12, because every
one of the 339 is a descendant of the store.

What is deliberately NOT done: no scan of the store, no per-package grant, no
writable grant anywhere inside it (`writableRootIsProtected` covers it exactly
as it covers `/usr`), and no second entry for Guix's `/gnu/store` until
somebody runs the enforcement smoke on Guix. The root-count bound moves from
256 to 1024 as well, because it was conflating what a REQUEST may contribute —
which is bounded tightly and separately — with what a MACHINE contributes; the
serialized-size bound remains the real ceiling, since the macOS profile travels
in argv.
