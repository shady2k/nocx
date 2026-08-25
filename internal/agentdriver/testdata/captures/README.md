# Claude Code, captured in the five states a driver has to tell apart

Real byte streams off a real PTY, produced by the capture harness `nocx-szb40.1` built and
driven by the scripts in `scripts/`. Nothing here is synthesised, for the reason
[ADR-0038](../../../../docs/decisions/0038-x-vt-as-the-backend-emulator.md) gives about its
own corpus and for the one `AGENTS.md` rule 4 gives about tests: a fixture written by the
person writing the classifier encodes that person's model of the TUI, including the parts
that are wrong, and then the code and the test agree and are wrong together.

Replay one to any point with `replay(t, name, atMs)` in `capture_test.go`, which exists
because a driver is built against MOMENTS and every one of them is gone by the end of the
capture. It replays through `internal/panegrid`, so what a test asserts on is a frame
produced the way production produces one.

| capture             | script              | what it holds                                                                       |
| ------------------- | ------------------- | ----------------------------------------------------------------------------------- |
| `claude-idle`       | `idle.script`       | the idle input box — nothing typed, the TUI left to settle (`11000`)                |
| `claude-working`    | `working.script`    | a turn in flight, spinner up, then the same turn finished (`17000` and `44000`)     |
| `claude-permission` | `permission.script` | the Write tool's approval dialog, waiting on a human (`49000`)                      |
| `claude-modal`      | `modal.script`      | the `/model` menu, opened by the user rather than by the agent (`20000`)            |
| `claude-subagent`   | `subagent.script`   | the main turn and a backgrounded Explore agent (`30000`, `40000`, `43000`, `70000`) |

Captured against Claude Code v2.1.238 at 120×40. All five run on the ALTERNATE SCREEN, so
`Frame.AltScreen` is true throughout and distinguishes nothing here.

## Four things the captures decided, which reasoning got wrong

**The approval dialog uses the same glyph as the input marker, and replaces the input box
rather than overlaying it.** Its selected row reads `❯ 1. Yes`; the input row reads `❯ `. A
rule that looks for `❯` and stops has just found an input box in a dialog whose first
option is "Yes". That is the argument for TWO markers — the input row is only an input row
when it sits between the two full-width rules that bound the box, and the dialog has
neither.

**The cursor is the second marker, and it is the one that cannot be forged.** In the
approval dialog the cursor sits ON the selected option's `❯` (1,26); in `/model` it sits on
the same glyph (3,31); in every frame with a live input box it sits just after the input
marker instead. An agent can print `❯ 1. Yes` into its own transcript. It cannot take the
cursor, because the TUI parks it after every repaint.

**The subagent trap is real and has TWO chrome forms, plus an end.** With a backgrounded
agent running the input box is live, the mode footer is present and there is NO spinner.
From 25s to 38s the only chrome saying work is happening is a task panel drawn UNDER the
mode line — `● main` over `◯ Explore  List files in directory`. From 40s that panel
collapses and the mode line carries `· /tasks to see subagents ·` instead. And by 70s the
background agent has finished, the segment is gone, and the pane is genuinely waiting: a
rule anchored on that segment without its second end would leave the pane refusing input
for the rest of the session.

**The spinner is chrome by POSITION _and_ by grammar, and position alone is not enough.**
The spinner is not at a fixed offset: it sits in the status stack directly above the token
meter, and that stack grows — with a tip line present it is three rows above the input
box's top rule rather than two (42s). Worse, the turn's closing summary lands in the same
slot and STAYS there: `✻ Sautéed for 29s` occupies it from 46s to the end of
`claude-subagent`, long after the turn ended. So the stack decides where to look and the
grammar decides what was found: a live spinner always carries `… (` and closes its
parenthesis, and a finished one never does.

## And one thing that is not a signal at all

**The input box's contents are not evidence that a human typed.** `now echo goodbye`,
`wait for the results` and `what's in caps?` all appear in the input row of captures whose
scripts sent none of them — they are the agent's own suggested follow-ups, rendered in the
box. A rule that reads "the input row is non-empty, therefore someone is composing" is
reading the agent's output through a chrome-shaped hole.

## What these do not cover

A crashed or exited agent, which is a process fact and is deliberately not taken from the
screen. Any agent other than Claude Code. Any width other than 120: the input box's rules
are full-width, so a narrow pane is a geometry the driver has to be tested at separately.
