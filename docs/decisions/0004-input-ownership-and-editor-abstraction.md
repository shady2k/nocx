# ADR-0004 — Input ownership state machine and a pluggable editor

- **Status:** Accepted
- **Date:** 2026-07-23
- **Related:** [ADR-0001](0001-xterm-js-as-vt-frontend.md) (xterm.js as VT engine),
  AD-5 (two-tier shell integration), AD-6 (single-owner state), `nocx-5mn.4`
  (OSC 133 emission + marker-only prompt), the command-input/blocks epic
  `nocx-4ff`.

## Context

The product goal is a Warp-style command experience: the command the user is
composing must be **freely-editable text in a real editor** — click anywhere to
place the caret, select, edit mid-line, multiline — not a shell readline line
editable only at the cursor. The **same input surface** must later host natural-
language queries to an LLM (agent mode), so the editor is a first-class surface
we own, decoupled from the PTY line discipline.

This collides with how a shell works. If our DOM editor owns composition and we
send the finished line to the PTY, the shell's own line editor (zle/readline)
echoes the command **and** the shell prints its `PS1` — the command appears
twice and an unwanted prompt shows up. The naive fixes are all fragile:

- `stty -echo`: readline/zle do their own redisplay; leaked termios state breaks
  child processes.
- PTY-level mode manipulation: races the foreground process, which owns the
  terminal modes.
- Parsing away the echoed region: breaks on wrapping, cursor motion, async
  output, shell plugins, and multiline commands.
- Mirroring readline state into our editor: two editors whose cursor, history,
  completion, quoting, and undo inevitably diverge.

The other open question was whether command **output** must be "frozen" out of
the xterm grid into per-command DOM blocks (for collapse/share). That would
create a second, less-correct render model and fight AD-6's single-owner rule.

## Decision

Two orthogonal axes.

### 1. Input-ownership state machine

nocx owns keyboard input **only when the integrated shell is positively at its
top-level prompt.** Everything else routes raw to the PTY. Three states, and the
only transitions into an enhanced state come from OSC 133 markers and xterm's
alternate-buffer event:

```
PROMPT_READY   OSC 133 A/B seen → keyboard drives the DOM editor
RUNNING_RAW    after submit until next prompt → keyboard goes straight to PTY/xterm
ALT_SCREEN     xterm entered the alt buffer → the app owns the full viewport
```

We do **not** try to infer "a process is reading stdin" from the byte stream or
termios — that is unknowable. The dependable boundary is "integrated shell is at
its prompt" vs "anything else is running." Consequently `read`, password
prompts, REPLs (python/node), and TUIs need no special detection: after submit
we are in `RUNNING_RAW` and keys pass through until the next prompt marker.

**Fail-open is an invariant, not an option.** If markers are missing or
malformed (unintegrated shell, nested SSH, `PS2` continuation on incomplete
syntax), fall back to a conventional terminal. Never trap the user in the DOM
editor.

### 2. Prompt/echo handling via atomic handoff

The shell integration renders a **visually empty, marker-only prompt** in
enhanced mode (empty `PROMPT`/`PS1` carrying only the OSC markers, with the
user's original prompt saved and restored when integration is off or nocx
exits). nocx renders its own prompt presentation (cwd/host/git/status) in the
editor chrome.

On submit we perform a **handoff, not duplicate suppression**:

1. Hide the DOM editor **before** sending anything.
2. Send the complete text as one bracketed paste (`ESC[200~` … `ESC[201~`)
   followed by `CR`.
3. Let zle/readline paint the accepted command once into xterm as the committed
   transcript.

No `stty`, no byte filtering, no readline mirroring. While composing, the DOM
editor owns and displays the command; once submitted, xterm owns and displays
it.

### 3. The editor is a passive surface behind a pluggable `InputTarget`

The editor widget holds text and selection and nothing else. Behavior — where a
submit goes, how completion and history work — comes from a registered
`InputTarget`. New capabilities (shell executor, LLM agent, future kinds) are
added by registering a target, never by editing the editor:

```ts
interface InputTarget {
  readonly id: string            // 'shell' | 'agent' | …
  readonly label: string         // UI chip
  submit(doc: string, ctx: SubmitContext): Promise<void>
  complete?(doc: string, pos: number, ctx: CompleteContext): Promise<Completion[]>
  history?(dir: 'back' | 'forward'): string | undefined
}

interface InputTargetRegistry {
  register(target: InputTarget): void
  setActive(id: string): void
  active(): InputTarget
}
```

`ShellInputTarget` (routes submit to the active PTY) is the first target;
`AgentInputTarget` (routes to the LLM controller) is added later on the same
editor. Targets keep separate drafts and histories but share the editor layout,
selection model, and completion UI. Mode selection is **explicit** (a
command/agent switch plus a keyboard shortcut), never a magic prefix such as
`?` — prefixes collide with valid shell syntax and obscure submission intent.

Implementation starts with a native `<textarea>` behind the editor: it already
handles mouse caret placement, selection, multiline, IME, clipboard, and native
undo. CodeMirror is introduced only when syntax-aware editing or inline widgets
justify it. Avoid `contenteditable`.

### 4. Output stays in one xterm; no freeze in the MVP

One continuous xterm instance and its scrollback own all output and selection.
OSC 133 boundaries create xterm markers carrying metadata (command, cwd,
start/end, exit code); we draw lightweight decorations (separators, status,
hover actions) over the grid. On the alternate buffer we hide block chrome and
the editor and give the app the full viewport.

Freezing rendered rows into DOM blocks is **deferred**. Collapse/share, when
needed, is built by preserving the raw byte interval per command or a serialized
terminal snapshot replayed into an isolated renderer — never reconstructed from
DOM text rows.

## Consequences

- AD-6 holds: xterm remains the single owner of render state; the backend stays
  byte-blind. The editor and state machine sit above the renderer boundary.
- AD-5 Tier A grows one responsibility: the injected hooks now also install the
  marker-only prompt (save/restore + raw fallback). Tracked in `nocx-5mn.4`.
- The MVP transcript is **plainer than Warp**: with an empty prompt, the
  committed command lands in xterm as bare text with no persistent styled block
  header. A per-shell "silent-accept adapter" (accept an injected buffer inside
  zle/readline/fish without redisplaying it) can later keep the command as a DOM
  block header — treated as polish because it needs separate, carefully tested
  implementations per shell.
- The LLM-in-the-same-input requirement is satisfied structurally by the
  `InputTarget` registry rather than a second widget.
- ADR-0001 is unaffected: this rides on the OSC handlers it proved reachable.

## Build order

1. Input-ownership state machine (`RAW`/`PROMPT_READY`/`ALT_SCREEN`) with the
   fail-open fallback. — `nocx-4ff.1`
2. Marker-only zsh prompt (macOS MVP), save/restore, raw fallback — `nocx-5mn.4`.
3. `InputTarget` abstraction + registry; `ShellInputTarget` first — `nocx-4ff.2`.
4. DOM editor surface + atomic handoff (`<textarea>`, hide-before-submit,
   bracketed paste + CR) — `nocx-4ff.3`.
5. Raw-input routing after submit until the next prompt (test: read, python,
   node, less, vim, htop, password, Ctrl-C, Ctrl-D) — `nocx-4ff.4`.
6. OSC-133-driven decorations (boundaries + exit code, no frozen blocks) —
   `nocx-4ff.5`.
7. App-owned history + basic completion (PATH, cwd-aware paths) — `nocx-4ff.6`.
8. Agent mode as a second `InputTarget` on the same editor — `nocx-4ff.7`.

## Revisit when

- A concrete need for collapsible/shareable output blocks arrives — then design
  the byte-interval/snapshot capture path (not DOM freeze).
- Native shell completion (plugin-accurate) becomes a requirement — then add a
  per-shell completion adapter or an explicit "native input mode" escape hatch,
  rather than mirroring two live editor states.
