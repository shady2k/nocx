# ADR-0063 — Typing is refused on evidence against the rule, not on its absence

- **Status:** Accepted
- **Date:** 2026-09-10
- **Related:** supersedes the typing-authority POSITION taken under
  `nocx-jse6x` — "typing authority is earned, and the currency is the
  labelled set" — as implemented in `internal/agentcalib`'s `Verify` and
  carried onto the wire by `internal/transport/ws_agent_calibration.go`. That
  position was never written as its own ADR; it lived in the package's file
  comment and in the bead. `nocx-jse6x`'s own falsifiers — a label may not be
  attached to a frame the person did not produce, and a completed calibration
  may not lack a required label — are **not** superseded and are not this
  record's to change. Built on `nocx-qddv8` (commit `a2545b0f`). Bead
  `nocx-9w0q0`.

## Context

`internal/agenttyping`'s `grant` puts one keystroke into a pane only after two
gates: the pane is positively classified `free_text` right now, and
`agentcalib.Verify(agent).MayType()` says the agent's rule may be believed.
Until this record, `Verify` answered false for every path that was not a
rule replayed against a _complete_ labelled set with _no disagreement_ in
it — which included an agent nobody had ever calibrated. On a machine where
nobody has run the guided calibration — every fresh install, and this
developer's own dev stand — that made typing permanently unreachable:
`workers.spawn` could open a pane and never deliver the task typed into it.

That refusal was not arbitrary when `nocx-jse6x` shipped it. Until
`nocx-qddv8`, `claude.rule.json` declared `"default": "free_text"` with no
branch that positively matched `free_text` — the state nocx types into was
reachable only by falling through every other identification. A chrome
change that broke every branch, while some incidental anchor still bound,
would keep answering `free_text` regardless — the exact state a mistimed
keystroke turns into an approved tool call. Refusing to type on an
uncalibrated rule was, at the time, the only thing standing between that
fall-through and a keystroke landing in a dialog whose first option is Yes:
a rule nobody had checked was indistinguishable from a rule that could no
longer read the screen at all, because both answered `free_text` by default.

`nocx-qddv8` closed the fall-through at its source. `claude.rule.json` now
requires the `prompt` anchor bound to reach `free_text` — a positive
identification — and the document's default is `unknown`, which every
consumer already treats as busy. An unidentified frame refuses on its own
now; it no longer needs the calibration gate to catch it. Replaying the full
corpus in `internal/agentdriver/testdata/captures` after that fix produced
the same verdict on every frame, before and after: nothing was reaching
`free_text` by accident. With the fall-through gone, "this rule has never
been checked" and "this rule cannot be believed" are no longer the same
risk, and treating them the same charges every fresh install a completed
guided calibration it does not need.

## Decision

**`agentcalib.Verify` refuses only on evidence that a rule should not be
believed. Everything else it finds permits, carrying a reason.**

Two causes refuse:

- **A disagreement** — a labelled frame the rule answered with something
  other than the state it was produced for. The person calibrated and found
  a real defect in the rule; this is the remedy working as designed.
- **No rule in this build for the named agent** — nothing can read that
  pane's screen at all, so there is no positive identification to type
  against, whatever the labelled set claims.

Everything else that used to refuse now permits, because each is a fact
about the _evidence_, not about the _rule_: the agent has never been
calibrated; the labelled set is incomplete (missing a required label); the
set could not be read; the capture could not be replayed; a label names a
state this build does not map. A permitted verdict that has not verified
still carries a non-empty `Reason`, so a surface can tell a person "may
type" apart from "verified" — `MayType() == true` no longer implies the
rule was checked against anything.

`Verdict.mayType` stays unexported and is written in exactly one statement,
now inside `Verify` itself rather than at the tail of a single linear
function: a new unexported `evaluate` method fills in `Labelled`, `Agreed`,
`Disagreements` and `Reason` and returns a `refused bool`, and `Verify` sets
`mayType = true` only when `refused` is false. Every refusing path returns
early with `refused = true`; every permitting path returns `false` and
merely records why. The zero `Verdict`, and a `Verdict` a caller builds by
hand, still deny — nothing about this record touches that: there remains no
exported way to write `true` into the field that carries the answer.

The wire contract (`contracts/agent.calibration.schema.json`) and the
calibration surface (`frontend/src/agent-calibration-section.tsx`) are
updated to say the new thing: `mayType` means "nothing found here
contradicts this rule," not "this rule was checked and agreed with
everything." `reason` is no longer absent whenever `mayType` is true — it is
absent only when the rule was actually verified, and present whenever a
permit rests on the absence of evidence rather than on a check. A person
reading "may type" for an agent they have never calibrated is told exactly
that, in words, not left to read a boolean as a verification it is not.

## Why this, and not the obvious alternative

The obvious alternative is to leave `Verify` alone and instead special-case
"never calibrated" at the call site in `internal/agenttyping`, treating an
absent set as an implicit pass. That was rejected for the same reason
`ADR-0062` rejected keeping two behaviors for one event: it would create a
second place that decides whether a rule may be believed, disagreeing with
`Verify` exactly where the two might diverge — an incomplete set, an
unreadable one, a set naming an unmapped label — none of which are "never
calibrated" but all of which want the same answer. `Verify` is already the
one place `agentcalib`'s design commits to being the sole source of this
value (`Verdict.mayType`'s unexported field, written in one statement); a
bypass at the call site would reintroduce the second derivation this
repository has already paid for elsewhere (AGENTS.md, "Look for the existing
answer before you write a second one").

Deleting the check entirely — always permitting regardless of disagreements —
was also rejected: it would erase the one case this record keeps as a real
refusal, the calibration finding an actual defect in a rule, which is the
scenario `nocx-jse6x` exists to catch and which remains exactly as strict as
before.

## Consequences

- An agent that has never been calibrated, with a rule in this build, may be
  typed into on a fresh install with no guided calibration run — the
  motivating case this record fixes — and the surface still tells a person
  their agent was never checked, rather than implying it was verified.
- A disagreement, and an agent with no rule in this build, still refuse
  exactly as before; nothing about the strictness of those two paths changed.
- An incomplete, unreadable, or unreplayable labelled set, and a set naming a
  label this build does not map, now permit instead of refusing — each with
  a reason naming which of those it was, so calibrating remains useful for
  narrowing the check without being required to unlock typing at all.
- `internal/agenttyping`'s frame gate is untouched: typing still requires a
  positively identified `free_text` frame read immediately before the write,
  independent of this record.
- `internal/agentcalib/verify_test.go`'s coverage of "every other path
  denies" is now split: `TestAnAgentWithNoRuleMayNotBeTypedInto` is the
  remaining refusal case, and the tests that used to assert a denial for
  "never calibrated," an unreplayable capture, and an unmapped label
  (`TestAnUncalibratedAgentMayBeTypedInto`,
  `TestASetThatCannotBeReplayedMayStillBeTypedAgainst`,
  `TestALabelThisBuildDoesNotAskForStillPermitsWithAReason`) now assert a
  permit with a reason instead. `TestAVerdictNobodyProducedDeniesTyping`
  is unchanged and still asserts the zero-value and hand-built-`Verdict`
  properties directly.
