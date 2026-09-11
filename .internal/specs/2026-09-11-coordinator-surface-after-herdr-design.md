# What nocx lets a coordinator do, measured against herdr — design

- **Status:** Draft, third revision, 2026-09-11. §4 holds the owner's decisions of that day. §6
  designs part 1a in full. §7 is the brief for part 1b, which gets a design of its own. §9 records
  both codex reviews and what became of each finding.
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

15. **Discovery and labels.** A discovery run on the owner's endpoint fixed Claude's startup sequence,
    and the owner fixed the expected state of each screen it found (§6.5, §6.8): security notes and
    the transcript viewer `unknown`, the `/btw` overlay over a running turn `working`. The same run
    found two rule misreads, `nocx-ys9jd` and `nocx-emors`; 1a does not close while they disagree.

## 5. Order of work

1. **1a — classification, verified** (§6), including `nocx-ys9jd` and `nocx-emors`.
2. **1b — pane interaction tools** (§7 is its brief; `nocx-tlaft` is its prerequisite).
3. **Event-driven worker state and the coordinator wake;** `workers.wait` and the drop removed;
   `workers.close` closes the tab.
4. **Spawn preamble and checkpoints.**
5. **Workspace neighbours.**
6. **Hooks as the authoritative tier.**

`nocx-tdiqs` is a standalone bug.

## 6. Part 1a — the classification, verified

### 6.1 What is verified, and what is not

**Verified:** that the shipped rule (`internal/agentdriver/claude.rule.json`) gives the state the
owner fixed in §6.4 at every classification mark of every scenario that ran, on the Claude Code
version installed when 1a closes. **Not verified here:** any write into a pane (1b), worker state
reduction (part 3), hook authority (part 6). A scenario the owner accepted as `unsupported` is listed
as **unverified** in the closing report, never folded into the claim. 1a adds no permission to write
into any pane and no product surface.

**Already found by the discovery run** (§6.8): `nocx-ys9jd` and `nocx-emors`. Both are inside 1a:
it does not close while either disagrees.

### 6.2 Isolation of every Claude a run starts

The discovery run showed what inheritance costs: `cmd/agent-capture` passes its caller's whole
environment except five terminal variables (`pinnedEnvironment`), so a run started from inside a
Claude Code session would hand the captured Claude that session's `CLAUDECODE`,
`CLAUDE_CODE_SESSION_ID` and `CLAUDE_CODE_MESSAGING_TOKEN`.

- **The environment is an allowlist the capture tool enforces**, not a shell wrapper's habit: a new
  `-env-file` replaces inheritance entirely. The file holds `HOME=<group>/home`,
  `CLAUDE_CONFIG_DIR=<group>/claude-config`, `PATH` (the directory of `claude` and the system's
  coreutils only), `TERM`, `LANG`, `ANTHROPIC_BASE_URL=<proxy>` (§6.3), a dummy
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL` and `ANTHROPIC_SMALL_FAST_MODEL` set to one model the
  endpoint lists, `DISABLE_TELEMETRY=1`. Nothing else reaches the process.
- **Settings sources.** `HOME` and `CLAUDE_CONFIG_DIR` are fresh per scenario group; `work/` is
  empty; the only settings passed are `--settings <group>/settings.json` (hooks and the scenario's
  permission rules). `--bare` is not used: it disables hooks.
- **Refusals before anything starts:** a managed settings file exists
  (`/etc/claude-code/managed-settings.json` on Linux, `/Library/Application
Support/ClaudeCode/managed-settings.json` on macOS); any `.claude/` or `CLAUDE.md` exists in
  `work/` or any of its ancestors; `HOME` or `CLAUDE_CONFIG_DIR` is not new and empty; the run root is
  not under `/var/tmp`.
- **Effective configuration is observed, not assumed:** every group's first classification mark is
  the idle screen, whose header row must name the configured model and whose mode line must read
  `manual mode on`; either missing fails the group before any prompt is sent.

### 6.3 The harness

**`cmd/agent-capture capture`, extended where the review found it cannot carry evidence:**

1. **One time domain.** The capture records, besides output chunks, three record kinds on its own
   monotonic clock: `input` (each script step, with the bytes' label and whether the write
   succeeded), `process` (start, exit with status, kill), and `hook` (each hook event the capture
   received). Hooks do not write files with wall-clock times: the settings' hook command writes the
   event JSON to a Unix socket the capture owns, and the capture stamps it on receipt. `Header.Started`
   stays informational.
2. **Bounded end.** The program runs in its own process group. On script end, timeout or error the
   capture signals the group, closes the PTY master after a bounded drain, and always writes what it
   has, ending with an `end` record: `{reason: exited | exited_early | script_ended | timeout | error,
exitStatus, capturedThroughMs, bytes}`. A program that exits before its script finishes is
   `exited_early`, not success.
3. **Completeness.** `agentcapture.Read` requires the `end` record and a parseable `Started`, and
   rejects a capture without them; `Frames` refuses a mark beyond `capturedThroughMs` instead of
   answering the last screen.

**`agent-capture verify`** reuses `agentcapture.Read` and `agentcapture.Frames` for replay and
`agentdriver.Registry.Explain` for the verdict — the entry points the product uses — and replaces the
separate test-only reader in `internal/agentdriver/capture_test.go` with the same parser, so there is
one replay path.

**A fault proxy.** `ANTHROPIC_BASE_URL` points at a loopback proxy the harness starts in front of the
real endpoint. It forwards until a scenario step tells it to fail, then either refuses connections
or stalls, which is how scenario 13 reproduces both retry chromes while pre-flight and startup still
reach a healthy endpoint.

**The procedure** is a skill, `.claude/skills/nocx-detection-verify/`, holding the settings template,
the scenario scripts and the pre-flight; the owner's endpoint is its input, recorded in `nocx-34r0i`.

**The run root** `/var/tmp/nocx-detect-<timestamp>/` holds one directory per scenario group (`home/`,
`claude-config/`, `work/`, `settings.json`, the capture) and the run's `report.jsonl` and
`versions.json` (`claude --version`, model id, nocx commit, proxy mode per scenario).

**Pre-flight, before any process starts; any failure stops the run and records why:** the refusals
of §6.2; `GET /v1/models` through the proxy lists the model; `POST /v1/messages` with one tool
definition answers `stop_reason: tool_use`; the hook socket accepts and returns a probe event; the
report and versions files can be written.

### 6.4 Labels

**A label comes from the scenario, never from a timestamp.** Each classification mark in a scenario
declares the state the owner fixed for it and its **entry evidence**, and verify places the mark only
when all of it holds:

1. the input that causes the state was recorded as written (`input` record, `ok`);
2. the scenario's entry text is present in the replayed frame — a literal string the scenario states
   (for example `Enter to confirm · Esc to cancel` and `Yes, I trust this folder`), checked by plain
   substring and not by the rule under test;
3. where the scenario names a corroborating hook, that hook was received after (1) and before the
   mark, for the same `session_id` and, for tool events, the same tool;
4. no input or hook the scenario names as **closing** the state lies between (1) and the mark;
5. no record of any kind lies within the ambiguity window (100 ms) either side of the mark.

Anything short of that is `unsupported` with the unmet condition named — never an inferred
agreement. A `measurement` row records hook coverage and carries no label; a `process` row records an
exit and its status.

**A disagreement is resolved in one of three places,** and the owner picks which: the rule (the
common case), the scenario's entry evidence, or the harness. Only the first changes
`claude.rule.json`.

### 6.5 The scenarios

Groups share one fresh `HOME`/`CLAUDE_CONFIG_DIR`/`work/`. Every group starts with the measured
startup sequence (§6.8): theme picker → Enter → security notes → Enter → trust question → the group's
answer. Expected states are the owner's (2026-09-11).

| Group | Scenario                    | Reached by                                                                                     | Entry evidence (literal) / corroborating hook                                  | Closing                 | Expected                                                                                |
| ----- | --------------------------- | ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ | ----------------------- | --------------------------------------------------------------------------------------- |
| A     | theme picker                | start                                                                                          | `Choose the text style`                                                        | Enter                   | `modal_choice`                                                                          |
| A     | security notes              | Enter                                                                                          | `Press Enter to continue`                                                      | Enter                   | `unknown`                                                                               |
| A     | folder trust                | Enter                                                                                          | `Yes, I trust this folder`                                                     | Enter                   | `permission_choice`                                                                     |
| A     | trust declined              | Enter on `No, exit`                                                                            | —                                                                              | —                       | process row: exit status 1                                                              |
| B     | idle at 120, 80, 60 columns | trust accepted (Down, Enter); one capture per width                                            | `manual mode on`, the model name in the header                                 | any input               | `free_text`                                                                             |
| B     | turn starting               | a fixed prompt answered at length, marked before the elapsed timer appears                     | `esc to interrupt` / `UserPromptSubmit`                                        | `Stop`                  | `working` (`nocx-ys9jd`)                                                                |
| B     | turn running                | the same turn once `(Ns)` shows                                                                | `esc to interrupt` / `UserPromptSubmit`                                        | `Stop`                  | `working`                                                                               |
| B     | turn finished               | the same turn completing                                                                       | `manual mode on` without `esc to interrupt` / `Stop`                           | any input               | `free_text`                                                                             |
| B     | `/btw` overlay              | `/btw What is 2+2?` during a running turn                                                      | `Esc to close` / `UserPromptSubmit` of the main turn, no `Stop` yet            | Esc                     | `working`                                                                               |
| B     | transcript viewer           | `ctrl+o` at idle                                                                               | `Showing detailed transcript`                                                  | `ctrl+o`                | `unknown`                                                                               |
| B     | `/model` menu               | `/model` at idle                                                                               | the menu's own confirm line, recorded by discovery before the plan             | Esc                     | `modal_choice`                                                                          |
| C     | Bash permission, declined   | settings rule `permissions.ask: ["Bash(touch marker.txt)"]`; prompt asking to run exactly that | `Do you want to proceed?` / `PreToolUse` for Bash, `Notification`              | Esc, after the evidence | `permission_choice`; after Esc, `marker.txt` does not exist                             |
| C     | Bash permission, approved   | the same prompt in a fresh group; confirm the allow option                                     | as above                                                                       | the confirm             | `permission_choice`; after, `marker.txt` exists                                         |
| C     | write permission, declined  | prompt asking to create `note.txt` with fixed content                                          | `Do you want to` / `PreToolUse` for Write, `Notification`                      | Esc, after the evidence | `permission_choice`; after Esc, no `note.txt`                                           |
| D     | background subagent         | prompt asking for the Explore agent with `run_in_background`                                   | the task panel row / `PreToolUse` for the agent tool; `SubagentStop` closes it | `SubagentStop`          | `working` while open; model refusal or foreground run is `unsupported` with that reason |
| E     | API refused, retrying       | a turn submitted, then the proxy refuses                                                       | `Retrying in`                                                                  | proxy restored          | `error`                                                                                 |
| E     | API stalled, waiting        | a turn submitted, then the proxy stalls                                                        | `Waiting for API response`                                                     | proxy restored          | `error` (`nocx-emors`)                                                                  |
| 0     | hook coverage               | every group                                                                                    | —                                                                              | —                       | measurement rows: which events fired, with which fields                                 |

The literal entry texts above come from the discovery captures (§6.8) and the committed corpus; where
a row says "recorded by discovery before the plan", the plan starts with that capture.

### 6.6 From a disagreement to the tree

A `disagree` goes to the owner, who picks where it is resolved (§6.4). A rule change comes with a unit
test on that moment — a committed capture moment or a painted frame — that fails on the previous
rule and passes on the new one; no large fixture suite is added. The corpus's expectations move into
one manifest, `internal/agentdriver/testdata/captures/manifest.json`, naming each mark, its expected
state and, where a test pins it, its expected branch; the rule tests read it and verify compares state
to state and branch to branch separately. The rule is embedded, so re-verification replays the run's
own captures against the rebuilt binary; hot reload stays `nocx-y6w66`.

### 6.7 Acceptance, as assertions

All of these run in CI on mocks — fake programs on a real PTY, a fake endpoint, a fake hook sender —
except (8), which is the live run.

1. **Isolation.** A capture started with `-env-file` gives the program exactly the file's variables:
   a fake program that prints its environment shows none of the caller's `CLAUDE*` variables. Each
   refusal of §6.2 stops the run with no process started, asserted per case; with none of them present
   the run starts.
2. **Pre-flight.** Endpoint unreachable, non-200, invalid JSON, model missing, no `tool_use`, hook
   socket probe lost, report not writable: each stops the run, names the cause, starts nothing. A
   fake endpoint that behaves passes and the capture starts.
3. **Capture lifecycle.** Against fake programs: a normal exit after the script ends `exited`; an exit
   before the script ends `exited_early`; a program that ignores signals and a descendant holding the
   PTY both end `timeout` within the bound, with the capture written; a failed input write records
   `input ok=false` and ends `error`. Each writes an `end` record and keeps the chunks it has.
4. **Capture integrity.** `Read` rejects a capture whose trailing records were removed, one with no
   `end` record, and one with an unparseable `Started`; `Frames` refuses a mark beyond
   `capturedThroughMs`; a complete capture replays. `agentcapture.Write` failing at create, encode,
   close and rename each surface as the run's error with no half-written file in place.
5. **Labels.** On a fake capture with fake hooks: every condition of §6.4 missing in turn yields
   `unsupported` naming it; all present yields the scenario's label; a hook for another `session_id`
   or tool does not corroborate; a record inside the ambiguity window rejects the mark.
6. **One replay path.** `internal/agentdriver` rule tests and `agent-capture verify` read the same
   manifest through `agentcapture.Read`; for every manifest mark both give the manifest's state, and
   the branch where the manifest names one.
7. **End to end on mocks.** A fake agent program that draws a scripted idle screen, a permission
   dialog and a working spinner, with a fake hook sender, goes through capture → verify → report and
   produces `agree` for each mark and a `measurement` row for the hooks.
8. **The live run.** On the Claude Code version installed when 1a closes, through the owner's endpoint:
   `report.jsonl` has every classification row of §6.5 `agree`, or `unsupported` with the owner's
   acceptance recorded and the row listed as unverified; `nocx-ys9jd` and `nocx-emors` are closed with
   their red-then-green tests; replaying the run's captures against the final binary gives the same
   verdicts.
9. **CI on mocks only.** No CI test starts `claude` or reaches a model endpoint.

### 6.8 What the discovery run established (2026-09-11)

Claude Code 2.1.266, `qwen/qwen3.6-35b-a3b` on LM Studio, `agent-capture` under `env -i` with a
fresh `HOME` and `CLAUDE_CONFIG_DIR` in `/var/tmp`, 120×40:

- **Startup sequence:** an unrecognized-model notice on the primary screen; the theme picker; security
  notes with `Press Enter to continue…`; the folder trust question with the cursor on `No, exit`
  (Enter there exits with status 1); the idle alternate screen with the model name in the header and
  `manual mode on`. No login screen, and no API-key prompt with `ANTHROPIC_AUTH_TOKEN`.
- **The rule's readings:** theme picker `modal_choice`; security notes `unknown`; trust
  `permission_choice`; idle `free_text`; transcript viewer `unknown`; `/btw` overlay `unknown`
  (owner's expectation: `working`); a turn before its elapsed timer `free_text` (`nocx-ys9jd`); a turn
  with its timer `working`; `Waiting for API response · will retry in …` `free_text` (`nocx-emors`).

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

### Second review (codex, 2026-09-11, on dcc47e14)

| #   | Finding                                                                           | Disposition                                                                   |
| --- | --------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| 1   | Isolation not established: inherited environment, managed settings, ancestors     | Accepted; verified the inheritance; §6.2                                      |
| 2   | Hook plus repaint is temporal association, not ground truth                       | Accepted; labels from scenario entry evidence; §6.4                           |
| 3   | `Started + AtMs` is not a common clock; inputs are not journalled                 | Accepted; one monotonic domain, hook socket; §6.3                             |
| 4   | Capture kills only the direct process, can hang on a descendant, writes nothing   | Accepted; verified; process group, `end` record; §6.3                         |
| 5   | Scenario 13 contradicts pre-flight                                                | Accepted; fault proxy; §6.3, §6.5 group E                                     |
| 6   | Startup and configuration sequence undefined; owner labels after capture          | Accepted; discovery ran, owner fixed labels; §6.5, §6.8                       |
| 7   | `ls` is auto-allowed, Esc may cancel a turn instead                               | Accepted; explicit ask rule, evidence before Esc, approved pair; §6.5 group C |
| 8   | `/btw` needs a question; a background subagent needs real evidence                | Accepted; §6.5 groups B and D                                                 |
| 9   | Report cannot represent measurements, process outcomes, unverified rows           | Accepted; row kinds and unverified listing; §6.1, §6.4                        |
| 10  | Failing-call matrix and success pairs missing                                     | Accepted; §6.7 (2)–(5), (7)                                                   |
| 11  | A capture with its tail cut still replays                                         | Accepted; verified; `end` record, coverage; §6.3, §6.7 (4)                    |
| 12  | Branch compared to a state; tests assert "not free_text"; duplicate replay reader | Accepted; manifest, one replay path; §6.6, §6.7 (6)                           |
