# ADR-0055: Sandbox Learn/Enforce modes and workspace policy authority

- Status: Accepted
- Date: 2026-09-03

## Context

The first sandbox launch design treated a sandbox grant as a pane-only flag and
used one enforcement availability result for every launch. That coupled the
renderer to a stale restore hint, made diagnostics indistinguishable from
authorization, and left workspace policy updates without an optimistic
concurrency boundary.

## Decision

`authority_grants` is the single durable grant table. A row has exactly one
subject: either an execution (`execution_id`, with an expiry) or a pane
(`pane_id`, without an expiry). Pane rows cascade when their pane is deleted;
there is no parallel `sandbox_grants` table. Pane grant issuance is accepted
only when the backend has revalidated the pane's current workspace and the
expected workspace-profile revision in the same transaction.

Sandbox launch has two explicit modes. Learn records a bounded candidate policy
and runs the local shell unrestricted; it is diagnostic and must never be
presented as enforcement. Enforce applies the native backend and fails closed
when setup, readiness, or policy validation fails. The open request carries
only the canonical workspace, one settings revision, one optional workspace
profile revision, and bounded class-specific deltas. The backend derives every
other path and metadata field.

The standard profile is held in typed settings. Named workspace profiles are
content records with copy-on-write materialization from the standard profile,
revision checks, canonical path validation, and explicit deletion back to
standard inheritance. A successful access-inbox promotion updates only the
latest profile; it never changes a running pane.

Learn diagnostics are best effort and separate from authorization. Linux
observes descendant `openat` and `openat2` through seccomp USER_NOTIF and always
continues the syscall. macOS uses permissive Seatbelt plus matching Unified
Log reports. Observer loss is surfaced and bounded; it cannot widen Enforce or
cause an unsandboxed Enforce fallback. The inbox is memory-only, coalesced,
and capped at 500 events. Resolve requires the backend-minted event id and the
backend-owned pane id.

The renderer exposes one Settings → Developer → Sandbox page for sandbox
status, policy controls, and denied-access diagnostics. Generic experimental
sandbox rows are not duplicated in the settings catalogue. The contextual
shield owns `open()`, uses the active local terminal's verified cwd, and
replaces the source tab only after the new session is ready. `/sandbox` is an
internal command and is intercepted before PTY submission.

## Consequences

- A stale pane grant is removed during backend startup and cannot silently
  authorize a restored process.
- The wire contains profile identifiers and realized metadata, never secrets,
  PTY bytes, command text, or renderer-authored policy roots.
- Learn remains useful on systems without native Enforce support, while Enforce
  remains fail closed.
- Settings and access promotion use revision conflicts instead of last-write
  wins; the user must reload after a concurrent policy update.
- The denied-access inbox is diagnostic only and cannot change an active tab.
