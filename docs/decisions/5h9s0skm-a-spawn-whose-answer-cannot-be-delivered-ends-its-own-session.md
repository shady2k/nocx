# ADR-5h9s0skm — A spawn whose answer cannot be delivered ends its own session

- **Status:** Accepted (2026-11-09, taken with the fix for `nocx-ouru5.1.5`,
  GitHub #689; it is the owner's amendment 3 to `nocx-isjh4` — "an abandoned
  session does not live for the life of the helper" — applied to a session whose
  answer never reached anybody, and it leaves amendment 4's rule — a live shell
  is never evicted to make room for a new one — exactly as it stands)
- **Date:** 2026-11-09
- **Related:** `nocx-isjh4` (a closed pane releases its reservation, and the
  unclaimed-session TTL), `nocx-ouru5.1.5`, ADR-8eej3e66 (every operation is
  cancellable and the busyness refusal is gone), D1/D5/D10
  (`internal/helper/session` and the helper protocol), AD-10 (the helper's
  aggregate window budget).

## Context

A helper's 512 MiB aggregate is fully reserved again after a long day and the
next pane cannot start (GitHub #689). The path that leaks is a spawn whose
caller is gone by the time the answer exists.

The helper forks a shell, registers the session, and reserves that session's
output window, row buffer and lifecycle window. The shell cannot be recalled:
by the time anything is noticed, the fork has happened. And the file `spawn`
answers with carries the session's id — the ONLY name anything may later use to
attach to it, to read its exit, or to end it.

A caller withdraws a slow open in three ordinary ways, and all three arrive here
as a request context that has ended:

- the renderer's Operations row offers **Cancel** on a slow open
  (`nocx-q9od5`), and every method is cancellable since ADR-8eej3e66: `rpc.cancel`
  reaches this daemon as `Host.cancel`, which cancels the request's context;
- the tab holding the opening pane is closed, which disposes the pane, aborts
  the open's signal and sends the same withdrawal;
- the transport dies, or a far route's own answer bound expires, and the host
  abandons the request's context because the answer has nowhere to go.

Until this record nothing read that context. The session was registered, its
reservation counted, and the answer written into a request nobody was waiting
on. From that moment the id existed only in a frame no reader would parse: no
pane held it, no durable binding named it (the binding is written after the
attach, which never happened), and the two other closers cannot help — the
unclaimed-session sweep closes EXITED sessions and an interactive shell at a
prompt never exits, and eviction under budget pressure touches neither a live
session nor one it cannot name. One withdrawn open pins 28 MiB at the shipped
settings for the life of the daemon.

It also pins the daemon. A helper retires when its last session ends; a helper
holding one orphan never reaches zero sessions again, so it stops retiring
between pieces of work and every later orphan lands in the same aggregate.
Eighteen withdrawn opens is the whole budget — and the first one is what turns a
helper that would have reset into one that accumulates.

## Decision

**A spawn whose answer cannot be delivered ends its own session, before it
answers.**

`Service.answerSpawn` (`internal/helper/session/service.go`) is the last step of
both spawn operations. If the session exists and the request's context has
ended, the helper ends that session through the ONE closer every other end uses
(`removeSession`, reason `spawn-abandoned`) and answers with
`ErrSpawnWithdrawn` rather than with an id nobody can hear. When this was the
daemon's last session, the drain fires as it does for any other end.

**The claim is dropped first.** A coordinator whose connection was lost
mid-spawn retries with the same durable idempotency key, and the answer that
retry deserves is a fresh shell rather than the id of a session that is closing.

**A delivered answer cannot be cancelled.** The host cancels a request's context
either on the client's own withdrawal or when it abandons the transport, and it
writes the answer only after the handler returns — so this decision can never
end a session whose id somebody is holding.

## Why this rather than the alternatives

- **Bounding never-attached sessions with the TTL sweep instead.** That sweep
  exists for a session whose coordinator may still come back, and it can only
  recognise an EXITED one. An interactive shell does not exit, so the bound
  would have to be "live but never attached", and the helper cannot tell an
  abandoned spawn from one whose attach is seconds away. A TTL would guess; the
  request context does not.
- **Closing a session when the connection that spawned it is released.** This is
  the shape the local opener's retry depends on NOT happening: the retry reuses
  the claim, and the claim answers with the session the lost attempt made —
  which is how a retry avoids forking a second shell beside the first. Closing on
  the release would turn every dropped connection mid-open into two shells.
- **Leaving it to the coordinator.** In exactly this case the coordinator has
  nothing to act on: it never learned the id. The helper is the party that made
  the session and the party writing the answer.

## What the next person inherits

A helper's reservation is released by one closer with four callers, and the
fourth — `spawn-abandoned` — is the only one that ends a session nobody ever
attached to. A test that opens a pane, withdraws it while the shell is starting,
and asks the helper what it holds must find it holding nothing; there are two,
one in `internal/helper/session` and one in `internal/app` that walks the shipped
opener, the endpoint socket and both kinds of pane.

Still open, and deliberately not decided here: a coordinator that HAS the id and
then dies before writing its durable binding leaves a live session nobody will
come back for. Bounding that needs a decision of its own — journaling the
spawn's answer, or retiring a live session that has never been attached — and it
is not this record.
