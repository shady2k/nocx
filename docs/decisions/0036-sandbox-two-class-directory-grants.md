# ADR-0036: Sandbox directories have explicit read-only or read-write grants

- **Status:** Accepted
- **Date:** 2026-08-17
- **Related:** ADR-0033, ADR-0034, ADR-0035, ADR-0011, AD-11, `nocx-83oba`
- **Design:** `docs/superpowers/specs/2026-08-17-sandbox-two-class-permissions-design.md`

## Context

ADR-0034 added multiple optional sandbox directories, but made every one writable. Common agent workflows need to inspect shared notes, archives, datasets, or sibling repositories without modifying them. The existing choice — no access or read-write access — violates least privilege.

The current implementation already appends multiple picker results. The missing capability is an explicit access class per directory, enforced the same way on Linux Landlock and macOS Seatbelt.

## Decision

1. Keep the typed Experimental `sandbox.allowedWritablePaths` setting and add the parallel `sandbox.allowedReadOnlyPaths` setting. Both are `paths` controls, `PrivateMetadata`, default `[]`, and bounded to 32 entries. They use the existing canonical path-list registry; there is no sandbox-specific store. Every single-setting, bulk, restore, and reset mutation validates the final pair atomically and rejects read-only paths equal to or below a writable path before persistence.
2. Settings and the pre-launch modal expose the two classes separately. Repeated native picker actions append entries. There is no free-form path input. Picker cancellation is a no-op.
3. `open.sandbox` replaces writable-only `add`/`remove` with four explicit bounded arrays: `addWritable`, `removeWritable`, `addReadOnly`, and `removeReadOnly`. The renderer still sends only workspace, settings revision, and deltas — never either baseline or an effective/native policy.
4. The backend reads both baselines from one atomic settings snapshot and requires the supplied revision to match. A missing, malformed, oversized, vanished, or conflicting persisted baseline fails closed as `-32012 setup-failed`; it is never silently narrowed.
5. The common policy is composed as:

   ```text
   writable = workspace
            + reciprocal Git common directory
            + canonical(globalWritable) − exact canonical(removeWritable)
            + canonical(addWritable)
            + runtime home/tmp

   read-only = canonical system/execution roots
             + canonical(globalReadOnly) − exact canonical(removeReadOnly)
             + canonical(addReadOnly)
   ```

6. Canonical comparisons use filesystem identity, not spelling alone, and enforce the native additive model:
   - the same effective canonical path cannot be both read-only and read-write;
   - a read-only root equal to or below an effective writable root is rejected because it would remain writable and misrepresent the grant;
   - a writable child below a read-only ancestor is allowed as an enforceable writable exception;
   - same-class duplicates collapse first-wins;
   - same-class add/remove collisions and unmatched removals are invalid;
   - changing a baseline path's class is remove-from-old plus add-to-new.
7. Request/delta faults map to `-32602`. Persisted-baseline faults map to `-32012`. Error messages, stable reasons, and logs contain no user path.
8. The workspace, Git common directory, runtime home/tmp, system/runtime execution roots, shell, and fixed opencode executable remain backend-derived. Mandatory roots cannot be removed or downgraded.
9. Both native renderers keep their existing machinery. User read-only roots join `Policy.ReadOnlyRoots`; user read-write roots join `Policy.WritableRoots`. Landlock maps them to `RODirs`/`RWDirs`; Seatbelt maps them to read-only/read-write clauses.
10. The immutable open result gains required `sandbox.readOnlyRoots` beside `sandbox.writableRoots`. Both are deep copies of the installed `Policy` after native readiness, not echoed request values.
11. Bounds remain explicit: 32 entries per persisted list and per delta array, 256 realized policy roots, and 64 KiB serialized policy. Backend runtime discovery is also bounded to 16,384 PATH entries/candidates, 1 MiB per ELF metadata section, 4 KiB per dynamic string, 256 strings per tag, 64 MiB aggregate ELF metadata, 65,536 dependency nodes/resolution probes, and dependency depth 64; relative PATH entries are ignored. “Any number of folders” means multiple user-selected entries within these safety bounds, never unbounded control-plane or executable-metadata input.
12. Running grants remain immutable. Any validation, policy, native launch, probe, or readiness failure registers no session and never retries unsandboxed.

## Superseded ADR-0034 clauses

This decision replaces ADR-0034's writable-only clauses:

- one global writable baseline becomes separate read-only and writable baselines;
- writable-only `add`/`remove` becomes four class-scoped deltas;
- read-only user grants are no longer a non-goal;
- the open result reports both root classes;
- policy conflict rules now include cross-class containment.

All other ADR-0034 decisions remain: native picker only, canonical existing directories, exact baseline removals, settings revision gating, backend policy authority, immutable running grants, strict decode, canonical workspace ownership, and post-enforcement readiness.

## Consequences

### Positive

- Optional directories receive least privilege instead of automatic write access.
- Linux and macOS still consume one common policy with no new native rule kind.
- Existing typed settings and row-list UI are reused.
- Full realized metadata lets the tab describe both installed classes.
- Unenforceable “read-only inside writable” configurations are rejected rather than silently widened.

### Cost

- The transport has four class-scoped delta arrays and both baselines share the revision gate.
- A persisted cross-class conflict blocks new sandbox launches until corrected; this is intentional fail-closed visibility.
- The realized read-only metadata includes system/execution roots and can make the tooltip longer.
- Native smokes must assert both positive and negative operations for each class.

## Rejected alternatives

- **One setting containing `{path, access}` objects:** adds a new settings value type, control kind, corruption path, schema, and partition step, while both native backends still need separate slices.
- **Writable wins on conflict:** silently turns a requested read-only path into writable authority.
- **Read-only child under writable ancestor:** neither additive backend can enforce that deny hole; accepting it would lie in UI and metadata.
- **Unlimited arrays:** violates bounded input and policy-document invariants.
- **Renderer sends final roots:** makes the renderer a second policy author and bypasses the atomic baseline/revision contract.
- **Live access changes:** Landlock and Seatbelt cannot safely mutate the already-running boundary and session metadata would stop being immutable.
