# Mesh from day one, and what a progress claim may decide

- **Date:** 2026-09-03
- **Session bead:** `nocx-jnh2i`, discovered from `nocx-4n3bz`
- **Status:** approved by the owner in session, 2026-09-03
- **Amends:** `.internal/specs/2026-08-24-orchestration-mechanism-design.md` — `D10`, and `§7.2`

Two amendments the owner raised against the mechanism design. They are one document because
they turn out to be one question asked twice: **who is entitled to assert what, and what may
be built on the assertion.**

## 1. What this decides, and what those documents already decided

`.internal/specs/2026-08-24-orchestration-mechanism-design.md` **D10** decided "star topology
now; participants are addressable nodes from day one", and its rejected column already names
the discipline mesh requires: the coordinator is mirrored on **mutations of the record** — file
ownership, task scope, participant set — and never on message content. _Content is theirs;
commitments are his._ This document does not overturn D10. It splits it, because the expensive
half and the cheap half were being decided together.

**`D9`** of the same design decided that only process exit and the participant's own
declaration may decide wave state, and that the screen decides typing and lighting and nothing
else. **`D2`** put the escalation backstop on a per-fact deadline. **`D12`** made a wait-for
cycle a suspicion requiring corroboration. **`§5`** classed MCP surfaces, skills and prompt
text as PERSUASION: they raise probability and cannot carry an invariant. None of these change;
everything below is placed inside them.

`nocx-9le.5.7` and `nocx-9le.5.9` are **closed and shipped**, and they already answered the
progress question for a different subject. A transfer's state comes from its result and its
`done`, **never from a progress sample**; a progress frame **does not mint** a transfer; a late
progress frame **does not resurrect** a finished one; progress is live and lossy, dropped when
no subscriber is attached and coalesced to one in flight, while completion is retained per
session, bounded, and flushed on the next attach. This document transfers that rule set from
bytes to a wave participant rather than inventing a second one — AGENTS.md's "look for the
existing answer before you write a second one", applied to a predicate rather than a package.

`nocx-szb40` is **closed and on main**: observations are appended with their provenance and
reduced, bound to session epoch + lifecycle domain + attempt + output offset, because a single
status field with source priority produces a stuck value. Declared progress is one more
observation in that model and needs no new machinery.

`nocx-tnx44` (open) already owns MEASURED progress: a `moving | stalled` facet derived from
**transcript growth**, not frame equality — a hung agent's spinner keeps spinning — asked of
the same rule as the screen facet, and explicitly a conjunction rather than a fifth enum value.

`nocx-01ud6` (open) owns the advisor: `profile.RoleClassifier` is assignable,
`internal/assistant/classifier.go` is written, and `ResolveClassifier` has **no production
implementation** — only test fixtures (`classifier_test.go:88`, `:98`). Its success criteria
already fix the invariant this document needs: **the classifier may only ever raise suspicion
(permit → ask), never lower it.** Its description already names "the judge for a stalled run"
as one of the things resting on that unconnected seam.

**The AD-6 amendment** (`docs/architecture.md`, the enrolled-pane grid) grants the backend grid
**exactly two powers, and says so exhaustively**: whether nocx may write into the pane, and
what the pane's activity indicator shows. §7 below is written to stay inside that list rather
than to widen it.

## 2. The problem

**Mesh.** "Every participant may address every other" reads as one request and is two. Deciding
them together means either paying for the expensive one now, or refusing the cheap one for its
sake.

**Progress.** The coordinator's real question is _has my wave stalled, and who needs me_. A
percentage is a proxy for it, and a percentage produced by an agent is a **claim**, not
evidence — the same class as the `worker_done` prose §7.2 already declines to trust, and the
same class as the memory in which a coordinator invented a "workers overran their scope"
narrative and wrote that falsehood into two commit messages. Asked "what percent are you", a
model answers with a number that is monotone, anchored on the plan it made before it knew
anything, and long-resident at ninety. Rendered as a bar, it reads as **measured**, which is
the defect: a number asserts a precision that prose does not.

The owner's own framing settles the requirement rather than the objection: _«это число
примерное и не претендует на точность. Может ли воркер застыть на 90%? Конечно может.»_ The
number is wanted, it is known to be approximate, and it is known to freeze. The design's job is
therefore not to refuse it but to make freezing **harmless and visible**.

## 3. Decisions

| #   | Decision                                                                                                                                                                                              | Rejected alternative, and why                                                                                                                                                                                                                                                                                            |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| M1  | **Mesh on the TALK axis from day one.** Any participant may address any other with messages, findings and questions                                                                                   | Star on this axis (D10 as written). It forces every finding through the coordinator, including what the coordinator has no business reading, and it spends a coordinator turn to move a fact between two workers who are both already awake                                                                              |
| M2  | **Star on the ACT axis.** Reassigning scope or file ownership, changing the participant set, interrupting, spawning and closing are the coordinator's, and the coordinator is mirrored on all of them | Full mesh on this axis too. It requires delegate-further, transitive revocation, and a stated answer to "a human took over B — what became of A's rights over B", all inside the very authority model `nocx-z3nkr` exists to write. Deferred, **not denied**: the wire is general, so adopting it later costs no rewrite |
| M3  | **The talk/act split is enforced by the SHAPE of the API, never by convention.** There is no call that can carry prose and also mutate the record                                                     | A single "say" call whose payload the recipient interprets. Under it, A silently reassigning B's work is expressible, and the coordinator's mirror on commitments becomes a hope                                                                                                                                         |
| P1  | **Progress is a CHECKPOINT: a named milestone declared when reached, appended, never overwritten**                                                                                                    | Step `k` of `n`. The owner's measurement: a worker has no reliably pre-known scope and may have exactly one task, so the denominator is often a fiction, and a fiction in the denominator is a lie in the quotient                                                                                                       |
| P2  | **An approximate estimate is an OPTIONAL field on a checkpoint**, declared as the participant's own approximation                                                                                     | A required percentage, and equally a refusal to carry one. The first invents precision; the second withholds what the owner asked for over an objection he has already answered                                                                                                                                          |
| P3  | **A checkpoint MAY carry an artifact reference** — a commit, a path, a test run — which the coordinator may corroborate against what nocx already owns                                                | Requiring one. Not every milestone has an artifact, and a requirement would push workers to invent them. This is a gradient: a checkpoint that names something checkable is worth more than one that does not, and neither is refused                                                                                    |
| P4  | **A declared checkpoint does NOT wake the coordinator, does NOT enter D2's undispatched-fact set and starts NO deadline.** It is read at the coordinator's next call                                  | Waking on every checkpoint, which spends a coordinator turn per milestone per worker on information that asks for nothing. And a worker-set "important" flag, which hands the wake decision to the one party whose assertions this document has just finished declining to trust                                         |
| P5  | **The declared row and the measured row are never merged.** A frozen estimate beside an independently measured `stalled` is the intended reading, not a defect                                        | One reduced "progress" value. The freeze is expected — merging is what would turn an expected, harmless lie into an authoritative one                                                                                                                                                                                    |
| P6  | **The advisor is a THIRD source and may only ever worsen the picture.** It may report a run as stuck, off-track or misdirected; it may never confirm progress and may never authorise anything        | A symmetric advisor. An advisor that can confirm is a second unfalsifiable claimer wearing the authority of an independent model, and two models agreeing that all is well is correlated error, not evidence. This is `nocx-01ud6`'s "may only ever raise suspicion, never lower it", applied to a second subject        |
| P7  | **The advisor is consulted on the measured `stalled` transition**, not continuously                                                                                                                   | Polling every participant. It spends a second model on every healthy worker forever, to learn what the measured row already reports for free                                                                                                                                                                             |

## 4. The three sources, and what each may decide

| Source       | Produced by                                    | May decide                                                 | May never                                                                       |
| ------------ | ---------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------------------------------------------- |
| **Declared** | the participant, as a checkpoint               | light the indicator; supply the displayed estimate         | terminalize a participant; decide wave state; open or close a lifecycle attempt |
| **Measured** | the backend, from transcript growth            | the `moving \| stalled` facet, which is an indicator state | say _why_ a participant stopped                                                 |
| **Inferred** | the advisor: own context, own model, read-only | **worsen** the picture, with a reason                      | confirm progress; authorise anything; decide wave state                         |

`D9` is untouched by all three. **Only process exit and the participant's own terminal
declaration decide wave state**, and none of the rows above is that declaration: a checkpoint
is expressly not a completion report, which is why P1 makes checkpoints append-only and leaves
`Report, structurally` exactly where §7.2 put it.

## 5. Mesh: what it costs, stated

**The per-fact deadline keeps an owner.** A fact A sent to B that B has not dispatched is
supervised as **B's**, and escalation still runs coordinator → human, because the coordinator
is the only role with authority to act on it. The coordinator therefore learns _"B has an
undispatched fact from A"_ **without learning its content** — which is precisely D10's
content/commitment split, expressed as a deadline rather than as etiquette.

**D12 stops being theoretical.** Under star a wait-for cycle is at most length two. Under M1 it
is a real multi-hop graph — A waits on B waits on C waits on A — so D12's five corroboration
conditions are load-bearing in the first version rather than in a later one. No new machinery;
a change in when it must work.

**The delivery weakness multiplies.** Coordinator → worker is already unreliable: `nocx-dkawo.1`
refuses to type into anything but a positively identified `free_text`, so a worker mid-turn
reads at its next call. Under M1 every **pair** inherits that, and a worker-to-worker fact has
no escalation path of its own — which is the second reason the deadline above is the
recipient's and terminates at the human.

## 6. Progress: the record

A checkpoint is an observation in `nocx-szb40`'s model and carries its provenance: backend
instance, session id, session epoch, lifecycle domain, attempt and output offset. It holds a
name, an optional approximate estimate, an optional artifact reference, and nothing else.

The rules are `nocx-9le.5.9`'s, transferred verbatim to a new subject:

1. A participant's state comes from its terminal declaration and its process exit, **never from
   a checkpoint**.
2. A checkpoint **does not mint** a participant.
3. A late checkpoint **does not resurrect** a terminal one — which is what the provenance
   binding is for: an estimate from a previous incarnation must not attach to a new one.
4. Checkpoints are **live and lossy** — dropped when nobody is attached, coalesced — while the
   structural report is **retained and flushed on attach** (`nocx-9le.5.7`).

**Why the freeze is not a defect.** The declared row is _allowed_ to lie, because the measured
row beside it catches the lie: `nocx-tnx44` reads transcript growth, so a worker stuck at its
own "90%" displays `90% · stalled`. A lone percentage rendered as measurement would be the
defect. A percentage beside an independently measured "stopped moving" is true, and carries
more than either half does alone.

## 7. The advisor, and the boundary it must not cross

**The advisor reads the transcript, not the grid.** It would be natural to hand it the backend's
live VT grid, and that is refused: the AD-6 amendment grants the grid exactly two powers and
declares the list exhaustive, so making it a content source for a model widens the carve-out by
side effect — the failure mode AGENTS.md names as re-deciding a settled question inside
something else. The advisor needs no live screen; it needs the transcript, which models in this
product already reach through granted block windows (`nocx-5u3oz`). AD-6 and ADR-0002 are
therefore **untouched by this document**.

**The grid does not decide to consult the advisor either.** The grid decides the indicator, and
`stalled` is an indicator state. What observes that facet and spends a model call is supervision
policy, outside the grid. Stated because the careless version of P7 — "the grid triggers the
advisor" — would be a third power.

**The pane is untrusted input.** The advisor reads output written by an agent that may itself
have been fed hostile content, so it treats the transcript as data and never as instructions.
`nocx-01ud6` already names prompt-injection defence as resting on this seam; this is that seam's
second consumer, under the same rule.

**This is a dependency, not a decoration.** Until `ResolveClassifier` has a production
implementation, the inferred source does not exist. The declared and measured rows are
independently useful and ship without it.

## 8. Deliberately out

- **Delegate-further** and transitive revocation (M2's deferred half). It arrives with a case,
  not with an anticipation.
- **A coordinator wake on a checkpoint** (P4). Reconsidered only if a measured case shows the
  next-call delay costing something real.
- **Continuous advisory** (P7).
- **A reduced single progress value** (P5). This one is not merely out; it is falsifying — see
  §10.
- The **advisory note channel**, which `nocx-01ud6` already places behind the per-call verdict.

## 9. Assertions

1. There is no call that both carries prose and mutates the wave record (M3). Asserted by the
   API surface, not by a test of intent.
2. A participant addressing another participant with a message succeeds; the same participant
   attempting to change another's scope, interrupt it or close it is refused, and the refusal
   names the missing delegation (M1, M2).
3. A fact from A undispatched by B escalates to the human on B's deadline, and the escalation
   carries the fact's existence and not its content (§5).
4. A checkpoint arriving for a terminal participant is refused and does not resurrect it; a
   checkpoint arriving for an unknown participant does not create one (P1, §6 rules 2 and 3).
5. A checkpoint whose provenance names a previous epoch or attempt is refused (§6 rule 3).
6. A participant that declares "90%" and then stops writing to its transcript displays as
   `stalled` while retaining the declared 90 (P5). Both values are present; neither overwrites
   the other.
7. Wave state after a checkpoint is byte-for-byte what it was before it (D9, P1).
8. An advisor verdict can move a participant's presentation from healthy to suspect and can
   never move it the other way (P6). Asserted by feeding it a verdict of each polarity.
9. With `ResolveClassifier` unresolved, the declared and measured rows still work and the
   inferred row is absent rather than erroneous (§7).

## 10. What would falsify this design

- **Progress expressed as a single reduced value**, or the declared estimate overwritten by the
  measured facet, or vice versa. That is P5 inverted, and it is `nocx-tnx44`'s own stated
  falsifier in a second place.
- **A checkpoint terminalizing a participant**, or opening or closing a lifecycle attempt. That
  is D9 breached.
- **An advisor verdict that improves a participant's standing**, or that authorises anything.
- **A grid read that serves the advisor**, or a grid that triggers it. Either widens the AD-6
  carve-out this document was written to stay inside.
- **A worker-to-worker call that mutates the record.** M3 is the whole of M2's enforcement; if
  it is expressible, the star on the act axis is a naming convention.
