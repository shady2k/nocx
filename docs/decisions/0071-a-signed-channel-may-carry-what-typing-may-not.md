# ADR-0071 — A signed channel may carry what typing may not

- **Status:** Accepted
- **Date:** 2026-09-18
- **Supersedes:** decision 2 of the 2026-09-11 coordinator-surface design
  (`.internal/specs/2026-09-11-coordinator-surface-after-herdr-design.md` §4) in part: its
  "a pointer line, never the worker's content" now binds TYPING only, not every way into an
  agent. The same rule as restated in epic `nocx-i8umd` ("never as typed content") is narrowed
  the same way. No record is edited.
- **Related:** [ADR-0070](0070-a-worker-says-nocx-sees-the-coordinator-judges.md) (the mailbox
  and the wake, unchanged); [ADR-0064](0064-a-pane-that-is-read-may-be-answered.md)
  (the typing gate, unchanged). Bead `nocx-xn63t.4.17`.

## What was wrong

The rule that a peer's words are never put into an agent's pane was written when the only way
into an agent was to type into its prompt, as if the person at the keyboard had. Typed text is
indistinguishable from the human, so a worker's words typed into its coordinator would be read
as the owner's instruction. The rule was right about that, and it was phrased as if it were
about every channel.

It never kept the words away from the model. `workers.inbox` returns them today, as a tool
result, to the same agent a moment later. What the rule protects is ATTRIBUTION — that an agent
never mistakes a peer for its human — not secrecy of the content.

Two agents now offer a way in that keeps the attribution: Claude Code's MCP channels
(`notifications/claude/channel`, rendered `› Message from @<source>` and delivered to the model
in a tagged channel block, not as a user turn) and omp's extension API (`pi.sendMessage`, a
framed message the model receives as a developer message). Both carry the text itself, and both
are how those agents' own peer messaging works.

## Decision

1. **A channel the agent itself marks as not-the-user may carry a message's content**, signed
   `from <name> · <short id>` inside the text, because an agent may drop any metadata field
   (omp does not show `details` to the model).
2. **Typing stays pointer-only.** For an agent with no such channel — Codex, a plain shell, or a
   Claude session where the channel is not enabled — nocx types one signed pointer line through
   the typing gate, exactly as ADR-0064 and ADR-0070 say, and the content is read with
   `workers.inbox`.
3. **The mailbox stays the record.** A channel is a delivery, not a store: every message is
   committed to the mailbox first, and what a channel dropped is still there to read. Claude's
   channels are a research preview with delivery losses reported upstream, so nothing may
   depend on a channel having arrived.
4. **The opt-in is nocx's.** Claude channels must be enabled per session; nocx launches the
   worker, so nocx passes the flag. An agent a person launched by hand without it gets the typed
   pointer.

## Why this rather than the alternatives

- **Keep pointer-only everywhere.** It costs every message a round trip and a turn spent calling
  `workers.inbox`, and it protects nothing the inbox does not already hand over.
- **Type the content with a header**, as AgentsRoom does (`[AgentsRoom] New message from
devops`). A header is text too; the model cannot tell nocx's header from a peer that writes
  one, so the attribution it seems to add is not there.
- **Use Claude's cross-session socket** (`CLAUDE_CODE_MESSAGING_SOCKET`). It is documented only
  for the session's own children, not as an interface for another process.

## What the next person inherits

A per-agent delivery seam with three answers — Claude channel, omp extension, typed pointer — and
one question it must answer before sending content: does this agent mark this channel as not the
user? If the answer is not a documented yes, it is the pointer.
