# ADR-0008 — Command blocks are a keyboard-first ledger, not cards

- **Status:** Accepted
- **Date:** 2026-07-24
- **Related:** [ADR-0004](0004-input-ownership-and-editor-abstraction.md) §4
  (decorations over one continuous xterm, no freeze), [ADR-0006](0006-marker-only-prompt-mode.md)
  (marker-only prompt), AD-5 (two-tier shell integration), AD-6 (single-owner
  state), AD-9 (bounded output ring). Refines the "block decorations" work of
  epic `nocx-4ff` (`nocx-4ff.5`).

## Context

The product goal is a Warp-style command experience. Warp models a session as a
stack of **blocks**: each command+output is a discrete card with a header (cwd,
duration), exit-code color, hover actions (copy/rerun/share), collapse, and
click-to-select. We use Warp as a reference for *what problems to solve*, not a
spec to copy — the explicit goal is to be **better** than Warp for a local-first,
SSH-heavy, agent-TUI-heavy daily driver.

Two facts constrain the design:

- **Architecture (ADR-0004 §4, AD-6):** output lives in **one continuous xterm.js
  instance rendered to a WebGL canvas**. Terminal text is in the canvas, not the
  DOM. Block visuals are lightweight decorations drawn *over* the grid (a spike,
  `nocx-4ff.8`, recommends a sibling "gutter" div anchored to OSC-133 marker
  rows). Freezing output rows into DOM cards forks the render model and fights the
  single-owner rule — it stays deferred.
- **What we already have wired:** OSC 133 A/B/C/D (with exit codes) for zsh+bash,
  local and over SSH; cwd via OSC 7; the app-owned submitted command text (from
  the editor, ADR-0004); and command start/end timings. The data for structure is
  already flowing — what is missing is a model and a surface for it.

Warp's card model is mostly mouse furniture: it wraps every command in visual
weight, makes a real terminal feel like a feed, and optimizes for click-to-select
and cloud sharing. It is a poor fit for our canvas-single-owner architecture and,
we judge, for the actual workflow (hundreds of commands, long-running jobs, SSH,
agents) of our users.

## Decision

Model command blocks as a **keyboard-first structural ledger of trusted command
landmarks over a real terminal** — not as cards. The transcript stays a live
xterm; blocks are an *index and control surface* on top of it.

For each trusted OSC-133 command cycle we retain a compact record:

- app-owned submitted command text, cwd, host (at submission),
- start/end xterm **`IMarker`s** (never cached parse-time line numbers),
- start/end timestamps,
- status: `running | success | failure | interrupted | unknown`,
- exit code (when known), and trust status (clean A→B→C→D vs anomalous).

**Output bytes are not retained by default** (they can carry secrets and are hard
to preserve faithfully); metadata is compact, searchable, and structurally
reliable.

The five pillars, in priority order:

1. **Navigation like an IDE symbol list** — prev/next command and **prev/next
   failure**; a command switcher (command, cwd/host, status, time, duration) with
   filters (failed / running / slow / current dir / current host / since-clear).
   Jumping scrolls xterm to the marker; it never selects or rearranges the
   transcript. A "return to live output" control after jumping back.
2. **Safe contextual rerun** — "Edit and run again" is the primary action: load
   the *app-owned* command into the editor, show its original `host:cwd`, warn
   when the current host/cwd differs. Never rerun scraped terminal text. Execution
   actions are disabled for untrusted/malformed marker cycles. A secondary
   explicit "run immediately" may exist for experts but is not the prominent
   action.
3. **Attention / failure queue** — surface "what needs me?": background-tab
   notification when a long/failed command finishes, "next unresolved failure",
   a configurable slow-command threshold, a small session summary
   (`2 running · 3 failed · 1 unread`), and local mark-as-reviewed. The existing
   title-based agent status participates here without pretending an agent TUI is a
   nested shell block.
4. **Durable local command provenance** — richer-than-shell history (command, cwd,
   host, exit, timing, session), searchable across recent sessions, pin/bookmark
   with a local note, open-in-editor with provenance visible, retention controls
   and an explicit "do not record" mode. Entirely local — no cloud.
5. **Output actions via explicit capture, later** — copy/export/search/diff a
   command's output built on an authoritative raw-byte interval or terminal
   snapshot replayed into an isolated renderer, never by scraping canvas cells.

### First increment (this is what `nocx-4ff.5` becomes)

A **command landmark system**, not a mini-card system:

1. a narrow gutter glyph on the command row, keyed to status/exit code,
2. prev/next command and prev/next failure keyboard navigation (jump scrolls
   xterm; "return to live"),
3. a lightweight inspector (opened by keyboard or clicking the glyph): command,
   cwd/host, status, exit code, duration, copy-command, and "Edit and run again".

Deferred from the first increment: persistent/sticky headers, block-wide hover
zones, hover-action toolbars, collapse, output copy, sharing, and click-anywhere
block selection.

### Deliberate divergences from Warp

- **No card around every command.** Subdued landmarks; a successful command almost
  disappears. Strong emphasis is reserved for running, failure, interruption, and
  focus/bookmark — **no green "success confetti".**
- **No click-anywhere block selection.** Terminal selection stays text selection;
  block focus is explicit via the gutter/inspector/palette/keyboard.
- **No sticky command header** over live terminal content (it consumes rows and
  fights alt-screen/split panes). Current-command status lives in stable pane
  chrome or a temporary inspector.
- **No collapse** until output has an authoritative secondary representation
  (byte-interval/snapshot). Hiding canvas rows xterm still owns breaks scroll,
  selection, search, and marker geometry.
- **No "share" as a default primary action** for a local-first terminal.

## Consequences

- **AD-6 holds:** xterm stays the single owner of render state; the ledger is an
  index above the renderer boundary; the backend stays byte-blind.
- **Keyboard-first** navigation over hundreds of commands beats mouse-driven cards
  during long/SSH/agent sessions — the workflow we actually target.
- **Fail-open (ADR-0004/0006) extends to block UX:** a renderer/reconnect reset,
  session exit, or malformed marker stream closes inspectors, disposes anchors,
  finalizes open commands as interrupted/unknown, and disables execution actions
  until a clean marker cycle resumes.
- The gutter overlay is `pointer-events: none` by default and positions glyphs by
  live `IMarker` line vs the viewport, coalesced into one `requestAnimationFrame`
  — it must not turn the Linux forced-refresh pump (ADR-0005) into a full-history
  layout pass, and must not silently reduce xterm columns.
- MVP transcript stays "plainer than Warp" (ADR-0004 already anticipated this);
  richer output actions and cross-session provenance are additive, not a re-render.

## Revisit when

- A concrete need for collapsible/shareable output arrives — then build the
  byte-interval/snapshot capture path (pillar 5), not DOM freeze.
- Cross-session provenance/search graduates from "nice" to "required" — then design
  the local store and its retention/redaction model (pillar 4).

## Credits

Design direction pressure-tested with an external model (Codex) on 2026-07-24;
the "ledger of landmarks, not cards" framing and the sibling-gutter pitfall list
come from that consultation.
