# What nocx lets a coordinator do, measured against herdr — design

- **Status:** Draft, fourth revision, 2026-09-11. §4 holds the owner's decisions of that day. §6
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

1. **1a — classification, verified:** `nocx-nru89` (§6), with `nocx-ys9jd` and `nocx-emors`.
2. **1b — pane interaction tools:** `nocx-6q1uh` (§7 is its brief); blocked by `nocx-nru89` (the rule
   and menu extraction) and `nocx-tlaft` (concurrency).
3. **Event-driven worker state and the coordinator wake:** `nocx-luqz9`, holding `nocx-9f1d4`;
   blocked by `nocx-6q1uh` (`internal/app/workers.go`).
4. **Spawn preamble and checkpoints:** `nocx-k2csf`, blocked by `nocx-luqz9`.
5. **Workspace neighbours:** `nocx-i8umd`, blocked by `nocx-luqz9`.
6. **Hooks as the authoritative tier:** `nocx-7faow`, blocked by `nocx-luqz9` and `nocx-nru89`; it
   also holds the automatic label oracle the reviews shaped (§9).

Standalone bugs: `nocx-tdiqs` (tab position), `nocx-tlaft` (MCP bridge and endpoint serialise calls),
`nocx-8a38l` and `nocx-fqpbw` (capture tool). Blocking edges follow shared files, as `AGENTS.md`
requires; the order above is also the owner's.

## 6. Part 1a — the classification, verified (epic `nocx-nru89`)

### 6.1 What changed after three reviews

The previous revisions built an automatic label oracle: hook events on the capture's clock, online
expect steps before dependent input, a fault proxy with request-level faults, a capture completion
record, mutually exclusive entry predicates per screen. Each review found real holes in it, and the
third found that its capture-format change would have silently removed a typing refusal
(`agentcalib` writes headers without `Started` and reads a failed load as no evidence against the
rule, `internal/agentcalib/verify.go` `evaluate`). The owner fixed the labels by looking at the
screens, which is also how herdr keeps its rules correct. So 1a verifies the rule against moments the
owner labelled, and the oracle moves to the hooks epic (`nocx-7faow`), where hooks exist anyway.

### 6.2 What is verified

The shipped rule (`internal/agentdriver/claude.rule.json`) gives the owner's state at every moment in
`internal/agentdriver/testdata/captures/manifest.json`, through `agentcapture.Read`,
`agentcapture.Frames` and `agentdriver.Registry.Explain` — the path the product uses — for moments
recorded on the Claude Code version current when the epic closes. Not verified: writes into a pane
(1b), worker state (part 2), hook authority (hooks epic).

### 6.3 Tasks

1. **Isolation — `nocx-nru89.1`.** `agent-capture` gains `-env-file`, which replaces inheritance: the
   program gets exactly the file's variables. Before starting, it refuses when Claude would read
   settings or instructions from outside the run: `managed-settings.json` or `managed-settings.d/`
   (Linux `/etc/claude-code`, the macOS equivalents), or `CLAUDE.md`, `CLAUDE.local.md` or `.claude/`
   in the working directory or any ancestor. The launcher's own environment additions (the Nix
   `claude` wrapper sets several) are recorded, not hidden.
2. **Corpus, manifest and skill — `nocx-nru89.2`.** Record the moments below on the current Claude
   through the owner's Anthropic-compatible local endpoint with a fresh `HOME` and
   `CLAUDE_CONFIG_DIR` under `/var/tmp`; write the manifest (capture, mark, expected state, optional
   branch); commit captures and scripts; add `.claude/skills/nocx-detection-verify/` so the recording
   is redone after a Claude update. Permission moments use an explicit `permissions.ask` rule in a
   `--settings` file, because read-only commands such as `ls` are allowed without asking.
3. **One replay path — `nocx-nru89.3`.** The rule tests read the manifest through `agentcapture`;
   the test-only reader in `capture_test.go` goes; state and branch are compared separately.
   `agentcapture.Read`'s contract does not change.
4. **Rule fixes — `nocx-ys9jd`, `nocx-emors`** and whatever the new moments show, each with a test that
   fails on the old rule and passes on the new.

Two defects of the capture tool the reviews verified are filed on their own, because 1a does not need
them to be fixed first: `nocx-8a38l` (a descendant holding the PTY hangs the capture, which then
writes nothing) and `nocx-fqpbw` (a new capture with its tail cut replays as whole).

### 6.4 The moments and their labels

Owner's labels, 2026-09-11. The literal text is what identifies the moment when marks are placed by
reading the replay; it is not a second classifier.

| Moment                                 | Identified by                                  | Expected                                 |
| -------------------------------------- | ---------------------------------------------- | ---------------------------------------- |
| theme picker (first run)               | `Choose the text style`                        | `modal_choice`                           |
| security notes (first run)             | `Press Enter to continue`                      | `unknown`                                |
| folder trust                           | `Yes, I trust this folder`                     | `permission_choice`                      |
| idle at 120, 80, 60 columns            | `manual mode on`, prompt box, no spinner       | `free_text`                              |
| turn before its elapsed timer          | spinner row without `(Ns)`, `esc to interrupt` | `working` (`nocx-ys9jd`)                 |
| turn with its timer                    | spinner row with `(Ns)`                        | `working`                                |
| turn finished                          | prompt box, no spinner, no `esc to interrupt`  | `free_text`                              |
| `/btw` overlay over a running turn     | `Esc to close` under the main turn's spinner   | `working`                                |
| transcript viewer                      | `Showing detailed transcript`                  | `unknown`                                |
| `/model` menu                          | the menu as recorded                           | `modal_choice`                           |
| Bash permission                        | `Do you want to proceed?`                      | `permission_choice`                      |
| Write permission                       | the file-specific question                     | `permission_choice`                      |
| background subagent running, and after | the task panel row; `/tasks to see subagents`  | `working`; then `free_text` when it ends |
| API refused, retrying                  | `Retrying in`                                  | `error`                                  |
| API waiting for a response             | `Waiting for API response`                     | `error` (`nocx-emors`)                   |

A moment that cannot be reached on the current Claude or model is named in the manifest with the
reason and stays unverified; the epic does not claim it.

### 6.5 Acceptance, as assertions

1. **Isolation (mocks).** A fake program printing its environment under `-env-file` shows only the
   file's variables and none of the caller's `CLAUDE*`; each refusal source starts no process, one
   test per source; with none present the capture starts.
2. **Manifest (CI).** Every manifest moment classifies to its expected state, and to its expected branch
   where one is named; changing one expected state makes the test fail; every assertion the corpus
   tests carried before survives as a manifest entry; calibration tests are unchanged and green.
3. **Rule fixes.** `nocx-ys9jd` and `nocx-emors` each close with a moment that was red on the rule
   before the fix and is green after it, and the idle and finished moments stay `free_text`.
4. **Currency.** The manifest's moments for every row of §6.4 were recorded on the Claude Code version
   current when the epic closes, and the skill reproduces one of them from an empty run directory.
5. **CI on mocks only.** No CI test starts `claude` or reaches a model endpoint.

### 6.6 What the discovery run established (2026-09-11)

Claude Code 2.1.266, `qwen/qwen3.6-35b-a3b` on LM Studio, `agent-capture` under `env -i` with a fresh
`HOME` and `CLAUDE_CONFIG_DIR` in `/var/tmp`, 120×40:

- **Startup:** an unrecognized-model notice on the primary screen; the theme picker; security notes;
  the trust question with the cursor on `No, exit` (Enter there exits with status 1); the idle
  alternate screen with the model in the header and `manual mode on`. No login screen, no API-key
  prompt with `ANTHROPIC_AUTH_TOKEN`.
- **The rule's readings:** theme picker `modal_choice`; security notes `unknown`; trust
  `permission_choice`; idle `free_text`; transcript viewer `unknown`; `/btw` overlay `unknown`
  (owner: `working`); a turn before its timer `free_text` (`nocx-ys9jd`); a turn with its timer
  `working`; `Waiting for API response · will retry in …` `free_text` (`nocx-emors`).

## 7. Part 1b — brief for its own design (epic `nocx-6q1uh`)

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

### Third review (codex, 2026-09-11, on 40bbae26)

Eleven findings, all checked against the tree; the ones about the harness were verified and led the
owner to simplify part 1a (§6.1) rather than keep extending an automatic label oracle.

| #   | Finding                                                                               | Disposition                                                                  |
| --- | ------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| 1   | Requiring an `end` record in `Read` breaks calibration and removes a refusal          | Verified; `Read`'s contract kept (§6.3.3); completion record in `nocx-fqpbw` |
| 2   | Isolation misses `managed-settings.d`, managed and local `CLAUDE` files, launcher env | Verified; `nocx-nru89.1`                                                     |
| 3   | Entry evidence lets the API-wait screen label as `working`                            | Oracle removed; labels are the owner's per moment (§6.4)                     |
| 4   | Universal prerequisites make startup and `/btw` scenarios impossible                  | Oracle removed (§6.1)                                                        |
| 5   | The 100 ms window rejects animated states                                             | Oracle removed (§6.1)                                                        |
| 6   | Evidence before input needs an online expect driver                                   | Deferred to the hooks epic `nocx-7faow`                                      |
| 7   | The hook socket needs a sender; Notification has no `tool_name`                       | Deferred to `nocx-7faow`                                                     |
| 8   | The fault proxy lacks request-level fault semantics                                   | Deferred to `nocx-7faow`; both API chromes recorded as moments (§6.4)        |
| 9   | Capture lifecycle contradicts the trust-decline exit                                  | `nocx-8a38l` (expect-exit step)                                              |
| 10  | `/model` and background subagent rows had placeholders                                | Recorded as moments in `nocx-nru89.2`; unreachable ones named, not claimed   |
| 11  | Failing-call matrix and success pairs still incomplete                                | §6.5 for what 1a now builds; the oracle's matrix goes with `nocx-7faow`      |
