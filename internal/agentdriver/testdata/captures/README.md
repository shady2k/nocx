# Claude Code, captured in the five states a driver has to tell apart

Real byte streams off a real PTY, produced by `cmd/agent-capture` and driven by the
scripts in `scripts/`. Nothing here is synthesised, for the reason
[ADR-0041](../../../../docs/decisions/0041-x-vt-as-the-backend-emulator.md) gives about its
own corpus and for the one `AGENTS.md` rule 4 gives about tests: a fixture written by the
person writing the classifier encodes that person's model of the TUI, including the parts
that are wrong, and then the code and the test agree and are wrong together.

Replay one in a driver test with `replay(t, name, atMs)` in `capture_test.go`, which exists
because a driver is built against MOMENTS and every one of them is gone by the end of the
capture. To inspect a committed capture from the command line, run:

```sh
go run ./cmd/agent-capture replay -at 49000 internal/agentdriver/testdata/captures/claude-permission.jsonl
```

To take a NEW capture, provide a program and, optionally, a timed-keystroke script:

```sh
go run ./cmd/agent-capture capture \
  -out /tmp/agent-capture.jsonl \
  -script internal/agentdriver/testdata/captures/scripts/permission.script \
  -- claude
go run ./cmd/agent-capture replay -at 49000 /tmp/agent-capture.jsonl
```

For a local smoke capture without an agent, `bash -i` running one command is enough:

```sh
go run ./cmd/agent-capture capture -out /tmp/bash-capture.jsonl -- \
  bash -i -c 'printf "fresh capture\n"'
go run ./cmd/agent-capture replay -at 100 /tmp/bash-capture.jsonl
```

Both replays now go through `internal/panegrid`, so what anything asserts on is a frame
produced the way production produces one. The format itself — the header, the chunks, the
mark arithmetic and the replay — lives in `internal/agentcapture`, because a calibration
set (`internal/agentcalib`, nocx-etejh) is a capture too: one chunk per labelled state and
one mark per label, so `agent-capture replay` reads a person's calibration as readily as it
reads this corpus.

| capture                             | script                              | what it holds                                                                                                                                                                                                                                                                  |
| ----------------------------------- | ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `claude-idle`                       | `idle.script`                       | the idle input box — nothing typed, the TUI left to settle (`11000`)                                                                                                                                                                                                           |
| `claude-idle-60`                    | `idle.script`                       | the same idle input box at the narrow 60×40 geometry (`11000`)                                                                                                                                                                                                                 |
| `claude-idle-80`                    | `idle.script`                       | the same idle input box at the narrow 80×40 geometry (`11000`)                                                                                                                                                                                                                 |
| `claude-working`                    | `working.script`                    | a turn in flight, spinner up, then the same turn finished (`17000` and `44000`)                                                                                                                                                                                                |
| `claude-permission`                 | `permission.script`                 | the Write tool's approval dialog, waiting on a human (`49000`)                                                                                                                                                                                                                 |
| `claude-permission-60`              | `permission.script`                 | the Write tool's approval dialog at 60×40 (`49000`)                                                                                                                                                                                                                            |
| `claude-modal`                      | `modal.script`                      | the `/model` menu, opened by the user rather than by the agent (`20000`)                                                                                                                                                                                                       |
| `claude-subagent`                   | `subagent.script`                   | the main turn and a backgrounded Explore agent (`30000`, `40000`, `43000`, `70000`)                                                                                                                                                                                            |
| `claude-error`                      | `error.script`                      | the TUI's own error: the API unreachable, retrying (`20000`, `30000`, `44000`)                                                                                                                                                                                                 |
| `claude-lmstudio-turn`              | `lmstudio-turn.script`              | onboarding, trust accepted, idle, ctrl+o transcript viewer (40000), a turn before its timer (47500, 49000), /btw overlay (55000, 58000), API waiting (72000)                                                                                                                   |
| `claude-trust`                      | `trust.script`                      | the folder-trust question, raised before the TUI will start in a directory (`11000`)                                                                                                                                                                                           |
| `claude-lmstudio-idle-80`           | `lmstudio-onboarding.script`        | the idle input box at 80×40, on today's Claude (`38000`)                                                                                                                                                                                                                       |
| `claude-lmstudio-idle-60`           | `lmstudio-onboarding.script`        | the idle input box at 60×40, on today's Claude (`38000`)                                                                                                                                                                                                                       |
| `claude-lmstudio-model`             | `lmstudio-onboarding-model.script`  | onboarding, then `/model` typed and submitted: the model picker (`49000`)                                                                                                                                                                                                      |
| `claude-lmstudio-permission`        | `lmstudio-permission.script`        | onboarding, a Bash permission dialog for `touch marker.txt` (spinner with timer at `48000`, the dialog at `49000`, cancelled with Esc), then a Write permission dialog for `note.txt` (`110000`), also cancelled; the box back to idle right after the first cancel (`101000`) |
| `claude-lmstudio-subagent`          | `lmstudio-subagent.script`          | the main turn and a backgrounded Explore agent, still generating above the task panel (`65000`)                                                                                                                                                                                |
| `claude-lmstudio-subagent-finished` | `lmstudio-subagent-finished.script` | the same shape, run long enough to watch the task panel clear (`75000`) and, roughly 20-30s of real time later, the `/tasks to see subagents` mode-line hint clear too (`112000`)                                                                                              |
| `claude-lmstudio-api-refused`       | `lmstudio-api.script`               | the TUI's own error against a dead port, `http://127.0.0.1:9` (`40000`)                                                                                                                                                                                                        |

`claude-error` was captured against v2.1.245 by pointing `ANTHROPIC_BASE_URL` at a dead port,
which is how the error chrome is reproduced without waiting for a real outage. What it settled:
the error is drawn in the SAME slot as the spinner — the status-stack row directly above the
token meter — so position cannot separate them and only the grammar can. Before it existed the
frame classified as `free_text`, not as `unknown`: nocx reported a pane whose agent could not
reach its API as ready for input. Only the RETRYING class of error is captured; quota
exhaustion and overload draw their own chrome and are not in this corpus.

Captured against Claude Code v2.1.238: the baseline five captures are 120×40;
narrow captures cover idle at 60×40 and 80×40, plus the approval dialog at 60×40.
Every capture above runs on the ALTERNATE SCREEN except `claude-trust`, so `Frame.AltScreen`
distinguishes nothing a rule may rely on: the trust question is drawn on the PRIMARY screen,
before the TUI takes the alternate one, and a rule that required the alternate screen for a
menu would miss exactly the menu that blocks an agent from starting at all.

`claude-trust` was captured against v2.1.263, in a directory with no trusted ancestor. That
second condition is not incidental: Claude Code inherits trust from a parent, and `/tmp` was
trusted on the machine that took it, so the same script run under the capture tool's own
scratch directory recorded an idle input box instead. It settled one thing reasoning did not:
the trust question numbers none of its options, so it misses the numbered-menu branches by
exactly that predicate and fell through to `unknown` — the verdict for a screen the driver
could not read — while the driver read every cell of it (`nocx-f545a.2`).

`claude-lmstudio-turn` was captured 2026-09-11 against Claude Code 2.1.266 with
`qwen/qwen3.6-35b-a3b` on LM Studio through `ANTHROPIC_BASE_URL`, under `env -i` with a
fresh `HOME` and `CLAUDE_CONFIG_DIR` in `/var/tmp`, 120×40.

## Re-recorded for nocx-nru89.2, on Claude Code 2.1.266

The `.claude/skills/nocx-detection-verify/SKILL.md` skill, and its `record.sh`, replay the
onboarding sequence — theme, security notes, folder trust, idle — for free in every one of
the `claude-lmstudio-*` captures below, because that sequence is the first four steps of
every one of their scripts. So `claude-lmstudio-turn` alone carries the recorded marks for
`theme-picker` (`8000`), `security-notes` (`17000`), `folder-trust` (`20500`) and
`transcript-viewer` (`40000`, opened by the `\x0f\x0f` steps of `lmstudio-turn.script`) —
none of those needed a dedicated recording.

`claude-lmstudio-idle-80`, `claude-lmstudio-idle-60`, `claude-lmstudio-model`,
`claude-lmstudio-permission`, `claude-lmstudio-subagent`, `claude-lmstudio-subagent-finished`
and `claude-lmstudio-api-refused` were all captured 2026-09-11 against Claude Code 2.1.266
with `qwen/qwen3.6-35b-a3b` on LM Studio (`http://192.168.0.202:1234`, except
`claude-lmstudio-api-refused` against `http://127.0.0.1:9`), each in its own
`/var/tmp/nocx-detect-*` run with a fresh `HOME` and `CLAUDE_CONFIG_DIR`, via `record.sh`.
Every prior recorded entry for these moments, from Claude Code 2.1.238/2.1.245, is kept as
an anonymous (no `moment`) regression entry in `manifest.json` rather than deleted.

**The Bash and Write permission dialogs both live in one capture.** The local model, asked
to "run exactly this shell command," used the Bash tool as asked, giving `bash-permission`
at `49000` with only two options (`1. Yes` / `2. No`) because the run's `--settings` file
names `Bash(touch marker.txt)` under `ask`. The write-file request went through the Write
tool as expected, giving `write-permission` at `110000`. Both were left with Esc — the
recording never approved a tool call.

**The Bash dialog's own layout reflows under your feet.** Between the dialog first drawing
(`49000`) and roughly `58000`, the collapsing of a multi-line "thinking" preview above it
shifts every row below up by one, and by `60000` the option row's text has visibly
corrupted (`❯ 1. Yes` becomes ` marker.txt creation`) while the cursor position is
unchanged — a real rendering artifact of this Claude version under heavy relative-cursor
ANSI, not a nocx defect. The mark for `bash-permission` is placed at `49000`, before either
of these repaints, where the dialog is clean.

**The subagent's `/tasks to see subagents` mode-line hint outlives the task panel by
roughly 20-30s of real time, not until the next interaction.** `claude-lmstudio-subagent`
(scripted exactly per `lmstudio-subagent.script`) shows the task panel from about `50000` to
`90000` and the main turn's own completion by `90000`, but 47s of subsequent silence on the
wire never clears the hint before the run ends — a plain re-run under the same script can
reach `subagent-finished` and can also not, depending on how long the process is left
running past completion. `claude-lmstudio-subagent-finished` extends the same script's final
wait and is long enough to watch the hint itself clear at idle, around `110000`, entirely
without a second keystroke — so the clearing is a matter of enough real time elapsing, not
of a follow-up turn. `subagent-finished` is marked on that capture, at `112000`.

**`turn-with-timer` and `turn-finished` are sourced from `claude-lmstudio-permission`
instead of a dedicated recording**, because its first Bash-tool turn already draws a timed
spinner (`✻ Blanching… (9s · thinking)`, `48000`) and, once the dialog is cancelled with Esc,
the same box returns to `free_text` without leaving the alternate screen (`101000`) — the
same predicate the old `claude-working` capture existed to cover.

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
screen. Any agent other than Claude Code. At 60×40, idle and approval-dialog states are
covered; at 80×40, idle is covered. Other states at narrow widths and other widths remain
untested.
