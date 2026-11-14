# ADR-4khgzeyp — An agent process lives for its conversation, and the agent ends its turn

- **Status:** accepted (2026-11-14)
- **Date:** 2026-11-14
- **Taken from** `nocx-a62cl` stage 4b-1 (`nocx-a62cl.14`), the owner's decisions of 2026-11-14:
  «Значит нужно остановиться, сделать шаг назад и перепроектировать» and, shown the options
  below, «Переделывай, как у других».
- **Supersedes, in ADR-anntnxdj:**
  - decision 1: the "One agent process per turn, one conversation on disk" paragraph, and the
    contract comment that `Wait` returns when "the process and its bridge are gone" — `Wait`
    returns when the turn has ended (decision 2 below);
  - decision 3: the "A third way in, for one turn" bullet's per-turn bearer, slot and reconnect,
    replaced by decision 5 below; and "A cancelled turn closes its connection, which ends what is
    left", replaced by decisions 3 and 5 below;
  - the Constraints row "No process outlives its turn" and the one-second cancellation bound of
    scenario 3 and its Constraints row, replaced by this record's Constraints;
  - the Consequences sentence that each question costs about half a second to start the agent on
    pi, and decision 8's explanation of the cache in terms of a process per turn — its obligation
    that the model input reproduces across turns stands, and now covers session restarts.

  Everything else in ADR-anntnxdj stands: the contract's shape, the authority pipeline, the
  approval service with its immediate decline on a person's message, cancel per call, steer as a
  capability, decision 5's "A turn ends with the connection that started it" and "A dead backend
  takes its agent with it, through the pipes", and decision 8's cache prefix and model pinning.

## Context

ADR-anntnxdj chose one agent process per turn, so that a turn's process, MCP connection, bearer,
grant and lifetime were one unit and a late call from a cancelled turn could not reach the next
turn's grant. Building it (`internal/harness`, the pi connector) made the turn's outcome depend on
three parties at once: the agent's own end-of-turn message, the process's exit status, and a
cancellation that killed the process. Seven review rounds of the pi connector each closed the
races reported and found new ones at the same seam: an End published before the exit verdict, a
cancel that lost to a decoded end, an abort written before the prompt it should have stopped, a
blocked publish that stalled cleanup, per-turn files swept under a starting child. The inventory of
the code that produced them: about ten goroutines per turn and three independent sources of truth
(the base's mutex, the connector's atomic state, the connector's write lock) plus channels between
them.

What the rest of the practice does, read from primary sources on 2026-11-14:

|                              | Process                               | End of a turn                                | Cancel                                                                       |
| ---------------------------- | ------------------------------------- | -------------------------------------------- | ---------------------------------------------------------------------------- |
| ACP v1 (`prompt-turn.mdx`)   | per connection, many sessions         | the `session/prompt` response's `stopReason` | `session/cancel`; the turn still ends with that response, `cancelled`        |
| pi `--mode rpc` (`rpc.md`)   | one process, many prompts in sequence | `agent_settled`                              | `abort`, answered when the session is idle                                   |
| Codex app-server             | long-lived, many threads              | `turn/completed{status}`                     | `turn/interrupt`, answered once the turn is aborted                          |
| Claude Agent SDK             | one CLI process per session           | `result` / `session_state_changed: idle`     | `interrupt()`; SIGTERM records no result                                     |
| Zed, paseo, agent-of-empires | one process per session or longer     | the agent's message                          | protocol cancel; kill after a bound (≈10 s in agent-of-empires), then resume |

None of them runs a process per turn, none treats process exit as the end of a turn, and each
gives a session one owner — a serial queue or an actor — with a run token so a stale cancel cannot
settle a newer turn.

## Decision

### 1. One agent process per conversation, one owner per session

A conversation's agent process starts with its first turn and serves the turns after it in
sequence. It ends when the window that owns the conversation closes, when the backend stops, when
the session is idle for an hour (idle: no turn running or cancelling, no accepted tool call, no
approval pending), or when the session is restarted after a failure. A session is owned by one
actor: a single goroutine that holds the session's state, writes every command to the agent, reads
every line from it, and is the only party that moves the session or a turn from one state to
another. Its pipe reads and writes and its deliveries to the consumer are interruptible, so the
actor can always act on a cancel, a kill or a deadline. Connectors keep no lifecycle state: they are
a protocol — how to start the agent, how to encode a prompt, an abort and any setup command, and how
to decode a line into events and command responses. The actor gives every command it writes an id
of its own and matches responses to it; the agent's events, which carry no command id, belong to the
one turn the actor has running in that process incarnation, and nothing else.

### 2. The agent ends its turn

A turn ends with the agent's own end-of-turn message (pi's `agent_settled`, ACP's prompt response,
Codex's `turn/completed`, Claude's `result`), decoded by the connector into one terminal: end,
failure, or cancelled. An end-of-turn message is not success by itself: a turn whose final message
or provider reported an error is a failure (pi: the last assistant message's stop reason and
error). A prompt the agent reports it did not run (pi's `disposition: "handled"`) ends the turn at
once as a failure that says so. The actor publishes the terminal on the same ordered stream as the
turn's deltas, after every tool call the endpoint accepted for the turn has settled, and `Wait`
returns exactly that terminal. Process exit is never the end of a turn: an agent that exits or is
killed during a turn fails that turn — the actor publishes that failure itself — and the session,
and the next turn starts a new session that resumes the conversation from disk. When a cancel and
the agent's own end race, the first one the actor takes in its loop decides: an end decoded before
the actor took the cancel stands; a cancel taken first makes the turn's terminal cancelled whatever
the agent says after it.

### 3. Cancel is a protocol request, and kill is its fallback

Cancelling a turn makes the actor stop writing that turn's work (setup commands, prompt) and send
the connector's cancel sequence — for pi, `clear_queue` and then `abort`, which pi answers once the
session is idle and its turn persisted — and wait for the turn's end message. If none arrives
within ten seconds, the actor ends the process group. Either way the turn is published as
cancelled, and **a cancelled turn ends its session**: after the agent's end message the actor closes
the agent's input and lets it exit (killing it at the same bound), the session's bearer is retired
and its connections closed, and the next question starts a new session that resumes the
conversation from disk. A cancel is the one case where an agent abandons tool calls it already sent
(on a normal end it has waited for every result before ending its turn), so ending the session is
what keeps those abandoned requests away from the next turn. It costs one agent start per cancel. A
person's message the agent had queued for steering when the turn was cancelled is not lost: it
becomes the next question, as ADR-anntnxdj decision 4 says for an agent without a steerer.

### 4. One turn at a time

The actor admits a new turn only after the previous one has published its terminal and the endpoint
has settled every call of it — answered, or cancelled per call — whether the turn ended by the
agent's message, by a cancel, or by the process dying. Agent output while no nocx turn is running
(work a person's own pi extension starts by itself, for example) is not shown and its tool calls are
refused (decision 5); output that cannot belong to any turn is a protocol failure of the session,
which ends it.

### 5. The bearer belongs to the session; a connection belongs to a turn

The session's bearer is minted when its process starts and handed to the bridge the agent launches;
a new session gets a new bearer, and ending a session retires its bearer and closes its connections,
so nothing of an ended session reaches the endpoint again. Admission keeps ADR-anntnxdj decision 3's
uid check, the assistant's chain and a slot of its own. A connection is bound, at admission, to the
turn running then — its grant, pane access, attachments and approvals — and is never rebound; a
connection admitted while no turn is running is refused. At a turn's end the endpoint settles that
turn's calls and closes the turn's connections; the bridge reconnects for the next turn's first
call. A turn that ends normally leaves no request of it in flight, because the agent waited for
every result; a turn that is cancelled ends its session (decision 3). Together: a call is executed
only under the turn whose connection carried it, and an approval's answer and its fresh grant stay
pinned to the call that asked.

### 6. Delivery

Deltas are delivered in order, and a consumer that stops reading pushes back on the turn, as pi,
Claude and Codex over stdio do; the push-back never holds the actor: a cancel, a kill and a deadline
are acted on while a delivery waits, and the terminal is always deliverable.

### 7. What the prompt and the model keep

nocx's system prompt is appended to the agent's own on every run, once, byte-identical (pi's
`before_agent_start` returns the agent's prompt for that run plus nocx's fixed text), with the
person's instructions inside it. A conversation's model is pinned at its first turn (ADR-anntnxdj
decision 8) and carried on every later turn and every session restart; a changed role or default
applies to new conversations. The model input of a conversation's later turn reproduces the earlier
one's across an idle restart, a crash and a cancel, within the cache loss ADR-anntnxdj decision
8 allows after a cancellation; a session that cannot be resumed is reported as lost, never presented
as resumed.

## Consequences

- The 0.4–0.6 s pi start is paid once per conversation instead of once per question, and the agent's
  own state (its session, its loaded model) is kept between turns.
- A live process per open conversation holds memory between questions; the idle bound limits it.
- A crashed, killed or cancelled session is restarted on the next turn and resumes from its session on disk;
  the block says the session was restarted, as ADR-anntnxdj decision 5 says for a lost session.
- Per-turn files, the per-turn bearer and the exit-status verdict go, and a connection now lives for one turn while the bearer lives for the session; the stage's code that
  implements them is replaced inside the same stage (`nocx-a62cl.14`).
- An ACP connector later maps directly onto this shape: one ACP connection per agent process,
  `session/prompt` per turn, `session/cancel` as the abort.

## Constraints

| Requirement                                                                                                 | Check                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| ----------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| One party moves a session's and a turn's state                                                              | review: the base exposes no lifecycle state to connectors; a connector package holds no mutex, atomic or channel for turn or session state                                                                                                                                                                                                                                                                                                           |
| Each turn publishes exactly one terminal, after its deltas, and `Wait` returns that terminal                | randomized interleaving test in `internal/harness` (2000 seeds) over decoded end, decoded failure, cancel, process exit, kill, consumer stall, blocked stdin, full stdout, duplicate and stray responses; asserts exactly one terminal per turn, terminal last in order, `Wait` equal to it                                                                                                                                                          |
| A cancel taken before the agent's end makes the turn cancelled; an end taken before the cancel stands       | same test asserts the precedence by the order the actor took the two                                                                                                                                                                                                                                                                                                                                                                                 |
| No turn work command is written after its cancel was taken; the cancel sequence is                          | same test; pi real-process test cancelling before the prompt and during model selection (the fake model receives no request)                                                                                                                                                                                                                                                                                                                         |
| Cancel ends the turn within ten seconds, an agent ignoring abort included                                   | conformance scenario: an agent ignoring abort is killed at the bound and the turn is cancelled; after any cancel the session ends (new pid and new bearer on the next turn) and the next turn continues the conversation                                                                                                                                                                                                                             |
| A turn's terminal waits for its accepted calls to settle, and the next turn waits for the terminal          | conformance scenario with a blocked tool call cancelled mid-call; `internal/app` test over the real endpoint                                                                                                                                                                                                                                                                                                                                         |
| A call executes only under the turn whose connection carried it                                             | `internal/app` tests over the real endpoint and bridge: a delayed request of turn N, a retried one, and an approval answer for N, delivered while N+1 is running, each fail without touching N+1's authority; after a cancelled turn every request of the ended session is refused (old bearer retired), the turn's terminal published only after the endpoint settled its calls, whether the turn ended normally, by cancel or by the process dying |
| Two turns of a conversation are served by one process                                                       | conformance scenario: same pid across two turns; a new pid after a kill, with the conversation continued                                                                                                                                                                                                                                                                                                                                             |
| A session ends with its window, the backend, or an hour idle — never while a call or an approval is pending | conformance scenarios for window close and idle (with the idle bound injected); an approval pending across the idle bound keeps the session; ADR-anntnxdj's parent-death check stays                                                                                                                                                                                                                                                                 |
| nocx's prompt is appended once per run, byte-identical, and the model input reproduces across restarts      | pi real-process test: system prompt identical across turns, an idle restart, a crash restart and a cancel restart; the pinned model is used after each restart. Steering's own checks come with the steer stage (`nocx-a62cl.6`)                                                                                                                                                                                                                     |
