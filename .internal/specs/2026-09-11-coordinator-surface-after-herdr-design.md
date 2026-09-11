# What nocx lets a coordinator do, measured against herdr — design

- **Status:** Draft for review, 2026-09-11. Decisions in §4 are the owner's, taken in conversation
  the same day; §6 is part 1's design, section A approved, section B drafted from decisions §4.9–§4.10.
- **Brainstorm bead:** `nocx-34r0i` (its notes carry the decisions verbatim).
- **Triggered by:** `nocx-9f1d4`, with `nocx-tlaft` and `nocx-tdiqs` found in the same run.

## 1. Why this document exists

**What the owner saw, 2026-09-11.** A coordinator Claude (session `a71215c4`) called
`workers.spawn` with a task telling its worker to write `NOCX_AGENT_REPORT`, then
`workers.wait(600)`. The worker (session `b8c6748d`) wrote `ok` and a summary at 10:43:46Z and
finished its turn at 10:44:00Z. The wait did not return; it came back at 10:48:14Z only because
the coordinator's own Claude was closed and the MCP connection dropped. `nocx.log` records
`worker participant reported ok=true` at 13:48:20 local — when the worker's Claude was closed too.
Meanwhile `workers.screen`, called to find out why, sat behind the wait (`nocx-tlaft`), and the
worker's tab opened to the left of the coordinator's (`nocx-tdiqs`).

**Why it could not have worked.** The shell wrapper sends a declaration only after the agent
returns (`internal/shellintegration/scripts/nocx.bash`, `__nocx_agent_run`), per
`docs/lifecycle-protocol.md` §16. `workers.spawn` starts an interactive Claude, which does not exit
when its turn ends. So the coordinator waits for a declaration, the declaration waits for an exit,
and the exit waits for the coordinator's `workers.close`.

**How the answer was lost.** The 2026-08-15 design
(`.internal/specs/2026-08-15-workspaces-lineage-and-orchestration-design.md`) had it: **D11** —
state is evidence, not a value — and a provenance table in which a hook on the authenticated channel
is `declared` and a pattern match on the screen is `inferred`. On 2026-09-04 the owner confirmed
that hooks are staged at launch. The later designs narrowed that without the owner asking for it:
**D6** and **D9** of `2026-08-24-orchestration-mechanism-design.md` ("no vendor-specific route
carries anything required"; "the screen decides typing and lighting, and nothing else") and **D5**
of `2026-09-05-the-tool-surface-at-launch-design.md` ("hooks are optional and non-load-bearing").
No hook is staged anywhere in the tree today. The product nocx replaces does the opposite, and
this document starts from what it does.

## 2. The references

### 2.1 herdr (`~/repos/herdr`, v0.8.2)

- **One status authority per pane** (`docs/.../agents.mdx`, "Status authority"): an agent's
  lifecycle hooks when installed and reporting; otherwise a screen manifest evaluated against the
  bottom of the buffer. For Claude Code the manifest is the authority
  (`src/detect/manifests/claude.toml`): title spinner and `/btw` are `working`, the prompt box is
  `idle`, forms and permission prompts are `blocked`, the transcript viewer and model picker are
  `unknown` with `skip_state_update`. Claude's hooks give herdr session identity only.
- **States** `idle | working | blocked | done | unknown`; `done` is `idle` not yet seen.
- **Control surface** (`agent-automation.mdx`): `agent wait`, `agent prompt [--wait]` (submits
  while the agent is working), `agent read`, `agent send-keys` (any logical key), `pane wait-output`.
- **How detection is kept correct** (`AGENTS.md`, "Agent Detection Updates"): CI holds unit tests
  of manifest mechanics with short inline screens, and explicitly no large per-agent full-screen
  fixture suites. Rule evidence is gathered live, outside CI, in a throwaway session
  (`.agents/skills/herdr-throwaway-repro`): drive the real agent into the state, read it with
  `agent read --source detection`, inspect it with `agent explain --json`, edit the manifest as a
  local override, hot-reload, verify, restore the override, commit. Paid tokens only with approval, cheap model, nothing
  destructive approved.

### 2.2 nelix (`~/repos/nelix`) — sending keys into a screen that keeps changing

A frame is never compared whole: spinners and timers change it continuously. The driver normalises
the frame (chrome zeroed) and fingerprints regions (`daemon/fingerprints.py`). A modal's identity
is `(prompt kind, options, body fingerprint)`. An answer is enqueued with the identity it targets;
one monitor thread is the sole writer and re-observes the frame immediately before writing — same
identity, write; different modal, no modal or no body fingerprint, abort with nothing typed
(`daemon/session.py`, `_drain_pending_answer`). Free text is written only while the input box is
on screen and confirmed from frames produced after the write: the echo appears and leaves
(`_drain_pending_submit`), and an echo that never leaves is escalated.

## 3. Binding documents this crosses

| Document                                         | What it decided                                                                                                                                 | What this design does                                                                                                                                                                                    |
| ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6** amendment, `docs/architecture.md`       | An enrolled pane's grid may decide exactly two things: whether nocx may write, and the indicator. It "may not … assign status to a wave state". | **Amended, deliberately** (part 2): worker state takes screen-inferred and hook-declared events. Part 1 restates power (1): a write is permitted by a re-classified **target**, not only by `free_text`. |
| **ADR-0024** decision 2                          | The authenticated channel carries enrolment and declarations; no bearer material in the environment.                                            | Kept. The carrier for hook events is decided in part 6, not here.                                                                                                                                        |
| **ADR-0063**                                     | Typing is refused on evidence against the rule, not on its absence; the frame is re-read before a write.                                        | Kept and extended: re-classification before every write, now against a named target.                                                                                                                     |
| **ADR-0064** §1                                  | Writes into a pane: text into positively identified `free_text`, or keys from the closed set a menu offers.                                     | **Superseded** by a new ADR: any key, conditional on its target (§6.1).                                                                                                                                  |
| **ADR-0064** §2                                  | A coordinator reads only panes it holds; a person's own pane is readable by nobody.                                                             | Kept for screens. Part 5 adds a neighbour **list** within a workspace, which carries no screen.                                                                                                          |
| **ADR-0020**                                     | Authority is granted per run.                                                                                                                   | Kept: which sessions a tool may touch is the grant's answer, never the tool's name (§6.1).                                                                                                               |
| 2026-08-15 design §6, **D11**, **D12**           | One dispatcher, two callers; state is evidence; detection rules are local and user-editable.                                                    | **Restored.** `session.*` serves both callers; worker state is evidence (part 2); rules stay local (no manifest catalogue).                                                                              |
| 2026-08-24 design **D6**, **D9**, §7.2           | Hooks carry nothing required; only exit and declaration decide state; `wait` is a convenience.                                                  | **D6/D9 superseded** (new ADR, part 2). `workers.wait` removed (part 2).                                                                                                                                 |
| 2026-09-05 design **D5**                         | Hooks optional and non-load-bearing; first Claude adapter stages MCP only.                                                                      | **Superseded** (part 6).                                                                                                                                                                                 |
| 2026-09-03 mesh design **M1**, **M2**, **P1–P7** | Talk is mesh, act is star; progress is appended checkpoints that wake nobody.                                                                   | Kept: neighbours may talk (part 5), only a coordinator acts on its workers; checkpoints per P1–P7 (part 3).                                                                                              |
| `AGENTS.md`, "Look for the existing answer"      | One owner per behaviour.                                                                                                                        | `workers.screen` and `session.read` are two readings of one screen; they become one tool (§6.1).                                                                                                         |

## 4. Decisions (owner, 2026-09-11)

1. **Worker state is two-tier.** A hook-declared event is authoritative while the agent's hooks
   report; the screen classifier (`internal/paneobserve`, `internal/agentdriver`) is the fallback;
   process exit is its own event. Screen detection is verified first, hooks come after.
2. **nocx wakes the coordinator without an LLM call** on `blocked`, turn finished and exited, by
   typing a pointer line into its pane — never the worker's content (the principle already in
   `internal/workers/backstop.go` `wakeText`).
3. **At spawn nocx gives the worker a preamble:** who its coordinator is, how to reach it, which
   tools it has.
4. **Checkpoints** come from the worker through a tool, per P1–P7: appended, waking nobody.
5. **`workers.wait` is removed. The `NOCX_AGENT_REPORT` drop is removed;** a worker's final word is
   its last checkpoint and its screen.
6. **Scope.** Over its own workers a coordinator has every right: list, mail, turn-starting
   message, screen, keys, close. Agents enrolled in the same nocx **workspace** are neighbours:
   listed, reachable by quiet mail and by a nocx **pointer** wake only — never typed content,
   because a turn-starting message borrows the recipient's permissions (an agent in auto mode runs
   what it is told). Plain shells are never listed.
7. **A message to an agent has two deliveries:** at its next free prompt, and during its turn
   (Claude Code queues typed input). During a turn the text is pasted, the frame re-read to confirm
   it landed in the input box, and only then Enter is sent.
8. **`workers.close` also closes the tab.** A worker's tab opens to the right of its coordinator's
   (`nocx-tdiqs`). The MCP bridge serves calls concurrently (`nocx-tlaft`).
9. **Any key may be sent,** conditional on its **target**, never on the frame (§2.2).
10. **Part 1's method.** CI runs mocks only. Correctness is verified live, outside CI, by a
    coordinator Claude in a nocx dev-stand pane inside a throwaway folder, through the worker MCP
    tools, following a repository skill so the run repeats after every Claude update. Labels come
    from Claude Code hooks staged for the run and from the script's own causation; herdr is a second
    reader; the owner arbitrates only disagreements. Tokens may be spent: cheap model, nothing
    approved.
11. **Interaction with a pane lives in `session.*`,** one tool per act for both callers, with
    authority from the grant (§6.1).

## 5. Order of work

1. **Screen detection, verified** (this document, §6).
2. **Event-driven worker state and the coordinator wake:** amend AD-6, supersede D6/D9, remove
   `workers.wait` and the drop, `workers.close` closes the tab.
3. **Spawn preamble and checkpoints.**
4. **Messages:** `when=free` and `when=now` delivered as §4.7 (the tool itself lands in part 1).
5. **Workspace neighbours:** list, mail, pointer wake.
6. **Hooks as the authoritative tier.**

`nocx-tlaft` and `nocx-tdiqs` are standalone bugs and are not blocked by any of these.

## 6. Part 1 — screen detection, verified before hooks

### 6.1 Section A: the tools the verification needs (approved)

**What exists.** `agentdriver.Explanation` (`internal/agentdriver/explain.go`) already carries the
rule's reading — branches reached and matched, predicates, anchors, extractor rows — and
`agent.emitting` serves it to the calibration view. `agenttyping.ReadMenu` extracts a menu's
options; `Typist.Submit/Type/Choose` re-read the frame before writing and confirm nothing after it.
`workerSpawner.deliverTask` delivers a task by waiting for `free_text` and calling `Typist.Submit`.
The built-in assistant reaches a pane through `session.list`, `session.read`, `session.run` and
`session.wait`, each bound to `runCtx.Session` with no session parameter; `session.read` takes its
screen from the renderer (`executeSessionScreen`, `RequestScreen`). The coordinator reaches its
workers' panes through `workers.screen` (the backend grid) and `workers.answer`.

**The tools.**

1. **`session.read`** gains an optional `session` (default: the caller's own). For a pane with an
   enrolled agent the result also carries:
   - `reading` — the rule's reading, the same projection `agent.emitting` renders, never a second
     renderer of it;
   - `target` — the identity a key or message may be conditioned on (below).

   Source of rows: the backend grid when the pane is enrolled (it exists without a connected
   client), the renderer otherwise (AD-6: the renderer owns the VT state of an unenrolled pane).
   A row's text is produced the same way on both paths. **`workers.screen` is removed.**

2. **`session.message(session, text, when)`** — `when=free` waits for the free input box and
   submits, the path `deliverTask` already takes; `when=now` pastes during a turn, confirms the echo
   in the input box from frames produced after the paste, and only then sends Enter. No echo, or a
   menu on screen at the Enter, refuses with nothing further written.
3. **`session.keys(session, keys | option, target)`** — any keys, or a menu option named by its
   text as the screen drew it (what `workers.answer` does today). **`workers.answer` is removed.**

**The target.** Computed by nocx from the rule's reading, never supplied by the caller's own idea
of the screen:

| On screen          | Target identity                                                                                                                                                               |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| a menu             | state (`permission_choice` / `modal_choice`), the question, the options, a digest of the menu body rows the rule's extractors bound — spinner, timer and token meter excluded |
| the free input box | `free_text`                                                                                                                                                                   |
| a turn in flight   | `working`, and no menu                                                                                                                                                        |
| anything else      | `error` / `unknown` — a key conditioned on it is refused                                                                                                                      |

**The write.** Immediately before writing, nocx re-classifies the frame and recomputes the target.
Equal: the keys are written. Not equal: nothing is written, and the refusal names the target now
on screen, so the caller can look again. A spinner or a timer never changes a target. A menu with
no body rows bound has no identity and refuses, as in nelix.

**Authority is the grant's.**

| Caller                                | May touch                                                 | Through                                                                                                                                          |
| ------------------------------------- | --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| nocx's built-in assistant             | its own session                                           | its approval policy; `session.keys` and `session.message` declare the same effects as `session.run`, since a keystroke into a shell is a command |
| an external coordinator               | sessions of workers it holds                              | the delegation check `workers.answer` uses today (`ErrNotHeld` / `ErrNotDelegated`)                                                              |
| a neighbour in the workspace (part 5) | none of `session.read`, `session.message`, `session.keys` | —                                                                                                                                                |

`workers.*` keeps what concerns worker records: `spawn`, `holdings`, `close`, mail, and (part 3)
checkpoints.

**Records and contracts.** A new ADR: a key is conditional on its target; it supersedes ADR-0064 §1
and restates power (1) of the AD-6 amendment. `contracts/` gains the result of `session.read` and
the params and results of `session.message` and `session.keys`, each with the DTO and the
over-the-wire conformance tests (`AGENTS.md` rule 5). The pinned product-grant tool list
(`05bb0c11`) moves with the renames.

### 6.2 Section B: the verification procedure

**Where it runs.** A coordinator Claude started in a nocx dev-stand pane whose working directory is
a throwaway folder, `/var/tmp/nocx-detect-<timestamp>`. Its workers open there (`coordinatorCwd`).
Never in CI, never in a real repository, never in the default profile of the installed app.

**The skill.** `.claude/skills/nocx-detection-verify/SKILL.md` with its scripts, modelled on
`herdr-throwaway-repro`:

- **Safety:** throwaway folder only; cheap model (`claude --model haiku`); never approve anything —
  decline with `session.keys`; close only the workers and tabs this run created; never edit the
  person's configuration — hooks reach the worker only through `--settings <throwaway>/hooks.json`.
- **Versions:** record `claude --version`, `herdr --version` and the nocx commit in the report.
- **Labels:** the hooks file writes each event with a timestamp to `<throwaway>/hook-events.jsonl`.
  Where a state has no hook, the label is the script's causation (the keystroke that opens it).

**The states.**

| State                       | Reached by                                              | Label from                  | nocx expected                                                                    | herdr rule                         |
| --------------------------- | ------------------------------------------------------- | --------------------------- | -------------------------------------------------------------------------------- | ---------------------------------- |
| folder trust question       | a fresh throwaway folder                                | script                      | `permission_choice` (as `TestTheFolderTrustQuestionIsAPermissionChoice` asserts) | to be read in the run              |
| idle, 120 / 80 / 60 columns | start, nothing typed                                    | script                      | `free_text`                                                                      | `live_prompt_box`                  |
| working                     | a short prompt                                          | `UserPromptSubmit`          | `working`                                                                        | screen fallback (no title in file) |
| turn finished               | the same prompt completing                              | `Stop`                      | `free_text`                                                                      | `live_prompt_box`                  |
| bash permission             | a prompt that needs a shell command                     | `Notification` (to measure) | `permission_choice`                                                              | `bash_permission_prompt`           |
| write permission            | a prompt that writes a file in the throwaway folder     | `Notification` (to measure) | `permission_choice`                                                              | `generic_permission_prompt`        |
| `/model` menu               | typing `/model`                                         | script                      | `modal_choice`                                                                   | `model_picker_menu` (skip)         |
| transcript viewer           | `ctrl+o`                                                | script                      | to decide                                                                        | `transcript_viewer` (skip)         |
| `/btw` overlay              | `/btw` during a turn                                    | script                      | to decide                                                                        | `btw_overlay_working`              |
| background subagent         | a prompt that starts an Explore agent                   | `PreToolUse`                | `working`                                                                        | —                                  |
| API error, retrying         | `ANTHROPIC_BASE_URL` pointed at a dead port             | script                      | `error`                                                                          | idle fallback                      |
| dynamic workflow prompt     | a prompt that triggers it, if reachable on this version | script                      | `modal_choice`                                                                   | `dynamic_workflow_prompt`          |

"To decide" rows are decided by the owner when the run shows what nocx and herdr read there.

**For every state** the coordinator writes one line to `<throwaway>/report.jsonl`: the rows as
`session.read` returned them, `reading` and `target`, the matching hook events, and
`herdr agent explain --file <rows.txt> --agent claude --json`. States map `free_text↔idle`,
`working↔working`, `permission_choice`/`modal_choice↔blocked`, `unknown↔unknown`; herdr's
`skip_state_update` rules are recorded as such, not as disagreements.

**Three things the run must measure** and write into the report: whether each listed hook fires in
an interactive Claude; whether a Claude menu reacts to pasted text (§4.7); how long the echo of a
`when=now` paste takes to appear and leave.

**Disagreements** — label against nocx, or nocx against herdr — go to the owner. Each one decided
becomes a rule change plus one small mock in `internal/agentdriver` tests (a text frame or a moment
of a capture), never a full-screen suite. The rule is embedded (`go:embed`), so a change means
rebuilding the stand; hot reload stays `nocx-y6w66`.

### 6.3 Acceptance, as assertions

1. `session.read` from the external endpoint on a held worker returns `rows`, `reading` and
   `target`; on a session the caller does not hold it is refused with `ErrNotHeld`, and on a
   neighbour's session too.
2. `session.read` by the built-in assistant on its own unenrolled pane returns the renderer's rows;
   on an enrolled pane it returns the grid's rows with the same text for the same screen.
3. `session.keys` whose target no longer matches writes zero bytes to the pty and names the current
   target; with a matching target on a frame whose spinner moved, the keys are written.
4. `session.keys` with a menu target whose body rows are unbound refuses.
5. `session.message when=now`: with a menu appearing between the paste and the Enter, no Enter is
   written; with the echo observed and no menu, Enter is written once.
6. `session.message when=free` on a working pane writes nothing until `free_text`, then submits.
7. `workers.screen` and `workers.answer` no longer exist in the registry, the dispatcher's worker
   list or the product-grant pin; the schemas and over-the-wire tests exist for the three tools.
8. The skill's run produces `report.jsonl` covering every row of §6.2 with a label, nocx's reading
   and herdr's verdict, and part 1 closes with no disagreement left undecided.
9. No test in CI starts `claude` or `herdr`.

## 7. Out of scope for part 1

Parts 2–6. A keystroke surface in nocx's own UI. Hot reload of rules (`nocx-y6w66`). Any network
catalogue of rules (D12). The built-in assistant's approval page for the new effects beyond
declaring them like `session.run`.

## 8. Open questions

- **Body digest region per menu** — which extractor rows bound a menu body, per menu kind; settled
  in the plan against the corpus.
- **Transcript viewer and `/btw`** — what nocx should call them (§6.2 "to decide").
- **The carrier for hook events** (part 6): the authenticated lifecycle channel, the tool endpoint
  under the enrolled pin, or a drop the shell forwards.
