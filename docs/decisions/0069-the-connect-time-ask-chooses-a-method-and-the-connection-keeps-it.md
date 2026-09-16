# ADR-0069 — The connect-time ask chooses a method, and the connection keeps it

- **Status:** Accepted
- **Date:** 2026-09-16
- **Supersedes:** the part of [ADR-0068](0068-the-helper-is-decided-by-the-connection-never-by-a-feature.md)
  that records the connect-time answer "as ADR-0034 records it — by host-key fingerprint",
  and the yes/no shape of that ask. ADR-0068's decision that no feature surface may initiate,
  offer or request the helper stands, and so does
  [ADR-0034](0034-consent-belongs-to-the-machine-not-the-connection.md)'s machine-scoped
  consent to deploy a binary. Neither record is edited.
- **Related:** [ADR-0033](0033-auto-is-the-name-for-not-yet-answered.md) (`auto` is "not yet
  answered"). Beads `nocx-k32ql`, `nocx-xn63t.6.5`.

## Context

ADR-0068 moved the integration question to the connect and kept ADR-0034's store for the
answer: a yes or no about the helper, keyed by the destination's host-key fingerprint. Built
that way (`nocx-k32ql`), two things showed on the first full run.

The question was the wrong size. A connection at `auto` has not answered _how nocx integrates
with it_, and the helper is one of several answers — the shell scripts we ship, nothing at all,
and a nocx server running on that host once that stage exists. Asking "use the helper?" leaves
the rest to be inferred from a "not now".

And the answer had two owners. `profile.DesiredMode` is where a connection's method lives and
what the connection editor writes; a fingerprint record answering the same question for every
connection to that host is a second place, and the two disagree the moment one connection to a
host is set to `script` on purpose.

Separately, the combined dialog shipped without its own "Trust host key" action — a person who
trusts the key had to answer the method question to say so — and the ask was raised on one
open path only, so every other way of opening an SSH tab produced a tab with no session and
nothing on screen (commit `0bf150b0`, found by bisect on `e2e/shell-mode.spec.ts:145`).

## Decision

**The connect-time ask is a choice of integration method, drawn from the methods the product
can actually carry.** Today those are: no integration (`raw`), the shell scripts (`script`),
and the helper (`helper`). A method appears in the choice when the mode exists and not before;
the stage that builds a nocx server on the remote host adds its own entry. No entry is shown
disabled or as "coming soon".

**The answer is written to the saved connection's `desiredMode`** — the same field, through
the same write path, the connection editor uses. From then on the connection is no longer
`auto` and is never asked again; changing its method is the editor's job. A hand-typed `ssh`
with no saved connection has nowhere to keep it, so the choice applies to that session.

**Choosing `helper` also records the machine's consent**, by fingerprint, as ADR-0034 does:
deploying a binary to a machine stays a permission of the machine. That record answers "may a
binary be deployed here", never "which method does this connection use".

**An unknown or changed host key keeps its own action in the same dialog.** "Trust host key"
stands beside the method choice; trusting the key without choosing a method is allowed, and
the next connect then asks about the method alone.

**Every path that opens an SSH session raises the ask.** The ask has one owner; an open path
that cannot raise it is a defect, not a mode.

## Rationale

- **One owner per decision** (AD-8). The connection already owns its method; the ask is a way
  of filling that field at the moment it matters, not a second store beside it.
- **A choice names what the person is choosing between.** A yes/no about one method makes the
  others implicit, and an implicit default is the answer nobody gave.
- **Nothing on screen that does nothing.** A disabled "server" entry would advertise a
  capability the product does not have — the soft degrade AGENTS.md says must never be
  contradicted by the UI.

## Consequences

- The dialog's helper question becomes a method choice; `connections.helperConsent` and its
  contracts change to carry a method, written to the connection.
- `e2e` specs that open an `auto` SSH connection answer the choice, or declare the method
  their subject needs on the connection so they are not asked.
- The server method is filed under the stage that builds it (`nocx-xn63t.2.1`).
