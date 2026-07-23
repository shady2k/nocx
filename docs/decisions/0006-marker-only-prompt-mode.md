# ADR-0006 — Marker-only prompt mode (enhanced-input shell contract)

- **Status:** Accepted
- **Date:** 2026-07-24
- **Related:** [ADR-0004](0004-input-ownership-and-editor-abstraction.md) §2 (refines
  its "empty, marker-only prompt + save/restore" mechanism), `nocx-5mn.4`
  (remainder), the command-experience epic `nocx-4ff`, `nocx-4ff.1` (state
  machine), Codex design review 2026-07-24.

## Context

The Warp-style experience requires that, when nocx's own DOM editor owns the
command line, the shell renders a **visually empty, marker-only prompt** —
otherwise the user sees a double prompt (nocx's editor chrome + the shell's
native `PS1`). ADR-0004 §2 decided this should happen "in enhanced mode, with
the user's original prompt saved and restored." Making that safe and robust
requires resolving *how* the shell is told to suppress its prompt, and *how the
empty prompt survives* real-world shells.

The shipped scripts (`nocx.{zsh,bash}`) today only **append** the `B` marker to
the existing visible `PS1` — they do not empty it, and that is correct while no
editor exists. Turning on suppression naively (a one-time `PS1=''` at load, plus
save/restore of a captured `PS1` string) is fragile:

- Prompt frameworks (oh-my-zsh, powerlevel10k, starship) **rewrite `PS1` every
  precmd**, clobbering both the emptying *and* the `B` marker.
- A captured `PS1` scalar is stale the moment it is captured — real prompts are
  regenerated per cycle from cwd/git/exit-status/async state.
- A spawn-time flag cannot detect a later frontend failure, so "empty shell
  prompt + no working editor = a shell with no usable prompt" is a real hazard.

## Decision

### 1. Two separate, static-at-spawn contracts

- `NOCX_SHELL_INTEGRATION=1` — unchanged baseline: emit OSC 133 `A/B/C/D` +
  OSC 7, **visible** native prompt. Independently useful and safe (cwd,
  activity, future blocks); shippable without any editor.
- `NOCX_PROMPT_MODE=marker-only` — opt-in: additionally suppress the visible
  prompt (marker-only) and let nocx's DOM editor own input. Set by nocx **only**
  when the editor feature flag is on **and** the frontend is ready (below).
- `NOCX_SESSION_ID=<opaque>` — identifies the compatible nocx session so a
  nested/forwarded environment can be distinguished from a genuinely attached
  enhanced session.

Named for the observable shell behavior, not a vague capability. A general
`NOCX_CAPABILITIES` negotiation is deferred until multiple versioned behaviors
exist.

### 2. Per-prompt overlay, applied last — not a one-time assignment

The marker-only prompt is re-applied **every prompt, at the final prompt hook**,
after prompt frameworks have run:

- **zsh:** the suppressor `precmd` is kept **last** in `precmd_functions`
  (reasserted idempotently), replaces `PROMPT`/`PS1` with only the non-printing
  `OSC 133 B`, and clears `RPROMPT`/`RPS1`. (Contrast: status capture is forced
  *first*.) Powerlevel10k **instant prompt** is handled explicitly — disabled
  under marker-only mode, or a first-prompt artifact is accepted — and covered
  by a test.
- **bash:** reorder `__nocx_prompt_command` to (1) capture `$?`, (2) run the
  user/framework `PROMPT_COMMAND` (string **and** array forms), (3) emit
  `D`/`A`/`OSC 7`, (4) set `PS1` to the marker-only `B` prompt as the **final**
  action, (5) arm the preexec latch.

The documented contract: nocx is sourced **after** prompt initialization and its
suppressor runs last; configuration loaded afterward must not reorder it.

### 3. Overlay model, not save/restore of a `PS1` string

nocx does not capture and later re-assign `PS1`. It lets the framework compute
its prompt each cycle and overrides the **final rendered** prompt for that cycle.
"Restore" means **remove nocx's hooks/wrapper and request a fresh prompt cycle**,
so the framework regenerates its own prompt — never assigning a stale captured
scalar, and never overwriting a `PROMPT_COMMAND` the user replaced later.

### 4. Ownership requires `A → B`; secondary prompts stay native

- DOM keyboard ownership is authorized only by a valid **`A → B`** sequence
  (`A` = prompt processing began; `B` = the marker-only prompt was rendered and
  the shell is ready). `A` alone is **not** sufficient — if a framework removed
  `B`, keys stay raw (never "visible native prompt + active DOM editor"). This
  tightens `nocx-4ff.1`'s machine.
- `PS2` continuation, `PS3`, `read`/password prompts, and other secondary
  prompts are **left native and visible** — the safe fallback when a submitted
  command is incomplete or a program reads a line itself.

### 5. Fail-open is a coordinated capability, not just a flag

- **Frontend-readiness gating:** an enhanced (`marker-only`) PTY is spawned only
  after the editor, marker handler, input router, and PTY transport are
  initialized; if readiness is uncertain, spawn in **baseline** mode.
- **State-independent escape:** a global "native mode" shortcut works regardless
  of DOM focus or state-machine state and **restores a visible prompt** via a
  one-shot control operation (disable marker-only / launch a visible-prompt
  recovery shell). Merely routing keys raw while `PS1` stays empty is **not**
  fail-open.
- **Missing `B`** keeps routing raw; teardown/re-sourcing is idempotent.

### 6. Nested / remote / live-toggle scope (MVP)

- Marker-only mode is enabled only for a **top-level** shell known to have loaded
  compatible integration (matched via `NOCX_SESSION_ID`); accidental env
  inheritance by a nested shell must **not** silently suppress its prompt.
- Remote (SSH) enhanced mode is **negotiated/installed explicitly**, never via
  generic env forwarding. Deferred beyond MVP.
- Static-at-spawn: a settings toggle applies to **new** tabs/sessions only.
  Live in-session toggling is a later one-shot mode change — no per-prompt
  frontend→shell signaling.

## Rationale

- Separating `NOCX_SHELL_INTEGRATION` from `NOCX_PROMPT_MODE` keeps the safe,
  already-shipped marker layer independent of the higher-risk prompt-suppression
  layer, so the coherent editor feature ships behind its own flag without
  regressing the baseline.
- Static-at-spawn avoids the race a dynamic per-prompt "is the editor active?"
  query would create exactly where atomic ownership is needed.
- A per-prompt overlay is the only thing that survives framework prompt
  rewrites; `A → B` ownership prevents the double-prompt failure; the escape +
  readiness gating are what actually deliver the fail-open invariant.

## Consequences

- `nocx.zsh` / `nocx.bash`: reorder hooks; add the per-prompt marker-only overlay
  gated on `NOCX_PROMPT_MODE`; clear zsh RPROMPT; handle p10k instant prompt;
  keep secondary prompts native. `scripts.go`: version bump; `ActivationEnv()` /
  `app.go` emit `NOCX_PROMPT_MODE` + `NOCX_SESSION_ID` only when the feature is on
  and the frontend is ready.
- `frontend/src/input-state.ts`: authorize DOM ownership on `A → B`, not `A`.
- Frontend: DOM editor + atomic handoff; frontend-readiness signal before an
  enhanced PTY is requested; a state-independent native-mode escape.
- Refines ADR-0004 §2: the "save/restore the user's original prompt" mechanism is
  replaced by the non-destructive overlay described here.

## Revisit when

- Remote enhanced mode is needed → design the explicit negotiation/bootstrap.
- Live in-session enhanced toggling is required → add the one-shot mode change.
- Multiple independently-versioned shell behaviors emerge → introduce
  `NOCX_CAPABILITIES` negotiation.
