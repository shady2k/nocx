# ADR-3kqrem7v — A running program keeps one header and one grid across screen modes

- **Status:** Accepted (owner approval, 2026-10-30, on `nocx-3an1s.1.4`; partially
  supersedes ADR-0004, ADR-0091 and the 2026-09-15 terminal-screen mockup decision
  only as specified below)
- **Date:** 2026-10-30
- **Supersedes, in part:**
  [ADR-0004](0004-input-ownership-and-editor-abstraction.md) §4's alternate-screen
  instruction to hide block chrome and give the application the full viewport: the one
  active running header remains visible in both presentations. Prompt-editor ownership,
  raw-running input, atomic handoff and explicit Ask ownership remain unchanged.
- **Supersedes, in part:**
  [ADR-0091](0091-a-running-command-s-output-is-one-block-and-a-separate-live-grid-exists-only-where-there-are-no-blocks.md)'s
  retained running cap/inset geometry, including the ancestor-derived live inset and
  mode-dependent header deduction. Its one-block output ownership and controller-owned
  live-region seat remain unchanged.
- **Supersedes, in part:** the
  [2026-09-15 terminal-screen mockup decision](../../.internal/specs/2026-09-15-terminal-screen-mockup-decision.md)'s
  running footer/ProcessBar policy, two-line active header and active live-body padding
  and trailing space. Its frozen transcript styling and other presentation decisions
  remain unchanged. Neither that accepted record nor the accepted ADRs are edited.
- **Related:** AD-1 (transport planes), AD-6 and
  [ADR-0066](0066-one-emulator-and-it-is-the-backends.md) (VT authority), AD-8
  (one owner per behaviour), [ADR-0024](0024-authenticated-shell-integration-channel.md)
  (independent lifecycle and buffer axes),
  [ADR-0072](0072-xterm-is-removed-first-and-the-interval-record-follows-the-cell-model.md)
  (cell vocabulary), [ADR-0073](0073-the-screen-frame-is-keyed-by-its-session-and-its-reader-and-continues-on-its-own-carrier.md)
  (frame authority), and [ADR-0092](0092-a-dom-seat-is-not-an-input-owner.md)
  (the grid keeps its own gestures wherever it is seated).

## Context

A running program may borrow the alternate screen to redraw after a real window resize.
The two presentations currently charge different amounts of space to that program. The
primary presentation has an inset live body, padding, a wrapping two-line command header
and a bottom process bar. The alternate presentation removes those costs. A buffer switch
therefore changes the measured grid, sends another resize, and can provoke another redraw
and buffer switch. Deduplicating identical reports cannot stop this loop: the reports
really differ.

A screen mode is a program-owned VT fact, not a request for a different terminal size.
Suppressing a brief alternate screen would hide a real mode. Delaying a switch or a
resize would mask the feedback rather than remove its cause. The owner instead chose one
running header in both presentations, with no bottom process panel.

## Decision

### One geometry, independent of presentation

**The size the program is told depends ONLY on pane geometry, NEVER presentation. An
already-applied equal size means NO resize RPC.** Switching primary/alternate buffers,
seating the header elsewhere, repainting output or advancing the duration must not change
the desired grid or send another resize.

Both active running presentations pay exactly these costs:

| Part                           | Primary running | Alternate running |
| ------------------------------ | --------------- | ----------------- |
| Active header, border-box      | 32px            | 32px              |
| Active grid inline gutter      | 0px             | 0px               |
| Active body top/bottom padding | 0px / 0px       | 0px / 0px         |
| Active trailing space          | 0px             | 0px               |
| Bottom footer/ProcessBar       | 0px             | 0px               |
| Ordinary running composer      | 0px             | 0px               |

The common terminal area's content width is the desired grid width. Its height minus
**32px** is the desired grid height. Persistent pane chrome is outside this area and unchanged. The old
alternate-only PaneContext strip is removed from the flow; the shared header
carries the running context instead. The header uses `--control-height-md`: one 20px text line and centered
22px icon targets inside the 32px border-box, `flex: none`, no wrapping and no vertical
margins. A narrow pane truncates command text; it never adds a second status row. The
full command remains available through its accessible text and title. The header may
retain its own 24px inline text inset; that inset is not an ancestor inset on the grid.

The existing pane/terminal-area measurement is the single geometry owner. It does not
read the live node's current ancestor chain or buffer mode to decide capacity. Pane
geometry, font/cell metrics and DPR changes recompute the geometric report through that
same owner. Output, duration ticks, header-seat changes and `1049h/l` do not. This is one
shared calculation, not two mode-specific calculations whose constants happen to agree.
The normal running cap and alternate fill use the same available height.

An assistant-owned running command remains inside its turn. Its active box is
placed at the pane width with a negative left margin equal to its ancestors'
computed left padding and border up to the terminal area (for the normal turn,
24px gutter + 12px child indent + 1px border = 37px). This affects placement only,
never the desired PTY size. Siblings and frozen rows retain their insets. Freeze
replaces the active element by established semantics: the same record/entry keeps
its turn parent and order, without carrying active width/margin styles.

At the idle prompt, pre-submit prediction uses this same fixed running geometry rather
than measuring a wrapped command header. The prompt-ready composer remains an input
surface; its visibility is not a reason to resize a command mid-run. A primary command's
short live body still follows its actual output tail and retains the backend
output-anchor echo clip. Grid capacity is not an instruction to allocate blank rows.

### One actual header, not two copies

Extend the existing CommandBlockFrame with a typed `variant: 'running'`, projected as
`data-variant="running"`. Keep **the same header DOM node** across both presentations.
BlockManager owns that reference and its duration and control updates. The controller's
existing seat transition coordinates its alternate placement and restores it before
freeze. Do not construct a copied fullscreen toolbar or a second status implementation.

The header contains the one-line command, the existing accessible Running spinner,
elapsed time, always-visible Interrupt and Stop icon buttons, and the existing overflow
menu. The buttons use the kit's small IconButton variant and focus ring. The surface
places them; it does not repaint kit controls. Context remains available in the pane
context and frozen header, not a second active path line.

On primary, all output of a running command remains inside its block, including committed
and live rows, as ADR-0091 requires. The alternate live grid remains outside the
transcript. A buffer switch does not close or reopen an execution interval. The header
is presentation chrome, never part of recorded rows. The one-block rule does not give
the block ownership of the grid's pointer or keyboard gestures.

### Frozen transcript geometry stays frozen

Only active rows become edge-to-edge. A still-running block's committed rows use the
same edge-to-edge running variant as its live rows, so their columns remain aligned.
Freeze restores the normal frozen layout without altering stored cells.

Frozen rows keep their **24px side insets**, **2px top / 6px bottom output padding**,
**16px trailing rhythm**, two-line metadata/header, styles, selection and shared
row-pitch contract. This is not a transcript redesign, a font change or an independent
DOM reflow of serialized output. The controller retains live-region ownership and moves
that region out before a block replacement, clear or freeze can remove it.

### Input stays with the program; chrome has an explicit route

After submit, input still reaches the existing terminal intent element. Clicking the
live grid focuses it. Removing Send input does not introduce a new textbox, empty
command, newline or editor handoff. Ctrl+C retains its existing terminal interrupt
behaviour. Interrupt calls the existing `signalActiveCommand('interrupt')` path; Stop
calls the existing `runningActions.stop()` path. They remain distinct intents, not two
labels for Ctrl+C, and no second signal or escalation implementation is added.

The native buttons have labels `Interrupt` and `Stop`, normal `tabIndex=0`, and
`data-block-control` so existing focus repair does not take their focus. Interrupt
exposes Ctrl+C and Alt+F10 in its title and `aria-keyshortcuts="Alt+F10"`. Logical control
order is Interrupt, Stop, overflow. Native Enter/Space and ordinary intra-header Tab
navigation remain browser behaviour.

**Alt+F10** is a new, local chrome-navigation chord, consumed through the existing
`IntentInput.consume` seam before intent emission. No existing grid-to-command-chrome
chord was found. F10 belongs to a familiar toolbar/menu navigation family; Alt qualifies
it so plain F10 still reaches the application. Meta/Ctrl+Enter already summons/toggles
Ask and cannot be reused without changing its meaning. Plain Tab must continue to
reach the program. On Mac function-key hardware the chord may require Fn, as other
function-key bindings do. The chord is available only in the active running pane, not
while an overlay or another text surface owns input.

PROGRAM and CHROME below are logical focus states **derived from DOM focus**, not new
lifecycle/presentation states or a stored latch. PROGRAM means focus on the existing
terminal intent element. CHROME means focus inside the same active running header.

| Focus state            | Key or action          | Result                                                                                           |
| ---------------------- | ---------------------- | ------------------------------------------------------------------------------------------------ |
| PROGRAM                | Tab / Shift+Tab        | Existing terminal intent reaches the program; no focus navigation.                               |
| PROGRAM                | Escape / Enter / Space | Existing terminal intents; unchanged.                                                            |
| PROGRAM                | Ctrl+C                 | Existing terminal interrupt behaviour; unchanged.                                                |
| PROGRAM                | Alt+F10                | Consume the chord, focus Interrupt, enter CHROME; no program bytes.                              |
| CHROME / Interrupt     | Tab                    | Native focus advances to Stop.                                                                   |
| CHROME / Stop          | Tab / Shift+Tab        | Native focus advances to overflow / returns to Interrupt.                                        |
| CHROME / overflow      | Shift+Tab              | Native focus returns to Stop.                                                                    |
| CHROME / first control | Shift+Tab              | Header boundary handler returns focus to the terminal; PROGRAM.                                  |
| CHROME / last control  | Tab                    | Header boundary handler returns focus to the terminal; PROGRAM.                                  |
| CHROME                 | Escape                 | Consume the key and return focus through `takeKeyboardToGrid()`; PROGRAM, no Escape byte.        |
| CHROME / Interrupt     | Enter / Space          | Native click invokes existing Interrupt; return focus to the grid if the command remains alive.  |
| CHROME / Stop          | Enter / Space          | Native click invokes existing Stop; current lifecycle handback owns completion.                  |
| CHROME / overflow      | Enter / Space          | Native click opens the existing menu; the overlay owns keys, and closing returns to its trigger. |
| Either                 | Click live grid        | Existing focus path returns PROGRAM.                                                             |
| Either                 | Genuine prompt / exit  | Existing lifecycle handback removes stale running controls.                                      |

Chrome navigation and activation emit no program bytes. Only header boundaries need new
Tab handling; there is no Tab listener on the grid and no second raw-input listener.
Pointer input can enter either focus state directly. Existing menu and Ask ownership
remain separate and authoritative.

### Receipts and genuine resize ordering remain authoritative

Compare the entire desired report, `{cols, rows, xpixel, ypixel}`, with the session's
applied geometry. An already-applied equal report needs no RPC and opens any applicable
fence immediately. An equal pending report shares its application receipt rather than
sending a duplicate request. Do not equate a request having been sent with its size
having applied.

A real pane resize, including one overlapping a buffer switch, still needs the existing
input-ordering fence and receipt correlation. Only the newest applicable resize receipt
may release held input; an older or superseded receipt is not success. Preserve ordered
release, close/error handling and the existing fail-open safeguard for an unanswered
request. A frame is not a resize receipt: waiting on a painted frame can deadlock when
that frame depends on held input. No mode-switch resize is manufactured merely to open
a fence. Removing the presentation feedback is not permission to remove real resize
ordering, input receipts or the backend's synchronized-output publication authority.

## Considered alternatives

Debouncing or classifying an alternate screen as a brief borrow hides real program
modes and introduces timing as policy. Identical-report deduplication alone leaves the
unequal geometry intact. Permanently latching the primary size underfills alternate
presentation and can ignore genuine pane changes. Giving the program a full grid while
clipping it into a smaller running block makes the last rows unreachable or forces
scrolling. None removes the mismatch. Common chrome and one geometric measurement do.

## Consequences and checks

The application deliberately keeps a 32px running header even on alternate screen.
Ordinary running commands lose the 56px bottom process panel and wrapping active
metadata. Frozen transcript appearance and truthful program modes remain intact.

The acceptance seam is the real resize-borrow program, not a synthesized mode flag:
`e2e/fixtures/resize-borrow.cjs` driven by `e2e/agent-redraw-mode.spec.ts`. One real pane
resize must produce at most one resize RPC, exactly one actual SIGWINCH, at most two
presentation changes and at most one alternate entry/exit. Lifecycle stays RUNNING and
the banner occurs at most once, with counts and matched lines available on failure.
Both observed modes must show the same header node at 32px, equal reported geometry
and no running bottom panel.

Checks also cover Alt+F10 from the grid, native button Tab/Enter/Space, distinct real
Interrupt/Stop paths, Escape handback and unchanged Ctrl+C. Preserve last-row and
rightmost-column reachability, short output, output-anchor exclusion, frozen gutters,
font/DPR changes and an in-flight real resize's input ordering. No retry, wider timeout
or delayed mode switch is evidence that the feedback has been removed.
