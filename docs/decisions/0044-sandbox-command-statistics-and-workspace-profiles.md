# ADR-0044: Sandbox command, statistics, and workspace profiles

- **Status:** Accepted
- **Date:** 2026-08-23
- **Amends:** ADR-0020 §5, ADR-0039 baseline ownership, ADR-0041 UI placement, ADR-0043 entry points

## Context

ADR-0043 replaced the `panes.ephemeral` consequence with pane-subject grants and consolidated conversion into the activity-bar shield. The resulting authority boundary is correct, but three product gaps remain: keyboard-only conversion, operational diagnostics hidden in Settings, and one standard profile shared by every workspace.

A saved profile cannot itself become authority. ADR-0020 requires the workspace to mint a default while the immutable grant stays on the execution subject. Landlock cannot widen a running process and Seatbelt applies before `exec`, so applying or removing policy remains replacement, never mutation.

## Decision

The exact, argument-free command `/sandbox` toggles the active local terminal through the same replacement conversion controller as the shield. It is intercepted before PTY submission, shell planning, history, ledger, or redaction. Applying always shows the permissions dialog; a profile only pre-fills defaults. Removing creates an ordinary replacement. Refusal preserves the editor draft, and consumption emits no terminal line or command block.

Each named workspace has at most one optional sandbox profile in the `sandboxProfile` namespace of `workspaces.payload`. A missing profile inherits the standard profile represented by the existing typed sandbox path settings. Tabs in the default workspace use that standard profile. Profiles are versioned durable defaults, not authority; every launch mints a fresh pane grant, and editing or deleting a profile never changes a running grant.

A denied-access promotion targets the event's backend-owned workspace. A named workspace without an explicit profile receives a copy-on-write profile initialized from the current standard profile. A default-workspace event updates the standard profile. The renderer supplies only a backend-minted event ID and decision; it cannot redirect authority to another workspace.

A dedicated global activity-bar action opens one non-restorable singleton Sandbox statistics tab. It is distinct from the active-tab conversion shield. Pane-scoped sections bind to the source terminal selected when the action opens or retargets the singleton, because the statistics tab itself becomes active; relaunch reactivates that source before using the shared controller. The tab owns live backend/observer status, source grant provenance, effective profile, the bounded memory-only denied-access inbox, and profile editing. Settings retains sandbox configuration and the standard profile but no longer owns the Sandbox access page.

Statistics remain product state, not telemetry: no durable denial counters, time series, rankings, export, cloud logging, PTY bytes, argv, environment, command text, or secrets.

## Consequences

- Shield and `/sandbox` share one conversion state machine and in-flight guard.
- `/sandbox` has no path or policy arguments; one-launch narrowing stays in the native-picker permissions dialog.
- `workspaces.payload` gains a typed reader/writer that preserves unrelated keys and uses the existing content writer goroutine.
- Open requests always carry the displayed settings revision and carry a workspace profile revision only for an explicit workspace source; the backend re-resolves pane → workspace and refuses either stale revision before process start.
- Grant payloads gain backward-tolerant workspace/profile provenance and record the effective settings/workspace profile revision; only legacy grants have no revision.
- Access events gain backend-owned pane/workspace provenance in their explicit PrivateMetadata list result; change notifications remain path-free. Resolve decisions are `dismiss`, `workspaceReadOnly`, or `workspaceReadWrite` and cannot redirect the backend-owned target.
- Named workspace profiles replace rather than merge the standard profile. Absence is the only inheritance state.
- Running sandbox panes remain non-restorable and immutable. A profile change is applied only by an explicitly confirmed relaunch.

The complete contracts, state tables, transaction boundaries, and test matrix are specified in `docs/superpowers/specs/2026-08-23-sandbox-command-statistics-and-workspace-profiles-design.md`.
