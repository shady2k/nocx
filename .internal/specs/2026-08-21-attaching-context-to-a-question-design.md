# Attaching is a choice: the selection stops doing it for you

**Bead:** `nocx-TBD` (epic) · **Date:** 2026-08-21 · **Status:** approved by the owner, 2026-08-21

Continues `nocx-4wtlh`, which said of itself: _"this is a deliberate first cut of an
interaction to live with, not a final answer. Build it so it can be changed."_ The owner
has now lived with it. This is the change.

## What a person can do that they cannot today

**Select output to copy it without silently attaching it to their next question — and
attach exactly what they mean, by saying so.**

The end-to-end check that watches them do it (rule 2, `AGENTS.md`):

> A person selects a word in a finished block and copies it. They type a command and press
> Enter: it runs in the shell, and nothing was attached to anything. They then drag across
> three rows of that block, press **Attach**, and see those three rows marked in the block
> and one chip in the input line naming their text. ⌘Enter asks; the answer arrives; the
> ask carried exactly one reference.

## The three complaints, and the one cause

The owner's review, 2026-08-21, of the chip reading `ls · row 1`:

1. **A selection made to copy attaches something.** `terminal-content.ts` listens on
   `document`'s `selectionchange` and raises a chip for every selection landing inside a
   finished block's output. Copying output is the most common thing anyone does with
   output.
2. **One drag makes several chips.** The dedupe is by exact fingerprint
   (`block:rowStart:rowEnd`), and a drag across three rows passes through 1–1, 1–2 and 1–3
   — three fingerprints, three chips. It looks like one chip only when the drag stays
   inside a single row, which is what the reported screenshot happened to be.
3. **The chip names coordinates.** `referenceChipLabel` builds `<command> · row N` from the
   term-line index. The person selected the word `orca`; the chip talks about a row number
   they never counted, and about a whole row when they pointed at one word.

One cause: **attaching is inferred from a selection.** Everything above is what inference
costs — it fires when nobody asked, it fires repeatedly during one gesture, and it can only
name what it inferred rather than what was meant.

## What is NOT reopened

- **Nothing but the person changes where Enter goes** (`nocx-4wtlh`, from the owner's Warp
  complaint). Attaching still does **not** arm ask, does not move the input target, and
  does not touch the selection. The gesture for asking is still ⌘Enter.
- **Pointing freezes** (assistant design §2.1). A chip is still `frame + region`, frozen at
  the moment of attaching and never re-derived from the DOM at submit time (AD-8: selection
  is copy, the chip is the record).
- **No control in every block header.** That was the first of `nocx-4wtlh`'s three
  complaints and it is not coming back: the new affordance exists only while a selection
  exists, and the block-level action lives in the overflow menu that is already there.
- **Context is never invisible** (assistant design §2.4). Everything that will be sent is
  on screen before it is sent.
- **A zero-reference ask is a general question** (`ws_agent.go`: no references, no "screen
  content follows" claim). Unchanged.

## The gesture

### 1. A selection offers to be attached; it never attaches itself

While a document selection is non-empty and both ends are inside **one finished block's
output**, a single button — **Attach** — floats near the end of the selection. Pressing it
freezes that region into a chip. Nothing else does.

- It appears on `selectionchange` like today's chip did, but it only _offers_: no chip
  exists until the button is pressed.
- It disappears when the selection collapses, when it stops satisfying
  `chipFromSelection` (crosses two blocks, lands in a running block, lands in the editor),
  or once it has been pressed.
- It never covers the selection: anchored below the selection's last client rect, flipped
  above when there is no room below, clamped to the viewport.
- It follows the scrollback while the selection is alive, so a scroll does not strand it.

The button is the kit's `Button` (`variant="primary"`, `size="sm"`) inside a
surface-owned fixed-position wrapper. The surface **places** it; the kit paints it — no new
component, no repaint (`frontend/src/ui/README.md`).

### 2. The block's overflow menu attaches the whole output

`scrollback/blocks.ts` already builds a per-block `⋯` menu (Copy command / Copy output /
Copy all / Wrap lines). It gains one row: **Attach output**, which raises a chip covering
every term-line of that block — `rowStart: 0, rowEnd: <line count>`. Same noun, same chip,
no selection needed.

This is the answer to "I want to ask about this whole thing", which today means selecting
a screenful by hand.

### 3. The keyboard does what the button does

**⌘⇧A / Ctrl+Shift+A** attaches the current selection, from anywhere. A mouse-only attach
would put the whole gesture out of reach of the keyboard, which is the same defect as a
control with no accessible name.

It attaches and does **nothing else** — it does not summon the assistant and does not move
the input target, exactly like the button it stands in for (`nocx-4wtlh`). Summoning the
assistant is its own chord, `Cmd/Ctrl+Shift+/`, in the sibling design; the two never contend,
because there is nothing to attach while a full-screen program owns the screen and nothing to
summon while the editor is already up (⌘Enter is the gesture there).

### 4. Attaching twice is not two chips

The fingerprint dedupe stays and now does what it was written for: pressing Attach on a
region already attached is a no-op rather than a stack.

## What is shown

### The chip says what it carries

The label becomes the **text**, not the coordinates:

```
ls: Downloads  go  orca  repos          one row
ls: total 48 +6 rows                    a range — first row, then how many follow
```

- The command name stays as the prefix: with several chips in the line it is the only thing
  that says which block each came from.
- The preview is the region's first non-empty row, whitespace-collapsed, cut at ~32
  characters with an ellipsis.
- The chip's `title` carries the full sentence a coordinate is good for — `rows 12–14 of
40` — plus the first lines of the text. A tooltip is where a number belongs: available
  when wanted, absent when not.
- The `×` stays and does what it does today.

### The block marks what was taken

Every term-line a live chip covers carries `data-attached="true"` while that chip exists,
drawn as a left bar plus a faint tint (existing tokens; the mark is not a selection and
must not look like one).

This is the half the chip cannot do: with 200 rows of output and a chip reading
`ls: total 48 +6 rows`, the chip says how much and the mark says **which**. It is also how
a person notices they attached the wrong thing before sending it.

One owner: `terminal-content` repaints the marks from the chip list on every change to it
(clear all, then apply). A block re-rendered for any other reason (a wrap toggle) is
repainted from the same list.

### When they go away

Unchanged from today, and now visible in two places at once: a question consumes its chips;
`clear` takes their blocks; `×` removes one. The marks follow the chips.

## What goes to the model

**No wire change in this pass.** `agent.captureFrame` + `agent.ask` keep their shapes; a
chip raised by a button is the same `frame + region` a chip raised by a selection was, so
`agent-ask.ts` and `ws_agent.go` are untouched.

**The next bead, deliberately separate.** Today `ws_agent.go` sends
`"Referenced frame:\n" + <region text>` and nothing more: the model is given the rows and
is told nothing about where they came from or what surrounds them, and no declared tool can
read a stored frame (`readScreen` reads the live screen, not a frozen capture). The owner's
position — _"передаём текст и id блока или смещение, а дальше модель сможет запросить
больше"_ — needs two things this pass does not build:

1. each reference named in the prompt (`Reference 1 — "ls", rows 12–14 of 40`), and
2. a declared tool that reads more rows of a frame the ask already carries, effect
   `observe` (a live effect — the policy page's row set does not change), scoped to frames
   this run references.

It is its own bead because it touches the tool registry, the contracts and the prompt
assembly, and needs its own end-to-end proof: attach three rows of a long file, ask a
question whose answer is not in those three rows, and watch the model pull the rest.

## Testing

**Unit (`frontend/`)**

- A selection inside a finished block raises **no** chip, and the Attach affordance appears;
  the same selection with the affordance pressed raises exactly one.
- A drag across three rows, simulated as the three intermediate selections a real drag
  fires, raises **one** chip when Attach is pressed once.
- A selection that crosses two blocks, or lands in a running block, offers nothing.
- Enter after a selection still reaches the **shell** target (the `nocx-4wtlh` rule, now
  asserted with the affordance on screen).
- `Attach output` raises a chip covering the block's full line count.
- The chord attaches without the mouse.
- The label is the region's text, and contains no row number; the title carries the range.
- The covered term-lines carry the mark while the chip exists and lose it when it is
  dismissed, consumed or its block is cleared.

**e2e (`e2e/agent-ask.spec.ts`)** — the walk in "What a person can do", end to end against
the real backend and the fake OpenAI: copy without attaching, attach, ask, one reference.

## Deliberately out

- Attaching anything that is not a finished block's output (the live screen, an alternate
  screen, a file). The alternate screen is `nocx-x8s2`.
- Editing a chip's region after it is raised. Remove it and attach again.
- Any auto-attachment, in any form — including "attach the last block when the question
  looks like it is about output". This is the rule the epic exists to enforce.
- Moving the block's `⋯` menu onto the kit's `ContextMenu`, or adding a right-click menu to
  blocks. Both are wanted; both are their own bead (the menu is hand-rolled DOM today).

## Risks and open questions

- **Discoverability.** Nothing tells a person the button exists until they select something,
  and the button is the only teacher. Accepted: it appears exactly when it is useful, which
  is the best teacher available without a tour. If it turns out to be missed, the fallback
  is a one-time hint beside the first selection, not a permanent control.
- **The floating button over a scrolling surface.** Reposition-on-scroll is a listener on
  the scrollback container; if it proves janky the fallback is to hide on scroll and
  re-offer on the next selection change.
- **Two surfaces, one action.** The button, the menu row and the chord must all raise the
  chip through **one** function (`attachRegion(blockEl, rowStart, rowEnd)`); three call
  sites minting chips three ways is the defect `AGENTS.md` names as a second implementation
  of one concept.
