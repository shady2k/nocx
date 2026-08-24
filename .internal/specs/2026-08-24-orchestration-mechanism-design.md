# The orchestration mechanism: a coordinator that cannot forget it has children

Status: draft, first revision after adversarial review. Owner approved sections 1–3 in
discussion on 2026-08-24; the review by codex the same day rewrote two of them.

Bead: `nocx-4n3bz`. Continues `nocx-dg51h`, which continues `nocx-kewo`.
Implements the mechanism for `nocx-dkawo`; `nocx-szb40` owns the indicator that reads it.

## 1. In one sentence

Orchestration in nocx is the harness's own subagent tool with the subagent replaced by a real
pane running a real agent TUI, on any host nocx has a session to, that a human can take over —
and the reason a coordinator cannot lose track of one is that **supervision is a leased interval
with a named end, not a call that happens to be outstanding**.

## 2. What this crosses, and what those documents already decided

**AD-1** splits the wire into a binary data plane and a JSON-RPC control plane and forbids
additions without their own decision. **AD-6** forbids the backend to infer meaning from terminal
bytes; the renderer owns VT state. **AD-7** makes the session the backend-owned identity.
**AD-8** puts every module behind one interface at one composition root. **AD-9** makes output
offsets per-session replay coordinates, not globally meaningful numbers.

**ADR-0020** records authority per RUN, never per session, as an immutable grant on the run.
**ADR-0024** decision 1 is the sharpest constraint here and is quoted in full below, because this
design's first draft violated it. **ADR-0025** refused a pass-through for a user's typed options
in a backend-composed line. **ADR-0028** keeps the agent loop ours and narrows by capability
rather than checking before a call.

`.internal/specs/2026-08-15-workspaces-lineage-and-orchestration-design.md` (second draft, NOT
approved) decides: §6 one dispatcher and two callers; D8 that a controller SESSION, not a run and
not a lineage edge, owns a continuing delegation, and that delegation is revocable while lineage
is immutable; D9 that spawning a child is the `delegate` effect over a resource environment,
permitted only into an environment already in scope; D13/D14 that an external caller is
authenticated by enrolment of a process TREE, pinned by a non-reusable handle, approved once by a
human, with no bearer material in any environment; D5 that at stage 1 workers die with the
backend. §11 lists ten open questions; §11's closing section lists what would falsify the whole
thesis.

`nocx-d6gn4` owns the declarative-plan half and is not re-invented here: the model proposes a
bounded executable artifact and a deterministic executor owns control flow, authority, state
transitions and the auditable trace. It already separates an APPLICATIVE plan (every effect
describable before any result is known) from a MONADIC one (a step depends on an earlier result,
so evaluation and execution interleave and the host only ever sees the current resolved effect).
Spawning a worker is intended to become one more effect in that kernel (`nocx-d6gn4.3`), not a
second mechanism.

## 3. The problem, measured rather than assumed

The coordinator forgets it has children. Every item below is from this repository's own memories,
recorded by sessions that lived it:

- `lesson-orca-worker-waves-nocx-orchestration-check-wait`: `check --wait` returns ONE message per
  call. A coordinator looped twice for its two workers while five had been dispatched. Three
  worker_done reports and one escalation went unread; it then invented a false "workers overran
  their scope" narrative and wrote that falsehood into two commit messages.
- `lesson-orca-worker-waves-nocx-never-leave-two`: `--wait` CONSUMES. Two supervisors running at
  once silently split the mail; a live worker's report landed in a superseded task's output file
  and was found only by accident, 17 minutes later.
- `lesson-herdr-coordination-nocx-2026-08-15-in`: every background wait is reaped at the turn
  boundary — socket subscriptions and server-side waits alike, 9 minutes and 40 minutes died the
  same.
- `herdr-worker-waves-putting-the-completion-sentinel-in`: the completion sentinel matched ITSELF
  at 7–13 s, because the agent printed the brief file it had just read. Both worktrees empty, both
  agents still working.
- `playbook-orca-workers-omp-pi-how-to-run`: dispatch is not delivery; "dispatched" is not proof
  of start; a stale pane handle is indistinguishable from a dead worker; worker_done prose is a
  claim, not evidence.

These are five faces of one defect. An agent exists only while it takes a turn, so any protocol
that requires it to remember to come back is a protocol that will be forgotten. **Discipline
cannot be repaired with discipline.**

The reason the harness's own subagent tool does NOT have this defect is worth stating, because it
is the whole design: **its fan-out and its fan-in are one blocking call**, so "children started,
nobody watching" is not a state a caller can reach. herdr's `dispatch` and `wait` are two
commands, so that state is reachable, and everything above follows from it.

### 3.1 Constraints the owner set

- **The coordinator must be free.** Workers are subscription CLIs. Making nocx's own agent the
  coordinator moves the token cost onto the one role that produces no code.
- **Full agents with their own TUI in panes**, not the harness's headless subagents. This is the
  product difference and it is not negotiable.
- **The human does not hand-split the work.** A design where a person declares the wave in the UI
  was raised and rejected as a manual operation for its own sake.
- **Invasive at launch is welcome**; editing anyone's config files is not.

## 4. Decisions

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                     | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | **Supervision is a LEASE: an interval with a named end.** A wave is created together with a lease held by a coordinator session; children stay `prepared` until it exists; returning from a wave call does NOT release it; any wave call renews it; it ends only on all-children-terminal, explicit transfer, explicit human abandonment, or expiry                                          | "One blocking call makes the bad state inexpressible." **This is false and the first draft asserted it.** The call must return at decision points, and during model inference, compaction, an approval prompt or a failed next call, the children are live and no call is outstanding. That is precisely the state claimed unreachable; the draft had renamed the dangerous interval "decision point", not closed it                                                                                                         |
| D2  | **Lease expiry is the only alarm.** It means nobody has been on duty longer than the renewal interval                                                                                                                                                                                                                                                                                        | A count of attached waiters (nelix's `waiters.py`). It measures connections, not supervision: it reads zero on every legitimate thinking pause — many times per wave — and non-zero for a zombie listener with no authority to act. An alarm that fires in normal operation is not an alarm                                                                                                                                                                                                                                  |
| D3  | **A restarted coordinator asks which waves it holds a lease on.** The lease is the identity it recovers with                                                                                                                                                                                                                                                                                 | Reconstructing children from the transcript or from lineage. Lineage proves provenance and confers no authority (D8); a lost context has nothing to ask with                                                                                                                                                                                                                                                                                                                                                                 |
| D4  | **Failure is CLOSED: no enrolment, no orchestration, and the pane says so**                                                                                                                                                                                                                                                                                                                  | The `ssh` path's fail-open, where every refusal runs the user's line conventionally (`nocx.bash:721`, and `lifecyclepub/publisher.go:307` documents the empty bootstrap as that contract). Correct for an optional enhancement, fatal for an invariant: one hook error, one unknown flag, one dead channel and an entirely ordinary, uncounted agent exists. Optional integration and invariant-bearing enrolment need opposite failure policies                                                                             |
| D5  | **Delivery is an ALIAS to a launcher that `exec`s the agent.** nocx defines the alias in the panel session it already composes; the launcher resolves the agent adapter, requests enrolment over the local socket, pins the enrolment to ITS OWN pid, stages config in temporary files, and `exec`s the real agent — `exec` preserves the pid, so the pin survives and the launcher does not | Extending the shell's nested-environment detector to agent names. Measured false: `internal/shellintegration/scripts/nocx.bash:578` recognises only `ssh`/`sudo`/`su`, only while the lifecycle channel is active, and `internal/ssh/typed_wrap.go:30` keeps the user's own argv rather than rewriting it. There is no generic argv-rewrite seam to extend, and agent launches add aliases, absolute paths, `env FOO=… claude`, wrappers, shell functions, version managers and vendor flags that no such detector can cover |
| D6  | **Hooks carry nothing required.** They may optimise; the invariant does not rest on them                                                                                                                                                                                                                                                                                                     | Making a per-agent hook API load-bearing. It multiplies integration cost by the number of agents and by their versions, and it was the only reason the design needed one                                                                                                                                                                                                                                                                                                                                                     |
| D7  | **Mail rides our own calls and is acknowledged explicitly.** A report, a wait or an explicit inbox check returns pending mail                                                                                                                                                                                                                                                                | Piggybacking mail on the result of ANY tool call via hooks. Agent-specific and unverified; schemas and size bounds differ; delivery after a destructive call is already too late; a retry redelivers; parallel calls make order ambiguous; and unrelated tool output becomes a covert channel for coordination data                                                                                                                                                                                                          |
| D8  | **Four acknowledgements are distinct facts and are never merged**: committed to the mailbox, fetched by the participant, present in the model's context, acted upon. The cursor advances on the second                                                                                                                                                                                       | One "delivered" state. Collapsing them is how consumed mail became invisible in the measured failure                                                                                                                                                                                                                                                                                                                                                                                                                         |
| D9  | **Only PROCESS EXIT may decide wave state among observed facts**                                                                                                                                                                                                                                                                                                                             | The 2026-08-15 spec's `observed` row, which also lists alternate screen, blocked-on-stdin and silence. ADR-0024 decision 1 forbids any sequence parsed from the byte stream — "standard OSC, private OSC, DCS, title, terminal mode, or anything else" — from opening or completing an execution attempt. Alternate screen and the title are exactly that. Silence is not a state. Blocked-on-stdin is not trustworthily observable from a PTY. See §8.1: this obliges an amendment to that spec                             |
| D10 | **Star topology now; participants are addressable nodes from day one**                                                                                                                                                                                                                                                                                                                       | Mesh at stage 1. Under mesh the coordinator is mirrored on MUTATIONS OF THE RECORD — file ownership, task scope, participant set — and never on message content. Content is theirs; commitments are his. This matches the repo's own `TRUST NOTHING, DIFF EVERYTHING`: a coordinator that judges the diff does not need the conversation, but it does need to know when the conversation changed a fact it owns                                                                                                              |
| D11 | **The harness's built-in subagent tool is NOT restricted**                                                                                                                                                                                                                                                                                                                                   | Denying it with a `PreToolUse` hook. Considered and dropped: it blocks, therefore it does not have this defect, therefore banning it solves nothing and costs the cheap uses (search, reconnaissance)                                                                                                                                                                                                                                                                                                                        |
| D12 | **A wait-for cycle is a SUSPICION that must be corroborated, not a proof**                                                                                                                                                                                                                                                                                                                   | "Both waits are our own edges, therefore the deadlock is proved." A wait may be cancellable, an OR-wait, bounded by an external or human event, stale after a restart or attempt replacement, already satisfied by a durable-but-unreduced mailbox event, or owned by a process that has exited. A cycle proves deadlock only if every edge is current, exclusive, necessary, unsatisfied and attached to a live attempt with no external resolver                                                                           |

## 5. The invariant

1. **Supervision is a leased interval with a named end** (D1). The wave cannot come into existence
   unsupervised, and the coordinator remains on duty while it thinks.
2. **The record outlives the coordinator**, and the lease is what a restarted coordinator
   identifies itself with (D2, D3).
3. **Failure is closed** (D4).
4. **Three deterministic levers, and only three**: what we launch the agent with; what the PTY
   gives us as a process fact; what the participant declares over the authenticated channel.
   MCP surfaces, skills, tool descriptions and prompt text are PERSUASION — they raise probability
   and cannot carry an invariant.

## 6. The wave record

Backend-owned, and it holds exactly six things.

- **Participants.** Named, addressable nodes from the first day (D10), each carrying the immutable
  lineage edge this epic produces — a pane opened by a run has exactly one possible parent, the
  session of the run that asked. The three ambiguous UI origins are `nocx-ebl4.1` and are NOT this
  epic's business.
- **A mailbox per participant, whose read consumes nothing.** Per-reader cursors. This is the
  direct answer to the 17-minute split-mail failure: a second reader cannot take the first
  reader's mail because reading is not taking.
- **Liveness with provenance**, bound to backend instance, session id, session epoch, lifecycle
  domain, attempt and output offset — the full identity, because AD-9 offsets are per-session
  replay coordinates and a bare attempt number attaches old evidence to a new incarnation.
- **A wait-for graph**, whose cycles are suspicions to corroborate (D12).
- **The lease**, with its holder, epoch, expiry and the closing event that ended it.
- **Terminal facts**: what each participant declared it produced, and its process exit.

### 6.1 Evidence, by what it may decide

| Class                | Source                                          | May decide wave state | May light the indicator | May wake your phone   |
| -------------------- | ----------------------------------------------- | --------------------- | ----------------------- | --------------------- |
| `known`              | our own loop (ADR-0028)                         | yes                   | yes                     | yes                   |
| `declared`           | the participant, over the authenticated channel | yes                   | yes                     | yes                   |
| `observed`           | **process exit only** (D9)                      | yes                   | yes                     | no                    |
| `declared-anonymous` | terminal title, OSC 0/2                         | **no**                | yes                     | per-agent opt-in only |
| `inferred`           | screen matching                                 | **no**                | yes, weakest            | no                    |

The indicator is legal because AD-6 binds the BACKEND and the renderer owns VT state;
`frontend/src/agent-status.ts` already derives `working | idle` from the title. That module
answers only whether SOMETHING is working, never WHO — its own header says so — so it can never be
bound to a participant, and it belongs on the `declared-anonymous` row by construction.

herdr's indicator lives on the weakest row: twenty TOML manifests matching literal UI copy against
screen regions, refreshed from a network catalogue. Ours lives on the strongest, because a
participant that was handed a tool can simply say. Screen matching remains available for what we
failed to integrate, and never wakes anything.

The indicator itself is `nocx-szb40`'s deliverable. This design's obligation is only to keep
observations separable, provenance-tagged and attempt-bound, so that a late observation from a
previous attempt cannot overwrite a current one.

## 7. Delivery: how the tool reaches the agent

### 7.1 Alias → launcher → `exec`

nocx defines the alias in the panel session it already composes — the same session that carries
shell integration from the versioned bundle under `~/.nocx/`. **No rc file is edited**, which the
delivery-modes design already requires, and the alias therefore exists in nocx panes and nowhere
else. That is the right scope: orchestration is a nocx feature.

The user types `claude`. `nocx agent run claude …` runs, and it:

1. resolves the per-agent adapter (an interface under AD-8, one implementation per agent);
2. requests enrolment over the private local socket;
3. has the enrolment **pinned to its own pid**;
4. stages the agent's config — tool surface, and hooks where the agent supports them — in
   temporary launch-owned files;
5. `exec`s the real agent. `exec` preserves the pid, so the pin survives and the launcher does not.

If enrolment cannot be established, it **refuses visibly** (D4). A bare `claude` typed outside a
nocx panel session stays an ordinary agent and is marked as not orchestrated.

The alias is itself a visible, removable footprint, which satisfies `nocx-bu6q` without separate
work; consent is asked at the feature, not at the connection (`nocx-8d6e`).

### 7.2 The calls that must exist

- **One spawn primitive, and it blocks.** It has no non-blocking form. It returns at decision
  points — the first participant settles, one asks, one exits, a corroborated deadlock — and
  returning does not release the lease (D1).
- **Say to a participant.** Star policy admits one legal recipient today; addressing is real from
  the first day (D10).
- **Report, structurally.** Completion is what a participant SAID; process exit corroborates it.
  This is what kills the self-matching sentinel: there is nothing on a screen to match.
- **Check the inbox**, explicitly, with explicit acknowledgement (D7, D8).

Every response carries a cursor, and the coordinator acknowledges the cursor together with the
effects it commits from that response — otherwise "read consumes nothing" prevents loss but not a
duplicated spawn, interrupt or scope mutation on retry.

### 7.3 The asymmetry, stated rather than hidden

Worker → coordinator is reliable: the coordinator is inside a wave call, and the event returns it.

Coordinator → worker arrives at the worker's next wave call, which may be a long time. There is no
mechanism here that makes a busy agent read sooner, and none is claimed.

**A real interrupt is a separate, coarser operation** with its own effect class: it names a target
process group, has an escalation sequence, records an attempt, and confirms what it terminalized.
It is not message delivery wearing a different hat. Signals and keystrokes have ownership
consequences — `SIGINT` reaches the TUI or its foreground child depending on process groups, a
keystroke can answer a prompt instead of interrupting, and an interrupt during a tool call can
leave the filesystem half-mutated.

## 8. What the adversarial review changed

codex reviewed the first draft against the tree on 2026-08-24 and found eighteen items. Items 1-3
below are AUTHORIAL ERRORS — the draft stated things about this repository that are false — and
they are recorded because that mistake is cheap to repeat: all three came from writing about a
mechanism the author remembered rather than one he had read. Item 4 is not an error about the
repository but the sharpest design defect, and it is listed with them because it is the one that
would have shipped a false guarantee.

1. **"The same seam that rewrites a hand-typed `ssh`" does not exist.** `internal/ssh/typed_wrap.go:30`
   keeps the user's own `ssh` process AND ITS ARGV and adds two options; the replacement for the
   nested case is a backend-composed line `eval`ed by the shell hook, and ADR-0025 already refused
   the general pass-through form. Replaced by D5.
2. **Fail-open is the documented contract of that path** (`nocx.bash:721`), and it is fatal here.
   Replaced by D4.
3. **The "zero cooperation" backstop does not work.** Once the agent owns the foreground, the
   parent shell is blocked inside that one long command and never sees what the agent spawns
   internally. `agent-status.ts` says why in its first paragraph. **Dropped, not replaced**: a
   requirement the first draft invented for itself.
4. **The blocking call closes a moment, not an interval** (design defect). Replaced by D1 — and
   this is the AGENTS.md rule the draft quoted at its reviewers while breaking it. A fifth
   finding, that `frontend/src/agent-status.ts` answers only whether SOMETHING is working and
   never WHO, is folded into 6.1 rather than listed here.

The review's remaining findings are either folded into the decisions above (D2, D7, D8, D9, D12)
or recorded as open questions in §10 — none is discarded silently.

A sidecar supervisor process per participant was proposed and **rejected after costing it**: its
three benefits are either unnecessary (descendant capture, item 3, which is now out of scope),
already ours (process exit, because nocx owns the PTY), or obtainable without it (D13's pinned
handle, because `exec` preserves the pid).

Running workers headless behind our own loop and projecting each into a pane was proposed and
**rejected on the owner's constraints**: it breaks subscription auth and the vendor TUI, which are
the two fixed requirements. It is recorded because it is genuinely the strongest design on every
axis except those two.

### 8.1 An amendment this design owes another document

The 2026-08-15 design's §5.2 puts alternate screen, blocked-on-stdin and silence in the `observed`
class and lets that class decide. ADR-0024 decision 1 forbids exactly that for anything derived
from the byte stream, terminal modes and the title included. That spec is not approved; the
contradiction should be resolved there rather than worked around here. D9 states this design's
position.

## 9. Deliberately out

- **Capturing an agent that another agent spawned around the tool.** Not caught, and not pretended
  to be caught.
- **A sidecar supervisor**, and any per-participant helper process (§8).
- **Restricting the harness's own subagent tool** (D11).
- **The relay, durable sessions and reattach** — `nocx-1p1h4`, blocked behind the helper reaching
  `main`.
- **Worktree creation as one call** — `nocx-kdccc`.
- **Agent lifecycle classification and the indicator** — `nocx-szb40`.
- **The declarative plan and its executor** — `nocx-d6gn4`. A wave becomes an effect in that
  kernel; this design does not build a second one.
- **The three ambiguous UI origins of a lineage edge** — `nocx-ebl4.1`.

## 10. Open questions

Each is a real hole, and each traces to a review finding that this draft did not close.

1. **Delegation and authority.** D8 of the 2026-08-15 design gives a controller SESSION a revocable
   delegation over `observe`, `receive-events`, `send-input`, `delegate-further`, `close`; this
   design says "addressable participant" and names none of it. Takeover, re-credentialing, domain
   change and coordinator replacement currently have no defined effect on who may address whom.
2. **Spawn as D9's `delegate` effect, and the run grant.** ADR-0020 requires an immutable grant on
   each run; D9 permits spawning only into an environment already in scope. The spawn primitive
   here is described without either, which risks encoding the wrong principal in the record and
   the API before authority is added.
3. **Enrolment versus injection.** D13 requires peer identity, foreground process-tree membership,
   a non-reusable pinned handle and human approval. D5 pins to the launcher's pid, which is
   necessary and not obviously sufficient: what binds every later request to that tree, and what
   stops another process in it from impersonating the participant?
4. **ADR-0028 narrowing for the external caller.** Every mailbox read, send, spawn, interrupt and
   record mutation should hold a capability incapable of naming anything outside its delegation.
   Described here as one shared surface, which is the ambient dispatcher API that ADR-0028 rejects.
5. **Spawn-and-register has no stated transaction.** What is true on disk, in the record and in the
   process table when step 3 of 5 fails; whether recovery adopts the pinned process or terminalizes
   it; and the interval within which a participant record must exist — from before the first
   irreversible spawn effect until the enrolled incarnation is supervised or the record is durably
   terminal.
6. **Cursor lifecycle.** Who mints a reader id; whether a restarted coordinator is the same reader;
   what happens to an abandoned cursor; what a compacted mailbox returns; whether mesh implies
   N² cursors.
7. **Does the record survive a backend restart?** D5 says stage-1 workers die with the backend.
   "Outlives the coordinator" is deliberately narrower than "outlives the backend", and the
   recovery semantics differ; this draft has not stated which it claims.
8. **Bounds.** Participants per wave, nesting depth, waves, mailbox bytes, message size, retained
   cursors, wait edges, declaration rate. §11.6 of the 2026-08-15 design already has lineage depth,
   fan-out and fork-bomb resistance open, and this builds on that edge.
9. **What mesh actually needs**, beyond addressable participants: sender authentication, causal
   ordering, mutation arbitration, revocation, loop prevention, dead-letter handling — and the
   atomicity hole where a message promises a mutation that then fails to commit.

## 11. Assertions

Written as assertions rather than prose, per AGENTS.md's cheapest defence against a test that
encodes the implementer's model.

1. A coordinator in a nocx pane starts three workers, gives each a task, waits with ONE wait that
   returns when the first settles, reads what each produced, and closes them — with herdr not
   installed and not running.
2. The wave cannot be created without a lease, and a spawn attempted without one produces no child
   process at all.
3. A wave call that returns leaves the lease held: a probe immediately after the return finds the
   coordinator still on duty, and no alarm is raised.
4. A coordinator killed between two wave calls causes the lease to expire, exactly one alarm, and
   an attention event that reaches the human.
5. A coordinator restarted with no context asks which waves it holds and is told its three
   children by name.
6. A launcher whose enrolment fails does not exec the agent, and the pane says the agent is not
   orchestrated. No wave record is created, and no participant appears.
7. Two readers read the same mailbox; neither loses a message, and the cursor of one does not move
   because the other read.
8. A message committed to a mailbox, but never fetched, is reported as committed-not-fetched, and
   is not reported as delivered.
9. A worker's report is accepted while its process is still alive, and its process exit is recorded
   separately; neither alone terminalizes the participant.
10. A title containing a braille spinner changes the indicator and changes NO wave state; a title
    engineered to look like a completion wakes nothing.
11. A wait-for cycle whose edge is already satisfied by a durable mailbox event is not reported as
    a deadlock.
12. The alias is removable, and after removal `claude` runs ordinarily and is marked not
    orchestrated.

## 12. What would falsify this design

The 2026-08-15 thesis is "orchestration is a view over sessions, not a second product", and its own
falsification list includes transactional fan-out/fan-in, dependency-aware retries, and scheduling
work when no session yet exists. **A backend-owned wave record with a lease is fan-out/fan-in.**
That spec says the first such need is the signal to revisit the thesis; this design claims the
trigger has fired and says so here rather than passing quietly.

What would falsify THIS design:

- If supervision as a lease turns out to need renewal so often that it becomes a poll, it is a
  blocking call with extra steps and D1 bought nothing.
- If the per-agent adapter cannot be written for two agents without a shared shape, D5's AD-8 seam
  is a fiction and the launcher is twenty manifests wearing a Go interface.
- If a launcher that refuses visibly is routinely worked around by typing the agent directly, then
  fail-closed was not acceptable to the user and D4 must be re-argued rather than re-implemented.
- If most real waves turn out monadic, the token cost falls back onto the coordinator's turns and
  the free-coordinator constraint from §3.1 is not met by this mechanism alone.
