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

## The terminal

**Block**:
One command in a pane's transcript: its header, its outcome and its output
(ADR-0008). The output comes from the backend, never from what the client's screen
happened to hold.
_Avoid_: card, cell, entry (for what a person sees; the history record behind a block is
an entry)

**Live region**:
The part of a pane showing the terminal as it is now, painted from the backend's screen
frames. Blocks sit above it.
_Avoid_: prompt area, viewport, live block

**Helper**:
The process that runs where the shell runs — this machine or a remote host — and owns
the PTY and the terminal emulator (ghostty): the screen, its scrollback and which rows
left the screen. It keeps no history and no disk.
_Avoid_: agent, daemon, remote

**Coordinator** (process):
The nocx backend process on the machine the app connects to. It owns history (the
encrypted store on its disk), authenticates each command's start and end, and decides
what may be kept. Not the same thing as a coordinating agent above; say "backend" where
the two could be confused.
_Avoid_: server (when a helper is meant), host
