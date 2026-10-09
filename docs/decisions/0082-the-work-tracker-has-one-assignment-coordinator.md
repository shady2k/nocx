# ADR-0082 — The work tracker has one assignment coordinator

- **Status:** Accepted
- **Date:** 2026-10-09
- **Beads:** `nocx-q8yjf.22`
- **Scope:** Development workflow only; no application coordinator, session authority or AD-1…AD-10 invariant changes.

## Context

The tracker adapter recognizes an implemented prerequisite only after its integration evidence and checkout ancestry pass. The native tracker still considers that prerequisite unfinished. Claiming its consumer therefore needs `br update --claim --force`.

Native `claim_exclusive` atomically protects the task's holder. It does not bind the adapter's earlier prerequisite snapshot to the native write. A concurrent dependency edit or prerequisite reopening could invalidate that snapshot before the forced claim. Re-reading is not a lock, and adding a second advisory lock would not serialize native writers that do not use it.

## Decision

The owner confirmed one assignment coordinator. That coordinator serializes assignments with dependency edits, prerequisite reopening, implemented transitions and stage acceptance. Workers may submit their own results, but never self-assign or change another prerequisite. The worker's full agent name remains the task holder; the coordinator is not substituted for it.

Parallel implementation remains permitted at the capacity recorded in `.githooks/backlog-gate/config.json`. Independent features gain no artificial dependencies. Assignment pauses if another coordinator or writer is changing readiness-affecting state.

The integration records this as an operating policy, not an interprocess or distributed lock. Native exclusive claiming remains required, and forced claims are permitted only for contextually verified integrated prerequisites under this policy.

## Alternatives rejected

- **Treat the native claim as an atomic graph check.** It protects ownership, not the exported prerequisite snapshot.
- **Re-read immediately before the claim.** Another writer can still change the graph after that read.
- **Add an adapter-only lock.** Native tracker commands would not participate, so it would falsely imply safety.
- **Require a new native graph transaction before setup can finish.** The owner chose coordinated assignment instead; a native atomic graph guard would be a separate tracker change, not a hidden installation prerequisite.

## Consequences

The team's coordinator must serialize readiness-affecting writes with assignment. Multiple concurrent coordinators are unsupported. The setup proves native exclusive ownership and contextual claims in isolated, coordinated state; it does not claim to prove race-free graph authorization for arbitrary concurrent native writers.

Commands and the policy live in `docs/agents/backlog.md`; changing choices live in the project config. No product protocol, credential boundary, network exposure or acceptance requirement is weakened.
