# Claude Code, captured in the five states a driver has to tell apart

Real byte streams off a real PTY, produced by `bin/capture` from the scripts in
`scripts/`. Nothing here is synthesised, for the reason `nocx-szb40.1` gives about its own
corpus and for the one `AGENTS.md` rule 4 gives about tests: a fixture written by the
person writing the classifier encodes that person's model of the TUI, including the parts
that are wrong, and then the code and the test agree and are wrong together.

Replay one to any point with `bin/frameat`, which exists because a driver is built against
MOMENTS and every one of them is gone by the end of the capture:

```
go build -o bin/frameat ./cmd/frameat
bin/frameat -at 17000,44000 captures/claude-working.jsonl
```

| capture             | script               | what it holds                                                                      |
| ------------------- | -------------------- | ---------------------------------------------------------------------------------- |
| `claude-idle`       | `idle.script`        | the idle input box — nothing typed, the TUI left to settle                         |
| `claude-working`    | `working.script`     | a turn in flight, spinner up, then the same turn finished (`-at 17000` and `44000`) |
| `claude-permission` | `permission.script`  | the Write tool's approval dialog, waiting on a human (`-at 49000`)                 |
| `claude-modal`      | `modal.script`       | the `/model` menu, opened by the user rather than by the agent (`-at 20000`)       |
| `claude-subagent`   | `subagent.script`    | the main turn blocked on a backgrounded Explore agent (`-at 25000` and `40000`)    |

Captured against Claude Code v2.1.238 at 120×40. All five run on the ALTERNATE SCREEN, so
`Frame.AltScreen` is true throughout and distinguishes nothing here.

## Three things the captures decided, which reasoning would have got wrong

**The permission dialog uses the same glyph as the input marker, and replaces the input
box rather than overlaying it.** Its selected row reads `❯ 1. Yes`; the input row reads
`❯ `. A rule that looks for `❯` and stops has just found an input box in a dialog whose
first option is "Yes". That is the whole argument for TWO markers — the input row is only
an input row when it sits between the two full-width rules that bound the box, and the
dialog has neither.

**The subagent trap is real and its chrome is narrower than the bead assumed.** With a
backgrounded agent running, the input box is live, the mode footer is present, and there
is NO spinner. The only chrome that says the turn is blocked is a segment inside the mode
line: `· /tasks to see subagents ·`. `✻ Waiting for 1 background agent to finish` is in the
transcript, which is the agent's own output and therefore not admissible as an anchor. So
"input box, no spinner, therefore idle" classifies a blocked agent as ready for input,
which under D14 is a keystroke into whatever appears next.

**The spinner is chrome by POSITION, not by content.** While a turn runs, the spinner sits
on the row directly above the token-counter row, which sits directly above the input box's
top rule. After the turn ends the very same wording stays on screen in the transcript —
`✻ Brewed for 4s` — twenty rows higher. Anchoring on the text finds both; anchoring on the
position finds only the live one. This is the injection-safety rule paying for itself on
the first capture taken, and it is the same failure this repository already measured once,
as a completion sentinel that matched itself because the agent printed the brief it had
just read.

## What these do not cover

A crashed or exited agent, which is a process fact and is deliberately not taken from the
screen. Any agent other than Claude Code. Any width other than 120: the input box's rules
are full-width, so a narrow pane is a geometry the driver has to be tested at separately.
