---
title: Warp-style command experience — input ownership, DOM editor, and command blocks
status: draft
created: 2026-07-23
binding-design: docs/decisions/0004-input-ownership-and-editor-abstraction.md
related: nocx-4ff (epic) + children .1–.7, nocx-5mn.4
bead: nocx-gs0 (brainstorm)
review: Codex adversarial design review (2026-07-23) — findings folded in below
---

# Warp-style command experience — input ownership, DOM editor, and command blocks

## 0. Decision framing

"Blocks like Warp" is **one coherent feature**, not a shippable half. Warp's
blocks carry the command *you typed into Warp* — which is trustworthy precisely
because Warp owns the input surface. The same is true here: the block's command
text and its "re-run" action are safe only when they come from nocx's **own DOM
editor** (app-owned, trusted submission), never from scraping xterm cells.

Therefore we build the whole thing in the ADR-0004 build order — input-ownership
state machine → editor → atomic handoff → blocks — and keep it behind a flag
until the coherent experience works end-to-end. We do **not** ship status-only
decorations as a standalone milestone.

The binding architecture is **ADR-0004**; this spec adds current-state truth,
the hardening required by the Codex review, and the ordered build plan.

## 1. Current state (verified)

Shipped on `main`:
- **OSC 133 emission** — `nocx.{zsh,bash}` emit `A`/`B`/`C`/`D;<code>` + OSC 7,
  injected live (`NOCX_SHELL_INTEGRATION=1` on PTY spawn; scripts auto-installed).
- **OSC 133 reception** — `xterm.ts` registers `registerOscHandler(133)`,
  `parseOsc133` → `CommandMarker`, surfaced via `onCommandMarker`; `tabs.ts`
  stores `_lastExitCode` only.

**Not built (all of the input side + blocks):** no input-ownership state
machine, no `InputTarget`, no DOM editor, no atomic handoff, no block model /
decorations. Keystrokes go raw to the PTY (`renderer.onData → session.send`).

**Correction to ADR-0004 §2 vs shipped scripts:** the "visually empty,
marker-only prompt" is **not** implemented. The scripts *append* `B` to the
existing visible `PS1` (`nocx.zsh:59`, `nocx.bash:92`); they do not empty it.
That is correct *today* (no editor exists, so emptying the prompt would leave no
prompt at all), but the enhanced-mode empty prompt is real, deferred work that
must land **with** the editor. Tracked against `nocx-5mn.4` remainder.

## 2. Scope

**In — the coherent MVP (ADR build order steps 1–6):**
1. Input-ownership state machine `RAW / PROMPT_READY / ALT_SCREEN` (`nocx-4ff.1`).
2. Enhanced-mode marker-only prompt: empty `PS1`/`PROMPT` + save/restore, gated
   on integration + enhanced state (`nocx-5mn.4` remainder).
3. `InputTarget` registry; `ShellInputTarget` first (`nocx-4ff.2`).
4. DOM `<textarea>` editor + atomic handoff — hide-before-send → one bracketed
   paste → CR (`nocx-4ff.3`).
5. Raw-input routing after submit until next prompt (`nocx-4ff.4`).
6. Command blocks: gutter status glyph + exit-code colour, command header/hover
   fed by **app-owned submitted text**, safe re-run via the same atomic handoff
   (`nocx-4ff.5`).

**Follow-ons (not this pass):** app-owned history + completion (`nocx-4ff.6`);
agent mode as a second `InputTarget` (`nocx-4ff.7`).

**Out (ADR invariants):** freeze-to-DOM of output; collapse/share; shipping
partial input/UI to real users before the whole flow works.

## 3. Hardening required (from the Codex review)

These are requirements on the ADR-0004 build, organised by component.

### 3.1 Prompt / shell (step 2)
- Implement the enhanced-mode **empty prompt** (`PS1`/`PROMPT` reduced to the OSC
  markers), saving and restoring the user's prompt when integration is off,
  nocx exits, or the shell is not at PROMPT_READY. Raw fallback stays mandatory.

### 3.2 OSC 133 plumbing (foundation)
- **One** OSC 133 handler, owned by `XtermRenderer`. It synchronously snapshots
  terminal facts at parse time (buffer line **and cursor column**, active buffer)
  and fans out typed events to all subscribers (state machine, blocks, tab
  exit-code). Store the parser `IDisposable`; dispose it in `XtermRenderer.dispose()`.
  Do not let multiple consumers each call `registerOscHandler(133)`.
- **Marker placement:** the command-row anchor comes from **`A`/`B`** (prompt
  row), *not* `C`. `C` fires in `preexec`/DEBUG after Enter, when the cursor is
  already on the output row — a marker made there marks output, not the command.
  `C`/`D` drive state; the gutter glyph anchors to the prompt/command row.

### 3.3 Input-ownership state machine (step 1)
- Explicit, **validating** transitions: only `A/B → PROMPT_READY`, submit →
  `RUNNING_RAW`, alt-buffer → `ALT_SCREEN`; enhanced states reachable **only**
  from markers/alt-buffer, never inferred from bytes/termios.
- **Resync on violation** (out-of-order / duplicate / partial markers): finalize
  or discard the open block, disable block actions, resume only on a clean
  `A→B→C` sequence. Never trap the user in the editor.
- **Nested integrated shells / SSH:** an inner shell interleaves its own
  `A/B/C/D` with no source identity. Treat an unexpected `A/B/C` while a block is
  open as loss of trust (§resync). Full nesting fidelity (scope ids) is out of
  scope; correctness/fail-open is not.
- **`reset()`:** add `CommandBlocks.reset()` / state-machine reset, invoked from
  `XtermRenderer.reset()` (reattach path), clearing current block, capture
  anchors, decorations, tooltip, and returning to `RAW`.
- **Terminal exit / no-`D`:** a running block may never get `D` (`exec`, shell
  death, disconnect, long-running command). On session exit/reset, finalize open
  blocks to an explicit terminal state; never leave a forever-spinner or fire
  actions against a dead PTY.
- **Malformed `D`:** `parseOsc133` yields `{kind:'D'}` with no code. Use an
  explicit `unknown` state (or ignore + resync); do not silently render "success".

### 3.4 Editor + submission (steps 3–5)
- `<textarea>` passive surface; behaviour via a registered `InputTarget`
  `{id,label,submit,complete?,history?}`; `ShellInputTarget` routes submit to the
  active PTY through the **atomic handoff** (hide editor → bracketed paste → CR).
- The **submit boundary is the single source of truth** for a command's text.
  Store it on the block. This is what makes command header/hover and re-run both
  correct and safe — no buffer scraping (the approach ADR-0004 §Context rejects).

### 3.5 Blocks (step 6)
- Gutter status decoration: running / ok (`exit 0`) / error (`exit ≠ 0`), colour
  from `D`. Command header/hover uses the stored submitted text (`textContent`,
  never `innerHTML`, CSS truncation).
- **Re-run** re-submits the stored text through the same `ShellInputTarget`
  atomic handoff, and only when the state machine reports `PROMPT_READY`. Never
  raw `text+CR`; never from untrusted/scraped text.
- **Copy** is an upward intent (`requestCopy(command)`) routed to the existing
  clipboard policy in the tab layer — `blocks.ts` does not acquire clipboard
  policy.
- **Gutter geometry is unproven** — `registerDecoration({x:0,width:1})` reserves
  a cell inside the grid; a negative transform moves the element into a clipped
  layer and drifts on fit/DPI/font/renderer-fallback. **De-risk with a spike
  before committing** (see §5): prefer a dedicated sibling gutter aligned from
  marker/viewport state, or reserved container padding accounted for in fitting.
- **Alt-screen:** hide via a single class toggled on the terminal container +
  close any tooltip on `onBufferChange`; verify restore after `1049h/1049l`,
  resize, and renderer fallback. Do not iterate historical records.
- **Disposal / eviction:** every registration returns/stores an `IDisposable`;
  `onRender` must not attach duplicate listeners across re-renders; eviction and
  `marker.onDispose` both dispose decoration + marker + listeners idempotently;
  never evict the currently-open block.

## 4. Files (indicative)

- New: `frontend/src/input-state.ts` (state machine), `frontend/src/editor.ts`
  (textarea surface), `frontend/src/input-target.ts` (registry + ShellInputTarget),
  `frontend/src/blocks.ts` (block model + decorations), plus `*.test.ts` each.
- Changed: `renderers/xterm.ts` (single 133 handler + cursor snapshot; marker/
  decoration API; dispose/reset wiring), `renderers/types.ts` (richer marker
  event carrying cursor facts; capability surface if needed), `tabs.ts` (wire
  editor/state machine/blocks per tab; copy intent), `style.css` (editor chrome,
  gutter, tooltip), `internal/shellintegration/scripts/nocx.*` (enhanced-mode
  empty prompt + save/restore).

## 5. Testing & de-risking

- **Spike first** (before deep planning of step 6): a small xterm 5.5 harness
  proving (a) gutter placement that survives fit/resize/DPI/fallback, and (b)
  marker placement on the command row for single-line, wrapped, multiline, and
  no-output commands, plus a PTY-driven script-sequence test asserting the real
  `A/B/C/D` order. Result feeds the plan; if gutter-in-grid is infeasible, adopt
  the sibling-gutter approach.
- **Unit (vitest):** pure state-machine transitions incl. resync/reset/nesting/
  malformed; block bookkeeping over a fake terminal surface.
- **Integration (xterm-in-DOM):** placement, wrapping/reflow, marker movement on
  trim, `onRender` idempotence, alt-buffer hide/restore.
- **PTY / e2e:** real shell — `true`→green, `false`→red; `vim`/`htop`→alt hides
  chrome; unintegrated shell → nothing drawn; editor atomic handoff produces one
  clean transcript (no double echo). Follow-ups: `nocx-9x1`, `nocx-bq7`.

## 6. Open decisions for planning
1. Gutter approach — decided by the §5 spike (sibling gutter vs reserved padding).
2. Enhanced-prompt coupling — how the state machine signals the shell to swap to
   the empty prompt (env/DSR/dedicated marker) vs always-empty-when-integrated.
3. Editor engine — start with `<textarea>`; CodeMirror only if/when syntax-aware
   editing is needed (ADR-0004 §3).
