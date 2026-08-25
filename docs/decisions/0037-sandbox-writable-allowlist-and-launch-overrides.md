# ADR-0037: Sandbox writable allowlist and launch-time overrides

- **Status:** Accepted
- **Date:** 2026-08-16
- **Related:** ADR-0036, ADR-0011, AD-11, `nocx-y46q.14`
- **Design:** `docs/superpowers/specs/2026-08-16-sandbox-per-tab-permissions-design.md`

## Context

ADR-0036 deliberately shipped the experimental filesystem sandbox with one writable workspace and no configurable roots. That proves the native Landlock/Seatbelt boundary but makes common agent workflows leave the cage whenever a tab needs a shared notes, data, archive, or sibling-repository directory.

The sandbox branch is not merged. Adding the permission model now avoids freezing a workspace-only wire contract and avoids compatibility aliases later.

## Decision

1. Add one typed Experimental setting, `sandbox.allowedWritablePaths`, default `[]`, control `paths`, data class `PrivateMetadata`. It is a persisted global baseline for **additional read-write directories** and uses the existing settings registry, settings RPC, notifications, backup, and restore. It is not a secret and never contains secret material.
2. A new sandboxed tab starts with that baseline. Before launch the user may remove exact baseline entries and add ephemeral directories for that tab. Overrides are never persisted and a running tab cannot be changed.
3. The `open.sandbox` request carries required `workspace` and `settingsRevision`, plus bounded optional non-null `add` and `remove` arrays. It never carries an effective policy, global baseline, Git root, runtime root, or native-backend clauses.
4. The backend takes one atomic settings snapshot and rejects a revision mismatch. It alone canonicalizes and composes the policy:

   ```text
   workspace
   + validated reciprocal Git common directory
   + canonical(global) − exact canonical(remove)
   + canonical(add)
   + ephemeral runtime home/tmp
   ```

5. Workspace, global, additions, and removals must resolve to existing absolute directories. Invalid renderer values are invalid params; invalid persisted globals are setup failures. Removals match only exact canonical global entries. Mandatory workspace/Git/runtime roots cannot be removed.
6. A user writable root, including the mandatory workspace, equal to or an ancestor of a documented platform system read-only root is rejected. A broad grant must not erase the read-only execution floor.
7. The existing immutable open result remains authoritative: `sandbox.{backend,workspace,writableRoots}` is copied from the installed policy after native readiness.
8. `open` sandbox decoding is strict. A missing member alone means ordinary local; present `null`, unknown/duplicate members, wrong types, and oversized arrays are rejected. No malformed opt-in can silently become an ordinary launch.
9. Sandboxed sessions start in the canonical workspace. The same value is `cmd.Dir`, session CWD, policy input, and result metadata.
10. Linux retains its post-Landlock readiness pipe. macOS adds a nocx re-exec shim under `sandbox-exec`; the shim acknowledges readiness only after Seatbelt applied the profile, then `exec`s the shell. A rejected profile never registers a session.
11. Shared `DocumentStore` enforces its documented size bound; the settings loader admits only a bounded typed path list.

## Amendments to ADR-0036

This decision supersedes these ADR-0036 V1 clauses:

- “one setting, `sandbox.enabled`; no allowed paths” becomes two settings: the default-off flag and the writable baseline;
- “renderer requests only `{workspace}`” becomes workspace, settings revision, and per-launch deltas; the renderer still never authors policy;
- “no arbitrary user-supplied extra roots” is replaced by explicit, bounded directory grants selected before launch;
- macOS “`-p` launch success is readiness” is replaced by the post-profile shim acknowledgement.

Everything else remains: filesystem-only scope; local tabs only; default-off; no fallback; backend-owned canonical policy; one PTY per tab; immutable running grants; Linux ABI floor/cap; runtime probe; no network guarantee; no Windows backend.

## Threat-model boundary

ADR-0036 protects against filesystem operations by the new sandboxed process and descendants after launch. A separate already-unrestricted same-user process racing a pathname replacement during setup is outside that model because it already owns the host access withheld from the sandboxed process. The implementation revalidates immediately before policy construction but does not claim inode-stable authorization against that excluded actor. Linux O_PATH-fd binding remains an available hardening if the threat model expands.

## Consequences

### Positive

- Useful sandboxed agent workflows remain inside the cage.
- Every tab can carry a different immutable grant without another store or policy language.
- The backend remains the only policy authority; both native backends consume one document.
- A settings revision gate prevents unseen concurrent global grants.
- macOS setup failures become typed pre-session failures rather than briefly registered dead tabs.

### Cost

- The settings registry gains its first list control and filesystem-backed validation.
- Launch adds one modal step and one strict DTO revision field.
- A global entry that disappears blocks new sandbox launches until removed; this is intentional visibility, not a silent privilege drop.
- Any settings mutation while the launch dialog is open can cause a conservative stale-revision rejection.
- Native macOS artifact smoke is required because `sandbox-exec` is deprecated and inherited-pipe behavior is runtime-specific.

## Rejected alternatives

- **Separate sandbox store/RPCs:** duplicates settings persistence, notifications, backup, and generated UI.
- **Newline-delimited string setting:** hides list validation inside a text convention and creates partial-edit invalid states.
- **Renderer sends final effective roots:** makes the renderer a policy author and permits stale/unseen grants.
- **Live grant mutation:** neither Landlock nor Seatbelt provides the required safe widening/narrowing model; it breaks immutable session metadata.
- **Drop invalid persisted entries:** cannot claim the requested baseline was realized and hides configuration drift.
- **Wait only for macOS process start:** proves `sandbox-exec` loaded, not that the profile applied.
- **Network proxy/isolation from termic:** outside nocx’s filesystem-only scope.
