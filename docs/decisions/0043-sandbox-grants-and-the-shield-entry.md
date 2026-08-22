# ADR-0043: Sandbox grants and the sidebar shield entry

- Status: Accepted
- Date: 2026-08-22

## Context

PR #91 represented non-restorability as `panes.ephemeral`. That field names a consequence, not the authority that caused it, and spends schema version 12 on a boolean that cannot answer which policy was granted. ADR-0020 already treats authority as an immutable grant. Agent-run `authority_grants` are execution-subject grants and remain unchanged.

The same draft exposed sandbox launch in Quick Connect and the tab-strip More menu. Conversion instead needs the active local terminal's verified cwd and must preserve the source tab until the replacement is known to exist.

## Decision

Add `sandbox_grants` as a parallel table whose subject is one durable pane. A grant contains version, backend issue time, canonical workspace, and the realized policy metadata serialized in `payload`. One pane has at most one grant. The grant is inserted after policy realization and before the native helper starts; insertion failure aborts launch with `setup-failed`. A running grant is immutable.

Non-restorability is derived. At backend startup, every open pane named by `sandbox_grants` is closed before the first `layout.read`; the renderer also refuses to adopt any granted row. Authority therefore ends with the backend incarnation and cannot be silently re-issued. `layout.read` annotates open panes with `sandboxGranted`; `panes` stores no sandbox flag.

Schema version remains 12. The previous v12 shape existed only on this unmerged branch, and the documented discard mechanism already rebuilds files whose version differs. Creating v13 would preserve no released data.

The only launch entry is the shield in the activity bar's top zone beside the Files view icon; it is not part of the Files panel header. It is hidden while `sandbox.enabled` is false unless the active tab is already sandboxed. It is disabled when the active surface is not a local terminal or OSC 7 has not verified a cwd; applying sandbox additionally requires an available backend. Quick Connect, the tab-strip More menu, `+`, and Cmd/Ctrl+T contain no sandbox action.

After native readiness confirms the sandbox, the activity-bar shield stays selected and the replacement tab's title line is prefixed with the shield icon. The title shield is the first name-line element, before pin, warning, activity, program title, cwd, or other status.

Pressing the selected shield removes sandbox by replacement, never by widening the running process: create an ordinary local pane in the verified current cwd, wait for its durable create acknowledgement, place it at the sandbox tab's strip position, then close the sandbox pane. The old pane's immutable grant remains historical and the closed pane is not restorable. A failed ordinary create leaves the sandbox tab untouched.

Applying sandbox follows the symmetric replacement sequence. A failed create leaves the source untouched. The existing descendant-close confirmation still governs source closure.

Both replacement directions preserve the frontend-owned transcript before closing the source. Frozen blocks are copied through the existing restored-block grammar, the normal xterm buffer through the existing SGR serializer, and the unsent editor draft with its selection and scroll offset. The replacement inserts `Sandbox enabled — new shell` or `Sandbox removed — new shell` as the session boundary. This handoff is memory-only and frontend-only under AD-6: raw PTY bytes never enter JSON or persistence. Alternate-screen program state and running processes are not transferable because the replacement is a new shell.

Generalizing `authority_grants` to multiple subject kinds is deferred. Reopening a grant or changing a running pane's grant is also deferred; both require explicit user consent and a restart design.

## Consequences

- The persisted model records cause and realized authority rather than a restore hint.
- A crash after grant insertion but before enforcement leaves a pane that the next startup closes; it never restores unsandboxed.
- The shield depends on live settings, backend status, active-surface identity, and verified cwd.
- Conversion temporarily has both source and replacement tabs; source removal occurs only after replacement readiness.
