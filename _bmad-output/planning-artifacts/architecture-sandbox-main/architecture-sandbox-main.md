---
title: Local filesystem sandbox architecture spine
status: final
created: 2026-10-05
updated: 2026-10-05
---

# Architecture spine

## Paradigm

Brownfield layered, interface-first Go services with manual DI at app composition
root; feature-owned typed repositories, one ContentDB writer, helper-owned execution
runtime and SolidJS presentation. Full implementation contract:
[implementation spec](../../../docs/superpowers/specs/2026-10-05-sandbox-main-rewrite-design.md).

## Inherited invariants

AD-1 through AD-10 remain binding, including binary data/control split, helper-minted
session IDs, one runtime emulator and bounded delivery. ADR-0011 storage/secret
boundaries, ADR-0055 ordered migrate-or-refuse, ADR-0057 mandatory local helper,
ADR-0066 runtime ownership and ADR-0068 connection-owned helper selection stand.
ADR-0058 is Proposed and does not supply native sandbox authority.

## Decisions

### SB-AD-1 — One immutable authority per launch [ADOPTED]

- **Binds:** profiles, ContentDB, helper and recovery.
- **Prevents:** pane membership/default changes silently conferring live rights.
- **Rule:** workspace/standard profiles are mutable defaults only. One
  execution-or-launch authority_grants aggregate has an XOR subject; launch policy
  is immutable, unexpired only for its exact process. pane_launch_heads selects an
  incarnation, never grants another launch. Off has no native grant and requires
  explicit confirmed Remove. Schema21→22 carries every prior row without reset.

### SB-AD-2 — Restrict child execution before exec [ADOPTED]

- **Binds:** native runner, helper and packaging.
- **Prevents:** restriction of shared trusted daemon, silent unsupported fallback,
  same-UID escalation through current or older nocx pathname endpoints.
- **Rule:** minimal CGO-free runner restricts only shell/descendants. Linux requires
  go-landlock v0.9.0 fixed ABI9 V9.RestrictPaths with RESOLVE_UNIX handled and never
  granted; no BestEffort/network/scoped restrictions. Seatbelt filesystem policy
  explicitly denies outbound pathname Unix outside private runtime. Restriction
  and shell exec are separately confirmed over inherited private channels before
  candidate success. No control socket/token or unrestricted directory FD enters
  shell; policy is not argv/env. Ordinary sessions continue on existing helpers.

### SB-AD-3 — One bounded canonical effective policy [ADOPTED]

- **Binds:** preview, native prepare, grant and launch.
- **Prevents:** UI/helper builder drift, TOCTOU alias widening and reserved-state
  exposure through mandatory or broad ancestors.
- **Rule:** sandbox package canonicalizes full policy, checks reserved subtrees,
  trusted artifact RO identity and RO/RW conflicts before coalescing; exact prepared
  policy/digest is persisted and launched, not rebuilt. Linux roots pinned O_PATH;
  macOS identities rechecked with explicit path-based limitation. Visible versioned
  workspace/runtime/system/dependency baseline plus bounded explicit user roots.
  Owner-only per-launch HOME/TMP survives with helper process; no automatic dotfiles.

### SB-AD-4 — Selection commits before publish [ADOPTED]

- **Binds:** staged opener, ContentDB, registry, UI rebind and retirement.
- **Prevents:** selectable/admitted uncommitted candidate, source loss before ready,
  duplicate ordinary Remove replay and forgotten cleanup after pane deletion.
- **Rule:** one confirmed durable operation consumes one helper ticket for at most
  one attempt. Candidate opens privately; atomic binding/head/active-state/source
  retirement transaction is commit point. Only then idempotent publish and same-pane
  rebind. Source retirement full identity has no pane-cascade dependency. Lost
  response queries operation/head; expired/unknown ticket never spawns. Coordinator
  crash rolls back all uncommitted candidates, never auto-continues spawning.
  The selection commit holds source input/admission fence, drains previously
  admitted writes and marks source retired/revoked before fence release/publish.
  Every input route refuses stale source while helper close remains pending.
  Outstanding uncommitted candidates are bounded one/pane and 32/helper; cleanup
  uncertainty prevents another launch. No helper-side lease may kill a DB-committed
  shell in the coordinator-crash window. Permanently unavailable generation keeps
  explicit pending cleanup until close/no_such_session/exact inventory absence;
  missing installation or elapsed time is never proof that its process ended.

### SB-AD-5 — Recovery and admission are fail-closed [ADOPTED]

- **Binds:** inventory reconciliation, backend ordinary open and frontend restore.
- **Prevents:** dead/unknown protected pane becoming an ordinary shell or agent
  gaining trusted GUI/coordinator side-channel rights.
- **Rule:** readopt only exact helper generation and live entry with matching
  session/launch/grant/digest/version; exited entry is drain-only ended. Confirmed
  absence is dead; timeout/unavailable/generation ambiguity is unknown. Neither
  creates a process. Ordinary open refuses Enforce head server-side. Candidate and
  retired sessions are excluded before generic adoption. Enforce and unknown
  provenance deny coordinator tool admission; replacement revokes source admission.

### SB-AD-6 — Defaults and observations never mutate live authority [ADOPTED]

- **Binds:** feature repository, diagnostic inbox and Settings.
- **Prevents:** stale profile writes, implicit widening/retry and path leakage.
- **Rule:** typed sandbox.json and sparse workspace override use monotonic CAS,
  short feature config lock across revision/membership checks and ContentDB writes;
  native preparation/IO never runs under that mutex.
  Backup import/recovery owns an exclusive feature restore scope: ordinary profile
  writes and grant CAS refuse while it is active; only its unexported capability
  may import or roll back with destination-local clocks. Journal and other IO run
  outside the config mutex; scope closure on return/error/panic invalidates the
  capability. Startup restores settings prerequisites before ContentDB, then
  workspace/default configuration before exposing transport.
  Bounded helper-owned observations are best-effort attempts
  with declared source/precision, not universal/proven denial. Listener death ends
  affected Linux session; overflow CONTINUE never stalls syscall. Event-bound CAS
  promotion changes future defaults only, separate relaunch confirmation required.
  Proposals bind event revision, exact canonical existing target/parent identity
  and missing-target status; any substitution/disappearance/new target requires
  refreshed human confirmation. Unresolved paths/socket events cannot promote.
  Notification-copy budgets and asynchronous prediction queues are numeric in the
  implementation spec; CONTINUE precedes slow prediction/canonicalization.
  Paths only explicit authenticated details, never unsolicited events/logs/metrics.

### SB-AD-7 — One existing user surface and pane history owner [ADOPTED]

- **Binds:** Settings, shield, command editor, rebind and runtime.
- **Prevents:** fake navigation panel, duplicate modal/history/grid and commands
  mistaken for shell execution.
- **Rule:** existing Settings Sandbox hosts profiles/status/grant/actions/inbox;
  typed contextual shield and exact shell-target /sandbox reach it. Typed internal
  interception precedes planning/history/ledger/input and shares async submit guard.
  Replacement reuses existing teardown/bind without open RPC, preserves pane-owned
  blocks/draft/chrome and adds divider, never reinjects old live grid.

## Seed and boundaries

Main pin: 7e600425. ContentDB schema21→22. Linux library go-landlock v0.9.0.
Helper operations are additive sandbox-prepare/launch/get/discard/access-list/
access-resolve; frozen spawn/sessions/SessionEntry unchanged. Coordinator uses
feature-owned authenticated sandbox RPC and generated contract types. Platform
runner compiled CGO_ENABLED=0 and installed with matching local helper generation;
remote-only helper does not advertise local enforcement. Existing Zig0.16.0/
libghostty-vt artifact prerequisites remain. Native Linux ABI9 and macOS source
and packaged positive/negative smoke precede feature acceptance; unavailable native
host is missing evidence, never a pass. One final ci-full/security set before PR.

## Deferred / deliberately excluded

Network/container/VM/Windows/SSH enforcement, comprehensive process/IPC isolation,
metadata hiding, closure of pre-existing FD/hardlink authority, clearing arbitrary
environment credentials, Learn/Statistics, per-agent permissions, live widening and
command retry are outside approved scope, not implementation placeholders. TCP/UDP
and Linux abstract sockets remain unrestricted; macOS runtime-only pathname socket
allow differs from Linux same-domain allow and is shown in help. User data is never
reset; feature rollback disables new Enforce or explicitly Remove/closes selected
session, not destructive ContentDB downgrade. Merge requires user approval.

## Design gate evidence

Rubric and adversarial reviews pass after input-fence/proposal-identity clarification.
Current-versions review corroborates fixed Landlock ABI9/v0.9.0 and Seatbelt
pathname-socket filter syntax; Seatbelt support deprecation remains explicit.
Native descriptor/readiness/exec behavior still needs runtime acceptance.
Numeric observer-copy and collector budgets are fixed in the implementation spec.
Product, UX and review artifacts are peers under planning-artifacts. This final
design status does not claim implementation, native smoke or CI results.
