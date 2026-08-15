# Sandbox command, statistics, and workspace profiles

## Status

Approved product direction, 2026-08-23. Implementation is tracked by `nocx-a0qhd.2`, `.3`, `.4`, `.5`, and `.7`.

## Jobs to be done

1. While typing in a terminal, toggle sandboxing without leaving the keyboard or sending a nocx command to the shell.
2. Inspect live sandbox health, the active tab's grant, and denied-access events in one dedicated surface rather than Settings.
3. Reuse one filesystem policy per workspace while minting a fresh immutable grant for every sandboxed pane.

## Owner decisions

- Bare, exact `/sandbox` toggles the active tab by replacement. It takes no arguments.
- Applying sandbox always opens the permissions dialog. A saved profile pre-fills the dialog; it never silently reissues authority.
- Each named workspace has at most one optional sandbox profile.
- A standard profile applies to tabs in the default workspace and is the fallback when a named workspace has no profile.
- A denied-access promotion targets the event's workspace profile. For a named workspace without a profile, promotion materializes a copy-on-write profile from the standard profile. Events from the default workspace update the standard profile.
- A dedicated activity-bar action opens one singleton Sandbox statistics tab. Sandbox access is removed from Settings.

## Product acceptance

### `/sandbox`

- The exact trimmed command is consumed before PTY, history, ledger, redaction, or shell planning.
- On an ordinary local terminal with verified cwd, it opens the existing permissions dialog with the effective workspace profile and then uses the same replacement conversion as the shield.
- On a sandboxed terminal, it removes sandbox through the existing ordinary-shell replacement path.
- A refused command stays in the draft and shows the same reason as the shield.
- A consumed command clears the draft but emits no terminal line or command block.
- Shield and command share one in-flight guard.

### Sandbox statistics

- A distinct global action in the activity bar opens/focuses one non-restorable singleton tab.
- The tab shows native backend status, denied-access observer status, current pending/lost counts, active pane grant provenance, and the effective profile for the active workspace.
- It reuses the bounded, memory-only denied-access inbox and its resolve operations.
- It contains no telemetry, persisted aggregates, time series, export, PTY bytes, argv, environment, command text, or secrets.
- Settings has no Sandbox access page after the cutover.

### Workspace profiles

- A named workspace optionally stores one versioned RO/RW profile.
- Missing named profile means inherit the standard profile; no second merge rule exists.
- The permissions dialog shows inherited defaults and per-launch narrowing separately.
- Editing a profile never changes a running grant. The next conversion mints from the new revision.
- Stale dialogs are rejected by profile revision.
- Denied-access promotion is backend-resolved to the event's workspace; the renderer never supplies an authority target.

## Non-goals

- No `/sandbox <path>` or inline RO/RW arguments.
- No multiple named profiles per workspace.
- No global reusable profile catalog.
- No silent auto-apply.
- No mutation of live Landlock or Seatbelt policy.
- No restoration of a prior sandbox grant without confirmation.
- No SSH sandboxing in this slice.
- No analytics dashboard or durable denial history.
