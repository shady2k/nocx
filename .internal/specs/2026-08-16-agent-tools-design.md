# The agent's tools: the agent is a user of the terminal, not a second implementation of one

- **Date:** 2026-08-16
- **Owner:** shady2k
- **Brainstorming bead:** `nocx-4aobp`
- **Epics this feeds:** `nocx-5u3oz` (reading granted blocks — the first tool), `nocx-lndv`
  (the policy and the narrowed capability), `nocx-dw3` (agent mode: the agent acts).
  `nocx-x8s2` owns the surface and the EXPLAIN rung this builds on.

## What a user can do that they could not before

**Ask the assistant to do something, and watch it do it in the terminal you are looking
at** — run a command, read output too long to paste, act on a file or a repository — where
every effect it produces is a block or an audited action with the agent named on it, and
every effect it is not permitted is never offered to the model at all.

The end-to-end check that watches this happen is in [Acceptance](#acceptance-criteria).

---

## 1. What this rests on, before it says what to build

| Decision        | What it already decided                                                                                                                                                                                                                                                                                                           | What this design must therefore do                                                                                                                                                              |
| --------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6**        | Terminal render state lives in the **frontend**; the backend never sniffs the byte stream.                                                                                                                                                                                                                                        | The backend never parses OSC 133 out of the PTY stream to learn that a command finished, and never reconstructs output text from bytes. Both facts come from the renderer, which owns the grid. |
| **AD-7**        | Sessions are server-authoritative; the PTY lives in Go (`internal/session`, `Write`/`EnqueueWrite`).                                                                                                                                                                                                                              | The backend **could** write to a lane's PTY directly and deliberately does not — see §2.                                                                                                        |
| **AD-8**        | One owner per behaviour; variation is expressed by the interface, never by a fork inside an implementation.                                                                                                                                                                                                                       | A tool and a JSON-RPC method are **two clients of one owner**, never one wrapping the other. The tool calls `internal/git`; it does not call `git.status` over a socket to itself.              |
| **AD-1**        | One WebSocket: binary data plane + JSON-RPC 2.0 control plane. Ledger facts may cross; raw bytes may not.                                                                                                                                                                                                                         | Effect requests and their results are JSON-RPC. PTY bytes are never wrapped.                                                                                                                    |
| **ADR-0004**    | An input-ownership state machine; `InputTargetRegistry` decides where a submission goes; agent input is an `AgentInputTarget` on the same editor.                                                                                                                                                                                 | The agent's command reaches the shell through the **same submit orchestration** a human's does. There is no second submission decider.                                                          |
| **ADR-0019**    | One authoritative ledger, disposable projections. A human command and an agent command are both `entries`, distinguished by `kind`, joined by `caused-by` edges. Adjacency must never imply causation.                                                                                                                            | Attribution lives on the entry. The audit is a **projection** over the same ledger, never a second store.                                                                                       |
| **ADR-0020**    | The agent gets a **lane**, never the user's PTY; execution runs under a **lease**; authority is a **per-run grant over resources and effects**, immutable once execution starts; policy decides act/ask/refuse; low classification confidence **escalates on its own**; on interactivity the agent is demoted to read-and-advise. | The grant is never over tool names. The classifier's job is honesty about confidence, not cleverness.                                                                                           |
| **ADR-0021**    | Masking is for **durable rows**, in exactly one place (`internal/transport/ws_history_record.go`); the recognizer is separate (`internal/secrets.Detect`). "An honest redaction that says nothing is indistinguishable from there having been nothing to redact."                                                                 | Egress to a model provider is a **different consumer of the same recognizer**, with a stricter policy on a hit. Not a second detector.                                                          |
| **ADR-0028**    | eino runs the loop in-process; the grant is ours; the policy sits in the framework's own middleware; a refusal is a control decision, never a tool result.                                                                                                                                                                        | The pipeline of §6 is written at eino's seams, not beside them.                                                                                                                                 |
| **ADR-0029**    | A proposed keystroke is bound to what makes it meaningful. At the instant a byte enters the lane **there is no model in the chain**; the final gate is local and synchronous. Generation inequality is a trigger, not a verdict.                                                                                                  | Keystroke delivery is decided in the renderer. That gate can only ever **subtract**.                                                                                                            |
| **ADR-0011 §2** | A secret never comes back out of the backend.                                                                                                                                                                                                                                                                                     | The egress gate may **compare** against known vault material, because the comparison happens in the backend and nothing leaves.                                                                 |

**Two rules from `AGENTS.md` that shaped this document more than any of the above.** _Look for
the existing answer before you write a second one_ — most of what follows is an existing owner
gaining a caller. And _two surfaces may never own the same input_ — which is what killed the
first draft of §2.

---

## 2. Who executes

**The agent is a user of the terminal, not a second implementation of one.** Everything below
follows from that sentence; nothing below is a preference.

### 2.1 The rejected shape, and why it is written down

The first draft had the backend write `ls\r` into its lane's PTY directly. `session.Write` and
`session.EnqueueWrite` exist (`internal/session/session.go:90,97`), the PTY is backend-owned by
AD-7, and it is the shortest path: no round trip, no renderer dependency, the agent survives a
closed window.

The owner rejected it, and the reason is the repository's own rule: a direct PTY write bypasses
the submit orchestration the composition root runs for `routesToShell` targets — the keyboard
handoff, the ledger record, the running block, the lifecycle attempt. The agent's command would
exist as **bytes with no entry**: not in the block flow, not in history, not in the audit. That
is a second input surface, and one that is invisible. The first draft bought a clean seam and
paid for it with a shadow.

### 2.2 The boundary, in two halves

**Everything a human does with the keyboard, the agent does through the same surface.** Submit a
command, send keys to a full-screen program, read the screen, read a block's output — all through
the renderer, through the same `InputTarget` and the same orchestration. The consequence is not a
feature we add: the agent's command gets a block, a ledger entry, an attempt and an output
artifact **because there is no other path**. A shadow cannot be created because there is nowhere
to create one.

**Everything the product does on the human's behalf stays where the product does it.** Signals to
a process group (`INT → TERM → KILL`), lease deadlines, the output budget, tearing down a wedged
lane, terminalizing a run — all Go. A human does not do these with the keyboard either; the
product does them for them. So `Ctrl-C` as a keystroke (through the renderer) and `SIGINT` to a
process group (in Go) are not duplicates: they are two different actions, and they are two
different actions in the UI as well.

**A consequence worth stating rather than leaving implicit: the agent's lane is necessarily a
rendered session.** If the renderer executes, there must be a renderer-side session to execute in.
So ADR-0020's "the lane is projected into the block flow" stops being a product promise we must
remember to keep and becomes structural — invisible agent work has nowhere to happen.

**Authority crosses in neither direction.** The grant, the policy, the attempt record and the
narrowed capability are all Go, all evaluated before a request goes down. The renderer never
learns the word "grant". The one decision it does make — ADR-0029's delivery gate — **can only
subtract**: it refuses delivery of a key that was already permitted. It can never widen.

### 2.3 Where the text comes from, and why there are only two answers

The agent runs `ls`. The bytes return from the PTY into the backend, which already has them. It
still cannot answer "what did that print", because bytes are not output: producing what a person
would have seen means interpreting CR, backspace, erase-in-line, wrapping and cursor motion. On
`ls` the difference is invisible; on `npm install` with a progress bar, raw bytes are noise. And
learning _that the command finished at all_ means recognising OSC 133 in the stream, which is
precisely what AD-6 forbids the backend to do.

So a grid must exist somewhere, and there are exactly two places it can be:

|                                                   | the renderer's grid (chosen)             | a headless VT in Go                                                             |
| ------------------------------------------------- | ---------------------------------------- | ------------------------------------------------------------------------------- |
| autonomy with the window closed                   | no                                       | yes                                                                             |
| testing the happy path                            | needs a browser                          | `cmd/devharness`, a Go test                                                     |
| VT implementations in the product                 | one                                      | two                                                                             |
| divergence when an agent lane is shown to a human | impossible                               | possible — the worst defect class in this repo                                  |
| what must be built                                | wait for a fact `nocx-2f0f` needs anyway | a new dependency, grid ownership, **and** `nocx-2f0f` regardless, for the human |

**Decision: the renderer's grid.** The headless VT is recorded as a revisit condition, not a
rejected idea — see §8.

### 2.4 The channel

The backend asks the renderer to produce an effect and waits for its result. This pattern is
already written twice — `vault.unlockRequest`/`unlockResolved` and
`connections.passwordRequest`/`passwordResolved`, brokered in
`internal/transport/unlock_requester.go` with minted request ids, closed outcome enums, timeouts
and an honest "unknown request id" for a stale one.

**Decision: the third time it becomes the mechanism, not a third copy.** Server-to-client requests
with real ids and results — which JSON-RPC 2.0 is bidirectional for, and which is how LSP works,
the precedent AD-1 names for choosing it. The two existing brokers become its first customers, so
timeouts, id minting and validation stop being duplicated per feature.

**Accepted cost, stated as a decision rather than an accident:** the agent is alive while the
renderer is alive. A closed window is no run. A renderer reload mid-run leaves the run
non-terminal, and the existing sweep — every non-terminal run becomes `interrupted` at start —
is what makes that honest. §8 carries what this forbids us to promise.

---

## 3. How the agent's work is marked

### 3.1 In the block flow: the command, and only the command

A command carries its author. That is the whole requirement, and it is sufficient: "this command
was run by the agent" needs no elaboration in the flow.

**The author is minted at submit, by the target that submits, and is never derived afterwards.**
`ShellInputTarget` means the human; the agent's target means the agent. Deriving it later — "the
commands during an agent run are the agent's" — breaks exactly where it hurts: you type your own
command while the agent works, and it is attributed to the agent. That is the interleaving
ADR-0019 §2 warns about, arriving through the back door.

The durable half already exists: `entries.kind IN ('shell','agent','action')`
(`internal/content/sqlite.go:570`), `executions.lane` ("agent lane; NULL for a human's shell"),
`executions.executor`. The gap is the renderer's: `CommandRecord`
(`frontend/src/command-ledger.ts:20-33`) carries command, cwd, host, status, exit code and times —
and no author at all. It gains one, from the submitting target, and the fact it sends up carries
it, so the two sides never derive the same thing twice.

**Why a command is caused, not merely adjacent:** the `caused-by` edge ties an agent command to
the question that produced it, so "why did this run" is answerable even when blocks from two
authors interleave.

### 3.2 Outside the block flow: the audit

Things the agent does that print nothing — `files.write`, `git.commit`, opening or closing a tab —
have no business in the block flow and must not be invented into it. They are recorded as an
audit.

**The audit is a projection over `kind='action'` entries in the same ledger, not a second store**
(ADR-0019). The schema for it exists: the design's payload union already has the `action` arm —
`actionId`, `effect`, `approval`, `result` — of which only the `shell` arm is implemented today.

So "what did the agent do" is one query, and it includes what left no trace on screen.

---

## 4. The catalogue

Three classes. The class decides who executes and where it is visible.

### 4.1 Terminal — through the renderer, visible in the flow as commands with an author

| tool                              | returns                                                   | note                                                                      |
| --------------------------------- | --------------------------------------------------------- | ------------------------------------------------------------------------- |
| `run(lane, command)`              | entry id, then exit status and a window of the output     | the same submit as a human's: block, ledger entry, attempt, artifact      |
| `readOutput(entry, start, count)` | total, the window, and which window was actually returned | the `read_terminal` shape; also serves the granted blocks of `nocx-5u3oz` |
| `readScreen(lane, region?)`       | cells with attributes, cursor, capture identity           | today a renderer **push** (`agent.captureFrame`); becomes a **pull**      |
| `sendKeys(lane, keys, boundTo)`   | delivered, or refused with the reason                     | ADR-0029's gate, evaluated in the renderer; subtracts only                |

### 4.2 Domain — executed in Go against an existing owner; invisible in the flow, mandatory in the audit

| tool                                                | owner in the repo                          | effect                                  |
| --------------------------------------------------- | ------------------------------------------ | --------------------------------------- |
| `files.list` / `read` / `write` / `move` / `delete` | `internal/filesystem`                      | observe / mutate, by operation and path |
| `git.status` / `log` / `diff` / `stage` / `commit`  | `internal/git`                             | observe / mutate-reversible             |
| `ports.list`, `connections.list`                    | `internal/nativeports`, `internal/profile` | observe                                 |
| `history.search`                                    | `internal/content`                         | observe                                 |

These call the owner directly. The JSON-RPC method of the same name is the renderer's client of
that same owner (AD-8) — the tool does not go through it, and a tool that did would put the
authority check on the far side of a socket from the thing it protects, which is the mistake
ADR-0028 rejected an external agent for.

### 4.3 Application — audit only

`tabs.open` / `close` / `focus`, and later workspace operations. `close` is the only destructive
member of the class: a live process stands behind the tab.

### 4.4 Two rules across the whole catalogue

**Every tool that returns text returns a window** — total, an explicit window, and a statement of
which window was actually returned. Without it one `files.read` on a large log consumes the
context the run needs.

**Every return path passes the egress gate of §7, including error paths.** An error string is
output too: it carries paths, hostnames and names.

### 4.5 Deliberately absent: the vault

The agent never reads a secret. It **uses** a connection whose credential the vault resolves on
its behalf. A tool that returned secret material would be `disclose` with a model provider as the
recipient, which no grant should be able to express.

---

## 5. Declaring a tool: one row, and the machinery reads it

The tempting shortcut — a boolean on existing JSON-RPC methods, so the catalogue derives itself —
is rejected for three reasons:

1. **The grant is over resources and effects, never names.** A boolean on a method is a grant over
   a name wearing a declaration's clothes: the policy is left with nothing to evaluate, because
   the effect class and the resources touched were never stated.
2. **The RPC surface was designed for the renderer.** The renderer's parameters come from a human
   gesture and it operates on handles it was itself given (`files.open` returns one). The model is
   untrusted input. `files.read` for the renderer means "read this open document"; for the model
   it must be bounded by the grant's paths, windowed and screened. That is a different function,
   not a decorated one.
3. **The default would invert.** Today a tool is not declared unless someone declares it, and the
   strongest refusal is the one never proposed (`BeforeAgent`). Auto-exposure makes "exposed" the
   default and a forgotten flag a silent grant.

What **is** structural is the declaration. Go has no annotations, so the equivalent is a table that
grows by addition plus tests for exhaustiveness:

```go
// the only place a tool comes into existence
{
  Name:      "files.read",
  Effect:    content.EffectObserve,                 // the ADR-0020 lattice
  Resources: []content.ResourceKind{content.ResourcePath},
  Executes:  InGo,                                  // or InRenderer
  Narrow:    filesystem.ScopedReader,               // constructs the narrowed capability
  Params:    "contracts/tools/files.read.schema.json",
}
```

Four consumers read that one row: `BeforeAgent` (what to declare to the model under this grant),
the policy (what to evaluate), the middleware (which narrowed capability to construct), and the
parameter schema the model is shown and its arguments are validated against.

**Three tests make it structural, not the table itself:**

- A tool with no schema in `contracts/` does not assemble into the set — what `contracts/README.md`
  already requires of results, extended to parameters (`nocx-e4bn` is the same requirement arriving
  from the other direction).
- `Effect` and `ResourceKind` are closed enums mirroring the `CHECK` constraints already in the
  schema (`grant_resources.resource_kind`), so "forgot to classify it" does not compile.
- A tool whose effect the grant does not permit is **absent from the set the engine hands the
  model** — asserted against the request actually built, not against our intent. This is the
  assertion `nocx-5u3oz.6` already names.

---

## 6. The pipeline of one tool call

This layer is `nocx-lndv`, at eino's seams. **It sequences and enforces; it does not implement.**
Masking has an owner (ADR-0021), the audit has an owner (the ledger), token usage has an owner
(the model client). A layer that implements any of them itself produces a second answer that agrees
until the day it does not.

1. **Declaration lookup.** A name absent from the registry is not a refusal — it is malformed model
   output; there is nothing to call.
2. **Parameter validation** against the tool's schema: `additionalProperties: false`, an explicit
   `required`, and an ingress size bound. The transport already applies this discipline to the
   renderer's parameters; the model is a **less** trusted source than the renderer and gets the
   same check, never a weaker one.
3. **Classification and policy** — permit / ask / refuse over the ADR-0020 lattice. A refusal
   returns `ErrPolicyRefused` and terminalizes the run rather than becoming a tool result the model
   works around; the batch latch then refuses every later call in the same model response. An
   escalation is `StatefulInterrupt` **before** `next`, so the call that is asking has not run.
4. **The attempt is written — before the call.** If that write fails, no capability is constructed,
   `next` is not called, and the run fails with a terminal infrastructure error. Otherwise an
   interrupted run cannot say the only thing that matters: _this may already have happened._
5. **The narrowed capability is constructed.** The tool receives only that, so it cannot exceed the
   grant because it never holds more (a check would leave it holding a full manager).
6. **Execution** — in Go, or as a request to the renderer. **The only step that differs**, and it
   differs by exactly one field of the declaration.
7. **Result ingest** — the egress gate (§7), the window, the "data, not instructions" framing
   (`nocx-5u3oz.10`), the size bound.
8. **The outcome is recorded** on the attempt. The audit is a projection and needs no separate act.

**Three invariants, each stated with both ends:**

- **Validation precedes policy.** A policy reading arguments that have not been validated is
  deciding about something that may not be what executes.
- **The attempt exists from before the effect until the outcome or a terminal reason is recorded.**
  Not "the attempt is written before the call" — that names a moment, and the interval is the thing.
- **Every return path is screened, including errors** — from the tool's return until the bytes are
  handed to the model client.

**Token accounting is not a step of this pipeline.** Usage is reported by the model client per
_model_ call, not per tool call, and accrues to the run's entry payload (`model`, `tokensIn`,
`tokensOut`, `toolCalls`). The per-tool budget that _is_ a step is the volume a tool returns —
step 7's window. One is about money, the other about context; merging them loses both.

---

## 7. Two gates, in two directions

### 7.1 Outbound — everything that leaves for the provider

The points are more numerous than they look: the question and the system prompt, the referenced
frames, **and every tool result**. A tool result reaches the model exactly as a frame does.

The recognizer is already factored: `internal/secrets` exposes `Detect(s) []Finding` beside
`Mask`, so "what looks like a secret" is already separate from "what we do about it". **One
recognizer, two policies** — not two detectors.

The policies differ because the consequences do:

|                     | durable history                    | outbound to a provider                |
| ------------------- | ---------------------------------- | ------------------------------------- |
| destination         | a local file, encrypted (ADR-0018) | a third party, irreversibly           |
| a miss costs        | a secret in local storage          | a secret at the provider, permanently |
| the honest response | mask and continue                  | **refuse, and say what was found**    |

**Decision (owner, 2026-08-16): refuse and ask.** Nothing is sent; the user is shown what was
found and where, and chooses: send it masked, send it as it is, or cancel. It is the same consent
surface as a destructive command, so a person meets one kind of question, not two. The accepted
cost is an interruption mid-run.

The reason a silent redaction is not acceptable here is ADR-0021's own sentence, which becomes
decisive when the destination is off-machine: _an honest redaction that says nothing is
indistinguishable from there having been nothing to redact._ A miss is invisible.

**The vault knows the real values**, and a comparison beats any pattern. It is legitimate here
precisely because it happens in the backend and nothing leaves — ADR-0011 §2 survives intact. Known
material and a heuristic match are different confidences and the surface says which one fired.

### 7.2 Inbound — everything the model proposes

Parameter schema, then effect classification, then policy, then the question to the human.

**A command line cannot be classified in the general case, and the classifier's job is to be
honest about that rather than clever.** `rm -rf` reads; `make deploy`, an alias, `$(...)` and
`curl … | sh` do not. ADR-0020 already decided the consequence: low confidence escalates on its
own, "because the apparent effect is the thing we cannot see". The default for anything unreadable
is to ask. A classifier that answers "probably safe" to an opaque string is worse than none,
because it converts an unknown into a permission.

**Approval binds to the exact proposal** — run, attempt, tool name, call id and a hash of the
canonical arguments — or approving one thing authorises a changed thing. It resumes as a **new
attempt with a new grant**, never as a widening of the running one.

**What approval honestly promises:** the call that is asking has not run, and no call after it in
that model response will. It does **not** promise the domain is untouched — a permitted sibling
earlier in the same batch has already run, and the surface says so rather than implying otherwise.

### 7.3 The symmetry

Both gates end at one surface — _allow this?_ — and both are recorded on the attempt. A person
meets one kind of question whether the risk was a secret going out or an effect coming in.

---

## 8. Open, and deliberately out

**Open — the autonomy question, now sharpened rather than answered.** §2.4 binds the agent's life
to the renderer's. `nocx-mp2vd` (an agent runs workers in sibling sessions) must be read against
that: if any part of it assumed background work with no UI, the assumption is wrong and is fixed
_there_, not here. The revisit condition for the headless VT of §2.3 is exactly this — when
"works with the window closed" becomes a requirement rather than a preference, that is the design
that delivers it, and §5's `Executes` field plus the declaration table is the seam it lands on
without a rewrite.

**Open — autonomy versus ADR-0020 rule 6 for keystrokes.** The owner's requirement is that the
agent works autonomously. ADR-0020 rule 6 and ADR-0029 rule 7 say agent-driven _input_ is
permanently low confidence and escalates by default, and that the local gate exists to keep an
untrusted path advisory. Both cannot hold for `sendKeys`. Autonomy under a grant is settled for
`run` and the domain tools; **for keystrokes it is not, and the decision is the owner's.** Nothing
in this design depends on which way it goes; the tool exists either way and the policy row changes.

**Out — the tool catalogue is not final and is not meant to be.** The owner's position, 2026-08-16:
add or remove later as needed. §5 exists so that costs one row.

**Out — a policy engine.** ADR-0020 §7 ships three presets over the policy function and calls the
full lattice "the enum we grow into, not a policy engine we build now".

**Out — anything remote**, multi-agent arbitration, and reasoning display.

---

## Acceptance criteria

Written as assertions, per `AGENTS.md` testing rule 4.

**The headline, and the one that closes the work:** a person asks the assistant to do something
that requires running a command; the command appears in the block flow **marked as the agent's**,
its output is read by the model in windows, and the answer names something that appeared in it.
Asserted end to end.

1. A command submitted by the agent produces a block, a ledger entry with `kind='agent'`, an
   attempt row and an output artifact — asserted on the ledger, not on the DOM. Stops being false
   exactly once: today there is no path by which the agent can submit at all.
2. The author on a block comes from the submitting target: a human command submitted **while an
   agent run is in flight** is attributed to the human. Asserted by interleaving the two.
3. The narrowed capability a terminal tool holds **exposes no direct PTY write** — asserted on
   what the tool is handed, so §2.1's shortcut cannot be reintroduced without a red test. Asserting
   the behavioural negative ("a direct write produces no entry") would only restate that the
   bypassed path bypasses; the narrowing is what is actually assertable.
4. A tool the run's grant does not permit is **absent from the tool set the engine hands the
   model** — asserted on the request actually built.
5. A tool holding a capability scoped to lane A cannot reach lane B — asserted by trying, not by
   inspecting.
6. A refusal terminalizes the run: no tool message is produced and no second model request is
   made. A test asserting only "`next` was not called" is insufficient and is named as such.
7. A refusal or escalation on the second of three calls in one model response prevents the third —
   the batch latch, asserted against eino's `sequentialRunToolCall`, which does not break on error.
8. A failed attempt write means the tool is not called: asserted with a ledger whose write fails.
9. A tool returning text longer than the window returns the total, the window, and which window it
   returned; a window past the end is answered honestly rather than as an error.
10. Text containing a value known to the vault is **not sent**: the run suspends and the surface
    names the finding. Asserted for a known value and, separately, for a heuristic match — and the
    surface distinguishes them.
11. An **error** returned by a tool is screened on the same path as a success — asserted with an
    error string containing a secret-shaped value.
12. An opaque command (`$(...)`, a pipe into a shell) escalates rather than being permitted, with
    the confidence recorded on the attempt. Paired with the other end: an ordinary, readable,
    non-destructive command under an autonomous preset **runs without asking** — the "and on a
    normal machine it succeeds" half of the interval.
13. An approval resumes as a new attempt bound to run, attempt, tool, call id and argument hash; a
    changed argument does not resume under the old approval.
14. A non-terminal run at startup becomes `interrupted`, and a run whose renderer vanished mid-call
    reaches a terminal state rather than hanging.
15. `tabs.close` by the agent produces an audit entry with `kind='action'` and its effect, and
    produces no block.

---

## Risks

**The renderer becomes load-bearing for the agent's happy path**, so the end-to-end check needs a
browser where `cmd/devharness` would have sufficed. Mitigation: everything except step 6 of §6 is
Go-testable, and the `Executes` field is the seam that keeps it that way.

**A second implementation of a gate.** The pipeline is the most tempting place in this codebase to
re-implement masking, audit or classification locally. §6's first paragraph is the rule; the review
question is "which owner does this step call".

**The declaration table rots into a place where tools are registered but not classified.** The
three tests of §5 are what stop it, and they are cheap; without them the table is documentation.

**`nocx-2f0f` is a hard dependency for the headline criterion** — the agent reads its own command's
output through the same capture the human's blocks need. It is open, 0/5. This design does not
duplicate it and must not; sequencing is the plan's problem, not this document's.
