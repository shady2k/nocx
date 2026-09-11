# What nocx lets a coordinator do, measured against herdr — design

- **Status:** Draft, second revision, 2026-09-11. §4 holds the owner's decisions of that day. §6
  designs part 1a in full. §7 is the brief for part 1b, which gets a design of its own. §9 records
  the codex review of the first revision and what became of each finding.
- **Brainstorm bead:** `nocx-34r0i` (its notes carry the decisions verbatim).
- **Triggered by:** `nocx-9f1d4`, with `nocx-tlaft` and `nocx-tdiqs` found in the same run.

## 1. Why this document exists

**What the owner saw, 2026-09-11.** A coordinator Claude (session `a71215c4`) called
`workers.spawn` with a task telling its worker to write `NOCX_AGENT_REPORT`, then
`workers.wait(600)`. The worker (session `b8c6748d`) wrote `ok` and a summary at 10:43:46Z and
finished its turn at 10:44:00Z. The wait did not return; it came back at 10:48:14Z only because the
coordinator's own Claude was closed and the MCP connection dropped. `nocx.log` records
`worker participant reported ok=true` at 13:48:20 local — when the worker's Claude was closed too.
Meanwhile `workers.screen`, called to find out why, sat behind the wait (`nocx-tlaft`), and the
worker's tab opened to the left of the coordinator's (`nocx-tdiqs`).

**Why it could not have worked.** The shell wrapper sends a declaration only after the agent
returns (`internal/shellintegration/scripts/nocx.bash`, `__nocx_agent_run`), per
`docs/lifecycle-protocol.md` §16. `workers.spawn` starts an interactive Claude, which does not exit
when its turn ends. The coordinator waits for a declaration, the declaration waits for an exit, and
the exit waits for the coordinator's `workers.close`.

**How the answer was lost.** The 2026-08-15 design
(`.internal/specs/2026-08-15-workspaces-lineage-and-orchestration-design.md`) had it: **D11**, state
is evidence and not a value, with a provenance table in which a hook on the authenticated channel is
`declared` and a pattern match on the screen is `inferred`. On 2026-09-04 the owner confirmed that
hooks are staged at launch. Later designs narrowed that without the owner asking: **D6** and **D9**
of `2026-08-24-orchestration-mechanism-design.md` ("no vendor-specific route carries anything
required"; "the screen decides typing and lighting, and nothing else") and **D5** of
`2026-09-05-the-tool-surface-at-launch-design.md` ("hooks are optional and non-load-bearing"). No
hook is staged anywhere in the tree today.

## 2. The references

### 2.1 herdr (`~/repos/herdr`, v0.8.2)

- **One status authority per pane** (`agents.mdx`, "Status authority"): lifecycle hooks when
  installed and reporting, otherwise a screen manifest over the bottom of the buffer. For Claude
  Code the manifest is the authority; Claude's hooks give herdr session identity only.
- **Control surface** (`agent-automation.mdx`): `agent wait`, `agent prompt [--wait]` (submits while
  the agent works), `agent read`, `agent send-keys` (any logical key).
- **How detection is kept correct** (`AGENTS.md`, "Agent Detection Updates"): CI holds unit tests
  of manifest mechanics with short inline screens and no large per-agent fixture suites; rule
  evidence is gathered live, outside CI, by driving the real agent in a throwaway session
  (`.agents/skills/herdr-throwaway-repro`).
- **Why herdr is not a comparator here.** Its Claude `working` rules are `osc_title_working` and
  `btw_overlay_working` only (`src/detect/manifests/claude.toml`); `agent explain --file` evaluates
  text with empty title and progress inputs (`src/detect/manifest.rs`), so an ordinary working
  screen reads there as `idle`; and it has no `error` state. Its method is taken, its verdicts are
  not.

### 2.2 nelix (`~/repos/nelix`) — keys into a screen that keeps changing

Nelix normalises a frame (chrome zeroed) and fingerprints it whole and by region
(`daemon/fingerprints.py`); a modal's identity is `(prompt kind, options, body fingerprint)`. On the
queued pre-delivery answer path, one monitor thread is the sole writer: it re-observes and writes
only when the on-screen modal has the answer's identity and a non-empty body fingerprint, otherwise
aborts with nothing typed (`daemon/session.py`, `_drain_pending_answer`). Free text on the
monitor-owned path is written while the input box is on screen and confirmed from later frames —
the echo leaving, or a high-confidence state transition — with a stuck echo escalated
(`_drain_pending_submit`). Other post-delivery answers still write from the RPC thread. What 1b
takes from it is the separation of identity from the preconditions of an action, not the claim that
observation and input can be made atomic: they cannot, because the application runs on its own.

## 3. Binding documents this crosses

| Document                                         | What it decided                                                                                                                                 | Where this design touches it                                                                                                                                                              |
| ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6** amendment, `docs/architecture.md`       | A grid decides two powers only; power (1) has two cases — text into `free_text`, keys from a menu's closed set; the grid assigns no wave state. | **1a: untouched** (no write). **1b: amended** — arbitrary keys and input during a turn are new cases of power (1). **Part 2: amended** — worker state takes inferred and declared events. |
| **AD-1**                                         | Raw bytes on the data plane, JSON-RPC on the control plane.                                                                                     | 1b: keys are semantic control operations encoded at the input seam, never raw PTY bytes carried over JSON-RPC.                                                                            |
| **AD-7**, **AD-8**                               | Server-authoritative sessions; every module behind an interface at one composition root.                                                        | 1b: an own-session and a delegated-session capability behind one interface, wired at the root.                                                                                            |
| **ADR-0024** decision 2                          | The authenticated channel carries enrolment and declarations.                                                                                   | Kept. The carrier for hook events is part 6's decision.                                                                                                                                   |
| **ADR-0029** (proposed)                          | A keystroke is bound to what makes it meaningful.                                                                                               | 1b must settle it: accept, supersede or fold into the new ADR.                                                                                                                            |
| **ADR-0063**, **ADR-0064**                       | Typing refused on evidence against the rule; answers named by option text; reads only by the holding session.                                   | 1b supersedes ADR-0064 §1's closed key set and must re-home its answer path (selection check, owed task). §2's read boundary is kept.                                                     |
| **ADR-0020**                                     | Authority granted per run.                                                                                                                      | 1b: which sessions a tool reaches is the capability's answer, never the tool's name.                                                                                                      |
| 2026-08-15 design §6, **D11**, **D12**           | One dispatcher, two callers; state is evidence, reduced over, never one field with source priority; rules local.                                | §6 keeps D12 (no catalogue). Part 2 and part 6 restore D11 including its rejection of a priority field: hook authority needs activation, expiry and per-turn identity.                    |
| 2026-08-24 design **D6**, **D9**, §7.2           | Hooks carry nothing required; only exit and declaration decide state; `wait` is a convenience.                                                  | Superseded in parts 2 and 6 by new records.                                                                                                                                               |
| 2026-09-05 design **D5**                         | Hooks optional and non-load-bearing.                                                                                                            | §6 stages hooks only as measurement labels in a throwaway run; part 6 supersedes D5.                                                                                                      |
| 2026-09-03 mesh design **M1**, **M2**, **P1–P7** | Talk is mesh, act is star; checkpoints wake nobody and are expressly **not** completion reports; structural reporting kept.                     | Part 3 conflicts with P1's "not a completion report" if the drop goes (§4.5): part 2/3 must decide what carries a final outcome and supersede the provision it replaces.                  |
| `AGENTS.md` testing rules 1–5                    | User-path tests, a happy path per epic, failure paths as intervals, independent tests, the wire in the contract.                                | §6.4 for 1a; §7 carries them into 1b's brief.                                                                                                                                             |

## 4. Decisions (owner, 2026-09-11)

1. **Worker state is two-tier.** Hook-declared events are authoritative while the agent's hooks
   report; the screen classifier (`internal/paneobserve`, `internal/agentdriver`) is the fallback;
   process exit is its own event. Screen detection is verified first, hooks after.
2. **nocx wakes the coordinator without an LLM call** on blocked, turn finished and exited, by typing
   a pointer line into its pane, never the worker's content (`internal/workers/backstop.go`
   `wakeText`).
3. **At spawn nocx gives the worker a preamble:** its coordinator, how to reach it, its tools.
4. **Checkpoints** come from the worker through a tool, per P1–P7.
5. **`workers.wait` is removed; the `NOCX_AGENT_REPORT` drop is removed,** a worker's final word being
   its last checkpoint and its screen — subject to the mesh conflict in §3, settled in part 2/3.
6. **Scope.** Over its own workers a coordinator has every right. Agents enrolled in the same nocx
   **workspace** are neighbours: listed, reachable by quiet mail and a nocx pointer wake only, never
   typed content, because a turn-starting message borrows the recipient's permissions. Plain shells
   are never listed.
7. **A message has two deliveries:** at the next free prompt, and during a turn.
8. **`workers.close` closes the tab too;** a worker's tab opens right of its coordinator's; the MCP
   bridge serves calls concurrently.
9. **Any key may be sent,** conditional on what the caller saw, not on the frame.
10. **Interaction with a pane lives in `session.*`,** one tool per act for both callers.
11. **Part 1 is split.** 1a verifies classification with no new write power; 1b is the pane
    interaction tools. The owner verifies after the whole implementation, not between them.
12. **CI runs mocks only;** correctness is verified live, outside CI, by a repeatable procedure.
13. **herdr is not a comparator** (§2.1).
14. **Live runs never touch the owner's Claude account or configuration.** Claude Code talks to the
    owner's LM Studio (Anthropic-compatible `/v1/messages`) with a fresh `CLAUDE_CONFIG_DIR` per run.
    Verified 2026-09-11: `/v1/messages` answered 200, a tool definition produced
    `stop_reason: tool_use`, and Claude Code 2.1.266 in print mode answered `ok` from a fresh config
    directory, printing an unrecognized-model notice.

## 5. Order of work

1. **1a — classification, verified** (§6).
2. **1b — pane interaction tools** (§7 is its brief; `nocx-tlaft` is its prerequisite).
3. **Event-driven worker state and the coordinator wake;** `workers.wait` and the drop removed;
   `workers.close` closes the tab.
4. **Spawn preamble and checkpoints.**
5. **Workspace neighbours.**
6. **Hooks as the authoritative tier.**

`nocx-tdiqs` is a standalone bug.

## 6. Part 1a — the classification, verified

### 6.1 What is verified, and what is not

Verified: that the shipped rule (`internal/agentdriver/claude.rule.json`) classifies every state in
§6.3 correctly on the Claude Code version installed at the time of the run. Not verified here: any
write into a pane (1b), worker state reduction (part 3) or hook authority (part 6). 1a adds no
permission to write into any pane and no product surface.

### 6.2 The harness

**Driving Claude: `cmd/agent-capture`, unchanged in how it drives.** It already runs a program on a
real PTY, sends a timed-keystroke script (`<delayMs> [input]` per line), and records every chunk
with its offset from `Header.Started`; the corpus in `internal/agentdriver/testdata/captures` was
made by it. Replay goes through `internal/panegrid`, the path the product uses.

**One new subcommand, `agent-capture verify`.** Inputs: a capture, its hook log and the scenario's
marks. For each mark it replays the moment through `panegrid`, classifies it with the shipped rule
through the same entry point the rule tests use, takes the `agentdriver.Explanation`, computes the
label (§6.3), and appends one line to `report.jsonl`:

```
{scenario, markMs, label: {state, source: "hook"|"script", evidence}, verdict: "agree"|"disagree"|"unsupported"|"failed",
 nocx: {state, matchedBranch}, rows, reason}
```

**The procedure: a skill, `.claude/skills/nocx-detection-verify/`,** with the scenario scripts and
the pre-flight script, so the run is the same after every Claude update.

**The run directory,** `/var/tmp/nocx-detect-<timestamp>/`: `work/` (the agent's cwd),
`claude-config/` (a fresh `CLAUDE_CONFIG_DIR`), `hooks.json`, `hook-events.jsonl`, one capture per
scenario, `report.jsonl`, `versions.json`.

**Environment of every Claude started:** `CLAUDE_CONFIG_DIR=<run>/claude-config`,
`ANTHROPIC_BASE_URL=<endpoint>` (the skill's input; the owner's LM Studio is recorded in
`nocx-34r0i`), a dummy `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL` and `ANTHROPIC_SMALL_FAST_MODEL`
set to one model the endpoint lists, and `--settings <run>/hooks.json`. With a fresh config
directory and an empty `work/`, `hooks.json` is the only settings source there is, so no merge with
the person's settings can occur, no permission was granted before the run, and no credential
exists to use.

**Pre-flight, before any keystroke; any failure stops the run with nothing started:**

1. The run directory is new and under `/var/tmp`; `claude-config/` and `work/` are empty.
2. `GET <endpoint>/v1/models` answers and lists the configured model.
3. `POST <endpoint>/v1/messages` with one tool definition answers `stop_reason: tool_use`.
4. `hook-events.jsonl` is writable and a probe hook line lands in it.
5. `versions.json` records `claude --version`, the model id, the nocx commit.

**Labels, and when a label is not one.** The hook command appends
`{event, atMs (wall clock), session_id, tool_name, notification_type}`. `Header.Started` plus a
chunk's `AtMs` is the same clock. A mark is labelled from a hook only when the last relevant event
precedes the mark, no contrary event lies between them, and the screen changed between the event
and the mark. Otherwise the mark is labelled from the script's causation if the scenario names one,
and `unsupported` if not. A missing hook, a denied tool or an unchanged screen yields `unsupported`
or `failed`, never an inferred agreement. Which hooks fire in an interactive Claude is itself the
first measurement (§6.3, scenario 0), because the only measurement so far is of a print-mode turn.

### 6.3 The scenarios

Expected states for the rows marked **owner** are fixed by the owner before the plan is written, so
no row is decided after its result is seen.

| #   | Scenario                            | Reached by                                                                                        | Label from                          | nocx expected                                                         |
| --- | ----------------------------------- | ------------------------------------------------------------------------------------------------- | ----------------------------------- | --------------------------------------------------------------------- |
| 0   | hook coverage                       | one short prompt with a Bash call declined                                                        | —                                   | measurement only: which events fire, with which fields                |
| 1   | first-run screens of a fresh config | starting Claude with `claude-config/` empty                                                       | script                              | **owner**, once the screens are captured                              |
| 2   | folder trust, answered yes          | fresh `work/` (untrusted: the config directory trusts nothing); select "Yes, I trust this folder" | script                              | `permission_choice` (`TestTheFolderTrustQuestionIsAPermissionChoice`) |
| 3   | folder trust, answered no           | as 2, select "No, exit"; Claude exits                                                             | script, then process exit           | `permission_choice`, then nothing enrolled                            |
| 4   | idle at 120, 80 and 60 columns      | after 2, nothing typed                                                                            | script; `Stop` absent               | `free_text`                                                           |
| 5   | a turn in flight                    | a prompt the model answers at length                                                              | `UserPromptSubmit`, screen changed  | `working`                                                             |
| 6   | the turn finished                   | 5 completing                                                                                      | `Stop`, screen changed              | `free_text`                                                           |
| 7   | Bash permission                     | a prompt asking to run `ls` in `work/`; declined with Esc                                         | `Notification` if scenario 0 has it | `permission_choice`                                                   |
| 8   | write permission                    | a prompt asking to create `work/note.txt`; declined with Esc                                      | `Notification` if scenario 0 has it | `permission_choice`                                                   |
| 9   | `/model` menu                       | typing `/model`                                                                                   | script                              | `modal_choice`                                                        |
| 10  | transcript viewer                   | `ctrl+o` at idle                                                                                  | script                              | **owner**                                                             |
| 11  | `/btw` overlay                      | `/btw` during 5                                                                                   | script                              | **owner**                                                             |
| 12  | background subagent                 | a prompt asking for an Explore agent                                                              | `PreToolUse` with the agent tool    | `working`; `unsupported` if the model never starts one                |
| 13  | API unreachable, retrying           | `ANTHROPIC_BASE_URL` at a closed loopback port in a separate run directory                        | script                              | `error`                                                               |

A scenario whose state cannot be reached on the model or version at hand is recorded `unsupported`
with its reason, and part 1a does not close with an `unsupported` row the owner has not accepted.

### 6.4 From a disagreement to the tree

A `disagree` goes to the owner. Each decided one becomes a rule change and a unit test on that
moment — a committed capture moment or a painted text frame in `internal/agentdriver` tests — that
fails on the previous rule and passes on the new one. No large fixture suite is added. The rule is
embedded (`go:embed`), so the verification re-runs by replaying the run's own captures with
`agent-capture verify` against the rebuilt binary; hot reload stays `nocx-y6w66`.

### 6.5 Acceptance, as assertions

1. **Pre-flight refuses** — tested against a fake endpoint and temporary directories, each case
   asserting no `claude` process was started and no capture written: run directory not new or not
   under `/var/tmp`; non-empty `claude-config/`; `/v1/models` unreachable or missing the model;
   `/v1/messages` answering without `tool_use`; hook log not writable. **And on a normal machine it
   succeeds:** the same checks against a fake endpoint that behaves pass and start the capture.
2. **Labels are never inferred** — `agent-capture verify` on a committed capture with a synthetic
   hook log: a matching event followed by a screen change labels the mark; a missing event, a
   contrary event in between, or no screen change after the event yields `unsupported`; a
   truncated capture (offsets disagree) yields `failed`.
3. **One classification path** — for every mark of every committed capture, `verify`'s nocx state and
   matched branch equal the state the rule tests assert for that moment.
4. **The live run is complete** — the run on the Claude version installed when 1a closes produces a
   `report.jsonl` with every row of §6.3 either `agree` or an `unsupported` the owner accepted, with
   `versions.json` recorded; every earlier `disagree` has its red-then-green unit test from §6.4,
   and replaying the run's captures against the final binary gives `agree` on those rows.
5. **Failure mid-run** — the endpoint closing during a scenario, a capture hitting its timeout, and a
   Claude process exiting early each record `failed` with a reason, keep the capture, and continue
   with the next scenario; tested with a fake endpoint and a fake program.
6. **CI stays on mocks** — no test in CI starts `claude` or reaches a network model endpoint.

## 7. Part 1b — brief for its own design

Section A of the first revision (approved in intent by the owner) moves pane interaction into
`session.read`, `session.message` and `session.keys` for both callers, removing `workers.screen` and
`workers.answer`. The review showed it cannot be built as written. Its design must satisfy:

1. **The race.** `Typist` re-reads the frame and then only `Accept`s bytes into the session's write
   queue; a separate goroutine writes them later (`internal/app/panetyping.go`,
   `internal/session/session.go` `startWriteLoop`). Conditional operations go through the session's
   single writer and are revalidated when consumed; competing automated operations on one pane are
   serialised; the result reports what was actually written; and the design says plainly which race
   with the running application remains.
2. **Preconditions, not only identity.** A menu's identity does not say what Enter will do: the
   selection can move while question, options and body stay the same (`agenttyping.go` `confirm`
   checks the selection today). Confirmation binds the selected option, text submission the input
   contents, every operation the session and enrolment incarnation; each key in a sequence is
   revalidated. ADR-0029 is settled in the same record.
3. **Menu extraction first.** The Claude rule's only extractor is `subagents`; `ReadMenu` derives
   options around the cursor with no question or body boundary. Question, options and body regions
   per supported menu are added to the rule engine, and `ReadMenu` and the target both consume that
   one result, with a real menu answered successfully beside the unbound-body refusal.
4. **AD-6 amended in 1b,** naming the ADR-0064 provisions superseded, and stating whether an
   unenrolled pane refuses the write tools or gets a separately designed renderer-owned gate —
   never a backend classifier for it.
5. **Capabilities.** `resourceSession` resolves only `runCtx.Session`; the coordinator's grant holds
   its own session; worker access narrows to `WorkerCoordinator` through the record. One capability
   interface with an own-session and a delegated-worker implementation, server-side resolution of
   session to participant, revocation checked throughout a waiting operation.
6. **Approval of opaque input** for the built-in assistant: `session.run`'s approval derives effects
   from `CommandArg`; a key has none. Conservative effects, a readable action and target in the
   existing approval flow, revalidation after approval; permit, refuse, decline, resume and
   target-changed paths tested.
7. **One read, two sources, a contract.** `session.read` keeps its item and window behaviour and its
   existing schema (`contracts/tools/session.read.schema.json`); rows, reading and target come from
   one snapshot; normalisation is stated (the worker path right-trims, the renderer keeps blanks);
   missing renderer, missing grid and unknown classification are distinct outcomes.
8. **The answer path re-homed.** Answering a spawn-blocking question today also delivers the task
   spawn left owed (`workerAnswerer.withOwedTask`), after waiting for selection movement and the menu
   to settle; that survives `workers.answer`'s removal, exactly once, with partial failure and retry.
9. **Messages as a state machine.** `panegrid.Frame` has no generation or offset, so "frames after
   the write" needs an ordering mechanism; outcomes are refused, partially typed, written, submission
   confirmed or unconfirmed; stale echo, multiline and truncated paste, cancellation and duplicates
   are specified.
10. **Concurrency is a prerequisite:** a waiting `when=free` must not stop the same coordinator
    reading the pane or answering the menu that blocks it. `nocx-tlaft` (the bridge's chain and the
    endpoint's one-caller-per-session rule) lands first, and 1b proves a read and a corrective key
    through the real bridge and endpoint while another call waits.
11. **Tests:** user-path assertions that pass, not merely exist; a mock-agent end-to-end test through
    production wiring; the failing-call matrix (renderer request, lost or malformed frame, delegation
    store, revoked grant during a wait, queue refusal, PTY write failure, disconnect after paste) with
    bytes written, remaining input, outcome and cleanup for each; each paired with its ordinary
    success.

## 8. Open for later parts

- **Final outcome and terminal state** (parts 3–4): without a declaration an exit reduces to
  `abandoned` (`internal/workers/registrar.go` `reduce`), and a terminal participant is no longer
  readable through `Screen`; turn completion, exit, declared outcome and checkpoint need separate
  semantics before the drop is deleted.
- **Hook authority** (part 6): activation, expiry, per-turn identity, stale-event rejection and the
  hand-over to fresh screen evidence, with the case of a `Stop` followed by unhooked activity.
- **The carrier for hook events** (part 6).

## 9. Review of the first revision (codex, 2026-09-11)

Every claim below was checked against the tree before it was accepted.

| #   | Finding                                                                     | Disposition                                                             |
| --- | --------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| 1   | Re-check before `Accept` does not close the race; writes are queued         | Accepted; §7.1                                                          |
| 2   | Menu identity does not bind the selection, input contents or incarnation    | Accepted; §7.2                                                          |
| 3   | No menu extractor exists to compute a menu target                           | Accepted; §7.3                                                          |
| 4   | AD-6 must be amended where the new write cases land, not in part 2          | Accepted; §3, §7.4                                                      |
| 5   | A throwaway cwd does not isolate Claude's home, credentials and settings    | Accepted; §4.14, §6.2 (LM Studio, fresh `CLAUDE_CONFIG_DIR`)            |
| 6   | The authority table is not implementable on the current narrowing           | Accepted; §3, §7.5                                                      |
| 7   | Declaring `session.run`'s effects does not carry its approval semantics     | Accepted; §7.6                                                          |
| 8   | Merging the reads needs a source contract                                   | Accepted; §7.7                                                          |
| 9   | Removing `workers.answer` drops the owed-task delivery                      | Accepted; §7.8                                                          |
| 10  | `session.message` has no state machine                                      | Accepted; §7.9                                                          |
| 11  | Concurrency is a prerequisite                                               | Accepted; §5, §7.10                                                     |
| 12  | Trust inheritance, "No, exit" and `coordinatorCwd` break the state sequence | Accepted; §6.3 scenarios 2–3 in a fresh config; 1a needs no coordinator |
| 13  | Hook timestamps are not labels by themselves                                | Accepted; §6.2 label rule, scenario 0                                   |
| 14  | herdr's file verdict is partial and its working row was wrong               | Accepted; herdr dropped (§2.1, §4.13)                                   |
| 15  | Acceptance permits a green review without a working feature                 | Accepted; §6.5.4, §7.11                                                 |
| 16  | Failing-call matrix and success pairs missing                               | Accepted; §6.5.1, §6.5.5, §7.11                                         |
| 17  | Removing the declaration conflicts with the mesh design and `reduce`        | Accepted; §3, §8                                                        |
| 18  | Two-tier authority does not restore D11 as stated                           | Accepted; §3, §8                                                        |
| 19  | §2.2 overstated nelix                                                       | Accepted; §2.2 rewritten                                                |
