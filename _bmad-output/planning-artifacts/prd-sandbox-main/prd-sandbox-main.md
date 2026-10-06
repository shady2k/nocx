---
title: Filesystem Sandbox for Local Shell Sessions
status: final
created: 2026-10-05
updated: 2026-10-05
---

# PRD: Filesystem Sandbox for Local Shell Sessions

## 0. Document Purpose

This product requirements document defines the user-visible behavior and security acceptance contract for adding filesystem sandboxing to the current nocx main. It is an input to UX, architecture, implementation planning, and verification. The approved main-rewrite plan is the source of product and security decisions; implementation details that do not change this contract belong in the downstream architecture/specification. This PRD does not claim implementation or runtime verification.

## 1. Vision

A user can run any supported local shell or coding agent with an explicit, understandable filesystem grant that the operating system enforces for the complete child-process tree. The grant is fixed for that launch, visible in nocx, and independent of pane type, workspace membership flags, mutable profile edits, and agent brand.

Users can inspect effective access before launch, keep a live sandbox session across application restarts, and deliberately replace its shell when changing protection. Unsupported or uncertain states remain visibly unknown or unavailable; they never silently turn into an unrestricted shell. The feature is filesystem-only and does not misrepresent diagnostic observations as proof of denial.

## 2. Target User

### 2.1 Jobs To Be Done

- As a local developer, I want a shell and the programs it starts to have only the filesystem access I approved, so an unfamiliar or agent-operated command cannot freely modify or read the rest of my account.
- As a nocx user, I want to understand the effective grant and its limitations before committing, and to retain the same protected process when reconnecting after an application restart.
- As an operator, I want explicit errors rather than an apparently successful protection mode when the native mechanism or helper generation cannot enforce it.

### 2.2 Non-Users (v1)

- SSH/remote panes are not supported for Enforce.
- This is not a network sandbox, container/VM, complete process isolation, kernel-exploit defense, or protection against arbitrary host processes running as the same user.

### 2.3 Key User Journeys

- **UJ-1. Alex reviews and starts a protected local shell.** Alex opens Settings → Sandbox or the pane shield, chooses standard or workspace defaults, reviews a pane-bound preview of effective read-only/read-write roots and limitations, and confirms. The shell starts only after native enforcement is ready. If capability or policy preparation fails, the existing shell remains current and the user receives an actionable error without an ordinary-shell fallback.
- **UJ-2. Alex reconnects to a live protected process.** After restarting nocx, Alex selects the pane. If the exact helper session is live and its launch/grant metadata match, nocx attaches to the same session ID and process under the same grant. If it has ended, nocx shows an explicit dead state and requires confirmation for a new launch; it never silently opens an ordinary shell. If the helper cannot establish exact state, the pane stays unknown/pending.
- **UJ-3. Alex adjusts access after a denied operation.** In the contextual diagnostic inbox, Alex reviews an observed attempt and its source/precision, then saves a canonical root as a future profile rule. The running grant remains unchanged; using the new access requires a separately previewed and confirmed replacement launch.
- **UJ-4. Alex removes protection.** Alex chooses Remove and confirms the explicit transition to Off. The same pane is rebound to a new ordinary shell only after the replacement succeeds; failures before commit preserve the original session and its protection.

## 3. Glossary

- **Profile** — Mutable defaults for future launches; either the standard profile or a workspace override.
- **Effective policy** — The complete canonical read-only/read-write root set after user rules, workspace, runtime, system, executable, and Git-derived requirements are combined and conflict-checked.
- **Launch** — One explicitly selected shell-process incarnation in a pane, with a fixed mode and policy.
- **Grant** — Immutable authority for one Enforce launch. An Off launch has no filesystem grant.
- **Preparation** — A bounded, expiring, pane- and source-bound preview of the policy and native capability used by the confirmed launch.
- **Replacement** — Apply, relaunch, or Remove in the same pane, committing a candidate session before retiring its source.
- **Unknown** — State that cannot be resolved from an exact, successfully queried helper generation; it is never equivalent to Off or dead.
- **Diagnostic attempt** — A bounded observation of a filesystem operation attempt. It may predict policy outcome but is not authoritative evidence that the kernel denied it.

## 4. Features

### 4.1 Profiles, policy preview, and authority

Users configure standard defaults and sparse named-workspace overrides. A workspace without an override inherits standard settings; first edit creates a copy-on-write override, and reset restores inheritance while preserving monotonic revision history. The backend resolves workspace from the pane, not from a UI-supplied identity. CAS conflicts require reload rather than overwriting a newer revision. Profile changes affect future launches only.

The pane-bound preview shows source and revision of defaults, canonical effective roots with provenance, mandatory roots, isolated temporary HOME behavior, OS limitations, and unsupported conditions. It validates the complete effective policy, not only user-entered paths. A confirmation is bound to pane, source session incarnation, revisions, and one prepared native policy; expiry or any mismatch requires a fresh preview and confirmation.

#### FR-1: Manage future-launch profiles

Authorized users can view/update/reset standard and workspace profiles with expected revisions. The system stores standard feature enablement and its root lists atomically. Workspace overrides are sparse and resettable. Stale revisions are rejected and the current snapshot can be re-read. Disabling new Enforce launches does not revoke or remove a live grant.

**Consequences (testable):**

- A profile edit never changes the effective grant of a running process.
- Standard updates do not overwrite workspace overrides; reset removes the override and advances its revision.
- A workspace identity supplied by the client cannot redirect a pane's profile or grant.

#### FR-2: Preview effective policy and fail closed

The user can request a preview for a specific pane and expected source session. The system canonicalizes all policy inputs and reports effective RO/RW roots, their provenance, native capability, and limitations. The system rejects invalid paths, root conflicts, reserved-path overlap, unsupported platform/capability, missing runner, and bounded-input overflow. No Enforce request may silently downgrade.

**Consequences (testable):**

- Canonical duplicates are deduplicated; conflicting RO/RW roots and unsupported nesting are rejected, and allowed RW-under-RO is explicit.
- Aliases and symlinks are canonicalized and checked against reserved coordinator/helper/config/credential/state subtrees; an ancestor allow cannot bypass a reserved child.
- Invalid, oversized, stale, or expired preparation produces no shell and no authority.

#### FR-3: Issue immutable launch authority

On confirmation, the backend creates a launch-specific immutable grant from the same prepared policy that is used to start the process. The grant captures its policy version/digest, canonical roots/provenance, profile revisions, workspace identity, and native enforcement facts. Grant authority is scoped to that process incarnation and cannot be edited, moved, or replayed onto another launch. Explicit Off launch records confirmed removal without creating a filesystem grant.

**Consequences (testable):**

- Pane kind, `ephemeral`, current profile content, UI shield state, and workspace membership are never authority.
- A profile edit affects only a future confirmed launch; old grants remain inspectable and unchanged.
- Unknown or mismatched grant version/digest prevents protected adoption and new unrestricted spawn.

### 4.2 Native filesystem enforcement and boundary

Enforce means mediated filesystem operations by the shell and its fork/exec descendants are restricted by the selected OS to the confirmed effective RO/RW roots. RO permits reads/execution but not policy-mediated writes. RW permits file-content writes and truncation, creating and removing files/directories, and policy-mediated name operations (rename, hard-link, and symbolic-link operations) within the granted roots, subject to the selected OS mechanism. These are location-based grants, not complete metadata lockdown: nocx does not promise to block every chmod or ioctl, access through already-open file descriptors, or access to pre-existing hardlinks. The native mechanism is established before shell exec and inherited by descendants. The Linux implementation requires Landlock ABI 9 and the fixed v9 path-right set; macOS uses Seatbelt and a constrained launch shim. If either platform cannot prove the needed capability for that launch, Enforce is unavailable. Windows reports unsupported; it does not emulate Enforce.

Every Enforce launch uses a per-launch owner-only isolated HOME and temporary directories. Only explicit eligible workspace/user descendants may be projected into that HOME; host credentials, shell configuration, history, and arbitrary dotfiles are not copied or opened by default. Mandatory workspace/runtime/system/executable roots are visible in preview. If mandatory writable roots intersect reserved nocx authority/control trees, preview fails rather than attempting a deny carve-out.

#### FR-4: Enforce grant over actual process tree

The system starts a local shell only after native policy setup and exec readiness both succeed. The restriction applies to the shell, grandchildren, and arbitrary executed programs, including agent shell/eval subprocesses, without brand-specific launchers or cooperation from command parsers. Runtime HOME/TMP paths are isolated and available only to that launch. TCP/UDP remain available and outside this feature's promise.

**Consequences (testable):**

- On RW roots, create/write/truncate/remove and directory/name operations (including rename, hard-link, and symbolic-link cases) follow the displayed grant; on RO roots, mediated mutation attempts fail. Acceptance separately checks cross-root rename/link behavior and records the native backend's policy outcome. Metadata-only changes, every ioctl, pre-opened descriptors, and pre-existing hardlinks are not represented as universally blocked.
- Shell → fork/exec child → Python/Bun or agent-launched shell retains the same native restriction.
- Missing/old native capability, runner failure, malformed policy, failed readiness, or shell exec error returns an error and does not start an unrestricted shell.

#### FR-5: Protect nocx-owned pathname IPC

An Enforce child cannot use nocx-owned local pathname Unix endpoints to access helper spawn/attach/control or coordinator discovery/token flows, including endpoints served by an older same-UID helper generation. Nocx's trusted coordinator continues to use those services. This endpoint boundary is mandatory alongside filesystem policy; UI origin checks, directory mode, or agent-tool admission alone are insufficient.

**Consequences (testable):**

- From the restricted process tree, direct helper spawn/attach and coordinator hello/discovery attempts fail, including with an already-running old helper endpoint.
- An ordinary trusted coordinator can still communicate with its helper.
- Nocx agent-originated coordinator tools for a sandboxed session are denied server-side; stale admissions are revoked at replacement.
- TCP/UDP connectivity and Linux abstract sockets are explicitly not claimed to be blocked.

### 4.3 Replacement and recovery lifecycle

Apply, relaunch, and Remove replace the process in the same pane. Pane/tab/layout, ledger identity, saved history, editor, and draft remain pane-owned. The source session remains current until the replacement candidate is natively ready and durably committed. No previous command is replayed, no terminal transcript is injected into the new process, and switching panes cannot retarget an open confirmation.

| State / failure point                                                    | Required behavior                                                                                                                                                                                                         |
| ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Before commit; preview, validation, helper, native setup, or spawn fails | Keep source session and protection current; close/discard only the candidate and clean its temporary resources where exact state is available. Never fall back to ordinary shell.                                         |
| Candidate succeeds and durable commit completes                          | Select candidate as the pane's new session; persist new launch head and source retirement obligation; then close the exact source helper session. Close failure stays pending and visible, never rolls back the new head. |
| Coordinator crashes before commit                                        | On recovery, roll back uncommitted candidates, preserve previous head, and do not resume spawning automatically. Unknown helper state stays pending/unknown.                                                              |
| Coordinator crashes after commit / response is lost                      | Resolve through durable operation/head state, publish the committed session idempotently, and retry exact source close. A retry never creates a second process.                                                           |
| Source shell is live after app restart; helper exact generation queried  | Adopt same helper session ID, process, launch and grant; do not rebuild policy, spawn, or issue a new grant.                                                                                                              |
| Exact inventory confirms process ended                                   | Show explicit dead state/history. A new process requires a fresh explicit confirmation and new grant if Enforce is selected.                                                                                              |
| Helper unavailable, query timeout, generation or metadata unknown        | Keep pane unknown/pending; do not infer Off/dead, adopt ordinary, or spawn shell.                                                                                                                                         |

#### FR-6: Replace sessions transactionally in the same pane

A user can Apply or Relaunch with a newly previewed Enforce grant and can Remove only through an explicit Off replacement. A successful replacement preserves pane identity and history while binding a fresh session. The client cannot commit against a different source session or stale profile/workspace revision.

**Consequences (testable):**

- Pre-commit failure leaves source subscriptions, input draft, and foreground session intact.
- Post-commit source-close errors remain a durable pending cleanup and cannot restore the old session as current.
- Retrying a lost operation response returns its original result; it cannot spawn twice.
- Apply/Remove never replays the last terminal command or clones a live VT grid.

#### FR-7: Restore exact live launch and fail closed

After application or coordinator restart, the system restores a live launch only when exact helper identity, live process state, launch metadata, grant digest, and policy version match. A dead launch is not revived by its durable head. Unknown, mismatched, unsupported, or absent authority cannot be treated as an ordinary shell.

**Consequences (testable):**

- A live sandbox reconnects with the same helper session ID, process, and grant.
- Exact inventory with an exited entry is shown as ended, not live; it can be drained for history without input or tool admission.
- Dead requires explicit user action and fresh preview/confirmation for a new launch; unknown requires recheck, never a shell fallback.

### 4.4 Settings, shield, and contextual diagnostics

One Settings → Sandbox surface manages availability, standard/workspace profile selection, contextual pane grant and preview, and bounded diagnostics for the selected launch. A pane shield is a contextual action, not a fake navigation view: Off, Enforce, unknown/error, and unsupported/no-pane states are distinct. Selecting it opens the same Settings surface pinned to that pane. Exact `/sandbox` in a local shell-routed command editor opens the same surface without entering the PTY, command history, or ledger; other strings and non-shell targets are unaffected.

Diagnostics are best-effort, bounded, and tied to a launch. Linux records observed file-operation attempts with prediction rather than guaranteed kernel errno; macOS records available filtered system-log observations. Missing or failed observer never weakens enforcement. Saving a proposed path canonicalizes it and changes future profile defaults only; it never widens the running grant or retries a command.

#### FR-8: Expose truthful state and contextual control

Users can inspect protection availability, live launch/grant summary, source profile/revisions, observer capability, and immutable grant details. Settings and shield stay available to inspect a running grant when new Enforce launches are disabled. SSH/unsupported/unregistered panes cannot imply Enforce availability.

**Consequences (testable):**

- Shield cannot show active protection based only on profile settings or a stale UI flag.
- Shield and `/sandbox` open the same context-bound Settings screen; exact `/sandbox` is not sent to shell, history, or ledger.
- `/sandbox arg`, embedded/multiline non-exact text, and agent/chat targets are passed through unchanged.
- Cancel, stale context, and a second Enter while async interception is active do not clear or submit the draft or open duplicate dialogs.

#### FR-9: Provide bounded future-policy diagnostics

Users can list/resolve diagnostic attempts for the selected launch with source, precision, counts, dropped count, and revision/cursor. A resolution can dismiss or propose RO/RW future-profile access only after canonical identity is rechecked and the profile revision is current. Resolution has no authority over the current grant.

**Consequences (testable):**

- At most 500 inbox records are retained per launch, list pages are at most 200, and at most 32 resolutions are provisional at once; overflow increments dropped counts and does not stall filesystem operations.
- Missing observer, helper loss, unreadable/racing paths, and unknown outcomes are labeled unavailable/unknown, never presented as proven denials.
- Path substitution or concurrent profile update rejects the resolution; resolving the same event cannot commit conflicting rules twice.
- An installed Linux observer's fatal failure terminates only the affected sandbox session with a visible cause rather than silently removing its behavior.

### 4.5 Data integrity, privacy, and delivery

#### FR-10: Preserve existing state and protect sensitive metadata

The feature's configuration and launch state integrate with current nocx storage and lifecycle without destructive migration or silently exposing sensitive paths. Profile path details are returned only over explicit authenticated UI requests; unsolicited events and ordinary logs/metrics/errors carry IDs, counts, codes, and field/index references, not paths, HOME, commands, or environment. Backup/export may include mutable configuration but not transferable live process authority, grant-as-future-launch permission, runtime HOME, or diagnostic inbox.

**Consequences (testable):**

- Ordered migration preserves existing workspace, pane, session, ledger, and execution-grant data; malformed or newer databases refuse safely rather than reset.
- A launch grant is immutable, launch-scoped, and never imported as permission to start or adopt a process on another machine.
- New logs, notifications, generic errors, and metrics contain no protected path, HOME, command text, token, or environment value.

#### FR-11: Ship only where the required capability is available

The supported local platform must ship the runner and required helper generation together through the existing artifact path. Deployment variants that cannot host a supported local helper do not advertise Enforce. Build/package checksums/manifests cover the runner; no runtime shell download supplies enforcement binaries.

**Consequences (testable):**

- Linux and macOS artifact smoke tests execute the real packaged helper/runner path and fail if mandatory native enforcement is skipped.
- A platform unable to exercise its required native backend is reported unsupported/unverified, not passed by mock or cross-platform substitute.

## 5. Non-Goals (Explicit)

- Learn mode, global learning inbox, standalone Statistics screen/tab, or collection while Off.
- OpenCode-specific or other brand-specific launchers and command parser as a security boundary.
- Network isolation, container/VM isolation, full process namespace isolation, Windows emulation, SSH/remote enforcement.
- Protection from kernel exploits, arbitrary same-user host processes, every IPC service, all credential-bearing environment variables, metadata disclosure, all ioctl/chmod, pre-opened file descriptors, or pre-existing hardlinks.
- Guarantee that detached processes created before replacement are terminated or retroactively sandboxed.
- Automatic retry of commands after a diagnostic suggestion or live widening of an existing grant.

## 6. MVP Scope

### 6.1 In Scope

Linux Landlock ABI 9 and macOS Seatbelt local process-tree enforcement; immutable launch grants; Off/Enforce; atomic standard defaults and workspace overrides; isolated HOME; explicit preview/confirmation; same-pane replacement and restore; fail-closed unknown/dead handling; nocx pathname endpoint protection; server-side agent-tool admission denial; shield and exact `/sandbox` interception; bounded, contextual best-effort diagnostics; migration, packaging, and native process-tree acceptance.

### 6.2 Out of Scope for MVP

Learn, separate Statistics, network/Windows/remote sandboxing, broad cross-process containment, automatic retries, and granting diagnostic paths to a running process. These are excluded to preserve the approved filesystem-only authority contract and avoid representing observation or consent as stronger isolation than the OS provides.

## 7. Success Metrics

- **SM-1 (FR-4, FR-5, FR-11):** Native Linux and macOS acceptance demonstrates that actual shell descendants cannot read/write outside effective roots, can use intended roots, and cannot reach nocx pathname helper/coordinator endpoints; ordinary trusted coordinator/helper communication remains functional.
- **SM-2 (FR-6, FR-7):** Lifecycle fault-injection acceptance shows one candidate at most per confirmed operation; live reconnect preserves exact session ID/grant; dead and unknown restore outcomes never produce an ordinary shell.
- **SM-3 (FR-1–FR-3, FR-10):** Migration and policy acceptance preserves prior data, rejects stale/conflicting/reserved rules, and does not leak protected metadata through unsolicited surfaces.
- **Counter-metric (SM-C1):** Do not optimize for “sandbox launch success” by degrading to Off, under-reporting unknown state, widening roots silently, or treating diagnostic attempts as proven denials.

## 8. Cross-Cutting NFRs and Constraints

- **Security:** Kernel enforcement, not UI indication or agent-tool permission scanning, is the authority. Enforcement-ready acknowledgement is separate from PTY output and is required before session publication. Failed preparation/exec cleans only the candidate and cannot silently execute an unrestricted shell.
- **Reliability:** Durable operation/head state and exact helper session identity resolve lost responses and coordinator restarts. Unknown is preserved until an exact helper generation supplies evidence. Source cleanup survives pane closure and remains pending on helper outage.
- **Bounds:** Policy envelope ≤64 KiB; up to 32 user roots per access class before effective expansion and ≤1024 effective roots. PATH/dependency discovery examines at most 16,384 entries; ELF strings are limited to 4 KiB, 256 tags, 1 MiB per section, and 64 MiB total; the dependency graph is limited to 65,536 nodes and depth 64. Limits are checked before allocation/expansion and inputs are rejected, never silently truncated. The Nix-store coalescing fixture includes more than 256 package roots and must not expand write authority. Linux diagnostic collection bounds tracee argument/cwd/dirfd reads; macOS collection is bounded and filtered to launch/session identity. Before implementation, architecture must set explicit finite per-event read/work budgets for those collectors and define overflow behavior without blocking syscalls. FR-9 sets the diagnostic inbox, page, and provisional-resolution caps and overflow behavior.
- **Privacy:** Paths and process-specific metadata are `PrivateMetadata`; explicit authorized detail retrieval is permitted. No private roots, HOME, command text, environment, or confirmation token in generic logs, notifications, metrics, or error strings.
- **Interaction:** Confirmation remains bound to the originating pane/source/revisions despite active-pane changes. Async `/sandbox` interception is single-flight; refusal/cancel preserves the draft.
- **Platform:** Linux Enforce requires ABI 9; macOS requires usable Seatbelt with launch-specific Unix-socket constraints. Windows and remote sessions report unsupported with reason. TCP/UDP remain out of scope.

## 9. Acceptance Summary

Release acceptance requires all of the following against the real production paths, not source-text checks or mocked RPC echoes:

1. **Actual native process tree:** Real local helper/runner starts a shell and grandchild, execs a non-shell executable (including Python/Bun or an agent shell/eval path), and verifies writes inside RW succeed, RO writes fail while reads succeed, outside sentinel reads/writes fail, and intended RW-under-RO behavior matches preview. Restriction demonstrably persists across fork/exec/reparenting.
2. **Nocx endpoint escape resistance:** From the restricted child, direct spawn/attach against current and old same-UID helper pathname endpoints and coordinator hello/token discovery fail; normal trusted coordinator access succeeds. Agent-originated nocx coordinator tools are denied. Verify TCP to a temporary test server remains available to establish this is not an accidental network sandbox.
3. **Policy/path edges:** Symlink escape/retarget and root substitution are rejected; reserved coordinator/helper/config/credential/state trees cannot be exposed by an ancestor rule; invalid/missing roots and conflicting RO/RW nesting fail; Unicode/whitespace do not widen write authority. Dependency discovery stays within 16,384 entries, ELF string/tag/section/total limits of 4 KiB/256/1 MiB/64 MiB, and a graph of 65,536 nodes/depth 64; the >256-package Nix-store fixture does not widen write authority. Isolated HOME projection collisions fail; PTY works with only allowed devices.
4. **Fail-closed launch:** Unsupported ABI, missing/bad runner, malformed/oversized plan, Seatbelt policy/socket-filter failure, readiness timeout, and shell exec failure create no unrestricted shell, preserve source pane/session, and clean exact candidate resources.
5. **Storage/authority:** Migration preserves old workspace/session/ledger/execution-grant data; stale CAS and wrong workspace/session/digest/version fail; grants cannot mutate/replay onto another launch; resetting workspace override preserves monotonic revision.
6. **Lifecycle:** Crash before commit rolls back candidate; crash after commit restores the exact selected head and retries source close; lost response never duplicates spawn; live coordinator restart reconnects same process/session/grant; exited process shows dead; helper timeout or generation uncertainty stays unknown; neither dead nor unknown silently opens shell.
7. **Diagnostics/privacy/UI:** Enforce the architecture-defined per-event Linux tracee-read and bounded identity-filtered macOS collection budgets; inbox/page/provisional caps and dropped counters are correct, and overflow does not stall syscalls or imply no denial. Live-profile suggestions require later preview/relaunch; event/path substitution and duplicate resolution are rejected; exact `/sandbox` and shield open one Settings context without PTY/history/ledger submission; cancel/double-enter/stale pane are safe; sensitive paths/tokens/commands/env stay out of unsolicited messages and generic logs.
8. **Native artifacts:** Existing packaging includes the matching runner/helper and verifies manifests/checksums; Linux and macOS packaged smoke targets exercise the actual native backend. A platform with no native host evidence remains unverified, not passed.

## 10. Open Questions

1. **Architecture gate:** What explicit per-event byte/work limits will bound Linux tracee argument/cwd/dirfd reads and macOS filtered-log collection? The approved plan requires bounded collection but gives no numeric limits. Define finite budgets and overflow behavior before implementing collectors; overflow must not stall syscalls or be presented as a proven denial.

## 11. Assumptions Index

- No user-detail assumptions were added beyond the approved plan and task-provided direction. Runtime implementation evidence is intentionally not claimed.
