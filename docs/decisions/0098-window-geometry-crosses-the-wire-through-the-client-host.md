# ADR-0098 — Window geometry crosses the wire through the client host

- **Status:** Accepted
- **Date:** 2026-10-21
- **Beads:** `nocx-1ebrs.41` (the shipped-build regression), `nocx-39vhn` (the fix this replaces), `nocx-mqie.1` (the original contract)
- **Supersedes:** ADR-0048 §7's window-geometry ruling and its premise — the section's other rulings (the whole-value set, the absence of a `uistate.changed` broadcast, fire-and-forget renderer writes) stand unchanged
- **Scope:** The window half of the UI-state document; the layout half (ADR-0048 §1–§6, §8) is untouched.

## Context

ADR-0048 §7 kept window geometry off the control plane: `uistate.get` and
`uistate.set` carry the renderer's half and nothing else, because "the
renderer can neither know it nor act on it, and a copy on the wire would
create a second potential owner of a fact the Go side already owns (AD-8)."
That premise was true when one process — the Wails app — owned both the
window and the document. `nocx-39vhn` fixed the last defect in that shape:
`uistate.Store.RestoreAndWatch` sampled the window in-process and wrote the
document in-process.

The coordinator cutover (`.internal/specs/2026-08-28-the-nocx-server-design.md`
D3; `nocx-p2q1q` and its stages) split that process in two. The window lives
in the Wails shell; the UI-state document lives in `nocx-server`, and the
shell "must not open a second one over the daemon's" store (the ADR-0043
argument, one level up). The cutover deleted the in-process sampler and
opened every window at the default, with a comment in `main.go` saying
restoring "moves to the client host with the rest of the native-window
surface (design D3, A1.2)" — and nothing ever did. The shipped 0.6.1 opens
at the default size every launch: the regression the owner found on
2026-10-21. `deadcode -whylive` confirmed the whole write half of the
package (`RestoreAndWatch`, `Sample`) reachable only through reflection.

So the question §7 answered has changed shape. It is no longer "may the
renderer have a copy of a fact the Go side owns" — there is no Go side that
owns both halves. It is: **how does a window in one process persist into a
document in another, without minting a second owner of either?**

## Decision

The document keeps exactly one owner, and it is the coordinator's
`internal/uistate.Store`. The window keeps exactly one owner, and it is the
shell. The renderer is a relay that decides nothing. Three seams carry this:

1. **`uistate.window.get`** (method) answers the recorded geometry. The
   renderer calls it once at bootstrap, beside `uistate.get`, and hands the
   answer to the shell's `HostRestoreWindow` binding. The shell applies
   `uistate.Restore(saved, displaysNow)` through `uistate.Apply` — the
   clamp, the fingerprint rule (identity, not containment) and which state
   to re-enter stay in `internal/uistate`, tested without a display, and the
   shell performs. AD-3 keeps its shape: the shell implements, never decides.
2. **`uistate.window.observe`** (notification) carries one live sample. The
   shell samples its own window on the same interval the in-process loop
   used (`uistate.DefaultSampleInterval`), emits only changes as a Wails
   event, and the renderer relays each to the store, where
   `uistate.Observe` folds it — a maximised or full-screen sample moves only
   its flags, exactly as before. The fold's one owner is unchanged.
3. **The sampler starts from the restore binding and only there.** A launch
   can therefore never record its own opening over the geometry being
   restored — the ordering `nocx-39vhn` bought inside one process, restated
   across two. A first launch with nothing saved still reaches the binding,
   applies nothing, and starts recording; a renderer reload reaches it again
   and is inert.

A client with no native host — a plain browser, the e2e suite — gets neither
half: no restore, no samples, and the window half of the document stays as
it was. That is the honest degrade, not a gap: a browser window is not the
window of record.

`uistate.Store.RestoreAndWatch` and `Sample` are deleted. No process can run
them against this store again, and clean-only forbids keeping code whose
only defence is that nothing calls it. `Restore`, `Observe`, `Apply`,
`Probe`, `Placer` and `Fingerprint` all remain, each with a live caller on
one side of the wire or the other.

## Alternatives rejected

- **Extend `uistate.get`/`uistate.set` with a `window` field.** Mixes the
  renderer's half with shell-relayed data, so every sidebar write would
  carry geometry fields and every sample would re-send the layout. §7's
  "whole value" contract would silently become two contracts in one shape.
- **A second, shell-local document** for geometry (open it in the Wails
  process, leave the layout in the coordinator). Two documents for one
  ADR-0048 tenant, two write paths, two repair stories, and a backup that
  carries half a tenant. The cutover comment already names this the
  ADR-0043 argument.
- **The shell opens its own control-plane client** to the coordinator and
  fetches geometry before the window is created. Requires resolving the
  coordinator before `shell.Run()` — today the renderer drives that — and
  hides the window until it answers. A visible one-time resize after the
  backend is up costs less than restructuring the launch path this bead
  does not own.
- **The coordinator polls the client host for geometry.** Reverses the
  direction of every existing client-host ask, and turns four samples a
  second during a drag into four request/response round trips the
  coordinator initiates.

## Consequences

The window opens at the default and visibly settles once, after the backend
answers — the cost of the shell not owning the store, accepted here rather
than restructured. `uistate.window.get`/`uistate.window.observe` carry JSON
Schema contracts like every method (`contracts/`), with
`additionalProperties: false` and explicit `required`. The store's debounce
and its synchronous `Close` flush are unchanged, so a quit inside the
debounce window loses nothing that was relayed. Two windows would be
last-writer-wins on the window half; there is one main window, and
multi-window ownership is unscheduled work with its own decisions to owe.
