# nocx

A terminal in which a person, and agents working for them, run programs. This glossary
names what the coordinator surface talks about; `AGENTS.md` owns the working rules.

## Coordinating agents

**Coordinator**:
An agent that started workers and is responsible for them.
_Avoid_: orchestrator, supervisor, parent

**Worker**:
An agent a coordinator started in its own tab to do a task.
_Avoid_: participant, child, subagent

**Idle** (worker):
A worker that is doing nothing and waiting for nothing: no turn, no subagent, no command
running, its input box free.
_Avoid_: done, finished, completed

**Blocked** (worker):
A worker that cannot continue until someone steps in: a menu or a question on its screen,
or its agent's own error (the API unreachable, the quota gone).
_Avoid_: idle, waiting

**Exited** (worker):
A worker whose agent process is gone.
_Avoid_: abandoned, completed, failed

**Report**:
What a worker says about itself through its tool — a kind (`done`, `question`) and free
text. It is the worker's claim, never a verdict.
_Avoid_: declaration, drop, outcome

**Outcome**:
Whether a worker's task succeeded. Judged by the coordinator alone; nocx never records one.
_Avoid_: result, status

**Mailbox**:
Where an agent's messages wait until that agent reads them: reports and state changes of
its workers for a coordinator, the coordinator's mail for a worker.
_Avoid_: inbox (as a noun for the store), queue, holdings

**Wake**:
One line nocx types into an idle coordinator saying its mailbox has something new. It
carries a pointer, never a worker's words.
_Avoid_: notification, prompt, nudge
