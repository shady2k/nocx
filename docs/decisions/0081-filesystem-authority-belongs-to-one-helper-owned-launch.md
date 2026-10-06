# ADR-0081 — Filesystem authority belongs to one helper-owned launch

- **Status:** Accepted (approved rewrite design; implementation acceptance pending)
- **Date:** 2026-10-05
- **Related:** AD-1…AD-10; ADR-0011 typed storage and secret references;
  ADR-0020 execution authority; ADR-0055 migrate-or-refuse; ADR-0057 local helper;
  ADR-0066 runtime-owned terminal; ADR-0068 connection-owned helper selection.
- **Extends:** ADR-0020 execution-only grant subject with a discriminated launch
  subject; its execution expiry/effect matrix and grant scopes remain unchanged.
- **Design:** [Main-based sandbox implementation contract](../superpowers/specs/2026-10-05-sandbox-main-rewrite-design.md).

## Context

PR #91's final head introduced a separate immutable sandbox grant. Its earlier
pane-ephemeral criticism is therefore historical, not a defect asserted of that
head. The current main's production local PTY is helper-owned, its durable store
is schema21 with ordered migrations, and terminal runtime/history ownership has
moved. Reusing coordinator-side launcher or frontend snapshot replacement would
violate those owners. The rewrite is from fresh main, not a port of the old branch.

A workspace expresses launch defaults, not live enforcement: moving a pane or
editing a profile must not confer or revoke a running process's rights. A protected
pane restored without its process must not become an ordinary shell. A boolean
cannot express the difference between an immutable permission, a selected process,
a mutable default and uncertainty about a helper that currently cannot be reached.

Same-UID helper/coordinator Unix endpoints trust the sandbox child's UID as much
as the coordinator's. Directory0700 and renderer origin validation do not change
that. A filesystem wrapper that can call trusted host tools through those endpoints
has not established the product's boundary.

## Decision

1. **Authority:** extend one authority_grants aggregate with XOR execution_id /
   launch_id. Execution semantics stay execution-only; launch's native snapshot is
   immutable and authorizes exactly its process, with null expiry. pane_launches
   records explicit operations; pane_launch_heads selects successful incarnation
   and survives process death. Off has no native grant and requires explicit
   confirmed Remove. Mutable standard/workspace profiles are never enforcement.
2. **Storage:** bounded typed sandbox.json through DocumentStore plus sparse typed
   workspace payload override, monotonic revision/CAS including reset. Feature
   lock encloses short config/membership/ContentDB CAS only; no native IO there.
   Transactional schema21→22 preserves layout/ledger/execution IDs and semantics.
   Full-identity session_retirements are independent of pane cascade and remain
   until helper confirms close or exact-generation absence.
3. **Execution:** restrict only helper child shell/descendants with separate minimal
   CGO-free runner. Linux requires go-landlock v0.9.0 ABI9 fixed V9.RestrictPaths,
   including handled but never granted RESOLVE_UNIX; no BestEffort/network/scoped
   operations. This blocks host-created pathname sockets, including old nocx
   generations. macOS deterministic Seatbelt profile additionally denies outbound
   pathname Unix outside this launch's private runtime. Mechanism/runner/version
   absence refuses Enforce, never starts an unrestricted process.
4. **Prepared policy:** one sandbox owner canonicalizes complete user/mandatory/
   dependency/system/runtime policy and rejects reserved control/config/credential/
   state subtrees and ancestors. No deny carve-out under broad allow. Trusted
   executables are RO and never under RW. Linux O_PATH identity pins survive to rule
   application; macOS path identities are rechecked with different semantics made
   explicit. Policy travels over inherited private descriptors, not argv/env/path.
   No preconnected control socket/token or unrestricted directory FD enters shell.
5. **Launch contract:** additive helper operations, frozen old spawn/sessions/
   SessionEntry unchanged. Single-use preparation ticket is consumed for one
   attempt; expired/unknown/evicted result cannot become fresh. Native restriction
   and successful shell exec must both be confirmed before success. Owner-only
   HOME/TMP runtime survives app restart with process and is removed only after
   confirmed retirement/exit; no automatic host dotfile access.
6. **Replacement:** common opener separates private candidate and public publish.
   Atomic ContentDB binding/head/launch/source-retirement commit selects candidate
   only after readiness, before registry selection/admission/public events. Before
   commit source survives errors; after commit close failure stays pending and does
   not restore source. Same pane/tab/ledger/history/draft are retained by existing
   rebind, not copied grid/transcript. Crash rolls back uncommitted candidates;
   lost response reads durable operation/head, never launches another process.
7. **Recovery:** attach same PID/session/grant only after exact generation/live
   inventory/digest/version verification. Exited entry is drain-only ended;
   confirmed absence dead; unavailable/timeout/ambiguous generation unknown. No
   ordinary fallback on backend or UI. Agent coordinator admission is refused for
   Enforce/unknown provenance before any tools and revoked on replacement.
8. **Product/privacy:** Off/Enforce, one Settings Sandbox surface reached by typed
   contextual shield and exact shell-target /sandbox intercepted before execution
   planning/history/ledger/input. No Learn/Statistics/live widening/retry. Bounded
   helper observations declare source/precision, not universal/proven errno denial.
   Event-bound CAS proposals change future defaults only. Paths are PrivateMetadata,
   returned only to explicit authenticated detail reads, never notifications/logs/
   metrics/errors. Backup exports mutable config, not authority to start/adopt.

## Guarantee and limitations

Kernel-mediated selected filesystem operations obey effective RO/RW roots for
shell and fork/exec descendants; native pathname socket deny also prevents nocx
host-tool endpoint escape. Network isolation, containers/VMs, Windows/SSH Enforce,
all metadata hiding, every ioctl/chmod, revocation of previously open FDs/hardlinks,
all-IPC/process isolation, kernel exploit protection, same-user host attacker
protection and environment credential scrubbing are **not** promised. Visible RO
system baseline includes process/device metadata paths. TCP/UDP and Linux abstract
sockets are not restricted. Linux same-domain new pathname sockets and macOS
private-runtime-only allow differ and must be explained in UI help. Existing
unrelated ordinary helper sessions remain operational.

## Alternatives rejected

- Separate sandbox_grants: two durable authority owners instead of discriminated
  existing aggregate; execution effects need not be generalized into native policy.
- Pane flag/workspace membership/profile mode as authority: cannot represent
  immutable incarnation, dead/unknown recovery or explicit protection removal.
- Restrict coordinator/shared helper: affects unrelated sessions and trusted user
  actions; containment belongs on the child before exec.
- Landlock ABI3/capped ABI8 fallback: permits same-UID pathname endpoint escape.
- Extending frozen spawn/inventory: old helpers cannot safely validate added fields;
  additive operations make unsupported Enforce explicit without breaking ordinary.
- Snapshot/grid cloning and pane recreation: duplicates history/render ownership.
- Successful unsupported empty sandbox or diagnostic-driven permission: claims
  enforcement without a kernel boundary or changes live authority implicitly.

## Consequences and acceptance

Native runner joins existing local helper artifact/install/checksum/signing chain.
Source and packaged Linux ABI9/macOS positive/negative production smoke are required,
including descendant restriction, current/old nocx endpoints, unrelated ordinary
session and unrestricted test TCP. Older Linux tests must prove refusal, not be
counted as positive enforcement. Missing macOS runtime is missing evidence, not
cross-platform completion. Migration/refusal/CAS/immutability, all commit/crash
windows, listener death/overflow/identity races, real authenticated browser UX and
privacy are acceptance in the linked spec. No destructive user-DB downgrade is
supported; forward migration/refusal and copied-backup recovery preserve data.
