# ADR-0104 — Live terminal history does not depend on command recording

- **Status:** Accepted for implementation
- **Date:** 2026-10-25
- **Context:** the owner's universal-scrolling implementation request on `nocx-zg5w0`.
- **Supersedes:** ADR-0091 only where an unqualified live history window cannot honestly be assigned to a running command block. Its qualified running-body rule, and ADR-0092's input ownership, remain unchanged.
- **Related:** ADR-0066, ADR-0078 and ADR-0089.
- **Beads:** `nocx-zqr4z`, `nocx-3y64b`, `nocx-if2d8`, `nocx-tdncs`.

## Context and problem

A recovered primary-buffer `omp` retained thousands of emulator history rows but had no durable command entries. The renderer enabled emulator-backed history only for markerless sessions; an integrated running program needed a recorded output artifact to acquire a scroll range. Recording policy had become a prerequisite for scrolling a live terminal, contrary to ADR-0078.

Enabling both sources is not a join. A stored output row's `from` is an ordered row-stream cursor, while `block.outputStart.row` and `session.historyPage` use screen coordinates. `durableThrough` remains reserved and null. Empty artifacts are also not coverage: an asynchronous, capped or disabled recording can be empty while live history exists.

The wheel had two independent listeners. A mouse-reporting program could receive an event that had already moved DOM history, fractional captured events could retain their native default, and historical deltas could enter the live accumulator. Modifiers were lost before reaching an emulator that already supports them. Non-reporting alternate-screen programs had no mode-1007 fallback.

## Considered options

- Enable live history only when recorded rows are empty. Rejected: temporary emptiness and capture limits do not establish ownership or a boundary.
- Merge stored and live rows by numeric cursor or text. Rejected: their coordinates differ, and repeated text is legitimate terminal output.
- Identify applications and specialize their input or presentation. Rejected: applications choose terminal modes, not nocx-specific names.
- Use one explicitly qualified live projection, with a whole-terminal presentation when the boundary is unknown. Chosen: it preserves retained rows without inventing attribution or another history store.

## Decision

### One live source and an explicit projection

The existing `LiveHistorySurface` pages the backend emulator in primary-buffer running presentations as well as markerless ones. This does not depend on history recording settings or the presence, size or arrival time of an artifact.

A running block may own a live window beginning at its backend-published output-start anchor. The live-history owner qualifies the anchor against the accepted primary buffer instance, columns and runtime revision; its caller uses the result rather than deriving a second answer. An old anchor cannot regain qualification merely because a resized terminal returns to its original width. A newly recovered anchor must postdate the latest committed geometry.

An already qualified window may survive a directly observed, same-width geometry commit: its revision must immediately follow the previously accepted runtime revision. That proves no committed width transition was coalesced between frames. An unobserved interval is conservatively unqualified even if its final width matches. Height alone does not rename row space, but can pull historical rows onto the live screen. Installed pages are clipped at every accepted primary frame's `baseRow`, including while the reader is away; incoming pages are also capped at that boundary. Unknown geometry transitions retire cached coordinates rather than guessing a reflow mapping.

While that window owns the running body, the block's durable output projection is hidden. Completed cards still use durable artifacts. There is no comparison between a durable `from` and a live row coordinate, no text deduplication and no second scrollback buffer.

If the boundary is unavailable or invalid, the live rows belong to a whole-terminal-history presentation outside all command cards. The archive/card layer is hidden atomically; earlier rows are not attributed to the current command. A late anchor may qualify the block while the reader is at the tail, never by moving a reader already in the past. An upward local wheel on the idle live grid can explicitly enter terminal-history review. An opaque live view may remain through the command's completion until the next command selects its projection.

Pruning, ED3, paging, reader holds and tail following remain with the existing live-history owner. ED3 invalidates a command boundary rather than preserving a stale numeric cut. Alternate-screen presentation deactivates primary history and removes its scroll range; Shift does not reveal primary history there.

### One wheel owner before accumulation

`IntentInput` is the sole wheel arbiter. It classifies the target, Shift override, primary/alternate buffer and reporting mode before accumulating a delta or cancelling a native action.

- Historical targets remain local; nested archive scrollers can retain the browser's native default.
- Shift-wheel and primary-buffer wheel without reporting use local history, not application input.
- A valid live target with reporting, or in the alternate buffer, belongs to application input. Fractional events are captured immediately, even before a complete row has accumulated. A backend refusal does not redispatch the event locally.
- Application residuals are reset across target/context changes, accepted mode/buffer/geometry changes and session rebinding, including transitions with no intervening wheel event. There is no timing-based guess at a physical gesture boundary.

The existing mouse payload keeps four fields. Its action token accepts the same `Ctrl+`, `Alt+`, `Shift+` and `Super+` prefixes as a key intent, for example `Ctrl+Alt+press wheel-up 3 7`. The helper fills the existing `MouseEvent.Mods`; it still owns all VT encoding.

In the backend emulator, vertical wheel input on the alternate buffer uses the existing cursor-key encoder only when reporting is off and DEC private mode 1007 is on. Reporting takes precedence; primary-buffer input never becomes shell history navigation. Horizontal wheel does not invent vertical keys. As in pinned Ghostty's Surface implementation, this fallback uses unmodified normal/application cursor keys, while actual mouse reporting preserves modifiers.

ADR-0089's deliberate simultaneous drag selection and application pointer reporting is unchanged. Exclusive ownership here is the wheel rule, not a reversal of the drag decision.

## Consequences

A retained primary transcript is scrollable immediately after frontend recovery without recreating the command or recovering a missing recording. A recovered unqualified window can temporarily lose card chrome, but retains the terminal's real rows and existing process controls; it does not pretend an old row belongs to a new command. Erased or pruned rows are not recoverable through this view.

Known intervals keep their running block and completed archive. Width changes may retire an old reader position rather than guess a text-based anchor. Durable recording remains independently useful for completed commands and restart history, with its existing retention and cap semantics.

Mouse grammar and mode-1007 encoding are helper-runtime behavior: their implementation must be present in the helper that owns the PTY. Access to retained primary history uses the existing history-page seam and does not require restarting that PTY.
