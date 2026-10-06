---
title: Main-based local filesystem sandbox
status: final
created: 2026-10-05
updated: 2026-10-05
---

# Local filesystem sandbox — implementation contract

## Authority and scope

Implement from main `7e60042546581fde39035dce29110cd3e6a684ac`, not by rebasing,
cherry-picking or copying PR #91. The approved rewrite supersedes its integration
points, not its requirement for a shell usable with arbitrary local CLI programs.
Tracking: `nocx-a0qhd.10` through `.16`. Inherit AD-1…AD-10 and ADR-0011,
ADR-0055, ADR-0057, ADR-0066 and ADR-0068. ADR-0058 remains Proposed; it is not
an adopted native filesystem contract. New decision record: ADR-0079.

Only the helper's **child shell and fork/exec descendants** are restricted. The
coordinator and shared helper daemon remain trusted and unrestricted. Native
Enforce means kernel-mediated operations obey the immutable launch's effective
RO/RW roots. RO permits read/execute; RW permits working file operations. Roots
outside the policy do not gain those mediated rights. There is no network sandbox,
container, VM, comprehensive metadata hiding, all-IPC isolation, protection from
kernel exploits or arbitrary same-user host processes, or automatic environment
credential removal. Existing open FDs/hardlinks and unmediated operations are not
promised to disappear. GUI files/git/completion remain trusted user operations.
Agent-originated coordinator tools from an Enforce session are refused server-side.

Local Linux/macOS only; SSH Enforce is refused. Windows is unsupported. Missing
runner/mechanism/helper capability is an Enforce error, never ordinary launch.
No Learn, separate Statistics tab, agent-brand launcher, live policy widening,
automatic command retry or command replay.

## Data ownership

`internal/sandbox` owns typed modes, profiles, canonical effective policies,
serialization/version/digest and the feature repository. It must not import app,
helper or terminal runtime packages. ContentDB owns launch authority/lifecycle
records through its existing short single-writer transaction path. Helper owns
shell choice, environment facts, compiled native preparation, process lifetime,
runtime directories, pinned root FDs and bounded observations. UI owns presentation
and explicit confirmation; it never supplies authoritative workspace membership,
session IDs or a second containment implementation.

### Mutable configuration

A bounded typed `sandbox.json`, via `storage.DocumentStore`, carries
`schemaVersion`, durable `revision`, `enabled` (initially false),
`readOnlyDirs: []`, `readWriteDirs: []`. Standard roots initially empty. Mode is
not stored in root lists. Do not use JSON-in-string settings or independently
written scalar keys. Validate at most 32 roots/class before copying or expanding.

Workspace payload has a typed sparse sandbox field `{revision, override}`;
`override: null` means inherit standard, not a copied profile. Named workspace's
first edit copies the current standard into an override. Reset increments the
workspace clock even when removing an override; clocks never reset to zero.
Default workspace uses standard. Preserve unrelated workspace payload fields.
Backend resolves membership from pane in grant issuance; edit RPC targets are
validated, not authority for a pane in a different workspace.

Every mutation is CAS. A feature-owned short config mutex encloses the standard
revision check and ContentDB transaction that verifies workspace revision and
membership; standard writes use this same mutex. Native preparation/filesystem IO
must not run while it is held. Profile edits never change issued grants. Error
payloads contain bounded code/field/index, not paths. Paths are PrivateMetadata:
explicit authenticated detail RPC may return them; unsolicited events, logs,
metrics and error strings may not. Backup/export copies only mutable config with
that classification, never live bindings, grant-as-new-launch authority, runtimes
or inbox; imported config cannot automatically adopt/start a process.

Backup restore enters a feature-owned exclusive restore scope before its inner
preview-token check. The short mutex records the active scope then is released:
other sections' filesystem restore does not run under the config mutex. Normal
profile writes and grant CAS refuse while that scope is active. Only its private
scoped-restorer capability may import/rollback mutable profiles, advancing local
clocks; the capability expires at callback end/error/panic. Reads remain available.
Internal rollback uses the same capability, not a nested scope. Cold-start recovery
restores settings/connection prerequisites before ContentDB opens, preserves the
prepared journal, then completes workspace/config rollback before transport starts.

### Schema 21 → 22

Use the ordered transactional migrate-or-refuse ladder. Pin the released v21
fixture/catalog shape before changing schemaV1. Preserve IDs, layout, session,
ledger and execution grant/scopes/effects rows. Do not reset or import experimental
PR databases. Existing execution grants retain their expiry and effect semantics.

- `pane_launches`: backend-minted text `id`, `pane_id` FK, operation identity,
  source full session identity, nullable future session binding, mode Off/Enforce,
  state preparing/active/ended/failed and timestamps. Partial unique indexes allow
  at most one preparing and one active per pane. This records explicit replacement
  operations, not every ordinary session.
- `pane_launch_heads`: pane PK/FK → selected launch. Head remains after shell death:
  process absence is not permission to turn Enforce into Off. Pane deletion removes
  its layout-dependent metadata.
- Extend `authority_grants`: nullable unique `execution_id` or nullable unique
  `launch_id` FK, CHECK exactly one subject. Execution expires_at remains non-null;
  launch expires_at is null, its authority lasts only for that process. Preserve
  integer grant IDs/version/issued_at and immutable JSON policy. Subject selects
  policy type, never JSON heuristics. Scopes/effects remain execution-only; all
  queries must filter execution subject. Launch grants are insert/read, not update.
- Launch binding stores helper HostSessionID, exact install generation, policy
  version and digest. No adoption of unknown policy version or mismatched digest.
- `session_retirements`: full HostSessionID/install generation, operation/cause and
  close_pending. Unique full identity; **no pane cascade FK**. Includes ordinary
  source of first Apply and uncommitted candidates; delete only after confirmed
  close/no_such_session/exact-generation inventory absence.

Off has no native filesystem grant. A confirmed Remove still records an Off
launch/head, so an old Enforce head cannot revive on restart. Feature disable
forbids new Enforce launches, but never removes a live restriction or its status.

Launch policy snapshot includes workspace identity/root, source profile revisions,
canonical user/mandatory roots/provenance, backend/version, isolated runtime
locations and digest. Observations and mutable status do not mutate that snapshot.

Digest version 1 is SHA-256 of the versioned typed policy JSON with digest omitted,
no maps, deterministic field order and roots sorted by class/canonical path/
provenance. Grant ID and mutable enforcement/observer state are not digest inputs.
Coordinator verifies the helper's encoded policy bytes/version/digest, not a
separately rebuilt list; malformed/unknown envelope refuses. Object identities
are part of prepare/launch validation; native descriptor numbers are not authority.

## Canonical effective policy

Inputs: backend-resolved pane local workspace/CWD, host HOME, profile roots,
optional launch delta, helper-selected shell. Expand `~` against real host HOME
before substitution; relative paths only against explicitly shown workspace root.
Reject empty/NUL/newline/nonexistent/non-directory user roots. Canonicalize
symlinks and use platform-aware containment. Validate the **entire final policy**:
workspace, Git roots, profiles/delta, system/dependency/runtime/artifact roots.

Reserved coordinator/helper control/config/credential/state subtrees and ancestors
must never be opened by user or mandatory roots. No deny-child carve-outs beneath
a broad Landlock allow. Only typed exceptions: this launch's private runtime RW,
and necessary trusted executable files RO. Trusted artifacts must never be under
any effective RW root. Reserved-root workspace (HOME, .config, app data directory)
is refused in preview/prepare with narrower-workspace guidance. All aliases share
canonical validation.

Remove duplicates within class. Same canonical root in RO/RW conflicts. RO nested
inside/equal to RW conflicts; RW inside RO is allowed and visible. Check conflicts
before coalescing same-class ancestors. Mandatory workspace/runtime RW conflicts
with user RO are refused, not silently promoted. Git worktree common-dir RW needs
bounded reciprocal .git/backlink validation and a visible external preview root.
No arbitrary RW from untrusted .git text.

Limits: 32/class/profile or delta; ≤1024 effective roots after expansion/coalescing;
≤64 KiB policy envelope. Refuse excess, never truncate. Dependency discovery never
executes ldd/shell snippets/discovered binaries. Bound ≤16384 PATH entries, ELF
strings 4 KiB, 256/tag, section 1 MiB, aggregate 64 MiB, graph 65536 nodes/depth64.
Nix fixture must support >256 package roots without write expansion.

### Runtime and baseline

Owner-only 0700 runtime per Enforce launch. HOME/XDG_CONFIG_HOME/XDG_DATA_HOME/
XDG_CACHE_HOME/XDG_STATE_HOME/TMPDIR/TMP/TEMP point inside it.
NOCX_SANDBOX=filesystem is a hint only; keep current environment scrubber and warn
that other environment credentials are not isolated. Runtime survives app restart
with helper process, removed only after confirmed retirement/exit, not app sweep.
Warn that durable work belongs in workspace/explicit RW roots.

Projection is a nofollow-dirfd symlink forest only for explicitly authorized user/
workspace roots strictly under canonical host HOME. No host-dotfile copy, rc,
history, .ssh/keychain/.config auto-read. System/PATH/helper/runtime/Git roots do
not project. HOME/ancestors do not project. Owner/mode/identity validation,
collision or runtime-tree intersection fail; target already has RO/RW authority.

Versioned visible baseline (only existing paths): Linux RO /usr,/bin,/sbin,/lib,
/lib64,/etc,/dev,/proc,/sys,/nix/store. macOS RO /usr,/bin,/sbin,/System/Library,
/System/Volumes/Preboot/Cryptexes,/Library/Developer/CommandLineTools,/etc,/dev,
/private/etc,/private/var/db. Device RW/ioctl only /dev/null,/dev/zero,/dev/random,
/dev/urandom,/dev/tty; no broad /dev/pts or host TMP RW. Shell/runner and loader
artifacts RO. Explicit PTY stdio/bootstrap/lifecycle FDs remain functional.

## Native boundary

Separate minimal `cmd/nocx-sandbox-runner`, CGO_ENABLED=0, without app/helper/VT
imports. Linux go-landlock v0.9.0 requires ABI9, fixed V9.RestrictPaths; no
BestEffort/RestrictNet/RestrictScoped. Handle RESOLVE_UNIX but grant it to no root.
Host-created pathname Unix endpoints (including old helper generations and
coordinator discovery) are blocked regardless of being under RW roots. Same-domain
new sockets work. TCP/UDP and abstract sockets remain outside this contract.

Pin validated Linux roots using O_PATH descriptors, preserve object identity
through prepare/apply and use /proc/self/fd/N for library rules, never re-resolve
user paths. Apply+exec on one locked OS thread. Close policy/rule FDs before exec.
macOS uses runtime-probed /usr/bin/sandbox-exec, deterministic escaped filesystem
policy and explicit outbound pathname Unix deny outside private launch runtime;
bind/connect inside runtime works. No TCP/UDP/process blanket sandbox. Seatbelt
path identities rechecked before spawn, not claimed equivalent to Linux pinning.
Native escaping/socket semantics must pass probe or Enforce is unsupported.
Seatbelt read rules include exact ancestor-directory literals for native path
traversal (including `/`), never ancestor subpaths or additional writable roots.
Ancestor directory names may be enumerated; descendant file contents remain
denied unless the effective policy grants them. Native RO/RW and outside-read
probes must still pass with this platform baseline.
Darwin device roots retain their parent namespace descriptor and verify the
actual character-device identity with no-follow `fstatat`, before native apply
and in the restricted shim. Opening `/dev/tty` is not a prerequisite for a helper
without a controlling terminal. Parent descriptors remain private and are closed
before the shell; Linux still pins each device directly with O_PATH.

Policy via anonymous/unlinked owner-only file or pipe inherited FD, never argv/env
or readable policy pathname. macOS `-f /dev/fd/N`, never `-p` private policy.
For macOS, the runner sends an unlinked bounded profile source and anonymous pipe
writer via SCM_RIGHTS on private FD6. The ordinary helper relays the source with
the same startup deadline and closes the writer; sandbox-exec consumes only the
inherited reader. Received FDs become CLOEXEC under the nonblocking ForkLock
critical section before any concurrent ordinary spawn can inherit them. This is
profile provisioning, not a publication/commit acknowledgement. There is no
additional producer process, readable profile file, or private-policy argument.
Minimal restricted shim closes policy FD before shell. Keep existing bootstrap/
lifecycle FD numbers; add new ones after them. Allowlist inherited FDs; no
preconnected helper/coordinator/control socket or renderer token enters shell.
No unrestricted directory FD enters shell.

Native readiness is separate from PTY: applied-policy confirmation **and** exec
success using CLOEXEC error channel, timeout30s; premature EOF/exit is failure.
Single unwind kills only candidate, closes FDs and removes unused runtime. Source
survives failure. No ordinary fallback. Same-UID/0700/UI-origin checks do not by
themselves isolate a sandbox child: pathname socket deny is mandatory acceptance.

## Frozen helper operations

Do not extend old spawn/sessions/SessionEntry wire shapes. Add operations and closed
schemas to session service; old generation unknown_op is unsupported, not Off.

- sandbox-prepare: discriminated Off/Enforce intent, bounded roots/CWD and launch/
  operation correlation, no shell/argv. Enforce creates runtime/native policy and
  returns canonical snapshot/digest/status + opaque single-use token. Off reserves
  launch ticket only.
- sandbox-launch: existing ticket, geometry/lifecycle/launch identity; Enforce also
  persisted grant ID/digest. Return unchanged SessionEntry/replay contract. Payload
  mismatch refused. Consume atomically before one attempt; never new spawn on replay.
- sandbox-get: full HostSessionID → correlation/mode; Enforce grant/digest/version/
  enforcement/observer, Off explicit absent grant. No old inventory shape change.
- sandbox-discard: idempotent unused-preparation cancellation.
- sandbox-access-list/resolve: bounded diagnostic metadata/bookkeeping only.

Pending preparations ≤32, TTL60s; consumed result cache ≤1024/TTL10min. Keep live
session launch correlation. Evicted/expired/unknown/restarted ticket cannot become
fresh. Ordinary spawn's retry semantics are unsuitable for confirmed Remove.

Refactor hostedSpawn.run into shared internal openCandidate and publish stages.
Ordinary open runs both; replacement keeps candidate outside registry selection,
admission and public screen/block/session events until commit. Private attach/
lifecycle drains do not publish. pty retains PTY/setsid/signals ownership; runner
is a typed native launch strategy, not duplicate shell launcher.

## Coordinator RPC and replacement

Authenticated trusted UI only; deny mutations to agent channels. Closed schemas,
OpenRPC registration, generated TS bindings and existing error envelope:
status; profile.get/update/reset; preview; replace; operation.get; grant.get;
access.list/resolve. Detail paths only on explicit reads. Preparation/confirmation
tokens are not logged; grant IDs are not bearer capabilities.

Preview pins pane, expected source incarnation, backend membership, standard/
workspace revisions, delta and exact helper preparation/digest. Bounded32/TTL60s;
expiry/cancel discards. Replace takes confirmation identity+operation ID, not
mutable duplicate roots/mode. Recheck identities/revisions/membership/feature/
locality/store availability and preparing uniqueness; conflict needs new preview.
Do not reconstruct runtime beneath a confirmed digest. After confirmation operation
is durable/server-owned and outlives UI disconnect; explicit cancel only.

Transition: preview warning that successful replacement closes source shell;
daemons detached earlier do not magically become restricted or all die. Create
preparing claim+immutable grant from same preparation in short CAS transaction.
Open private native candidate while source remains current. One ContentDB commit
records binding/active launch/head, ends old launch and source retirement, guarded
by expected source and live pane. This is the sole selection commit point. Conflict
or closed pane closes only candidate; failed candidate close records retirement.
Serialize the selection commit with the source's input/admission fence: drain
already-admitted writes before commit, then mark source retired and revoke its
admissions before releasing the fence or publishing candidate. Every input route
(WS structured input, paste/keys and coordinator delegated writes) checks the
trusted current-incarnation fence; stale source handles refuse new writes even
while helper close is pending. Read/lifecycle drains may finish. On crash durable
retirement supplies the same rejection before any ordinary readoption.
Publish committed session idempotently; return standard open acknowledgement.
Use TerminalContent.bindSession(handle) with **no open/reconnect RPC**. Teardown
old session callbacks/projections; preserve pane/tab/layout/ledger/history/draft/
chrome, add divider, do not import old live grid into new runtime. Input paused
only across rebind. Close exact source from durable retirement; failures stay
pending without reverting committed foreground. Pane deletion does not erase close.

Lost response queries operation/head with same operation ID, never fresh launch.
Crash recovery rolls back **all uncommitted** candidates, never resumes spawning;
cleanup/helper unknown retains pending claim, prevents another candidate.
Outstanding uncommitted candidates are bounded one/pane and 32/helper. There is
no helper-side lease/finalize acknowledgement that could kill a DB-committed
shell after coordinator crash; coordinator reconciliation owns rollback.
Permanently unavailable exact generations remain explicitly pending without
claiming process death; no timer or missing installation substitutes close proof.
Committed head readopts and source retirement closes. Classify candidates/retired before
generic readoption; neither becomes selectable/admitted. Remove follows same
protocol with Off ticket and explicit protection-removal warning. Disabled feature
is not Remove; apply/relaunch never replay prior command.

### Restore and admission

Live requires exact successfully queried helper generation, entry.Exit==null,
same session/launch/grant/digest/version. Attach same PID/session without builder,
spawn or new grant. Exited inventory row is ended; drain-only no input/admission.
Exact confirmed absence is dead with history/grant inspect/explicit new launch.
Unavailable/timeout/unknown generation is unknown/pending with recheck, not Off.
Missing/corrupt grant or mismatch is fail-closed recovery error. Ordinary open for
pane with Enforce head refused server-side. Frontend adoption/open/reconnect use
discriminated recovery outcomes, never null/error → ordinary shell fallback.

worker_auth peer and pane/bearer paths check trusted launch provenance **before**
admission. Enforce denies all coordinator tools including workers.spawn and
session keys/read/message; unknown provenance also denied. Revoke old admissions
on replacement. Removing agent socket/token from bootstrap is defense in depth,
not native boundary. History remains ledger/backend-runtime owned.

## Diagnostics

Enforce observer active/unsupported/failed is separate from enforcement. Off does
not observe host activity. Linux helper owns seccomp USER_NOTIF openat/openat2
collector, bounded tracee argument/cwd/dirfd reads and always CONTINUE; observations
are attempted operations plus policy prediction, not proven errno/Landlock denial.
Unreadable/race is unknown, never fabricated path. Protected listener handoff FD
runner→helper; installed listener fatal failure ends only affected session because
silently abandoning it changes syscall semantics. Overflow still CONTINUE.
The notification path copies at most 4 KiB each for attempted path, cwd, dirfd
target and executable identity, at most eight remote/proc reads per event and
24 bytes of open_how. It replies CONTINUE before asynchronous prediction or
canonicalization; a bounded queue of 64 entries/four workers drops excess with
counter increment. Unreadable/non-terminated/oversized data is unknown, not truncated
into an accepted path. macOS log records are limited to 16 KiB/line, 256 KiB/batch,
64 records/batch and the same bounded processing queue; oversize/dropped input is
counted without growing buffers. A line collector discards through newline using
fixed-size chunks, never scanner allocation proportional to an attacker record.
macOS uses bounded nonce/session-filtered Seatbelt unified-log observations and
explicit source/precision. The nonce annotates existing filesystem-deny rules,
never replaces `allow default` with a process/Mach/IP default-deny policy;
unavailable logs do not weaken policy.

In-memory500 records/launch, pages≤200, resolving slots≤32; coalesce by launch/
executable/path/operation/access. Monotonic revisions, observed/dropped counts,
source/precision and discontinuity after helper loss. Notifications IDs/counts only.
No terminal stderr parsing. Resolve accepts eventID/decision dismiss/allowRO/allowRW
and expected profile revision, not arbitrary path/workspace. A list-time proposal
binds the event revision, canonical existing directory, device/inode identity,
whether the attempted target was missing and the existing parent selected, and
the observed spelling used for canonicalization. The UI displays that exact
proposal directory/class before resolve. Resolve rechecks the same target or
parent identity and canonical spelling: changed/disappeared/newly-created target
conflicts and requires a refreshed proposal and confirmation; unresolved/unknown
path and socket events cannot offer allowRO/allowRW. Never substitute a different
nearest parent after confirmation. Reserve a resolution slot then release inbox
mutex during store IO, CAS commit once; conflicting double resolve cannot issue
two updates. Save only future workspace override/default standard; running grant
unchanged, command not retried. Relaunch requires separate native preview/confirmation.

## UX and verification

One existing Settings Sandbox section: availability/toggle; standard/named-workspace
profile source/revision/editor/reset; pinned current pane mode/grant/default drift/
Apply/Relaunch/Remove; contextual inbox with source/precision/drop and grouping.
Use existing Dialog/RecordRow/CollectionView/StatusCard/EditableRowList/IconButton/
Select/sections/toasts/tokens, no alert/confirm/prompt or new modal framework.
Loading/CAS conflict/errors/Cancel/Escape/focus restoration are acceptance.

Typed top contextual shield action after Files, not navigation view. Off outline,
Enforce active, unknown/error distinct. Unsupported SSH/no registered pane disabled
with reason. Click opens same Settings pinned context, never toggles rights. Live
status/inspect/Remove remain visible when feature disabled. Keyboard/tooltips use
existing sidebar navigation.

CommandEditor hook pass/consumed/refused (sync/async) runs before secret planning,
history/ledger/input; in-flight guard includes it and beforeSubmit. Only exact
trimmed /sandbox on routesToShell consumed; agent/chat, /sandbox arg and other
multiline content pass. Consumed clears only still-matching draft; refusal/error/
cancel preserves it; repeated Enter opens no second dialog. Raw terminal has no
backend sniff hook; shield remains entry. Consumed command never reaches PTY or
executed ledger/history.

Each numbered plan slice must pass its behavioral evidence before dependent work.
Native smoke uses bounded temporary sentinels and guaranteed teardown, real helper/
app production RPC; positive/negative filesystem, descendants, current+old nocx
endpoints, ordinary session and TCP. Include symlink/retarget/rename/link/reserved
roots/projection/Nix/PTY/exec-ready failures and privacy. Migration preserves prior
IDs/content and refuses malformed/newer without reset; CAS/reset/immutability.
Exercise every replacement crash/response window, same PID live restart,
exited-inventory dead and timeout unknown. Diagnostics listener-death/overflow/
retarget/double resolve/revision behavior. Real browser Settings/shield/history/
keyboard against native RPC with root e2e stand and coverage collection.

Runner generation must be built/embedded/installed alongside local helper through
existing artifact/checksum/package/signing chain; remote-only helpers do not
advertise local sandbox. Make targets sandbox-smoke-linux, sandbox-smoke-macos and
artifact smoke fail rather than skip mandatory OS backend. Source **and packaged**
Linux ABI9/macOS native smoke are required. Lack of macOS runtime is an unverified
mandatory criterion, never cross-platform completion. Existing Zig0.16/libghostty
prerequisites apply. Final one ci-full set, gosec and frontend npm audit; critical
findings block PR. No merge without separate user request. No destructive DB
rollback: forward fix/refusal, backup recovery only on a copied database.

The Seatbelt re-exec copies its plan/profile descriptors above protocol slots
3–7, including when closing the original plan frees FD5. Native descriptor
inventory refuses leaked directory, regular-file or control-socket capabilities.
The log-context gate excludes zero-argument metadata/error methods, since both
supported logger APIs require a message; real message calls remain gated without
adding a sandbox exception to the baseline.

## Evidence boundary

BMAD product/UX artifacts, architecture spine and rubric/current-versions/
adversarial reviews are recorded under `_bmad-output/planning-artifacts/`.
The adversarial input-fence and proposal-identity findings are closed in this
contract/spine; rubric gate is PASS and native version assumptions are sourced.
This is a completed design gate, not evidence of implemented enforcement,
migration, native smoke or CI success. Runtime results must be recorded separately.
