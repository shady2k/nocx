# ADR-v7xk8mq8 — Closing a tab ends its session, and absence has a deadline

- **Status:** Accepted (owner decision, 2026-11-12; nocx-wkjdb)
- **Date:** 2026-11-12
- **Supersedes:** the tab-close-as-detach rule in the level-1 helper design D9
  (`nocx-k6p18.7`), and ADR-0104's exited-only TTL and “live shells are never
  swept” clause. Their other decisions remain unchanged.

## Context

Closing a tab left its shell running. On the owner's host this accumulated about
70 shells since 2026-10-18, including about 40 long-running htop processes.
ADR-0104's exited-only 24-hour sweep could never reclaim those live programs.
The three verbs were recorded in
`.internal/specs/2026-08-31-level-1-the-helper-owns-the-host-design.md` D9,
not in a separate ADR; this record supersedes that decision without rewriting it.

AD-1 keeps lifecycle requests on the control plane. AD-7 keeps identity and
closure authoritative at the session owner. AD-8 requires extending the existing
helper session service and sweep rather than creating another owner or timer.

## Decision

An intentional tab close ends its session immediately through the existing
close-session operation: hang up its process group and remove its record. The
existing running-program confirmation is the sole choice point. Reopening a
closed tab creates new work, never resurrects the ended process.

Quitting nocx, a crash, sleep and network loss are not tab closes. They release
attachments while preserving sessions and restorable tabs. The helper/daemon that
owns each session record owns its absence clock. Local and remote helper sessions
use the same mechanism. An attachment means an accepted session subscription
bound to a live helper connection, not a running program, an inventory read or a
coordinator process merely being alive. Any attachment protects the session.
The clock starts at spawn until first attachment; when the last attachment drops
it restarts. Reattachment cancels that deadline, and a later last detach starts
a fresh full interval. Neither output nor a foreground program resets the clock.

The existing session expiry sweep also ends live unattached sessions. Its
injected clock measures the interval; the existing scheduled and lazy sweep
entry points enforce it. The integer setting is a number of days, at least zero,
default 3; zero disables absence expiry. It travels through the settings module
and existing helper spawn configuration path, like the output buffer bound.
A session retains the value supplied at spawn, including across coordinator
replacement. Changing the setting governs newly created sessions; the field
states this. A day is 24 hours, not a local-calendar date.
Budget-pressure eviction remains restricted to exited unclaimed records and
must never kill a live program merely to admit another session.

A returning window uses the existing authoritative absent-session reconciliation
and shows that its session is no longer available, with an option to start a new
session. It must not claim the old process survived, silently replace it or infer
absence from a failed connection. No durable tombstone store or second recovery
protocol is introduced solely to distinguish timer expiry from another closure.

The closer remains the single owner of hangup and record removal. Failures must
be observable and must not be represented as successful process termination.
Tests cover failed hangup and failed removal at the existing owning seams.

## Acceptance

A confirmed tab close running `sleep 100000` removes the process locally and on
the SSH stand, observed by waiting matchers in Chromium and WebKit. Injected-clock
helper tests prove the exact configured boundary, protection while attached,
reattachment at 71h59m and a restarted interval, and disabled expiry at zero.
The setting persists through restart. Quit/disconnect continues to preserve work.
