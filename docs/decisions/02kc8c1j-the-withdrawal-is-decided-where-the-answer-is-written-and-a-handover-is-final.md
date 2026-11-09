# ADR-02kc8c1j — The withdrawal is decided where the answer is written, and a session already handed over is nobody else's to end

- **Status:** Accepted (2026-11-09, taken with the review of `nocx-ouru5.1.5` in PR
  #764; supersedes the mechanism of
  [ADR-5h9s0skm](5h9s0skm-a-spawn-whose-answer-cannot-be-delivered-ends-its-own-session.md))
- **Date:** 2026-11-09
- **Related:** ADR-5h9s0skm (the decision this one sharpens: a spawn whose answer
  reached nobody ends its own session), ADR-8eej3e66 (every operation is
  cancellable), AD-10 (the helper's aggregate window budget), `nocx-isjh4`
  (a closed pane releases its reservation, the unclaimed-session TTL), and
  GitHub #689.

## Context

ADR-5h9s0skm decided that the helper ends a session whose spawn answer nobody
read, because the answer carries the session's only name and no other party can
ever close it. It named the interval "from the moment the spawn is registered
until the host writes its answer" and read the request's context inside the
handler (`answerSpawn`, the last line of both spawn ops).

A review of that change found two ways the reading was in the wrong place:

- **The window between the check and the write.** `answerSpawn` looked at the
  context and returned; the host then marshalled and wrote the frame. A
  withdrawal landing in that gap cancelled nothing the handler could still see
  and released nothing — the client had sent `rpc.cancel` and dropped its
  pending answer, the host wrote success, and the session was leaked exactly as
  before. Deciding before the answer exists is deciding too early.
- **A retry that owns the shell.** A spawn's claim resolves inside
  `finishSpawn`, which wakes any retry waiting on the same idempotency key. The
  retry takes the existing session, is given its id and attaches — and then the
  first spawn, whose own caller withdrew, removed the session it had just handed
  over. Two parties decided one session's fate, and the loser was whoever wrote
  the answer last.

## Decision

**One decision, taken by the host, at the instant the answer is written; one
owner, recorded under one lock.**

The host owns the arbitration, because it is the only party that can make "the
caller withdrew" and "the answer went out" the same decision. Each pending
request carries a mutex and two facts — `withdrawn` and `answered`:

- a `TypeCancel` (or a transport the host abandons) records the withdrawal if,
  and only if, the answer has not been written yet; a cancellation that arrives
  after the answer is under way is not applied at all, because the caller may be
  holding what the frame carries;
- immediately before the response frame is written, the request's goroutine
  takes the same mutex, marks the answer written and snapshots the withdrawal;
- the answer's fate is then reported to the service
  (`host.AnswerDelivery{Withdrawn}`) on the existing `ResponseWritten` hook,
  which already runs after the write on that goroutine.

The session service acts on `Withdrawn` only for `spawn` and `spawn-ssh`, and
only under the mutex the idempotency claim is resolved and read under:

- `handedOut` is set the moment any caller is given the session's id under its
  claim (a retry that found the claim resolved). The spawn that made the session
  may not end one that has been handed out.
- `withdrawn` is set by the spawn that is ending its own unread session, and it
  makes the claim a claim on nothing: the map entry goes under the same lock, so
  a retry either forks its own shell or is handed a live one, never the id of a
  session that is closing.

Neither mark can follow the other, because both are taken under that one mutex.

## Why this rather than the alternatives

- **Keep deciding in the handler, and re-check later.** A second check is
  another window: whatever the handler checks, the frame is written after it
  returns. The host is where the frame is written, so the host is where the
  question is answerable.
- **Let the coordinator close what it was told about.** In the withdrawn case it
  was told nothing — that is the whole defect. For the retry it _was_ told, and
  the answer is the rule above: the retry's shell is the retry's.
- **A delivery acknowledgement on the wire.** It would close the last
  theoretical window (a cancel still in flight when the frame is written) and
  costs a protocol round trip on every spawn, for a case in which the caller's
  own select may have taken the answer anyway. Not taken; named here so the next
  reader knows it was considered.

## What the next person inherits

`Host.request` decides; `AnswerDelivery` carries the verdict; the session
service's `ResponseWritten` acts on it, and `releaseUnheardSpawn` refuses what
somebody else holds. The tests that pin it: `internal/helper/host`'s two
arbitration tests (a withdrawal before the write is reported, one after it is
not), `internal/helper/session`'s three ownership tests, and `internal/app`'s
two end-to-end ones — a withdrawal held open in exactly the gap this record is
about, and a retry that keeps the shell it was handed.
