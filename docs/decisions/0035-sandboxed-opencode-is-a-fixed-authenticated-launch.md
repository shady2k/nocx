# ADR-0035 — Sandboxed opencode is a fixed authenticated launch

- **Status:** Accepted
- **Date:** 2026-08-16
- **Related:** ADR-0033, ADR-0034, ADR-0024, AD-7, AD-8, AD-11, `nocx-y46q.17`
- **Design:** `docs/superpowers/specs/2026-08-16-sandboxed-opencode-launch-design.md`

## Context

ADR-0033 named the experimental action `Sandboxed shell…`. Its implementation builds a local enhanced-shell rcfile under the host `os.TempDir()` and only afterwards asks Landlock or Seatbelt to enforce policy. The policy intentionally grants no host temp root. The caged shell therefore cannot read the capability-bearing rcfile, starts conventionally, and may leave the unread artifact behind.

The action also stops after opening that shell. No backend layer starts the `opencode` agent the workflow exists to contain.

Three tempting fixes violate existing invariants:

- granting the shared host temp directory turns one private artifact into broad writable authority;
- sending `opencode` through the PTY after open races user input and makes the backend infer readiness from terminal bytes;
- starting `opencode` directly bypasses the authenticated bootstrap and cannot prove that nocxify became active.

## Decision

### 1. The product intent is fixed and backend-owned

The action is renamed **`Sandboxed opencode…`**. A confirmed sandbox open means: create one isolated local tab in the canonical workspace, establish the authenticated local nocxify lifecycle channel, then replace the bootstrap shell with the backend-resolved `opencode` executable.

The renderer continues to send only workspace, settings revision, and bounded add/remove deltas. No command, argv, executable path, runtime root, or trusted policy root is added to `open`.

The backend resolves and canonicalizes `opencode` from its launch PATH. Missing or unusable opencode is the stable typed reason `opencode-not-found`: status makes the row inert, and open rechecks authoritatively and registers no session on failure. There is no conventional-shell fallback for this explicit agent action.

### 2. Sandboxed bootstrap artifacts are born inside the runtime tree

The sandbox service creates the per-session 0700 runtime root before local shell-integration rendering. The bash rcfile or zsh ZDOTDIR is created under its `tmp/`, retaining the random basename, exclusive creation, 0600/0700 modes, and basename-bounded self-delete guards.

The native backend adopts that same root. Runtime `home/` and `tmp/` were already mandatory writable roots, so Landlock and Seatbelt need no broader grant. Failures and session close remove the tree idempotently.

Ordinary local tabs retain their existing host-temp launcher artifacts. SSH is unchanged.

### 3. The shell authenticates before the fixed agent replaces it

The private rcfile contains a shell-quoted, backend-rendered agent tail. It runs only when `__nocx_lc_active=1`, which occurs after hello/accept, and then uses `exec` to replace the shell with `opencode`. The lifecycle descriptor is inherited by the replacement process, keeping the accepted domain alive until the agent exits without leaving an idle shell behind it. The composition root omits its unexpected-shell-replacement watcher for this fixed authenticated intent. Agent exit ends the PTY and tab; failed `exec` reports a path-free diagnostic and exits.

A sandbox request also fails closed when no supported local bash/zsh integration tier or lifecycle kernel exists. Ordinary enhanced tabs retain their existing conventional fallback.

### 4. The agent executable is a trusted read-only policy input

The canonical executable and its required loader/runtime roots join the common policy read-only set before native rendering. An executable inside any writable sandbox root is rejected. Both platforms consume the same `Policy`; helper/shim enforcement and readiness order remain unchanged.

### 5. OpenCode host state stays outside this decision

Sandbox HOME and XDG paths remain ephemeral. nocx does not copy, read, export, or grant the host's OpenCode credentials, database, logs, plugins, or configuration. Credential bridging would cross a separate secret boundary and requires its own decision; it is not hidden in environment variables or a broad home grant here.

## Consequences

- The action name now describes the program the user receives.
- A sandboxed tab cannot silently become a conventional shell when nocxify or opencode is unavailable.
- Capability-bearing launcher files no longer touch shared temp for sandboxed sessions.
- The shell and agent share one PTY/session; the authenticated lifecycle descriptor survives the intentional replacement until agent exit.
- `sandbox.status` gains launch-intent availability, but `open` request and result shapes remain command-free.
- OpenCode begins with an ephemeral home and may require authentication supported without exposing host state.

## Not decided here

- Selecting Claude Code, Codex, or another agent instead of fixed `opencode`.
- Network isolation or a loopback proxy.
- Automatic import of host agent credentials/configuration.
- Restoring sandboxed agent tabs after restart.
