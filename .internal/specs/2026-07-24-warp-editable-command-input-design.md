# Warp-style editable command-input block — Design (minimum)

- **Date:** 2026-07-24
- **Status:** Approved in brainstorming — ready for `writing-plans`
- **Session bead:** `nocx-3js` (Brainstorming: Warp-style command blocks — epic 4ff refinement)
- **Chosen approach:** A — full safe path (bugs → gating → escape → raw-routing → enable by default)
- **Binding contracts:** [ADR-0004](../../docs/decisions/0004-input-ownership-and-editor-abstraction.md)
  (input ownership + pluggable editor), [ADR-0006](../../docs/decisions/0006-marker-only-prompt-mode.md)
  (marker-only prompt), epic `nocx-4ff`, plan
  `docs/superpowers/plans/2026-07-24-warp-m3a-marker-only-prompt.md`.

## Context

The user wants Warp's **editable command-input block**: the command being composed
is freely-editable text in a real editor (mouse caret placement, mid-line edits,
multiline, IME, clipboard) — decoupled from shell line discipline — not a readline
line editable only at the cursor. This is the bottom "input block" in Warp, not the
per-command output blocks.

The architecture is already accepted (ADR-0004/0006) and **most of it is built but
disabled** behind `ENHANCED_INPUT = false`. This design defines the *minimum
remaining work* to enable it safely for local zsh/bash, per the user-selected
"Approach A".

## Current state (already built)

- **Shell side complete** (`nocx-5mn.4`): `internal/shellintegration/scripts/nocx.zsh`
  and `nocx.bash` emit OSC 133 `A/B/C/D` (D carries exit code), local and over SSH,
  and render a **marker-only prompt** when `NOCX_PROMPT_MODE=marker-only`
  (commits `35b7ff7`, `3ca4c8d`, `7e963cc`). Script `version = "3"`.
- **Editor** (`frontend/src/editor.ts`): passive `<textarea>`; Enter performs the
  atomic handoff (clear + hide **before** send).
- **Input-ownership state machine** (`frontend/src/input-state.ts`):
  `RAW / PROMPT_READY / RUNNING_RAW / ALT_SCREEN` with `owned` / `trusted`.
- **Submit routing** (`frontend/src/input-target.ts`): `ShellInputTarget` sends the
  document to the PTY as bracketed paste + CR.
- **Wired** in `frontend/src/tabs.ts` behind `ENHANCED_INPUT = false`; `internal/app/app.go`
  currently spawns the PTY with `enhanced=false` (today's visible prompt).

## Goal

When enabled, a local zsh/bash tab spawns marker-only. At each **clean** prompt
(`A→B`) nocx hides the (invisible) shell prompt and shows the DOM editor; on Enter
the full text is handed to the PTY and the shell paints the committed command once;
output flows in the normal xterm. **Anything that is not a clean top-level prompt**
(a running command, alt-screen/TUI, an unintegrated shell, SSH without integration,
a `PS2` continuation) routes keys **raw** to the PTY. Fail-open — the user is never
trapped in an invisible prompt.

## Scope

**In:** W1–W6 below — the safe-enable of the existing editor for local zsh/bash.

**Out (deferred, tracked, not cut):**
- Editor chrome: cwd/git/status presentation, the directory chip, the submit-arrow
  button, the `⌘↵ new /agent conversation` hint (polish).
- Output blocks / OSC-133 decorations (`nocx-4ff.5`).
- App-owned history + completion (`nocx-4ff.6`).
- Agent mode as a second `InputTarget` (`nocx-4ff.7`).
- Nested-shell / SSH enhanced negotiation (ADR-0006 §6) — MVP inherits the env but
  only the top-level session owns input.

## Work items (build order = Approach A)

### W1 — Fix input-state latch bug (`nocx-4ff.11`)
- **Change:** In `input-state.ts` `reduce()`, `owned` may become `true` **only** when
  this prompt cycle passed a real `A` **then** `B`. Today `B` grants `owned` whenever
  `state === PROMPT_READY`, so `B,B` from RAW latches `owned` with no `A`, and an
  orphan `D` (state ≠ `RUNNING_RAW`) leaves a stale `owned:true`. Introduce an
  explicit "saw A this cycle" gate; force `owned=false` in every non-`PROMPT_READY`
  state.
- **Acceptance:** property tests over marker sequences — `owned` is true **iff** the
  last two clean markers were `A` then `B` with no intervening `C/submit/alt/reset`;
  `B,B` and orphan `D` never latch `owned`.

### W2 — Fix submit dispatch + refocus (`nocx-4ff.12`)
- **Change:** On Enter, dispatch `{type:'submit'}` to the state machine and refocus
  the xterm **before/at** send. Today `editor.ts` hides + submits but never tells the
  machine, so `owned` stays stuck and `PS2`-continuation keystrokes go nowhere.
  Keep the atomic-handoff ordering (hide before send).
- **Acceptance:** after submit → state `RUNNING_RAW`, `owned=false`, xterm focused;
  typing during a `PS2` continuation reaches the PTY.

### W3 — Readiness gating + enhanced spawn (`nocx-4ff.10`)
- **Change:** In `app.go` add `enhancedInput bool` to the local PTY factory and call
  `ActivationEnv(enhanced)`. Spawn marker-only **only** when the frontend is ready to
  own input (editor mounted + marker listeners attached for the tab); otherwise spawn
  in today's visible-prompt mode. Closes the race where the shell paints an invisible
  prompt before the frontend can show the editor.
- **Acceptance:** an enhanced PTY is spawned only after frontend-readiness is
  asserted; when not ready it falls back to a visible prompt — no invisible-prompt gap.

### W4 — Native-mode escape (`nocx-4ff.9`)
- **Change:** A **state-independent**, always-available action (keybinding + automatic
  trigger on marker anomaly) that restores a **visible** shell prompt and raw keyboard
  routing for the session, regardless of the current machine state — so the user is
  never stuck with an invisible prompt and no editor.
- **Acceptance:** from any state the escape restores a usable visible prompt and raw
  routing; verified against a shell that stops emitting markers mid-session.

### W5 — Raw-input routing after submit until next prompt (`nocx-4ff.4`)
- **Change:** In `RUNNING_RAW` all keys go straight to the PTY/xterm until the next
  clean prompt marker; the editor never steals input from a running program.
- **Acceptance:** the matrix works normally after submit — `read`, python, node,
  `less`, `vim`, `htop`, a password prompt, `Ctrl-C`, `Ctrl-D`.

### W6 — Enable + hardening (`nocx-4ff.13`, flip `ENHANCED_INPUT`)
- **Change:** Flip `ENHANCED_INPUT` default to on (gated by W3 readiness + W4 escape).
  Hardening: `NOCX_SESSION_ID` nested-shell gate (only the top-level session owns
  input), `crypto/rand` fail-closed for the session id, bash array / powerlevel10k
  tests, zsh source-first exec tests.
- **Acceptance:** default-on gives an editable block at a clean local zsh/bash prompt;
  nested and unintegrated shells fail open to raw; hardening tests are green.

## Safety invariants (must hold throughout)

- **Fail-open everywhere:** missing or malformed markers → `RAW` + visible prompt.
  Never trap the user (ADR-0004 §1, ADR-0006 §5).
- **Single-owner (AD-6):** the backend stays byte-blind; the editor and state machine
  sit above the renderer boundary. The renderer only reports facts.
- **Nested / SSH:** only the top-level integrated session owns input
  (`NOCX_SESSION_ID` gate); everything else routes raw.

## Testing

- **Unit:** `input-state.ts` `reduce()` property tests over marker sequences (W1);
  editor submit-dispatch + refocus (W2).
- **Exec (creack/pty):** marker-only prompt beats hostile frameworks (partly exists in
  `scripts_exec_test.go`); enhanced-spawn env from `app.go` (W3); hardening (W6).
- **e2e (Playwright):** click-to-focus caret placement, mid-line edit, multiline, and
  submit paints the command once (extends existing `e2e/`).
- **Manual:** the W5 raw-routing matrix and the W4 escape hatch.

## Build order & bead mapping

`W1 (nocx-4ff.11) → W2 (nocx-4ff.12) → W3 (nocx-4ff.10) → W4 (nocx-4ff.9) →
W5 (nocx-4ff.4) → W6 (nocx-4ff.13, flip ENHANCED_INPUT)`, all under epic `nocx-4ff`.

## References

- ADR-0004 — input ownership state machine + pluggable editor.
- ADR-0006 — marker-only prompt mode.
- `docs/superpowers/plans/2026-07-24-warp-m3a-marker-only-prompt.md` — shell-side M3a (done).
- Session bead `nocx-3js`; epic `nocx-4ff`.
