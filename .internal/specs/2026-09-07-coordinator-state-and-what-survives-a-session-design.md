# The coordinator's state is not in its transcript, and what it concluded outlives the window

Bead: `nocx-gt0mk`. Serves `nocx-dkawo` (the wave), `nocx-0s2gh` (the conversation survives),
`nocx-ot16h` (the goal it states) and `nocx-rb8ka` (what may be captured, and what may not).
Extends `.internal/specs/2026-09-03-mesh-and-what-progress-may-decide-design.md` — **which is not
on `main`**: its commit `e301aef5` is an ancestor of `feat/agent-orchestration` and several
worker branches only, though the bead that produced it is closed. That is a defect in its own
right and is filed separately; this document reads it from the commit object.

## 1. In one sentence

Three complaints — compaction loses the task, the coordinator stops while workers are still
alive, and nobody can see how far a session has got — are one defect stated three ways: **the
coordinator's working state is kept in the transcript, which is the one place the product
periodically deletes.** The fix is not a memory system. It is moving that state into records
nocx already owns, enforcing the invariants over those records with no model at all, and paying
for a model only where a conclusion has to be read out of prose.

## 2. What this crosses, and what those documents already decided

**AD-1** splits the wire; **AD-6** forbids the backend to infer meaning from terminal bytes;
**AD-7** makes the session identity backend-owned; **AD-8** puts every module behind one
interface. None of them is amended here.

**The mesh design (2026-09-03) already decided the progress question, and this document does not
reopen it.** P1–P5: a checkpoint is a named milestone, declared on reaching it, appended and
never overwritten, with no denominator, an optional approximate estimate and an optional artifact
reference. P4: a checkpoint does not wake the coordinator. P5: the declared row and the measured
row are never merged. §4's three-source table says what each may decide, and D9 is untouched by
all of them: **only process exit and the participant's own terminal declaration decide wave
state.**

**Its §7 already put a model on the seam this document reuses, under rules that bind here too.**
P6: the advisor may only ever _worsen_ the picture — it may never confirm progress and may never
authorise anything. P7: it is consulted on a transition, never continuously. And the advisor
reads the _transcript_, not the backend's VT grid, precisely so that the AD-6 carve-out is not
widened by side effect. The extractor introduced in §6 below is the second consumer of that seam
and inherits all three rules verbatim, including `nocx-01ud6`'s: **the transcript is untrusted
input — data, never instructions.**

**`nocx-0s2gh.3` already decided how a prompt survives compaction**: the system prompt is rebuilt
and never summarised, every question the person typed stays verbatim, and tool traces ride as
facts with a handle rather than as their text. §5 D3 below is that rule applied to the
coordinator's goal rather than to the conversation.

**`nocx-rb8ka` already decided what may not be captured** — environment-dependent failures,
negative claims about tools, transient errors that resolved, one-off narratives, and above all
unresolved failures written up as validated guidance. That list is a precondition of §6, not a
refinement of it.

## 3. The problem, measured rather than assumed

The owner runs worker waves daily. Three failures recur, and each is usually described as a
memory problem:

1. **Compaction loses the original task.** It cannot do otherwise: the task is a message, and
   messages are what compaction folds.
2. **The coordinator stops with live workers.** The mitigation today is a `goal` command the
   human must remember to type — a reminder standing in for an invariant.
3. **Session progress is opaque, and the window can run out before the work does.**

None of the three is repaired by better recall. A transcript index answers _what somebody said_,
and AGENTS.md already records why that is the wrong question: "a transcript is evidence of what
somebody did, not of what is true now — it carries the wrong turn as faithfully as the fix."
A search over conversation therefore returns a rejected option with the same confidence as the
chosen one, and searching harder makes it worse.

**The trigger cannot be an artifact, either.** A rule of the form "a result exists and no record
covers it" fires most often on mechanical work, where the commit message already records
everything, and stays silent through a design conversation, which produces the conclusions worth
keeping and no files at all. The session that produced this document is the counterexample:
several decisions, zero commits until this one.

## 4. What was measured, 2026-09-07

Believe the binary. Every line here was run on this machine against `claude-code-2.1.260`.

- **MCP sampling is not available.** A stub MCP server recorded the client's `initialize`
  params twice, once under `--bare` and once in a real turn: `capabilities` is
  `{roots: {listChanged: true}, elicitation: {}}`. There is no `sampling`, so the MCP route for
  "ask the host to run a completion" does not exist in this client. It may exist in another;
  codex has not been probed.
- **Hooks are the route, and there are five kinds.** The binary carries
  `BashCommandHookSchema`, `PromptHookSchema`, `HttpHookSchema`, `AgentHookSchema` and
  `McpToolHookSchema`; the documentation confirms all five are accepted on every event.
  `type: "prompt"` runs a fast model over the event JSON with no tools. `type: "agent"` spawns a
  subagent with `Read`/`Grep`/`Glob`, so it can open `transcript_path` — and is marked
  **experimental**.
- **`Stop` and `PreCompact` can both block**, via `permissionDecision` or exit 2, and both carry
  `transcript_path`. `PreCompact` also carries a `manual|auto` matcher. `PostCompact` and
  `SessionEnd` cannot block; `SessionEnd` matches on `clear|resume|logout|prompt_input_exit|other`.
- **Pacing is expressible in exactly one place.** `if` applies to tool events only; `once`
  applies to hooks declared in **skill frontmatter** only, where it removes the hook after its
  first _successful_ run — and a blocking exit 2 does **not** consume it. In settings files
  `once` is ignored.
- **The `http` type is already being hand-rolled here.** orca's `codex-hook.sh` builds a
  `curl -X POST http://127.0.0.1:$ORCA_AGENT_HOOK_PORT/hook/codex` with a token header. nocx is a
  server; the native type replaces the script.
- **codex accepts the same event vocabulary** — `~/.codex/config.toml` carries trusted hashes for
  `session_start`, `user_prompt_submit`, `pre_tool_use`, `post_tool_use`, `pre_compact`,
  `post_compact`, `stop`, `subagent_start`, `subagent_stop`, `permission_request`. Its _client
  capabilities_ are unprobed, and its hook-type support is not assumed.

**Prior art the owner already built and priced** (`shady2k/hermes-lifemodel`): the trigger for an
extraction is 0-LLM and reads a buffer; three gates stand before the spend — single-flight, a
minimum interval (`15 minutes`, tuned live on 2026-07-17 down from 30), and a durable daily
ceiling (`50` calls) whose module states the reason plainly: "the aux model slot is routing, not
a cost ceiling — the host never enforces a spend cap on our behalf." The apply side validates the
model's proposed seeds **against the segment actually shown to it** and drops any whose source
ids were never surveyed; a window is claimed before the call and released on a transient failure
so the same segment is re-surveyed rather than stranded.

## 5. Decisions

| #   | Decision                                                                                                                                                                                                                        | Rejected alternative, and why                                                                                                                                                                                    |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S1  | **The coordinator's working state lives in nocx records, not in its transcript.** The goal, the participant set, the wave record and the declared checkpoints are rows; the transcript holds none of them                       | Keeping them in the conversation and defending them at compaction. Compaction's whole job is to fold messages, so this asks the mechanism to make an exception for exactly the messages it cannot identify       |
| S2  | **Invariants over those records are enforced with no model.** On `Stop`, a `mcp_tool` or `http` hook asks nocx, and nocx answers from the wave record; an unfinished wave returns `permissionDecision: "block"` with the reason | A reminder injected into the prompt. A reminder is persuasion in §5's sense — it raises probability and carries no invariant — and it spends the very window that is scarce                                      |
| S3  | **The goal is re-injected after every compaction and on every resume**, by handle and headline, with the body fetched over MCP on demand                                                                                        | Re-injecting the body. It reproduces `nocx-0s2gh.3`'s rejected shape — text where a handle belongs — and pays for it in the scarce resource                                                                      |
| S4  | **A conclusion is extracted by a model only on rare events**: `PreCompact(auto)` and `SessionEnd`. The extractor is an `agent` hook; it reads `transcript_path` in its own window and returns a structured record               | An extraction per turn, or per `Stop`. A hundred-turn session would pay a hundred subagents to learn what the free predicates already report for most of them                                                    |
| S5  | **The extractor may only propose.** It may not write to `docs/decisions/`, may not close a bead, and may not authorise anything                                                                                                 | An extractor that files its own ADRs. It floods a reviewed directory, and it is the same asymmetry P6 already refused: a second model that can confirm is correlated error wearing the authority of independence |
| S6  | **Every extracted claim is validated against the segment actually surveyed**, and one whose source ids were never shown is dropped                                                                                              | Trusting a schema-valid answer. A JSON schema constrains the shape of a claim and says nothing about whether the claim was in the material                                                                       |
| S7  | **An accepted decision is a document, and nocx stores a pointer to it — never a copy**                                                                                                                                          | Storing the decision in nocx. A machine-local store rides in no pull request, so nothing in it can bind a colleague; and a copy beside an original drifts, then wins somewhere nobody looked                     |
| S8  | **A rejected option is a first-class record, with its reason.** It is the one category with no other home                                                                                                                       | Recording only what was adopted. The rejected option is what the next session proposes again, precisely because it looks good — ADR-0004 has already been re-proposed once, in the order it rejects              |
| S9  | **The floor under S4 is one ask per session, declared in skill frontmatter with `once: true`** — which by its own semantics asks until it once succeeds, since a blocking exit does not consume it                              | A counter of our own, or a nag every N turns. The first reimplements a native semantic; the second decays into a ritual answer, the failure AGENTS.md names for gates people learn to pay cheaply                |

## 6. The record

One shape, whatever produced it:

- **claim** — one sentence, the thing concluded;
- **status** — `accepted`, `rejected` (with its reason) or `stale`;
- **scope** — the files, module or epic it bears on; this is the injection key;
- **origin** — who concluded it, in which session, at which offset;
- **document** — for `accepted` only: the ADR path, bead id or commit that holds it.

`scope` is what makes injection possible without keyword matching: nocx knows what a run touches,
so a record about the vault arrives when a run touches `internal/vault`, not when somebody types
the word. An index over conversation can only match phrasing, which is why duplicate beads here
are paraphrases rather than near-copies.

A record with no `scope` or no `status` is structurally empty and does not satisfy S9's ask. The
check is a schema, not a judgement — the same `additionalProperties: false` plus explicit
`required` that makes the wire contracts exact.

## 7. Accepted decisions are documents, and the gate is about their absence

AGENTS.md settles this for recall and it settles it here: "nothing you write into deja can be
relied on by a colleague… a rule the next person must follow goes in this file, where a pull
request makes somebody read it." nocx's records are machine-local by the same test, so a decision
that must bind anyone is an ADR in `docs/decisions/` or a rule in AGENTS.md, exactly as before.

What the memory layer adds is therefore not storage but a **detectable condition**: _a decision
was accepted and no document holds it._ That is checkable rather than tasteful, and `PreCompact`
can `deny` on it — refusing to fold the material until a pointer exists. The bar for which
decisions deserve an ADR is already written in the `brainstorming` skill (hard to reverse **and**
surprising without context **and** a genuine trade-off); below it, the lighter record is the
right home. The skill's own rule stands: the human confirms, the agent never auto-creates.

## 8. Carriers

| Need                          | Event                                          | Type                             | Blocks                     |
| ----------------------------- | ---------------------------------------------- | -------------------------------- | -------------------------- |
| live workers, unfinished wave | `Stop`                                         | `mcp_tool` / `http` → nocx       | yes, `block`               |
| goal after a fold             | `PostCompact`, `SessionStart(compact\|resume)` | `http` → `additionalContext`     | no                         |
| extraction of conclusions     | `PreCompact(auto)`                             | `agent`, reads `transcript_path` | yes, `deny`                |
| final extraction              | `SessionEnd(<reason>)`                         | `agent`                          | no                         |
| the once-per-session floor    | `Stop`, in skill frontmatter                   | any, `once: true`                | yes; exit 2 keeps it armed |

nocx installs these itself, into the agents it launches, scoped to that process — never by
mutating a user-level settings file. `SessionEnd` supports no decision and may not fire at all on
a killed process, which is why `PreCompact` and the S9 floor are not conveniences but the
insurance.

## 9. Deliberately out

- **Indexing transcripts for search.** deja stays personal recall on the developer's machine and
  does not move into the product; the owner's decision that the mechanism is shared and the
  database is not already settles it.
- **Any capture without `nocx-rb8ka`'s exclusion list.** A trigger without it writes the wrong
  records faster.
- **Re-deciding progress.** P1–P7 hold as written.
- **An extractor that confirms.** P6, applied to a second subject.

## 10. Open questions

1. **What is a session?** `--resume`, `/clear` and compaction each make a vendor boundary that
   may or may not be the working unit. The unit must be one nocx owns — a run or a wave — and
   which is not yet chosen.
2. **The numbers.** lifemodel's 15 minutes and 50 calls were tuned on a continuously living
   system; a coding session is bursty. The mechanism transfers, the constants do not.
3. **`agent` on every `Stop` versus spreading across event frequency.** §8 assumes the second.
   The first is a stronger guarantee at a cost nobody has measured here.
4. **What enters the buffer** — a whole turn, or a pre-filtered slice.
5. **codex's client capabilities and hook-type support**, by the same probe.

## 11. Assertions

- A `Stop` on a session whose wave has unfinished participants is blocked, and the reason names
  them. No model is invoked on that path.
- A run whose goal was set survives an automatic compaction and states the same goal afterwards.
- An extracted claim whose source ids were not in the surveyed segment never reaches a record.
- A record with no `scope` or no `status` does not satisfy the once-per-session ask.
- The extractor writes nothing under `docs/decisions/`.
- An `accepted` record whose document is absent is reported, and `PreCompact` denies on it.

## 12. What would falsify this design

- If a coordinator can be shown to stop with live workers **while** the `Stop` carrier is
  installed and the wave record is correct, S2 is wrong and the invariant does not live where
  this document puts it.
- If the extraction at `PreCompact` and `SessionEnd` misses the conclusions of a session whose
  transcript demonstrably contains them, S4's rarity is too rare and the trigger must move.
- If `once: true` proves not to survive a blocking exit in practice, S9 has no floor and needs
  a mechanism of our own after all.
