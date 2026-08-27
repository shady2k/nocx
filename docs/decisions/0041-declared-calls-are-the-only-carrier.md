# ADR-0041 — Declared tool calls are the assistant's only carrier

- **Status:** Accepted
- **Date:** 2026-08-27
- **Related:** [ADR-0020](0020-the-agent-gets-a-lane-authority-is-granted-per-run.md)
  (authority is granted per run; the grant is what a carrier executes through),
  [ADR-0028](0028-eino-runs-the-loop-the-grant-is-ours.md) (decision 4: the dispatcher
  narrows, it does not check — which is why the kernel survives this ADR unchanged),
  AD-8 (one owner per behaviour), `nocx-d6gn4` and its children, in particular
  `nocx-d6gn4.8` (the kill criteria and the measurement), `nocx-d6gn4.10` (what to measure)
  and `nocx-d6gn4.14` (the removal).

This ADR is written **from the result**. That was decided in advance and deliberately: an
ADR opened while the experiment was running would have recorded the argument we happened
to find convincing that week, and the whole point of the measurement was that we did not
yet know which way it went.

## Context

The owner found [lisptc](https://github.com/1hachem/lisptc) and asked whether its method
applies here. Its interesting line is the agent integration: the agent has no tools, its
replies are Lisp source evaluated straight into a REPL, and replies are constrained to a
valid dialect by grammar-based structured output.

Stated without the language, the invariant is: **the model proposes a bounded executable
artifact; a deterministic executor owns control flow, authority, state transitions and the
production of an auditable trace.** Lisp is one carrier of that invariant and an expensive
one. Note what it does not claim — it does not make the agent deterministic. It makes the
_orchestration between_ nondeterministic decisions deterministic. The model, the shell, the
filesystem, clocks and the network stay exactly as nondeterministic as they were.

Two adversarial review rounds followed. What survived is narrower than where the discussion
peaked, and the narrowing was the useful part. Two things we believed for a while and which
are not true:

- **"We already have the journal."** We do not. An audit ledger is not a deterministic
  workflow history. Temporal's history exists to reproduce the executor's internal
  decisions; ours records product facts for provenance and recall. Both being append-only
  is not what would make them interchangeable.
- **"Effects as data means approval sees the whole program."** Only for an _applicative_
  plan, where every effect is describable before any result is known. The moment a step
  depends on an earlier result the shape is _monadic_, evaluation and execution interleave,
  and the host can only ever see the current step.

So we built two composing carriers beside the declared-call one and put a setting in front
of them, because "which one the model reaches for when both are offered" measures
tool-description salience, ordering and model bias — not fitness. That metric was named and
rejected in writing so it could not come back. The kill criteria were written **before** the
switch shipped, for the same reason.

## The measurement

Run 559, qwen3.6-35b-a3b, carrier = program, taken once nothing of ours was in the way — the
result contract declared, the dialect stated, `GlobalReassign` on, `print` reaching the
person, the parked resume fixed:

```
streaming  36.0s  reasoning=7340  answer=2     calls=0  suspended
streaming  25.8s  reasoning=3270  answer=2     calls=1  suspended
streaming  12.2s  reasoning=416   answer=1251  calls=1  answered
```

Zero dialect errors, three model turns, two approvals, 79 seconds. And the program it wrote
was **one `run` call whose argument is a bash script** with the loop, the condition and the
accumulator inside it.

**Dependency depth inside a program was one in every program measured**, across two models
and every presentation we tried.

An earlier observation had already cut at the case, and it is why the measurement counted
what it counted. Asked to "create test.txt with contents test, then rename it to
test2.txt", the model answered in a single `run` call — `echo test > test.txt && mv
test.txt test2.txt`. It did not make two calls and compose them; it composed with `&&`
inside the argument, and the host saw one invocation. Part of the benefit attributed to a
program carrier — fewer round trips, intermediates never entering context — is **already
available through `run`, for free, whenever the steps are shell-shaped.** A carrier that
competes with the shell on shell work is competing with something the model reaches for
without being told.

That is why "calls per task" was never scored alone. Every task was scored on invocations
_and_ on steps — the `&&`, `||`, `;`, pipelines and substitutions inside each `run`
command, which are steps the model composed and the host never saw — reported separately
and never summed. And the bucket that actually decides it is the third one: steps that
**cannot** be shell-composed, where a value has to leave the shell, be reasoned about and
come back, or a branch turns on a tool result that is not an exit code, or the work crosses
tools. Only that bucket is demand a program carrier serves and the shell does not. It was
close to empty.

## Decision

**Declared tool calls are the assistant's only carrier.** Both composing carriers — the
Starlark program carrier and the CEL plan/graph carrier — are removed, along with the
`assistant.carrier` setting that chose between them, since with one cohort left it has
nothing to choose.

Declared calls answer the same questions in the same commands with the same approvals and
fewer turns. That is the whole of it.

**Declared tools remain the authority floor**, and this is the part that would have held
whichever way the measurement went. Authority is granted per run and narrowed per call
(ADR-0020, ADR-0028); a carrier is a way of _reaching_ that kernel, never a way around it.
Any future carrier — a fourth idea, a grammar-constrained dialect, something not yet
proposed — is an implementation behind the same interface and changes no policy, no
approval, no attempt record. The effect kernel was pulled out from behind eino's middleware
for exactly this reason, and **it survived the deletion of both carriers that motivated it**,
which is the check that it was a seam and not a switch.

### The plan carrier was removed without a measurement of its own

Stated plainly, because it is the weakest part of this decision. It got no run. The reason
is cost: a declared call is faster, and another day of correcting our own defects before the
carrier could be measured fairly was not worth what it could return.

What we saw of it live was the model looping for fifteen rounds of reasoning and concluding
"the tool is fundamentally not working as described". **The cause was ours.** A step's
arguments are CEL expressions, so a literal shell command has to be a CEL string literal;
the compile failure returned CEL's own error naming a column, never the fix; and the one
example the description offered wrote its arguments as `{...}` — omitting the exact thing
that breaks. So this is not evidence the plan carrier is bad. It is evidence we had not
finished telling it what it was. Anyone reopening the question should know that the door was
closed on cost, not on merit.

## What was kept, and why it is worth keeping

The experiment paid for several things that have nothing to do with carriers, and they stay:

- **The result contract.** `$defs/result`, `agenttools.Tool.ResultSchema`, the registry's
  refusal to assemble an executable row without one, and the kernel's `checkResult`. This is
  the wire-contract rule applied at the tool boundary — it caught a defect the day it landed,
  and it governs what a _declared_ call hands the model just as much as what a program did.
- **The argument and result rendering.** The declared-call path already showed the params
  schema; the same helpers now also state what a tool returns.
- **Tracing:** `log.CallPath`, `WithTraceID` / `WithRequestID`, `AddSource` on every record,
  and the ask's streaming counters.
- **Named cancellation causes.** A run this process ends must not tell a person their
  connection dropped, whatever carries it. They now live in their own file rather than inside
  a carrier.
- **The effect kernel behind an interface**, as above.

`parkedRuns` went with the carriers: nothing parks any more, because a declared call unwinds
and the checkpoint carries the resume. That was confirmed against the approval tests before
deleting, not after.

## The lesson, which outlives both carriers

Three times in one day a model was defeated by an interface we had described inaccurately,
and each time **the prose was right and the example or the error was wrong**:

- the program example indexed `result["text"]` on a tool that has no text;
- the plan example wrote its arguments as `{...}`, omitting the one thing that breaks;
- both carriers' failures returned a parser's error rather than the fix.

A model copies the example and reads the error. Prose it skims. Whatever we build next for a
model to use, **the example is the contract and the failure names the fix.**

## What the next person inherits

One carrier, one kernel, one shape of prompt. A stored `assistant.carrier` from before this
change is dropped at load and asserted to be. If you want to reopen the question, the
measurement is in `nocx-d6gn4.8`'s notes with the kill criteria it was written against, and
the third bucket above — steps that cannot be shell-composed — is the number to go and
collect. Do not reopen it on a demo.
