# Native per-tab filesystem sandbox — design

- **Date:** 2026-08-02
- **Touches:** ADR-0033 (this decision), AD-7/AD-8 (session model and composition-root wiring),
  `internal/settings`, `internal/transport`, `internal/session`, `internal/pty`,
  `internal/app`, `frontend/src/quick-connect.tsx`, `frontend/src/tabs.ts`,
  `frontend/src/terminal-content.ts`
- **Status:** approved by the owner on 2026-08-02 for the V1 architecture below; evidence
  ledger in `.internal/reports/2026-08-02-native-filesystem-sandbox-research.md`; executable
  decomposition in `.internal/plans/2026-08-02-native-filesystem-sandbox-v1.md`.

This spec owns the complete V1 architecture. Where this spec and the implementation plan
disagree, this spec wins and the plan is wrong.

> **Amended 2026-08-18:** ADR-0034, ADR-0036, and
> `docs/superpowers/specs/2026-08-16-sandbox-per-tab-permissions-design.md` supersede this
> document's workspace-only request, one-setting/no-extra-roots, and macOS start-as-readiness
> clauses. ADR-0037 and
> `docs/superpowers/specs/2026-08-18-sandbox-shell-contract-restoration-design.md` restore
> the original `Sandboxed shell…` product intent and supersede ADR-0035's fixed-OpenCode
> launch, while retaining its placement of private bootstrap artifacts inside the runtime
> tree. This document remains authoritative for the unchanged platform policy and lifecycle.

---

## 1. What is actually true today

Verified against the tree on 2026-08-02, not recalled (full citations in the research report §4):

- `internal/transport/ws.go:581` `openParams` carries `{cols, rows, xpixel, ypixel, enhanced,
kind, profileId, host, user}` and nothing else; `handleOpen` (`:805`) builds
  `session.Config{Kind: KindLocal, …}`, branches to the SSH paths, calls
  `s.registry.Open` (`:926`) and returns `{sessionId, cwd}` (`:973-982`).
- `internal/session/session.go` `Config` (`:30`) has no sandbox field; `Reg.Open` (`:159`)
  creates the PTY **before** minting the session ID and registering — a failed
  `NewPTY` today returns no registry entry. That ordering is the invariant the sandbox
  setup-failure path must keep.
- `internal/pty/pty_local.go:62` `NewLocal` builds `exec.Command(shell, "-i")` with
  `cmd.Dir = resolveCwd(cfg.Cwd)` and a scrubbed, UTF-8-forced env plus `cfg.Env`, then
  `pty.StartWithSize`. There is exactly one PTY per tab (AD-7).
- `internal/app/app.go` is the single composition root: `localPTYFactory` wraps
  `pty.NewLocal`; `session.New(logger, ptf)`; `storage.NewAppPaths()`; `settings.New(docStore,
v)`; `SetDialogService` (`:37`). There are no package globals for services.
- `internal/settings/settings.go` auto-registers typed declarations (`MustRegisterBool`); the
  renderer renders from `settings.describe` (`frontend/src/settings.tsx:81` `Declaration`),
  so one new declaration is the whole settings change. Live updates flow through
  `Registry.SetNotifier` → `broadcastSettingsChanged` (`ws.go`).
- `internal/transport/ws_dialog.go` exposes `DialogService.OpenFile` behind `dialog.openFile`
  (JSON-RPC `-32601` when unwired); renderer contract types are **generated** from
  `contracts/*.schema.json` (`frontend/src/generated/`), so the new picker needs a new schema.
- `frontend/src/quick-connect.tsx` `ActionsQuickConnectProvider` lists `__local__` and
  `__new_connection__`; `frontend/src/tabs.ts` `newTab()` uses
  `restoreDescriptor: {type: 'local'}`; `frontend/src/terminal-content.ts:455-456` opens local
  sessions with `openSession(cols, rows, true)`.
- `internal/storage/paths.go` resolves `ConfigDir/DataDir/CacheDir` with no fallback;
  `internal/shellintegration/shellintegration.go` `EnsureInstalled(home)` installs the OSC
  hooks into a home directory.
- Error convention: `error.data.reason` strings ride `-32603`/`-32000` responses
  (`ws_vault.go`); `-32010`, `-32011`, `-32012` are unused in the tree (verified by search).
- `go.mod` is `go 1.26`; `go-landlock` is not a dependency. Host kernel (2026-08-02) runs
  Landlock ABI 9; the product floor is ABI 3 (§8.1).

---

## 2. Threat model and scope

**In scope (the guarantee):** a sandboxed tab's shell and its descendants cannot open host
filesystem paths outside the documented root set, and can only mutate the documented writable
roots — after launch. The mechanism defends against mistakes or malicious filesystem
operations performed by the sandboxed shell and its descendants.

**Explicitly excluded (stated wherever user expectations are set):**

- Compromise of the renderer, the Wails host, or the nocx backend process itself.
- Kernel or `sandbox-exec`/Seatbelt bypass, and Landlock kernel-side limitations (e.g. the
  acknowledged `fallocate(FALLOC_FL_COLLAPSE_RANGE)` truncation sidestep, `/proc/<pid>/fd/*`
  special files, `chroot(2)`) — see research report §2.
- Inode-level aliasing: a file reachable through a path inside a writable root (e.g. a hard
  link that already exists there before launch) is accessible through that path even when
  the underlying inode's original path is outside the roots — both backends restrict by
  path hierarchy, not by inode (kernel docs: "files are identified and restricted by their
  hierarchy"). Post-launch creation of such links is denied (see §8.1 note).
- Normal unsandboxed tabs, SSH tabs, the initial tab, and any tab already running when the
  flag changes.
- Network traffic, environment variables, inherited credential sockets, host IPC, devices,
  process visibility/signals, and secure deletion of the ephemeral tree.

The guarantee is **filesystem-only and common across backends** (§5). Network, environment,
IPC, and process control are outside the boundary by contract, and no document or UI text may
claim otherwise.

---

## 3. Product and UX contract

### 3.1 One setting: `sandbox.enabled`

Exactly one setting is declared in `internal/settings/settings.go`:

| Field       | Value                                                                                                                                            |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| Key         | `sandbox.enabled`                                                                                                                                |
| Type        | `MustRegisterBool(BoolSpec{…})`                                                                                                                  |
| Section     | `Experimental`                                                                                                                                   |
| Label       | `Filesystem sandbox`                                                                                                                             |
| DataClass   | `PublicConfig`                                                                                                                                   |
| Control     | toggle (default `ControlToggle`)                                                                                                                 |
| Default     | `false`                                                                                                                                          |
| Description | States that the toggle only exposes an opt-in action for **new local tabs**; it does not sandbox existing tabs, and the feature is experimental. |

No sandbox mode, allowed-path, network, or global-default setting is introduced in V1.

The flag is a **capability/visibility gate**, not "sandbox every tab":

- Disabling it never changes or kills running tabs.
- Enabling it never changes an already running tab.
- The backend **rejects** a sandbox request while the flag is off (`-32010`), so UI and wire
  behavior agree even if the renderer is stale.

### 3.2 Quick Connect action

- New `QuickConnectItem` with stable id `__sandboxed_local__`, title `Sandboxed shell…`,
  detail `Choose a workspace and open a filesystem-isolated local tab`.
- Rendered only while `sandbox.enabled` is true (the provider reads the live setting).
- Existing `New tab`, `New connection…`, `+` button, initial tab, keyboard (`Cmd/Ctrl+T`), and
  SSH flows are untouched.
- Activation calls the new native `dialog.openDirectory` (sibling of `dialog.openFile`).
  Cancellation is a no-op (no tab, no error toast). Success opens a **new** local tab; an
  existing PTY is never retrofitted.

### 3.3 Tab metadata and marker

Every sandboxed tab carries immutable metadata `{backend, workspace, writableRoots}` where
`backend` is `landlock` or `seatbelt`, `workspace` is the canonical workspace, and
`writableRoots` is the realized list returned by the backend (workspace + detected Git common
dir + ephemeral runtime roots). The tab renders a lock/shield marker whose tooltip names the
backend and the writable roots.

`restoreDescriptor` is `null` for V1: a filesystem grant requires a fresh picker action after
restart. No restore record is persisted for sandboxed tabs.

### 3.4 Unavailable and failure surfaces

- Flag on + backend unavailable → Quick Connect renders a non-activatable `Sandbox
unavailable` row with the typed reason from `sandbox.status` (§4.2).
- Launch/setup failure → toast with the typed reason, and **no tab**.
- There is never an unsandboxed fallback for a request that asked for a sandbox.

---

## 4. Control-plane contract

### 4.1 `open` params

Extend `openParams` with optional:

```jsonc
"sandbox": { "workspace": "/abs/path" }   // optional; presence is the sole wire opt-in
```

Validation order in `handleOpen` (all failures are `-32602` "Invalid params"):

1. The request must be local (`kind` empty, `profileId` empty, `host` empty) — a sandboxed SSH
   request is invalid.
2. The feature flag `sandbox.enabled` must be true — otherwise `-32010`
   (`data.reason: "feature-disabled"`).
3. `workspace` must canonicalize (§6): `filepath.Abs` → `filepath.EvalSymlinks` → `os.Stat`,
   resulting in an existing absolute directory.

The canonicalized value is used in exactly three places, and the same value in all three:
`cmd.Dir`, the policy input, and the response metadata.

### 4.2 `sandbox.status`

New method `sandbox.status` → `{available, backend, reason?, detail?, abi?}`:

- `backend`: `landlock`, `seatbelt`, or `unsupported`.
- `reason` (stable values): `landlock-unavailable`, `landlock-abi-too-old`,
  `sandbox-exec-unavailable`, `probe-failed`, `unsupported-platform`.
- `abi`: the detected Landlock ABI when backend is `landlock` (Linux), absent otherwise.
- `detail`: human-readable backend status (e.g. sandbox-exec probe failure) for the
  unavailable row's tooltip.

### 4.3 `dialog.openDirectory`

New native dialog method as a sibling of `dialog.openFile`: same shape
(`{path}` result, `""` on cancel, `-32601` when unwired). New schema
`contracts/dialog.openDirectory.schema.json`, regenerated client type, `DialogService`
interface gains `OpenDirectory(ctx context.Context) (string, error)`.

### 4.4 Error codes

Reserve in the JSON-RPC error space (unused in the tree today, verified):

| Code     | Meaning                                                             | `data.reason`                 |
| -------- | ------------------------------------------------------------------- | ----------------------------- |
| `-32010` | feature disabled                                                    | `"feature-disabled"`          |
| `-32011` | backend unavailable (mirrors a `sandbox.status` reason)             | e.g. `"landlock-abi-too-old"` |
| `-32012` | setup failed (policy construction, helper handshake, native launch) | `"setup-failed"`              |

Logs on any of these paths contain backend/reason/ABI but **never filesystem paths**.

### 4.5 Successful `open` result

Optional addition to the `open` result:

```jsonc
"sandbox": { "backend": "landlock", "workspace": "/abs/…", "writableRoots": ["/abs/…", …] }
```

Ordinary local and SSH results omit `sandbox` entirely.

---

## 5. Common filesystem policy

### 5.1 The portable guarantee

The shell and its descendants may:

- read/execute the documented system/runtime roots (§5.5);
- read/write only the selected workspace plus explicitly surfaced runtime roots (§5.2);
- cannot open other host filesystem paths.

Explicitly outside V1's guarantee (never claimed): network, process visibility/signals,
environment variables, inherited credential sockets, IPC, devices, denial auditing, secure
deletion.

### 5.2 Writable roots

1. The canonical workspace (§6).
2. An optional canonical Git common directory detected from a worktree `.git` file (§5.4).
3. One mode-`0700` per-session runtime tree under
   `storage.Paths.CacheDir()/sandbox-sessions/<random>/` containing `home` and `tmp`
   (created by the backend before policy construction).

No arbitrary user-supplied extra roots exist in V1.

### 5.3 Ephemeral environment

- `HOME` and all XDG data/config/cache variables → the ephemeral `home`.
- `TMPDIR`, `TMP`, `TEMP` → the ephemeral `tmp`.
- nocx shell-integration files are installed into that home
  (`shellintegration.EnsureInstalled(home)`).
- `NOCX_SANDBOX=filesystem` is set; the remaining environment is retained (with the existing
  `scrubLauncherSession` and UTF-8 locale handling preserved).
- Best-effort recursive deletion of the runtime tree runs after child exit and after startup
  failure. Deletion is **not** secure erase (explicitly stated in docs and tooltips).

### 5.4 Git common directory (explicit compatibility exception)

- Parse only the selected root's regular, bounded `.git` file (a `gitdir: <path>` line).
  Accept only Git's canonical linked-worktree shape
  `<common>/.git/worktrees/<name>`, require its regular `gitdir` backlink to resolve exactly
  to the selected workspace's `.git` file, then add the parent common `.git` directory
  read-write and expose it in `writableRoots`. This reciprocal check prevents an
  attacker-authored workspace `.git` file from widening the cage to an arbitrary directory.
- Do **not** recursively search parent directories; do **not** shell out to Git. A malformed,
  symlinked, oversized, non-worktree, or non-reciprocal `.git` file yields no extra root (and
  is not an error).

### 5.5 Read-only roots

A documented, platform-specific set of existing OS/runtime roots (system dirs the shell needs:
Linux: `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/etc`, `/dev` (read only as required by
glibc), `/proc`, `/sys` for process/runtime introspection as documented per platform; macOS:
`/usr`, `/bin`, `/sbin`, `/System/Library`, `/Library/Developer/CommandLineTools`, `/etc`,
`/dev`, `/private/etc`), plus:

- the canonical shell executable (`filepath.EvalSymlinks` of the resolved shell path), and
- existing absolute directories from the inherited `PATH` (canonicalized; skipped when
  missing).

Missing optional roots are skipped; **permission and canonicalization errors are fatal**
(fail closed, `-32012`). The real user home, broad `/tmp`, and user config/credential
directories are never granted.

### 5.6 Composition rules

- Policy composition is allow-list-only and canonical-path-based.
- Reject: NUL bytes, empty strings, relative paths, nonexistent paths, a workspace that is
  not a directory, failed symlink resolution, duplicate conflicting permissions, and policy
  documents above a fixed size / root-count bound (implementation-defined constants, tested
  in slice 1).
- A read-write rule subsumes a read-only duplicate of the same canonical path.
- The backend returns the realized writable-root list for the tab tooltip (§3.3).

---

## 6. Request validation (canonicalization)

For the `workspace` value:

1. Require a non-empty absolute path with no NUL byte.
2. `filepath.Abs(workspace)` — normalize and reject on error.
3. `filepath.EvalSymlinks(abs)` — reject on error (missing path or broken symlink).
4. `os.Stat(canonical)` — must be a directory; reject otherwise.

The single canonical value is used as `cmd.Dir`, as policy input, and as response metadata.
Invalid or cancelled input is `-32602`. Cancellation is detected at the dialog layer and
results in no RPC error at all.

---

## 7. Module contract: `internal/sandbox`

### 7.1 Types and interface

Platform-neutral, in `internal/sandbox`:

- `Request{Workspace string}` — the sole renderer-supplied field. Transport performs
  `Abs → EvalSymlinks → Stat`, requires an existing directory, and passes the canonical value;
  `Service.Prepare` defensively revalidates it at the enforcement boundary. Policy construction
  remains exclusively inside `internal/sandbox`.
- `Status{Available bool; Backend string; Reason, Detail string; ABI int}`.
- `Policy` — the validated, canonical-path document (roots + metadata), serializable for the
  Linux helper FD handshake.
- `CommandSpec{Path string; Args []string; Dir string; Env []string; ExtraFiles []*os.File}` —
  the ordinary shell command exactly as `pty.NewLocal` builds it, including inherited
  descriptors such as the shell-integration lifecycle channel.
- `PreparedCommand` — owns the `*exec.Cmd`, the post-start readiness handshake, and
  idempotent cleanup (Close may be called once; subsequent calls are no-ops).
- `Service` interface:

```go
type Service interface {
    Status(ctx context.Context) Status
    Prepare(ctx context.Context, req Request, spec CommandSpec) (*PreparedCommand, error)
}
```

Platform constructors live behind `sandbox_linux.go`, `sandbox_darwin.go`, and an unsupported
stub (`sandbox_other.go`, `Status{Available:false, Reason:"unsupported-platform"}`; `Prepare`
returns a typed setup failure). The unsupported stub compiles on every target so the
composition root is uniform.

### 7.2 Request plumbing

`*sandbox.Request` is threaded through `openParams` → `session.Config` → `pty.Config`
(one new field each: `Sandbox *sandbox.Request`). SSH and ordinary local paths set it to nil.

### 7.3 `pty.NewLocal` refactor

`NewLocal` constructs the ordinary shell command first (shell detection, `exec.Command(shell,
"-i")`, `cmd.Dir`, env — unchanged), then:

- `cfg.Sandbox == nil` → current behavior, byte-for-byte (one PTY, resize/close/enhanced env
  unchanged).
- `cfg.Sandbox != nil` → `Service.Prepare(ctx, *cfg.Sandbox, spec)`; on success the
  `PreparedCommand` becomes the session's `*exec.Cmd`; on failure `NewLocal` returns the typed
  error (`-32012`) and **no session is registered**.

Preserved semantics: PTY byte stream, resize, close, enhanced-shell environment, and
one-PTY-per-tab (AD-7).

### 7.4 Composition

`internal/sandbox.Service` is injected only at `internal/app/app.go` (constructor param or
`Option`), held on `localPTYFactory`, and passed to `session.Reg`/`pty` via the request
plumbing. No package globals. The transport never renders policy — it validates the request
and maps errors to codes; enforcement is owned by the sandbox package.

---

## 8. Linux backend (Landlock)

### 8.1 ABI floor and cap

- Pin `github.com/landlock-lsm/go-landlock v0.9.0` directly in `go.mod` (research report §3
  has the tagged-source facts; license MIT, `go 1.24.0` baseline, compiles for
  `linux/amd64` + `darwin/arm64`).
- Require kernel Landlock ABI `>= 3`: below ABI 3 the kernel cannot deny truncation at all
  (kernel docs, "File truncation (ABI < 3)"), so an ABI-2 cage would be escapable by
  truncation. Unsupported or ABI < 3 kernels fail closed with
  `landlock-unavailable` / `landlock-abi-too-old` (`-32011`).
- Use the strict configuration matching `min(detectedABI, 8)` (i.e. `landlock.V8`-equivalent
  access set). Do **not** call `BestEffort()` (succeeds with no enforcement at all), do **not**
  call `RestrictNet` or `RestrictScoped` (network/scope restrictions are out of contract;
  ABI 9's `RESOLVE_UNIX` is filesystem-adjacent but the cap at ABI 8 keeps the declared
  filesystem-only contract stable on ABI 9+ kernels).

### 8.2 Helper process and FD protocol

- Re-exec `os.Executable()` as an internal helper before Wails startup
  (`__sandbox-landlock-exec` as argv[1] marker).
- Serialize the bounded policy (JSON, validated by the same parser as the parent) into an
  **unlinked** mode-`0600` file descriptor; append it to the ordinary command's inherited
  `ExtraFiles`.
- Append a second descriptor for the readiness/error pipe. The helper receives both child FD
  numbers as argv, so the protocol does not assume fixed fd 3/4 and never displaces inherited
  descriptors. The parent reads one byte: `0` = enforced, `1` = setup failure with the typed
  reason on the pipe; the read has a bounded timeout.

Helper sequence (no intermediate shell):

1. decode and validate the policy (same rejection rules as §5.6);
2. `PR_SET_NO_NEW_PRIVS`;
3. apply strict Landlock path restrictions (`RestrictPaths` with the strict config, RW
   subsuming RO per canonical path);
4. send success **only after** the restriction succeeds;
5. close policy/status descriptors;
6. remove helper-only environment variables; the parent already scoped the shell
   environment in `CommandSpec.Env` (including `NOCX_SANDBOX=filesystem`), and helper-internal
   markers never reach the shell;
7. `unix.Exec` the real shell.

On any error the helper reports the typed setup failure and exits `126`.

### 8.3 Readiness and failure path in `pty.NewLocal`

- The PTY is started with the helper as its child (`pty.StartWithSize`), so the shell's
  stdio descriptors exist **before** restriction; Landlock (like Seatbelt) restricts
  acquisition of new resources, not already-open descriptors — this is what makes an
  interactive PTY work inside the cage, and it is also why hard-link aliasing (§2) cannot
  be closed by either backend.
- Return a session only after the readiness handshake succeeds within a bounded timeout.
- On timeout/error: close the PTY, terminate and wait for the helper, run cleanup once
  (runtime tree), return `-32012`. No session registry entry leaks (matches `Reg.Open`
  ordering, §1).

---

## 9. macOS backend (Seatbelt)

### 9.1 Runtime probe

- Probe `/usr/bin/sandbox-exec` by executing `/usr/bin/true` under a minimal profile
  (`sandbox-exec -p '(version 1) (deny default) (allow default)' /usr/bin/true`); cache only a
  successful status for the app lifetime.
- Deprecation makes this explicitly experimental and runtime-gated: `sandbox-exec(1)` is
  marked DEPRECATED and App Sandbox (entitlement-based, whole-app) cannot supply per-tab
  policy; `sandbox_init(3)`'s named profiles are killed on SDK >= 27.0 (research report §5).

### 9.2 Profile rendering

- Render a fresh, deterministic SBPL profile from the common policy (§5) and launch
  `/usr/bin/sandbox-exec -p <profile> <shell> -i` without invoking another shell.
- Begin with `(version 1)` and `(deny default)`; allow process self/children, the required
  signal and Mach/IPC operations for an interactive process, and `(allow network*)` because
  network isolation is out of scope.
- Emit escaped literal/subpath clauses for only the common read-only/read-write roots and the
  PTY/device operations required for an interactive shell (character-device write/ioctl for
  the PTY).
- SBPL string escaping is centralized and rejects newline/NUL/control-character injection
  before rendering (tested in slice 3).
- Do not copy termic source (AGPL); the research report §1.4 compares behavior and cites it.

### 9.3 Fail-closed

A profile/render/probe failure is fail-closed: `-32011`/`-32012`, no tab, no unsandboxed
fallback.

### 9.4 Shipping gate

macOS Seatbelt is deprecated; V1 ships on macOS **only while the real release artefact
(`.app`) passes the platform smoke suite** (§11.2). Until that suite runs on a real packaged
app, all macOS enforcement claims are `[ASSUMPTION]`.

---

## 10. Invariants

| #   | Invariant                                                                                                                                                                                                                                                                                   |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| I1  | Default off: `sandbox.enabled` defaults to `false`; no other sandbox setting exists.                                                                                                                                                                                                        |
| I2  | Explicit new-tab opt-in: a sandboxed tab is created only via `Sandboxed shell…` + a native picker; presence of `sandbox` in `open` params is the sole wire opt-in.                                                                                                                          |
| I3  | Ordinary behavior unchanged: local/SSH/initial/`+`/keyboard tab flows are byte-for-byte unchanged; a nil `Sandbox` request never touches the sandbox path.                                                                                                                                  |
| I4  | Local-only: a sandbox request with `kind=ssh` is `-32602`.                                                                                                                                                                                                                                  |
| I5  | No fallback: a request that asked for a sandbox never launches unsandboxed (deliberate divergence from termic's fail-open behavior).                                                                                                                                                        |
| I6  | Common filesystem contract: both backends enforce the same writable/read-only root model (§5); no backend-specific extra access.                                                                                                                                                            |
| I7  | Canonical paths only: policy and `cmd.Dir` use the single canonicalized workspace; NUL/empty/relative/nonexistent/non-directory inputs are rejected.                                                                                                                                        |
| I8  | No renderer-built policy: the renderer supplies only `{workspace}`; policy construction and enforcement live in `internal/sandbox`.                                                                                                                                                         |
| I9  | Registration only after enforcement readiness: the backend session registry entry and output ring exist only after the readiness handshake succeeds. The frontend may hold one provisional tab while `open` is pending; any failure shows the typed toast and removes that tab immediately. |
| I10 | Child exit/close semantics unchanged: exit, close, and one-PTY-per-tab behavior are identical to ordinary tabs; cleanup runs once after child exit.                                                                                                                                         |
| I11 | Sandbox state always visible: every sandboxed tab carries the marker + tooltip (§3.3); `restoreDescriptor` is null.                                                                                                                                                                         |
| I12 | Private paths absent from logs: logs carry backend/reason/ABI, never filesystem paths.                                                                                                                                                                                                      |
| I13 | One composition root: `sandbox.Service` is injected only at `internal/app/app.go`.                                                                                                                                                                                                          |

---

## 11. Verification

### 11.1 Linux (ABI >= 3 runner, slice 2)

A sandboxed child must, inside each surfaced writable root: create, truncate, and rename
files; read system executables; **cannot** read or mutate a sentinel outside all roots; cannot
bypass with a symlink, with a subprocess, or with a renamed path; creating a hard link to a
file outside all roots is denied; and retains outbound TCP connectivity (contract: network
unrestricted). The suite also asserts the documented limitation, not a guarantee: a hard link
that already exists inside a writable root before launch remains reachable through that path
(§2, §8.3). Release additionally runs the same assertions by invoking the release-tagged
Linux binary, proving its early helper dispatch and self-re-exec path.

### 11.2 macOS (real packaged `.app`, slice 3)

The same common assertions against canonical temp fixtures, plus `sandbox-exec`
availability. The release gate invokes `build/bin/nocx.app/Contents/MacOS/nocx`; that
executable constructs the real Seatbelt service and launches an external probe inside the
cage, so a missing early dispatch, profile render, or packaged-binary runtime dependency
fails the release rather than being inferred from a test binary.

### 11.3 Regression (slice 6)

- Ordinary local and SSH tabs remain behaviorally unchanged (open/resize/close/exit).
- A failed sandbox setup creates no registered backend session/ring and leaves no frontend tab.
- Disabling the flag affects only future action visibility; running tabs are untouched.

### 11.4 Behavior matrix (also drives the implementation plan tests)

| Case                                             | Expectation                                                                                              |
| ------------------------------------------------ | -------------------------------------------------------------------------------------------------------- |
| flag off, ordinary `open`                        | unchanged, no `sandbox` in result                                                                        |
| flag off, `open` with `sandbox`                  | `-32010 feature-disabled`                                                                                |
| flag on, ordinary/SSH `open`                     | unchanged; SSH + `sandbox` → `-32602`                                                                    |
| flag on, `Sandboxed shell…` → cancel             | no-op, no tab, no error                                                                                  |
| flag on, picker → workspace                      | new sandboxed local tab, marker + tooltip, `sandbox` in result                                           |
| invalid/relative/nonexistent/symlinked workspace | `-32602`; symlink resolved to canonical target when it exists                                            |
| Valid reciprocal Git worktree `.git` present     | parent common `.git` added RW and listed in `writableRoots`; malformed/untrusted pointer → no extra root |
| Landlock unavailable / ABI < 3                   | `-32011` with stable reason; Quick Connect shows `Sandbox unavailable` row                               |
| helper timeout / exit 126                        | `-32012`, no session, PTY closed, helper waited, cleanup once                                            |
| Seatbelt missing / profile rejected              | `-32011`/`-32012`, no tab, no fallback                                                                   |
| close/exit of sandboxed tab                      | child exit semantics unchanged; runtime tree deleted best-effort once                                    |
| flag toggled live                                | only Quick Connect row visibility changes; running tabs untouched                                        |
| malicious SBPL/path input                        | escaping/rejection tests in slices 1 and 3; NUL/newline injection rejected                               |

---

## 12. Rollout order

1. `internal/sandbox` contracts + canonical policy builder + typed errors/status + stubs +
   pure policy tests (platform-independent).
2. Linux Landlock backend + helper + ABI probes + enforcement integration tests.
3. macOS SBPL renderer + probe/wrapper + injection tests + release-artefact smoke gate.
4. `pty`/`session`/transport plumbing + composition.
5. Frontend: `dialog.openDirectory`, generated client types, live flag, Quick Connect
   action/unavailable row, tab metadata/marker, user-visible errors.
6. Cross-platform regression, packaging, docs consistency, release gates.

Slices are independently verifiable and ordered so that each lands against a green tree; the
executable plan in `.internal/plans/2026-08-02-native-filesystem-sandbox-v1.md` owns the
task-level detail.
