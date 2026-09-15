---
name: nocx-detection-verify
description: Re-record Claude Code screen moments for internal/agentdriver's manifest after a Claude update, isolated from the person's Claude account and configuration, and check the shipped rule against them.
---

# Re-recording Claude's screen moments

Use after `claude --version` changes, or when a pane's reported state looks wrong.

1. **Endpoint.** An Anthropic-compatible local model (the owner's LM Studio; see bead `nocx-34r0i`
   notes 13). Check it: `curl -s <endpoint>/v1/models` lists the model, and a `/v1/messages` call with one
   tool definition answers `stop_reason: tool_use`. Never use an Anthropic account for recording.
2. **Record** with `.claude/skills/nocx-detection-verify/record.sh <endpoint> <model> <script> <out> [cols] [settings]`.
   The permission captures (Bash and Write dialogs) pass
   `.claude/skills/nocx-detection-verify/permission-ask-settings.json` as `[settings]`, which
   asks (rather than auto-allows or auto-denies) `Bash(touch marker.txt)`, so the recorded
   dialog is a real approval prompt rather than a tool call that ran unattended.
   It builds `agent-capture`, runs `claude` under `-env-file` in a fresh `/var/tmp/nocx-detect-*` directory
   and refuses if Claude would read any configuration outside it — including a `HOME` or
   `CLAUDE_CONFIG_DIR` that is missing or that points at real configuration, and a symlinked run
   directory or ancestor. `run.env` inside that directory sets both. Scripts live in
   `internal/agentdriver/testdata/captures/scripts/lmstudio-*.script`.
   - `claude` must be on `PATH`; the script fails immediately, with a message, if it is not.
   - API refused: endpoint `http://127.0.0.1:9`.
   - API waiting ("Waiting for API response · will retry in"): do NOT use a listener that
     accepts a connection and never answers (`python3 -c 'import socket;s=socket.socket();
     s.bind(("127.0.0.1",18999));s.listen();c=[s.accept() for _ in range(64)]'`) — tried twice
     for nocx-nru89.9, once at `lmstudio-api.script`'s documented 40s final wait and once with
     the wait extended to 190s (~228s of real elapsed time, just under this script's 240s hard
     capture ceiling), and in both runs Claude's TUI never advances past a plain, ever-growing
     spinner. It treats an accepted-but-silent connection as still in flight, not a failure, so
     it never draws this chrome. The corpus's only recorded evidence of it is
     `claude-lmstudio-turn@72000` (manifest.json's `api-waiting` entry), where it appeared
     incidentally during a real slow LM Studio response. Reproducing it deliberately likely
     needs a connection that answers with a mid-stream failure rather than one that never
     answers at all — untried as of nocx-nru89.9.
   - `<out without .jsonl>.meta.json`, beside the capture itself (not in the `/var/tmp` run
     directory, which is deleted along with everything else once the run is done), records what
     actually ran: the resolved `claude` binary and its digest, the run's own environment variable
     names, the names (never values) of any variables the resolved launcher itself sets or
     prefixes — the Nix wrapper does — and the Claude version (`-version-arg --version`, run once
     under the same isolated environment). Read it before trusting a capture, and before
     committing it: it must hold no home path, email address, token or private network address —
     variable names, `/nix/store` paths and `/var/tmp/nocx-detect-*` paths are fine, because only
     names are ever recorded, never values (so `ANTHROPIC_BASE_URL`'s endpoint never lands in it).
     A launcher that could not be parsed (a binary, not a wrapper script) says so instead of
     silently reporting no additions.
3. **Place marks by reading the replay:** `go run ./cmd/agent-capture replay -at <ms,...> <capture>` around each
   script step, until the moment's identifying text (spec §6.4) is on screen. Never place a mark by timing
   alone.
4. **Update `internal/agentdriver/testdata/captures/manifest.json`:** one entry per inventory moment —
   recorded (capture, `atMs`, the owner's state) or `unverified` with the reason. Add the capture name to
   `captureNames` in `capture_test.go` and a row plus the Claude version to the captures `README.md`.
5. **Check:** `go test ./internal/agentdriver/ -run TestTheManifestHolds -count=1`. A disagreement goes to the
   owner, who decides whether the rule, the mark or the label is wrong; a rule change comes with a test that
   is red first.
6. **Never** approve a tool call, work outside the run directory, or commit a capture from a directory that
   was not isolated.

## Task 14 live checks: the session surface against a real Claude

Runnable by the coordinator once epic `nocx-6q1uh` merges — never by a worker (the worker
that wrote this section did not run it; `TestACoordinatorReadsAnswersAndMessagesItsWorker`
in `internal/app/session_surface_happypath_test.go` is the CI-running acceptance check, over
a fake agent, and it does not substitute for this). Two kinds of evidence, recorded in two
different places:

- A **fact about Claude's own screen** (a menu zone row span, an echo's exact bytes) is a
  capture plus a `manifest.json` entry, exactly as steps 1-5 above already describe.
- A **fact about whether nocx's own session.keys/session.message worked against a real
  Claude** is not a capture at all — it is a live coordinator run, the same shape as
  `internal/app/worker_happypath_test.go`'s `TestManualRealClaudeCoordinator` (a real
  `claude --print --strict-mcp-config` process driving the shipped `cmd/nocx-helper mcp`
  bridge against a real `newHappyStand`), and its outcome is a row appended to this design's
  own §15, never `manifest.json`.

1. **Menu zone and input-box displacement.** Already measured and shipped: `Document.
   MenuDisplacesInputBox` (`internal/agentdriver/document.go`), recorded in commit
   `e8d0dd84` (`nocx-6q1uh.10`'s own message names the nine corpus pairs it was measured
   against). Nothing to re-run here unless a Claude update changes the finding — if it does,
   re-derive it the way that commit did (replay every permission/modal pair against the
   nearest preceding free_text/working moment with `agentdriver.Registry.Observe`) and file
   a bead before touching `claude.rule.json`'s `menuDisplacesInputBox` field.

2. **The echo form of a pasted multi-line message.** Record with
   `internal/agentdriver/testdata/captures/scripts/session-message-paste-turn.script`
   (new, this task) — it submits a short task, then a bracketed, multi-line paste
   (`\x1b[200~...\n...\x1b[201~`) a few seconds into the turn. Place a mark on the input
   box's own repaint (step 3 above) and read what it actually shows: nocx's own
   `boxContainsEcho` (`internal/app/pane_messages.go`) accepts either the verbatim text or
   Claude's own `[Pasted text #N +M lines]` placeholder, matched loosely on `"+M lines]"` —
   confirm which one a real Claude draws for a 3-line paste (`M` should read `2`), and if it
   is neither, that is a defect in `boxContainsEcho`'s own assumption, filed as a bead before
   changing it. **The script's own paste encoding is unverified as of this writing** — check
   by reading the replay (step 3) that the pane actually shows one pasted block and not three
   typed lines; if `cmd/agent-capture`'s script parser does not forward `\x1b[200~`/`\x1b[201~`
   as a literal byte sequence, adjust the script and note what worked here.

3. **An option answered through session.keys.** Not a capture — a live coordinator run.
   Stand up `newHappyStand` (or the shipped app) with a real `cmd/nocx-helper mcp` bridge per
   `TestManualRealClaudeCoordinator`'s own `--mcp-config`, spawn a real `claude` worker, wait
   for a real permission menu, call `session.read {target:"menu"}` then `session.keys
   {option:<the drawn option text>}`, and confirm the menu closes and the pane proceeds.
   `internal/agentdriver/testdata/captures/scripts/permission.script` names the same trigger
   command (`touch marker.txt`) this run should provoke, so a disagreement between the two is
   itself worth recording.

4. **Messages during and after a turn.** Live coordinator run, same stand: `session.message
   {when:"now"}` under a `target:"input"` (or `"working"`) minted while the worker is
   mid-turn, and `session.message {when:"free"}` once it is back at its prompt — both against
   the SAME worker, in that order, confirming each reaches `phase:"submitted"` via a
   follow-up `session.read`'s `pendingMessages`. `session-message-paste-turn.script` above
   records the SCREEN half of the "during a turn" case for the manifest; this step is the
   TOOL half, and it is what actually exercises `internal/app/pane_messages.go`'s production
   code against a real pane rather than this task's own fake helper.

5. **Spec §8.2's one open measurement: does a Claude menu react to pasted text?** Record with
   `internal/agentdriver/testdata/captures/scripts/session-message-paste-menu.script` (new,
   this task): it provokes a permission menu, then attempts the same bracketed multi-line
   paste WHILE the menu is still showing and unconfirmed, waits several seconds, and only
   then confirms. Read the replay (step 3) across that wait: does the menu's own selection,
   its option text, or its row span change; does the pasted text appear anywhere (the menu
   zone, the box underneath it, nowhere); does the menu simply not react at all. **Write the
   answer into this design's own `.internal/specs/2026-09-14-the-session-surface-design.md`,
   §15, as a new "Live measurements" entry** (a row: what was tried, the capture name and
   mark, what was observed, and what it settles about `PaneMessages.pasteReady`'s current
   precondition that the box be identifiably empty before a paste — design §8.2 step 1).
   This is the one item spec §13's "Live, outside CI" line names and this epic's plan (Task
   14) leaves for the coordinator; do not guess the answer from `claude.rule.json` alone —
   the whole point of this step is that nothing in the rule says what happens, only what the
   screen looks like once it has.
