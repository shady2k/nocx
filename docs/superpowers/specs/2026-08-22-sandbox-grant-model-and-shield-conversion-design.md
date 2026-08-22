# Sandbox grant model and shield conversion

**Status:** Approved  
**Date:** 2026-08-22  
**Decision:** [ADR-0043](../../decisions/0043-sandbox-grants-and-the-shield-entry.md)

## Goal

Represent sandbox authority as an immutable pane-subject grant and provide one launch path: converting the active local terminal through the activity-bar shield beside the Files view icon.

## Persistence and startup

`panes` contains only durable pane facts. `sandbox_grants(pane_id UNIQUE, version, issued_at, workspace, payload)` records the realized authority. `payload` mirrors the enforced `open.result.sandbox` metadata for a future explicit restart-with-rights flow; this change does not read it in the UI.

A startup transaction selects open panes joined to `sandbox_grants`, marks them closed, and dissolves empty tabs and workspaces using the existing layout rules. Pane rows and ledger anchors survive. `layout.read` exposes `sandboxGranted` as an annotation derived from the join.

## Open gate and enforcement ordering

A sandbox `open` must name an open pane with no existing grant. Duplicate grants return `-32602`. After `sandbox.BuildPolicy` realizes canonical roots, the PTY boundary invokes a grant callback before `StartWithSize`. Grant insertion failure closes prepared resources and returns `-32007`; no helper starts and no session registers. Native readiness remains the final session-registration gate.

## Shield eligibility

The shield is hidden when `sandbox.enabled` is false unless the active tab is already sandboxed. Applying sandbox is disabled unless `sandbox.status.available` is true, the active surface is a local terminal, and its cwd came from verified OSC 7. A sandboxed active tab keeps the shield visible and selected even if the feature flag or sandbox backend becomes unavailable; removing sandbox needs only a verified local cwd. The disabled title names the unmet condition. A ready title names the workspace and whether the action applies or removes sandbox.

The activity bar's top zone owns action buttons that sit beside view icons without activating a view. The shield renders there immediately after Files; it is not part of the Files panel header. It uses the UI kit `IconButton` and existing `ShieldIcon`.

A confirmed sandbox tab prefixes its title line with `ShieldIcon`. The marker is the first element of the name line; pin, warning, activity, title, cwd, and status follow it.

## Conversion

Clicking a ready shield opens the existing permission dialog with the verified cwd supplied as the workspace; the initial native picker is skipped, while the dialog's per-grant folder pickers remain native. Confirmation creates a new sandbox pane. The source pane is captured before creation. On create acknowledgement the new durable tab replaces the source tab id in the workspace's cached order, then the source pane closes. On create failure the existing toast is shown and the source remains unchanged.

Removing sandbox is the reverse replacement. The selected shield creates an ordinary local pane with the verified current cwd carried in the strict `open.cwd` field. The backend accepts only an existing absolute local directory and canonicalizes symlinks; SSH and sandbox requests reject `cwd`. After create acknowledgement the new tab takes the old strip position and the sandbox pane closes. The immutable grant is not deleted or mutated; it remains attached to the closed historical pane.

## Transcript handoff

Before creating either replacement, `TerminalContent.captureConversionTranscript` snapshots frontend-owned presentation state: frozen command blocks, inherited restored blocks from earlier toggles, the settled normal xterm buffer as SGR, and the unsent editor document, selection, and scroll offset. Alternate-screen frames are deliberately omitted: they are a live program's repaintable screen, not terminal history.

After the replacement session reports ready, `installConversionTranscript` renders the blocks through the existing `restoredBlock` grammar, restores the draft, and inserts a visible `Sandbox enabled — new shell` or `Sandbox removed — new shell` boundary. Only then may the source pane close. Failed creation or hydration leaves the source pane untouched. The handoff is in-memory and frontend-only; no PTY bytes or transcript enter JSON-RPC, the content database, or logs.

## Removed paths

The command palette has no `Sandboxed shell…` row. The tab-strip More menu has no `New sandboxed tab` row and no sandbox-enabled imperative state. `+`, Cmd/Ctrl+T, ordinary local creation, SSH creation, and Settings placement are unchanged.

## Verification

Backend tests cover grant insertion/existence/enumeration, startup sweep, duplicate-open refusal, fail-closed grant persistence, and `layout.read` annotation. Frontend tests cover every shield state, workspace override, panel action ordering, conversion readiness, and absence of the removed palette/menu entries. The e2e scenario verifies flag visibility, backend-dependent disabled state, palette absence, and conversion when native enforcement is available.
