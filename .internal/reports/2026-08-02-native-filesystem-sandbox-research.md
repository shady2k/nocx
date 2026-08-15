# Native filesystem sandbox — source-verified research

- **Date:** 2026-08-02
- **Status:** evidence ledger for `.internal/specs/2026-08-02-native-filesystem-sandbox-design.md`,
  ADR-0030, and `.internal/plans/2026-08-02-native-filesystem-sandbox-v1.md`
- **Tree inspected:** nocx working tree at 2026-08-02 (`module github.com/shady2k/nocx`, `go 1.26`,
  no `go-landlock` dependency yet)
- **Host measured:** Linux 7.1.4-1-MANJARO, x86_64, kernel with Landlock active (see §4.4)

Every external claim below was re-read at its pinned source on 2026-08-02. Claims that could
only be established by reasoning from code comments or by third-party reports are marked
`[ASSUMPTION]`. Seatbelt and Landlock are **not** presented as mechanically identical.

---

## 1. termic — the reference implementation, at its pinned commit

**Repository:** `simion/termic`, commit `a4d2d29d13ea5df5017f9f310babf42fe3278431`
(repo tree and every file citation fetched from `raw.githubusercontent.com/simion/termic/<SHA>/…`
on 2026-08-02). termic is a **Tauri 2 app (Rust backend, React/TS frontend)** that orchestrates
agent CLIs (`claude`/`codex`/`gemini`/`copilot`/`grok`/`agy`) in per-workspace PTYs. The
sandboxed unit is the **agent task PTY**, not a user shell tab and not the app.

### 1.1 License and the AGPL boundary

- `LICENSE` (repo root, 34,523 bytes) is the standard GNU AGPL v3 text ("GNU AFFERO GENERAL
  PUBLIC LICENSE / Version 3, 19 November 2007").
- `README.md` line 10 carries the AGPL badge; lines 391–396: "License — AGPL-3.0-or-later.
  Fork it, modify it, build a derivative — the only string is that derivatives stay AGPL too."
- **Boundary for nocx:** nocx reuses _architectural lessons and behavior_, never AGPL code.
  Anything that is a direct code candidate (argv construction, SBPL renderer/escaping, proxy
  HTTP parsing) is on the reject side of §6 and must be re-implemented from the common
  contract, not translated.

### 1.2 Shipped macOS behavior

- `src-tauri/src/sandbox.rs` `wrap_command()` (~L1725) rewrites the spawn to
  `sandbox-exec -f <profile_path> env <vars…> <original_cmd> <args…>`; profile is written per
  spawn to `std::env::temp_dir()/termic-sandbox-<task.id>.sb` (`provision()`, ~L1652) and
  passed with `-f` (file), not `-p` (inline).
- Header comment in `sandbox.rs`: "⚠ sandbox-exec is Apple-deprecated. The binary still works
  on macOS 15 and there's no replacement on the horizon…" — the deprecation is acknowledged in
  termic's own source. macOS minimum 12.0.

### 1.3 Per-task scope

- `docs/sandbox.md`, "Scope": "ONLY the agent CLI's PTY is sandboxed. AuxTerminal, setup
  script, run script, and archive script run unsandboxed by design."
- `lib.rs` `Task.sandbox_enabled` (~L295): "PINNED at task creation. The sandbox decision can't
  change afterwards — otherwise an agent could talk the user into loosening its own cage."
  Editing via `task_set_sandbox` (~L4452) persists AND SIGKILLs every live PTY of the task.
- Policy is generated **per spawn**: `provision()` → `render_profile()` (~L926) →
  `render_profile_with()` (~L942).

### 1.4 Generated Seatbelt (SBPL) profile

- Header constant `SBPL_HEADER` (~L1817): `;; termic sandbox profile - generated; do not edit.`
  / `(version 1)` / `(deny default)` — deny-by-default base.
- Process/IPC essentials: `(allow process-exec)`, `(allow process-fork)`,
  `(allow signal (target self))`, `(allow signal (target children))`,
  `(allow mach-priv-task-port)`, `(allow mach-lookup)`, `(allow sysctl*)`, plus PTY device
  clauses `(allow file-write-data (vnode-type CHARACTER-DEVICE))` and
  `(allow file-ioctl (vnode-type CHARACTER-DEVICE))`.
- Pure **allow-list** filesystem model: `system_read_roots()` (~L379) emits
  `(allow file-read* (subpath "<root>"))` for `/usr`, `/opt`, `/bin`, `/sbin`, `/dev`,
  `/private/etc`, `/etc`, `/System/Library`, `/System/Volumes/Preboot/Cryptexes`,
  `/private/var/db`, `/Library/Developer/CommandLineTools`, `/lib*`, `/proc`, `/sys`, `/run`;
  the root node as `(allow file-read* (literal "/"))`. Writable: task path + composition
  members + worktree parent `.git/` (via `parent_git_dir_for_worktree()`, ~L1948, which parses
  the `.git` file's `gitdir:` line — the same mechanism this project's common policy reuses in
  §5 of the design spec) + `builtin_runtime_paths()` (~L1335).
- A code comment (~L1467) states the old hard-deny set (`~/.ssh`, `~/.aws`, browser data,
  shell histories, …) was **removed**; those paths are denied only by absence from the
  allow-list. The older "ALWAYS denied" wording in `docs/sandbox.md` is behaviorally true but
  describes the older mechanism.
- SBPL is last-match-wins; the renderer appends final `(deny …)` clauses for the app data dir
  and the control-plane socket.

### 1.5 HTTP CONNECT proxy (network isolation)

- `src-tauri/src/proxy.rs`: `start()` (~L221) binds `127.0.0.1:0` per sandboxed PTY;
  `handle_connect()` (~L338) tunnels CONNECT; `handle_plain_http()` (~L443) forwards
  absolute-form HTTP; `host_allowed()` (~L537) matches a compiled regex allowlist and answers
  403 with an `X-Termic-Sandbox: blocked-by-allowlist` header otherwise.
- Wired into the child by `wrap_command()` **env vars** (~L1758-1770):
  `http_proxy`/`https_proxy`/`HTTP_PROXY`/`HTTPS_PROXY` = `http://127.0.0.1:<port>`,
  `no_proxy`/`NO_PROXY` = `localhost,127.0.0.1,::1`, plus `TERMIC_SANDBOX=1`,
  `TERMIC_SANDBOX_MODE`, `TERMIC_SANDBOX_HELP`.
- Modes: `Off` / `Monitor` (allow + report) / `Enforce` (full cage + proxy) / `EnforceFs`
  (filesystem cage, `(allow network*)`, **no proxy**). Network containment is thus opt-out per
  task in termic; nocx has no network policy at all (design spec §2.1).

### 1.6 Runtime availability probe

- `sandbox.rs::available()` (~L1477): `cfg!(target_os = "macos") && std::path::Path::new("/usr/bin/sandbox-exec").exists()`.
  This is an **existence check only** — no trial execution of `/usr/bin/true` under a profile,
  no exit-code probing. Exposed to the renderer as the Tauri command `sandbox_available`
  (`lib.rs` ~L4024); `provision()` re-checks and errors as defense in depth.
- nocx's design deliberately **strengthens this**: the macOS backend executes `/usr/bin/true`
  under a minimal profile (§9 of the design spec). That is an adapt, not a borrow.

### 1.7 UX

- Enablement: a per-task Shield toggle (New Task dialog pre-checks it when the project default
  is set), a per-task editor (`TaskSandboxDialog.tsx`) with mode selector and allowed-path /
  allowed-host editors, and Settings → Repositories project defaults. `Project.default_sandbox`
  (~L78) defaults to **false**; the main-checkout quick path is UNCAGED unless explicitly
  opted in (lib.rs ~L2709-2717).
- termic's unit is the **task/workspace**, and its settings model is global/project + per-task
  with user-editable path lists. nocx's per-tab action + single feature flag is an adaptation,
  not a termic concept (design spec §3).

### 1.8 Fail-closed behavior — termic is FAIL-OPEN (plan hypothesis corrected)

- `lib.rs` `pty_spawn` (~L1803), sandbox branch (~L1839-1854):
  `match sandbox::provision(…) { Ok(bundle) => wrap_command(…), Err(e) => { … spawning
unsandboxed: {e} … } }` — on any provisioning/render/write error the spawn proceeds
  **unsandboxed** with the original command.
- `SandboxStatus` (~L1630): "False for an unsandboxed task AND for the degraded case where
  provisioning failed (we proceed unsandboxed rather than crash)."
- Partial degradation: a proxy failure keeps the filesystem cage and drops network
  (`SandboxStatus.warning`), rather than failing the spawn.
- **Consequence:** nocx's fail-closed launch is a _deliberate divergence_ from termic, not a
  borrow. It is motivated by the different trust model: nocx sandboxes interactive user shells,
  where a silently-unsandboxed tab would be indistinguishable from a genuinely caged one
  (design spec §10, invariant I5).

### 1.9 Docker design is proposal-only

- `docs/plans/docker-sandbox/README.md`: "Docker sandbox (research bundle)" — "Status:
  **proposed, not started.**" Contents: `design.md`, `findings.md` (experiments of 2026-06-25),
  `Dockerfile`.
- `docs/plans/docker-sandbox/design.md`: "Status: proposed, not started. Opt-in,
  experimental." Planned-but-unshipped: global `docker_sandbox_enabled` toggle, per-workspace
  Seatbelt-vs-Docker exclusivity, `docker run` command preview, `src-tauri/src/docker.rs`
  module sketch.
- `README.md` roadmap (~L345-355) lists "Docker-based sandboxing" as future work and
  "Sandbox parity on Linux + Windows. macOS Seatbelt today; bubblewrap / landlock on Linux and
  AppContainer on Windows are the gap" as roadmap.
- **No shipped Linux sandbox exists in termic at this commit**: `available()` is the only
  `target_os` check in `sandbox.rs`; README ~L78-81 / ~L177-179 / ~L299-301 state the Shield
  toggle is disabled on Linux/Windows and agents run unsandboxed.

### 1.10 termic facts at a glance

| Fact                                                            | Evidence                                                         | Confidence |
| --------------------------------------------------------------- | ---------------------------------------------------------------- | ---------- |
| AGPL-3.0 (v3 text; README "AGPL-3.0-or-later")                  | `LICENSE`, `README.md:10,391-396`                                | verified   |
| macOS-only `sandbox-exec -f <tmp .sb>` per spawn                | `sandbox.rs` `provision()` ~L1652, `wrap_command()` ~L1725       | verified   |
| Per-task (agent PTY) scope, pinned at creation                  | `docs/sandbox.md` Scope; `lib.rs` ~L295                          | verified   |
| Generated SBPL: `(version 1)` / `(deny default)` allow-list     | `sandbox.rs` `SBPL_HEADER` ~L1817, `render_profile_with()` ~L942 | verified   |
| Worktree `.git` `gitdir:` parsing for RW common dir             | `sandbox.rs` `parent_git_dir_for_worktree()` ~L1948              | verified   |
| In-process CONNECT proxy, env-injected, host allowlist          | `proxy.rs` L221/L338/L443/L537; `sandbox.rs` ~L1758-1770         | verified   |
| Availability = `cfg!(macos) && exists("/usr/bin/sandbox-exec")` | `sandbox.rs::available()` ~L1477                                 | verified   |
| Default OFF; per-task opt-in; edit SIGKILLs live PTYs           | `lib.rs` ~L2285/L2465, `task_set_sandbox` ~L4452                 | verified   |
| **Fail-open** on provision failure (unsandboxed spawn)          | `lib.rs` ~L1839-1854, `SandboxStatus` ~L1630                     | verified   |
| Docker sandbox proposal-only                                    | `docs/plans/docker-sandbox/README.md`, `design.md`               | verified   |
| No Linux sandbox shipped                                        | `sandbox.rs`; `README.md` ~L78-81                                | verified   |

---

## 2. Official Linux Landlock facts

Source: `Documentation/userspace-api/landlock.rst` in the Linux kernel tree (torvalds/linux
`master`, dated June 2026), fetched at
`https://raw.githubusercontent.com/torvalds/linux/master/Documentation/userspace-api/landlock.rst`
on 2026-08-02. Canonical rendered form (current kernel release docs):
`https://docs.kernel.org/userspace-api/landlock.html`. Section references are to that file.

- **Unprivileged, stackable LSM.** "Because Landlock is a stackable LSM, it makes it possible
  to create safe security sandboxes as new security layers in addition to the existing
  system-wide access-controls… Landlock empowers any process, including unprivileged ones, to
  securely restrict themselves." (§introduction)
- **ABI query.** `abi = landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);`
  — negative with `ENOSYS` means "not supported", `EOPNOTSUPP` means "currently disabled"
  (§"Landlock ABI versions"). This is the query go-landlock wraps as
  `syscall.LandlockGetABIVersion()` (verified in the tagged source, §3).
- **`no_new_privs`.** "The next step is to restrict the current thread from gaining more
  privileges (e.g. through a SUID binary): `prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)`" must
  precede `landlock_restrict_self()` (§"Defining and enforcing a security policy").
- **Inherited restrictions.** "If the `landlock_restrict_self` system call succeeds, the
  current thread is now restricted and this policy will be enforced on all its subsequently
  created children as well. Once a thread is landlocked, there is no way to remove its security
  policy; only adding more restrictions is allowed." (§"Inheritance"; clone(2) inherits the
  domain.) Sibling threads are **not** restricted without the TSYNC flag (§"Thread
  synchronization (ABI < 8)").
- **ABI 3 — truncation coverage.** "File truncation could not be denied before the third
  Landlock ABI, so it is always allowed when using a kernel that only supports the first or
  second ABI. Starting with the Landlock ABI version 3, it is now possible to securely control
  truncation thanks to the new `LANDLOCK_ACCESS_FS_TRUNCATE` access right."
  (§"Previous limitations — File truncation (ABI < 3)"). This is the justification for the
  ABI 3 floor in the design spec (§8.1). Two kernel-side nuances are recorded there:
  `creat(2)` on an existing name also needs the truncate right, and on some filesystems
  `fallocate(FALLOC_FL_COLLAPSE_RANGE)` can shorten a file opened for writing without the
  truncate right (§"Truncating files") — an acknowledged kernel limitation, outside the
  guarantee.
- **ABI 8 — thread synchronization.** "Starting with the Landlock ABI version 8, it is now
  possible to enforce Landlock rulesets across all threads of the calling process using the
  `LANDLOCK_RESTRICT_SELF_TSYNC` flag." (§"Previous limitations — Thread synchronization
  (ABI < 8)")
- **ABI 9 — pathname UNIX sockets.** "Starting with the Landlock ABI version 9, it is possible
  to restrict connections to pathname UNIX domain sockets using the new
  `LANDLOCK_ACCESS_FS_RESOLVE_UNIX` right." (§"Previous limitations — Pathname UNIX sockets
  (ABI < 9)") — the newest filesystem access right; capping the strict config at ABI 8 keeps
  the filesystem-only, network/scope-unrestricted contract stable on ABI 9+ kernels.
- **Other ABI additions:** 2 = `REFER` (rename/link across directories); 4 = TCP
  `bind`/`connect`; 5 = `IOCTL_DEV`; 6 = `SCOPE_*` (abstract UNIX sockets, signals); 7 = audit
  logging flags; 10 = UDP rights and per-rule quiet logging (newer than go-landlock v0.9.0
  knows).
- **Kernel support conditions.** `CONFIG_SECURITY_LANDLOCK=y` and Landlock in `CONFIG_LSM`
  (or `lsm=landlock,…` on the boot command line); Landlock first appeared in Linux 5.13
  (§"Kernel support"). Availability is therefore a runtime probe, not a build tag.
- **Errata.** `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_ERRATA)` returns a
  bitmask of fixed errata. go-landlock consults erratum 2 (signal-scoping bug) and downgrades
  a detected ABI ≥ 6 to 5 when the fix is absent (§3). The kernel docs warn most applications
  should NOT check errata (§"Landlock errata").
- **Known limitations relevant to the threat model:** `chroot(2)` is not denied; files
  reachable only via `/proc/<pid>/fd/*` and nsfs cannot be explicitly restricted (ptrace
  domain rules apply instead); at most 16 stacked ruleset layers (E2BIG beyond); Landlock
  denies `mount(2)`/`pivot_root(2)` (no topology changes). Rules are keyed to **file
  hierarchies, not inodes** — a hard link that already exists inside an allowed hierarchy
  to a file outside it remains reachable through that path [INFERENCE from "files are
  identified and restricted by their hierarchy", §"Layers of file path access rights"];
  post-launch creation of such links is governed by `REFER` (ABI 2+). The design spec
  records this as an explicit exclusion (§2), not a guarantee.

---

## 3. `go-landlock` v0.9.0 — facts from the tagged source

Source: module cache of `github.com/landlock-lsm/go-landlock@v0.9.0` (verified tag resolved by
the Go module proxy on 2026-08-02; `go.mod` declares `go 1.24.0`, MIT license, dependencies
`golang.org/x/sys v0.40.0` and `kernel.org/pub/linux/libs/security/libcap/psx v1.2.77`).
All paths below are relative to the module root.

- **Strict `RestrictPaths`.** `landlock/config.go` `RestrictPaths(rules …Rule) error`:
  "Restricts all goroutines to only 'see' the files provided as inputs… RestrictPaths returns
  an error if any of the given paths does not denote an actual directory or file, or if
  Landlock can't be enforced using the desired ABI version constraints." It also sets the
  "no new privileges" flag for all OS threads. `BestEffort()` is explicitly documented to
  "succeed without error even when Landlock is not available at all" — the design spec
  therefore forbids `BestEffort` (§8.2).
- **ABI-versioned configurations.** Package-level presets `landlock.V1 … landlock.V9`
  ("restrict the full set of access rights available at this Landlock ABI version"), where
  `V8` restricts the same operations as `V7` and differs only in using TSYNC-backed
  multithreaded enforcement, and `V9` adds `RESOLVE_UNIX` (pathname UNIX sockets). The
  strict configuration in the design spec is `min(detectedABI, 8)` from `landlock.V8`.
- **Path rule types.** `RODirs`/`RWDirs` (directory + file access), `ROFiles`/`RWFiles`
  (file-only), `PathAccess(accessFS, paths…)`, and `FSRule` methods `WithRefer` (ABI 2+),
  `WithIoctlDev` (ABI 5+), `WithResolveUnix` (ABI 9+). RW subsumes RO per rule, and
  `RWDirs` grants the full read/write/make/remove set within the config.
- **All-thread handling.** `landlock/restrict.go`: `useTsync := abi.version >= 8`; below ABI 8
  the thread is pinned with `runtime.LockOSThread()` and `PR_SET_NO_NEW_PRIVS` +
  `landlock_restrict_self` are applied on that thread; at ABI 8+ the
  `LANDLOCK_RESTRICT_SELF_TSYNC` flag is used. Enforcement is inside `restrict()`; the public
  methods `RestrictPaths`/`RestrictNet`/`RestrictScoped`/`Restrict` all funnel into it.
- **ABI detection.** `landlock/internal/abi.go` `DetectedABIVersion()`: returns 0 when the
  syscall fails (kernel without Landlock), and applies the errata downgrade (detected ≥ 6
  with signal-scoping erratum unfixed → 5). `landlock/syscall.LandlockGetABIVersion()`
  performs the raw `LANDLOCK_CREATE_RULESET_VERSION` query (same as `cmd/landlock-abi-version`).
- **Non-Linux build-tag stubs.** `landlock/path_opt_nonlinux.go` (`//go:build !linux`) makes
  `FSRule.addToRuleset` return `errors.New("Landlock is only supported on Linux")`;
  `landlock/restrict_nonlinux.go` makes `restrict()` return the same kind of error unless
  `bestEffort` (which would silently do nothing — another reason BestEffort is banned). The
  package therefore compiles on darwin but is inert.
- **Compilation evidence (recorded 2026-08-02):** a scratch module importing
  `github.com/landlock-lsm/go-landlock/landlock` at v0.9.0 compiled for `linux/amd64` and
  `darwin/arm64` on this host (`GOOS=linux GOARCH=amd64 go build` and `GOOS=darwin GOARCH=arm64
go build`, both `BUILD-OK`). The built linux binary's `LandlockGetABIVersion()` reported
  `abi: 9` when executed. This is dependency/build-tag evidence, not enforcement evidence.
- **Deliberately not used:** `BestEffort()`, `RestrictNet`, `RestrictScoped` — the V1 contract
  is filesystem-only (design spec §8.2).

---

## 4. Observed nocx callsites and constraints

All symbols below were re-read in the working tree on 2026-08-02 at the cited paths.

### 4.1 Local PTY boundary

- `internal/transport/ws.go:581` `openParams` — `{cols, rows, xpixel, ypixel, enhanced, kind,
profileId, host, user}`. No sandbox field yet; the design spec adds optional
  `sandbox?: {workspace}` (§4.1).
- `internal/transport/ws.go:805` `handleOpen` — validates cols/rows (`-32602`), builds
  `session.Config{Kind: KindLocal, Cols…, Enhanced}`, branches to SSH paths, calls
  `s.registry.Open` (line 926), registers the ring, and returns `{sessionId, cwd}` (lines
  973-982) before starting the output pump.
- `internal/session/session.go:30` `Config{Kind, Cwd, Host, Local *pty.Config, Remote,
Cols, Rows, XPixel, YPixel, Enhanced, ProfileID, CredentialID}`; `PTYFactory.NewPTY(ctx,
pty.Config)` (line 47); `Reg.Open` (line 159) creates the PTY **before** minting the ID and
  registering — a failed `NewPTY` returns without a registry entry, which is the invariant the
  sandbox setup-failure path must preserve.
- `internal/pty/pty.go:25` `pty.Config{Command, Args, Env, Cwd, Cols, Rows, XPixel, YPixel,
Enhanced}` and `WithExtraEnv`.
- `internal/pty/pty_local.go:62` `NewLocal` — shell detection (`$SHELL` → NixOS path →
  `/bin/bash` → `/usr/bin/bash` → `/usr/local/bin/bash` → `/bin/sh`), `exec.Command(shell,
"-i")`, `cmd.Dir = resolveCwd(cfg.Cwd)`, env =
  `withUTF8Locale(scrubLauncherSession(os.Environ()) + TERM/COLORTERM) + cfg.Env`, then
  `pty.StartWithSize`. The sandbox refactor constructs this ordinary command first and calls
  `Service.Prepare` only when a request is present (design spec §7.3).
- `internal/app/app.go` composition root: `localPTYFactory` (line ~91, `NewPTY` wraps
  `pty.NewLocal`), `session.New`, `storage.NewAppPaths()`, `settings.New(docStore, v)`,
  `SetDialogService` (line 37). `sandbox.Service` is injected here and nowhere else
  (design spec §7.4).

### 4.2 Native dialogs

- `internal/transport/ws_dialog.go` — `DialogService.OpenFile(ctx) (string, error)`;
  `dialog.openFile` returns `{path}` and `-32601` when no dialog service is wired (dev-web
  harness). `SetDialogService` is mutex-guarded and set post-construction from Wails startup.
  `dialog.openDirectory` is designed as its sibling (design spec §4.3).
- `contracts/dialog.openFile.schema.json` → generated `frontend/src/generated/dialog.openFile.ts`
  ("GENERATED FILE — do not edit… Regenerate: cd frontend && npm run contracts"). The
  renderer-side contract for the new method is a new schema file, not a hand-written type.

### 4.3 Settings

- `internal/settings/settings.go` — typed declarations via `MustRegisterBool(BoolSpec{…})`
  auto-register into `allDecls`; `DataClass` (`PublicConfig`, …) and `ControlKind`
  (`ControlToggle`, …) drive the generated UI; `Registry.GetBool`/`SetBool`/`GetSnapshot`;
  `SetNotifier` + transport `broadcastSettingsChanged` (`ws.go`) push live updates to every
  connected client.
- `frontend/src/settings.tsx:81` `Declaration{key, section, label, description, control,
dataClass, default, options, min, max}` — the UI renders from `settings.describe` with no
  hand-maintained list; one new `MustRegisterBool` costs zero frontend changes. The single
  consumer that must name the key (the Quick Connect provider) does so in the composition
  root, matching the documented pattern in `frontend/src/main.tsx` ("a CONSUMER has to name
  what it consumes").

### 4.4 Frontend tab / open lifecycle

- `frontend/src/tabs.ts` — `Tab` + `TabManager`; `newTab()` (line 403) builds
  `TerminalContent` with `restoreDescriptor: {type: 'local'}` (lines 419-423); `newSSHTab`
  uses `{type: 'ssh', profileId, host, user}`.
- `frontend/src/terminal-content.ts` (lines 455-456) — local tabs call
  `this.client.openSession(cols, rows, true)`; `frontend/src/ipc.ts:324` `openSession`
  issues the `open` RPC and resolves `{sessionId, cwd}`. Tooltip/title are pushed through
  `onTooltipChange` — the seam for the lock/shield marker.
- `frontend/src/quick-connect.tsx` — `QuickConnectItem{id, label, detail, system, run}`,
  `QuickConnectProvider{id, label, getItems}`, `ActionsQuickConnectProvider` ('actions';
  items `__local__`, `__new_connection__`), `QuickConnectController` (line 390). The new
  `__sandboxed_local__` action slots in here.

### 4.5 Storage, shell integration, release profile

- `internal/storage/paths.go` — `Paths{ConfigDir, DataDir, CacheDir}` via `NewAppPaths()`
  (build-tag-selected `AppDirName`: `nocx` for release, `nocx-dev` otherwise). The per-session
  runtime tree lives under `CacheDir()/sandbox-sessions/<random>/` (design spec §5.2).
- `internal/shellintegration/shellintegration.go` — `ShellIntegration.EnsureInstalled(home)`
  installs the OSC 7/133 hooks; the sandboxed ephemeral home reuses it (design spec §5.3).
- e2e native-integration convention: `e2e/vault.spec.ts` drives the real frontend against
  `cmd/devharness` and skips the gnome-keyring case only when the daemon is unavailable, with
  the reason marked in the test name; `e2e/home-boundary-live.spec.ts` skips only when
  `DEVHARNESS_BIN` is absent. This is the "explicit native-integration convention" the
  platform smoke suites must follow (plan §6): a gated run is skipped loudly with a named
  reason, never silently.
- JSON-RPC error conventions: `newJSONRPCError` / `rpcErrorFor` / `newVaultError` with
  `error.data.reason` (e.g. `vault-sealed`); the vault domain already uses `-32000`
  (`ws_vault.go:71-76`). Codes `-32010`, `-32011`, `-32012` are unused in the tree
  (verified by search) and are reserved by the design spec (§4.4).
- `go.mod`: `go 1.26`; current direct deps listed. `github.com/landlock-lsm/go-landlock
v0.9.0` will be added as a direct dependency by the implementation plan (slice 2), not by
  this documentation set.

### 4.6 Host measurement (recorded separately from the product minimum)

| Probe                                                                           | Result                                                              | Purpose                                         |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------- | ----------------------------------------------- |
| `uname -r` / `/sys/kernel/security/lsm`                                         | `7.1.4-1-MANJARO`; `capability,landlock,lockdown,yama,bpf,apparmor` | Landlock enabled in this kernel's LSM stack     |
| Raw C probe `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)` | **ABI 9**                                                           | Real kernel capability on this host, 2026-08-02 |
| go-landlock v0.9.0 `syscall.LandlockGetABIVersion()` (built binary)             | **ABI 9**                                                           | Library-observed ABI on this host               |
| Scratch cross-compile                                                           | linux/amd64 + darwin/arm64 both build                               | go-landlock build-tag/dependency evidence only  |

The product minimum is **ABI 3** (design spec §8.1); the host happens to run ABI 9. The two
numbers are not the same fact and must not be conflated in later documents.

---

## 5. Official Apple facts

Sources (man page content as shipped in current Xcode SDKs, mirrored at
`https://keith.github.io/xcode-man-pages/` on 2026-08-02; Apple's App Sandbox documentation at
`https://developer.apple.com/documentation/security/app-sandbox`):

- **`sandbox-exec(1)` — "execute within a sandbox (DEPRECATED)".** Synopsis
  `sandbox-exec [-f profile-file] [-n profile-name] [-p profile-string] [-D key=value …]
command [arguments …]`. The man page directs: "Developers who wish to sandbox an app should
  instead adopt the App Sandbox feature described in the App Sandbox Design Guide." The binary
  is still shipped on supported macOS releases [ASSUMPTION — the man page remains in current
  SDKs and termic ships against it with a macOS 12.0 floor; the design spec's runtime probe
  (§9.1) makes the actual check at launch, so this claim is belt-and-braces rather than load-
  bearing].
- **`sandbox_init(3)` — DEPRECATED.** The C API is deprecated with the same App Sandbox
  guidance. New and load-bearing: "These profiles must not be used when building against macOS
  SDK >= 27.0. Processes opting into them will be killed on attempt to do so." — the named
  `kSBXProfile*` profiles are being removed; the raw profile mechanism (which `sandbox-exec -p`
  uses) remains but is deprecated.
- **`sandbox(7)`** — the facility is "voluntary" restriction; "New processes inherit the
  sandbox of their parent"; "Restrictions are generally enforced upon acquisition of operating
  system resources only" (an already-open file descriptor keeps working after sandboxing).
- **App Sandbox is entitlement-based and whole-app.** "App Sandbox provides protection to
  system resources and user data by limiting your app's access to resources requested through
  entitlements" (`com.apple.security.app-sandbox` plus capability entitlements such as
  `com.apple.security.files.user-selected.read-write`). Entitlements are baked into the app's
  code signature at build time; there is **no per-child/per-tab policy API** — a sandboxed app
  cannot grant a child process a narrower or wider cage, which is exactly what per-tab sandbox
  needs. Hence: App Sandbox cannot supply the V1 mechanism.
- **No supported Seatbelt/SBPL replacement API.** `sandbox-exec` and `sandbox_init` are the
  only public entry points into the profile language, both deprecated; Apple's supported path
  is entitlement-based App Sandbox. There is no public, supported API for building per-process
  SBPL profiles. V1 therefore uses the deprecated `sandbox-exec -p` path, runtime-probed and
  release-smoke-gated (design spec §9.4).

---

## 6. Borrow / adapt / reject

| #   | Item                                                     | Verdict                           | Rationale / evidence                                                                                                                                                                                                                                                     |
| --- | -------------------------------------------------------- | --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | Per-session (per-spawn) policy generation                | **Borrow** (behavior)             | termic `provision()`/`render_profile()` regenerate the profile per spawn; nocx does the same per `open` with a sandbox request                                                                                                                                           |
| 2   | Allow-list filesystem model                              | **Borrow** (behavior)             | termic's pure allow-list (deny-by-absence, no hard-deny set) is exactly the common policy in design spec §5; `system_read_roots()` shape maps to the read-only root set                                                                                                  |
| 3   | Startup availability reporting to the UI                 | **Borrow** (behavior, strengthen) | termic's `sandbox_available` gates the toggle; nocx adds `sandbox.status` with typed reasons and a functional probe (termic only `Path::exists()`)                                                                                                                       |
| 4   | Security-visible tab state (Shield)                      | **Borrow** (behavior)             | termic's Shield/toolbar indicator → nocx lock/shield marker with backend + writable roots tooltip (design spec §3.3)                                                                                                                                                     |
| 5   | Fail-closed launch                                       | **Divergence, not borrow**        | termic is fail-open on provisioning errors (`lib.rs` ~L1839-1854); nocx deliberately fails closed (design spec §10 I5)                                                                                                                                                   |
| 6   | Worktree `.git` `gitdir:` parsing for the Git common dir | **Borrow, then harden**           | termic `parent_git_dir_for_worktree()` ~L1948 supplies the path-shape idea; nocx additionally bounds metadata reads, rejects symlinked pointers, and requires the target's reciprocal `gitdir` backlink so a writable workspace cannot grant an arbitrary host directory |
| 7   | Native backends behind one Go interface                  | **Adapt**                         | termic has no interface — `sandbox.rs` is macOS-specific and callers branch; nocx's `internal/sandbox.Service` (design spec §7.1) is the single seam                                                                                                                     |
| 8   | Typed settings / RPC boundaries                          | **Adapt**                         | termic's global/project + per-task JSON config model is rejected (design spec §3.1, §10 I1); nocx uses its typed settings registry (`MustRegisterBool`) and `data.reason` error convention                                                                               |
| 9   | Network isolation (CONNECT proxy)                        | **Reject**                        | out of scope — V1 guarantees filesystem only; the contract explicitly leaves network unrestricted (design spec §2, §5.1)                                                                                                                                                 |
| 10  | Global/project settings model, user-editable path lists  | **Reject**                        | conflicts with the single default-off flag + picker-driven workspace (design spec §3.1); user-supplied extra roots are not in V1 (§5.2)                                                                                                                                  |
| 11  | Proposed Docker/OCI path                                 | **Reject**                        | `docs/plans/docker-sandbox/*` is proposal-only in termic and container runtime is not selected for nocx (ADR-0030 "Alternatives")                                                                                                                                        |

AGPL note: borrowed items are behavior-level only. The SBPL renderer/escaping, `wrap_command`
argv construction, and proxy HTTP parsing are direct code candidates and are rejected for
copying (§1.1).

---

## 7. Unsupported / [ASSUMPTION] claims

- `sandbox-exec` remains present on the _latest_ macOS release — not executed on this host
  (Linux). Man page still ships in current SDKs; termic ships against it (macOS 12.0 floor,
  "works on macOS 15" in its source comment). The runtime probe in design spec §9.1 is the
  authoritative check.
- macOS Seatbelt profile render behavior under `sandbox-exec -p` with an interactive PTY shell
  — not executed on this host. Design spec §9 marks the platform smoke suite as the gate and
  the enforcement claims as `[ASSUMPTION]` until it runs on a real packaged `.app`.
- `go-landlock` TSYNC/LockOSThread runtime behavior on this host — the scratch binary only
  queried the ABI; enforcement integration is planned (implementation plan slice 2), not run.
- Exact line numbers in termic cited with "~" are pinned from raw-file greps at the SHA and may
  drift by a few lines; file/symbol names are exact.
