# ADR-0064 — A pane nocx can read may be answered, and its reading may reach the coordinator that holds it

- **Status:** Accepted
- **Date:** 2026-09-11
- **Related:** amends the AD-6 amendment of 2026-08-25 (`docs/architecture.md`,
  "the two powers, exhaustively"), which reserved any addition to itself for an
  amendment and not an extension. Builds on [ADR-0041](0041-x-vt-as-the-backend-emulator.md)
  (the emulator the grid reads), [ADR-0063](0063-typing-is-refused-on-evidence-against-the-rule.md)
  (when a rule may be believed) and [ADR-0024](0024-authenticated-shell-integration-channel.md)
  decision 2 (who moves a participant's record). Epic `nocx-f545a`, bead
  `nocx-f545a.1`. Nothing here supersedes an earlier record.

## Context

A coordinator asked for a worker. The worker's agent opened a question of its
own — "Quick safety check: is this a project you created or one you trust?",
with `❯ No, exit` above `Yes, I trust this folder` — and stopped there. nocx
watched that screen for the whole enrolment budget, called it `unknown`, threw
the pane away and told the coordinator the pane "never became ready to receive
a task ... spawning again may succeed if this was transient". It was not
transient, retrying could not help, and the screen that said so was deleted
before anybody could look at it (`nocx-ty5ks`, measured 2026-09-10).

Two of those three failures are already someone else's bead. The third is this
record's, and it is not a defect at all — it is the boundary working as
written. The AD-6 amendment grants an enrolled pane's grid exactly two powers,
"(1) whether nocx may write into this pane — the refusal in `nocx-dkawo.1`,
where anything but a positively identified `free_text` receives nothing at all;
and (2) what the pane's activity indicator shows", and closes: "That is the
whole list ... If a future reader wants a third power, that is an amendment to
this bullet and not an extension of it."

Answering a menu is a write into a pane that is not `free_text`, which power
(1) forbids in as many words. Telling a coordinator what a pane is asking, and
letting it read that pane's screen, is a third consumer of the grid's reading
which the list does not carry. Both are wanted, and the owner asked for both on
2026-09-11 — including, explicitly, the folder-trust dialog. So this is the
amendment the bullet reserved.

**What makes the bullet's own safety argument stop covering this.** It reads:
"Being wrong here costs a refused keystroke or a mislit dot — both recoverable,
both visible. It cannot cost a false completion, because completion is not one
of the two." A misread menu answered by a coordinator is a keystroke into
something other than what nocx thought, on a screen nobody is watching, and the
first option of the dialog that motivated this record is a standing permission
over a directory. The recoverability argument has to be re-made rather than
inherited, and this record makes it out of the identification instead of out of
the consequence: nocx sends a key only where it has positively identified a
menu AND the key is one that menu itself offers, so being wrong means the menu
was misidentified — not that a keystroke went somewhere unbounded.

## Decision

### 1. Power (1) is restated, and the set of keys is closed

nocx may write into an enrolled pane in exactly two situations:

- the pane is a positively identified `free_text`, and what is written is
  arbitrary text — unchanged, and still gated by ADR-0063's `MayType`; or
- the pane is a **positively identified menu**, and what is written is drawn
  from the **closed set of keys that menu offers**: move the selection, confirm
  it, dismiss it. Nothing else, and never arbitrary text.

"Positively identified" carries ADR-0063's meaning exactly: a branch of the
rule matched the frame and named the state. A frame that reaches its verdict by
falling through, or reaches `unknown`, receives nothing — as today.

The selection is named by the **option's own text as the screen drew it**,
never by a key, a row number or a coordinate. nocx turns that name into the
keys, which is what keeps the closed set closed: a caller that could send a key
could send any key, and a caller that named a row could name a row the menu no
longer has. An option no longer on the screen at the moment of the write is
refused rather than approximated.

The frame is re-read immediately before the write, as `internal/agenttyping`
already does for text: the identification that authorises the write is the one
taken microseconds before it, never the one a caller saw.

### 2. The reading may reach the session that holds the participant

Two things may cross to a coordinator, for a participant it holds a live
delegation over and for no other pane:

- **what the pane is**, and when that is a menu, **the question and its options
  as the screen drew them**;
- **the pane's screen**, as rows of text.

Both are reads. Neither decides anything, and neither may be produced for a
pane the caller does not hold — the delegation is the whole boundary, and it is
the same one every `workers.*` method already enforces (`ErrNotHeld` /
`ErrNotDelegated`, kept apart by `nocx-e5e8q`).

**A person's own pane is not readable this way by anybody.** A coordinator may
read the pane it asked nocx to create, for the task it gave it. It may not read
the pane its own person is working in, and a worker may not read its
coordinator's: the delegation points one way and this permission points with
it.

**One projection, not two.** The rows a coordinator receives are the rows
`agent.emitting` already renders for the calibration view, out of the same
`panegrid.Frame`. A second renderer of one screen is the shape AGENTS.md's
"look for the existing answer" rule is about.

**Nothing read here is persisted.** A pane's screen is not written to the
ledger, not recorded as history, and not logged. It is answered to the caller
that asked and kept nowhere.

### 3. The trust dialog is inside this, deliberately

A folder-trust question is a standing permission over a directory, and the
first option under the cursor is the one that grants it. It is admitted anyway,
by the owner's decision of 2026-09-11, and the reasoning is recorded here so
that a later reader can weigh it rather than guess it: the worker is a process
the coordinator asked for, in a directory this record's sibling bead
(`nocx-ty5ks`) now takes from the coordinator's own pane, and a coordinator
that may not answer is a coordinator whose worker silently does nothing. The
alternative — nocx answering it — was never on the table, and neither was
suppressing the question.

### 4. What this still may never do

Unchanged from the bullet it amends, and restated because an addition is where
a reader looks for the limits:

- A menu answer **moves no record**. It does not open, complete, alter or
  assign status to a wave state, a lifecycle attempt or an execution attempt.
  Those move on a fact a participant declared over the authenticated channel
  (ADR-0024 decision 2) or on a process exit, and on nothing read off a screen.
- nocx **never chooses an option itself**. Not on a timeout, not as a default,
  not to unblock a spawn. A budget that expires with a pane on a menu answers
  the caller and leaves the pane exactly as it is.
- The pane's **agent may not answer its own menu**. The caller is a session
  holding a delegation, or the person at the keyboard.
- The grid still reaches **no network destination of nocx's own**, and no
  unenrolled pane is read at all.

## Why this, and not the obvious alternative

**Why not leave power (1) alone and let the coordinator send a raw keystroke?**
Because that is not a smaller permission, it is an unbounded one: a caller that
may send `\r` may send anything, and the pane it is aimed at is one nocx has
already decided it may not type into. Naming the option instead keeps the
identification and the write in the same place — nocx is the only party that
turns a name into a key, and it does so only against a frame it has just read.

**Why not have the person answer, and tell the coordinator to wait?** It was
the narrower option and it was considered. It fails on the case that produced
this record: the pane is a background tab a person has no reason to be looking
at, and the coordinator is the only party awake. A design where the worker
blocks until somebody happens to look is a worker that hangs.

**Why let a coordinator see the screen at all, when nocx can say what it read?**
Because "what nocx read" is exactly the thing that was wrong. A coordinator
told "your worker is waiting on a question", given a list of options nocx
derived, has no way to notice that nocx misread the screen — and `unknown`, the
verdict for a screen nocx cannot describe at all, is the case where a
description is most needed and least available. The screen is the evidence
behind the verdict, and withholding it leaves the coordinator acting on a
conclusion it cannot check.

**And the cost of that, stated rather than discovered.** A coordinator is a
model. A screen handed to it leaves this machine on its next request, which no
other consumer of the grid does — the indicator stays local, and the
calibration view is a person reading their own screen. This record admits that
egress for panes the coordinator holds a delegation over and for nothing else,
and it is the reason the boundary is the delegation rather than "any enrolled
pane": a person's own terminal, with whatever is on it, is never in scope. The
narrower alternative — a per-participant consent prompt — was rejected as a
prompt nobody could answer usefully: the person is asked about a pane that does
not exist yet, for content nobody has seen, at the moment they are least able
to judge it. `docs/vision.md`'s "no cloud, ever" is about services nocx
operates and does not decide this; what decides it is that the pane in question
exists only because the coordinator asked for it.

## Consequences

- `docs/architecture.md`'s AD-6 amendment carries a new paragraph naming the
  second write case and the closed key set, and the reading that may reach a
  delegation holder. The "two powers" sentence stays true: the powers are still
  two, and one of them now has two cases.
- `internal/agentdriver`'s rule grammar needs a positively identified state for
  a menu whose options are not numbered — the folder-trust dialog misses the
  existing `modal_choice` branch by exactly that predicate. That is
  `nocx-f545a.2`, and it comes with a capture taken from a real agent, because
  a fixture written by the person writing the classifier proves nothing.
- `internal/agenttyping` gains a second entry point beside its text one, taking
  an option name and refusing everything else. It is the same gate, not a
  second door onto a pane's input queue.
- The worker tool surface gains a way to read a held participant's pane and a
  way to answer its menu, each with its own refusals and its own contract in
  `contracts/` (`nocx-f545a.4`, `nocx-f545a.6`).
- A spawn whose pane reaches a menu no longer compensates the pane away: the
  participant reaches a state the coordinator can act on, with its tab standing
  (`nocx-f545a.3`). A pane that reaches `unknown` still fails exactly as it
  does today.
- If a later reader wants a third case for power (1) — a form, a text field
  inside a dialog, a pane a coordinator may type freely into — that is another
  amendment to the AD-6 bullet, on the same terms this one was taken under.
