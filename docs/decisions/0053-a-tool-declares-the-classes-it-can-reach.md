# ADR-0053 — A tool declares the classes it can reach, not a worst case

- **Status:** Accepted
- **Date:** 2026-08-31
- **Related:** [ADR-0020](0020-agent-lane-and-per-run-authority.md) (the effect
  lattice, the policy matrix and the per-run grant this amends), AD-8 (one owner
  per behaviour).
- **Beads:** `nocx-4h0m7.3` (execute is delegation — blocked by this),
  `nocx-4lxxj` (the assistant never calls a tool — P0, caused by this),
  `nocx-cmj11` (the run fence and the policy selector).
- **Consulted:** the owner, 2026-08-31, who put it in one sentence — _"session.run
  cannot be classified at all without its arguments"_ — and who proposed and then
  helped reject `EffectUnknown`.

## Context

Every tool declaration carries `Effect content.Effect`: one position on the
ADR-0020 lattice, documented as "the tool's declared worst-case class". Two
different consumers read it, and only one of them can:

- **At OFFER time**, `Registry.ForGrant` drops any tool whose declared effect the
  run's grant does not permit. The arguments do not exist yet.
- **At EXECUTION time**, `commandEffect` parses the actual invocation and may
  LOWER the proposal's class beneath the declared one.

For fifteen of the sixteen declared tools this is coherent. `files.read` is a read
whatever it is pointed at; `notes.create` is a reversible write. Their declaration
is a fact about the tool, and the offer-time filter asks a question that has an
answer.

**`session.run` is the sixteenth, and it is a different kind of thing.** It is a
door, not an action: `lsblk` and `rm -rf /` are not two severities of one act,
they are two acts that share a carrier. Its declared `EffectMutateDestructive` is
therefore not a conservative ceiling — it is a mandatory field with nothing true
to put in it, filled with the worst of its possibilities.

The registry already knows this and records it: `session.run` is the ONLY
declaration carrying `CommandArg`, the field that names which argument determines
what the call does. The distinction exists in the table and is then ignored by the
consumer that most needs it.

### What that costs today, measured

The offer-time filter asks "is the declared class permitted?" For `session.run`
the question has no correct answer, and both answers are wrong:

- judged as `mutate-destructive` — an operator who refuses irreversible change
  loses `lsblk` and `df -h` along with `rm`;
- judged as `observe` — an operator who refuses irreversible change gets `rm -rf`.

The first is not hypothetical. It is `nocx-4lxxj`: the assistant plans a command
in its visible reasoning and then asks the person to run it, because it was
offered no way to run anything. The product reports the turn `completed` and
green. A test reproduces the shape deterministically —
`TestAssistant_LooksAtPaneDespiteOperatorSessionScope`, where a grant that fails
to cover a tool's resource kinds produces `calls=0 outcome=answered` and a model
that politely asks the human instead.

It also blocks `nocx-4h0m7.3`. Execution is delegation and belongs in the
`Delegate` row, but the derivation may only pick a class NOT ABOVE the declared
one, and `Delegate` is not below `MutateDestructive` — it is a different row on a
different question. While `session.run` declares a single point, no derivation can
reach `Delegate` at all, and that bead's acceptance criterion is unsatisfiable by
construction rather than merely unmet.

## Decision

**A tool declares the SET of classes it can resolve to. The set replaces the
single declared worst case.**

    files.read     → {Observe}
    notes.create   → {MutateReversible}
    session.run    → {Observe, MutateReversible, MutateDestructive,
                      Delegate, CrossBoundary}

For fifteen tools the set is a singleton and means exactly what the single value
meant. Only `session.run` is genuinely plural, and it is plural because it is
genuinely a door.

Three consequences, each replacing a question that had no answer with one that
does:

1. **Offer time asks a different question.** Not "is the declared class
   permitted?" but **"is any class in this tool's set not refused?"** If one is,
   the tool is offered and the decision moves to execution. If none is, the tool
   is useless under this policy and is dropped — **and the person is told that,
   naming the tool and the rows that refused it.** A tool withheld in silence is
   the defect this ADR exists to end.

2. **Execution time is unchanged in mechanism and finally correct in effect.**
   `commandEffect` parses the invocation and selects one class FROM THE SET. The
   selected row's decision governs. `lsblk` resolves to `Observe`, `./deploy.sh`
   to `Delegate`, `rm -rf` to `MutateDestructive`. The person's setting for that
   row is what applies, which is what the settings page has always promised.

3. **Not knowing keeps its present home and its present direction.** When the
   parse cannot determine what a command touches, the class stays at the WORST
   member of the set. Unresolved input makes the decision stricter, never looser.
   This is already true and already guarded by
   `TestCommandEffect_DisqualifiedAlwaysHasUnresolvedCause` over thirteen forms;
   the set changes what "worst" ranges over, not the direction of the rule.

### Rejected: a new `EffectUnknown` class

Proposed by the owner in the same conversation and rejected with him. It is the
obvious move — the field is mandatory, so give it an honest value — and it is
wrong for a reason worth recording, because it will be proposed again.

**An effect class is not a label; it is a row in the policy matrix, and every row
is a question put to a person.** `EffectUnknown` puts up the question "what may
the assistant do when we could not classify the action?" — which nobody can answer
from what the UI can show them.

And the failure mode is inverted. An `Unknown` row set to _allow_ is a universal
bypass: everything the parser could not understand becomes permitted, and what the
parser could not understand is exactly what deserves the most suspicion. A
deliberately obscured command would pass more easily than a plain one. That is the
inversion `nocx-y47mi` closed for effect classes and `nocx-jrnso` found again one
layer down; this would be its third appearance.

It also does not solve the problem. `PermittedEffects` and `ForGrant` would read
the `Unknown` row and produce, once again, ONE answer for the whole tool — `lsblk`
and `rm -rf` sharing a decision under a new name.

**The instinct behind it is right and is already implemented at the correct
level.** `content.ResourceUnknown` and `ResourceReport.Unresolved` model not-knowing
as a FINDING OF THE PARSE, carrying a human-readable reason, and they make the
outcome stricter. Not-knowing belongs where it is discovered, not in the table of
what a person has permitted.

## Consequences

- Sixteen declarations change shape; fifteen change only syntactically. There is
  no data migration: the policy matrix, its rows and its stored settings are
  untouched, and no new row appears in Settings.
- `Registry.ForGrant` gains a second question and an explanation path. The
  explanation is the product-visible half and is not optional.
- `nocx-4h0m7.3` becomes satisfiable: with no declared point, `Delegate` is not
  "above" anything, and the derivation selects it for an execution.
- `nocx-4lxxj`'s offer-time cause is removed. Its instrumentation half
  (`w2instr`) stands regardless — the tool set a run was offered must be
  observable whatever decides it.
- The `CommandArg` field stops being a lone marker nothing consumes and becomes
  the thing that says which argument the selection reads.
- A future tool that is also a door — a remote exec relay, a package manager
  wrapper — declares a set and needs no new mechanism.

## What this does not decide

- Whether `session.run`'s set should include `PrivilegeChange` and `Disclose`.
  Both are arguably reachable through a shell. Left open deliberately: those rows
  have no tool behind them yet, and `LiveEffects` would surface them in Settings
  as soon as one claims them. Decide it when a derivation can actually select
  them.
- The wording the approval window uses for each verb (`nocx-4h0m7.3`).
- Territories — where an act may happen, as opposed to what kind of act it is
  (`nocx-4h0m7.4`).
