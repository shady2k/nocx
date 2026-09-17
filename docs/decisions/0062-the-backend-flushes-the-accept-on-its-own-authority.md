# ADR-0062 — The backend flushes the accept on its own authority

- **Status:** Accepted
- **Date:** 2026-09-10
- **Related:** supersedes the establishment-acknowledgement MECHANISM built
  under [ADR-0024](0024-authenticated-shell-integration-channel.md) decision
  9 — the `lifecycle.establishAck` round trip, `docs/lifecycle-protocol.md`
  §5 as amended below, and everything in `internal/lifecyclepub` and
  `internal/transport` keyed on it. Decision 9 itself — prompt suppression
  is forbidden unless the domain is live, past `ACCEPT` — is **not**
  superseded and is not this record's to change; the kernel goes on
  enforcing it (`requireActive` returning `ErrDomainPending` while a domain
  is `acceptPending`). Bead `nocx-ui8q6.2`; the renderer-side half that makes
  this safe is `nocx-ui8q6.1` (commit `790dbc3e`).

## Context

`docs/lifecycle-protocol.md` §5 step 5 states two separate conditions: "Only
after `accept` may the shell suppress its prompt or emit lifecycle events. …
Enhanced mode is entered only after the frontend has the published
`domain_established` fact." The implementation built under decision 9
collapsed them into one. `internal/lifecyclepub.Publisher.Ingest` minted the
accept, held it in a `pending` map keyed by (lane, domain, epoch), and
deferred the call to `kernel.Deliver` until a renderer sent
`lifecycle.establishAck` naming the exact generation the published
`prompt_ready` fact carried. Until that ack landed — or a ten-second bound
expired and rolled the domain back — the accept never reached the shell.

That is stricter than §5 asks. §5's second condition is about the
**frontend's** enhanced mode, not about whether the **shell** may leave
`ACCEPT`; nothing in decision 9 requires the backend to wait for a renderer
before flushing an accept. The wait was an implementation choice, and it had
a dependency the design did not have: a renderer that receives the fact and
answers it. `internal/transport/session_open.go`'s own doc comment on
`WSServer.OpenSession` says a session the backend opens itself creates no
ring and no subscriber, by design — a worker participant's pane is opened
exactly this way. For such a pane the published fact reaches nobody, no
acknowledgement is possible, and the pending accept has no way to ever be
released.

**Measured 2026-09-10, trace `051ee139c2d6e716566cecfe54bd5607`.** The
shell's 219-byte hello crossed all three carriage hops intact and reached the
adapter in 82ms. The channel still died at exactly 10.0s with
`cause=hello-timeout` — the establishment bound and the shell's own hello
bound expiring together, because both were waiting on the same absent
acknowledgement. A healthy frontend-opened pane on the same machine
establishes in `after_ms=78`. This is not a race that occasionally loses: a
backend-opened hosted pane could **never** establish, on any machine, no
matter how fast the renderer would have answered — there was no renderer to
answer.

## Decision

**The accept is flushed as soon as the kernel mints it, on the backend's own
authority, with no wait for anyone to acknowledge anything.**
`internal/lifecyclepub.Publisher.Ingest` no longer special-cases
`KindAccept`: it goes out through `kernel.Deliver` in the same pass as
`refresh_request`, after the lane's fact is published, exactly like every
other outbound envelope. `lifecycle.establishAck`, its params and result
schemas, `AcknowledgeEstablishment`, the pending-accept bookkeeping
(`estKey`, `pendingAccept`, the `gen`/`pending` maps, the establishment
timer, `WithEstablishmentTimeout`, the two sentinel errors) and the
`generation` field on the published fact are all removed — nothing mints a
value whose only reader no longer exists.

**What stays, unchanged.** Decision 9's actual property — a domain may not
leave `ACCEPT` until the kernel says so — is enforced exactly as before, at
the kernel (`internal/lifecycle`), and this record does not touch it:
`requireActive` still refuses `ErrDomainPending` for a domain whose accept is
minted but undelivered, `Deliver` still refuses to send an accept for a
domain that is not `Established`, and `EstablishmentTimeout` still exists as
a kernel primitive for a domain whose accept genuinely cannot be delivered
(a send failure, not an absent renderer). `TestEstablishmentUndeliveredAcceptNotLive`
and the new `TestEstablishmentUndeliveredAcceptRefusesAgentEnrolment` in
`internal/lifecycle/kernel_test.go` assert this directly: agent enrolment is
refused under the same gate as `start` and `prompt_ready`. §5 step 5's
second condition — enhanced mode waits for the frontend to have the
published fact — also stays exactly as written; it was never the backend's
job to enforce it; the frontend already gates on its own receipt of the fact.

**What makes the removal safe, rather than merely convenient.** Decision 8's
concern about a suppressed prompt with no editor — "the worst of both" — is
what the old mechanism was defending against on the frontend's behalf. That
window is now closed on the renderer's own side by `nocx-ui8q6.1`: a pane
whose axis reads `starting` shows no grid and drops keystrokes, so an accept
that reaches the shell before the renderer has finished applying the fact no
longer produces an editor-less suppressed prompt taking raw input — it
produces a pane that visibly is not ready yet and accepts nothing. The
backend flushing the accept immediately is safe because the frontend no
longer needs the backend's cooperation to stay safe while it catches up.

## Why this, and not the obvious alternative

The alternative considered and rejected: keep the acknowledgement, but only
wait for it when the publisher's emitter reports a subscriber exists — flush
immediately for a subscriber-less session (the worker-pane case) and keep
the old wait-then-flush path otherwise.

Rejected because it leaves **two behaviors for one event**, and the second
one — the wait — is almost never the path exercised. Every ordinary
frontend-opened pane already applies the fact and would answer an
acknowledgement in tens of milliseconds against what was a ten-second bound;
the wait bought nothing measurable there and existed only for the path that
had no answer at all. A mechanism kept alive for a case it is not needed in,
beside a case it cannot serve, is the shape that rots: the two paths agree
in every test anyone writes against a subscribed session and silently
diverge in exactly the situation — no subscriber — that the second path
exists for, which is the same failure class AGENTS.md's "look for the
existing answer" section already names ("two implementations… agree
everywhere you look and disagree somewhere you did not"). Deleting the wait
entirely, and leaning on the kernel's own past-ACCEPT gate plus the
renderer's own starting-axis discipline, leaves exactly one behavior to
reason about.

## Consequences

- A session the backend opens itself (a worker participant's pane) can now
  establish at all. This is the acceptance criterion: a hello ingested for
  such a session results in an accept on the transport port and the domain
  reaching `Established`, with no subscriber and no acknowledgement ever
  sent.
- The ordinary path is unchanged in outcome and faster in the general case:
  an accept that used to wait for a renderer's round trip (tens of
  milliseconds on a healthy machine, the full ten-second bound on a stalled
  one) now goes out synchronously within the same `Ingest` call as the
  hello that produced it.
- `lifecycle.submitAttempt` and `lifecycle.recoverAck` remain on one ordered
  submission in `internal/transport/ws_lifecycle.go`, but the race that used
  to require it — a concurrent submit landing between the fact reporting
  `PromptReady` and a deferred accept flush — no longer exists, because
  there is no window between the two any more.
- If a transport genuinely cannot carry the accept (a dead port, not an
  absent subscriber), the domain stays `acceptPending` and the kernel goes
  on refusing lifecycle events under it; there is currently no timer that
  rolls such a domain back on its own, the same as before this record for a
  `Deliver` failure specifically. `internal/lifecyclepub`'s
  `TestPublisherLeavesDomainPendingWhenTheAcceptCannotBeDelivered` documents
  this rather than hides it; revisiting it is future work, not a regression
  this record introduces.
