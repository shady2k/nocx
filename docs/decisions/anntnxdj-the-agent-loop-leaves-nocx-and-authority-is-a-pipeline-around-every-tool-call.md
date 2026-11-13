# ADR-anntnxdj — The agent loop leaves nocx, and authority is a pipeline around every tool call

- **Status:** accepted (2026-11-06)
- **Date:** 2026-11-06
- **Taken from** `nocx-a62cl`, the owner's decisions of 2026-11-05, 2026-11-06 and 2026-11-13
- **Supersedes ADR-0028 and ADR-0030.** Parts of other records move, mapped under "What happens to
  other records" below. ADR-0020's grant and ADR-0019's ledger stand.

## Context and problem

nocx carries an agent engine: a loop on eino, an OpenAI-compatible provider path, endpoints and
roles, a skill library with an audit and drafts. The owner's decision: nocx does not develop an
engine. The next steps would each be a product of their own (MCP, then providers and models one by
one), and the agents people already run do that work every day. What no agent has is what nocx is
for: the terminal's context, and tools over its panes with authority over those tools.

So the assistant stays and its engine leaves. A person still asks from a pane, in the same block.
An agent installed on their computer does the thinking. nocx ships no agent.

**The agent always runs where `nocx-server` runs, and acts on the pane's host only through nocx's
tools** (owner, 2026-11-13). A person connected to a server who asks for help configuring it gets
an agent that reads, edits and runs on that server, while its process, its model key and its
installation stay on their own computer. Nothing is installed on a remote host for the assistant.
An agent's own built-in tools act on the machine its process runs on, which for an SSH pane is the
wrong one, so they are switched off wherever the agent allows it; where it does not, nocx's system
prompt directs the agent to nocx's tools, and the product says that agent's own tools are outside
nocx's authority (owner, 2026-11-13).

**The agents come first by name, and each is an implementation of one abstraction** (owner,
2026-11-13): Claude Code, Codex, pi, omp and prime-agent. Everything nocx does with an agent goes
through one contract; an agent is added by implementing it, never by a branch in the core. pi is
built first, because everything the contract needs of it is measured below. No throwaway prototype
precedes the others: each connector is built for real and is supported once it passes the
conformance suite.

Read at `bd16c0268`, the following decide the shape:

- `agent.ask`, with its three entries (composer, summon over a program, marked rows), is the only
  function that must go on reaching a model once the removal below is done.
- The whole authority contract exists only on the in-process path. `kernel.go` holds floors, command
  classification, row scope, the policy matrix, approval, file-version and expansion evidence and
  the egress screen, and knows no framework. The tool endpoint has admission, effect refusal,
  resource resolution, the narrowed capability and attempts, and none of the rest:
  `endpointEffectGate` has no ask, so an `ask` row would pass and run, though no production grant
  holds one today. A person is asked once per executable at enrolment, never per call.
- The endpoint offers 10 of the 33 declared tools, admits only a process inside a watched pane's
  tree or a remote helper, sends no progress, and a cancelled call goes on running (`nocx-98qho`).
- Read at `04b45bbf4`: the `session.*` tools already act on the pane's host, because a command is
  typed into the pane and its screen is read through the helper. `files.read`, `files.edit` and
  `files.create` do not: their capabilities are built on `local.New()`
  (`internal/agenttools/narrow.go`), so on an SSH pane they read and write the local disk. No tool
  searches file contents or lists a directory. v0-9's charter moves Files to the helper only, with
  SFTP removed, and ADR-akhhb098 gives each host one supervisor in the local helper.
- An approval suspends a run by returning an error and resuming through eino's checkpoint. Nothing
  can block a call on a person.
- `dispatch(invocation, transform, gate, …)` already takes steps as parameters. That is the seam
  this record grows, rather than building a second one.

Measured on pi 1.4.0 with a throwaway prototype on 2026-11-05 and 2026-11-06, against a real model
and against a fake one whose requests could be read:

- With `--no-builtin-tools --tools 'mcp__nocx__*'` the request declares only the MCP tools. Without
  the flags it also declares `read`, `bash`, `edit`, `write`. A forced `bash` call answers
  `Tool bash not found` and creates nothing; the same call without the flags does.
- An extension passed by `-e` connects an MCP server through `pi.registerMcpServer` and leaves
  `~/.pi` unchanged. `--no-extensions` removes a provider that comes from a package, so a launch
  must not use it.
- An MCP request is cut at 60 s by default. 90 s passed with progress every 20 s, 300 s passed with
  `timeout: 600`.
- `abort` settles at once with `aborted: true` and reaches the MCP server as
  `notifications/cancelled`. The partial answer is absent from the next request.
- `steer` answers `queued` and arrives before the next model call, so it waits for the running tool
  and for the text being streamed. A second `prompt` during a run is refused unless it names
  `streamingBehavior`.
- A provider failure arrives as `errorMessage` on `message_end` while `agent_settled` says
  `aborted: false`.
- A new process per turn with `--session-dir` and `--session-id` carries the whole conversation into
  the next request, tool results included, and reached its first event in 0.4 to 0.6 s with the
  MCP server connected. Another id starts clean.

Not measured: the real endpoint, a person's own extensions and MCP servers against the `--tools`
filter, whether `abort` closes the stream to the provider, and a long wait against a real model.

Read from each agent's own `--help` on 2026-11-13 (claude 2.1.321, codex 0.167.1, pi 1.5.2, omp
18.18.5, prime-agent 0.10.2), not measured:

- Claude Code has a documented headless mode, `-p --input-format stream-json --output-format
stream-json`, with `--tools`, `--mcp-config` plus `--strict-mcp-config`, `--model`, `--session-id`,
  `--resume` and `--append-system-prompt`.
- Codex has `codex app-server` (JSON-RPC), `-m`, `-c` overrides for MCP servers, and a `shell_tool`
  feature flag. Whether every built-in tool can be removed is not known.
- omp is a fork of pi: `--mode rpc`, `--tools`, `-e`, `--model`, `--session-dir`, and `omp acp`.
- prime-agent has `--mode rpc|acp`, `--model`, `--session-dir` and `--append-system-prompt`, no tool
  allowlist and a built-in Python REPL.

Read from two products that drive these agents, on 2026-11-13: agent-of-empires reaches Claude,
Codex and pi through ACP adapters it pins and installs (`claude-agent-acp`, `codex-acp`, `pi-acp`), and
its changelog raises the Claude adapter's version floor seven times, from 0.37 to 0.103. paseo reaches
Claude through its SDK, Codex through `app-server` and pi through `--mode rpc` with a generated
extension carrying its MCP server and a bearer, keeps ACP for the long tail, and excludes `pi-acp`
deliberately.

## Decision drivers

The owner confirmed this frame on 2026-11-06. For a developer asking from a pane, and for us, who no
longer maintain an engine. **Authority over panes outranks features: when something must give, tools
are cut, not checks.** Main path: ask, streamed answer, tool call, a person's approval, result in the
same block, the conversation goes on, and the person can steer or cancel at any point. Signal: the
same question works through pi with no provider settings in nocx, and no new bug needs knowledge of
providers.

Quality scenarios, each with its measure:

1. A model asks to run a destructive command in a pane → the call waits for a person, and nothing
   executes unanswered. Measure: zero side effects before the answer.
2. A person takes ten minutes to answer → the call does not break, and the turn goes on. Measure: a
   ten-minute wait completes.
3. A person cancels a turn or a call → it stops. Measure: the agent and the model stream stop within
   one second, with no event after. A command already running in a pane stops by the lease's own
   walk (`INT`, `TERM`, `KILL`, a one-second grace after each of the first two), so it can take over
   two seconds, and where signalling is unsupported the block says it is still running. Skipping
   the grace would hurt a person's program to satisfy a number.
4. A person sends a message while a call awaits approval → the agent receives it and does not hang on
   the approval. Measure: the pending call settles at once.
5. A tool result holds vault material → it is caught before it reaches the agent. Measure: the bytes
   are absent from everything the agent received.
6. The agent crashes or its key is dead → the person sees the cause and is led to the agent section.
   Measure: never a silent end, never a green end.
7. The connection that started a turn goes (the window closes, the backend restarts) → the run ends as
   `interrupted`, as it does today. Measure: no agent process or MCP bridge outlives the turn, no
   approval survives, and a later question continues the conversation with no effect replayed.
8. A person with no agent invokes any AI function → the agent section of Settings opens.
   A person whose chosen model has left the agent's catalog is told so, and no neighbouring model is
   substituted.
9. A person in an SSH pane asks for help configuring that server → the agent reads, searches and
   edits the server's files and runs commands in that pane, and each call is decided by the person's
   permission matrix exactly as on a local pane: a permitted effect runs, an `ask` row waits for
   them, a refused one runs nothing. Measure: the file changed on the remote host, the local disk
   untouched, the matrix's decision recorded on each attempt, and nothing installed on the host for
   the assistant.

Constraints and their sources:

- No agent is shipped or embedded (owner, 2026-11-05).
- Claude Code, Codex, pi, omp and prime-agent are the agents to support, each behind one abstraction
  (owner, 2026-11-13).
- The permission matrix, the standing answers, the person's own instructions and the role table
  stay (owner, 2026-11-13).
- One surface: no parallel path behind a setting, and the old engine does not live beside the new
  one (owner, 2026-11-06).
- Authority plugs into the chain; modules stay isolated behind extension points (owner, 2026-11-06;
  AD-8).
- Every operation is declared in the catalogue with its lifetime (ADR-8eej3e66).
- The ledger is the record of truth (ADR-0019). An agent's session is not.
- No migration of old data (greenfield rule, `AGENTS.md`).

Kept principles of the area: AD-1, AD-6, AD-7, AD-8; a grant per unit of work (ADR-0020, ADR-0058); a
tool declares the classes it can reach (ADR-0053); a declared call is one record from proposal to
outcome (ADR-0039, ADR-0040).

## Considered options

**Where authority lives.**

**A. A pipeline with named extension points around every call at the tool endpoint** (Interceptor
and Chain of Responsibility, as a Policy Enforcement Point). Chosen. Works for any agent, because the
agent only ever sees the MCP boundary.

**B. A gate inside the agent's process**, an extension answering pi's `tool_call`. Least code now.
Rejected: the enforcement point sits in a process we do not own, and is written again per agent. This
is the case in which ADR-0028's objection holds.

**C. A second MCP server for the assistant beside the endpoint.** Leaves agents in panes untouched.
Rejected: two paths to one tool, the second surface the owner refuses.

**D. One generic `Around` step for everything.** Simple to state. Rejected after the owner pointed at
deepseek-harness's pipeline: any step could skip the next one or replace a result, so what a step may
do would be a matter of review rather than of its type.

**How an agent is reached.**

**E. One contract, and a connector per agent family speaking that family's own documented
interface: pi's RPC (pi, omp), Claude Code's stream-json, Codex's app-server, and ACP for prime-agent
and every later agent.** Chosen. Each is the vendor's interface for programs rather than a third
party's translation of it, needs nothing installed beyond the agent, and gives its flags directly.

**F. ACP for every agent, through adapters for those without it** (`claude-agent-acp`, `codex-acp`,
`pi-acp`). One protocol. Rejected: each adapter is one more thing to install, its launch flags are
out of our hands, and its version moves under us, as agent-of-empires' seven version floors for one
adapter show; paseo, which has both, keeps ACP for the long tail only. `pi-acp` in particular has no
`session/request_permission` and its author calls it an MVP.

**Options that reject the shared assumption.**

**G. Keep eino and finish it** (MCP client, more providers). Rejected by the owner: it is the work this
decision exists to stop.

**H. Drop the embedded assistant; a person runs an agent in a pane.** That path already works and
needs no contract. Rejected as the goal, because the assistant stays: a question from a pane with
its context, answered in the same block. It remains the fallback if the connector cannot deliver.

## Decision

### 1. nocx-server owns a harness contract, and a connector conforms to it

`nocx-server` is the process that holds it. The contract is a Go interface in a new `internal/harness`,
with connectors beside it and the choice wired in `internal/app`. The core and the renderer know only
the contract.

```go
type Harness interface {
    Readiness(ctx context.Context) Readiness // not installed | not configured | ready, and what to tell a person
    Run(ctx context.Context, t TurnSpec) (Turn, error) // one turn: conversation id, text, model, nocx's system prompt, the turn's MCP admission
}

type Turn interface {
    Events() <-chan Event // ordered: answer, reasoning, tool calls, end, failure
    Cancel(ctx context.Context) error
    Wait(ctx context.Context) (Outcome, error) // the process and its bridge are gone when this returns
}

// Capabilities: optional, implemented by a connector or not, and asked of it — never assumed, and
// never inferred from the agent's name.
type Steerer interface   { Steer(ctx context.Context, text string) error }
type Cataloguer interface { Models(ctx context.Context) ([]Model, error) } // what this agent can run, for the role picker
```

**Every agent is an implementation of this contract, and the core knows nothing else.** The
pipeline, the transport, the renderer and Settings see `Harness`, `Turn` and the capabilities; no
code outside a connector names an agent. What differs between agents is a capability a connector
has or lacks, or a fact it states (below), never a condition in the core. What every connector needs
is written once beside the contract and used by each: starting the process with its pipes, the
turn's lifetime, the MCP bridge with its bearer, and nocx's system prompt. A connector adds only
what its agent's interface requires, and a family shares one: omp is pi's connector with omp's
launch, not a second copy of it.

The connectors, in the order they are built:

| Connector   | Agents             | Interface                                           |
| ----------- | ------------------ | --------------------------------------------------- |
| pi family   | pi, then omp       | `--mode rpc`, MCP through a `-e` extension          |
| Claude Code | Claude Code        | `-p` with stream-json in and out, `--mcp-config`    |
| Codex       | Codex              | `codex app-server`                                  |
| ACP         | prime-agent, later | ACP over stdio, MCP through `session/new`'s servers |

prime-agent goes through the pi family instead if its `--mode rpc` is pi's protocol; its connector stage
reads that first.

**Each connector states facts the product shows**, rather than leaving them to be discovered:

- its **tool boundary**: _nocx's tools only_, where the agent's own tools are switched off at launch
  (pi, omp, and Claude Code through `--tools`); or _nocx's tools by instruction_, where they cannot
  be, and nocx's system prompt directs the agent to nocx's tools for anything on a pane or its host.
  A person is told which, and that in the second case the agent's own tools are not under nocx's
  matrix (owner, 2026-11-13);
- whether it **continues a conversation** by id; one that cannot answers each question alone;
- what it needs **installed and signed in**, which `Readiness` reports.

**One agent process per turn, one conversation on disk.** The turn is the unit of everything: its process,
its MCP connection, its bearer, its grant, its lifetime. That is what removes a late call from a cancelled
turn reaching the next turn's grant, a persistent bridge's reconnects and caller slot, and idle cleanup: a
turn ending closes all of it. It costs 0.4 to 0.6 s at the start of each question on pi. An agent that cannot
reload a conversation (`loadSession` absent in ACP, as on prime-agent today) answers each question alone, and
its connector says so.

**The model comes from the agent, and the person chooses it per role** (owner, 2026-11-13). nocx
holds no provider, endpoint or key. A connector with `Cataloguer` lists the models its agent can run;
the person picks one for a role in Settings, and the turn carries it. A chosen model the catalogue no
longer lists is reported as such, never repaired into a neighbour, as `roles.list` already rules. A
role with no choice runs on the agent's own default, and the block says which model answered. A
connector without `Cataloguer` runs on the agent's default, and the picker says it cannot choose. Either
way the default is **resolved once, at a conversation's first turn, and pinned** for that conversation
(decision 8): the connector reports the model that actually answered, and later turns carry it
explicitly. A connector that cannot learn which model answered says it cannot promise a conversation
keeps its model, and the block says so.

The role contract is rewritten with the swap, since today it pairs every assignment and its default
with an endpoint (`roles.list`, `internal/profile/role.go`): an assignment becomes _(agent, model id)_;
there is no product-wide default pair, because an unassigned role means the agent's own default,
pinned as above; choices are kept per agent, so switching agents and back finds them again; and the
closed set of roles is unchanged.
Handing an agent a provider of nocx's own is not excluded: it would be one more source of models for
the connectors that can accept one, with no change to this shape.

**The person's own instructions stay** (`assistant.personalInstructions`), and reach the agent inside
nocx's system prompt, which also carries what `systemprompt.go` tells the model today about panes,
blocks and tools. A connector passes it by its agent's means (`--append-system-prompt`, the
extension, the session's prompt), appending to the agent's own prompt rather than replacing it.

The vocabulary is ACP's (session, prompt, update, cancel), so an ACP connector is nearly transparent.
Success is never read from one event: a connector reports a failure as a failure, which is why
`agent_settled` alone is not an answer on pi.

A connector is the thinnest thing that satisfies the contract. An agent the ACP connector reaches is a
data record in `internal/agentrecord` (how to launch it, what must be installed) rather than code, and
one a record cannot make honest carries "not supported".

The pi connector runs:

```
pi --mode rpc --session-dir <nocx data> --session-id <conversation> --no-builtin-tools --tools 'mcp__nocx__*' [--model <chosen>] -e <nocx extension>
```

The extension is one file embedded in the binary and written to nocx's data directory. It registers
the MCP server for that session and appends nocx's system prompt, and does nothing else; the prompt goes
through the extension rather than `--append-system-prompt`, which would displace a person's own
`APPEND_SYSTEM.md` (paseo's note). Nothing is written to `~/.pi`, and a person's pi is unchanged outside
this launch.

### 2. Authority is a pipeline with named points

`internal/assistant`'s `dispatch` becomes the pipeline. The pipeline itself owns what must not be
skippable: the call's identity and immutable arguments, schema validation before any policy, attempts,
the catalogue's lifetimes, the one execution of the tool, and the final result. Its extension points
differ by what they may do, and the type says so. No point receives a continuation, so none can skip,
repeat or replace the execution:

```go
type Decision interface{ decision() } // Allow | Deny{Reason} | Ask{Reason, Facts}

type PreCall interface  { Decide(ctx context.Context, c Call) Decision }             // classification, row scope, matrix, standing declines
type Approver interface { Answer(ctx context.Context, c Call, a Ask) Answer }        // a person: approve, widen, decline with a reason
type Guard interface    { Refuse(ctx context.Context, c Call) (reason string, refused bool) } // grant, floors, evidence: can only refuse
type Screen interface   { Inspect(ctx context.Context, c Call, r Outbound) Verdict } // Pass | Refuse | Ask: never replaces
```

```
attempt → schema → PreCall* → Approver (on Ask) → fresh grant → Guard* → narrowed capability → tool (once) → Screen* → attempt closes
```

- **A person's answer is not the last word.** Guards and the capability come after the answer, on the grant
  as it stands then, because the wait can be ten minutes long: a file changed meanwhile fails its evidence,
  and an approved widening reaches the capability.
- **Guards cannot allow.** No ordering can turn a refusal back into permission.
- **`Ask` is an answer, and approval is a service.** A policy says "ask"; it does not wait for anyone. **No
  approver, or no answer, is a refusal.** This closes the endpoint's gap, where an `ask` row would run.
- **A call is immutable, and its fields are copies.** What a person approved is what executes. An approval stays
  bound to the run, attempt, tool, call id and argument hash, as `approvals.go` does today.
- **A screen reads what actually leaves**, the final outbound bytes, errors included, and nothing changes them
  afterwards. Refusing and asking on a secret stays; an approval then releases the retained bytes without running
  the tool again, as today.
- **Attempts keep their meaning.** An approval produces a subsequent attempt of the same entry, as ADR-0028 and the
  kernel have it, and the result body is recorded where it is now.
- **Every point observes `ctx`.** A blocked approver stops when the call is cancelled.

**Calls of one turn run one at a time, in order.** That is today's behaviour, since eino runs tools sequentially, and
it replaces the batch latch. The endpoint already accepts concurrent calls, so the pipeline holds one chain per turn.
So there is at most one pending question, `awaiting_approval` stays one state, a person sees one question at a time,
and a call behind a declined one is refused with the same reason rather than started. A turn is terminal only after
every accepted call has settled.

`internal/app` composes one chain per kind of caller. An assistant turn gets every point. **An agent in a pane gets its
grant and its timeouts: exactly its behaviour today**, stated by composition rather than by a condition inside the
endpoint, and an `ask` row there is refused. A new rule is one new link and one line of composition, and the endpoint
does not change. The links are compiled in; nobody asked for loading at runtime.

`kernel.go` does not move as a block. It is taken apart, and nothing it enforces is left unassigned:

| What the kernel enforces today                | Where it goes                                                  |
| --------------------------------------------- | -------------------------------------------------------------- |
| Fixed and raw-command floors                  | `Guard`, from `kernel.go` and `cmdeffect.go`                   |
| Command classification                        | `PreCall`, from `cmdeffect.go`                                 |
| Policy matrix and standing rules              | `PreCall`, from `globalpolicy.go`                              |
| Selected-row scope and bounded widening       | `PreCall` asks; the `Answer` widens; the grant is minted fresh |
| Standing declines                             | `PreCall`                                                      |
| Approval binding                              | `Approver`, from `approvals.go`                                |
| File-version evidence, expansion verification | `Guard`, after the answer                                      |
| Script readings                               | `Ask.Facts`: advisory today, and they stay advisory            |
| Narrowed capability                           | The pipeline, as `dispatch.go` does now                        |
| Egress finding and retained result            | `Screen`, from `egress.go`                                     |
| Batch latch                                   | The per-turn order above                                       |
| Skill-install facts                           | Kept through stage 2; they leave with skills at stage 3        |
| Attempts and their entries                    | The pipeline                                                   |

### 3. The boundary gains what it lacks

- **A person can block a call, and a service owns that.** The approver is a service in `internal/transport`
  that moves the run to `awaiting_approval`, publishes `agent.approvalRequested`, waits, and wakes the very
  call on `agent.approve`. Today the question is published only after `Ask` returns, and an answer starts a
  new drive; both change here. The eino checkpoint, the interrupt and the re-drive go.
- **The wait holds nothing others need.** Per ADR-8eej3e66 it is an `answer` phase, which pauses `max` and is
  bounded by its own declared limit of at least ten minutes. It holds no execution capacity and no resource
  chain; those are taken after the answer, in the same way as for any operation with phases. Progress never
  renews a lifetime.
- **Progress while waiting.** Where the client sent a `progressToken`, the bridge sends
  `notifications/progress` during the wait. The interval is under pi's default 60 s (inherited from the
  measurement above, and to be read again per agent). A connector that can set a request timeout sets
  it from the catalogue as well.
- **Cancel reaches the call.** Each call gets its own context, and the endpoint's wire gains a cancel
  message, which fixes `nocx-98qho`. A cancelled turn closes its connection, which ends what is left.
- **A third way in, for one turn.** `nocx-server` starts the agent itself, so it mints a bearer for the turn, hands
  it to the bridge the extension launches (`nocx-helper mcp --socket`) in that child's environment, and retires it
  when the turn ends. Admission checks the uid and the bearer, publishes under the existing rule that publication is
  part of the decision, and selects the assistant's chain. Its slot is the turn's, so it does not compete with a
  pane's caller slot. A reconnect presents the same bearer within the same turn; a retired one is refused, and
  retiring closes the connection and cancels its calls. ADR-0058's boundary is unchanged: this does not protect
  against another process of the same uid.
- **The grant is minted at admission, as on the existing ways in**, and refreshed after an answer. A turn longer than
  a grant's hour goes on on a fresh grant only through an answer; otherwise the call is refused and says why.
- **Failures the agent cannot overrule.** A declined egress ends the run today, and it still does: nocx cancels the
  turn. An agent that ignores a tool's error and reports success does not make the attempt succeed, and the block shows
  the attempt as it was.
- **The tools act on the pane's host.** The first version offers the assistant's present tools except
  `skills.*`, through `session.run` and its lease, and every tool acts where the pane is. `files.*`
  stop building on `local.New()` and act through the files binding the Files panel takes
  (`internal/filesystem/binding.go`): the local filesystem for a local pane, the helper-backed
  provider for a remote one (v0-9 charter, its Files stage `nocx-oalo1.6`; ADR-akhhb098). The
  assistant gets no file path and no resolver of its own, and never holds a raw provider: it
  acquires a `Handle`, with the binding's ownership, guard and teardown, for the call's lifetime and
  releases it when the call ends or is cancelled.
- **Swapping the provider is not enough, and the record says what else moves.** Today the
  `ScopedEditor` resolves a path through the provider and then edits it with `hashline`, which uses the
  local filesystem (`edit.go`, `hashline.go`), and the approval's file-version evidence reads with
  `os.Stat` and `os.ReadFile` (`fileversion.go`). So the `Handle` gains guarded capabilities beside its
  reads: `Canonical`, the revision-checked snapshot, edit and create (hashline over the provider's
  bytes, not over a local path), the evidence read, and the content search and recursive listing as
  one optional interface beside `DomainNamer`. The helper-backed provider supplies the primitives
  they stand on, in its DONE WHEN (agreed with the SSH foundation, `nocx-oalo1.6`): stat, list,
  canonical, a whole-file read with its version (size, mtime, sha256), and one atomic write whose
  optional precondition is checked by the helper on the host at the rename, refusing a file changed
  since it was read. The revision check is a read, an in-memory patch and that conditional write, so
  no check in the backend leaves a window open. The Files panel does not use the `ScopedEditor`; this
  is the assistant's part. A remote file changed
  while its approval was pending is refused, and its local namesake is never read or touched. Where
  the pane's host has no helper, the file tools say why, as the panels do, and `session.*` still
  work.
- **When something must give, a tool is cut before a check.** That still holds tool by tool; it does
  not make remote file tools optional, because without them the assistant cannot help on a server,
  which is the case the feature exists for.

### 4. The person steers and cancels the turn

- **Cancel** is mandatory in the contract, for a turn and for a call. The block keeps what was streamed:
  the ledger writes it as it arrives, because the agent's context drops a partial answer.
- **Steer** is optional in the contract, and pi has it. ACP has no standard for it. Composer, summon and a
  running answer must allow a message during a turn, which nocx does not do today (`nocx-7l4ex.9`).
- **A person's message during a pending approval declines that call, with their text as the reason**, and
  also refuses whatever is queued behind it, and then reaches the agent as a steer. Otherwise they write "no,
  do it differently" and the agent sits waiting on a button.
- **Without a steerer the message is not lost.** The declines still happen, and the text waits and becomes the next
  question once the turn ends. A connector states which of the two it does, and scenario 4 is judged on that: the
  pending call settles at once in both.

### 5. What stays true of the run

`agent.ask` keeps its transaction: an entry, an execution in the agent's lane, a grant per run, the states
`prepared`, `streaming`, `awaiting_approval`, and a terminal one. The renderer keeps `agent.run*`,
`agent.approvalRequested`, `agent.approve` and `agent.cancel`. Attempts from a run carry its id, its pane
and its cwd, which endpoint attempts do not today. `RunFacts` names the harness and whatever model it
reports. `agent.status` and the model chip are rebuilt on `Readiness`, in `agent-readiness.ts`, which stays
the one owner; a refusal opens the agent section.

**The conversation's context is the agent's**: one pi session per conversation, in nocx's data directory.
The ledger stays the truth of what is shown. If a session is lost, a new one starts and nocx says so:
the blocks remain visible while the model has forgotten them. nocx does not rebuild context from the ledger,
because that is the work this decision stops, so `PriorTurns` goes.

**A turn ends with the connection that started it**, as a run does today: its drive derives from that connection,
and `session.run` needs a person's renderer to execute at all. So closing the window cancels the turn, ends the
process and the bridge, and marks the run `interrupted`; a backend restart does the same through the existing sweep,
and approvals and bearers are in memory, so none survives. The session on disk lets the next question go on, and no
effect is replayed, because a new turn only sends the new text. A turn that outlives its window is a later feature:
it needs detached ownership, tools that work without a renderer, and approval on reattach.

**A dead backend takes its agent with it, through the pipes.** `nocx-server` cannot run `Cancel` once it is dead, and
the next start's sweep only updates rows. The agent's stdin and stdout are pipes held by `nocx-server`, so its death
closes them, the agent exits, and that closes its bridge's stdin in turn. Measured on pi by `SIGKILL` of the parent on
Linux, while idle, while a model stream was running and during a blocked tool: the agent and the bridge were gone
within 0.02 s each. It needs no platform-specific call, and macOS is measured by the same check in CI. A connector
whose agent survives this does not pass the conformance suite.

### 6. Settings detect what is installed

An agent section replaces the endpoints page and takes the roles page into it. It lists what is
installed, found through the login shell's `PATH` because the app's own is not a person's
(`nocx-4d6uf`). Agents with a connector can be chosen; the rest say "not supported yet". "pi found, no
provider configured" is its own state, and a dead key found during a turn leads to the same place.
**Any AI function with no agent ready opens this section**, through the one readiness owner.

Under the chosen agent, the **role table** stays as it is shaped today, a closed set the product
defines, and each role's model is picked from that agent's catalogue instead of from an endpoint
(owner, 2026-11-13). The first version resolves one role, `answering`. The roles that serve nothing
yet stay declared and unresolved, so a feature that asks for one later adds its resolution rather
than the table. A choice is held per agent, since one agent's model ids mean nothing to another.

What stays as it is: assistant permissions with the policy matrix and the standing answers, the
person's own instructions, agent access, agent records, rules and calibration.

### 7. What leaves

Eino and everything that reaches a model from nocx's own process: `engine.go`, `einoadapter.go`,
`answer_order.go`, `carrier.go`, `checkpoints.go`, `connection.go`, `headers.go`, `httpguard.go`,
`presentation.go`, `probe.go`, `endpoint_error.go`, `wiretap.go`. `systemprompt.go` has no model call: its
standing instructions about panes, blocks and tools become nocx's system prompt, and the facts it writes
per question today (cwd, host) move into the turn's own message (decision 8). Endpoints, with their contracts,
page and store, and Probe; the roles stay (decision 6). The classifier's model call, which no production
path reaches today; its role stays declared. The whole skill library with its audit and drafts (owner,
2026-11-06). `nocx-ga29v`, the assistant's own MCP client, never reached `main`, so it is closed rather
than removed. `agent.dump` goes with the wire capture. `PriorTurns` goes (decision 5).

`internal/toolendpoint`, `internal/mcpstdio` and the tool executors stay: they are the boundary.

### 8. The cache prefix is kept, and usage is recorded

A provider's prompt cache matches the exact bytes of a request from its start: tools, then the system
prompt, then the history. Losing it costs most of what a conversation spends, so nothing nocx controls
may change those bytes from one turn to the next (owner, 2026-11-13).

- **nocx's system prompt is the same bytes on every turn.** It holds nothing that varies: no time, cwd,
  host, pane state or run id. What belongs to one turn (the pane, its host, the marked rows, the screen)
  goes into that turn's user message. The prompt changes only when the person edits their instructions.
- **The MCP tool list is the same on every turn**: the same tools, in the same order, with the same
  schemas and descriptions. A grant, the matrix or a host without the helper never hides a tool; the
  pipeline refuses the call and says why. A turn's bearer and anything else per turn stay out of every
  description.
- **So are the MCP server's initialization instructions.** Today the endpoint derives them from the
  admitted grant (`toolendpoint/instructions.go`) and the bridge hands them over at initialization
  (`mcpstdio.go`), so a narrowed grant would change what the model reads. The assistant's chain sends
  one fixed text; an agent in a pane keeps its grant-derived instructions, by composition.
- **The history is only appended to.** nocx does not rebuild, edit or trim it; the agent holds it. A
  steer or a message during an approval goes at the end. A cancel drops what the cancelled model call
  was producing (measured on pi), so the next turn keeps the prefix up to the **last completed model
  call** and loses only the cancelled step; that is the accepted loss. Compaction is the agent's, rare,
  and restarts the prefix; the block says when it happened.
- **A conversation keeps its model.** A role's new choice applies from the next conversation; the one
  under way finishes on the model it started with, and the block says so. An unassigned role's default is
  pinned at the first turn (decision 1), so a changed agent default does not move a conversation.
- **A connector reproduces the previous request.** A process per turn costs the cache nothing, because the
  cache is the provider's and keyed by bytes, provided the agent rebuilds the same request from its
  session on disk. That is checked, not assumed (Constraints).

**Usage is recorded on every run.** A connector reports what its agent states (input, output, cache read,
cache write, per model call), `RunFacts` carries it, the ledger keeps it, and the block shows how much of
the input came from the cache. nocx records none of this today; it is new with the swap. An agent that
states no usage says so, and the block shows nothing rather than a zero.

## Constraints

Each is a requirement with its check.

| Constraint                                                                   | Check                                                                                                                                                                                                                                                                                                                           |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Nothing reaches a model from nocx's own process                              | `go.mod` has no eino or OpenAI client; the source test that today keeps eino out of kernel files widens to the whole tree                                                                                                                                                                                                       |
| An unanswered `Ask` runs nothing                                             | Pipeline test with planted policies: no approver, approver erroring, cancelled wait; the tool's body is never entered                                                                                                                                                                                                           |
| A guard cannot allow; no point can skip, repeat or replace the tool          | The types offer no allow result and no continuation; a test with a refusing guard beside every other link; the tool's body counted once                                                                                                                                                                                         |
| Schema precedes policy; the attempt spans the call                           | Pipeline test observing the order, with a failing tool, a cancelled call and a panicking tool                                                                                                                                                                                                                                   |
| An answer does not outlive what it answered                                  | A file changed during the wait is refused with no effect; a widening reaches the capability; a declined egress releases nothing and runs nothing again                                                                                                                                                                          |
| Calls of one turn run in order, one pending question at most                 | Two concurrent calls on one turn against a blocked approver: the second has not started; declining settles both                                                                                                                                                                                                                 |
| A late call cannot reach another turn                                        | A bearer retired by a cancelled turn is refused when a new turn is under way, and an in-flight call on it ends                                                                                                                                                                                                                  |
| An assistant run's chain holds policy, approval and the screen               | A test on the composition root's assistant chain; a removed link fails it                                                                                                                                                                                                                                                       |
| An agent in a pane behaves as before                                         | The existing endpoint tests pass unchanged, and a test proves an `ask` row is refused there                                                                                                                                                                                                                                     |
| A ten-minute wait does not break                                             | e2e on the fake agent with the clock under test control, plus a measured run on pi outside CI                                                                                                                                                                                                                                   |
| Cancel stops the agent within a second, with no event after                  | Contract suite in both orders against a blocked tool; a tool whose context ended records its cancel; a pane command is judged by the lease's own existing test                                                                                                                                                                  |
| A message during a pending approval settles the call at once                 | e2e on the fake agent: steer during approval, the attempt records a decline carrying the text                                                                                                                                                                                                                                   |
| A vault secret in a result never reaches the agent                           | e2e with known material in a tool result, asserting on every byte the fake agent received                                                                                                                                                                                                                                       |
| A failure is never a success or silence                                      | Contract suite: crash, dead key, provider error, a process that closes stdout                                                                                                                                                                                                                                                   |
| No process outlives its turn                                                 | Contract suite counting descendants, including the MCP bridge, after end, cancel, crash and a closed connection; no effect replays on the next question                                                                                                                                                                         |
| No process outlives the backend                                              | The conformance suite kills `nocx-server` with `SIGKILL` during a stream and during a blocked tool, on Linux and macOS, and watches the agent and the bridge disappear                                                                                                                                                          |
| A file tool on an SSH pane acts on that host                                 | e2e on the headless backend with a remote host behind the helper: read, search, an edit under a `permit` row and one under an `ask` row once approved change the remote file and leave the local path untouched                                                                                                                 |
| The assistant has no file path of its own                                    | A test on the composition root: the files.* narrowing takes the pane's provider from the files binding the Files panel takes; `local.New()` is not reached from `agenttools`                                                                                                                                                    |
| A person with no agent lands in the agent section                            | e2e on the headless backend: no agent, ask from a pane                                                                                                                                                                                                                                                                          |
| A connector is only supported once it passes                                 | One conformance suite runs against the fake agent in CI and against each declared real agent at release                                                                                                                                                                                                                         |
| The cache prefix survives a turn                                             | Conformance run against each real agent with a fake model behind it, comparing the ordered model input (tools, system content, messages), not serialized bytes: turn N+1's input begins with turn N's last completed model call's input, across a change of pane host, a refused call, a steer, and a cancel after a tool cycle |
| nocx's system prompt, tool list and MCP instructions do not vary             | Contract suite: two turns from different panes and hosts, one with a narrowed grant, give the model byte-identical system content, MCP instructions and tool lists                                                                                                                                                              |
| A conversation keeps its model                                               | Contract suite: the agent's default changes between two turns of a conversation with an unassigned role; the second turn runs on the pinned model                                                                                                                                                                               |
| Usage reaches the block                                                      | Contract suite: the fake agent's stated usage, cache read included, is on the run's facts in the ledger and in the block; an agent stating none shows none                                                                                                                                                                      |
| No agent-specific behaviour outside a connector                              | A source test: only `internal/harness/<connector>` and `internal/app` import a connector; outside connectors, composition and the declarative records of `internal/agentrecord`, no package holds an agent's name, flags or protocol handling                                                                                   |
| A connector's stated tool boundary is true                                   | The conformance run against each real agent asks it to call a built-in tool: a connector stating "nocx's tools only" shows no such tool declared and nothing executed                                                                                                                                                           |
| The chosen model is the one that answers, and a vanished one is not replaced | Contract suite on the fake agent: the turn carries the role's model; a model absent from the catalogue refuses the turn and names it                                                                                                                                                                                            |
| The person's instructions reach the agent                                    | Contract suite: the fake agent receives nocx's system prompt with the person's paragraph in it, appended to its own                                                                                                                                                                                                             |
| Every new operation is catalogued with its lifetime                          | ADR-8eej3e66's own checks                                                                                                                                                                                                                                                                                                       |

## Consequences

Easier: no bug needs knowledge of providers, models, context or compaction; a new agent costs a connector
or, through ACP, a record, and never a change to the core;
a new rule costs a link; an assistant turn through the endpoint is under the full authority contract, while
pane callers keep their reduced chain on purpose; a view such as a trajectory of a turn can be built later over
the ledger with no change to the chain.

Harder, and accepted:

- With no agent installed there is no assistant. "Paste a key and it works" is gone, and a provider is
  configured in the agent, not in nocx.
- Four connectors are four interfaces to follow as their vendors change them. Each is caught by the
  conformance run at release, and an adapter in between would add a fifth party, not remove one.
- Each question costs about half a second to start the agent, on pi.
- Tool calls of one turn never run in parallel, as today. A model that asks for several waits for each.
- Closing the window ends the turn, as today.
- Only nocx's tools are under nocx's authority. For pi, omp and Claude Code the launch removes the
  rest. For an agent that cannot be restricted, its own tools stay, nocx's system prompt steers it to
  nocx's tools, and its own tools act on the local machine outside the matrix, which the product says.
  An instruction is not a boundary: a model can ignore it.
- The assistant is only as capable on a server as nocx's tools are there. Remote file tools wait for
  the SSH foundation's helper-backed provider, and a host without the helper gets commands and the
  screen but no file tools.
- What an agent sends to a model from its own context is not screened. The egress screen covers tool
  results.
- A lost session means the model forgets while the blocks stay visible.
- pi requires Node, on the person's machine rather than in our package.
- `docs/vision.md` promised "any AI model"; that now holds through the agent, and its text changes with
  the first stage.
- 24 e2e specs and most of 56 transport test files assert the present engine, and they are most of the
  volume. The e2e path moves from a fake provider to a fake agent speaking the contract.

Would make this worth revisiting: `--no-builtin-tools` stopping to hold in pi; a long wait failing on a real
agent in a way progress cannot fix; or ACP gaining, natively in an agent, what makes its own connector
necessary, which would retire that connector.

Assumptions: the `--tools` filter holds against a person's own extensions and MCP servers (not yet measured,
and the pi connector's stage must). What the table in decision 1 says of Claude Code, Codex, omp and
prime-agent is read from their help, and each connector's stage measures it before relying on it. A person's agent is trusted as a program, since it already runs as them.

## Migration

The order keeps one path in `main` at each step, on short branches. It runs in v0-9 beside the SSH
foundation (`nocx-oalo1`; owner, 2026-11-13), which shares the composition root, `internal/transport`
and `ws.go`, and owns the file provider stage 8 consumes. Whoever merges second rebases, and a change to
`internal/app` wiring or `ws.go` is announced to the other line before it is pushed.

1. **The pipeline.** `dispatch` gains the named points, and the endpoint's gate becomes a guard. Behaviour
   does not change.
2. **The links.** The kernel comes apart, and the in-process run uses the pipeline. This stage carries the
   approval service whole: the wait, `agent.approvalRequested`, the run's state, waking the call on
   `agent.approve`, a fresh grant and subsequent attempts. A call blocks on it, so eino's interrupt and
   checkpoint go while eino still runs the loop, and the batch latch becomes the per-turn order.
3. **What leaves without a replacement.** The classifier, then the skill library, audit and drafts.
4. **Cancel and progress** at the boundary, with `nocx-98qho`.
5. **The swap, as one change:** the contract with its shared base, the fake agent, the conformance suite,
   the pi connector, the third way in, nocx's system prompt with the person's instructions, the agent
   section with the `answering` role picked from pi's catalogue, readiness, and usage on the run's facts. `Client.Ask` runs through the
   harness, and eino, endpoints and Probe leave in the same change, with the role contract rewritten
   (decision 1). `go.mod` loses eino here. **If stage 8 has not landed, `files.*` refuse on a pane that is
   not local**, saying the remote file tools are coming, and stay declared, so the cache does not move
   when they arrive; no state of `main` lets a file tool on an SSH pane reach the local disk.
6. **Steer** in the composer and the running answer.
7. **The other connectors, one stage each, in this order:** omp on the pi connector; Claude Code; Codex;
   prime-agent, through the pi family or through ACP as its stage finds. Each lands when it passes the
   conformance suite, and changes nothing outside its connector.
8. **Tools on the pane's host.** After the SSH foundation's Files stage (`nocx-oalo1.6`) lands: the
   `Handle`'s editing, snapshot, evidence, search and listing capabilities, the evidence guard on the
   handle, and `files.*` through the binding; then the refusal of stage 5 goes. It depends on that stage,
   not on stages 1 to 7, and may run before them, in which case stage 5 needs no refusal.

**Not in this record.** An agent that starts children, and children on a remote host started over
nocx's connections, are the next part of the feature (`nocx-dkawo`, `nocx-cxq7d`). Here the assistant
itself always runs beside `nocx-server`; a child placed on a host by an orchestrator is that part's
decision, and nothing above forecloses it.

## What happens to other records

The rule is that citations move with the record, and here they move with the code: comments citing ADR-0028
describe code that goes on running until stage 5, so each is moved or deleted by the stage that changes it.

| Record     | What holds now                                                                                                                                                                                                                                                                                  |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0028       | Superseded. "Authority cannot be enforced across the process boundary" is answered by decision 2: it is enforced on our side of it. Its findings on pi were read at 0.84.1, and pi now has MCP. Its client-callable `bash` is a command from the client, which is us; the model cannot reach it |
| 0030       | Superseded: endpoints as a record no longer exist                                                                                                                                                                                                                                               |
| 0020       | Stands: a grant per run, the lease. Decision 6's classification confidence goes with the classifier                                                                                                                                                                                             |
| 0031       | The endpoint counts leave; the rest stands                                                                                                                                                                                                                                                      |
| 0032       | The AI endpoint is no longer an unlock caller, and "material already handed to the in-process client" no longer applies; the fence stands                                                                                                                                                       |
| 0039, 0040 | Stand, with a new writer of the prose. The ledger no longer feeds a model                                                                                                                                                                                                                       |
| 0045       | "Any future carrier changes no policy, no approval, no attempt record" becomes true for the first time. The in-process carrier in its title goes                                                                                                                                                |
| 0058       | The run stops being the in-process instance and gains the conversation's way in; the unit of work stands                                                                                                                                                                                        |
| 0053       | Stands: the registry's rule. Its examples are model offers                                                                                                                                                                                                                                      |
| 0067       | "The renderer screen path stays for the assistant's own pane" is read again at stage 5                                                                                                                                                                                                          |

## Review

Codex, read-only, in ATAM's manner, against the checkout at `bd16c0268`, on 2026-11-06. It ran no tests and did not
verify the pi measurements. Validation: the direction matched the owner's settled decisions, and the first draft did
not deliver the frame as written. Nine blocking findings, all changing this record:

| Finding                                                                                   | Answer                                                                                       |
| ----------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| The chain did not account for what the kernel enforces; nothing rechecked after an answer | The preservation table; guards and the capability after the answer, on a fresh grant         |
| `Around` and replacing `PostCall` kept the powers rejected in option D                    | No continuation at any point; a screen cannot replace; the call is copies                    |
| "The conversation's current run" let a late call reach another turn's grant               | One process, connection and bearer per turn                                                  |
| Stage 2 removed publishing and resuming of approval without replacing their ownership     | The approval service, carried whole by stage 2                                               |
| Parallel calls and the terminal state were undefined once the latch left                  | One call at a time per turn; terminal after all settle; failures the agent cannot overrule   |
| The kept lease cannot stop a pane command in one second                                   | Scenario 3's measure is split: the agent in a second, a pane command by the lease's own walk |
| The approval lifetime contradicted ADR-8eej3e66                                           | An `answer` phase, holding no capacity or chain, with progress renewing nothing              |
| Outliving the window needs far more than subprocess ownership                             | The turn ends with its connection, as today; outliving it is a later feature                 |
| Optional steering did not deliver scenario 4 everywhere                                   | The decline always happens; without a steerer the text becomes the next question             |

Comments taken: `ModelChooser` served no scenario and left the contract; wording on what the endpoint has today was
narrowed.

The second and last round closed all nine, confirmed that a blocking approver works while eino runs the loop, since
its sequential tool invocation stays suspended on the stack, and found that scenarios 3 and 7 as changed do not betray
the frame. It found one new blocking finding: nothing terminated the agent and the bridge when the backend itself
dies. Answered by the pipes, measured, with a check that kills the backend. That answer has not been read by a
reviewer, since the round rule allows no third; it stands on the measurement.

Several of the records above were read by targeted lines rather than whole (0029, 0053), and the review did not read
the other cited records in its second round, nor pi's or ACP's implementations.

### Third round, 2026-11-13

The owner's decisions of 2026-11-13 changed decisions 1, 3, 6, 7 and 8 and the migration, so those
changes were read again by Codex, read-only, against `03f88fefa`. Six blocking findings and three
comments, all answered in this record:

| Finding                                                                                      | Answer                                                                                                   |
| -------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| A provider swap leaves `hashline` editing and the approval's file evidence on the local disk | The `Handle` gains editing, snapshot, evidence, search and listing; stage 8 delivers them                |
| A binding hides its provider, and its `Handle` cannot feed a `ScopedEditor`                  | The assistant acquires a guarded `Handle` per call and never holds a raw provider                        |
| Stage 5 could land before stage 8 and leave file tools on the local disk for an SSH pane     | Until stage 8, `files.*` refuse on a non-local pane and stay declared                                    |
| The MCP initialization instructions vary with the grant                                      | The assistant's chain sends one fixed text; pane agents keep theirs                                      |
| Cancel drops a partial answer, so "turn N+1 begins with turn N" cannot hold as bytes         | Compared over ordered model input up to the last completed model call; the cancelled step is the loss    |
| An agent default is not a pinned model                                                       | The default is resolved at the first turn and pinned; a connector that cannot learn it says so           |
| Roles need a replacement contract once endpoints go                                          | Assignments become (agent, model), no product-wide default, choices kept per agent, rewritten at stage 5 |
| `systemprompt.go` has no model call                                                          | Its standing text is kept; its per-question facts move into the turn's message                           |
| The agent-name test forbade composition and records                                          | It forbids agent-specific behaviour outside connectors, composition and records                          |

The answers were not read by a reviewer again.
