# Sandboxed opencode launch design

**Date:** 2026-08-16 · **Status:** superseded by ADR-0037 on 2026-08-18 · **Bead:** `nocx-y46q.17`
**Replacement:** `2026-08-18-sandbox-shell-contract-restoration-design.md`

## 1. Problem

`Sandboxed shell…` currently produces neither promised behavior:

1. The enhanced bash rcfile or zsh ZDOTDIR is created in the host's shared temp tree before native policy construction. That tree is intentionally absent from both the Landlock and Seatbelt policies, so the shell cannot read the bootstrap. It starts conventionally and the capability-bearing artifact may remain on disk.
2. No backend layer has a launch intent for `opencode`; the selected workspace only becomes the shell's `cwd`.

Granting `/tmp` or a macOS temp ancestor would fix the symptom by destroying the filesystem boundary. Injecting text through the PTY after open is race-prone. Starting `opencode` directly would bypass lifecycle authentication; the enhanced shell must authenticate first and then replace itself with the fixed agent.

## 2. User job and copy

Quick Connect exposes one experimental command:

- label: **`Sandboxed opencode…`**
- available detail: **`Start opencode in a filesystem-isolated workspace (<backend>)`**
- unavailable backend: **`Sandbox unavailable (<reason>)`**
- unavailable launch intent: **`opencode unavailable (opencode-not-found)`**

The stable internal id is `__sandboxed_opencode__`; no generic-shell compatibility alias is retained.

Activation opens the native directory picker, then the existing permission dialog. Cancellation at either surface creates no tab and no error. Confirmation creates one non-restorable local tab whose title, shield marker, backend name, workspace, and writable-root tooltip continue to describe enforced policy.

## 3. Binding decisions

### D1. Fixed backend-owned intent

A sandboxed Quick Connect open means `opencode`. The renderer never supplies a command, argv, executable path, runtime root, or trusted policy root. The strict `open` request remains workspace + settings revision + add/remove deltas.

The backend resolves `opencode` from its launch PATH, canonicalizes symlinks, verifies a regular executable, and repeats the check at open time. Missing or unusable executables fail closed with typed reason `opencode-not-found`; no session is registered and no shell fallback opens.

### D2. The enhanced shell authenticates, then the fixed agent replaces it

The enhanced login shell owns bootstrap and lifecycle authentication. The backend-authored tail runs only when `__nocx_lc_active=1`; it then uses `exec` to replace that shell with the canonical `opencode` executable. The lifecycle descriptor remains inherited by the replacement process, so the established domain remains live until the agent exits, while no idle shell is left behind the agent.

The composition root deliberately does not install its unexpected-shell-replacement watcher for this fixed launch intent: replacement is the authenticated success path, not an rcfile takeover. A failed `exec` prints a path-free diagnostic and exits; a normal or non-zero agent exit ends the PTY and closes the tab. There is no integrated prompt after the agent.

### D3. Sandboxed bootstrap artifacts live in the runtime tree

For sandboxed enhanced launches, the sandbox service creates the per-session runtime root before rcfile rendering. The launcher writes the bash rcfile or zsh ZDOTDIR under `<runtime>/tmp`, preserving:

- random `nocx-bash.??????` / `nocx-zsh.??????` basename;
- `O_EXCL` creation;
- 0600 file and 0700 directory modes;
- existing basename-bounded self-delete guards.

The native backend adopts this exact runtime root. No new writable policy grant is needed: runtime `home/` and `tmp/` are already mandatory writable roots. Spawn failure, policy failure, readiness failure, shell self-delete, and session close converge on idempotent runtime-tree cleanup.

Ordinary enhanced local tabs retain the existing host-temp artifact path. SSH does not use the local artifact path.

### D4. Agent executable policy is trusted and read-only

The canonical resolved executable is an internal trusted input. The common policy adds its exact file and required executable/runtime roots read-only before either native backend renders enforcement. A canonical agent path inside any writable sandbox root is rejected; the caged agent must not be able to replace the executable it is about to run.

Linux and macOS consume the same `Policy`. Landlock helper and Seatbelt shim ordering/readiness do not change.

### D5. Availability is advisory; open is authoritative

`sandbox.status` keeps backend availability and adds a launch-intent object:

```json
{
  "available": true,
  "backend": "landlock",
  "intent": {
    "name": "opencode",
    "available": true
  }
}
```

When the intent is missing, the row is disabled and names `opencode-not-found`. PATH may change between status and open, so open resolves again and returns the same typed reason. The renderer must enforce `disabled` with both accessibility state and activation no-op; it must not rely on the backend rejection as click handling.

### D6. Fail closed for the complete product promise

A sandboxed agent open fails instead of degrading when any of these are unavailable:

- native filesystem backend;
- bash/zsh local nocxify tier or lifecycle kernel;
- private bootstrap artifact;
- fixed opencode executable;
- policy preparation or native readiness.

Ordinary local integration keeps its existing conventional fallback. The stricter rule applies only to the explicit sandboxed-opencode action.

## 4. Data flow

```mermaid
sequenceDiagram
    participant UI as Quick Connect
    participant WS as open handler
    participant App as localPTYFactory
    participant SI as shellintegration
    participant SB as sandbox.Service
    participant OS as Landlock / Seatbelt
    participant SH as login shell
    participant OC as opencode

    UI->>WS: open{kind:"local", sandbox:{workspace, revision, add, remove}}
    WS->>WS: canonicalize + authorize settings
    WS->>App: NewPTY(Enhanced=true, Sandbox, Cwd=workspace)
    App->>App: resolve canonical opencode
    App->>SB: NewRuntimeRoot()
    App->>SI: LocalEnhancedLaunch(ArtifactDir=runtime/tmp, AgentExec=opencode)
    SI-->>App: shell argv/env + private artifact
    App->>SB: Prepare(adopted runtime, trusted executable)
    SB->>OS: enforce then acknowledge readiness
    OS->>SH: start shell in canonical workspace
    SH->>SH: read/self-delete rcfile; hello/accept
    SH->>OC: exec fixed opencode only if lifecycle active
    OC-->>UI: PTY ends when opencode exits
```

## 5. Failure and cleanup table

| Failure                  | Result                                                | Cleanup                            |
| ------------------------ | ----------------------------------------------------- | ---------------------------------- |
| backend unavailable      | disabled row or `-32011`                              | no runtime/session                 |
| opencode missing         | disabled row or `-32012`, reason `opencode-not-found` | no session                         |
| unsupported shell/kernel | `-32012`, no conventional fallback                    | runtime/channel/artifact closed    |
| launcher render/write    | `-32012`                                              | runtime/channel/artifact closed    |
| native prepare/readiness | existing fail-closed setup error                      | native backend removes runtime     |
| opencode exits           | PTY/session/tab close                                 | channel and runtime tree removed   |
| tab closes               | session closes                                        | channel, PTY, runtime tree removed |

All wire/log messages remain path-redacted.

## 6. Explicit boundary: OpenCode state

The sandbox still redirects HOME and XDG paths into the ephemeral runtime tree. This change does not read, copy, export, or grant the host's OpenCode `auth.json`, database, logs, plugins, or config directories. The agent can start and can use credentials already supplied by its supported external mechanisms, but automatic host credential bridging requires a separate secret-boundary decision. It must not be smuggled through environment variables or a broad home grant.

## 7. Verification

- Launcher units: bash/zsh artifact directory, mode/name guards, authenticated agent `exec` tail, shell quoting, ordinary-byte stability.
- Policy units: trusted executable roots read-only; writable overlap rejected.
- Linux real PTY + Landlock: rcfile is read in-cage, hello arrives, fixture agent reports canonical `PWD`, host temp remains ungranted, cleanup removes the runtime tree.
- Darwin: profile parity tests and `GOOS=darwin` cross-build; packaged Seatbelt smoke remains the release-host proof.
- Transport: missing agent typed and path-redacted; strict open shape still rejects commands.
- Frontend: label/detail, intent-disabled row, inert activation, renamed failure toast; existing cancellation behavior.
- Regressions: ordinary local integration and SSH work with opencode absent.
