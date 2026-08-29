# The orchestration mechanism: a coordinator that cannot forget it has children

Status: second revision. The first draft put supervision inside the coordinator as a blocking
call; the adversarial review of 2026-08-24 replaced that with a lease; working the constraint out
with the owner the same day removed the lease too. **Supervision is not the agent's at all.** The
backlog that revision produced (`nocx-dkawo.1`–`.5`, `nocx-szb40.1`–`.4`, `nocx-22k1c`) is the
sequencing authority; this document is the reasoning behind it.

Bead: `nocx-4n3bz`. Continues `nocx-dg51h`, which continues `nocx-kewo`.
Implements the mechanism for `nocx-dkawo`; `nocx-szb40` owns the driver and the indicator it
reads, and `nocx-dkawo` now **consumes** them rather than the other way round.

## 1. In one sentence

Orchestration in nocx is the harness's own subagent tool with the subagent replaced by a real
pane running a real agent TUI, on any host nocx has a session to, that a human can take over —
and the reason a coordinator cannot lose track of one is that **supervision belongs to the
backend, a process that has no turns and therefore cannot forget; the agent's attention stops
being load-bearing rather than being guaranteed.**

## 2. What this crosses, and what those documents already decided

**AD-1** splits the wire into a binary data plane and a JSON-RPC control plane and forbids
additions without their own decision. **AD-6** forbids the backend to infer meaning from terminal
bytes; the renderer owns VT state. **AD-7** makes the session the backend-owned identity — which
is what a restarted coordinator recovers with (D3). **AD-8** puts every module behind one
interface at one composition root. **AD-9** makes output offsets per-session replay coordinates,
not globally meaningful numbers.

**AD-6 and ADR-0002 are crossed by this design, deliberately and narrowly.** A backend that
watches while the window is shut needs a live grid for a participant pane (D13), and AD-6's own
amendment refuses that by name, while ADR-0002 closed the same door twice in its own consequences:
"**AD-6 stands.** No VT grid on the Go side; render state stays in the frontend", and, of zellij's
"server holds the grid and repaints on attach", that the alternative "is _closed_ to us by AD-6,
not merely unchosen — worth recording so it is not re-proposed as a shortcut". (Those are two
separate bullets; `nocx-szb40.2` quotes them spliced into one.) ADR-0002's stated revisit trigger is _a session
that survives the client process entirely_, and it has now fired. The boundary is written as
acceptance criteria on `nocx-szb40.2` until it is written into `docs/architecture.md` beside AD-6,
in the same shape as its existing carve-out: an interval with both ends, and an explicit statement
of what it does not license.

**`internal/notify` is crossed too, and it is code rather than a document.** Its `Trust` — three
classes stamped by the source adapter, never on the wire, enforced default-deny — already owns
"what may reach a human, and by which channel", which §6.1 maps onto rather than restating.

**ADR-0020** records authority per RUN, never per session, as an immutable grant on the run.
**ADR-0024** decision 1 is the sharpest constraint here and is quoted in full below, because this
design's first draft violated it; decision 2 built the authenticated channel that carries every
fact this design trusts. **ADR-0025** refused a pass-through for a user's typed options in a
backend-composed line. **ADR-0028** keeps the agent loop ours and narrows by capability rather
than checking before a call.

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

These are five faces of one defect, and the defect is not a missing feature in any of those tools.
**An agent exists only while it takes a turn.** Anything that depends on an act the agent must
remember is therefore not an invariant, however durable the mechanism underneath it: that holds
against a better `--wait`, against a subscription that survives the turn boundary, and against the
harness's own `Monitor`, which is genuinely cross-turn and still has to be armed by an agent that
must remember to arm it. **Discipline cannot be repaired with discipline, and durability is not a
substitute for it.**

The reason the harness's own subagent tool does NOT have this defect is worth stating, because it
is what the first draft copied and got wrong: **its fan-out and its fan-in are one blocking call**,
so "children started, nobody watching" is not a state a caller can reach. herdr's `dispatch` and
`wait` are two commands, so that state is reachable, and everything above follows from it. §8 says
why copying the blocking call did not work either.

### 3.1 Constraints the owner set

- **The coordinator must be free.** Workers are subscription CLIs. Making nocx's own agent the
  coordinator moves the token cost onto the one role that produces no code.
- **Full agents with their own TUI in panes**, not the harness's headless subagents. This is the
  product difference and it is not negotiable.
- **The human does not hand-split the work.** A design where a person declares the wave in the UI
  was raised and rejected as a manual operation for its own sake.
- **Invasive at launch is welcome**; editing anyone's config files is not.

## 4. Decisions

`D1`–`D3` were rewritten in this revision; `D13`–`D15` are new. Where a decision of the
2026-08-15 design is meant, it is named as such, because that document has a `D5`, `D8`, `D9`,
`D13` and `D14` of its own.

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                     | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | **Supervision is the BACKEND's.** The backend holds the wave record, watches the children and cannot forget, because it has no turns. The coordinator being idle is the normal state, not the failure                                                                                                                                                                                        | **A LEASE held and renewed by the coordinator** — this document's own previous D1. Renewal is an act the agent must remember, so it is a poll under another name, and the argument that killed the blocking call kills it too. Before that, "one blocking call makes the bad state inexpressible", which is false: the call returns at decision points, and during inference, compaction or an approval prompt the children are live and no call is outstanding                                                              |
| D2  | **The backstop is a PER-FACT DEADLINE.** A fact that needs judgement and has not been dispatched within a named deadline reaches the human. Nothing ticks while there is nothing to dispatch                                                                                                                                                                                                 | Lease expiry (previous D2): it times the SUPERVISOR, so it runs during a wave that is entirely healthy and silent, and it cannot distinguish an idle coordinator with nothing to do from an absent one. And a count of attached waiters (nelix's `waiters.py`), which measures connections rather than supervision: zero on every legitimate thinking pause, non-zero for a zombie listener with no authority to act                                                                                                         |
| D3  | **A restarted coordinator asks what its SESSION holds and is told by name.** AD-7 makes the session the backend-owned identity, and the 2026-08-15 design's D8 already puts a continuing delegation on a controller session rather than on a run or a lineage edge                                                                                                                           | Recovering by lease handle (previous D3), which a lost context has nothing to present. And reconstructing children from the transcript or from lineage: lineage proves provenance and confers no authority                                                                                                                                                                                                                                                                                                                   |
| D4  | **Failure is CLOSED: no enrolment, no orchestration, and the pane says so**                                                                                                                                                                                                                                                                                                                  | The `ssh` path's fail-open, where every refusal runs the user's line conventionally (`nocx.bash:721`, and `lifecyclepub/publisher.go:307` documents the empty bootstrap as that contract). Correct for an optional enhancement, fatal for an invariant: one hook error, one unknown flag, one dead channel and an entirely ordinary, uncounted agent exists. Optional integration and invariant-bearing enrolment need opposite failure policies                                                                             |
| D5  | **Delivery is an ALIAS to a launcher that `exec`s the agent.** nocx defines the alias in the panel session it already composes; the launcher resolves the agent adapter, requests enrolment over the local socket, pins the enrolment to ITS OWN pid, stages config in temporary files, and `exec`s the real agent — `exec` preserves the pid, so the pin survives and the launcher does not | Extending the shell's nested-environment detector to agent names. Measured false: `internal/shellintegration/scripts/nocx.bash:578` recognises only `ssh`/`sudo`/`su`, only while the lifecycle channel is active, and `internal/ssh/typed_wrap.go:30` keeps the user's own argv rather than rewriting it. There is no generic argv-rewrite seam to extend, and agent launches add aliases, absolute paths, `env FOO=… claude`, wrappers, shell functions, version managers and vendor flags that no such detector can cover |
| D6  | **No vendor-specific route carries anything required.** Hooks and a vendor inbox socket may make a wake cheaper or faster; the invariant rests on neither                                                                                                                                                                                                                                    | Making a per-agent hook API load-bearing. It multiplies integration cost by the number of agents and by their versions. `nocx-dkawo.5` costs the two concrete accelerators and finds each disqualified as a carrier by the vendor's own documentation — see §7.4                                                                                                                                                                                                                                                             |
| D7  | **Mail rides our own calls and is acknowledged explicitly.** A report, a wait or an explicit inbox check returns pending mail                                                                                                                                                                                                                                                                | Piggybacking mail on the result of ANY tool call via hooks. Agent-specific and unverified; schemas and size bounds differ; delivery after a destructive call is already too late; a retry redelivers; parallel calls make order ambiguous; and unrelated tool output becomes a covert channel for coordination data                                                                                                                                                                                                          |
| D8  | **Four acknowledgements are distinct facts and are never merged**: committed to the mailbox, fetched by the participant, present in the model's context, acted upon. The cursor advances on the second                                                                                                                                                                                       | One "delivered" state. Collapsing them is how consumed mail became invisible in the measured failure                                                                                                                                                                                                                                                                                                                                                                                                                         |
| D9  | **Only PROCESS EXIT and the participant's own DECLARATION may decide wave state.** Process exit is ours because nocx owns the PTY (`internal/procwatch` locally, the channel's exit status for a remote pane); the declaration arrives over the authenticated channel of ADR-0024 decision 2. **The screen decides typing and lighting, and nothing else**                                   | The 2026-08-15 spec's `observed` row, which also lists alternate screen, blocked-on-stdin and silence. ADR-0024 decision 1 forbids any sequence parsed from the byte stream — "standard OSC, private OSC, DCS, title, terminal mode, or anything else" — from opening or completing an execution attempt. Alternate screen and the title are exactly that. Silence is not a state. Blocked-on-stdin is not trustworthily observable from a PTY. See §8.1: this obliges an amendment to that spec                             |
| D10 | **Star topology now; participants are addressable nodes from day one**                                                                                                                                                                                                                                                                                                                       | Mesh at stage 1. Under mesh the coordinator is mirrored on MUTATIONS OF THE RECORD — file ownership, task scope, participant set — and never on message content. Content is theirs; commitments are his. This matches the repo's own `TRUST NOTHING, DIFF EVERYTHING`: a coordinator that judges the diff does not need the conversation, but it does need to know when the conversation changed a fact it owns                                                                                                              |
| D11 | **The harness's built-in subagent tool is NOT restricted**                                                                                                                                                                                                                                                                                                                                   | Denying it with a `PreToolUse` hook. Considered and dropped: it blocks, therefore it does not have this defect, therefore banning it solves nothing and costs the cheap uses (search, reconnaissance)                                                                                                                                                                                                                                                                                                                        |
| D12 | **A wait-for cycle is a SUSPICION that must be corroborated, not a proof**                                                                                                                                                                                                                                                                                                                   | "Both waits are our own edges, therefore the deadlock is proved." A wait may be cancellable, an OR-wait, bounded by an external or human event, stale after a restart or attempt replacement, already satisfied by a durable-but-unreduced mailbox event, or owned by a process that has exited. A cycle proves deadlock only if every edge is current, exclusive, necessary, unsatisfied and attached to a live attempt with no external resolver                                                                           |
| D13 | **The backend keeps a LIVE GRID for a participant pane**, fed from byte zero of a process nocx launched, from enrolment to the terminal record and then discarded. It may decide exactly two things — may nocx write to this pane, and what the indicator shows — and never wave state, a lifecycle attempt or an execution attempt                                                          | A stateless transformer over the tail of the stream, which the first draft claimed and which is false: a TUI sends diffs, not full repaints, so a frame cannot be reconstructed from a tail. And leaving the grid in the renderer, which fails the moment the window is shut — the case this whole design exists for. This crosses AD-6 and ADR-0002 (§2); the boundary is `nocx-szb40.2`'s deliverable                                                                                                                      |
| D14 | **The backend wakes the coordinator by TYPING into its pane, and only when the driver says it may.** Locally the backend types; on a remote host the helper types on the far side, so nothing leaves the machine. Delivery is UNACKNOWLEDGED: a wake never closes a fact, only the coordinator's own subsequent call does                                                                    | Sending Escape first to make a mistimed keystroke recoverable. Strictly worse than refusing: a mistimed keystroke does not merely fail to deliver, it ANSWERS whatever modal is on screen, which can approve a tool call the user never saw. Type only on positively identified `free_text`; a permission menu, a spinner or an unknown state receives nothing at all and the refusal is recorded with its reason                                                                                                            |
| D15 | **One worker first, not three.** Three is the demonstration; one is the mechanism, and fan-out is cheap once one works                                                                                                                                                                                                                                                                       | Building the routing table and the N-worker queue with the record. `nocx-dkawo.4` is where fan-out and the escalation policy live, and it is deliberately after the mechanism rather than inside it                                                                                                                                                                                                                                                                                                                          |

## 5. The invariant

1. **Supervision is the backend's, not the agent's** (D1). The wave cannot come into existence
   unsupervised, and no act of the coordinator's is required to keep it supervised.
2. **The record outlives the coordinator**, and a restarted coordinator asks what its session
   holds and is told by name (D3).
3. **A fact that needs judgement reaches somebody within a named deadline** — the coordinator by a
   wake, the human if the coordinator does not dispatch it (D2, D14).
4. **Failure is closed** (D4).
5. **Three deterministic levers decide wave state, and only three**: what we launch the agent
   with; what the PTY gives us as a process fact; what the participant declares over the
   authenticated channel. The backend's grid is a fourth source and is **not** one of these levers
   — it decides typing and lighting (D9, D13). MCP surfaces, skills, tool descriptions and prompt
   text are PERSUASION: they raise probability and cannot carry an invariant.

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
- **Undispatched facts and their deadlines** (D2). A fact enters when it needs judgement and
  leaves when the coordinator dispatches it; its deadline is what escalates it to the human. This
  is the slot the lease used to occupy, and the difference is the point: it is a property of the
  WORK, so an empty set means no timer is running at all.
- **Terminal facts**: what each participant declared it produced, and its process exit.

### 6.1 Evidence, by what it may decide

**Half of this table is not this document's to write, and an earlier revision wrote it anyway.**
"What may reach you, and by which channel" already has an owner: `internal/notify`'s `Trust`, one
of three classes stamped by the source adapter, never carried on the wire, enforced default-deny
against a routing table that refuses at construction to give a heuristic row a sink which leaves
the machine (`notify.go:355`, and ADR-0047 §3 — the one titled "A program may ask; it never
chooses", which has to be named by title because a second accepted decision also numbered 0029
exists in `docs/decisions/`; that is `nocx-jp0h3`). The previous revision invented a parallel "may wake
your phone" axis with its own vocabulary and its own answers — including "per-agent opt-in only",
which the shipped model does not have and does not need. That is the second-owner defect this
repository has paid for repeatedly, in a document whose §8 records two instances of it.

So the columns are split by owner. What an evidence class may decide about the WAVE and about
TYPING is this design's, because nothing else has those concepts. What it may reach is a mapping
onto `Trust`, and where the two disagree, `Trust` wins, because `Trust` is enforced in code:

| Class                | Source                                          | May decide wave state | May decide whether nocx types | `notify` Trust         |
| -------------------- | ----------------------------------------------- | --------------------- | ----------------------------- | ---------------------- |
| `known`              | our own loop (ADR-0028)                         | yes                   | yes                           | `attested`             |
| `declared`           | the participant, over the authenticated channel | yes                   | yes                           | `attested`             |
| `observed`           | **process exit only** (D9)                      | yes                   | yes                           | `attested` — see below |
| `declared-anonymous` | terminal title, OSC 0/2                         | **no**                | **no**                        | `heuristic`            |
| `inferred`           | the backend's grid (D13)                        | **no**                | yes — this is its job         | `heuristic`            |

One mapping is a better answer than the one it replaces, one corrects an error this revision made
on its way here, and one is an open question.

`inferred` is "an inference from stream content", which is `heuristic` verbatim, and the machine
boundary the old row asserted in prose is the one the routing table already refuses to cross.

`declared-anonymous` looked like `programRequest` and is not, which is worth recording because the
mistake is the natural one: a title IS a sequence the program printed. But `programRequest` is for
a program **asking to notify you** — `KindBell` is BEL, and `ws_notify.go:173` stamps a raised
request — whereas a title is a thing the program set for its own reasons, from which we infer
something it never said. The shipped code settles it without ambiguity: `KindPaneWorkFinished` is
commented "title-transition inference (heuristic)", and `ws_notify_pane_work_finished.go:24` says
`TrustHeuristic` "is what confines the event to local attention". So the title row is `heuristic`,
and this table has no `programRequest` row at all — correctly, because a program requesting your
attention is not evidence about a wave, and the two axes should not be merged just because both
end at you.

**`observed` is the open one:**
process exit originates at a backend boundary that owns the PTY, which reads as `attested`, and
the previous revision nonetheless wrote "may wake your phone: no" with no argument for it. Either
it is attested and may, or there is a reason it should not be, and that reason belongs in writing
rather than encoded as a silent no. `nocx-dkawo.4` is where it gets decided, because that is where
what reaches the human is measured rather than assumed.

The `inferred` row is where this revision differs most from the last one. The previous draft
argued that the indicator was legal _because_ AD-6 binds the backend and the renderer owns VT
state, pointing at `frontend/src/agent-status.ts`, which already derives `working | idle` from the
title. That argument no longer holds and is not patched: the backend now keeps a grid of its own
(D13), which is a real crossing of AD-6 with a written boundary, not a loophole. What survives
from the old argument is the _shape_ of the limit — `agent-status.ts` answers only whether
SOMETHING is working, never WHO, so it stays on the `declared-anonymous` row by construction, and
ordinary tabs keep using it untouched. That module is also no longer bare: `pane-work-finished.ts`
(`nocx-n3nfg`) already wraps it in a settle window on the `working → idle` edge and raises
`pane.workFinished` at `trust: heuristic` — so the provenance this design asks for partly exists
already, on the shipped model rather than on a new one.

The grid earns the strongest thing it is allowed to decide, and it is not wave state: it is
**whether nocx may type into this pane at all** (D14). That is the authority a screen reader
genuinely needs and the one it can genuinely carry, because being wrong costs a refused keystroke
rather than a false completion.

herdr's indicator lives on the weakest row: twenty TOML manifests matching literal UI copy against
screen regions, refreshed from a network catalogue. Ours does not need to, because a participant
that was handed a tool can simply say. Screen matching remains available for what we failed to
integrate, and never wakes anything.

The indicator itself is `nocx-szb40`'s deliverable, and `nocx-dkawo` now depends on it — the edge
between the two epics was reversed in this revision, because the wave consumes the driver. This
design's obligation is only to keep observations separable, provenance-tagged and attempt-bound,
so that a late observation from a previous attempt cannot overwrite a current one.

## 7. Delivery: how the tool reaches the agent

### 7.0 Two callers, one dispatcher

The 2026-08-15 design's §6 has ONE dispatcher and TWO callers, differing **only in how the grant
was sourced**: the built-in agent runs in-process with a grant minted per run, and an enrolled
external CLI reaches the same dispatcher over a private local channel. Everything in §4 through §6
is the dispatcher's and is therefore identical for both — the same record, the same mailbox with
its non-consuming reads, the same evidence classes, the same per-fact deadline. What follows in
§7.1–§7.4 is almost entirely the external caller's, and it is worth saying plainly which parts the
built-in agent does not need, because the asymmetry is easy to mistake for two mechanisms:

- **Enrolment, the alias and the launcher (§7.1, D5) do not apply.** There is no process to pin and
  no argv to reach, because the loop is ours (ADR-0028) and the grant is minted per run. D4's
  fail-closed still binds, in its in-process form: no grant, no spawn.
- **The keystroke wake (D14) is not the built-in agent's route.** Typing into a pane is the general
  mechanism precisely because it needs no cooperation from a vendor we do not control; where the
  loop is ours, a fact is delivered into it directly, with none of the modal hazard that makes D14
  refuse anything but `free_text`. D14 is what the general case costs, not what the mechanism is.
- **The grid (D13) applies to a PARTICIPANT pane regardless of which caller spawned it.** It is
  scoped to what nocx is observing, not to who asked; a built-in agent supervising an external
  worker still needs it, and an external coordinator supervising one needs the same grid.

So the invariant of §5 is the dispatcher's, and it holds for both callers by construction. The
external caller is the harder one and the rest of §7 is written for it; nothing in it is a second
design.

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

Because nocx launched the process itself, the grid of D13 starts at byte zero and never has to
guess at state accumulated before it was watching. A pane **adopted** rather than launched has no
such guarantee: its state is `unknown` until a full repaint has been observed, and `unknown`
refuses typing. herdr hit the same wall on server handoff and answers it with an app-safe resize
nudge (`headless.rs:341`) — usable when adopting, never as the mechanism.

The alias is itself a visible, removable footprint, which satisfies `nocx-bu6q` without separate
work; consent is asked at the feature, not at the connection (`nocx-8d6e`).

### 7.2 The calls that must exist

- **One spawn primitive.** It may block and return at decision points — the first participant
  settles, one asks, one exits, a corroborated deadlock — but **nothing rests on it any more.**
  In the previous revision the blocking call, and then the lease, were the mechanism; now they are
  a convenience over a record the backend is watching regardless, and a coordinator that never
  calls back loses nothing except its own promptness.
- **Say to a participant.** Star policy admits one legal recipient today; addressing is real from
  the first day (D10).
- **Report, structurally.** Completion is what a participant SAID; process exit corroborates it,
  and neither alone terminalizes the participant. This is what kills the self-matching sentinel:
  there is nothing on a screen to match.
- **Check the inbox**, explicitly, with explicit acknowledgement (D7, D8).
- **Ask what my session holds** (D3), which a coordinator with no context can call.

Every response carries a cursor, and the coordinator acknowledges the cursor together with the
effects it commits from that response — otherwise "read consumes nothing" prevents loss but not a
duplicated spawn, interrupt or scope mutation on retry.

### 7.3 The asymmetry, narrowed rather than hidden

Worker → coordinator is reliable: the fact enters the record, and the backend either returns it
into an outstanding call or wakes the coordinator for it (D14).

Coordinator → worker was, in the previous revision, purely a wait for the worker's next call, "and
there is no mechanism here that makes a busy agent read sooner". D14 narrows that, because the
typing primitive is general — `nocx-dkawo.1` is _nocx types into a pane_, not _into the
coordinator's pane_. It does not close the gap: the driver refuses a busy pane by design, so a
worker mid-turn still reads at its next call, and delivery remains unacknowledged in both
directions.

**A real interrupt is a separate, coarser operation** with its own effect class: it names a target
process group, has an escalation sequence, records an attempt, and confirms what it terminalized.
It is not message delivery wearing a different hat. Signals and keystrokes have ownership
consequences — `SIGINT` reaches the TUI or its foreground child depending on process groups, a
keystroke can answer a prompt instead of interrupting, and an interrupt during a tool call can
leave the filesystem half-mutated.

### 7.4 Accelerators, and why neither is the carrier

D6 says no vendor-specific route carries anything required. Two concrete ones are worth having
anyway, and `nocx-dkawo.5` measures both against the general route rather than assuming them.

Claude Code binds a per-session inbox socket, exports its path and token to that session's own
children, and documents exactly the delivery rule this design wants: a message is read between
tool calls during an active turn, and **when the session is idle Claude Code starts a new turn
with it**. No screen reading, no modal hazard, acknowledged by the vendor. It still cannot carry
the invariant, for four reasons that are all in the vendor's own documentation: it exists for one
agent; it is behind a feature flag that several ordinary privacy settings switch off; between
machines it travels through the vendor's servers rather than ours; and the receiving session's
inbound controls can hold or drop a message, which for a session that bypasses permission prompts
is the DEFAULT unless we stage `crossSessionInbound: accept` ourselves. Three documented traps
come with it: identical repeats inside a short window are dropped, so every wake must be distinct
and coalesced; a wake counts as usage like a typed prompt; and bare-mode headless sessions bind no
socket at all.

A `Stop` hook is the other accelerator and much weaker than it looks: it fires when a turn ENDS,
so it can stop a coordinator falling asleep with unread mail and can never wake one that is
already asleep. In a wave the facts almost always arrive after the coordinator has gone idle, so
it catches a narrow race and not the case.

## 8. What the design changed, twice

**Round one, 2026-08-24: codex reviewed the first draft against the tree** and found eighteen
items. Items 1–3 are AUTHORIAL ERRORS — the draft stated things about this repository that are
false — and they are recorded because that mistake is cheap to repeat: all three came from writing
about a mechanism the author remembered rather than one he had read.

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
4. **The blocking call closes a moment, not an interval** (design defect). This is the AGENTS.md
   rule the draft quoted at its reviewers while breaking it. Replaced at the time by the lease —
   see round two.

A fifth finding, that `frontend/src/agent-status.ts` answers only whether SOMETHING is working and
never WHO, is folded into §6.1. The remaining findings are either folded into the decisions above
(D2, D7, D8, D9, D12) or recorded as open questions in §10 — none is discarded silently.

**Round two, the same day, working the constraint out with the owner: the lease went too.** The
replacement for finding 4 named the interval's ends honourably and still asked the coordinator to
renew it, which is an act the agent must remember — a poll under another name. The argument that
killed the blocking call is general, and it does not stop at any upstream improvement: a durable
cross-turn monitor still has to be armed. So supervision left the agent entirely (D1), and the
mechanism became the backend's record plus a wake (D14) plus a per-fact deadline (D2).

That answer reached further than the epic that asked for it, and two of its consequences were
already defects nobody had filed. A backend that watches while the window is shut is a backend
that outlives the window: the replay ring is 256 KB (`internal/transport/ring.go:14`) and its own
comment says "The ring is NOT scrollback — the frontend owns the scrollback (AD-6)"
(`ring.go:38`), so closing the webview destroys output that never existed anywhere else — now `nocx-22k1c`. And the frontend owns the terminal size, so a session
with no client has none at all; the answer taken is herdr's (the client that attached last defines
it) and pointedly not tmux's smallest-common, because the minimum makes every client wrong except
one.

Two proposals were costed and rejected rather than deferred. **A sidecar supervisor process per
participant**: its three benefits are either unnecessary (descendant capture, item 3, now out of
scope), already ours (process exit, because nocx owns the PTY), or obtainable without it (the
2026-08-15 design's D13 pinned handle, because `exec` preserves the pid). **Running workers
headless behind our own loop and projecting each into a pane**: it breaks subscription auth and
the vendor TUI, which are the two fixed constraints of §3.1. It is recorded because it is
genuinely the strongest design on every axis except those two.

### 8.1 An amendment this design owes another document

The 2026-08-15 design's §5.2 puts alternate screen, blocked-on-stdin and silence in the `observed`
class and lets that class decide. ADR-0024 decision 1 forbids exactly that for anything derived
from the byte stream, terminal modes and the title included. That spec is not approved; the
contradiction should be resolved there rather than worked around here. D9 states this design's
position, and the contradiction is filed as `nocx-cdy3v`.

## 9. Deliberately out

- **Fan-out to N, the routing table and the escalation policy** — `nocx-dkawo.4` (D15). One worker
  is the mechanism; three is the demonstration.
- **Capturing an agent that another agent spawned around the tool.** Not caught, and not pretended
  to be caught.
- **A sidecar supervisor**, and any per-participant helper process (§8).
- **Restricting the harness's own subagent tool** (D11).
- **The relay, durable sessions and reattach** — `nocx-1p1h4`, blocked behind the helper reaching
  `main`.
- **Worktree creation as one call** — `nocx-kdccc`.
- **Durable output capture** — `nocx-22k1c` — and **session-owned size** — `nocx-eidfb`. This
  design made both urgent (§8) and neither is built here. Note that they land on opposite sides of
  ADR-0002: `nocx-eidfb` is careful to keep it intact, because two clients seeing the same bytes at
  the same size need no server-side grid, while D13 crosses it for the participant pane alone.
- **The declarative plan and its executor** — `nocx-d6gn4`. A wave becomes an effect in that
  kernel; this design does not build a second one.
- **The three ambiguous UI origins of a lineage edge** — `nocx-ebl4.1`.

**No longer out:** agent lifecycle classification and the indicator (`nocx-szb40`) were listed as
out of scope in the previous revision. D13 and D14 make the driver a dependency of the wave, not a
neighbour of it, and the epic edge was reversed accordingly.

## 10. Open questions

Each is a real hole. Those that have become beads say so — a question that lives only in a
document is a question that evaporates between rounds.

1. **Delegation and authority** — `nocx-z3nkr`. D8 of the 2026-08-15 design gives a controller
   SESSION a revocable delegation over `observe`, `receive-events`, `send-input`,
   `delegate-further`, `close`; this design says "addressable participant" and names none of it.
   Takeover, re-credentialing, domain change and coordinator replacement currently have no defined
   effect on who may address whom. D3 makes this sharper rather than softer, because it now hangs
   recovery on the session too.
2. **Spawn as the `delegate` effect, and the run grant** — `nocx-z3nkr`. ADR-0020 requires an
   immutable grant on each run; the 2026-08-15 D9 permits spawning only into an environment
   already in scope. The spawn primitive here is described without either, which risks encoding
   the wrong principal in the record and the API before authority is added.
3. **Enrolment versus injection.** The 2026-08-15 D13 requires peer identity, foreground
   process-tree membership, a non-reusable pinned handle and human approval. D5 pins to the
   launcher's pid, which is necessary and not obviously sufficient: what binds every later request
   to that tree, and what stops another process in it from impersonating the participant?
4. **ADR-0028 narrowing for the external caller** — `nocx-z3nkr`. Every mailbox read, send, spawn,
   interrupt and record mutation should hold a capability incapable of naming anything outside its
   delegation. Described here as one shared surface, which is the ambient dispatcher API that
   ADR-0028 rejects.
5. **Spawn-and-register has no stated transaction** — `nocx-u0apy`. What is true on disk, in the
   record and in the process table when step 3 of 5 fails; whether recovery adopts the pinned
   process or terminalizes it; and the interval within which a participant record must exist —
   from before the first irreversible spawn effect until the enrolled incarnation is supervised or
   the record is durably terminal.
6. **Cursor lifecycle.** Who mints a reader id; whether a restarted coordinator is the same reader
   (D3 says the session is the identity, which is an answer for the record and not obviously one
   for the cursor); what happens to an abandoned cursor; what a compacted mailbox returns; whether
   mesh implies N² cursors.
7. **Does the record survive a BACKEND restart?** This is now the sharpest of the nine, because D1
   put supervision in the backend: "outlives the coordinator" is what D1 buys, and "outlives the
   backend" is a different and larger claim. The 2026-08-15 D5 says stage-1 workers die with the
   backend, which is coherent — if the workers die, a record that outlived them would describe
   nothing — but it means a backend crash is an unsupervised interval by construction, and this
   draft has not said what the human is told when it happens.
8. **What the deadline actually is** (D2). A per-fact deadline needs a number, and a number that
   is wrong in either direction breaks it: too short and a thinking coordinator is escalated past;
   too long and the human learns late. It probably differs by fact class — a crash is not a
   completion — and `nocx-dkawo.4` is where that gets measured rather than guessed.
9. **Bounds.** Participants per wave, nesting depth, waves, mailbox bytes, message size, retained
   cursors, wait edges, declaration rate, and now grids per backend (D13, one per participant pane
   fed continuously). §11.6 of the 2026-08-15 design already has lineage depth, fan-out and
   fork-bomb resistance open, and this builds on that edge.
10. **What mesh actually needs**, beyond addressable participants: sender authentication, causal
    ordering, mutation arbitration, revocation, loop prevention, dead-letter handling — and the
    atomicity hole where a message promises a mutation that then fails to commit.

## 11. Assertions

Written as assertions rather than prose, per AGENTS.md's cheapest defence against a test that
encodes the implementer's model.

1. A coordinator in a nocx pane starts a worker, gives it a task, waits, reads what it produced,
   and closes it — with herdr not installed and not running.
2. The wave cannot be created without a backend record, and a spawn attempted without one produces
   no child process at all.
3. **The coordinator goes idle with the worker still running, and the wave stays supervised: no
   alarm is raised, and no timer is running while there is nothing undispatched.**
4. A coordinator killed between two turns does not lose the worker; the worker's next fact is
   still recorded, and reaches the human within the named deadline because nobody dispatched it.
5. A coordinator restarted with no context asks what its session holds and is told its children by
   name.
6. **An idle coordinator is woken when a worker declares completion, and takes a turn without the
   user touching anything.** The wake alone does not clear the fact; the coordinator's own
   subsequent call does.
7. **A pane showing a permission menu, a working spinner, or an unknown state receives nothing at
   all, and the refusal is recorded with its reason.** A pane nocx did not launch receives nothing
   until a full repaint has been observed.
8. **No wave state, lifecycle attempt or execution attempt can be opened, completed or altered
   from the grid** — asserted directly, not inferred from the absence of a call site.
9. A launcher whose enrolment fails does not exec the agent, and the pane says the agent is not
   orchestrated. No wave record is created, and no participant appears.
10. Two readers read the same mailbox; neither loses a message, and the cursor of one does not move
    because the other read.
11. A message committed to a mailbox, but never fetched, is reported as committed-not-fetched, and
    is not reported as delivered.
12. A worker's report is accepted while its process is still alive, and its process exit is recorded
    separately; neither alone terminalizes the participant.
13. A title containing a braille spinner changes the indicator and changes NO wave state; a title
    engineered to look like a completion wakes nothing.
14. A wait-for cycle whose edge is already satisfied by a durable mailbox event is not reported as
    a deadlock.
15. The alias is removable, and after removal `claude` runs ordinarily and is marked not
    orchestrated.
16. Closing the frontend does not stop the participant's grid being fed, and does not stop the
    backend recording the worker's output.

## 12. What would falsify this design

The 2026-08-15 thesis is "orchestration is a view over sessions, not a second product", and its own
falsification list includes transactional fan-out/fan-in, dependency-aware retries, and scheduling
work when no session yet exists. **A backend-owned wave record is fan-out/fan-in.** That spec says
the first such need is the signal to revisit the thesis; this design claims the trigger has fired
and says so here rather than passing quietly. ADR-0002's revisit trigger has fired for the same
reason (§2), and that one is owed an amendment to `docs/architecture.md`, not a note.

**One falsification from the previous revision has already fired, and is left here as the record
of it:** _"If supervision as a lease turns out to need renewal so often that it becomes a poll, it
is a blocking call with extra steps and D1 bought nothing."_ It did, and it was — the renewal was
recognised as a poll before it was ever built. The test that caught it was not a measurement but a
question: what act is the agent required to remember?

What would falsify THIS design:

- **If the driver cannot positively identify `free_text` often enough**, D14's wake is unavailable
  in practice and every fact escalates to the human. `nocx-szb40.1` is deliberately the first task
  in the tree for this reason: it measures the grid against real captures before anything is built
  on it.
- **If the escalated fraction is high** — if most facts reach the human rather than the
  coordinator — then the mechanism moved the work to the human and should say so out loud rather
  than be described as orchestration. `nocx-dkawo.4` measures this and reports it; it is the
  number the whole design is judged by.
- If a per-fact deadline turns out to need a timer running most of the time anyway, D2's advantage
  over the lease was bookkeeping rather than substance.
- If the per-agent adapter cannot be written for two agents without a shared shape, D5's AD-8 seam
  is a fiction and the launcher is twenty manifests wearing a Go interface.
- If a launcher that refuses visibly is routinely worked around by typing the agent directly, then
  fail-closed was not acceptable to the user and D4 must be re-argued rather than re-implemented.
- If a backend restart during a live wave turns out to be common, D1 put supervision in a process
  that is not durable enough to hold it, and open question 7 becomes a decision rather than a
  question.
- If most real waves turn out monadic, the token cost falls back onto the coordinator's turns and
  the free-coordinator constraint from §3.1 is not met by this mechanism alone.
