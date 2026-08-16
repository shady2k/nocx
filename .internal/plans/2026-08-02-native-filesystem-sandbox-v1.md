# Native filesystem sandbox V1 — implementation plan

> **For agentic workers:** execute this plan slice by slice; each slice is independently
> verifiable and lands against a green tree. Steps within slices use `- [ ]` checkboxes.

> **Amended 2026-08-16:** ADR-0034/0035 and their accepted design specs supersede this
> plan's workspace-only DTO and generic `Sandboxed shell…` target. The implemented action
> confirms writable grants, authenticates the private in-runtime bootstrap, and then launches
> fixed backend-resolved `opencode`. The original slice ledger below remains historical.

**Goal:** Ship the opt-in, experimental, filesystem-only, per-tab native sandbox designed in
`.internal/specs/2026-08-02-native-filesystem-sandbox-design.md` — `sandbox.enabled` flag,
`Sandboxed shell…` Quick Connect action + native workspace picker, `internal/sandbox` module
with a Linux Landlock backend and a macOS Seatbelt backend behind one `Service` interface,
fail-closed launch, and platform smoke tests that prove behavior.

**Architecture:** `internal/sandbox` owns policy construction and enforcement. The renderer
requests only `{workspace}`; the transport validates and canonicalizes it, threads
`*sandbox.Request` through `openParams → session.Config → pty.Config`, and maps typed errors
to reserved JSON-RPC codes `-32010`/`-32011`/`-32012`. `pty.NewLocal` builds the ordinary
shell command first and calls `Service.Prepare` only when a request is present. Linux re-execs
`os.Executable()` as an `__sandbox-landlock-exec` helper that applies strict Landlock
(`go-landlock v0.9.0`, ABI floor 3, cap 8) and handshakes readiness over a pipe. macOS renders
a deterministic SBPL profile and launches `sandbox-exec -p <profile> <shell> -i`, runtime
probed and release-smoke-gated.

**Tech Stack:** Go 1.26, `github.com/landlock-lsm/go-landlock v0.9.0` (new direct
dependency; MIT), `golang.org/x/sys` (already present), `internal/storage` CacheDir for the
per-session runtime tree, JSON-RPC over the existing WebSocket control plane, SolidJS
frontend, Playwright + `cmd/devharness` for E2E.

**Source of truth:** `.internal/specs/2026-08-02-native-filesystem-sandbox-design.md`.
Where this plan and the spec disagree, the spec wins and this plan is wrong. Evidence ledger:
`.internal/reports/2026-08-02-native-filesystem-sandbox-research.md`; decision:
`docs/decisions/0030-native-per-tab-filesystem-sandbox.md`.

> **Amended 2026-08-16:** this is the completed base implementation plan. The executable
> change for global writable paths, per-launch deltas, strict revision-gated requests, and
> macOS in-profile readiness is tracked by `nocx-y46q.14` and specified in
> `docs/superpowers/specs/2026-08-16-sandbox-per-tab-permissions-design.md`.

## Global Constraints

- **Wire tokens are frozen:** setting key `sandbox.enabled`; request field
  `sandbox: {workspace}`; result field `sandbox: {backend, workspace, writableRoots}`;
  method `sandbox.status`; dialog method `dialog.openDirectory`; error codes `-32010`
  (`feature-disabled`), `-32011` (backend status reason), `-32012` (`setup-failed`).
- **Backend names:** `landlock`, `seatbelt`, `unsupported`. Stable status reasons:
  `landlock-unavailable`, `landlock-abi-too-old`, `sandbox-exec-unavailable`, `probe-failed`,
  `unsupported-platform`.
- **ABI policy:** Landlock floor **ABI 3** (truncation deny), semantic cap **ABI 8**
  (strict config at `min(detectedABI, 8)`). No `BestEffort`, no `RestrictNet`, no
  `RestrictScoped`. Unsupported/old kernels fail closed.
- **Fail closed:** no unsandboxed fallback for any request that asked for a sandbox; a session
  is registered only after enforcement readiness. Deliberate divergence from termic's
  fail-open provisioning.
- **Canonical paths only:** the single canonicalized workspace is used as `cmd.Dir`, policy
  input, and response metadata. NUL/empty/relative/nonexistent/non-directory inputs are
  rejected (`-32602`).
- **No renderer-built policy, no package globals:** `sandbox.Service` is injected only at
  `internal/app/app.go`; transport never renders policy.
- **Logging:** backend, reason, ABI — never filesystem paths.
- **Ordinary tabs unchanged:** nil `Sandbox` request must produce byte-for-byte current
  behavior for local and SSH tabs, initial tab, `+`, and `Cmd/Ctrl+T`.
- **AGPL boundary:** borrow behavior, never termic code (SBPL renderer, argv construction,
  proxy HTTP parsing are re-implemented from the spec, not translated).
- **Gate before every commit:** `gofumpt -l .`, `golangci-lint run ./...`,
  `go test -race -count=1 ./...`; when frontend files are staged add `cd frontend && npm run
format:check && npm run lint && npm run typecheck && npm test`. Frontend contract changes
  additionally run `cd frontend && npm run contracts:check`.
- **Every commit names its bead** per AGENTS.md.

---

## File structure

| Path                                             | Responsibility                                                                         |
| ------------------------------------------------ | -------------------------------------------------------------------------------------- |
| `internal/sandbox/sandbox.go`                    | `Request`, `Status`, `Policy`, `CommandSpec`, `PreparedCommand`, `Service`             |
| `internal/sandbox/policy.go`                     | canonical policy builder, validation, root composition                                 |
| `internal/sandbox/policy_test.go`                | pure policy tests (slice 1)                                                            |
| `internal/sandbox/errors.go`                     | typed setup/status errors mapping to `-32010/11/12` reasons                            |
| `internal/sandbox/sandbox_linux.go`              | Landlock `Service` constructor (`NewLinux`)                                            |
| `internal/sandbox/helper_linux.go`               | `__sandbox-landlock-exec` entrypoint, FD protocol, handshake                           |
| `internal/sandbox/restrict_linux_test.go`        | real enforcement integration tests (slice 2)                                           |
| `internal/sandbox/sandbox_darwin.go`             | Seatbelt `Service` constructor (`NewDarwin`), probe                                    |
| `internal/sandbox/profile_darwin.go`             | deterministic SBPL renderer + escaping                                                 |
| `internal/sandbox/profile_darwin_test.go`        | render/injection tests (slice 3)                                                       |
| `internal/sandbox/sandbox_other.go`              | unsupported stub (`unsupported-platform`)                                              |
| `internal/pty/pty.go`                            | `Config.Sandbox *sandbox.Request`                                                      |
| `internal/pty/pty_local.go`                      | `NewLocal` → build command, then `Service.Prepare` when non-nil                        |
| `internal/session/session.go`                    | `Config.Sandbox *sandbox.Request` passthrough                                          |
| `internal/transport/ws.go`                       | `openParams.Sandbox`, `handleOpen` validation, `sandbox.status`, result `sandbox`      |
| `internal/transport/ws_sandbox.go`               | `sandbox.status` handler + error mapping                                               |
| `internal/transport/ws_dialog.go`                | `DialogService.OpenDirectory`                                                          |
| `internal/transport/ws_dialog_test.go`           | `dialog.openDirectory` contract tests                                                  |
| `internal/app/app.go`                            | inject `sandbox.Service` into `localPTYFactory`                                        |
| `internal/settings/settings.go`                  | `SandboxEnabled = MustRegisterBool(…)`                                                 |
| `contracts/dialog.openDirectory.schema.json`     | picker contract schema                                                                 |
| `frontend/src/generated/dialog.openDirectory.ts` | regenerated client type                                                                |
| `frontend/src/dialog-client.ts`                  | `openDirectoryDialog()`                                                                |
| `frontend/src/quick-connect.tsx`                 | `__sandboxed_local__` action + `Sandbox unavailable` row                               |
| `frontend/src/tabs.ts`                           | sandboxed tab descriptor (null restore) + marker wiring                                |
| `frontend/src/terminal-content.ts`               | marker/tooltip render, open params, failure toast                                      |
| `frontend/src/ipc.ts`                            | `openSandboxedSession(workspace)` + `sandbox.status` client                            |
| `frontend/src/main.tsx`                          | provider wiring (live flag read)                                                       |
| `e2e/sandbox.spec.ts`                            | regression e2e (flag off/on, unavailable row, cancel)                                  |
| `Makefile`                                       | `sandbox-smoke-linux`, `sandbox-smoke-macos` targets (named, gated like `conformance`) |
| `.github/workflows/ci.yml`, `release.yml`        | platform smoke gates                                                                   |

---

## Slice 1 — `internal/sandbox` contracts, policy builder, stubs, pure tests

**Files:** all `internal/sandbox/*` except the linux/darwin enforcement bodies (stubs in
`sandbox_linux.go`/`sandbox_darwin.go` return typed setup failures for now; `sandbox_other.go`
is final), `go.mod` (add `github.com/landlock-lsm/go-landlock v0.9.0` direct require).

**Interfaces / symbols:** `Request{Workspace string}`, `Status{Available bool; Backend string;
Reason, Detail string; ABI int}`, `Policy` (validated canonical document, JSON-serializable,
bounded size/root-count), `CommandSpec{Path, Args []string; Dir string; Env []string}`,
`PreparedCommand{cmd *exec.Cmd; …}` (idempotent `Close`), `Service{Status(ctx); Prepare(ctx,
Request, CommandSpec) (*PreparedCommand, error)}`. Policy builder entrypoints:
`BuildPolicy(workspace string, env []string, shellPath string, runtimeRoot string, cacheRoot
string) (*Policy, error)` and `gitCommonDir(workspace) (string, bool)`.

**Acceptance (test-first, `policy_test.go`):**

- A canonical workspace yields RW roots = `[workspace]` (+ git common dir when present) +
  the runtime `home`/`tmp`; RO roots = documented system set + canonical shell + existing
  canonicalized `PATH` dirs.
- Rejections: NUL, empty, relative, nonexistent, non-directory workspace, failed symlink
  resolution, duplicate conflicting permissions, policy above the size/root bound.
- RW subsumes RO on duplicate canonical paths; the realized writable list is returned in
  policy order for the tooltip.
- `gitCommonDir`: parses `gitdir:` from the selected root's `.git` file only; malformed file
  → no extra root, no error; no parent search; canonicalizes the target.
- Status reasons for `unsupported-platform` on non-linux/non-darwin (build-tagged test).
- Non-Linux builds compile: `GOOS=windows go build ./internal/sandbox/...` and
  `GOOS=darwin go build ./internal/sandbox/...`.

**Smallest focused command:** `go test -race -count=1 ./internal/sandbox/...`

**Rollback boundary:** slice-1 commits revert cleanly; nothing else consumes `internal/sandbox`
yet.

**Dependencies:** none (settings/transport untouched).

---

## Slice 2 — Linux Landlock backend

**Files:** `sandbox_linux.go`, `helper_linux.go`, `restrict_linux_test.go`, `go.mod` (pinned
direct require), `Makefile` (`sandbox-smoke-linux`).

**Interfaces / symbols:** `NewLinux(logger) Service`; `PreparedCommand` carries the helper
`*exec.Cmd`, the policy FD, the readiness pipe, and cleanup. Helper entrypoint
`RunHelper(argv []string, policyFD int, statusFD int, shell CommandSpec) int` matching
`os.Executable()` re-exec with argv[1] `__sandbox-landlock-exec`.

**Acceptance (test-first):**

- ABI probe tests (fake kernel via injected query): `DetectedABIVersion`-style wrapper
  returns the raw ABI; below 3 → `landlock-abi-too-old`; probe failure → `landlock-unavailable`.
- Strict-config selection: `min(detectedABI, 8)` never exceeds 8 and never uses
  `BestEffort`/`RestrictNet`/`RestrictScoped` (assert the constructed config's handled sets).
- Helper protocol unit tests: policy FD round-trip (unlinked, `0600`), status byte `0` only
  after `restrict` success, error path sends typed reason and exits `126`, env-scrubbing
  removes helper-only markers and keeps `NOCX_SANDBOX=filesystem`.
- `restrict_linux_test.go` (real enforcement; runs on ABI >= 3 CI runner — Linux job):
  sandboxed child can create/truncate/rename inside each surfaced writable root; can read
  system executables; **cannot** read or mutate a sentinel outside all roots; cannot bypass
  with a symlink (resolution is path-checked), with a subprocess, or with a renamed path
  (rename/link across hierarchies needs `REFER` on both sides and the source hierarchy is
  outside the roots); creating a hard link to a file outside all roots is denied at
  `link(2)`; retains outbound TCP connectivity (contract: network unrestricted). The suite
  also asserts the documented limitation, not a guarantee: a hard link that **already
  exists** inside a writable root before launch remains readable/writable through that path,
  because Landlock identifies files by hierarchy, not by inode.
- `PreparedCommand.Close` is idempotent; helper is waited, never leaked; a helper that never
  reports readiness trips the bounded timeout → typed `setup-failed`, PTY closed, cleanup
  once, no registry entry.

**Smallest focused command:** `make sandbox-smoke-linux` (builds devharness + runs
`restrict_linux_test.go` and the assertion suite; named target modeled on `make conformance` —
runs, and fails loudly, rather than hiding behind an env var).

**Rollback boundary:** slice-2 commits revert independently; slice 1 stays green and consumed
by nothing yet.

**Dependencies:** slice 1. Kernel ABI >= 3 on the runner.

---

## Slice 3 — macOS Seatbelt backend

**Files:** `sandbox_darwin.go`, `profile_darwin.go`, `profile_darwin_test.go`, `Makefile`
(`sandbox-smoke-macos`).

**Interfaces / symbols:** `NewDarwin(logger) Service`; `renderProfile(policy) (string, error)`;
`escapeSBPL(s string) (string, error)`; probe `probeSandboxExec() Status` (exec
`/usr/bin/sandbox-exec -p '(version 1) (deny default) (allow default)' /usr/bin/true`, cache
only success for the app lifetime).

**Acceptance (test-first):**

- Renderer is deterministic for a fixed policy; begins `(version 1)` + `(deny default)`;
  emits process self/children, required signal/Mach/IPC allowances, `(allow network*)`, and
  escaped literal/subpath clauses for only the common RO/RW roots plus PTY character-device
  write/ioctl.
- Injection tests: newline, NUL, and control characters in any path are rejected by
  `escapeSBPL` before rendering; no unescaped quote/backslash reaches the profile.
- Probe: missing binary → `sandbox-exec-unavailable`; non-zero exit → `probe-failed`;
  success cached.
- Pull-request enforcement runs on pinned `macos-15` with
  `make sandbox-smoke-macos`: deterministic profile/injection tests, the common filesystem
  assertions, `sandbox-exec` availability, and a real sandboxed `pty.NewLocal` interaction.
- Release-artefact enforcement runs
  `NOCX_SANDBOX_ARTIFACT=build/bin/nocx.app/Contents/MacOS/nocx make
sandbox-smoke-macos-artifact`. The executable inside the packaged `.app` constructs the
  Seatbelt service and launches an external probe inside the cage. Missing artefact,
  unavailable `sandbox-exec`, profile/render failure, or any failed assertion is a hard gate;
  nothing is silently skipped.
- Both smokes cover create/truncate/rename in writable roots, read-only system roots, sentinel
  denial, symlink/subprocess/renamed-path bypass denial, post-launch hard-link denial,
  outbound loopback TCP, and the documented pre-existing-hard-link limitation.

**Smallest focused command:** `make sandbox-smoke-macos` on macOS 15; release artefact:
`NOCX_SANDBOX_ARTIFACT=build/bin/nocx.app/Contents/MacOS/nocx make
sandbox-smoke-macos-artifact`. Locally on other platforms:
`go test -count=1 ./internal/sandbox/ -run Profile`.

**Rollback boundary:** slice-3 commits revert independently; slices 1-2 stay green.

**Dependencies:** slice 1. macOS enforcement is `[ASSUMPTION]` until `sandbox-smoke-macos`
passes on a real artefact; the feature stays behind the default-off flag regardless.

---

## Slice 4 — Backend plumbing: `pty`, `session`, `app`, transport

**Files:** `internal/pty/pty.go`, `internal/pty/pty_local.go`, `internal/session/session.go`,
`internal/app/app.go`, `internal/transport/ws.go`, `internal/transport/ws_sandbox.go`,
`internal/transport/ws_dialog.go`, `contracts/dialog.openDirectory.schema.json`,
`internal/transport/ws_dialog_test.go`.

**Interfaces / symbols:** `pty.Config.Sandbox *sandbox.Request`; `session.Config.Sandbox
*sandbox.Request`; `localPTYFactory` gains the injected `sandbox.Service` (constructor
param/`Option` from `app.New`); `openParams.Sandbox`; `handleOpen` validation order (§4.1 of
the spec); `DialogService.OpenDirectory(ctx) (string, error)`; `sandbox.status` handler.

**Acceptance (test-first, transport contract tests in `ws_contract_test.go` style):**

- `open` without `sandbox` → identical result `{sessionId, cwd}`; `open` with `sandbox` on an
  SSH request → `-32602`.
- Flag off + `sandbox` present → `-32010` with `data.reason: "feature-disabled"`.
- Invalid/relative/nonexistent/symlinked workspace → `-32602`; a valid symlink resolves to its
  canonical target, and the result's `sandbox.workspace` + `writableRoots` carry the canonical
  value.
- Backend unavailable (stub service) → `-32011` with the stable reason; `sandbox.status`
  returns the same `Status` the Quick Connect row renders.
- Successful sandboxed `open` → result carries `sandbox: {backend, workspace, writableRoots}`;
  ordinary and SSH results omit it.
- Setup failure (service returns typed error) → `-32012` `setup-failed`; the session registry
  has no entry (`registry.List()` unchanged), no ring exists, the PTY is closed, cleanup ran
  once.
- `dialog.openDirectory`: returns `{path}`; `""` on cancel; `-32601` when unwired; schema
  contract file + `npm run contracts:check` regenerate `frontend/src/generated/`.
- `pty` regression: with `Sandbox == nil`, `NewLocal` behavior is unchanged (existing
  `pty_local_test.go`/`pty_test.go` suites pass unmodified); with a request, the command is
  the ordinary `exec.Command(shell, "-i")` wrapped by `Prepare`, resize/close/byte-stream
  semantics preserved.
- Descriptor regression: a sandboxed command retains the ordinary command's inherited
  `ExtraFiles`; the Linux helper appends policy/readiness descriptors and receives their
  computed child FD numbers, so shell-integration handshakes are not displaced.
- Cross-compile gates: `GOOS=linux GOARCH=amd64 go build ./...` and
  `GOOS=darwin GOARCH=arm64 go build ./...` both green.

**Smallest focused command:** `go test -race -count=1 ./internal/...`

**Rollback boundary:** slice-4 commits revert independently; slices 1-3 untouched.

**Dependencies:** slices 1-3 (interface + backends exist, even if stub-only enforcement is
what CI exercises until smoke targets run).

---

## Slice 5 — Frontend: picker, action, live flag, metadata, errors

**Files:** `frontend/src/dialog-client.ts`, `frontend/src/ipc.ts`, `frontend/src/quick-connect.tsx`,
`frontend/src/tabs.ts`, `frontend/src/terminal-content.ts`, `frontend/src/main.tsx`,
`frontend/src/settings-observer.ts` (read-only use), `contracts/dialog.openDirectory.schema.json`
(generated type), `frontend/src/generated/dialog.openDirectory.ts` (regenerated),
`frontend/src/quick-connect.test.tsx`, `frontend/src/tabs.test.tsx`.

**Interfaces / symbols:** `DialogClient.openDirectoryDialog()`; `WSClient.openSandboxedSession(
cols, rows, workspace)` and `sandboxStatus()`; `ActionsQuickConnectProvider` gains the
`__sandboxed_local__` item (constructor-injected live flag reader + picker + open callback);
`TerminalContent` gains a sandboxed-construction path (immutable `{backend, workspace,
writableRoots}` metadata, marker with tooltip naming backend and writable roots,
`restoreDescriptor: null`); `TabManager.newSandboxedTab(…)`.

**Acceptance (test-first):**

- Flag off → `__sandboxed_local__` absent; `New tab`, `New connection…`, `+`, initial tab,
  and `Cmd/Ctrl+T` flows unchanged (existing tests pass unmodified).
- Flag on + status available → action present; picker cancel → no tab, no toast; success →
  exactly one new local tab with the marker and tooltip; `restoreDescriptor` is null.
- Flag on + backend unavailable → non-activatable `Sandbox unavailable` row carrying the
  typed reason; no action row.
- `-32012`/`-32011`/`-32010` from `open` → toast with the reason, no tab, no lingering
  session client state.
- Live toggle: flipping the flag updates only the Quick Connect row visibility; running tabs
  (sandboxed or not) are untouched.
- Generated types are produced by `npm run contracts`, not hand-edited.

**Smallest focused command:** `cd frontend && npm run contracts && npm run typecheck && npm test`

**Rollback boundary:** slice-5 commits revert independently; backend behavior is untouched.

**Dependencies:** slices 1-4 wire contract.

---

## Slice 6 — Cross-platform regression, packaging, docs, release gates

**Files:** `e2e/sandbox.spec.ts`, `Makefile` (smoke targets wired into `ci`/`release`),
`.github/workflows/ci.yml`, `.github/workflows/release.yml`, docs touched only for accuracy
(report/spec/ADR/architecture already published in this documentation set).

**Acceptance:**

- `e2e/sandbox.spec.ts` (devharness + real frontend): ordinary local and SSH tabs remain
  behaviorally unchanged (open/resize/close/exit); a failed sandbox setup creates no
  registered session/tab; disabling the flag affects only future action visibility; the
  `Sandbox unavailable` row renders with the typed reason when the backend status says so.
- Behavior matrix (each row a test name):

  | Case                                             | Expectation                                                     |
  | ------------------------------------------------ | --------------------------------------------------------------- |
  | flag off, ordinary `open`                        | unchanged, no `sandbox` in result                               |
  | flag off, `open` + `sandbox`                     | `-32010 feature-disabled`                                       |
  | flag on, ordinary/SSH `open`                     | unchanged; SSH + `sandbox` → `-32602`                           |
  | flag on, picker cancel                           | no-op                                                           |
  | flag on, picker success                          | new sandboxed local tab, marker/tooltip, `sandbox` in result    |
  | invalid/relative/nonexistent/symlinked workspace | `-32602`; existing symlink → canonical target                   |
  | Git worktree `.git`                              | common dir RW + in `writableRoots`; malformed → no extra root   |
  | Landlock unavailable / ABI < 3                   | `-32011` stable reason; unavailable row                         |
  | helper timeout / exit 126                        | `-32012`, no session, PTY closed, helper waited, cleanup once   |
  | Seatbelt missing / profile rejected              | `-32011`/`-32012`, no tab, no fallback                          |
  | close/exit of sandboxed tab                      | exit semantics unchanged; runtime tree deleted best-effort once |
  | flag toggled live                                | only action visibility changes                                  |
  | malicious SBPL/path input                        | escaping/rejection tests (slices 1, 3)                          |

- Release gates: `ci.yml` runs the Linux smoke on every PR; `release.yml` runs
  `sandbox-smoke-linux` and, for macOS artefacts, `sandbox-smoke-macos` against the packaged
  `.app` — the release fails if the gate did not run (loud skip with named reason only when
  the artefact genuinely cannot exist, mirroring `e2e/vault.spec.ts`'s gnome-keyring
  convention).

**Smallest focused command:** `make ci` plus `npx playwright test e2e/sandbox.spec.ts`

**Rollback boundary:** final slice; any earlier revert returns the tree to the corresponding
pre-slice state — no schema migrations, no persisted data.

**Dependencies:** all prior slices.

---

## Behavior matrix source

The matrix above is the executable form of the design spec §11.4; every row must exist as a
named test in slices 1-6 before the release gate is considered green.

## Cross-cutting evidence requirements

- Landlock ABI floor (3) and cap (8) appear verbatim in: spec §8.1, ADR-0033, this plan's
  Global Constraints, and the smoke-suite names (`sandbox-smoke-linux`).
- Backend names `landlock`/`seatbelt`/`unsupported` and reasons
  `landlock-unavailable`/`landlock-abi-too-old`/`sandbox-exec-unavailable`/`probe-failed`/
  `unsupported-platform` are single-sourced in `internal/sandbox` and asserted in transport
  and frontend tests.
- No document or UI copy claims networking, credentials, IPC, processes, or existing tabs are
  sandboxed.
