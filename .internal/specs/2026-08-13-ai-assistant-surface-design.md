# The AI assistant: you point at what you see, and ask about it

- **Date:** 2026-08-13 (rewritten the same day after an adversarial stress test — see
  [Stress Test Results](#stress-test-results))
- **Owner:** shady2k
- **Brainstorming bead:** `nocx-reoe`
- **Epic:** `nocx-x8s2` (the ladder: EXPLAIN → GUIDE → DRIVE). Agent-mode work overlaps
  `nocx-dw3`; streaming notifications are `nocx-dw3.1`.

## What a user can do that they could not before

**Run any program — a command in a block, or a full-screen program that owns the
viewport — point at part of what is on the screen, and ask about it in ordinary
words, without leaving the terminal and without the program noticing.**

And, on the same seam and with the same engine: **ask the assistant to do something**,
where "something" is our own tools — read a block, run a command in the agent's own lane —
each one authorised at the moment it is called.

The end-to-end check that watches the first sentence happen is in
[Acceptance](#acceptance-criteria).

---

## 1. What this rests on, before it says what to build

| Decision                | What it already decided                                                                                                                                                                                                                                                                                                                                                                        | What this design must therefore do                                                                                                                                               |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6**                | Terminal render state lives in the **frontend**; the backend never sniffs the byte stream.                                                                                                                                                                                                                                                                                                     | A frame is **minted in the renderer** and travels up as data. The backend never reconstructs one from the PTY stream.                                                            |
| **AD-1**                | One WebSocket: raw binary data plane + JSON-RPC 2.0 control plane.                                                                                                                                                                                                                                                                                                                             | Assistant events stream as JSON-RPC notifications. PTY bytes are never wrapped.                                                                                                  |
| **AD-8**                | One owner per behaviour; modules behind interfaces, one composition root.                                                                                                                                                                                                                                                                                                                      | The engine is one dependency behind our composition root. Nothing in the product reads eino's state to answer a question the ledger answers.                                     |
| **ADR-0004**            | An input-ownership state machine; on the alternate screen the program owns the viewport and every key; agent input is an **`AgentInputTarget` on the same editor**, and `InputTargetRegistry` decides where a submission goes.                                                                                                                                                                 | The assistant is a _target_, not a second editor and not a second submission decider. The panel changes what is visible; the registry stays the only thing that routes a submit. |
| **ADR-0019**            | **One authoritative ledger.** Schema v1 — `environments`, `entries`, `edges`, `artifacts`, `artifact_chunks` — is designed (`nocx-rtg0.2`) and not implemented; `command_history` is interim and says so. A human command and an agent command are both `entries`, distinguished by `kind` and joined by `caused-by` edges. §6: derived text is an **artifact with provenance, not a string**. | Frames, questions and answers are entries and artifacts in that one ledger. No second store, no blob hidden in a text column.                                                    |
| **ADR-0020**            | The agent gets a lane, never the user's PTY; execution runs under a lease; **authority is a per-run grant, immutable once execution starts**; policy decides act/ask/refuse; on interactivity the agent is demoted to read-and-advise.                                                                                                                                                         | The **policy** is ours and sits in the framework's own middleware: refusal and escalation are decided before a domain call, never returned to the model as a result.             |
| **ADR-0018 / ADR-0021** | The ledger is encrypted at rest. Masking happens in exactly one place — `history.record` — and today it masks the **submitted command**, not output.                                                                                                                                                                                                                                           | Frames are output. The existing masking owner must be **extended to the artifact-ingest path**, never duplicated by a second detector.                                           |
| **ADR-0029**            | A proposed keystroke is bound to **what makes it meaningful**, not to the frame: generation inequality triggers a re-evaluation and is never itself the verdict, an identity change refuses outright, and **no model call sits in the delivery path**. Written after the owner found that identity-equality refusal makes every self-refreshing program permanently undrivable.                | Nothing in this slice may wire `generation != saved` to a refusal, and no surface may present generation drift to the user as "stale". The refusal itself arrives with DRIVE.    |
| **ADR-0016 / ADR-0017** | A secret owns its name; a record references a secret by opaque reference.                                                                                                                                                                                                                                                                                                                      | An endpoint stores a **secret reference**, never an API key.                                                                                                                     |

---

## 2. The model: one rule, no special cases

### 2.1 Pointing freezes. Always.

**Every ask is about a frame, and a frame never moves.** Nothing reasons about, or
annotates, a live surface.

The reason is correctness, not caution, and it came from watching `top` inside a block: its
rows reorder every few seconds. An annotation pinned to a live surface keeps its cells and
loses its meaning — it does not visibly go stale, it goes on looking fresh while pointing at
a different process.

So "a finished block is already immutable, only the alternate screen needs freezing" is
rejected: two rules for one concept, agreeing everywhere except on a repainting block, which
is the common case.

### 2.2 The nouns

```
capture identity   what a frame belongs to and can be compared against:
                   buffer instance (normal | alternate, and WHICH alt-screen session),
                   geometry (cols × rows), and a content generation.
frame              cells + attributes + cursor of one capture identity, minted in the
                   renderer at the instant of pointing. Text, not a picture.
region             a rectangle or row range inside a frame.
reference          frame + region. A question carries a list of them.
```

**There are two capture sources, and they are not the same path.** An earlier draft claimed
one; the code says otherwise, and the difference is visible to the user:

| Source                                | What it reads                                              | What its provenance must say                                                                                                 |
| ------------------------------------- | ---------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| **live** — alt screen, running block  | cells of the active xterm buffer                           | buffer instance, geometry, generation; that rows may be evicted by the 10 000-line scrollback cap while the block still runs |
| **frozen** — a block after its freeze | the serialized block, which is what the user actually sees | that wrapped lines are joined and leading/trailing blanks dropped by the serializer, plus the serializer version             |

A frozen block has **no xterm cells left** — they are cleared after the visual freeze — so it
cannot be read the live way, and its text has already been transformed. Saying "the same
gesture, the same path" would be false; the gesture is the same, the source is recorded.

### 2.3 Generation, not repaint — and identity, not just a number

The first draft said "a monotonic revision per surface, bumped on repaint". That is wrong
here for two reasons found in our own renderer:

- **A repaint is not a change.** `onRender` reports rows painted. Paints happen for focus,
  cursor blink, theme changes, viewport exposure and scrolling — and ADR-0005 has us
  **deliberately forcing periodic repaints on Linux WebKitGTK**. A frame taken from a
  motionless screen would go stale continuously, on one platform only.
- **A number alone cannot be compared across a discontinuity.** The active buffer switches
  between normal and alternate (`onBufferChange`); the alternate buffer's contents are
  discarded on exit; a resize reflows the normal buffer and shifts absolute line indices;
  scrollback is capped at 10 000 lines and evicts.

So staleness is a comparison of **capture identity**, not of one integer. Entering the
alternate screen mints a new identity; leaving it terminates that identity; a resize
terminates comparability. Across identities the answer is never "stale", it is **"not
comparable"** — a different sentence in the UI and a different rule in the code.

**What advances the generation, concretely.** "Buffer mutation" is not observable — xterm has
no mutation journal, and `write()` is input, not change. What _is_ observable is
**`onWriteParsed`** (present in the xterm typings, not yet exposed by our adapter), which
fires once a written chunk has been parsed into the buffer, plus the explicit state-changing
operations: buffer switch, resize, `clear`, `reset`.

That is deliberately conservative: a write that repaints identical cells still advances the
generation, so a screen can be reported as moved when it did not. **We prefer a false "it
moved" to a false "unchanged"** — the first costs a re-ask, the second delivers advice about
a screen that is gone.

**There is also a capture fence, and `onWriteParsed` alone cannot be it.** `write()` queues
parsing, so "the frame at that instant" is meaningless without one: a snapshot taken mid-queue
can hold row 1 from before a write and row 20 from after it, and its generation would then
describe no state that ever existed. The frame is taken after the parse settles.

But **`onWriteParsed` fires at the end of every parse pass, and a pass can land between the
chunks of one large write** — so waiting for one fire settles nothing, and this paragraph said
otherwise until the implementation found it (`nocx-3j9b`). The exact signal is xterm's
**per-write callback**, `write(data, cb)`, which fires when _that_ write's bytes have been
parsed: the renderer keeps a count of unsettled writes from it, and the fence waits until the
count is zero, re-checking after every `onWriteParsed` fire. `onWriteParsed` keeps the two jobs
it can do — advancing the generation, and waking the waiter.

A frozen block is the degenerate case: its identity is closed and its generation never
advances again.

### 2.4 Consequences

- **A question may span artifacts.** "This error (block #12) and this `k9s` screen —
  related?" is two references in one question.
- **An answer points back.** Annotations render on any frame.
- **Context is never invisible.** _There is no context that is not shown as a chip._ Ask
  with nothing selected and the chip reads "last 3 blocks" — visibly.

That last one is the product position: the model gets what you pointed at, not everything
you have ever typed.

---

## 3. The surface

### 3.1 The panel is the ordinary layout, restored

Normal use is _output above, flow, editor at the bottom_. On the alternate screen the editor
is gone because the program took the room. The assistant does not get a chat pane; **it gets
the room back**:

```
┌───────────────────────────────────────────────┐
│  htop  root@192.168.0.57               live   │  the live program, re-rendered at a
│  ▏1[|||    ] 2[|      ] 3[||||   ] …          │  smaller FONT. The PTY is untouched.
├───────────────────────────────────────────────┤
│ ▸ frame 14:32:07             ✓ still matches  │  the frame — an entry in the flow
│   ▏1[|||    ] 2[|      ] 3[||||   ] …         │
│   ┌ assistant ─────────────────────────────┐  │
│   │ row 12 is the one holding the memory.  │  │
│   └────────────────────────────────────────┘  │
├───────────────────────────────────────────────┤
│ > why is that process holding memory?         │  the ordinary editor
└───────────────────────────────────────────────┘
```

**There is no second input surface anywhere in the product.**

### 3.2 The PTY is never resized

The live program is shown smaller by re-rendering the same cells at a smaller font, not by
changing `rows`/`cols`. No `SIGWINCH`, no reflow, no misbehaving program. Terminal output is
text, so it stays crisp — the same reason masking works on a frame.

Below a floor where a dense program becomes unreadable, **our panel shrinks, not the
program**. The frame is rendered at the same reduced font as the live region: two pictures
of one screen must be comparable by eye.

### 3.3 Input ownership extends the existing machine; it does not add one

Keyboard routing is already owned — by lifecycle state, buffer state, editor visibility,
renderer read-only state and `takeKeyboardToGrid()`, with `InputTargetRegistry.active()`
deciding where a submission goes. A panel boolean layered on top would be a second owner,
and the two would disagree exactly when the program exits while the panel is open.

So the assistant is expressed as **states of the existing machine** — the panel is open, the
active input target is the assistant — and everything else (editor visibility, `disableStdin`,
focus, the focus ring, what a click on the live region does) derives from that one state.

Two rules fall out and both are load-bearing:

- **Program exit invalidates "hand the keys back to the program".** There may be no
  foreground program any more; keys would land in a shell prompt while the UI still labels
  the target as the TUI. Exit forces a visible transition.
- **Key ownership binds to a live session generation.** After a reconnect, the same-looking
  target is a different process.

### 3.4 The gesture extends xterm's existing escape hatch

xterm already owns "the user wants to select, not to click at the program": **Option on
macOS, Shift elsewhere**, forcing selection inside mouse-tracking programs. The assistant's
picker extends that path — it does not add a parallel pointer listener, which would be a
second claimant on one mouse event.

- **In the flow:** drag across part of a block's output → a chip in the editor.
- **On the alternate screen:** mouse-down mints the frame at that instant (you cannot circle
  something that is moving), drag on the frozen frame, release → the same chip.

The acceptance criteria require proof that **no SGR packet reaches the PTY** during the
gesture, and that ordinary unmodified mouse input still does.

---

## 4. The engine: eino runs the loop, the grant is ours

**Decided in [ADR-0028](../../docs/decisions/0028-eino-runs-the-loop-the-grant-is-ours.md),
after that ADR was reversed.** Its first version — and an earlier version of this section —
said we must write the loop, because a framework cannot make a refusal a control decision or
suspend before a domain call. That was asserted, not run, and it is false. The correction is
kept at the top of the ADR.

### 4.1 What the framework gives, and what cannot be given

**The agent is `adk.ChatModelAgent`, not `flow/agent/react`.** An earlier version of this
section named one and then specified the other's hooks, which are not interchangeable:
`flow/agent/react` builds a compose graph and wires `compose.ToolsNodeConfig.ToolCallMiddlewares`,
and never mentions `adk`; `BeforeAgent`, `BeforeModelRewriteState` and `WrapInvokableToolCall`
are `adk.TypedChatModelAgentMiddleware`. Someone implementing from the old text would have
picked `react` and found the advertised seam absent. We take `adk` because rewriting the run's
tool set is exactly what the grant needs.

We do not write a tool-calling loop, an SSE client, or a provider adapter set. Verified in
`eino v0.9.13`:

| We need                                  | eino has                                                                                                                                                                                                                                 |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| a place to intervene before a tool runs  | `adk.TypedChatModelAgentMiddleware.WrapInvokableToolCall` — "called at request time when the tool is about to be executed" — receiving `next` and the tool's name, call id and arguments                                                 |
| to withhold a tool entirely              | `BeforeAgent` rewrites the run's tool configuration (per run, and runtime tools are cloned before modification); `BeforeModelRewriteState` rewrites `ToolInfos` before each model call                                                   |
| to suspend for a human                   | `compose.StatefulInterrupt(ctx, info, state)`; `ToolsNode` recognises it explicitly (`IsInterruptRerunError`), preserves completed siblings in `ExecutedTools`, and resumes from a `CheckPointStore` without re-running what already ran |
| a terminal failure that is not a message | an ordinary (non-interrupt) error from a tool aborts the node — `failed to invoke tool[...]` — instead of becoming a tool result the model reads                                                                                         |
| calls in a known order                   | `compose.ToolsNodeConfig.ExecuteSequentially` — default `false` (parallel), which we set `true`                                                                                                                                          |
| streaming, tool-call assembly, providers | the framework's, including `eino-ext` adapters for `openai`, `claude`, `ollama`, `openrouter`                                                                                                                                            |

**What it cannot give is the policy and the capability, and that is all that stays ours.**
eino has no grant, no resource scope and no effect classification — and structurally cannot:
"is this destructive", "does this cross an environment", "is this lane in scope", "is this
path inside the grant" are asked in terms of _our_ environments, lanes, connections, vault
and ledger, and a general framework has no vocabulary for them. Likewise the **capability** a
tool holds: eino passes a tool a context; what is inside it is ours by definition. (Its
`MCPToolApprovalRequest`/`Response` blocks are for relaying an approval that a remote MCP
server asked for — a transport shape, not a policy.)

So we write two small things over our own types: a function answering **permit / ask /
refuse**, and a constructor for a **narrowed capability**.

**And the ledger stays ours — which requires a decision, not an assertion.** Calling eino's
checkpoint "implementation detail" does not make it one: if `agent.approve` cannot resume
without it, the checkpoint is operationally authoritative until the run terminalizes, and a
serialization change across an upgrade would strand every suspended run.

So, for v1: **a checkpoint is encrypted process-lifetime state.** It exists from the moment an
interrupt persists it until cleanup succeeds — on terminalization, or by the startup sweep if
that delete failed — and a run is not reported terminal until its checkpoint is gone or its
cleanup is durably scheduled. A terminal ledger record beside a live, reusable checkpoint id
is the state this rule exists to forbid. **Approval does not survive a restart** — which is
already what §4.2's recovery rule says: every non-terminal run becomes `interrupted` and the
user asks again. Durable approval across restarts would make the checkpoint an artifact with
its own version, retention and migration rules, and it is deliberately not v1.

Nothing in the product reads eino's state to answer "what happened"; that is the ledger's
question and ADR-0019 owns it.

**And `logrus` is contained, not tolerated.** It arrives compiled-in through
`compose → schema → gonja/exec → logrus` — the template engine, not the logic — which
collides with the rule that structured logging goes through one `log/slog`-backed interface.
The composition root redirects the standard logrus logger into that interface, so nothing
reaches stderr around it, and a test asserts it.

### 4.2 One agent, two run modes

```
assemble a projection from the ledger + referenced frames
  → the framework streams the model and assembles proposed calls
  → OUR middleware sees a call about to execute, with its arguments
  → evaluate it against the run's immutable grant
        refuse  ·  suspend for approval  ·  permit
  → on permit: RECORD THE ATTEMPT, then construct the narrowed capability, then call the tool
       (a write that fails means the tool is not called at all)
  → continue until the model finishes, the user cancels, a lease expires, or policy stops it
```

**One driver, two run modes** — not "one undifferentiated loop", which was overselling it.
The modes differ in three named places, and the differences are owned by the mode rather than
scattered through the driver:

|                            | **explain**                                                                                                                                                                                                      | **agent**                                                |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------- |
| tools declared             | none                                                                                                                                                                                                             | ours                                                     |
| termination                | after the first completed response                                                                                                                                                                               | when the model finishes, or policy/lease/cancel stops it |
| context assembly           | question + referenced frames                                                                                                                                                                                     | plus tool calls and their results                        |
| a tool call arrives anyway | rejected by the run adapter as malformed model output. NOT by the middleware: with zero tools ADK builds `buildNoToolsRunFunc`, a direct model chain with no tools node, so there is no middleware to receive it | ordinary path                                            |

**A refusal ends something — and "do not call `next`" is not by itself what ends it.** The
middleware must return _something_, and the terminal behaviour comes from **which category**
it returns: a `ToolOutput` with no error becomes a tool result the model reads and works
around, which is exactly the failure this rule exists to prevent. So the contract is named
and asserted end to end:

1. the middleware does not call `next`;
2. it returns **`ErrPolicyRefused`**, a nocx error, not a message;
3. eino's `ToolsNode` aborts the node on a non-interrupt error rather than producing a tool
   message;
4. the run adapter recognises the error and terminalizes the attempt as `refused`;
5. **no tool message and no second model request are produced.**

A test that only asserts "`next` was not called" would pass while the model reads a polite
refusal and reformulates.

**Escalation is `StatefulInterrupt` before `next`**, so the call that is asking has not run,
and approval resumes as a new attempt with a new grant rather than widening a running one.
Resume must bind the approval to the exact proposal — run, attempt, tool name, call id and a
hash of the canonical arguments — or approving one thing authorises a changed thing, and a
wrapper that re-evaluates the same call under the original grant interrupts forever.

**Calls run one at a time, and stopping the batch is OUR job, not the framework's.**
`ExecuteSequentially` is set, but read what it does: `sequentialRunToolCall` loops over every
task and calls `run` on each, skipping only ones already executed. It never inspects
`tasks[i].err` and never breaks — the node collects all results and only then returns the
first non-interrupt error. So "sequential" means _in order_, not _stop on failure_, and a
refusal on the second of three calls would not prevent the third.

So the policy middleware carries a **batch latch** in the run context: once it has refused or
interrupted one call in a model response, every later call in that same response returns
immediately without calling `next`. This is the owner's rule — refuse one, refuse the rest —
implemented where it actually holds rather than assumed from a config flag.

What this still does **not** promise is that nothing at all has happened: a permitted sibling
_earlier_ in the batch has already run when a later call asks for approval. That is correct —
everything that ran held the grant, and the user refused the call they refused, not
retroactively the one before it — but the surface must say so. **The honest invariant is "the
call that is asking has not run, and no call after it will", and what already ran is visible
in the attempts** — not "the domain is untouched".

**The strongest refusal is the one never proposed.** A tool the run's grant does not permit
is not declared to the model at all (`BeforeAgent`), so most of the policy never has to say
no — it just never offers.

**Recovery is two lines, not a machine.** A run is durable, and on start **every non-terminal
run becomes `interrupted`** — the block says so and the user asks again. Nothing is retried
automatically: on the explain path nothing executed, and on the agent path a retry could
repeat an effect. The one ordering rule that makes this honest: **an attempt is written down
before the tool is invoked, never after.** Otherwise a crash between invocation and result
loses the record entirely, and an interrupted run cannot say the one thing that matters —
"this may already have happened".

### 4.3 The tools are Go functions over our own domain

`read a block`, `run a command in the agent's lane`, `look at a connection`, and so on — each
one a Go function over the ledger, the session manager, the vault. This is the whole reason
the engine is in-process: the tool holds our objects directly, so what bounds it is what it
holds — not a message across a pipe from the thing it protects.

**The grant is over resources and effects, never over tool names.** This is the same mistake
as `--no-tools`, one level up, and it has to be named so it is not made a third time: a
grant that says "may call `run-command`" permits nothing in particular and everything in
general, because one command tool reaches files, the network, ssh, other processes and the
vault. So a grant names environments, lanes, paths, destinations and effect classes — the
lattice ADR-0020 §6 already defines — and the tool name is not an authority at all.

**And the policy middleware narrows, rather than checks.** A check before the call is
advisory: the tool still holds a full `session.Manager`, the vault and the filesystem, and may
use more than the grant allowed. Instead the middleware resolves the run's grant into a
**scoped capability** and the tool holds only that — so it cannot exceed the grant, because it
never has more than it. ("Dispatcher" is retired as a word here: there is no component of ours
dispatching anything; there is a middleware in eino's own path.)

**The grant does not move; what is on offer does.** `BeforeModelRewriteState` may shrink
`ToolInfos` as the resource state changes, and that is not a change of authority: the grant is
immutable once execution starts, and only a new attempt carries a different one. The word
"scope" must not be used for both.

**All four tool shapes are wrapped, or only one shape is allowed.** `adk` has
`WrapInvokableToolCall`, `WrapStreamableToolCall` and two `Enhanced` variants, and each is
called only for tools implementing the matching interface. Wrapping one and later adding a
streaming tool would route it around the policy entirely. **v1 permits ordinary invokable
tools only**, enforced at registration, and each further shape is opened by wrapping it and
testing it.

This replaces an acceptance criterion the first rewrite got wrong. "The registry is
unexported and the middleware is the only caller" is not a security boundary: Go package
privacy stops another package naming the symbol, not code inside `internal/agent` calling the
tool directly, and it rots at the first refactor. What is assertable is the narrowing: a tool
given a capability scoped to lane A **cannot** reach lane B, and the test proves it by trying.

**And the attempt is written before `next`, or `next` does not happen.** The ordering rule of
§4.2 needs a failure branch: if the ledger write fails, the middleware constructs no
capability, does not call `next`, and returns a terminal infrastructure error. A middleware
that logs the write failure and proceeds produces an effect with no durable record, which is
the one thing an interrupted run cannot recover from.

### 4.4 Providers

**We write no model client.** SSE framing, incremental tool-call assembly, finish reasons,
usage, cancellation and per-provider quirks are the framework's — that is most of what
adopting it buys, and it is the part a hand-written client gets wrong slowly.

**One adapter to start: the OpenAI-compatible one**, on a base URL the user sets. That single
protocol reaches:

- **local models** — Ollama, llama.cpp, LM Studio, vLLM — so a frame need never leave the
  machine, which is the strongest possible answer to §6's sensitivity problem, and is
  `vision.md:75`;
- **any hosted provider**, directly or through an aggregator, on a base URL the user sets.

**Each further adapter is code, not configuration.** An earlier line here said the opposite
and was wrong: every `eino-ext/components/model` adapter is a separate compiled dependency
with its own constructor, options, streaming behaviour, error taxonomy and tool-call quirks.
eino says so itself — `react`'s default `StreamToolCallChecker` documents that "some models
(like OpenAI) output tool calls directly; others (like Claude) output text first, then tool
calls", and the default check fails for the second kind. So each adapter arrives with a
compatibility suite: streaming, tool-call assembly, usage, cancellation, malformed events,
finish reasons.

The one worth adding early is **`claude`** — not for the provider but because Anthropic's
protocol halts at `tool_use` and waits for `tool_result`. That is not a security property (the
pause that matters is ours, in the middleware), but it is a cleaner fit than reaching Claude
through an OpenAI-compatible aggregator, which loses streamed tool arguments.

**Each adapter is weighed before it is added.** `eino/compose` plus `eino/adk` — the driver we
actually chose — with no provider adapter at all, is 78 modules in the graph and 124 packages
compiled —
including `logrus`, which is a second logging vocabulary in a repo whose rule is one
`log/slog`-backed interface. Adding a provider means `go list -deps` and the stripped binary
size before and after, not an argument about breadth.

### 4.5 Configuration: endpoints and models

Modelled on Warp's custom-endpoint dialog, with three deliberate differences.

An **endpoint** has a display name, a base URL, a credential, and one or more **models**,
each with an optional alias for the picker. Endpoints are a list; the user adds as many as
they like.

- **The API key is not stored here.** The field is an input; what the endpoint record holds
  is an **opaque secret reference** into the vault (ADR-0016, ADR-0017). Warp stores the key
  with the endpoint; we already decided otherwise, and a second answer is not allowed.
- **The wire schema is a field on the endpoint; the select is not.** An endpoint's schema
  cannot be inferred from its URL, so the record carries it — a closed enum in the contract.
  But a select with one option is UI for a feature that does not exist: the control appears
  when the second implementation does. (Warp offers three — _OpenAI Chat Completions_,
  _OpenAI Responses_, _Anthropic Messages_ — which is empirical confirmation of risk 6:
  "OpenAI-compatible" is not one protocol, and two of those three are OpenAI's own.)
- **`http://` is permitted only for loopback and private addresses.** A frame can carry a
  password prompt or a token from a pager; sending it in clear text to a remote host is not a
  warning, it is a validation failure. Remote endpoints are `https` only.

  **And that rule is enforced on every connection, not in the form.** A form-time check is
  decoration, for four reasons that are the whole of why the rule reads this way: a hostname
  can resolve public while it is validated and private when it is dialled; a redirect can walk
  from `https` public to `http` private; `localhost` is not the only spelling of loopback —
  IPv6 loopback, link-local, IPv4-mapped and cloud metadata addresses are all reachable by
  name; and proxy environment variables can reroute a request the user believes is local. So
  the address is re-checked at dial time, redirects are re-checked as if they were new
  endpoints, and the credential is never forwarded across an origin change.

  **Its enforcement lands with `nocx-edio`**, together with the **Test** button, because both
  need the HTTP client to the model and there is none in this tree yet. Until then the record
  accepts any absolute `http(s)` base URL with parse-level validation only, and nothing in
  that pass implies an address restriction that does not exist. The rule above is not weakened
  by the deferral — it is the acceptance criterion `nocx-edio` inherits, and the four reasons
  are kept here because a rule whose reasoning is lost gets simplified away by the next reader.

- **The Test button tests what will actually be used** — not one cheap completion, which
  proves only that something answered, but the capabilities this endpoint must have (streaming
  now; tool calling before the agent rung claims to work there). One readiness bit over a
  protocol that is not uniform is how "compatible" endpoints fail in the middle of the first
  real question. Also `nocx-edio`.

### 4.5.1 The record

One endpoint is exactly the four nouns above:

| field           | meaning                                                                                                                                                                  |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `id`            | `endpoint:custom:<slug>:<uuid>`, minted by the backend — an id is identity, and the display layer must not invent one (the rule `NewProfileID` exists for)               |
| `name`          | the display name (required)                                                                                                                                              |
| `baseUrl`       | an absolute http(s) URL (required; parse-level only this pass, see above)                                                                                                |
| `schema`        | the wire schema — `openai-compatible` today, a closed enum in the contract (decision 2)                                                                                  |
| `credentialRef` | the opaque vault reference, persisted (ADR-0030); empty when the key is gone. On the wire it becomes `credential` — a row handle or `null`, never the reference (§4.5.4) |
| `models`        | one or more `{name, alias?}` — the model id and the optional picker label                                                                                                |

### 4.5.2 Storage — a config record, not a setting

An endpoint is shaped like a profile: a list of records that own vault
secret references. It is stored in the same JSON document as profiles and
groups (`internal/profile`, the profile store family) — not in
`internal/settings`, which is scalar declarations, and not in a second
store. The placement is load-bearing, not convenience: the bulk sweeps a
vault reset performs must be one atomic write (`secretrefs.go`'s own
invariant — "an interruption leaves half the store pointing at a vault
that has gone"), and `ClearSecretRefs`, the metadata-first half of
deleting a secret (ADR-0011 §4), must clear every reference in one write.
Two stores would split both sweeps across two documents that could
disagree; extending the one document keeps them one write. The decision
and its reason are recorded in ADR-0031.

### 4.5.3 The secret lifecycle

The endpoint **owns** the secret it mints (ADR-0030). The form's key field
is an input; what the record holds is the reference.

- **Create with a key** — the backend mints the key into the vault
  (`vault.CreateNamed`, auto-named `<endpoint name> API key` per
  ADR-0016 — the name derives from what is already known, never from the
  material), then writes the record holding only the reference. Mint
  first: a crash between the two leaves an ownerless secret the vault's
  journal reconciles at next start, which is the preferred end of the
  ADR-0011 §4 trade; the reverse order would leave a record pointing at a
  secret that cannot exist.
- **Create without a key** — allowed; the record holds an empty
  reference, `agent.status` (later) reports the credential unresolvable,
  and a key is added by update.
- **Update with a new key** — the backend **rotates** the material behind
  the endpoint's own secret (`vault.ReplaceSecret`: same id, same name,
  new material). This is ADR-0017 §2's rotate, and it is why the endpoint
  never orphans a key on update: the reference never changes, so no
  other record that happens to share the secret can be left dangling, and
  the vault's journal makes the rotation crash-safe. An update on an
  endpoint whose reference is empty mints instead.
- **Delete** — one atomic write removes the record and clears its
  reference from every remaining record — a shared secret loses its other
  bindings exactly the way `ClearSecretRefs` does when the user deletes a
  shared secret from the Secrets page: the connection stops claiming the
  password is saved — then the material is deleted (`vault.Delete`,
  metadata-first).
  A provider failure leaves the journal's pending delete — the record is
  gone and the catalogue row was dropped with the journal write, so
  nothing points at the material while it awaits cleanup. `Reconcile`
  retries the pending delete at the next start; that retry succeeds for
  lock-independent providers (the OS keychain, the product's default),
  and for the file store it is blocked while the vault is sealed — the
  entry is retained and logged, and a later reset sweeps the residue
  (ADR-0030 §4).
- **The Secrets page deletes the key** — the extended `ClearSecretRefs`
  clears the endpoint's reference in the same atomic write that clears
  profile bindings; the endpoint survives, credential-less, and says so.

The key itself never crosses back: it is sent once in the create/update
params, minted by the backend, never persisted, never echoed in a result,
and never logged (`credential.Secret` redacts in every fmt/slog path).

### 4.5.4 The wire

The form pass binds to this; the wire is the deliverable. Four methods
under the `endpoints` domain, one JSON Schema each in `contracts/` with
`additionalProperties: false` and an explicit `required`:

| method             | params                                                       | result        |
| ------------------ | ------------------------------------------------------------ | ------------- |
| `endpoints.create` | `name`, `baseUrl`, `schema`, optional `key`, `models`        | `{endpoint}`  |
| `endpoints.list`   | —                                                            | `{endpoints}` |
| `endpoints.update` | `id` + `name`, `baseUrl`, `schema`, optional `key`, `models` | `{endpoint}`  |
| `endpoints.delete` | `id`                                                         | `{}`          |

- The credential — the wire field `credential`, which is the stored
  `credentialRef` mapped to a **row handle** (`secrow:...`), exactly like
  profile secret bindings (ADR-0017 §1): `null` when no key is set, the
  handle otherwise, never the reference. `update`'s `key` is optional and
  "absent or empty" means "keep the existing material" — a blank key cannot
  erase a saved key, because erasing a secret is the Secrets page's job, not
  a form field's.

### 4.5.5 Vault reset

Endpoints participate in the reset bookkeeping (ADR-0031): the preview
counts endpoint references beside profile ones and the clear removes
both, with the wire's `vault.resetPreview` gaining `endpointCount`. The
counts stay per kind — "12 secrets, 9 connections, 2 endpoints" — because
they answer different questions.

### 4.5.6 Named gaps (this pass)

- **The secrets inventory usage projection counts connections only.** An
  endpoint-owned key shows on the Secrets page with its name and kind and
  a `usedBy` of 0 — accurate about connections, silent about endpoints.
  A per-kind usage projection lands with the surface that renders it (the
  form pass), and this paragraph is the record that it was considered.
- **The backup format carries profiles and groups; endpoints ride the
  next format version.** No endpoint data exists yet, so nothing is lost
  today; a restore taken after endpoints exist would drop them until the
  format gains them. Named here so it is a gap, not a surprise.
- **The reset sweep does not clear group-default references.** A secret
  reference stored in a group default is invisible to the reset preview
  and survives the clear today (pre-existing, predates endpoints);
  `ClearSecretRefs` handles group defaults on the per-secret path. Fixing
  the reset side means redefining `ProfileCount` as the effective
  computation, which changes the wire meaning of `profileCount` — a
  reset-semantics decision of its own, deferred (ADR-0031).

This form will meet the open bead `nocx-74cn` ("the kit has no validation: forms accept
anything and say nothing"). It is built from the kit per `frontend/src/ui/README.md`, and any
missing variance is added **in the kit**.

---

## 5. What lands in the ledger

ADR-0019's schema v1 is the shape, and it is designed but unimplemented (`nocx-rtg0.2`).
Building the assistant on `command_history` — which its own comment calls interim — or on a
second table would create exactly the second writable truth that ADR is about.

Each ask leaves entries in the one ledger:

- **the frame** — an **artifact with capture provenance**: capture identity (buffer instance,
  geometry, generation), serializer version, truncation and gaps, host, cwd, captured-at.
  ADR-0019 §6 is explicit that derived text is an artifact and not a string, so the frame does
  **not** become the text of a message.
- **the question** — an entry, with `references` edges to the frame artifacts and regions.
- **the answer** — an entry of `kind = agent`, streamed in, joined to the question by a
  `caused-by` edge.
- **each tool call** — an attempt, with its grant, its policy decision, its result and its
  termination reason.

This is also what makes the owner's request work: **the dialogue references the command
block**, so one flow can interleave commands and assistant turns without a second timeline —
which is ADR-0019 decision 1 verbatim.

**Which forces a cutover, and it is in scope.** If a command block and an assistant turn are
to be one flow, then commands are written to `entries` too. Leaving commands in
`command_history` while the assistant writes schema v1 is two timelines wearing one UI. The
project is greenfield — there is nothing to migrate and no shim to write — so
`command_history` is **replaced**, not doubled, which is what its own "interim" comment
anticipates.

**The minimum subset.** "Schema v1" as a whole is more than this design needs, and naming the
whole thing is a way of hiding a large dependency behind a word. What it actually needs:

- `entries` — question, answer and command identities: kind/author, session and environment,
  `ingest_seq`, phase, timestamps.
- `artifacts` + `artifact_chunks` — frame and answer bodies as **masked** content, appended in
  bounded, ordered, idempotent chunks, carrying capture provenance and a partial/complete
  state.
- `edges` — at least `references` (with region coordinates) and `caused-by`. Causality is
  never read from adjacency.
- `runs` and `attempts` — run mode, endpoint and model as they were at the time, the immutable
  grant, state, attempt number, provider request id, start/end, termination reason, approval
  lineage. ADR-0020 requires executions, and this is not only about tool calls: **an explain
  ask is itself a model execution** and needs a run row of its own.
- one atomic create (question + frame artifact + `references` edge + pending run), an
  idempotent streaming append, a terminalizing update, and a startup sweep.
- byte accounting and eviction that never evicts content an active run still needs.

**Ordering is not atomicity.** One backend-owned ask transaction records the frame reference,
the question and a pending run **before** the model is called. Streaming content follows; the
identities and the recovery state exist first. Otherwise a frame lands with no question, a
question with no run, or a retry duplicates both.

**The frame is ingested first, by its own method.** The previous draft contradicted itself:
`agent.ask` carried a frame id while the transaction claimed to record the frame. Both cannot
be true. So the renderer ingests the frame — `agent.captureFrame`, returning a
**backend-minted** id — and `agent.ask` references it. A frame that is never referenced is an
orphan and is swept; an ask naming a frame from another session is rejected.

**Retention.** Frames are the heaviest thing the ledger will hold. Byte accounting and
eviction for artifacts must exist before durable frames do — otherwise either frames grow
unbounded or command history is evicted by a budget that never counted the bytes that
actually filled the disk. ADR-0019 §7 already requires reconstruction to state its own
horizon, so an evicted frame shows a gap rather than a plausible lie.

---

## 6. What leaves the machine, and what comes back

### 6.1 Masking has an owner already; it must be extended, not copied

Today `history.record` is the single writer of durable rows and masks the **submitted
command** (`maskCommandSafe`). It does not look at output, cells or frames.

So the detector is extended into the **artifact-ingest path**, and that path becomes the
single owner of durable-and-egress masking. Renderer-supplied redaction ranges are **hints
only** — the renderer is not the authority, or we have two detectors that disagree.

What leaves the machine is the referenced regions with masked spans removed; what is stored
is masked by the same owner in the same place.

### 6.2 Screen content is untrusted input

A frame is not a document we wrote. A program can print whatever it likes, including text
addressed to the model: _"ignore previous instructions and …"_. At EXPLAIN, with zero tools,
the blast radius is a wrong answer. **At the agent rung it means the screen can propose tool
calls**, and at DRIVE it means the screen can propose keystrokes.

Three rules follow:

- Frame content is delivered to the model as **quoted, labelled data**, never as instruction,
  and the system prompt says so.
- **A proposed tool call is authorised by policy on its own merits**, never because the
  screen or the conversation asked for it convincingly. The policy does not read intent.
- **Provenance travels with the proposal**: a call proposed in a turn whose context included
  untrusted screen content is, by that fact, lower-confidence — and ADR-0020 rule 6 says low
  confidence escalates on its own.

**And the taint propagates, or it is worthless.** Attaching it to the first turn only loses it
exactly where the agent rung begins to iterate: a tool's output is untrusted too. A command
prints instructions, a file contains them, a remote endpoint returns them. So the taint is
carried on the **context**, not on the question: once anything untrusted has entered a run's
context, every later proposal in that run inherits it, through tool results, retries and any
summarisation or compaction of the context. A summary of tainted content is tainted content.

---

## 7. The wire

New methods, `<domain>.<method>`, one JSON Schema each in `contracts/`, with
`additionalProperties: false` and an explicit `required`.

| Method                             | Shape                                                                                                                          |
| ---------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `agent.captureFrame`               | the frame's cells, attributes, cursor, capture identity and source (`live` \| `frozen`). Returns a **backend-minted frame id** |
| `agent.ask`                        | question text + references (frame id + region). Returns a **backend-minted run id**.                                           |
| `agent.cancel`                     | takes a **run id** — "the current answer" is not an identity, and cancel from one thread must not abort another's              |
| `agent.approve`                    | a run suspended for approval is resumed by minting a new attempt with the approved grant — never by mutating the running one   |
| `agent.status`                     | endpoint configured, credential resolvable, last probe result                                                                  |
| `agent.entryUpdate` (notification) | per-entry streaming deltas — `nocx-dw3.1`                                                                                      |

Two things this domain needs that the directory does not yet do generally: **parameter**
validation and ingress limits. Frame content, region bounds, capture identity and run
identity all arrive as _params_, and `contracts/README.md` says schemas today cover results.
An oversized frame, an out-of-bounds rectangle, or a frame id from another session are all
reachable from the renderer. This domain validates params and bounds sizes; ids are minted by
the backend and ownership is checked.

**A run's state is on the wire, because the renderer has to draw it.** `prepared`,
`streaming`, `awaiting_approval`, and the terminal set `completed | cancelled | failed |
interrupted`. `interrupted` is what a run becomes when the backend restarts and finds it
non-terminal (§4.2), and the block says so rather than staying open forever. A renderer that
reconnects mid-answer reads the run's state and its appended chunks; it does not infer
liveness from the fact that notifications stopped arriving.

---

## 8. The first slice, and what is deliberately out

**In:** the panel and its geometry; the picker extending xterm's forced-selection path; frame
minting with capture identity; references and chips; the ask transaction and the ledger
entries; **eino wired in** with the OpenAI-compatible adapter and **zero tools declared**,
streaming out over the control plane; the endpoint/model configuration with the vault
reference; masking extended into artifact ingest; the failure surfaces.

**Deliberately out:**

- **GUIDE and DRIVE.** No keycaps, no keystroke delivery. Capture identity exists from day one
  because retrofitting it is what makes it wrong; the _delivery refusal_ arrives with DRIVE.
- **Tools.** The middleware, the grant and the narrowing are designed here and exercised by
  the zero-tool case — which is not a vacuous exercise: "a tool the grant forbids is never
  declared to the model" is exactly what the explain mode asserts. The first real tool is the
  next slice, and it is the moment the lane and the lease become load-bearing.
- **Mouse control of a TUI**, and any per-frame loop. Frames after an action arrive **on
  quiescence**, using the inactivity signal the lease already computes.
- **A provider catalogue.** One protocol, endpoints the user adds.

**Prerequisite, not scope:** ADR-0019 schema v1 (`nocx-rtg0.2`) with artifacts and byte
accounting. This design does not start on `command_history`.

---

## Acceptance criteria

Assertions, in the bead, authored before the implementation.

**The end-to-end check:**

1. A full-screen program owns the viewport in a real backend session.
2. The user presses the summon hotkey. **Assert:** the PTY's `rows`/`cols` are unchanged.
3. **Assert:** the editor is present, `InputTargetRegistry.active()` is the assistant target,
   and a frame entry is in the flow whose text contains a string visible on the screen.
4. The user drags a region and types a question. **Assert:** a chip names the region; the
   payload sent to the model contains the selected rows and **not** rows outside them.
5. **Assert:** an answer streams in and closes.
6. **Assert:** no keystroke and **no SGR mouse packet** reached the program at any point —
   with the fixture program having enabled SGR 1006, and with ordinary unmodified mouse input
   still reaching it.

**Also asserted, each its own check:**

- A repaint with an **identical buffer does not** advance the generation; an **offscreen
  mutation does**; a buffer switch or a resize makes the frame **not comparable** rather than
  stale, and the UI says the different sentence.
- A write that repaints **identical cells still advances** the generation, and the UI reports
  "moved" — the deliberate false positive of §2.3, asserted so it cannot be silently
  "optimised" into a false negative later.
- A frame is taken while a multi-chunk write is still queued: **assert** it is taken after the
  parse settles, and that no frame mixes rows from before and after one write.
- The same gesture on a frozen block mints a frame whose provenance records source `frozen`
  and the serializer version, and on a live surface records `live` and the buffer identity —
  **two sources, both recorded**, and neither silently substituted for the other.
- The program exits while the panel is open: **assert** the UI stops offering to hand keys to
  it, and no byte is delivered to the dead session.
- Two asks in flight: cancelling one leaves the other streaming, and deltas land on the right
  entry. Cancel is by run id.
- **The backend restarts mid-answer.** On start, the run is `interrupted`, the block says so,
  nothing is retried, and a reconnecting renderer reads that state rather than waiting
  forever. And with an attempt recorded before its tool was invoked, the interrupted run
  reports that the tool **may** have run.
- The tab is closed while an answer streams: the run terminalizes, and no notification is
  delivered to a disposed surface.
- **A tool the grant forbids is never declared to the model.** Assert on the request the
  adapter actually sends: its tool list contains only what the grant permits. This is the
  explain mode's whole assertion — the list is empty — and it is the strongest refusal there
  is.
- **The refusal contract, asserted as five things and not one.** A scripted model proposes a
  call; the middleware does not call `next`; it returns `ErrPolicyRefused`; the attempt is
  terminalized as `refused`; **no tool message is produced and no second model request is
  made**. Asserting only "`next` was not called" would pass while the model reads a polite
  refusal and reformulates. The adapter matches with `errors.Is` — eino wraps the error, so an
  equality check would silently miss it, and the test uses a wrapped error to prove it.
  **And the paired positive:** with a reachable endpoint, an ask succeeds end to end.
- **In explain mode a hallucinated tool call is rejected by the run adapter**, not by a
  middleware — there is none on that path — and the run ends rather than the response being
  rendered as an answer.
- **The security spine is exercised with a real tool, even though production explain declares
  none.** The suite registers a fake in-memory effect tool and drives argument policy,
  narrowing, refusal mapping, interrupt, approval resume and the sequential-batch rule through
  it. A zero-tool run cannot exercise any of them, and a criterion that cannot fail is not one.
- **A refused call ends the batch — because our latch ends it.** With three calls and a
  refusal on the second, the third **never reaches its tool**, asserted against real eino
  rather than against `ExecuteSequentially`, whose sequential runner does not break on error.
  The first, which was permitted and ran, is reported as having run rather than hidden. The
  same test with an escalation in place of the refusal.
- **Approval binds to the proposal it approved.** Resuming with different arguments, a
  different call id, or a different tool is refused, not executed.
- **A ledger write failure before `next` is terminal.** With the store made to fail, no
  capability is constructed, `next` is not called, and the run ends with an infrastructure
  reason.
- **A streaming or enhanced tool cannot be registered in v1** — registration refuses it, so a
  tool shape cannot route around the one wrapper that is installed. **Including a tool that
  implements several shapes at once**: asking "does it implement the invokable interface" is
  not enough, because a dual-shape tool answers yes and is then dispatched through the other.
- **The contained logger is asserted by receipt, not by silence.** A logrus write arrives at
  the injected `slog`-backed sink with its level and fields intact, **and** nothing is written
  to stderr. Silence alone would also be produced by discarding the records.
- **Capability narrowing, asserted by trying:** a tool invoked under a grant scoped to lane A
  cannot reach lane B, cannot open a path outside its scope, and cannot read a secret the
  grant did not name — because what it holds is scoped, not because a check preceded it.
- A refusal **ends the run**: a scripted model that, after a refusal, proposes the same effect
  through a different tool never reaches a second dispatch.
- **Escalation touches nothing before it asks:** a call classified as needing approval
  suspends via `StatefulInterrupt` with `next` uncalled, and the domain is provably
  untouched; approval resumes as a **new attempt** carrying the approved grant, and the
  original grant is unchanged in the record.
- Raw output containing each supported secret kind, with **no renderer hints**, is detected by
  the backend owner: **assert** both the payload sent to the model and the stored artifact
  exclude the plaintext.
- With no endpoint configured, `agent.status` says so and the ask surface reports it —
  asserted on the surface, not on a log line.
- A remote `http://` endpoint fails validation; `http://127.0.0.1:11434/v1` passes.

---

## Risks and open questions

1. **Readability at reduced font** — the floor is set by looking at `htop` and `k9s`, not by
   picking a number here.
2. **Which modifier.** Extending xterm's path means Option on macOS and Shift elsewhere; that
   Shift is free in the TUIs we care about is a check, not an assumption.
3. **Artifact byte accounting** must exist before durable frames (§5). Which unit is counted —
   plaintext bytes, encrypted chunk bytes, or database pages — is undecided and must be, or
   the budget will mean a different thing to the writer and to the sweeper.
4. **The ledger subset is the schedule risk.** §5 names a defensible subset rather than all of
   schema v1, and it still includes the command cutover. Panel work is behind it. If it slips,
   this slips — and the honest failure mode is doing the panel first against a store that
   cannot hold what it shows.
5. **The security surface we depend on is wider than two APIs**, and saying "the middleware
   and the interrupt" understated it. It is: the choice of agent driver (`adk`), the four tool
   wrapper shapes, `ToolsNode`'s error classification (non-interrupt aborts, interrupt reruns),
   sequential-versus-parallel execution, interrupt state bookkeeping, and checkpoint
   serialization. A change to any of them is a break; the tests that assert never-declared,
   refusal-terminalizes and narrowing are what turn a break into a red build.
6. **The dependency graph is real.** 78 modules and 126 packages before any provider adapter.
   `logrus` is decided rather than deferred (§4.1: contained at the composition root), but the
   graph should be measured again after the first adapter.
7. **"OpenAI-compatible" is not one protocol.** Endpoints differ on streamed tool arguments,
   parallel calls and usage accounting. The first adapter targets chat + streaming text; tool
   calling is verified per endpoint before the agent rung claims support there.

## Follow-on rungs

**GUIDE** — key sequences rendered as keycaps attached to the frame they were computed from.
**DRIVE** — the keycaps gain "press it for me": delivery goes to the live program and the
result is watched without closing anything. **What refuses it is ADR-0029, not identity
equality** — an identity change refuses outright, a moved generation triggers a scoped diff,
and only a diff that reached what the key was about costs a model call, which authors a
condition the lane then evaluates locally at delivery. The earlier line here said "refused
when the capture identity no longer matches", which would have refused `top` forever. Each delivered key leaves a frame, so the thread becomes the readable form of
ADR-0020's attempts table.

---

## Stress Test Results

Adversarial review on 2026-08-13, with codex as an independent second reader, against the
codebase rather than against the prose.

### Defects found and fixed

- **"The grant is the process" was false.** The first draft rested authority on
  `pi --mode rpc --no-tools`. Verified by running it: a **client-sent** `{"type":"bash"}`
  executes anyway — `--no-tools` bounds what the _model_ may call, not what the _client_ may
  request. Superseded entirely — first by a loop of our own, and then, when that turned out to
  rest on a false claim about eino, by the framework's loop with our policy middleware in it
  (§4, and the third round below).
- **"A frame is ordinary output, same store, same masking" was fabricated.** Output is not
  stored at all (`content.go:85`, `ws_history_record.go:130` — "a capture path that does not
  exist yet"), and masking covers the submitted command, not output. Replaced by §5 and §6.1.
- **"A frame is just the message text" (the second attempt) contradicted ADR-0019 §6**, which
  says derived text is an artifact with provenance. Replaced by the artifact model.
- **"Revision bumped on repaint" was wrong in our renderer** — ADR-0005 forces periodic
  repaints on Linux WebKitGTK, and one integer cannot span a buffer switch or a resize.
  Replaced by capture identity plus mutation-driven generation (§2.3).
- **Three concepts already had owners** and were being duplicated: input routing
  (`InputTargetRegistry`, `takeKeyboardToGrid`), agent input (`AgentInputTarget`, ADR-0004
  §3), and the selection modifier (xterm's forced-selection escape hatch). All three now
  extend rather than parallel (§3.3, §3.4).
- **Untrusted screen content was absent** from the design entirely (§6.2).
- **`agent.cancel` had no identity**, ingress params had no validation, and the ask had no
  atomicity contract. Fixed in §5 and §7.

### Second round, on the rewrite

The same reader re-read the rewritten document against the code. Seven more, and the first is
the same mistake made twice:

- **The grant was still name-shaped.** Moving the boundary from a CLI flag to "one dispatcher
  checks the call" kept the error: authority lives in the _arguments_, and a single
  `run a command` tool is a universal escape hatch. Now the grant is over resources and
  effects, and the dispatcher **narrows** rather than checks — the tool holds a scoped
  capability, so it cannot exceed the grant (§4.3).
- **"The registry is unexported" was offered as a security boundary.** It is not: Go privacy
  does not stop code inside the same package, and the test rots at the first refactor.
  Replaced by an assertion that narrowing actually holds.
- **A refusal did not end anything**, so the model could ask for the same effect another way.
  Refusal now terminalizes (§4.2).
- **The taint was attached to the first turn**, and so vanished exactly when the agent rung
  begins to iterate — a tool's output is untrusted too (§6.2).
- **The spec contradicted itself on the frame:** `agent.ask` carried a frame id while the
  transaction claimed to record the frame. `agent.captureFrame` now ingests it first (§5, §7).
- **"Buffer mutation" is not observable** in xterm, and `write()` is input rather than change.
  Replaced by `onWriteParsed` plus the explicit state-changing operations, with the
  conservative bias stated (§2.3) — and the same event is the capture fence.
- **"One loop, two rungs" was overselling.** Termination and context assembly genuinely
  differ; they are now named run modes (§4.2). And **the two capture sources are not one
  path** — a frozen block has no xterm cells left and its text is already transformed (§2.2).

Two of these — the grant shape and the refusal — are the same class of error as the original
`--no-tools` defect: a boundary that reads as enforcement and is advice. That it recurred
twice in one document is the argument for writing the narrowing test before the tool.

### Third round: the engine decision was wrong, and the owner found it

The rewrite argued at length that the loop had to be ours, because a framework "cannot make a
refusal a control decision or suspend before a domain call". **That was never run, and it is
false.** The owner asked why not, and `go get github.com/cloudwego/eino` answered in five
minutes: `InvokableToolMiddleware` is `func(next) next` and may simply not call `next`;
`ToolInput` carries the arguments; `StatefulInterrupt` suspends against a `CheckPointStore`;
and `adk`'s agent middleware rewrites the tool set per run, so a forbidden tool need never be
declared.

How it happened is worth keeping: two readers repeated the claim to each other — one from a
summary of a web page, the other with no network access at all, and _saying so_, which we
read past. Neither had run it.

The same document contains the opposite case: the pi defect was found **by running pi**. So
the rule this design ends with is: **a dependency that a decision turns on gets `go get`, not
a summary.** §4 and ADR-0028 now describe eino running the loop, with the policy and the
capability — the two things a general framework cannot have — as ours.

### Engine decision, and how it moved

`pi --mode rpc` → pi's Node SDK in a sidecar → `adk-go` → our own loop → **`cloudwego/eino`
runs the loop, the grant is ours**.

Three of the four steps were forced by something verified. pi's client-callable `bash` runs
under `--no-tools` (run locally), and pi deliberately ships no MCP (`docs/usage.md:303`), so
our tools would have lived in TypeScript inside someone else's process. `adk-go` has neither
Anthropic nor Ollama. The owner's requirement that the assistant call **our** tools ruled out
every out-of-process engine, because a tool call would then cross an invented boundary with
the grant check on the far side of it.

The fourth step was a detour on a false claim, corrected above: eino's seams do exactly what
we needed. `ADR-0028` records both the destination and the reversal.

### Confidence

- **The surface (§2, §3): high.** It survived the review; what changed was the mechanism
  underneath, not the interaction.
- **The engine (§4): high on shape, and now measured.** The seams are read from the module,
  not from documentation, and the weight is a number: 78 modules, 126 packages, `logrus`
  included. What is ours shrank to a policy function and a capability constructor.
- **The storage (§5): medium, and gated.** It is correct by ADR-0019 and it depends on work
  that is designed and unbuilt. That dependency is the schedule risk in this design.
