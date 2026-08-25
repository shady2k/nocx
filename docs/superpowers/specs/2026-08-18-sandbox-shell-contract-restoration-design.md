# Sandboxed shell contract restoration design

**Date:** 2026-08-18 · **Status:** accepted for implementation · **Bead:** `nocx-y46q.18`  
**Related:** ADR-0036, ADR-0037, ADR-0039, ADR-0040, AD-11  
**Supersedes:** `2026-08-16-sandboxed-opencode-launch-design.md`; its private runtime-tree artifact placement is retained

## 1. Problem

The original per-tab filesystem sandbox promised an interactive local shell. Commit `11498f1e` correctly moved the nocxify bootstrap artifact into the future sandbox runtime tree, but also changed the product into an automatic fixed OpenCode launch. That substitution makes every sandbox tab an ephemeral OpenCode installation and exposes OpenCode first-run output in the PTY.

Restore the original shell contract without regressing the artifact-placement repair or native fail-closed enforcement.

## 2. Product and wire contract

Quick Connect exposes **`Sandboxed shell…`** with stable id `__sandboxed_local__` while `sandbox.enabled` is true. Activation reads one settings snapshot, selects a workspace, confirms read-write/read-only deltas, and opens one non-restorable sandboxed local tab. Cancellation creates nothing.

The renderer sends no command or executable. `open.sandbox` remains workspace + settings revision + four bounded class-scoped deltas. `sandbox.status` reports only native backend availability:

```json
{
  "available": true,
  "backend": "landlock",
  "abi": 9
}
```

OpenCode installation or PATH state never affects action availability. The open-failure toast is `Sandboxed shell failed to start: <message>`.

## 3. Launch sequence

```mermaid
sequenceDiagram
    participant UI as Quick Connect
    participant WS as open handler
    participant App as local PTY factory
    participant SI as shell integration
    participant SB as sandbox service
    participant SH as login shell

    UI->>WS: open{kind:"local", sandbox:{workspace, revision, deltas}}
    WS->>WS: validate + authorize exact settings snapshot
    WS->>App: NewPTY(Enhanced=true, Sandbox, Cwd=workspace)
    App->>SB: NewRuntimeRoot()
    App->>SI: LocalEnhancedLaunch(ArtifactDir=runtime/tmp)
    SI-->>App: bash/zsh argv + private bootstrap artifact
    App->>SB: Prepare(adopted runtime, login shell command)
    SB->>SH: enforce native policy, then acknowledge readiness
    SH->>SH: source/self-delete artifact; authenticated hello/accept
    SH-->>UI: interactive prompt in canonical workspace
```

The composition root creates the mode-0700 runtime tree before rendering the local bootstrap. Bash receives a mode-0600 rcfile under `<runtime>/tmp`; zsh receives a private ZDOTDIR there. The native backend adopts that same runtime tree. Every launch failure and tab close removes it idempotently.

After authenticated hello/accept, the login shell remains the foreground PTY process. No rcfile tail executes or replaces it. The ordinary unexpected-process-replacement observer remains enabled.

## 4. Security and failure semantics

- Only the backend chooses the login shell, runtime tree, native backend, policy roots, and enforcement helper/shim.
- The capability-bearing bootstrap never enters shared host temp for sandboxed sessions.
- A sandbox request without the local lifecycle kernel or a supported bash/zsh integration tier fails closed.
- Invalid grants, stale settings, artifact creation failure, unavailable enforcement, policy failure, and readiness failure register no session and never fall back to an unsandboxed shell.
- The fixed-agent executable resolver, status reason, bootstrap `exec` tail, PTY trusted-executable option, and renderer intent field are deleted.
- The macOS in-profile readiness shim remains a backend-derived read-only enforcement dependency. No renderer-authored executable authority is introduced.
- OpenCode host credentials, config, database, logs, and plugins remain inaccessible unless the user explicitly grants matching directories under the ordinary two-class policy.

AD-1, AD-6, and AD-7 are unchanged: PTY bytes stay binary, the backend does not infer readiness from terminal output, and session identity is server-authoritative.

## 5. Verification contract

- Frontend provider test: exact action id/copy, backend-only disabled state, picker callback.
- Wire test: `sandbox.status` has no `intent`; open remains command-free and rejects unknown members.
- App test: bash/zsh bootstrap artifact is below the per-session runtime `tmp`, contains authenticated lifecycle bootstrap, contains no OpenCode launch, and prepares the configured login shell in the selected workspace.
- Local launcher tests: no fixed-agent tail; ordinary local and SSH launch bytes remain unchanged.
- Native Linux PTY smoke: the sandboxed shell is interactive, starts in the canonical workspace, enforces read/write boundaries, and cleans its runtime tree.
- Contract generation, TypeScript checks, focused Go/TypeScript suites, `make ci`, `gosec ./...`, and `npm audit` gate delivery.
