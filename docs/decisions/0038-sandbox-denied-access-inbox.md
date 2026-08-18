# ADR-0038: Sandbox denied-access inbox is diagnostic, bounded, and event-ID resolved

**Status:** Accepted
**Date:** 2026-08-18
**Related:** ADR-0033, ADR-0034, ADR-0036, ADR-0037, AD-1, AD-7, AD-8, AD-11, `nocx-091ij`

## Context

The native per-tab filesystem sandbox correctly fails filesystem operations outside its immutable policy, but the failure reaches the user only through the program that received it. A user cannot tell which executable in which sandboxed shell attempted which path, or promote the corresponding directory into the global read-only/read-write baselines used by future tabs.

The two supported platforms do not expose the same denial feed.

- Linux Landlock enforcement is unprivileged, but kernel-native `AUDIT_LANDLOCK_ACCESS` consumption uses the audit netlink multicast group and requires `CAP_AUDIT_READ`. An ordinary packaged desktop application cannot claim that feed.
- Linux seccomp user notification is unprivileged after `PR_SET_NO_NEW_PRIVS`. A filter inherited by descendants can report pathname syscalls to the parent, but it is not a security policy and `SECCOMP_USER_NOTIF_FLAG_CONTINUE` has an explicit TOCTOU warning. Landlock must remain the sole author of allow/deny.
- macOS Seatbelt can attach a backend-generated `(with message ...)` marker to the deny-default profile. `/usr/bin/log stream` can consume matching unified-log entries as the ordinary user. The text remains diagnostic platform output, not an authenticated authorization request.

Paths, shell names, process image paths, and timestamps reveal user activity. Persisting them would create a new retention, backup, and deletion contract. The current settings path APIs replace a client-supplied list; using them directly from the renderer would also permit lost updates and client-authored promotion paths.

## Decision

### 1. Observation is separate from enforcement

`internal/sandbox` owns a diagnostic observer beside, never inside, the native policy decision.

On Linux the Landlock helper installs Landlock first, then optionally installs a seccomp user-notification filter for `openat` and `openat2`. It sends the listener descriptor to the parent over a private inherited Unix socket. The parent reads bounded pathname bytes from the tracee, resolves `AT_FDCWD`/directory descriptors through `/proc/<pid>`, classifies the open flags as read-only or read-write, and checks the already-realized policy before emitting a candidate. It always replies `CONTINUE`; Landlock still makes the authoritative decision. The filter and listener are inherited by descendants.

On macOS the backend mints a 128-bit per-launch token, renders it into the Seatbelt deny-default message, and starts `/usr/bin/log stream --style ndjson` with a predicate containing only that token. NDJSON keeps each streamed event as one bounded record for fail-closed parsing. Only matching `Sandbox: <program>(<pid>) deny ... file-* <absolute-path>` entries are parsed.

If the platform observer cannot be installed before interposition exists, only `sandbox.access.status` changes and the enforced sandbox proceeds. macOS runtime observer failure likewise changes only status. Linux seccomp user notification is a synchronous syscall gate: after the filter exists, losing its listener cannot be made status-only. A listener handoff failure therefore rejects startup, and an unexpected runtime listener failure closes the listener, reports unavailable, and terminates that affected sandbox fail-closed rather than leaving pathname syscalls blocked. None of these paths weakens Landlock or falls back to an unsandboxed launch. Availability language says “best effort” and reports dropped events.

### 2. Correlation is one session incarnation

The session registry mints `{sessionId, instanceId, sessionEpoch}` before PTY preparation and copies it into the backend-only sandbox request. Each observer receives an `AccessSession` recorder bound to exactly that identity.

The recorder buffers at most 32 provisional observations. They become visible only after the native readiness acknowledgement. Closing before readiness discards them; closing after readiness expires unresolved rows. Every later delivery through that recorder is dropped and increments the loss counter. PID, PPID, `comm`, executable text, or path never select a session.

### 3. The inbox is bounded PrivateMetadata

The process owns one in-memory inbox:

- maximum 500 rows;
- maximum 200 rows per list response;
- repeated `{incarnation, executable, path, access class}` candidates coalesce with first/last time and a saturating count;
- oldest-row eviction increments a saturating loss counter;
- malformed, oversized, control-character, unknown-source, and late observations are dropped and counted;
- no persistence, backup, export, telemetry, argv, environment, command text, PTY bytes, or secret material.

`sandbox.access.changed` carries only the inbox revision. Clients re-list through `sandbox.access.list`; private event fields are never broadcast unsolicited.

### 4. Promotion is a backend event transaction

The renderer sends a backend-minted event ID and one closed decision: `dismiss`, `globalReadOnly`, or `globalReadWrite`. It never sends a path in the resolve request.

At observation time the backend derives one exact grant directory: the attempted directory itself, or the immediate existing parent of an attempted file. It never climbs to a broader existing ancestor and never offers the filesystem root. If no exact existing directory is safe, the row remains dismissible but both grant actions are disabled.

On promotion the backend revalidates the stored directory through the settings canonical path pipeline and atomically appends it to the latest corresponding baseline under the registry lock. Existing entries are idempotent. Cross-class conflicts, protected ancestors, disappearance, type changes, symlink resolution changes, capacity, and persistence failures change neither the setting nor the event state. A successful append records the settings revision and resolves the row. Current sessions are never widened or retried.

### 5. User surface

Settings has a separate **Developer → Sandbox access** page. Each row shows the exact attempted path, exact proposed directory, shell, explicitly untrusted executable attribution, access intent, source, time/count, state, and three visible actions:

- Add global read-only;
- Add global read-write;
- Dismiss.

For a write attempt the read-only action remains available but the page states that it will not satisfy the observed write. Unavailable/degraded and loss states are persistent page content, not transient toasts.

## Consequences

- Linux support is useful without audit capabilities and remains honest: it covers `openat`/`openat2` pathname attempts, not every Landlock denial class. Static binaries using other pathname syscalls can be missed.
- macOS support depends on the stability and visibility of Seatbelt unified-log text. A monitor that cannot remain alive is reported unavailable while Seatbelt enforcement continues.
- Process and pathname attribution are diagnostic, explicitly untrusted metadata. They cannot authorize a grant; only the stored event ID reaches the backend transaction.
- The bounded memory-only model deliberately loses history across app restart. Durable audit history requires a separate PrivateMetadata retention/export/backup/deletion decision.
- New global rules apply only to future sandboxed tabs, preserving immutable realized session policy.
