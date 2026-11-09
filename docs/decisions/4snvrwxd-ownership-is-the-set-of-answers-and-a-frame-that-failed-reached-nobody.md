# ADR-4snvrwxd — Ownership is the set of answers that name a session, and an answer counts only if its frame reached the wire

- **Status:** Accepted (2026-11-09, taken with the fix for `nocx-ouru5.1.9`, the
  review of PR #764; supersedes the ownership and delivery rules of
  [ADR-02kc8c1j](02kc8c1j-the-withdrawal-is-decided-where-the-answer-is-written-and-a-handover-is-final.md),
  which superseded ADR-5h9s0skm)
- **Date:** 2026-11-09
- **Related:** ADR-5h9s0skm and ADR-02kc8c1j (the decision and its first
  refinement: a spawn whose answer reached nobody ends its own session, decided
  where the answer is written), AD-10 (the helper's aggregate window budget),
  `nocx-isjh4`, and GitHub #689.

## Context

ADR-02kc8c1j made the host arbitrate a withdrawal at the instant the answer is
written, and gave the session service two marks: `handedOut`, set when a retry
was given the session's id under its claim, and `withdrawn`, set by the spawn
ending its own unread answer. A review of the merged change found three narrower
races, and two of them are assumptions in that record that stopped holding:

- **A hand-over is not a delivery.** `handedOut` was set the moment a retry read
  the resolved claim — before the retry's own answer was written. If both the
  first attempt's answer and the retry's reached nobody, neither caller had the
  id, yet the session was permanently protected by the hand-over mark and its
  whole reservation survived. The mark recorded an intention to tell somebody,
  not the telling.
- **A written answer is not a delivered one.** The arbitration marked the answer
  written and reported it as delivered; the write then failed — a transport that
  died between the two lines, a frame the writer refused — and the service was
  told nothing, because `respond` discarded the failure and the verdict was
  already optimistic. The session's id reached nobody and nothing could ever
  withdraw it again.
- **And the key was freed before the shell was gone.** The release dropped the
  claim under the lock and then stopped the process. A retry that took the key in
  that gap forked a second shell beside a first one that had not finished
  closing: two live shells for one key, which is the one thing an idempotency
  claim exists to prevent.

## Decision

**Ownership is a fact about the set of answers naming a session, and an answer
counts only once its frame reached the wire.**

The session service keeps three facts per session, all under the mutex that
resolves and reads the idempotency claim:

- `answers` counts the answers that name the session and have not been reported
  yet: one when the spawn registers it, and one more for every hand-over the
  claim performs. A hand-over is an answer still to come.
- `delivered` is set by the first answer reported as reaching somebody, and it
  is final: the id is that caller's, and no later failure may end the session.
- `withdrawn` ends the session, and only when the LAST answer has been reported
  and none of them reached anybody.

The host's verdict therefore means **"the answer reached nobody"**, not "the
caller had gone": it is set when a withdrawal was recorded before the write, and
also when the write itself failed — `respond` now returns whether the frame
reached the wire, chunked answers included, and the request's report is
withdrawn when it did not.

**The claim is held closed until the shell is gone.** Ending an unread session
marks its claim `closing` under the lock, and closes the claim's `closed`
channel only after the process has been stopped. A retry that finds the claim
closing waits for that channel — outside the service lock — and forks its own
shell once the old one has ended.

## Why this rather than the alternatives

- **Counting answers instead of marking a hand-over.** The alternative was to
  let a retry defer the release until its own answer was written, which is the
  same rule with the bookkeeping moved into the retry; a count keeps the
  decision in one place and makes "nobody was ever told" expressible.
- **Treating a failed write as a plain log line.** The request's answer is the
  only place a session's id exists. A write that failed is a delivery failure,
  and the same verdict that covers a withdrawal covers it — one meaning, one
  branch in the service.
- **Freeing the key immediately and letting the process close behind it.** That
  is the defect: the claim promises one session per key, and a key that can be
  re-taken while its shell is still running breaks that promise for as long as a
  process takes to close (bounded by the owner's own stop deadline, which is not
  zero for a PTY).

## What the next person inherits

`AnswerDelivery.Withdrawn` means "nobody was told". `noteSpawnAnswer` is the
one place a spawn's answer is accounted for, and it is the only caller of the
closer for this reason. A retry waits on `keyClaim.closed` rather than on the
key when the claim is closing. The tests that pin it:
`internal/helper/session`'s `TestBothAnswersWrittenToNobodyEndTheSessionOnce`,
`TestADeliveredRetryKeepsTheSessionEvenIfTheFirstAnswerWasLost` and
`TestARetryWaitsForTheShellItReplacesToFinishClosing`, and
`internal/helper/host`'s `TestAnAnswerWhoseFrameCouldNotBeWrittenIsReportedWithdrawn`
with its control — each red before this change.
