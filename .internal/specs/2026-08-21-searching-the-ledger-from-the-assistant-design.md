# Searching the ledger from the assistant: one owner, two consumers

**Bead:** `nocx-vorhg` (epic) · **Date:** 2026-08-21 · **Status:** approved by the owner, 2026-08-21

Third sibling of `2026-08-21-attaching-context-to-a-question-design.md` (which owns the
gesture) and `2026-08-21-what-the-model-is-told-design.md` (which owns everything that
crosses the wire to the model). This one owns **the assistant going and looking in the
ledger itself** — and, mostly, it owns saying which existing owner already answers each
question, because almost every part of this is already somebody's.

## What a person can do that they cannot today

**Ask the assistant about work that is no longer on any screen, and have it go and find
that work itself.** "What did I run on prod in `/srv/api` before the deploy failed", "where
did I see that `EACCES`" — asked in a pane, answered by an assistant that searched, rather
than by a person who first had to find the block and attach it.

The end-to-end check that watches them do it (rule 2, `AGENTS.md`):

> A person asks about a string that was printed three weeks ago and is on no screen now.
> The assistant calls `ledger.search`, gets rows and a coverage statement, and answers —
> naming the environment the work happened in, and saying plainly if retention has evicted
> part of the window it searched. Watched end to end, against the real backend.

## What is true today, stated exactly

Facts, read out of the code and the backlog on 2026-08-21. Every decision below rests on
one, and four of them are the reason this design is mostly a map rather than a plan:

- **`ledger.search` already exists as a filed task**, `nocx-rtg0.21`, and
  `internal/content/ledger.go:928-930` names it in prose as the owner of full-text search:
  "Full-text search is a different question with a different owner (ledger.search,
  nocx-rtg0.21)". It is `{ text, scope, limit } -> { entries, coverage }`, and its bead
  states that **no FTS exists in `internal/content` today** — so it is a store capability,
  not a wire method with a query behind it.
- **What `LedgerQuery.Text` does today is not full-text and says so.** It is a
  case-insensitive substring over the recorded **intent**, applied within the recall rung —
  "not a pattern and not full-text", deliberately, so the cutover off `command_history` was
  like for like. Nothing searches output, in any form.
- **The recall ladder and its filters already exist** on `LedgerQuery`: `Scope` (the rungs),
  `EnvironmentID`, `Cwd`, `PaneID`, `Since`/`Before`, `BeforeID` paging, `Limit`. The read
  side is `QueryEntries`.
- **Tool execution is wired.** `nocx-lndv` closed: the policy answers PERMIT / ASK / REFUSE
  and the dispatcher **narrows** rather than checks, at eino's three middleware seams
  (ADR-0028). A new tool is a row in `internal/agenttools`, a schema under
  `contracts/tools/`, and a `Narrow` — not a new mechanism.
- **`ResourceEnvironment` is already in the ledger's closed resource-kind set**
  (`content.ResourceKind`, and `agenttools/resourcekind.go` consumes it exhaustively). A
  search scoped by environment needs no new kind.
- **The egress gate already exists** (`internal/assistant/egress.go`, `nocx-0p7y2`): every
  tool result is screened before it leaves for the provider, and a finding **refuses and
  asks** rather than being silently redacted. Intent is masked on the way into the ledger
  (`ws_ledger.go:347`, `ws_history_record.go:195`).
- **The person's own search is `nocx-ms7v`** — "You can find what you did, in the place you
  did it", MVP, in progress, one of nine children closed (`.9`, search over commands).
  `.2` (FTS across intent and derived output) and `.7` (the indexing unit, and whether
  substring search is promised) are open. The epic depends on `nocx-jrdy` and `nocx-uahp`.

## 1. One owner of search, two consumers

**The assistant's tool has no predicate of its own.** It calls `ledger.search` and renders
what comes back. The recall overlay the person drives calls the same thing.

This is `AGENTS.md`'s "look for the existing answer before you write a second one" applied
to the most tempting possible case: a search that only the model uses looks like it could
have its own index, its own ranking and its own idea of what a hit is — and it would agree
with the person's search everywhere anyone looked, and disagree somewhere nobody did. The
2026-08-05 defect (two derivations of "am I in an ssh context") is the shape.

Concretely: no FTS table, no ranking function and no coverage computation lands in
`internal/assistant` or `internal/agenttools`. If the tool needs something `ledger.search`
does not return, that is an extension of `ledger.search`, argued in its own bead.

## 2. The row in the registry, and the one decision in it

```
Name:        ledger.search
Description: one sentence, model-facing (nocx-avogl.2's field)
Effect:      observe
Resources:   [environment]
ResourceArg: environmentId
Executes:    InGo
Params:      contracts/tools/ledger.search.schema.json
Narrow:      a reader scoped to the grant's environments
```

**`ResourceArg: environmentId` is the decision, and it is a security decision.** The policy's
scope check reads the argument a tool names to answer "is this call inside the grant"
(`policy.go`, `inScope`). A tool that names **no** resource takes the other branch, spelled
out in the code: _"The tool names no resource in its parameters; its scope is the grant's own
scope for the kinds it declares"_ — it returns `true`. So a search that did not name where it
searched would be in scope whenever `observe` was permitted, and a grant a person gave for
**one session** would silently read back the whole machine's recorded history.

Therefore the tool **must name the environment it is searching**, and "search everywhere" —
the top rung of the recall ladder — exists only when the grant's own scope carries those
environments. Searching everywhere becomes a thing a person granted, never a side effect of
an argument left empty.

Two consequences worth stating rather than discovering:

- The run's grant is minted per session today (`grantFor(p.SessionID)`), and a session's
  environment is derived by `environmentForSession`. A search wider than the pane it was
  asked in therefore needs a grant wider than today's — which is a **policy surface**
  question (what the person is offered, in the Agent policy screen), not a tool question.
  It is named here and owned by its own bead.
- A refusal here must reach the person as a refusal they can act on, not as a terminal
  "tool call refused". That is `nocx-avogl.3`'s seam, and this design depends on it rather
  than restating it.

## 3. What comes back, and what deliberately does not

`ledger.search` returns rows and a coverage statement. A row is the **facts about an entry**
— the intent, the environment, the cwd, the time, how it ended, and the **handle** of its
output artifact. A row is never the output text.

**The model reads the detail with the same mechanism everything else reads it with**: the
handle plus the bounded-slice tool that the attach design's "read more" follow-on bead
introduces, over the artifact that already exists in the ledger. Two ways to fetch a stored
result would be two ways to get them out of step — this is the same sentence
`what-the-model-is-told` §2 uses about tool traces, and it is the same mechanism.

**Coverage is not optional** (`nocx-rtg0.21`, design §5.4, ADR-0019 §7). A search that
answers out of a window retention has evicted, and says nothing about the hole, lies by
omission. The tool passes coverage through to the model as part of the result, and the
prompt's standing rules make it answerable: an assistant that says "I found nothing" when
the truth is "nothing was kept" has given a wrong answer with a straight face.

## 4. What may leave for the provider

Nothing new is built here, and that is the point of writing it down.

Search results are **tool results**, so they pass `internal/assistant/egress.go` like every
other tool result: the recognizer is `internal/secrets` through the masking service, a
finding suspends the run and shows the person what was found, and the material itself never
travels. Intent was already masked on the way into the ledger.

**The hard edge is `nocx-jrdy`.** That epic exists because retaining and indexing output is
what makes _printed_ secrets — `env`, `kubectl get secret -o yaml`, a script that echoes a
token — reachable by substring; composition-time detection cannot see them by construction.
Handing that same index to a model, which then sends what it selects to a third-party
endpoint, is strictly worse than showing it to the person sitting in front of it. So:

> **No index over output exists before `nocx-jrdy` is closed, and this epic carries a
> dependency edge on it — not on the argument that jrdy is more important, but because they
> touch the same code and the same corpus.**

The edge on the search itself is spelled one level up: `bd` refuses an epic-blocks-task edge,
so the epic depends on **`nocx-rtg0`**, which owns `nocx-rtg0.21`. The precise blocker is the
task, and it is named here and in the epic's body so the coarser edge does not lose it.

Search over **intent** is a different case and is already governed: intent is masked at
composition time, and `ms7v.9` shipped the person's search over it.

## 5. What this epic is, and what it is not

**It is:** a row in the registry, a params schema under `contracts/` with
`additionalProperties: false` and an explicit `required`, a `Narrow` that hands the tool a
reader scoped to the grant's environments, the model-facing sentence, and the end-to-end
check above.

**It is not** `ledger.search` itself. That is `nocx-rtg0.21` and it blocks this.

## Deliberately out

- **An index, a predicate or a ranking of the assistant's own.** §1.
- **Search over the live screen.** That is `readScreen`, which already exists.
- **Cross-pane conversation memory.** `what-the-model-is-told` puts it out explicitly; a
  search that reaches other panes' _recorded work_ is not that, and the difference is that
  one is a durable record under a grant and the other is another conversation's context.
- **Widening the grant.** The policy surface that would let a person grant more than one
  environment is named in §2 and owned elsewhere. This epic does not widen anything; if the
  grant is one session, the tool searches one environment.
- **Deciding `ms7v.7`** (the FTS indexing unit, and whether substring search is promised).
  It is upstream, it is unanswered, and answering it inside this epic is how a settled
  question stops being settled.

## Risks and open questions

- **`nocx-rtg0.21` and `nocx-ms7v.2` may be the same work under two beads.** Both want FTS
  over intent and derived output; one is filed under "your commands survive a restart", the
  other under the search epic. This design consumes whichever ships, but two owners of one
  index is exactly the defect §1 is about. **Triage before either starts.**
- **A model that searches too eagerly costs the person money and context.** The result is
  bounded by `Limit` and the rows carry no output text, which is the cheap half; the
  standing prompt's "how to answer" is the other half.
- **Coverage is only as honest as retention's own record.** If the store cannot say what it
  evicted, the tool cannot either, and the correct behaviour is to say that rather than to
  imply completeness.

## Order of work

1. Triage `nocx-rtg0.21` against `nocx-ms7v.2` — one owner, or a stated split.
2. `nocx-jrdy` (printed secrets) — before any index over output exists.
3. `ledger.search` in the store and on the wire (`nocx-rtg0.21`), with coverage.
4. **This epic**: the registry row, the schema, the `Narrow`, the sentence, the e2e check.
5. The policy surface for granting more than one environment — its own bead, named in §2.
